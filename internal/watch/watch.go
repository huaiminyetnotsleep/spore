// watch 包 — 监听源预热缓存（/watch）：管理端配置或用户申请的源频道/
// 超级群组，bot 以管理员身份接收新帖后由 listener 自动转储缓存频道预热
// dump_entries（重复链接直接命中复用）。本包只管"源的生命周期"：
// 申请（可审批/免审批/关闭，syscfg.WatchConfig）、上限校验、审批、
// 移除与列表；消息接收在 botapi，转储在 listener。
//
// 与 joinmgr 同构（申请→审批→生效 + Web 共用服务）；与 binding.Bind 的
// 校验同源（解析目标 → GetChat → 权限），但放宽 chat 类型（频道与超级
// 群组都可作为源）并要求 bot 是管理员——管理员身份绕过群组 privacy
// mode，保证能看到全部消息（成员身份 + privacy on 会静默收不到，
// 配置期就应拦下）。
package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// verifyTimeout 是一次源校验（GetChat + GetChatMember）的时间窗。
const verifyTimeout = 20 * time.Second

// Notifier 向用户私聊发送审批结果通知（joinmgr.SenderNotifier 结构性满足
// 本接口；nil 表示不通知）。
type Notifier interface {
	NotifyUser(ctx context.Context, userID int64, text string) error
}

// Service 管理监听源生命周期。Bot 客户端与通知通道在装配层 Bot 就绪后
// 经 SetBots / SetNotifier 注入（与 binding / joinmgr 同款模式）。
type Service struct {
	st  *store.Store
	log *slog.Logger
	now func() time.Time

	botMu sync.RWMutex
	bots  []*tgbot.Bot

	notifyMu sync.RWMutex
	notify   Notifier
}

// Options 构造参数；Store 必填。
type Options struct {
	Store *store.Store
	Log   *slog.Logger
	Now   func() time.Time
}

// New 创建服务。
func New(opt Options) (*Service, error) {
	if opt.Store == nil {
		return nil, errors.New("watch: Store 为必填项")
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Service{st: opt.Store, log: opt.Log, now: opt.Now}, nil
}

// SetBots 注入 Bot 客户端列表（主 bot 在前）。
func (s *Service) SetBots(bots []*tgbot.Bot) {
	s.botMu.Lock()
	defer s.botMu.Unlock()
	s.bots = bots
}

// SetNotifier 注入审批结果通知通道。
func (s *Service) SetNotifier(n Notifier) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.notify = n
}

func (s *Service) currentBots() []*tgbot.Bot {
	s.botMu.RLock()
	defer s.botMu.RUnlock()
	return s.bots
}

// SubmitOutcomeKind 是 /watch 提交的业务结果枚举（botapi 据此生成文案）。
type SubmitOutcomeKind string

const (
	SubmitDisabled       SubmitOutcomeKind = "disabled"         // 功能未开放（apply_enabled=false）
	SubmitUserNotAllowed SubmitOutcomeKind = "user_not_allowed" // 用户未 /start 通过（非 enabled）
	SubmitInvalid        SubmitOutcomeKind = "invalid"          // 目标无法解析或不可访问
	SubmitNotAdmin       SubmitOutcomeKind = "not_admin"        // bot 不是该聊天管理员
	SubmitAlreadyMine    SubmitOutcomeKind = "already_mine"     // 本人已添加（幂等，刷新资料）
	SubmitAlreadyOthers  SubmitOutcomeKind = "already_others"   // 已由他人添加
	SubmitUserLimit      SubmitOutcomeKind = "user_limit"       // 超出每用户申请上限
	SubmitSourceLimit    SubmitOutcomeKind = "source_limit"     // 超出监听源总数上限
	SubmitPending        SubmitOutcomeKind = "pending"          // 已提交待审批
	SubmitActive         SubmitOutcomeKind = "active"           // 已生效（免审批/号主/管理员路径）
)

