// 私有邀请链接监听申请（watch_invite_requests）的状态机与后台对账。
//
// Telegram 平台边界：邀请链接（t.me/+hash、t.me/joinchat/hash）只能让
// MTProto 读取账号加入目标频道/超级群组，Bot 无法经邀请入群。因此申请
// 生命周期能推进到哪一步取决于两个独立条件：
//
//	pending           → 等待系统管理员审批（普通用户）
//	waiting_telegram  → 系统已批准：读取账号加入中（频道侧审核或读取账号离线）
//	waiting_bot       → 读取账号已加入：等待人工把 Bot 设为管理员
//	approved          → 已创建正式 watch_sources，监听生效（hash 已清理）
//	rejected / failed → 终态（rejected 清理 hash；failed 保留以便重试）
//
// 敏感约定：完整 invite_hash 只在本文件内使用，禁止进入日志、审计、
// API 响应与 Bot 文案；频道定位成功后即清理（后续仅需 Bot 校验）。
package watch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// reconcileInterval 是邀请申请后台对账周期：waiting_telegram 由读取账号
// 成员状态或频道侧审批推进，waiting_bot 由 Bot 管理员状态推进，均无需
// 人工触发；管理员也可在管理端手动重试立即推进。
const reconcileInterval = 5 * time.Minute

// RunReconcile 启动邀请申请周期对账（阻塞直至 ctx 取消；装配层以独立
// goroutine 运行，生命周期与 MTProto ready 会话一致）。
func (s *Service) RunReconcile(ctx context.Context) {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.ReconcileInviteRequests(ctx); err != nil {
				s.log.Warn("监听邀请申请周期对账失败（下轮重试）", "error", err.Error())
			}
		}
	}
}

// ReconcileInviteRequests 推进全部活动申请一轮：waiting_telegram 重试加入
// 或确认成员状态，waiting_bot 重验 Bot 管理员。单条失败只记日志不中断
// 其余申请；对账互斥，避免与手动重试并发推进同一条申请。
func (s *Service) ReconcileInviteRequests(ctx context.Context) error {
	if s.member == nil {
		return nil
	}
	if !s.reconMu.TryLock() {
		return nil
	}
	defer s.reconMu.Unlock()
	reqs, err := s.st.ListActiveWatchInviteRequests(ctx)
	if err != nil {
		return err
	}
	for _, req := range reqs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, _, err := s.advanceInvite(ctx, req, ""); err != nil {
			s.log.Warn("监听邀请申请推进失败（下轮重试）",
				"request_id", req.ID, "status", req.Status, "error", err.Error())
		}
	}
	return nil
}

// submitInvite 处理 /watch 的私有邀请链接申请：查重 → 预检（不加入）→
// 上限 → 落库（按审批配置分流）→ 免审批时立即推进。owner 免审批且不受
// 上限约束，与普通源语义一致。
func (s *Service) submitInvite(ctx context.Context, userID int64, isOwner bool, hash string, botID int64, botUsername string, cfg syscfg.WatchConfig) (SubmitOutcome, error) {
	// 查重：同 hash 活动申请，本人幂等回当前状态，他人视为已占用
	existing, err := s.st.FindActiveWatchInviteRequestByHash(ctx, hash)
	switch {
	case err == nil:
		title := inviteDisplayTitle(existing)
		if existing.UserID != userID {
			return SubmitOutcome{Kind: SubmitAlreadyOthers, Title: title}, nil
		}
		return SubmitOutcome{Kind: inviteStatusOutcome(existing.Status), Title: title}, nil
	case !errors.Is(err, store.ErrNotFound):
		return SubmitOutcome{}, err
	}

	// 预检：只确认邀请有效且指向频道/超级群组，不产生入群副作用
	info, cerr := s.checkInvite(ctx, hash)
	if cerr != nil {
		if errors.Is(cerr, mtproto.ErrMembershipUnavailable) {
			return SubmitOutcome{Kind: SubmitReaderUnavailable}, nil
		}
		return SubmitOutcome{Kind: SubmitInviteInvalid}, nil
	}
	if !info.IsChannel {
		return SubmitOutcome{Kind: SubmitInviteInvalid}, nil
	}
	// 读取账号已加入且该频道已是监听源：直接回现状（激活后 hash 已清理，
	// hash 查重不可见；此时预检恰好带回频道 ID，按源幂等，不再重复加入）
	if outcome := s.existingSourceOutcome(ctx, info, userID); outcome != nil {
		return *outcome, nil
	}

	// 上限：活动邀请申请与监听源合并计算（非 owner）
	if !isOwner {
		if outcome, err := s.checkInviteLimits(ctx, userID, cfg); err != nil {
			return SubmitOutcome{}, err
		} else if outcome != nil {
			return *outcome, nil
		}
	}

	req, err := s.st.CreateWatchInviteRequest(ctx, store.WatchInviteRequest{
		UserID:       userID,
		InviteHash:   hash,
		Title:        info.Title,
		Participants: info.Participants,
		Enabled:      true,
		BotID:        botID,
		BotUsername:  botUsername,
	})
	if err != nil {
		return SubmitOutcome{}, err
	}
	if !isOwner && cfg.RequireApproval {
		s.log.Info("监听邀请申请已提交", "request_id", req.ID, "user_id", userID)
		return SubmitOutcome{Kind: SubmitInvitePending, Title: inviteDisplayTitle(req)}, nil
	}
	req, src, err := s.advanceInvite(ctx, req, "")
	if err != nil {
		return SubmitOutcome{}, err
	}
	return SubmitOutcome{Kind: inviteStatusOutcome(req.Status), Title: inviteOutcomeTitle(req, src)}, nil
}

