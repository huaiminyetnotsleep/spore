// Package joinmgr 频道加入的业务编排：/join 提交、号主审批、退出频道、
// 已加入频道列表与总开关熔断。botapi 与 web 都是薄壳，规则集中在本包
// （仿 internal/access 的服务形态）。
//
// 数据边界：invite_hash 仅在 join_requests 表内流转（审批延时执行所必需），
// 展示层一律脱敏；access_hash 不入库（数据范围红线），退出时经
// MembershipBridge 现场解析。
package joinmgr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// Notifier 把审批结果通知申请人（Bot 私聊）；nil 表示不可用（尽力而为）。
type Notifier interface {
	NotifyUser(ctx context.Context, userID int64, text string) error
}

// ActivityNotifier 是对活动通知（事件中心）的最小依赖（由装配层适配，
// 保持 joinmgr 不依赖 notify）；nil 表示不通知。
type ActivityNotifier interface {
	// ChannelJoinRequested 在新的待审批申请落库时触发；
	// 同链接重复提交（刷新）不触发。
	ChannelJoinRequested(ctx context.Context, info JoinRequestInfo)
}

// JoinRequestInfo 一次新频道加入申请的关键信息（活动通知 payload）。
type JoinRequestInfo struct {
	UserID       int64
	Username     string // Telegram username，可空
	DisplayName  string // Telegram 显示名，可空
	ChannelTitle string // 邀请链接指向的频道标题
	Participants int    // 链接携带的参与人数；0 表示未知
}

// SenderNotifier 用业务 Sender（Bot API 私聊文本）实现通知。
type SenderNotifier struct {
	Sender interface {
		SendMessage(ctx context.Context, chatID int64, html string) (int, error)
	}
}

// Bridge 是频道成员管理的 MTProto 能力接口；*mtproto.MembershipBridge
// 是生产实现，测试以 fake 注入。
type Bridge interface {
	CheckInvite(ctx context.Context, hash string) (mtproto.InviteInfo, error)
	JoinInvite(ctx context.Context, hash string, opts mtproto.JoinOptions) (channelID int64, title string, alreadyJoined bool, err error)
	ApplyPostJoin(ctx context.Context, channelID, accessHash int64, opts mtproto.JoinOptions) error
	LeaveChannel(ctx context.Context, channelID int64) error
	ListJoined(ctx context.Context) ([]mtproto.JoinedChannel, error)
}

// Options 装配依赖。
type Options struct {
	Store    *store.Store
	Bridge   Bridge
	Notifier Notifier         // 可空
	Activity ActivityNotifier // 可空：新加入申请活动通知
	Log      *slog.Logger
	Now      func() time.Time // 可空（测试注入）
}

// Service 是频道加入业务服务。
type Service struct {
	st       *store.Store
	bridge   Bridge
	notify   Notifier
	notifyMu sync.Mutex
	activity ActivityNotifier
	log      *slog.Logger
	now      func() time.Time
}

// New 创建服务。
func New(opt Options) (*Service, error) {
	if opt.Store == nil || opt.Bridge == nil {
		return nil, errors.New("joinmgr: Store 与 Bridge 为必填项")
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Service{st: opt.Store, bridge: opt.Bridge, notify: opt.Notifier,
		activity: opt.Activity, log: opt.Log, now: opt.Now}, nil
}

// NotifyUser 实现 Notifier：经 Bot API 私聊发送文本（HTML）。
func (n SenderNotifier) NotifyUser(ctx context.Context, userID int64, text string) error {
	if n.Sender == nil {
		return errors.New("joinmgr: 通知通道未就绪")
	}
	_, err := n.Sender.SendMessage(ctx, userID, text)
	return err
}

// SetNotifier 注入/替换通知通道（main 在 Bot ready 后装配，与
// bindingSvc.SetBot 同模式）；nil 表示通知不可用。
func (s *Service) SetNotifier(n Notifier) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.notify = n
}

// SubmitOutcome 描述一次 /join 提交的结果，botapi 据此生成用户文案。
type SubmitOutcome struct {
	Kind     SubmitKind
	Title    string // 已加入/待审批的频道标题（尽力获取）
	Requests int    // Kind==LimitReached 时的当前数量
}

// SubmitKind 提交结果分类。
type SubmitKind int

