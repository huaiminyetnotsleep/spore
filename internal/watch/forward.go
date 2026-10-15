// forward.go — 监听源专属转发目标的用户自助能力（Bot /watch 后选择、
// /watchtarget 重选/清除、私有邀请申请暂存）与管理员全局镜像开关。
//
// 服务层集中校验，Bot handler 与 Web handler 不拼联查 SQL：
//   - 源归当前用户所有（他人源/管理员源一律 ErrNotFound，不泄露存在性）；
//   - 目标必须是本人 active 绑定且与源受理 Bot 同路由（多 Bot 场景精确
//     投递的前提，也是隐私边界）；
//   - 循环防护：目标不能是任何监听源或缓存频道（双向校验的另一侧——
//     Submit/AdminAdd/激活路径拒绝被目标占用的源，见 watch.go / invite.go）。
//
// 私有邀请申请在激活成源前经 StageInviteForwardTarget 暂存选择，激活时
// 由 activateInvite 复验并原子转移到 watch_sources；终态申请不保留暂存。
package watch

import (
	"context"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// 专属目标操作的哨兵错误（apperr 包装受控文案；Bot/Web 按 errors.Is 分流）。
var (
	// ErrForwardTargetInvalid 目标不是本人有效绑定（不存在/已解绑/他人绑定）。
	ErrForwardTargetInvalid = errors.New("专属目标必须是本人当前有效的频道绑定")
	// ErrForwardRouteMismatch 绑定路由与源受理 Bot 不一致（或源未记录受理 Bot）。
	ErrForwardRouteMismatch = errors.New("专属目标必须经监听源的受理 Bot 绑定（先在该 Bot 用 /bind 绑定目标）")
	// ErrForwardTargetLoop 目标是监听源或缓存频道（循环转发防护）。
	ErrForwardTargetLoop = errors.New("专属目标不能是监听源或缓存频道（循环转发防护）")
	// ErrForwardTargetConflict 新源频道已被用作转发目标（循环转发防护，
	// AdminAdd/激活路径转受控错误；用户 /watch 走 SubmitForwardTargetConflict）。
	ErrForwardTargetConflict = errors.New("该频道已配置为转发目标，不能同时作为监听源（循环转发防护）")
	// ErrInviteRequestClosed 邀请申请已终态，无法变更暂存的专属目标。
	ErrInviteRequestClosed = errors.New("该邀请申请已结束，无法变更专属目标")
)

// ForwardTargetState 是专属目标展示状态（/watchlist 与管理端列表共用）。
type ForwardTargetState string

const (
	ForwardTargetSet   ForwardTargetState = "set"   // 已设置且当前有效
	ForwardTargetUnset ForwardTargetState = "unset" // 未设置
	ForwardTargetStale ForwardTargetState = "stale" // 已设置但绑定失效（软解绑/归属/路由变更）
)

// ForwardTargetView 是专属目标的管理/展示视图（仅数值 ID、标题快照与
// 受控状态，不含绑定来源敏感信息）。
type ForwardTargetView struct {
	State     ForwardTargetState `json:"state"`
	ChannelID int64              `json:"channel_id,omitempty"`
	Title     string             `json:"title,omitempty"`
}

// userBindingStatus 复查源专属目标的绑定行：返回绑定、失效原因码与是否
// 有效（plan 投递、/watchlist 与管理端展示共用同一判定）。
func (s *Service) userBindingStatus(ctx context.Context, src store.WatchSource) (store.ChannelBinding, string, bool) {
	if src.ForwardBindingChannelID == 0 {
		return store.ChannelBinding{}, "", false
	}
	if src.BotID == 0 {
		return store.ChannelBinding{}, "源未记录受理 Bot", false
	}
	b, err := s.st.GetChannelBinding(ctx, src.ForwardBindingChannelID)
	switch {
	case err != nil:
		return store.ChannelBinding{}, "绑定不存在", false
	case b.Status != store.BindingStatusActive:
		return b, "绑定已解绑", false
	case b.UserID != src.AddedBy:
		return b, "绑定已归属其他用户", false
	case b.BotID != src.BotID:
		return b, "绑定路由与源受理 Bot 不一致", false
	}
	return b, "", true
}

// ForwardTargetViewOf 计算源专属目标的展示视图。
func (s *Service) ForwardTargetViewOf(ctx context.Context, src store.WatchSource) ForwardTargetView {
	if src.ForwardBindingChannelID == 0 {
		return ForwardTargetView{State: ForwardTargetUnset}
	}
	b, _, ok := s.userBindingStatus(ctx, src)
	if !ok {
		return ForwardTargetView{State: ForwardTargetStale, ChannelID: src.ForwardBindingChannelID}
	}
	title := b.Title
	if title == "" {
		title = fmt.Sprintf("%d", b.ChannelID)
	}
	return ForwardTargetView{State: ForwardTargetSet, ChannelID: b.ChannelID, Title: title}
}

// EligibleForwardTargets 返回用户可为该源选择的专属目标候选：本人 active
// 绑定、与源受理 Bot 同路由，且不是监听源/缓存频道（循环防护）。源必须
// 归该用户（否则 ErrNotFound）。源未记录受理 Bot 时返回空列表（botapi
// 据此引导重新 /watch 记录受理 Bot）。
func (s *Service) EligibleForwardTargets(ctx context.Context, userID, sourceChannelID int64) ([]store.ChannelBinding, error) {
	src, err := s.st.GetWatchSource(ctx, sourceChannelID)
	if err != nil {
		return nil, err
	}
	if src.AddedBy != userID {
		return nil, store.ErrNotFound
	}
	return s.eligibleBindingsForSource(ctx, src)
}

// eligibleBindingsForSource 按源视图过滤可选绑定（路由一致 + 循环目标
// 候选级剔除）；源路径与邀请申请路径共用同一筛选口径。
func (s *Service) eligibleBindingsForSource(ctx context.Context, src store.WatchSource) ([]store.ChannelBinding, error) {
	bindings, err := s.st.ListChannelBindingsByUser(ctx, src.AddedBy)
	if err != nil {
		return nil, err
	}
	out := make([]store.ChannelBinding, 0, len(bindings))
	for _, b := range bindings {
		if b.BotID != src.BotID {
			continue
		}
		if err := s.checkForwardTargetLoops(ctx, src, b.ChannelID); err != nil {
			continue // 候选级过滤：循环目标直接不展示，不报错打断整个列表
		}
		out = append(out, b)
	}
	return out, nil
}

// EligibleForwardTargetsForInvite 返回活动邀请申请可选的专属目标候选：
// 与 StageInviteForwardTarget 同一套归属/路由/循环校验（以申请行构造等价
// 源视图），满足私有邀请「提交后立即选择」。申请不归该用户或已到终态
// 返回 ErrNotFound（申请 ID 由提交结果带出，存在性不额外泄露）。
func (s *Service) EligibleForwardTargetsForInvite(ctx context.Context, userID, inviteID int64) ([]store.ChannelBinding, error) {
	req, err := s.st.GetWatchInviteRequest(ctx, inviteID)
	if err != nil {
		return nil, err
	}
	if req.UserID != userID {
		return nil, store.ErrNotFound
	}
	switch req.Status {
	case store.WatchInvitePending, store.WatchInviteWaitingTelegram, store.WatchInviteWaitingBot:
	default:
		return nil, store.ErrNotFound
	}
	pseudo := store.WatchSource{ChannelID: req.ChannelID, AddedBy: req.UserID, BotID: req.BotID}
	return s.eligibleBindingsForSource(ctx, pseudo)
}

// SetForwardTarget 为源设置/重选专属目标（源必须归 userID 所有，任意状态
// 均可设置——待审批先保存）。校验失败返回受控哨兵错误；成功返回最新源行。
func (s *Service) SetForwardTarget(ctx context.Context, userID, sourceChannelID, targetChannelID int64) (store.WatchSource, error) {
	src, err := s.st.GetWatchSource(ctx, sourceChannelID)
	if err != nil {
		return store.WatchSource{}, err
	}
	if src.AddedBy != userID || src.AddedBy == 0 {
		// 他人源/管理员源一律按不存在处理（不泄露存在性）
		return store.WatchSource{}, store.ErrNotFound
	}
	b, err := s.st.GetChannelBinding(ctx, targetChannelID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.WatchSource{}, apperr.Wrap(apperr.CodeChannelTargetInvalid, ErrForwardTargetInvalid)
		}
		return store.WatchSource{}, err
	}
	if err := s.checkForwardTargetEligible(src, b); err != nil {
		return store.WatchSource{}, err
	}
	if err := s.checkForwardTargetLoops(ctx, src, b.ChannelID); err != nil {
		return store.WatchSource{}, err
	}
	row, err := s.st.SetWatchSourceForwardBinding(ctx, src.ChannelID, b.ChannelID)
	if err != nil {
		return store.WatchSource{}, err
	}
	s.log.Info("监听源专属目标已设置", "source_channel_id", src.ChannelID,
		"target_channel_id", b.ChannelID, "user_id", userID, "bot_id", src.BotID)
	return row, nil
}