// checkInviteLimits 合并计算每用户与总量上限；命中时返回对应 outcome。
func (s *Service) checkInviteLimits(ctx context.Context, userID int64, cfg syscfg.WatchConfig) (*SubmitOutcome, error) {
	if cfg.PerUserLimit > 0 {
		sources, err := s.st.CountWatchSourcesByUser(ctx, userID,
			[]string{store.WatchPending, store.WatchApproved})
		if err != nil {
			return nil, err
		}
		invites, err := s.st.CountActiveWatchInviteRequestsByUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		if n := sources + invites; n >= cfg.PerUserLimit {
			return &SubmitOutcome{Kind: SubmitUserLimit, Count: n, Limit: cfg.PerUserLimit}, nil
		}
	}
	if cfg.MaxSources > 0 {
		sources, err := s.st.CountWatchSources(ctx,
			[]string{store.WatchPending, store.WatchApproved})
		if err != nil {
			return nil, err
		}
		invites, err := s.st.CountActiveWatchInviteRequests(ctx)
		if err != nil {
			return nil, err
		}
		if n := sources + invites; n >= cfg.MaxSources {
			return &SubmitOutcome{Kind: SubmitSourceLimit, Count: n, Limit: cfg.MaxSources}, nil
		}
	}
	return nil, nil
}

// adminAddInvite 管理员经邀请链接添加：同 hash 活动申请幂等返回，否则
// 预检后立即创建并推进（不走审批）。读取账号不可用作为受控错误返回
// （Web 层映射 503），邀请无效映射 400。
func (s *Service) adminAddInvite(ctx context.Context, actor, hash string, enabled bool) (AdminAddResult, error) {
	existing, err := s.st.FindActiveWatchInviteRequestByHash(ctx, hash)
	switch {
	case err == nil:
		return AdminAddResult{InviteRequest: &existing}, nil
	case !errors.Is(err, store.ErrNotFound):
		return AdminAddResult{}, err
	}

	info, err := s.checkInvite(ctx, hash)
	if errors.Is(err, mtproto.ErrMembershipUnavailable) {
		return AdminAddResult{}, err
	}
	if err != nil {
		return AdminAddResult{}, apperr.New(apperr.CodeInvalidInviteURL, inviteErrNote(err))
	}
	if !info.IsChannel {
		return AdminAddResult{}, apperr.New(apperr.CodeInvalidInviteURL,
			"该邀请链接指向普通群组，仅支持频道/超级群组。")
	}
	// 读取账号已加入且该频道已是监听源：幂等返回源，不重复加入
	if src := s.existingSource(ctx, info); src != nil {
		return AdminAddResult{Source: src}, nil
	}

	req, err := s.st.CreateWatchInviteRequest(ctx, store.WatchInviteRequest{
		InviteHash:   hash,
		Title:        info.Title,
		Participants: info.Participants,
		Enabled:      enabled,
	})
	if err != nil {
		return AdminAddResult{}, err
	}
	req, src, err := s.advanceInvite(ctx, req, actor)
	if err != nil {
		return AdminAddResult{}, err
	}
	s.log.Info("监听邀请已由管理员添加", "request_id", req.ID, "actor", actor,
		"status", req.Status)
	return AdminAddResult{InviteRequest: &req, Source: src}, nil
}