const (
	SubmitJoined           SubmitKind = iota // 已加入成功（owner 即时或无需审核直接加入）
	SubmitAlreadyJoined                      // 账号此前已是成员
	SubmitPending                            // 已创建待审批申请
	SubmitPendingDuplicate                   // 已存在同链接的待审批申请
	SubmitDisabled                           // 功能已关闭（总开关）
	SubmitLimitReached                       // 已达加入数量上限
	SubmitJoinRequested                      // 链接为"加入需审核"制：已向频道管理员发送加入请求
)

// noteJoinRequestedPrefix 请求制频道的申请备注前缀：Approve 时写入，
// 频道侧批准后由 Reconcile 按此前缀找到记录并补执行静音/归档。
const noteJoinRequestedPrefix = "已向频道管理员发送加入请求"

// disabledText 总开关关闭时的用户文案。
const disabledText = "频道加入功能当前已关闭。"

// normalizeInviteError 将成员管理层的通用无效 URL 错误转换为 /join 专用错误，
// 避免用户看到普通消息链接文案。
func normalizeInviteError(err error) error {
	if err == nil {
		return nil
	}
	ae := apperr.From(err)
	if ae.Code != apperr.CodeInvalidURL {
		return err
	}
	return apperr.New(apperr.CodeInvalidInviteURL, ae.Message)
}

// Submit 处理 /join 提交：解析链接 → 开关/上限校验 → 预检 → owner 即时
// 加入 / 普通用户按审核配置落申请或直接加入。
// userID 为提交者 Telegram ID；isOwner 由 botapi 经 store.OwnerID 判定后传入。
func (s *Service) Submit(ctx context.Context, userID int64, isOwner bool, text string) (SubmitOutcome, error) {
	cfg := syscfg.LoadJoinConfig(ctx, s.st)
	if !cfg.Enabled {
		return SubmitOutcome{Kind: SubmitDisabled}, nil
	}

	hash, ok := tmeurl.ParseInvite(text)
	if !ok {
		return SubmitOutcome{}, apperr.New(apperr.CodeInvalidInviteURL,
			"无法识别邀请链接。请发送 /join https://t.me/+xxxx 形式的链接（可用你加入频道时拿到的链接，或向频道管理员索取）。")
	}

	// 「自动退出外部拉入」开启时的惰性检测入口之一（提交即顺带执行）
	if out, err := s.enforceIfNeeded(ctx); err == nil && out.enforced > 0 {
		s.log.Warn("提交时自动退出了外部拉入的频道", "count", out.enforced)
	}

	info, err := s.bridge.CheckInvite(ctx, hash)
	if err != nil {
		return SubmitOutcome{}, normalizeInviteError(err)
	}
	if info.AlreadyJoined {
		// 已是成员（含被外部拉入后提交 /join 的场景）：按配置补执行
		// 静音/归档——checkInvite 在此分支携带频道定位信息正是为此预留。
		// 自动退出开启时让位给 Enforce 的退出语义。
		if info.ChannelID != 0 && !cfg.AutoLeaveExternal && (cfg.MuteEnabled || cfg.ArchiveEnabled) {
			if err := s.bridge.ApplyPostJoin(ctx, info.ChannelID, info.AccessHash, mtproto.JoinOptions{
				Mute: cfg.MuteEnabled, Archive: cfg.ArchiveEnabled,
			}); err != nil {
				s.log.Warn("已是成员频道补静音/归档失败", "channel_id", info.ChannelID, "error", err.Error())
			}
		}
		return SubmitOutcome{Kind: SubmitAlreadyJoined, Title: info.Title}, nil
	}
	if !info.IsChannel {
		return SubmitOutcome{}, apperr.New(apperr.CodeInvalidInviteURL,
			"该邀请链接指向普通群组，仅支持频道/超级群组。")
	}

	// 上限校验（已加入的不占新名额）
	if cfg.MaxChannels > 0 {
		n, err := s.st.CountActiveJoinedChannels(ctx)
		if err != nil {
			return SubmitOutcome{}, err
		}
		if n >= cfg.MaxChannels {
			return SubmitOutcome{Kind: SubmitLimitReached, Requests: n}, nil
		}
	}

	// owner 即时加入；普通用户按审核配置分流
	if isOwner || !cfg.RequireApproval {
		outcome, err := s.joinAndRecord(ctx, hash, info.Title, userID, isOwner, cfg)
		if err != nil {
			return SubmitOutcome{}, err
		}
		return outcome, nil
	}

	req, created, err := s.st.CreateJoinRequest(ctx, store.JoinRequest{
		UserID: userID, InviteHash: hash, ChannelTitle: info.Title,
		Participants: info.Participants,
	})
	if err != nil {
		return SubmitOutcome{}, err
	}
	kind := SubmitPending
	if !created {
		kind = SubmitPendingDuplicate
	} else if s.activity != nil {
		// 新申请活动通知（异步，不阻塞 Bot 回复；重复提交刷新不通知）。
		// 用户名/显示名尽力补齐，查询失败不阻断申请流程。
		username, displayName := "", ""
		if u, uerr := s.st.GetUser(ctx, userID); uerr == nil {
			username, displayName = u.Username, u.DisplayName
		}
		go s.activity.ChannelJoinRequested(ctx, JoinRequestInfo{
			UserID: userID, Username: username, DisplayName: displayName,
			ChannelTitle: info.Title, Participants: info.Participants,
		})
	}
	return SubmitOutcome{Kind: kind, Title: req.ChannelTitle}, nil
}