// ClearForwardTarget 清空源的专属目标（仅申请人本人；未设置时幂等成功）。
// 清空后监听继续缓存预热（全局镜像与否取决于源开关，与专属目标无关）。
func (s *Service) ClearForwardTarget(ctx context.Context, userID, sourceChannelID int64) (store.WatchSource, error) {
	src, err := s.st.GetWatchSource(ctx, sourceChannelID)
	if err != nil {
		return store.WatchSource{}, err
	}
	if src.AddedBy != userID || src.AddedBy == 0 {
		return store.WatchSource{}, store.ErrNotFound
	}
	row, err := s.st.SetWatchSourceForwardBinding(ctx, src.ChannelID, 0)
	if err != nil {
		return store.WatchSource{}, err
	}
	s.log.Info("监听源专属目标已清空", "source_channel_id", src.ChannelID, "user_id", userID)
	return row, nil
}

// SetGlobalMirror 管理员逐源切换「额外镜像到全局监听转发频道」开关。
// 用户源默认关（隐私收紧）、管理员源默认开（保持历史行为）；切换即时
// 生效（投递计划按批实时读取）。Web 层负责会话/CSRF/审计边界。
func (s *Service) SetGlobalMirror(ctx context.Context, channelID int64, enabled bool) (store.WatchSource, error) {
	row, err := s.st.SetWatchSourceGlobalMirror(ctx, channelID, enabled)
	if err != nil {
		return store.WatchSource{}, err
	}
	s.log.Info("监听源全局镜像开关已切换", "channel_id", channelID, "global_mirror_enabled", enabled)
	return row, nil
}