// existingSource 预检显示读取账号已加入时，按频道定位查找既有监听源。
// 不存在返回 nil（继续正常申请流程）。
func (s *Service) existingSource(ctx context.Context, info mtproto.InviteInfo) *store.WatchSource {
	if !info.AlreadyJoined || info.ChannelID == 0 {
		return nil
	}
	src, err := s.st.GetWatchSource(ctx, binding.BotChannelID(info.ChannelID))
	if err != nil {
		return nil
	}
	return &src
}

// existingSourceOutcome 是用户提交路径的幂等回显：他人已添加拒绝，本人
// 直接回生效结果。
func (s *Service) existingSourceOutcome(ctx context.Context, info mtproto.InviteInfo, userID int64) *SubmitOutcome {
	src := s.existingSource(ctx, info)
	if src == nil {
		return nil
	}
	if src.AddedBy != userID {
		return &SubmitOutcome{Kind: SubmitAlreadyOthers, Title: src.Title}
	}
	return &SubmitOutcome{Kind: SubmitActive, Title: src.Title, Channel: *src}
}

// checkInvite 包装预检调用：Membership 未注入或读取账号离线分别归一为
// 受控错误（离线原样返回 ErrMembershipUnavailable 供上层分流）。
func (s *Service) checkInvite(ctx context.Context, hash string) (mtproto.InviteInfo, error) {
	if s.member == nil {
		return mtproto.InviteInfo{}, mtproto.ErrMembershipUnavailable
	}
	info, err := s.member.CheckInvite(ctx, hash)
	if err != nil {
		return mtproto.InviteInfo{}, err
	}
	return info, nil
}

// ApproveInviteRequest 管理员同意待审批邀请申请：记录审批人后立即推进
// 状态机（读取账号加入 → Bot 校验 → 激活或转入等待态）。返回最新申请与
// 激活产出的源（未激活时为 nil）。
func (s *Service) ApproveInviteRequest(ctx context.Context, actor string, id int64) (store.WatchInviteRequest, *store.WatchSource, error) {
	if s.member == nil {
		return store.WatchInviteRequest{}, nil, mtproto.ErrMembershipUnavailable
	}
	req, err := s.st.GetWatchInviteRequest(ctx, id)
	if err != nil {
		return store.WatchInviteRequest{}, nil, err
	}
	if req.Status != store.WatchInvitePending {
		return store.WatchInviteRequest{}, nil, apperr.New(apperr.CodeStoreConstraint,
			"该邀请申请已被处理或不存在")
	}
	req, err = s.st.SetWatchInviteRequestReview(ctx, id, actor, 0)
	if err != nil {
		return store.WatchInviteRequest{}, nil, err
	}
	req, src, err := s.advanceInvite(ctx, req, actor)
	if err != nil {
		return req, src, err
	}
	s.log.Info("监听邀请申请已通过", "request_id", id, "actor", actor, "status", req.Status)
	return req, src, nil
}