// SubmitOutcome 描述一次 /watch 提交的结果。
type SubmitOutcome struct {
	Kind    SubmitOutcomeKind
	Title   string            // 源标题（成功/幂等路径填充，供文案）
	Count   int               // 触发上限时的现有数量（上限提示用）
	Limit   int               // 触发上限时生效的上限值
	Channel store.WatchSource // 终态行（成功/幂等路径）
}

// Submit 处理用户 /watch 申请：用户准入（enabled）→ 配置开关 → 解析与
// 权限校验（bot 须为源管理员）→ 查重 → 上限 → 落库（按配置 pending 或
// 直接 approved；号主免审批且不受上限约束）。botID/botUsername 为受理
// bot 快照（多机器人池：申请经哪个 bot 提交，与 requests.bot_id 同语义；
// 展示自持，bot 移出池后历史仍可读）。幂等：本人重复提交同一源刷新资料
// 并按当前配置重定状态。
func (s *Service) Submit(ctx context.Context, userID int64, isOwner bool, target string, botID int64, botUsername string) (SubmitOutcome, error) {
	cfg := syscfg.LoadWatchConfig(ctx, s.st)
	if !cfg.ApplyEnabled && !isOwner {
		return SubmitOutcome{Kind: SubmitDisabled}, nil
	}
	// 准入：只有 /start 通过（enabled）的用户才能使用；号主豁免。
	if !isOwner {
		u, err := s.st.GetUser(ctx, userID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			return SubmitOutcome{Kind: SubmitUserNotAllowed}, nil
		case err != nil:
			return SubmitOutcome{}, err
		case u.Status != store.UserEnabled:
			return SubmitOutcome{Kind: SubmitUserNotAllowed}, nil
		}
	}
	chat, err := s.verifyTarget(ctx, target)
	if err != nil {
		switch apperr.From(err).Code {
		case apperr.CodeChannelTargetInvalid:
			return SubmitOutcome{Kind: SubmitInvalid}, nil
		case apperr.CodeChannelNotPostable:
			return SubmitOutcome{Kind: SubmitNotAdmin}, nil
		}
		return SubmitOutcome{}, err
	}

	// 查重：本人幂等刷新；他人（含管理员）已添加则拒绝。
	existing, err := s.st.GetWatchSource(ctx, chat.ID)
	switch {
	case err == nil && existing.AddedBy != userID:
		return SubmitOutcome{Kind: SubmitAlreadyOthers, Title: existing.Title}, nil
	case err == nil:
		// 本人重复提交：幂等，跳过上限校验
	case errors.Is(err, store.ErrNotFound):
		if !isOwner {
			if cfg.PerUserLimit > 0 {
				if n, cerr := s.st.CountWatchSourcesByUser(ctx, userID,
					[]string{store.WatchPending, store.WatchApproved}); cerr != nil {
					return SubmitOutcome{}, cerr
				} else if n >= cfg.PerUserLimit {
					return SubmitOutcome{Kind: SubmitUserLimit, Count: n, Limit: cfg.PerUserLimit}, nil
				}
			}
			if cfg.MaxSources > 0 {
				if n, cerr := s.st.CountWatchSources(ctx,
					[]string{store.WatchPending, store.WatchApproved}); cerr != nil {
					return SubmitOutcome{}, cerr
				} else if n >= cfg.MaxSources {
					return SubmitOutcome{Kind: SubmitSourceLimit, Count: n, Limit: cfg.MaxSources}, nil
				}
			}
		}
	case err != nil:
		return SubmitOutcome{}, err
	}

	status := store.WatchApproved
	if !isOwner && cfg.RequireApproval {
		status = store.WatchPending
	}
	row, err := s.st.UpsertWatchSource(ctx, store.WatchSource{
		ChannelID:   chat.ID,
		Username:    chat.Username,
		Title:       chat.Title,
		Status:      status,
		Enabled:     true,
		AddedBy:     userID,
		BotID:       botID,
		BotUsername: botUsername,
	})
	if err != nil {
		return SubmitOutcome{}, err
	}
	kind := SubmitActive
	if status == store.WatchPending {
		kind = SubmitPending
	}
	s.log.Info("监听源已提交", "channel_id", chat.ID, "user_id", userID, "status", status)
	return SubmitOutcome{Kind: kind, Title: chat.Title, Channel: row}, nil
}