// StageInviteForwardTarget 在活动邀请申请上暂存申请人已选的专属目标
// （满足私有邀请「提交后立即选择」）；激活时由 activateInvite 复验转移，
// 失败/拒绝终态不保留暂存。申请必须归 userID 所有且仍处活动状态。
func (s *Service) StageInviteForwardTarget(ctx context.Context, userID, inviteID, targetChannelID int64) (store.WatchInviteRequest, error) {
	req, err := s.st.GetWatchInviteRequest(ctx, inviteID)
	if err != nil {
		return store.WatchInviteRequest{}, err
	}
	if req.UserID != userID {
		return store.WatchInviteRequest{}, store.ErrNotFound
	}
	switch req.Status {
	case store.WatchInvitePending, store.WatchInviteWaitingTelegram, store.WatchInviteWaitingBot:
	default:
		return store.WatchInviteRequest{}, apperr.Wrap(apperr.CodeStoreConstraint, ErrInviteRequestClosed)
	}
	b, err := s.st.GetChannelBinding(ctx, targetChannelID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return store.WatchInviteRequest{}, apperr.Wrap(apperr.CodeChannelTargetInvalid, ErrForwardTargetInvalid)
		}
		return store.WatchInviteRequest{}, err
	}
	// 以申请行的归属/路由构造等价源视图做同一套校验
	pseudo := store.WatchSource{
		ChannelID: req.ChannelID, AddedBy: req.UserID, BotID: req.BotID,
	}
	if err := s.checkForwardTargetEligible(pseudo, b); err != nil {
		return store.WatchInviteRequest{}, err
	}
	if err := s.checkForwardTargetLoops(ctx, pseudo, b.ChannelID); err != nil {
		return store.WatchInviteRequest{}, err
	}
	out, err := s.st.SetWatchInviteRequestForwardBinding(ctx, req.ID, b.ChannelID)
	if err != nil {
		return store.WatchInviteRequest{}, err
	}
	s.log.Info("监听邀请申请专属目标已暂存", "request_id", req.ID,
		"target_channel_id", b.ChannelID, "user_id", userID, "bot_id", req.BotID)
	return out, nil
}