// RejectInviteRequest 管理员拒绝邀请申请（pending/waiting 态均可）：
// 清理 hash 并通知申请人。
func (s *Service) RejectInviteRequest(ctx context.Context, actor string, id int64) (store.WatchInviteRequest, error) {
	req, err := s.st.GetWatchInviteRequest(ctx, id)
	if err != nil {
		return store.WatchInviteRequest{}, err
	}
	switch req.Status {
	case store.WatchInvitePending, store.WatchInviteWaitingTelegram, store.WatchInviteWaitingBot:
	default:
		return store.WatchInviteRequest{}, apperr.New(apperr.CodeStoreConstraint,
			"该邀请申请已结束，无法拒绝")
	}
	req, err = s.st.SetWatchInviteRequestReview(ctx, id, actor, 0)
	if err != nil {
		return store.WatchInviteRequest{}, err
	}
	req, err = s.st.UpdateWatchInviteRequestStatus(ctx, id, store.WatchInviteRejected, "管理员已拒绝该申请。", 0)
	if err != nil {
		return store.WatchInviteRequest{}, err
	}
	if _, cerr := s.st.ClearWatchInviteRequestHash(ctx, id, 0); cerr != nil {
		s.log.Warn("清理已拒绝邀请申请 hash 失败", "request_id", id, "error", cerr.Error())
	} else if fresh, gerr := s.st.GetWatchInviteRequest(ctx, id); gerr == nil {
		req = fresh
	}
	if req.UserID > 0 {
		s.notifyUser(ctx, req.UserID, fmt.Sprintf(
			"你申请的监听邀请「%s」未通过审核。", inviteDisplayTitle(req)))
	}
	s.log.Info("监听邀请申请已拒绝", "request_id", id, "actor", actor)
	return req, nil
}

// RetryInviteRequest 手动重试等待/失败中的申请：waiting 态按原路径推进，
// failed 态（hash 保留）重试加入。pending 需先审批，已激活无需重试。
func (s *Service) RetryInviteRequest(ctx context.Context, id int64) (store.WatchInviteRequest, *store.WatchSource, error) {
	if s.member == nil {
		return store.WatchInviteRequest{}, nil, mtproto.ErrMembershipUnavailable
	}
	req, err := s.st.GetWatchInviteRequest(ctx, id)
	if err != nil {
		return store.WatchInviteRequest{}, nil, err
	}
	switch req.Status {
	case store.WatchInviteWaitingTelegram, store.WatchInviteWaitingBot, store.WatchInviteFailed:
	default:
		return store.WatchInviteRequest{}, nil, apperr.New(apperr.CodeStoreConstraint,
			"该邀请申请当前状态无需重试")
	}
	return s.advanceInvite(ctx, req, "")
}

// DeleteInviteRequest 管理端硬删除任意状态的邀请申请记录。
func (s *Service) DeleteInviteRequest(ctx context.Context, id int64) error {
	return s.st.DeleteWatchInviteRequest(ctx, id)
}

// ListInviteRequests 返回全部邀请申请（Web 管理端列表：pending 优先，
// 其余按申请时间倒序）。
func (s *Service) ListInviteRequests(ctx context.Context) ([]store.WatchInviteRequest, error) {
	return s.st.ListWatchInviteRequests(ctx)
}

// ListInvitesByUser 返回该用户的全部邀请申请（Bot /watch 列表用）。
func (s *Service) ListInvitesByUser(ctx context.Context, userID int64) ([]store.WatchInviteRequest, error) {
	return s.st.ListWatchInviteRequestsByUser(ctx, userID)
}

// advanceInvite 邀请申请推进一步：先解析频道（读取账号加入），成功后
// 校验 Bot 管理员并激活。返回最新申请与激活产出的源（未激活为 nil）。
// actor 非空表示由管理员操作触发（用于通知与审计归属），空为后台对账。
func (s *Service) advanceInvite(ctx context.Context, req store.WatchInviteRequest, actor string) (store.WatchInviteRequest, *store.WatchSource, error) {
	if req.ChannelID == 0 {
		next, err := s.resolveInviteChannel(ctx, req, actor)
		if err != nil || next.ChannelID == 0 {
			return next, nil, err
		}
		req = next
	}
	return s.activateInvite(ctx, req, actor)
}