// AdminAdd 管理员 Web 直接添加：不做用户准入/上限/审批（天然 approved）。
// enabled 供管理员以停用态预录入。
func (s *Service) AdminAdd(ctx context.Context, actor, target string, enabled bool) (store.WatchSource, error) {
	chat, err := s.verifyTarget(ctx, target)
	if err != nil {
		return store.WatchSource{}, err
	}
	row, err := s.st.UpsertWatchSource(ctx, store.WatchSource{
		ChannelID:  chat.ID,
		Kind:       string(chat.Type),
		Username:   chat.Username,
		Title:      chat.Title,
		Status:     store.WatchApproved,
		Enabled:    enabled,
		AddedBy:    0,
		ReviewedBy: actor,
	})
	if err != nil {
		return store.WatchSource{}, err
	}
	s.log.Info("监听源已由管理员添加", "channel_id", chat.ID, "actor", actor)
	return row, nil
}

// verifyTarget 解析并校验源目标：频道或超级群组，且 bot（主 bot 校验）
// 须为其管理员/创建者。返回校验后的 ChatFullInfo。
func (s *Service) verifyTarget(ctx context.Context, target string) (models.ChatFullInfo, error) {
	tgt, err := binding.ParseChannelTarget(target)
	if err != nil {
		return models.ChatFullInfo{}, err
	}
	bots := s.currentBots()
	if len(bots) == 0 {
		return models.ChatFullInfo{}, apperr.New(apperr.CodeInternal, "Bot 客户端尚未就绪，请稍后重试")
	}
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	chat, err := bots[0].GetChat(vctx, tgt.ChatParams())
	if err != nil {
		// bot 看不见目标聊天 = 不在其中，统一按"需先拉 bot 为管理员"提示
		return models.ChatFullInfo{}, apperr.Wrap(apperr.CodeChannelNotPostable, err)
	}
	if chat.Type != models.ChatTypeChannel && chat.Type != models.ChatTypeSupergroup {
		return models.ChatFullInfo{}, apperr.New(apperr.CodeChannelTargetInvalid,
			fmt.Sprintf("目标不是频道或超级群组（type=%s）", chat.Type))
	}
	botID := bots[0].ID()
	member, err := bots[0].GetChatMember(vctx, &tgbot.GetChatMemberParams{ChatID: chat.ID, UserID: botID})
	if err != nil {
		return models.ChatFullInfo{}, apperr.Wrap(apperr.CodeChannelNotPostable, err)
	}
	switch member.Type {
	case models.ChatMemberTypeOwner, models.ChatMemberTypeAdministrator:
		return *chat, nil
	}
	// 管理员身份绕过群组 privacy mode，保证能收到全部消息；成员身份在
	// privacy on 下会静默收不到，配置期即拒绝。
	return models.ChatFullInfo{}, apperr.New(apperr.CodeChannelNotPostable,
		"机器人不是该聊天的管理员（请先把我加为频道/群管理员）")
}

// Unwatch 用户移除自己的监听源（任意状态）；目标不存在或不属于该用户
// 返回 store.ErrNotFound。号主可移除任意源。
func (s *Service) Unwatch(ctx context.Context, userID int64, isOwner bool, target string) (store.WatchSource, error) {
	channelID, err := s.resolveTarget(ctx, target)
	if err != nil {
		return store.WatchSource{}, err
	}
	row, err := s.st.GetWatchSource(ctx, channelID)
	if err != nil {
		return store.WatchSource{}, err
	}
	if !isOwner && row.AddedBy != userID {
		return store.WatchSource{}, store.ErrNotFound
	}
	removed, err := s.st.DeleteWatchSource(ctx, channelID)
	if err != nil {
		return store.WatchSource{}, err
	}
	s.log.Info("监听源已移除", "channel_id", channelID, "user_id", userID)
	return removed, nil
}