// joinAndRecord 执行加入 + 加入后动作（静音/归档）+ 留痕。
// 已是成员时（already）不写留痕（此前加入可能已有记录，避免误标来源）；
// 请求制链接（ErrJoinRequestSent）账号尚未成为成员，同样不写留痕。
func (s *Service) joinAndRecord(ctx context.Context, hash, title string, userID int64, isOwner bool, cfg syscfg.JoinConfig) (SubmitOutcome, error) {
	channelID, joinedTitle, already, err := s.bridge.JoinInvite(ctx, hash, mtproto.JoinOptions{
		Mute:    cfg.MuteEnabled,
		Archive: cfg.ArchiveEnabled,
	})
	if errors.Is(err, mtproto.ErrJoinRequestSent) {
		return SubmitOutcome{Kind: SubmitJoinRequested, Title: title}, nil
	}
	if err != nil {
		return SubmitOutcome{}, normalizeInviteError(err)
	}
	if joinedTitle != "" {
		title = joinedTitle
	}
	if already {
		return SubmitOutcome{Kind: SubmitAlreadyJoined, Title: title}, nil
	}
	via := store.JoinedViaApproved
	if isOwner {
		via = store.JoinedViaCommand
	}
	if err := s.st.UpsertJoinedChannel(ctx, store.JoinedChannelRecord{
		ChannelID: channelID, Title: title, Kind: "channel",
		JoinedVia: via, JoinedBy: userID,
	}); err != nil {
		s.log.Error("写入加入留痕失败（不影响加入结果）", "channel_id", channelID, "error", err.Error())
	}
	return SubmitOutcome{Kind: SubmitJoined, Title: title}, nil
}