// resolveInviteChannel 阶段一：让读取账号加入目标并落频道定位。
// 请求制邀请转 waiting_telegram；读取账号离线留在可重试状态；永久失败
// 转 failed（保留 hash 供重试）。成功后清理 hash（后续仅需 Bot 校验）。
func (s *Service) resolveInviteChannel(ctx context.Context, req store.WatchInviteRequest, actor string) (store.WatchInviteRequest, error) {
	hash := req.InviteHash
	if hash == "" {
		// 防御：无频道定位也无 hash，无法继续（正常流转不会出现）
		return s.failInvite(ctx, req, "邀请信息缺失，无法继续处理。")
	}
	channelID, title, already, err := s.member.JoinInvite(ctx, hash, mtproto.JoinOptions{})
	switch {
	case errors.Is(err, mtproto.ErrJoinRequestSent):
		updated, uerr := s.st.UpdateWatchInviteRequestStatus(ctx, req.ID,
			store.WatchInviteWaitingTelegram, "已向频道管理员发送加入请求，等待频道侧批准。", 0)
		if uerr != nil {
			return req, uerr
		}
		if req.Status == store.WatchInvitePending && req.UserID > 0 {
			s.notifyUser(ctx, req.UserID, fmt.Sprintf(
				"你申请的监听邀请「%s」已通过：该频道开启了加入审核，系统已向频道管理员发送加入请求，批准后自动继续。",
				inviteDisplayTitle(req)))
		}
		return updated, nil
	case errors.Is(err, mtproto.ErrMembershipUnavailable):
		// 读取账号离线：留待周期对账自动重试；pending 视为已受理转 waiting
		if req.Status == store.WatchInvitePending {
			updated, uerr := s.st.UpdateWatchInviteRequestStatus(ctx, req.ID,
				store.WatchInviteWaitingTelegram, "系统读取账号暂时不可用，稍后自动重试。", 0)
			if uerr != nil {
				return req, uerr
			}
			return updated, nil
		}
		return req, nil
	case err != nil:
		return s.failInvite(ctx, req, inviteErrNote(err))
	}

	title = firstNonEmpty(title, req.Title)
	botAPIID := binding.BotChannelID(channelID)
	updated, uerr := s.st.UpdateWatchInviteRequestChannel(ctx, req.ID, botAPIID, "", "", title, 0)
	if uerr != nil {
		return req, uerr
	}
	if !already {
		// 加入留痕：标明来源为监听源流程，避免被"自动退出外部拉入"误判；
		// 已是成员时不写（无法区分此前加入渠道，与 joinmgr 同语义）
		if rerr := s.st.UpsertJoinedChannel(ctx, store.JoinedChannelRecord{
			ChannelID: channelID, Title: title, Kind: "channel",
			JoinedVia: store.JoinedViaWatchSource, JoinedBy: req.UserID,
		}); rerr != nil {
			s.log.Error("写入监听邀请加入留痕失败（不影响流程）",
				"channel_id", channelID, "error", rerr.Error())
		}
	}
	if _, cerr := s.st.ClearWatchInviteRequestHash(ctx, req.ID, 0); cerr != nil {
		s.log.Warn("清理监听邀请 hash 失败（频道已定位，不影响流程）",
			"request_id", req.ID, "error", cerr.Error())
	}
	updated.InviteHash = ""
	return updated, nil
}