// ClearInviteForwardTarget 清空活动邀请申请的暂存目标（仅申请人本人）。
func (s *Service) ClearInviteForwardTarget(ctx context.Context, userID, inviteID int64) (store.WatchInviteRequest, error) {
	req, err := s.st.GetWatchInviteRequest(ctx, inviteID)
	if err != nil {
		return store.WatchInviteRequest{}, err
	}
	if req.UserID != userID {
		return store.WatchInviteRequest{}, store.ErrNotFound
	}
	switch req.Status {
	case store.WatchInvitePending, store.WatchInviteWaitingTelegram, store.WatchInviteWaitingBot:
	default:
		return store.WatchInviteRequest{}, apperr.Wrap(apperr.CodeStoreConstraint, ErrInviteRequestClosed)
	}
	out, err := s.st.SetWatchInviteRequestForwardBinding(ctx, req.ID, 0)
	if err != nil {
		return store.WatchInviteRequest{}, err
	}
	s.log.Info("监听邀请申请专属目标暂存已清空", "request_id", req.ID, "user_id", userID)
	return out, nil
}

// checkForwardTargetEligible 校验绑定本身可用：active、归属源申请人、与
// 源受理 Bot 同路由。不查循环（见 checkForwardTargetLoops）。
func (s *Service) checkForwardTargetEligible(src store.WatchSource, b store.ChannelBinding) error {
	switch {
	case b.Status != store.BindingStatusActive:
		return apperr.Wrap(apperr.CodeChannelTargetInvalid, fmt.Errorf("%w：绑定已解绑", ErrForwardTargetInvalid))
	case b.UserID != src.AddedBy:
		return apperr.Wrap(apperr.CodeChannelTargetInvalid, ErrForwardTargetInvalid)
	case src.BotID == 0 || b.BotID != src.BotID:
		return apperr.Wrap(apperr.CodeChannelNotPostable, ErrForwardRouteMismatch)
	}
	return nil
}

// checkForwardTargetLoops 循环防护：目标不能是任何监听源（含源自身），
// 也不能是缓存频道（预热副本写回会被再次监听）。
func (s *Service) checkForwardTargetLoops(ctx context.Context, src store.WatchSource, targetChannelID int64) error {
	if targetChannelID == src.ChannelID {
		return apperr.Wrap(apperr.CodeStoreConstraint, ErrForwardTargetLoop)
	}
	if _, err := s.st.GetWatchSource(ctx, targetChannelID); err == nil {
		return apperr.Wrap(apperr.CodeStoreConstraint,
			fmt.Errorf("%w：该频道已是监听源", ErrForwardTargetLoop))
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if _, conflict := s.dumpChannelConflict(ctx, targetChannelID); conflict {
		return apperr.Wrap(apperr.CodeStoreConstraint,
			fmt.Errorf("%w：该频道是缓存频道", ErrForwardTargetLoop))
	}
	return nil
}

// globalForwardConflict 判定频道是否为已配置的全局监听转发目标（源创建
// 路径的循环防护另一侧）。命中返回展示标题与 true。
func (s *Service) globalForwardConflict(ctx context.Context, channelID int64) (string, bool) {
	for _, f := range syscfg.LoadWatchForwardChannels(ctx, s.st) {
		if f.ChannelID == channelID {
			title := f.Title
			if title == "" {
				title = fmt.Sprintf("%d", f.ChannelID)
			}
			return title, true
		}
	}
	return "", false
}

// sourceChannelBlockedAsTarget 报告频道是否已被用作转发目标（专属目标或
// 全局镜像），源创建/激活路径据此拒绝，维持「监听源不得成为转发目标」
// 的双向不变量。命中时返回受控错误。
func (s *Service) sourceChannelBlockedAsTarget(ctx context.Context, channelID int64) error {
	if title, ok := s.globalForwardConflict(ctx, channelID); ok {
		return fmt.Errorf("%w：频道「%s」（%d）已配置为全局监听转发目标；请先在「监听设置」移除，或换一个频道",
			ErrForwardTargetConflict, title, channelID)
	}
	inUse, err := s.st.ForwardTargetInUse(ctx, channelID)
	if err != nil {
		return err
	}
	if inUse {
		return fmt.Errorf("%w：频道（%d）已被选为其他监听源的专属转发目标；请换一个频道", ErrForwardTargetConflict, channelID)
	}
	return nil
}