// Approve 同意待审批申请：重验链接 → 加入 → 留痕 → 通知申请人。
// 链接失效/加入失败时申请置 failed（不回滚到 pending，可重新提交）。
func (s *Service) Approve(ctx context.Context, actor string, requestID int64) (JoinRequestView, error) {
	req, err := s.st.GetJoinRequest(ctx, requestID)
	if err != nil {
		return JoinRequestView{}, err
	}
	if req.Status != store.JoinPending {
		return JoinRequestView{}, apperr.New(apperr.CodeStoreConstraint,
			"该申请已被处理或不存在")
	}
	cfg := syscfg.LoadJoinConfig(ctx, s.st)
	if !cfg.Enabled {
		return JoinRequestView{}, apperr.New(apperr.CodeStoreConstraint, disabledText)
	}

	view := JoinRequestView{ID: req.ID, UserID: req.UserID, ChannelTitle: req.ChannelTitle}

	channelID, title, already, err := s.bridge.JoinInvite(ctx, req.InviteHash, mtproto.JoinOptions{
		Mute:    cfg.MuteEnabled,
		Archive: cfg.ArchiveEnabled,
	})
	if errors.Is(err, mtproto.ErrJoinRequestSent) {
		// 链接为"加入需管理员审核"制：系统已发出加入请求，待频道侧批准；
		// 申请置 approved（非 failed），频道批准后由 Reconcile 补静音/归档
		note := noteJoinRequestedPrefix + "，等待频道管理员批准。"
		updated, rerr := s.st.ReviewJoinRequest(ctx, req.ID, store.JoinApproved, actor, note, 0)
		if rerr != nil {
			return view, rerr
		}
		view.Status = updated.Status
		view.ReviewedAt = updated.ReviewedAt
		view.ReviewedBy = updated.ReviewedBy
		view.Note = updated.Note
		s.notifyUser(ctx, req.UserID, fmt.Sprintf(
			"你申请加入的频道 %s 已通过审核。该频道开启了加入审核，系统账号已向频道管理员发送加入请求，批准后即可发送该频道的消息链接。",
			displayTitle(view.ChannelTitle)))
		return view, nil
	}
	if err != nil {
		err = normalizeInviteError(err)
		if _, uerr := s.st.ReviewJoinRequest(ctx, req.ID, store.JoinFailed, actor,
			fmt.Sprintf("加入失败：%s", errText(err)), 0); uerr != nil {
			s.log.Error("标记加入申请失败态出错", "request_id", req.ID, "error", uerr.Error())
		}
		view.Status = store.JoinFailed
		return view, err
	}
	if title != "" {
		view.ChannelTitle = title
	}

	status := store.JoinApproved
	note := ""
	if already {
		note = "账号已是该频道成员，未重复加入"
	} else if err := s.st.UpsertJoinedChannel(ctx, store.JoinedChannelRecord{
		ChannelID: channelID, Title: title, Kind: "channel",
		JoinedVia: store.JoinedViaApproved, JoinedBy: req.UserID,
	}); err != nil {
		s.log.Error("审批后写入加入留痕失败（不影响审批结果）", "channel_id", channelID, "error", err.Error())
	}
	updated, err := s.st.ReviewJoinRequest(ctx, req.ID, status, actor, note, 0)
	if err != nil {
		return view, err
	}
	view.Status = updated.Status
	view.ReviewedAt = updated.ReviewedAt
	view.ReviewedBy = updated.ReviewedBy
	view.Note = updated.Note

	s.notifyUser(ctx, req.UserID, fmt.Sprintf(
		"你申请加入的频道 %s 已通过审核%s，现在可以发送该频道的消息链接了。",
		displayTitle(view.ChannelTitle), joinSuffix(already)))
	return view, nil
}

// Reject 拒绝待审批申请并通知申请人。
func (s *Service) Reject(ctx context.Context, actor string, requestID int64) (JoinRequestView, error) {
	req, err := s.st.GetJoinRequest(ctx, requestID)
	if err != nil {
		return JoinRequestView{}, err
	}
	if req.Status != store.JoinPending {
		return JoinRequestView{}, apperr.New(apperr.CodeStoreConstraint,
			"该申请已被处理或不存在")
	}
	updated, err := s.st.ReviewJoinRequest(ctx, req.ID, store.JoinRejected, actor, "", 0)
	if err != nil {
		return JoinRequestView{}, err
	}

	s.notifyUser(ctx, req.UserID, fmt.Sprintf(
		"你申请加入的频道 %s 未通过审核。", displayTitle(updated.ChannelTitle)))
	return JoinRequestView{ID: updated.ID, UserID: updated.UserID,
		ChannelTitle: updated.ChannelTitle, Status: updated.Status,
		ReviewedAt: updated.ReviewedAt, ReviewedBy: updated.ReviewedBy, Note: updated.Note}, nil
}

// JoinRequestView 是 Web 层的申请视图：不含 invite_hash 明文。
type JoinRequestView struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"user_id"`
	ChannelTitle string `json:"channel_title"`
	Status       string `json:"status"`
	RequestedAt  int64  `json:"requested_at"`
	ReviewedAt   int64  `json:"reviewed_at"`
	ReviewedBy   string `json:"reviewed_by"`
	Note         string `json:"note"`
	MaskedHash   string `json:"masked_hash"`
}

// RequestsQuery 是审批记录列表的筛选与分页参数（Web 层透传）。
// 零值字段表示该条件不限；Page/PageSize <= 0 时取全量。
type RequestsQuery struct {
	Status       string // pending/approved/rejected/failed，空为全部
	UserID       int64  // >0 精确匹配申请人
	TitleKeyword string // 频道标题模糊匹配
	Since, Until int64  // requested_at 范围（Unix 毫秒；0=不限）
	Page         int    // 1 起
	PageSize     int
}

// ListRequests 按筛选与分页列出加入申请，返回脱敏视图与总数。
func (s *Service) ListRequests(ctx context.Context, q RequestsQuery) ([]JoinRequestView, int, error) {
	if q.Status != "" && !map[string]bool{
		store.JoinPending: true, store.JoinApproved: true,
		store.JoinRejected: true, store.JoinFailed: true,
	}[q.Status] {
		return nil, 0, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法状态筛选 %q", q.Status))
	}
	f := store.JoinRequestFilter{
		Status: q.Status, UserID: q.UserID, TitleKeyword: q.TitleKeyword,
		Since: q.Since, Until: q.Until,
	}
	total, err := s.st.CountJoinRequests(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	if q.PageSize > 0 {
		f.Limit = q.PageSize
		f.Offset = (q.Page - 1) * q.PageSize
		if f.Offset < 0 {
			f.Offset = 0
		}
	}
	rows, err := s.st.ListJoinRequests(ctx, f)
	if err != nil {
		return nil, 0, err
	}
	out := make([]JoinRequestView, 0, len(rows))
	for _, r := range rows {
		out = append(out, JoinRequestView{
			ID: r.ID, UserID: r.UserID, ChannelTitle: r.ChannelTitle,
			Status: r.Status, RequestedAt: r.RequestedAt,
			ReviewedAt: r.ReviewedAt, ReviewedBy: r.ReviewedBy, Note: r.Note,
			MaskedHash: tmeurl.MaskInviteHash(r.InviteHash),
		})
	}
	return out, total, nil
}

// RequestDeleteOutcome 是审批记录批量删除的逐条结果。
type RequestDeleteOutcome struct {
	ID    int64  `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// DeleteRequests 批量删除审批记录：仅终态（approved/rejected/failed）可删，