// activateInvite 阶段二：校验 Bot 管理员身份并激活监听源。
// 未通过转 waiting_bot（等人工配置后自动/手动重试）；通过则创建正式
// watch_sources 并通知申请人。频道级冲突检查在此执行（此时才有真实 ID）。
func (s *Service) activateInvite(ctx context.Context, req store.WatchInviteRequest, actor string) (store.WatchInviteRequest, *store.WatchSource, error) {
	chat, err := s.verifyBotAdmin(ctx, req.ChannelID)
	if err != nil {
		if apperr.From(err).Code == apperr.CodeChannelNotPostable {
			updated, uerr := s.st.UpdateWatchInviteRequestStatus(ctx, req.ID,
				store.WatchInviteWaitingBot,
				"请先在 Telegram 中把监听 Bot 设为该频道/群的管理员，完成后自动生效。", 0)
			if uerr != nil {
				return req, nil, uerr
			}
			if req.Status == store.WatchInvitePending && req.UserID > 0 {
				s.notifyUser(ctx, req.UserID, fmt.Sprintf(
					"你申请的监听邀请「%s」已通过：系统读取账号已加入。请把监听 Bot 加为该频道/群的管理员，配置完成后自动开始监听。",
					inviteDisplayTitle(req)))
			}
			return updated, nil, nil
		}
		return req, nil, err
	}

	// 频道级冲突：普通用户申请时他人（含管理员）已有行则失败；管理员
	// 路径（user_id=0）与 AdminAdd 直接添加同语义——覆盖既有行
	if existing, gerr := s.st.GetWatchSource(ctx, req.ChannelID); gerr == nil {
		if req.UserID > 0 && existing.AddedBy != req.UserID {
			updated, ferr := s.failInvite(ctx, req, "该频道已由其他人添加监听。")
			return updated, nil, ferr
		}
	} else if !errors.Is(gerr, store.ErrNotFound) {
		return req, nil, gerr
	}

	row, uerr := s.st.UpsertWatchSource(ctx, store.WatchSource{
		ChannelID:   req.ChannelID,
		Kind:        string(chat.Type),
		Username:    chat.Username,
		Title:       chat.Title,
		Status:      store.WatchApproved,
		Enabled:     req.Enabled,
		AddedBy:     req.UserID,
		BotID:       req.BotID,
		BotUsername: req.BotUsername,
		ReviewedBy:  firstNonEmpty(req.ReviewedBy, actor),
	})
	if uerr != nil {
		return req, nil, uerr
	}
	// 申请行补全频道快照（kind/username 在 GetChat 后才可得）
	if _, uerr := s.st.UpdateWatchInviteRequestChannel(ctx, req.ID,
		req.ChannelID, string(chat.Type), chat.Username, chat.Title, 0); uerr != nil {
		s.log.Warn("监听邀请频道快照更新失败（不影响激活）",
			"request_id", req.ID, "error", uerr.Error())
	}
	updated, uerr := s.st.UpdateWatchInviteRequestStatus(ctx, req.ID,
		store.WatchInviteApproved, "", 0)
	if uerr != nil {
		return req, &row, uerr
	}
	if req.UserID > 0 {
		s.notifyUser(ctx, req.UserID, fmt.Sprintf(
			"你申请的监听邀请「%s」已完成配置，开始监听：源内新消息会自动预热缓存频道。", chat.Title))
	}
	s.log.Info("监听邀请申请已激活", "request_id", req.ID,
		"channel_id", req.ChannelID, "actor", actor)
	return updated, &row, nil
}

// failInvite 置申请为 failed（保留 hash 供重试）并尽力通知申请人。
func (s *Service) failInvite(ctx context.Context, req store.WatchInviteRequest, note string) (store.WatchInviteRequest, error) {
	updated, uerr := s.st.UpdateWatchInviteRequestStatus(ctx, req.ID, store.WatchInviteFailed, note, 0)
	if uerr != nil {
		return req, uerr
	}
	if req.Status == store.WatchInvitePending && req.UserID > 0 {
		s.notifyUser(ctx, req.UserID, fmt.Sprintf(
			"你申请的监听邀请「%s」处理失败：%s", inviteDisplayTitle(req), note))
	}
	return updated, nil
}

// inviteStatusOutcome 把申请状态映射为提交结果枚举（重复提交幂等提示用）。
func inviteStatusOutcome(status string) SubmitOutcomeKind {
	switch status {
	case store.WatchInvitePending:
		return SubmitInvitePending
	case store.WatchInviteWaitingTelegram:
		return SubmitInviteWaitingTelegram
	case store.WatchInviteWaitingBot:
		return SubmitInviteWaitingBot
	case store.WatchInviteApproved:
		return SubmitActive
	default:
		return SubmitInviteInvalid
	}
}

// inviteDisplayTitle 生成安全的展示标题：预检标题缺失时回落脱敏 hash
// （完整邀请链接绝不外显）。
func inviteDisplayTitle(req store.WatchInviteRequest) string {
	if req.Title != "" {
		return req.Title
	}
	return "邀请 " + req.MaskedHash
}

// inviteOutcomeTitle 激活结果标题优先取源行（GetChat 后的权威标题）。
func inviteOutcomeTitle(req store.WatchInviteRequest, src *store.WatchSource) string {
	if src != nil && src.Title != "" {
		return src.Title
	}
	return inviteDisplayTitle(req)
}

// inviteErrNote 把加入失败归一为受控备注文案（不含底层错误与 hash）。
func inviteErrNote(err error) string {
	switch apperr.From(err).Code {
	case apperr.CodeInvalidInviteURL, apperr.CodeInvalidURL:
		return "邀请链接无效、已过期或指向不支持的聊天。"
	case apperr.CodeChannelInaccessible:
		return "邀请链接指向的聊天无法访问。"
	default:
		return "读取账号加入失败，请稍后重试。"
	}
}

// firstNonEmpty 返回首个非空字符串（均空返回空串）。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