// resolveTarget 把目标标识解析为频道数字 ID（username 形态需 GetChat）。
func (s *Service) resolveTarget(ctx context.Context, target string) (int64, error) {
	tgt, err := binding.ParseChannelTarget(target)
	if err != nil {
		return 0, err
	}
	if tgt.ChannelID != 0 {
		return tgt.ChannelID, nil
	}
	bots := s.currentBots()
	if len(bots) == 0 {
		return 0, apperr.New(apperr.CodeInternal, "Bot 客户端尚未就绪，请稍后重试")
	}
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	chat, err := bots[0].GetChat(vctx, tgt.ChatParams())
	if err != nil {
		return 0, apperr.Wrap(apperr.CodeChannelNotPostable, err)
	}
	return chat.ID, nil
}

// ListByUser 返回该用户名下的监听源（Bot /watch 无参数列表）。
func (s *Service) ListByUser(ctx context.Context, userID int64) ([]store.WatchSource, error) {
	return s.st.ListWatchSourcesByUser(ctx, userID)
}

// SourceView 是 Web 管理端列表行：源 + 申请人资料 + 预热统计（健康度
// 展示——最近预热时间停更意味着监听可能已静默失效，如 bot 被移出源
// 管理员）。
type SourceView struct {
	store.WatchSourceWithUser
	// PrewarmCount 是该源累计写入缓存频道的条目数（双键合并计数）。
	PrewarmCount int `json:"prewarm_count"`
	// PrewarmLastAt 是最近一次预热时间（0 = 从未预热）。
	PrewarmLastAt int64 `json:"prewarm_last_at"`
}