// pending 须先同意或拒绝（避免申请人等待一个已消失的申请）。
// 单条失败不回滚其他项。
func (s *Service) DeleteRequests(ctx context.Context, ids []int64) []RequestDeleteOutcome {
	out := make([]RequestDeleteOutcome, 0, len(ids))
	deleted := 0
	for _, id := range ids {
		req, err := s.st.GetJoinRequest(ctx, id)
		switch {
		case errors.Is(err, store.ErrNotFound):
			out = append(out, RequestDeleteOutcome{ID: id, Error: "记录不存在"})
			continue
		case err != nil:
			out = append(out, RequestDeleteOutcome{ID: id, Error: errText(err)})
			continue
		}
		if req.Status == store.JoinPending {
			out = append(out, RequestDeleteOutcome{ID: id,
				Error: "待审批记录不可删除，请先同意或拒绝"})
			continue
		}
		if err := s.st.DeleteJoinRequest(ctx, id); err != nil {
			out = append(out, RequestDeleteOutcome{ID: id, Error: errText(err)})
			continue
		}
		deleted++
		out = append(out, RequestDeleteOutcome{ID: id, OK: true})
	}
	s.log.Info("删除审批记录", "requested", len(ids), "deleted", deleted)
	return out
}

// JoinedChannelView 是已加入频道列表行：实时数据 + 留痕来源标注。
type JoinedChannelView struct {
	ChannelID int64  `json:"channel_id"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`    // 留痕来源（无留痕 = external）
	JoinedVia int64  `json:"joined_by"` // 留痕归属用户（0 = 无）
	JoinedAt  int64  `json:"joined_at"` // 留痕时间（0 = 未知）
	Creator   bool   `json:"creator"`   // 创建者不可退出
}

// ListJoined 返回当前账号实际加入的频道（实时为准），并把留痕来源
// 标注上去；同时以实时数据补全留痕（新加入但未落库的记录为 external）。
// 顺带对未归档的外部拉入频道补执行静音/归档（惰性对账入口之一）。
func (s *Service) ListJoined(ctx context.Context) ([]JoinedChannelView, error) {
	cfg := syscfg.LoadJoinConfig(ctx, s.st)
	live, err := s.bridge.ListJoined(ctx)
	if err != nil {
		return nil, err
	}
	records, err := s.st.ListActiveJoinedChannels(ctx)
	if err != nil {
		return nil, err
	}
	byID := map[int64]store.JoinedChannelRecord{}
	for _, r := range records {
		byID[r.ChannelID] = r
	}

	out := make([]JoinedChannelView, 0, len(live))
	for _, c := range live {
		view := JoinedChannelView{
			ChannelID: c.ChannelID, Title: c.Title, Username: c.Username,
			Kind: c.Kind, Creator: c.Creator, Source: store.JoinedViaExternal,
		}
		if rec, ok := byID[c.ChannelID]; ok {
			view.Source = rec.JoinedVia
			view.JoinedVia = rec.JoinedBy
			view.JoinedAt = rec.JoinedAt
		} else {
			// 实时存在但无留痕：外部拉入，补一条留痕（Enforce 的判定依据）
			if err := s.st.UpsertJoinedChannel(ctx, store.JoinedChannelRecord{
				ChannelID: c.ChannelID, Title: c.Title, Username: c.Username,
				Kind: c.Kind, JoinedVia: store.JoinedViaExternal,
			}); err != nil {
				s.log.Warn("补写外部拉入留痕失败", "channel_id", c.ChannelID, "error", err.Error())
			}
		}
		out = append(out, view)
	}
	if n := s.archiveExternalPass(ctx, cfg, live, byID); n > 0 {
		s.log.Info("已归档外部拉入的频道", "count", n)
	}
	return out, nil
}

// archiveExternalPass 对"仍在主聊天列表（未归档）且来源为外部拉入（无留痕
// 或留痕 external）"的频道补执行静音/归档，返回处理成功的数量。实时
// （update）、页面刷新（ListJoined）与周期对账（ReconcileExternal）三个
// 入口共用。本系统经 /join 或审批加入的频道不在此处理（加入时已执行过，
// 且归档失败不越界重试）。失败只记日志——下次入口触发时按未归档状态重试。
func (s *Service) archiveExternalPass(ctx context.Context, cfg syscfg.JoinConfig,
	live []mtproto.JoinedChannel, records map[int64]store.JoinedChannelRecord) int {
	// 「自动退出外部拉入」开启时退出优先，归档让位（Enforce 负责退出）
	if cfg.AutoLeaveExternal || (!cfg.MuteEnabled && !cfg.ArchiveEnabled) {
		return 0
	}
	n := 0
	for _, c := range live {
		if c.Archived {
			continue // 已在归档夹
		}
		if rec, ok := records[c.ChannelID]; ok && rec.JoinedVia != store.JoinedViaExternal {
			continue // 本系统明示加入的频道
		}
		if err := s.bridge.ApplyPostJoin(ctx, c.ChannelID, c.AccessHash, mtproto.JoinOptions{
			Mute: cfg.MuteEnabled, Archive: cfg.ArchiveEnabled,
		}); err != nil {
			s.log.Warn("外部拉入频道补静音/归档失败", "channel_id", c.ChannelID, "error", err.Error())
			continue
		}
		n++
	}
	return n
}

// OnChannelsSeen 实时入口：MTProto update 携带的频道对象经 peer 缓存过滤
// （Fetcher.HarvestNovelChannels）后到达此处，仅"新见"频道。对外部拉入
// （无留痕或留痕 external）的频道按配置补静音/归档并补留痕，实现被拉入后
// 秒级归档。同步执行（装配层负责异步化，不在 gotd 读循环内做网络调用），
// 任何失败只记日志（惰性对账兜底）。
func (s *Service) OnChannelsSeen(ctx context.Context, seen []mtproto.JoinedChannel) {
	if len(seen) == 0 {
		return
	}
	cfg := syscfg.LoadJoinConfig(ctx, s.st)
	if cfg.AutoLeaveExternal || (!cfg.MuteEnabled && !cfg.ArchiveEnabled) {
		return
	}
	records, err := s.st.ListActiveJoinedChannels(ctx)
	if err != nil {
		s.log.Warn("实时归档前读取留痕失败", "error", err.Error())
		return
	}
	byID := map[int64]store.JoinedChannelRecord{}
	for _, r := range records {
		byID[r.ChannelID] = r
	}
	for _, c := range seen {
		if c.AccessHash == 0 {
			continue // 无定位信息（异常形态），留给惰性对账
		}
		rec, has := byID[c.ChannelID]
		if has && rec.JoinedVia != store.JoinedViaExternal {
			continue // 本系统加入的频道，加入时已执行过静音/归档
		}
		if err := s.bridge.ApplyPostJoin(ctx, c.ChannelID, c.AccessHash, mtproto.JoinOptions{
			Mute: cfg.MuteEnabled, Archive: cfg.ArchiveEnabled,
		}); err != nil {
			s.log.Warn("实时归档外部拉入频道失败", "channel_id", c.ChannelID, "error", err.Error())
			continue
		}
		if !has {
			if err := s.st.UpsertJoinedChannel(ctx, store.JoinedChannelRecord{
				ChannelID: c.ChannelID, Title: c.Title, Username: c.Username,
				Kind: c.Kind, JoinedVia: store.JoinedViaExternal,
			}); err != nil {
				s.log.Warn("实时归档后补写留痕失败", "channel_id", c.ChannelID, "error", err.Error())
			}
		}
		s.log.Info("实时归档外部拉入频道完成", "channel_id", c.ChannelID, "title", c.Title)
	}
}