// ListAll 返回全部监听源（含申请人资料与预热统计，Web 管理端列表）。
func (s *Service) ListAll(ctx context.Context) ([]SourceView, error) {
	rows, err := s.st.ListWatchSourcesWithUser(ctx)
	if err != nil {
		return nil, err
	}
	// 双键（数字键 + username 归一小写）一次聚合查询；键缺失（私有源
	// username 为空）跳过
	keys := make([]string, 0, len(rows)*2)
	for _, r := range rows {
		keys = append(keys, strconv.FormatInt(r.ChannelID, 10))
		if u := strings.ToLower(r.Username); u != "" {
			keys = append(keys, u)
		}
	}
	stats, err := s.st.DumpEntryStatsByKeys(ctx, keys)
	if err != nil {
		// 统计失败不拖垮列表：计数归零展示，只记日志
		s.log.Warn("监听源预热统计查询失败", "error", err.Error())
		stats = nil
	}
	out := make([]SourceView, 0, len(rows))
	for _, r := range rows {
		view := SourceView{WatchSourceWithUser: r}
		if stats != nil {
			if st, ok := stats[strconv.FormatInt(r.ChannelID, 10)]; ok {
				view.PrewarmCount += st.Count
				view.PrewarmLastAt = max64(view.PrewarmLastAt, st.LastAt)
			}
			if u := strings.ToLower(r.Username); u != "" {
				if st, ok := stats[u]; ok {
					view.PrewarmCount += st.Count
					view.PrewarmLastAt = max64(view.PrewarmLastAt, st.LastAt)
				}
			}
		}
		out = append(out, view)
	}
	return out, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// LeaveOutcome 描述一次"移出 Bot"的结果。
type LeaveOutcome struct {
	Row    store.WatchSource `json:"source"`
	Left   int               `json:"left"`   // 成功退出的 bot 数
	Failed int               `json:"failed"` // 退出失败（不在源内等）的 bot 数
}

// Leave 把池内全部 bot 逐个退出源聊天（leaveChat：频道即放弃管理员，
// 超级群即退群）并删除监听源记录。逐 bot 尽力而为：不在该源的 bot 报错
// 只计数不中断；行删除始终执行（管理员意图即"不再监听 + bot 离开"）。
func (s *Service) Leave(ctx context.Context, channelID int64) (LeaveOutcome, error) {
	row, err := s.st.GetWatchSource(ctx, channelID)
	if err != nil {
		return LeaveOutcome{}, err
	}
	out := LeaveOutcome{Row: row}
	bots := s.currentBots()
	if len(bots) == 0 {
		return LeaveOutcome{}, apperr.New(apperr.CodeInternal, "Bot 客户端尚未就绪，请稍后重试")
	}
	lctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	for _, b := range bots {
		if _, err := b.LeaveChat(lctx, &tgbot.LeaveChatParams{ChatID: channelID}); err != nil {
			// 不在该源/网络瞬时失败：计数并继续，不阻断其余 bot 与行删除
			s.log.Info("监听源移出 bot 失败（继续）",
				"bot_id", b.ID(), "channel_id", channelID, "error", err.Error())
			out.Failed++
			continue
		}
		out.Left++
	}
	removed, err := s.st.DeleteWatchSource(ctx, channelID)
	if err != nil {
		return LeaveOutcome{}, err
	}
	out.Row = removed
	s.log.Info("监听源已移出 bot 并删除", "channel_id", channelID,
		"left", out.Left, "failed", out.Failed)
	return out, nil
}

// Approve 同意待审批申请（pending → approved）并通知申请人。
func (s *Service) Approve(ctx context.Context, actor string, channelID int64) (store.WatchSource, error) {
	row, err := s.st.ReviewWatchSource(ctx, channelID, store.WatchApproved, actor)
	if err != nil {
		return store.WatchSource{}, err
	}
	s.notifyUser(ctx, row.AddedBy, fmt.Sprintf(
		"你申请的监听源「%s」已通过，新消息会自动预热缓存频道。", row.Title))
	s.log.Info("监听源申请已通过", "channel_id", channelID, "actor", actor)
	return row, nil
}

// Reject 拒绝待审批申请（pending → rejected，行保留供审计）并通知申请人。
func (s *Service) Reject(ctx context.Context, actor string, channelID int64) (store.WatchSource, error) {
	row, err := s.st.ReviewWatchSource(ctx, channelID, store.WatchRejected, actor)
	if err != nil {
		return store.WatchSource{}, err
	}
	s.notifyUser(ctx, row.AddedBy, fmt.Sprintf(
		"你申请的监听源「%s」未通过审核。", row.Title))
	s.log.Info("监听源申请已拒绝", "channel_id", channelID, "actor", actor)
	return row, nil
}

// SetEnabled 切换 approved 行的暂停开关。
func (s *Service) SetEnabled(ctx context.Context, channelID int64, enabled bool) (store.WatchSource, error) {
	return s.st.SetWatchSourceEnabled(ctx, channelID, enabled)
}

// Delete 管理端删除任意状态的行（含 pending，与 joinmgr 的终态限制不同：
// 监听源删除即停止监听，无外部副作用需要保护）。
func (s *Service) Delete(ctx context.Context, channelID int64) (store.WatchSource, error) {
	return s.st.DeleteWatchSource(ctx, channelID)
}

// notifyUser 尽力而为发送通知；失败只记日志（审批结论已落库，不因通知
// 失败回滚）。仅 pending 来源的行才需要通知（管理员添加 added_by=0 跳过）。
func (s *Service) notifyUser(ctx context.Context, userID int64, text string) {
	if userID == 0 {
		return
	}
	s.notifyMu.RLock()
	n := s.notify
	s.notifyMu.RUnlock()
	if n == nil {
		return
	}
	nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := n.NotifyUser(nctx, userID, text); err != nil {
		s.log.Warn("监听源审批通知发送失败", "user_id", userID, "error", err.Error())
	}
}

// EventsQuery 是监听记录列表查询（Web「监听记录」页）。
type EventsQuery struct {
	ChannelID int64 // 0 = 全部源
	Page      int
	PageSize  int
}

// EventView 是监听记录列表行：事件 + 规范化源消息链接（公开源
// t.me/<username>/<id>，私有源 t.me/c/<内部ID>/<id>，与 requests 的
// message_url 同一归一规则）。
type EventView struct {
	store.WatchEvent
	MessageURL string `json:"message_url"`
}

// ListEvents 按查询返回预热事件页（id 倒序）与总数。
func (s *Service) ListEvents(ctx context.Context, q EventsQuery) ([]EventView, int, error) {
	if q.PageSize <= 0 || q.PageSize > 100 {
		q.PageSize = 20
	}
	rows, total, err := s.st.ListWatchEvents(ctx, store.WatchEventsQuery{
		ChannelID: q.ChannelID, Page: q.Page, PageSize: q.PageSize,
	})
	if err != nil {
		return nil, 0, err
	}
	out := make([]EventView, 0, len(rows))
	for _, e := range rows {
		out = append(out, EventView{WatchEvent: e, MessageURL: eventMessageURL(e)})
	}
	return out, total, nil
}

// eventMessageURL 渲染事件的源消息链接：优先 username（公开源），否则
// channel_id 去 -100 前缀转内部 ID（私有源同 requests 归一）。
func eventMessageURL(e store.WatchEvent) string {
	ref := tmeurl.SourceRef{MessageID: e.MessageID}
	if e.Username != "" {
		ref.Kind = tmeurl.PeerUsername
		ref.Username = strings.ToLower(e.Username)
	} else {
		raw := strings.TrimPrefix(strconv.FormatInt(e.ChannelID, 10), "-100")
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return ""
		}
		ref.Kind = tmeurl.PeerChannelID
		ref.ChannelID = id
	}
	link, _ := ref.URL()
	return link
}