// ReconcileExternal 周期兜底对账：遍历对话列表，对未归档的外部拉入频道补
// 静音/归档。覆盖离线窗口被拉入、update 漏收（重连不补差异）的场景；
// 装配层在 MTProto ready 作用域内定时调用。
func (s *Service) ReconcileExternal(ctx context.Context) error {
	cfg := syscfg.LoadJoinConfig(ctx, s.st)
	if cfg.AutoLeaveExternal || (!cfg.MuteEnabled && !cfg.ArchiveEnabled) {
		return nil
	}
	live, err := s.bridge.ListJoined(ctx)
	if err != nil {
		return err
	}
	records, err := s.st.ListActiveJoinedChannels(ctx)
	if err != nil {
		return err
	}
	byID := map[int64]store.JoinedChannelRecord{}
	for _, r := range records {
		byID[r.ChannelID] = r
	}
	if n := s.archiveExternalPass(ctx, cfg, live, byID); n > 0 {
		s.log.Info("周期对账已归档外部拉入的频道", "count", n)
	}
	return nil
}

// LeaveOutcome 批量退出的逐条结果。
type LeaveOutcome struct {
	ChannelID int64  `json:"channel_id"`
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
}

// Leave 批量退出频道：creator 频道与未加入的 ID 记为失败，其余退出并
// 更新留痕；部分失败不阻断其余条目。
func (s *Service) Leave(ctx context.Context, channelIDs []int64) []LeaveOutcome {
	out := make([]LeaveOutcome, 0, len(channelIDs))
	var leftIDs []int64
	for _, id := range channelIDs {
		err := s.bridge.LeaveChannel(ctx, id)
		switch {
		case err == nil:
			leftIDs = append(leftIDs, id)
			out = append(out, LeaveOutcome{ChannelID: id, OK: true})
		case errors.Is(err, mtproto.ErrChannelCreator):
			out = append(out, LeaveOutcome{ChannelID: id, OK: false,
				Error: "该账号是频道创建者，无法退出"})
		case errors.Is(err, mtproto.ErrMembershipUnavailable):
			out = append(out, LeaveOutcome{ChannelID: id, OK: false,
				Error: "Telegram 用户号当前离线，请稍后重试"})
		default:
			out = append(out, LeaveOutcome{ChannelID: id, OK: false,
				Error: errText(err)})
		}
	}
	if len(leftIDs) > 0 {
		if _, err := s.st.MarkJoinedChannelsLeft(ctx, leftIDs, 0); err != nil {
			s.log.Error("更新退出留痕失败", "error", err.Error())
		}
	}
	return out
}

// enforceResult 是熔断执行结果。
type enforceResult struct {
	enforced int // 自动退出的频道数
	ids      []int64
}

// enforceIfNeeded 仅在「自动退出外部拉入」开关开启时执行（默认关，独立于
// 总开关）：实时列表中"非本系统加入"的频道（留痕来源 external 或无留痕）
// 自动退出。经 /join 或审批加入的记录保留——它们是本系统明示同意过的。
// 调用点：已加入频道页刷新（ListJoined 前置）与 /join 提交，均为惰性触发。
func (s *Service) enforceIfNeeded(ctx context.Context) (enforceResult, error) {
	cfg := syscfg.LoadJoinConfig(ctx, s.st)
	if !cfg.AutoLeaveExternal {
		return enforceResult{}, nil
	}
	live, err := s.bridge.ListJoined(ctx)
	if err != nil {
		return enforceResult{}, err
	}
	records, err := s.st.ListActiveJoinedChannels(ctx)
	if err != nil {
		return enforceResult{}, err
	}
	byID := map[int64]store.JoinedChannelRecord{}
	for _, r := range records {
		byID[r.ChannelID] = r
	}

	var res enforceResult
	for _, c := range live {
		rec, has := byID[c.ChannelID]
		// 无留痕（先补写再退出留痕）或有留痕但来源是 external：熔断对象
		if has && rec.JoinedVia != store.JoinedViaExternal {
			continue
		}
		if c.Creator {
			continue // 创建者无法退出
		}
		if err := s.bridge.LeaveChannel(ctx, c.ChannelID); err != nil {
			s.log.Warn("熔断退出频道失败", "channel_id", c.ChannelID, "error", err.Error())
			continue
		}
		if !has {
			if err := s.st.UpsertJoinedChannel(ctx, store.JoinedChannelRecord{
				ChannelID: c.ChannelID, Title: c.Title, Username: c.Username,
				Kind: c.Kind, JoinedVia: store.JoinedViaExternal,
			}); err != nil {
				s.log.Warn("熔断前补写留痕失败", "channel_id", c.ChannelID, "error", err.Error())
			}
		}
		res.enforced++
		res.ids = append(res.ids, c.ChannelID)
	}
	if len(res.ids) > 0 {
		if _, err := s.st.MarkJoinedChannelsLeft(ctx, res.ids, 0); err != nil {
			s.log.Error("熔断退出留痕更新失败", "error", err.Error())
		}
		s.log.Info("自动退出外部拉入的频道（join_auto_leave_external 已开启）", "count", res.enforced)
	}
	return res, nil
}

// Enforce 暴露给 Web 管理页刷新时调用（惰性熔断入口）。
func (s *Service) Enforce(ctx context.Context) error {
	_, err := s.enforceIfNeeded(ctx)
	return err
}

// ReconcilePendingJoins 懒对账：请求制频道的加入请求被频道管理员批准后，
// 账号才真正成为成员——此时补执行静音/归档并回写备注。
// 已加入频道页加载时调用（惰性触发，量级为待对账的申请数）。
func (s *Service) ReconcilePendingJoins(ctx context.Context) error {
	f := store.JoinRequestFilter{Status: store.JoinApproved, NotePrefix: noteJoinRequestedPrefix}
	reqs, err := s.st.ListJoinRequests(ctx, f)
	if err != nil {
		return err
	}
	if len(reqs) == 0 {
		return nil
	}
	cfg := syscfg.LoadJoinConfig(ctx, s.st)
	for _, r := range reqs {
		info, err := s.bridge.CheckInvite(ctx, r.InviteHash)
		if err != nil {
			s.log.Warn("请求制频道对账预检失败", "request_id", r.ID, "error", err.Error())
			continue
		}
		if !info.AlreadyJoined || info.ChannelID == 0 {
			continue // 频道侧尚未批准
		}
		if err := s.bridge.ApplyPostJoin(ctx, info.ChannelID, info.AccessHash, mtproto.JoinOptions{
			Mute:    cfg.MuteEnabled,
			Archive: cfg.ArchiveEnabled,
		}); err != nil {
			s.log.Warn("请求制频道补执行静音/归档失败", "request_id", r.ID, "channel_id", info.ChannelID, "error", err.Error())
			continue
		}
		// 频道补留痕（邀请制加入本系统无从记录来源，按审批通过归档）
		if err := s.st.UpsertJoinedChannel(ctx, store.JoinedChannelRecord{
			ChannelID: info.ChannelID, Title: info.Title, Kind: "channel",
			JoinedVia: store.JoinedViaApproved, JoinedBy: r.UserID,
		}); err != nil {
			s.log.Warn("请求制频道补写留痕失败", "channel_id", info.ChannelID, "error", err.Error())
		}
		note := "频道管理员已批准加入，已执行静音/归档。"
		if err := s.st.UpdateJoinRequestNote(ctx, r.ID, note, 0); err != nil {
			s.log.Warn("请求制频道对账备注回写失败", "request_id", r.ID, "error", err.Error())
		}
		s.log.Info("请求制频道已批准，补执行静音/归档完成", "request_id", r.ID, "channel_id", info.ChannelID)
	}
	return nil
}

// notifyUser 尽力而为地通知申请人；无 Notifier 或发送失败只记日志。
func (s *Service) notifyUser(ctx context.Context, userID int64, text string) {
	s.notifyMu.Lock()
	n := s.notify
	s.notifyMu.Unlock()
	if n == nil {
		return
	}
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := n.NotifyUser(nctx, userID, text); err != nil {
		s.log.Warn("通知申请用户失败", "user_id", userID, "error", err.Error())
	}
}

func displayTitle(title string) string {
	if title == "" {
		return "（标题未知）"
	}
	return title
}

// errText 把任意错误转为用户可读文案（AppError 取其码的标准文案）。
func errText(err error) string {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		return apperr.UserText(ae.Code)
	}
	return apperr.UserText(apperr.CodeInternal)
}

func joinSuffix(already bool) string {
	if already {
		return "（账号此前已在频道中）"
	}
	return ""
}