// WatchStatsView 是业务统计页监听模块的完整视图：源状态计数（当前态，
// 不随时间界过滤）、按源/按 bot/按用户聚合与按日趋势（均受时间界与
// bot 界约束；趋势升序、缺日由 Web 层补零）。
type WatchStatsView struct {
	Sources  store.WatchStatusCounts `json:"sources"`
	BySource []store.WatchSourceStat `json:"by_source"`
	ByBot    []store.WatchBotStat    `json:"by_bot"`
	ByUser   []store.WatchUserStat   `json:"by_user"`
	Trend    []store.WatchTrendPoint `json:"trend"`
}

// StatsQuery 是监听统计查询（时间/bot 界与 store.StatsFilter 同语义）。
type StatsQuery struct {
	Since int64 // Unix 毫秒；0 = 开放
	Until int64
	BotID int64 // 0 = 全部
}

// Stats 聚合监听模块统计（utcOffsetSec 为运营时区偏移秒，趋势日界与
// 业务统计的请求趋势同口径）。
func (s *Service) Stats(ctx context.Context, q StatsQuery, utcOffsetSec int64) (WatchStatsView, error) {
	var v WatchStatsView
	var err error
	if v.Sources, err = s.st.CountWatchSourcesByStatus(ctx); err != nil {
		return v, err
	}
	if v.BySource, err = s.st.WatchStatsBySource(ctx, q.Since, q.Until, q.BotID); err != nil {
		return v, err
	}
	if v.ByBot, err = s.st.WatchStatsByBot(ctx, q.Since, q.Until); err != nil {
		return v, err
	}
	if v.ByUser, err = s.st.WatchStatsByUser(ctx, q.Since, q.Until, q.BotID); err != nil {
		return v, err
	}
	if v.Trend, err = s.st.ListWatchTrend(ctx, q.Since, q.Until, utcOffsetSec); err != nil {
		return v, err
	}
	return v, nil
}
