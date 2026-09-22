// Package binding 实现用户频道绑定的应用服务：
// 用户把机器人拉进自己的频道并设为管理员后，经 Bot /bind 指令或 Web 管理端
// 绑定频道；worker 把任务结果复制到"任务所属用户"绑定的频道。
// 校验（Bot 必须是该频道管理员且有发言权限）、归属（同一频道只归属一个
// 用户）与审计都收敛在本服务，Bot 侧与 Web 侧共用同一入口。
package binding

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
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// verifyTimeout 是绑定校验（GetChat + GetChatMember）的时间窗。
const verifyTimeout = 15 * time.Second

// channelRefreshTTL 是发送路径构建脚注链接时刷新频道信息（GetChat）的
// 最小间隔，避免每条消息都打 Bot API；/channels 走强制刷新不受此限制。
const channelRefreshTTL = 10 * time.Minute

// refreshTimeout 是单次频道信息刷新（GetChat）的时间窗。
const refreshTimeout = 10 * time.Second

// Options 聚合 Service 依赖。
type Options struct {
	Store *store.Store
	Log   *slog.Logger
}

// Service 是频道绑定应用服务。Bot 客户端在装配层 Bot 就绪后经 SetBots 注入
// （Bot 构造依赖长轮询链路，无法在 New 时给出），重复注入无害（重连重装配）。
// 多机器人池：用户绑定按 bot 逐台判定（硬校验只卡接收请求的 bot，其余未
// 就绪仅点名提示，2026-09-22 由"全部达标才可绑定"的一刀切放宽——每个用户
// 实际固定使用少数 bot，全 bot 达标是过重负担）；缓存频道校验仍对全部 bot
// 执行（部署级共享目标）。副本投递按受理 bot 执行（消息坐标是 bot 私有的，
// 跨 bot 不可复制）。
type Service struct {
	store *store.Store
	log   *slog.Logger

	botMu sync.RWMutex
	bots  []*tgbot.Bot // 装配顺序，主 bot（首项）承担元数据刷新等单通道操作

	// refreshAt 记录各频道上次信息刷新时间（channelRefreshTTL 节流）。
	refreshMu sync.Mutex
	refreshAt map[int64]time.Time
}

// New 创建服务。Store 必填。
func New(opt Options) (*Service, error) {
	if opt.Store == nil {
		return nil, errors.New("binding: Store 为必填项")
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	return &Service{store: opt.Store, log: opt.Log, refreshAt: map[int64]time.Time{}}, nil
}

// SetBots 注入 Bot 客户端列表（多机器人池；主 bot 在前，单 bot 部署长度为 1）。
func (s *Service) SetBots(bots []*tgbot.Bot) {
	s.botMu.Lock()
	defer s.botMu.Unlock()
	s.bots = bots
}

// currentBots 返回当前 bot 客户端列表快照（可能为空：Bot 尚未就绪）。
func (s *Service) currentBots() []*tgbot.Bot {
	s.botMu.RLock()
	defer s.botMu.RUnlock()
	return s.bots
}

// currentBot 返回主 bot（首项）；池为空返回 nil。
func (s *Service) currentBot() *tgbot.Bot {
	s.botMu.RLock()
	defer s.botMu.RUnlock()
	if len(s.bots) == 0 {
		return nil
	}
	return s.bots[0]
}

// botFor 返回受理 bot 对应的客户端；未命中（已下线/存量任务）回退主 bot。
func (s *Service) botFor(botID int64) *tgbot.Bot {
	s.botMu.RLock()
	defer s.botMu.RUnlock()
	if len(s.bots) == 0 {
		return nil
	}
	if botID != 0 {
		for _, b := range s.bots {
			if b.ID() == botID {
				return b
			}
		}
	}
	return s.bots[0]
}

// verifyAllBotsCanPost 对池内全部 bot 逐一校验频道发帖权限；任一 bot 失败
// 即整体失败（错误带 bot id，定位需要在哪个频道补设管理员）。仅缓存频道
// 配置（VerifyChannel）使用：缓存频道是部署级共享目标，任何受理 bot 都可能
// 写入或复用副本，缺一不可。用户绑定走逐 bot 判定（BindWithAdvice）。
func verifyAllBotsCanPost(ctx context.Context, bots []*tgbot.Bot, chatID int64) error {
	for _, b := range bots {
		if err := verifyBotCanPost(ctx, b, b.ID(), chatID); err != nil {
			return apperr.Wrap(apperr.CodeChannelNotPostable,
				fmt.Errorf("机器人 %d 无法在频道 %d 发帖（需全部机器人设为频道管理员）: %w",
					b.ID(), chatID, err))
		}
	}
	return nil
}

// BindInput 描述一次绑定请求。
type BindInput struct {
	UserID int64  // 绑定归属用户（Telegram 用户 ID）
	Target string // 频道标识：@username / t.me 链接 / -100… 频道 ID
	Via    string // store.BoundViaBot | store.BoundViaWeb
	// BotID 是接收绑定请求的 bot（Bot 指令路径）；0 = Web 路径，硬校验
	// 回退主 bot。只有这台 bot 不达标才拒绝绑定，其余 bot 未就绪仅点名提示。
	BotID int64
}

// Bind 校验并写入绑定：解析目标 → 确认接收 bot 有权限（逐 bot 判定）→
// 归属查重（他人已绑定则拒绝）→ 落库 → 审计。不消费逐 bot 提示的调用方
// （Web 管理端）用这个入口。
func (s *Service) Bind(ctx context.Context, in BindInput) (store.ChannelBinding, error) {
	bound, _, err := s.BindWithAdvice(ctx, in)
	return bound, err
}

// BindWithAdvice 在 Bind 之上返回其余 bot 的未就绪提示（advice，可为空）。
// 绑定校验按 bot 逐台判定：只有接收请求的 bot（in.BotID，Web 路径回退主
// bot）不达标才拒绝——它不达标意味着用户当前使用的入口立即可见地不可用；
// 其余 bot 只探测、不拦截，未就绪者在提示里点名（副本/置顶由受理 bot 执行，
// 缺权限只影响它受理的那部分任务，运行时已有兜底：副本失败仅记日志不伤
// 任务结果，置顶失败在完工确认里逐目标可见）。
func (s *Service) BindWithAdvice(ctx context.Context, in BindInput) (store.ChannelBinding, string, error) {
	if in.UserID <= 0 {
		return store.ChannelBinding{}, "", apperr.New(apperr.CodeInternal, "频道绑定必须归属一个用户")
	}
	tgt, err := ParseChannelTarget(in.Target)
	if err != nil {
		return store.ChannelBinding{}, "", err
	}
	bots := s.currentBots()
	if len(bots) == 0 {
		return store.ChannelBinding{}, "", apperr.New(apperr.CodeInternal, "Bot 客户端尚未就绪，请稍后重试")
	}

	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	chat, err := bots[0].GetChat(vctx, tgt.ChatParams())
	if err != nil {
		// Bot 看不见目标聊天基本等于"不在该频道/无权限"，统一归类提示用户
		// 先把机器人拉进频道设为管理员
		return store.ChannelBinding{}, "", apperr.Wrap(apperr.CodeChannelNotPostable, err)
	}
	// 类型门槛：频道与超级群组可绑定（副本 + 置顶的落点）；普通群、话题群
	// 与其他类型一律拒绝——话题群的副本/置顶需要按话题路由，本期不支持。
	switch chat.Type {
	case models.ChatTypeChannel, models.ChatTypeSupergroup:
	default:
		return store.ChannelBinding{}, "", apperr.New(apperr.CodeChannelTargetInvalid,
			fmt.Sprintf("目标不是频道或超级群组（type=%s）", chat.Type))
	}
	if chat.IsForum {
		return store.ChannelBinding{}, "", apperr.New(apperr.CodeChannelTargetInvalid,
			"话题群（论坛）暂不支持绑定")
	}
	// 硬校验只针对接收请求的 bot。权限语义按类型区分——频道校验发帖权限
	// （can_post_messages）；超级群组校验置顶权限（can_pin_messages，
	// can_post_messages 是频道作用域权限，群管理员通常为 false）。
	hard := s.botFor(in.BotID)
	if chat.Type == models.ChatTypeSupergroup {
		if err := verifyBotCanPin(vctx, hard, hard.ID(), chat.ID); err != nil {
			return store.ChannelBinding{}, "", apperr.Wrap(apperr.CodeChannelNotPinnable,
				fmt.Errorf("机器人 %d 无法在群组 %d 置顶（需设为管理员并授予「置顶消息」权限）: %w",
					hard.ID(), chat.ID, err))
		}
	} else if err := verifyBotCanPost(vctx, hard, hard.ID(), chat.ID); err != nil {
		return store.ChannelBinding{}, "", apperr.Wrap(apperr.CodeChannelNotPostable,
			fmt.Errorf("机器人 %d 无法在频道 %d 发帖（需设为频道管理员并有发帖权限）: %w",
				hard.ID(), chat.ID, err))
	}
	advice := botReadinessAdvice(vctx, bots, hard, chat)
	// 路由归属：Bot 路径记录接收命令的 bot（硬校验对象，绑定经它建立即
	// 由它投递）；Web 路径（in.BotID=0）存 0 通配，任意受理 bot 均尝试
	// 投递。若配置的 bot 已不在池中（botFor 回退主 bot），按实际校验的
	// bot 记录，避免路由指向不存在的 bot。
	routeBot := int64(0)
	if in.BotID > 0 {
		routeBot = hard.ID()
	}

	// 归属预检与上限校验：本人重绑为幂等更新不受限；新绑定受数量上限约束。
	// 他人已绑定时给出明确的业务拒绝，而不是存储约束的笼统文案。
	existing, err := s.store.GetChannelBinding(ctx, chat.ID)
	switch {
	case err == nil && existing.UserID != in.UserID:
		return store.ChannelBinding{}, "", apperr.New(apperr.CodeChannelAlreadyBound, "")
	case errors.Is(err, store.ErrNotFound):
		if s.bindLimitReached(ctx, in.UserID) {
			return store.ChannelBinding{}, "", apperr.New(apperr.CodeChannelBindLimit, "")
		}
	case err != nil:
		return store.ChannelBinding{}, "", err
	}

	bound, err := s.store.UpsertChannelBinding(ctx, store.ChannelBinding{
		ChannelID: chat.ID,
		UserID:    in.UserID,
		Username:  chat.Username,
		Title:     chat.Title,
		BoundVia:  in.Via,
		BotID:     routeBot,
	})
	if err != nil {
		// 并发窗口下仍可能撞约束（他人恰好先绑定）；FK 约束（用户不存在）
		// 也归入此码，由调用方的用户校验兜底，不会误伤正常路径
		if apperr.From(err).Code == apperr.CodeStoreConstraint {
			return store.ChannelBinding{}, "", apperr.New(apperr.CodeChannelAlreadyBound, "")
		}
		return store.ChannelBinding{}, "", err
	}

	_ = s.store.AppendAudit(ctx, store.AuditEntry{
		Actor:  actorOf(in.Via, in.UserID),
		Action: "channel.bind",
		Target: fmt.Sprintf("channel:%d", bound.ChannelID),
		AfterJSON: fmt.Sprintf(`{"user_id":%d,"username":%q,"title":%q,"via":%q}`,
			bound.UserID, bound.Username, bound.Title, bound.BoundVia),
	})
	s.log.Info("频道已绑定", "user_id", in.UserID, "channel_id", bound.ChannelID, "via", in.Via)
	return bound, advice, nil
}

// BindBot 是 Bot /bind 指令路径的便捷入口：等价于 Via=bot 的 BindWithAdvice。
// botID 是接收命令的 bot（硬校验对象）；advice 是其余 bot 的未就绪点名提示。
func (s *Service) BindBot(ctx context.Context, userID int64, target string, botID int64) (store.ChannelBinding, string, error) {
	return s.BindWithAdvice(ctx, BindInput{UserID: userID, Target: target, Via: store.BoundViaBot, BotID: botID})
}

// UnbindBot 是 Bot /unbind 指令路径的便捷入口：只允许解绑自己的绑定。
func (s *Service) UnbindBot(ctx context.Context, userID int64, target string) (store.ChannelBinding, error) {
	return s.Unbind(ctx, userID, target, false)
}

// Unbind 解除绑定。userID > 0 且 anyOwner=false 时只允许解绑自己的绑定
// （Bot 指令路径）；anyOwner=true（Web 管理端）可解绑任意绑定。
// 目标不存在或不属于该用户返回 store.ErrNotFound。
func (s *Service) Unbind(ctx context.Context, userID int64, target string, anyOwner bool) (store.ChannelBinding, error) {
	tgt, err := ParseChannelTarget(target)
	if err != nil {
		return store.ChannelBinding{}, err
	}
	channelID, err := s.resolveChannelID(ctx, tgt, userID, anyOwner)
	if err != nil {
		return store.ChannelBinding{}, err
	}
	ownerScope := int64(0)
	if !anyOwner {
		ownerScope = userID
	}
	removed, err := s.store.DeleteChannelBinding(ctx, channelID, ownerScope)
	if err != nil {
		return store.ChannelBinding{}, err
	}
	_ = s.store.AppendAudit(ctx, store.AuditEntry{
		Actor:      actorOf(store.BoundViaWeb, userID),
		Action:     "channel.unbind",
		Target:     fmt.Sprintf("channel:%d", removed.ChannelID),
		BeforeJSON: fmt.Sprintf(`{"user_id":%d,"username":%q,"title":%q}`, removed.UserID, removed.Username, removed.Title),
	})
	s.log.Info("频道已解绑", "user_id", removed.UserID, "channel_id", removed.ChannelID, "actor_scope", ownerScope)
	return removed, nil
}

// resolveChannelID 把解析出的频道标识定位为绑定的 channel_id：
// 数字 ID 直接可用；用户名在绑定记录中匹配（解绑不需要 Bot 在线）。
func (s *Service) resolveChannelID(ctx context.Context, tgt ChannelTarget, userID int64, anyOwner bool) (int64, error) {
	if tgt.ChannelID != 0 {
		return tgt.ChannelID, nil
	}
	if anyOwner {
		rows, err := s.store.ListChannelBindingsWithUser(ctx)
		if err != nil {
			return 0, err
		}
		for _, r := range rows {
			if strings.EqualFold(r.Username, tgt.Username) {
				return r.ChannelID, nil
			}
		}
		return 0, store.ErrNotFound
	}
	rows, err := s.store.ListChannelBindingsByUser(ctx, userID)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		if strings.EqualFold(r.Username, tgt.Username) {
			return r.ChannelID, nil
		}
	}
	return 0, store.ErrNotFound
}

// ListByUser 返回该用户名下的全部绑定（Bot /channels 数据源）。
// 返回前强制刷新频道信息（/channels 是低频交互命令），保证频道转私有、
// 转公开或改名后用户看到的是实时状态。
func (s *Service) ListByUser(ctx context.Context, userID int64) ([]store.ChannelBinding, error) {
	rows, err := s.store.ListChannelBindingsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	return s.refreshBindings(ctx, rows, true), nil
}

// ListAll 返回全部绑定（含所属用户资料），供 Web 管理端展示。
func (s *Service) ListAll(ctx context.Context) ([]store.ChannelBindingWithUser, error) {
	return s.store.ListChannelBindingsWithUser(ctx)
}

// ListAllByUser 返回指定用户的全部绑定（含所属用户资料），供 Web 管理端筛选。
func (s *Service) ListAllByUser(ctx context.Context, userID int64) ([]store.ChannelBindingWithUser, error) {
	return s.store.ListChannelBindingsWithUserByUser(ctx, userID)
}

// ChannelURL 返回绑定的完整跳转链接（/channels 展示与脚注共用）：
//   - 公开频道：https://t.me/<username>；
//   - 私有频道：https://t.me/c/<内部ID>/1（Bot API 频道 ID 去掉 -100 前缀
//     即内部 ID；仅频道成员可跳转）。
func ChannelURL(b store.ChannelBinding) string {
	if b.Username != "" {
		return "https://t.me/" + b.Username
	}
	return "https://t.me/c/" + strings.TrimPrefix(strconv.FormatInt(b.ChannelID, 10), "-100") + "/1"
}

// PublicChannelLinks 返回该用户绑定频道的脚注跳转链接（按绑定时间排序），
// 实现 queue.ChannelLinksProvider：公开频道展示 @username，私有频道展示
// 频道标题（缺失兜底"频道"）。构建前先尽力刷新频道信息（TTL 节流），
// 频道在绑定后转私有/转公开/改名时链接随之自动修正。
func (s *Service) PublicChannelLinks(ctx context.Context, userID int64) ([]message.ChannelLink, error) {
	rows, err := s.store.ListChannelBindingsByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows = s.refreshBindings(ctx, rows, false)
	links := make([]message.ChannelLink, 0, len(rows))
	for _, r := range rows {
		label := "@" + r.Username
		if r.Username == "" {
			label = r.Title
			if label == "" {
				label = "频道"
			}
		}
		links = append(links, message.ChannelLink{Label: label, URL: ChannelURL(r)})
	}
	return links, nil
}

// refreshBindings 尽力而为地把绑定行的 username/title 同步为 Telegram 实时
// 状态：库里的值只是绑定时的快照，频道可能在绑定后转私有（username 被清空）、
// 转回公开或改名。变化经 UpsertChannelBinding 持久化并回填列表；GetChat
// 失败只记日志、沿用旧快照，不阻塞调用方（发送路径尽力而为）。
// force=false 时按 channelRefreshTTL 节流（发送路径），force=true 每行都查
// （/channels 交互路径）。
func (s *Service) refreshBindings(ctx context.Context, rows []store.ChannelBinding, force bool) []store.ChannelBinding {
	b := s.currentBot()
	if b == nil || len(rows) == 0 {
		return rows
	}
	now := time.Now()
	for i, r := range rows {
		if !force {
			s.refreshMu.Lock()
			last, seen := s.refreshAt[r.ChannelID]
			if seen && now.Sub(last) < channelRefreshTTL {
				s.refreshMu.Unlock()
				continue
			}
			// 先占位再查询：并发任务不重复打 GetChat，失败也等下个周期
			s.refreshAt[r.ChannelID] = now
			s.refreshMu.Unlock()
		}
		rctx, cancel := context.WithTimeout(ctx, refreshTimeout)
		chat, err := b.GetChat(rctx, &tgbot.GetChatParams{ChatID: r.ChannelID})
		cancel()
		if err != nil {
			s.log.Warn("刷新频道信息失败，沿用绑定快照",
				"channel_id", r.ChannelID, "error", err.Error())
			continue
		}
		if chat.ID != r.ChannelID {
			continue
		}
		if chat.Username == r.Username && chat.Title == r.Title {
			continue
		}
		updated, err := s.store.UpsertChannelBinding(ctx, store.ChannelBinding{
			ChannelID: r.ChannelID,
			UserID:    r.UserID,
			Username:  chat.Username,
			Title:     chat.Title,
			BoundVia:  r.BoundVia,
			BotID:     r.BotID, // 刷新不改变路由归属
		})
		if err != nil {
			s.log.Warn("持久化频道信息刷新失败",
				"channel_id", r.ChannelID, "error", err.Error())
			continue
		}
		s.log.Info("频道信息已刷新", "channel_id", r.ChannelID,
			"username", chat.Username, "title", chat.Title)
		rows[i] = updated
	}
	return rows
}

// bindLimitReached 判断该用户的新绑定是否达到数量上限（用户级 bind_limit，
// 0 = 跟随角色默认；见 store.User.EffectiveBindLimit）。
// 用户/绑定读取失败时降级为不拦截（FK 与归属校验仍兜底），只记日志。
func (s *Service) bindLimitReached(ctx context.Context, userID int64) bool {
	u, err := s.store.GetUser(ctx, userID)
	if err != nil {
		s.log.Warn("读取绑定用户失败，跳过数量上限校验", "user_id", userID, "error", err.Error())
		return false
	}
	rows, err := s.store.ListChannelBindingsByUser(ctx, userID)
	if err != nil {
		s.log.Warn("读取用户绑定数失败，跳过数量上限校验", "user_id", userID, "error", err.Error())
		return false
	}
	return len(rows) >= u.EffectiveBindLimit()
}

// CopyToChannels 把已发送给用户的消息复制到该用户绑定的频道（频道副本）。
// botID 是任务的受理 bot：已发送消息坐标是该 bot 私有的（用户私聊内消息
// ID 按 bot 隔离），必须由同一 bot 执行复制；未命中回退主 bot。
// 投递按绑定路由过滤：bot_id > 0 的绑定只接受该 bot 受理的任务（绑定属于
// 其他 bot 时跳过，pin 时计入 Skipped 供确认文案提示）；bot_id = 0 通配，
// 任意受理 bot 均尝试。
// pin 为 true 时对每个副本发送成功的目标静音置顶组首消息（CopyMessages
// 保序，返回首条即对应私聊 caption 主消息的副本；客户端置顶组首会自动
// 展开展示整个相册），并返回逐目标置顶结果——Label 为目标显示名（标题
// 优先，回退 @用户名 / 数字 ID），供 worker 置顶确认文案展示。
// pin 为 false 时返回零值。
// 实现 queue.ChannelCopier；尽力而为：单频道失败只记日志，不中断其余频道，
// 更不向调用方传播错误（worker 以此保证副本不影响任务结果）。
func (s *Service) CopyToChannels(ctx context.Context, botID, userID, userChatID int64, msgIDs []int, pin bool) queue.PinOutcome {
	var outcome queue.PinOutcome
	b := s.botFor(botID)
	if b == nil || userID <= 0 || userChatID == 0 || len(msgIDs) == 0 {
		return outcome
	}
	bindings, err := s.store.ListChannelBindingsByUser(ctx, userID)
	if err != nil {
		s.log.Warn("频道副本：读取绑定失败", "user_id", userID, "error", err.Error())
		return outcome
	}
	for _, bnd := range bindings {
		if bnd.BotID != 0 && bnd.BotID != botID {
			// 路由不匹配：绑定属于其他 bot，由它受理的任务才会投递到此
			s.log.Info("频道副本跳过：绑定属于其他机器人",
				"user_id", userID, "channel_id", bnd.ChannelID,
				"binding_bot", bnd.BotID, "accepting_bot", botID)
			if pin {
				outcome.Skipped = append(outcome.Skipped, bindingLabel(bnd))
			}
			continue
		}
		if pin {
			outcome.Total++
			outcome.Targets = append(outcome.Targets, queue.PinTarget{Label: bindingLabel(bnd)})
		}
		sent, err := b.CopyMessages(ctx, &tgbot.CopyMessagesParams{
			ChatID:     bnd.ChannelID,
			FromChatID: userChatID,
			MessageIDs: msgIDs,
		})
		if err != nil {
			s.log.Warn("频道副本发送失败",
				"user_id", userID, "channel_id", bnd.ChannelID, "messages", len(msgIDs), "error", err.Error())
			continue
		}
		s.log.Info("频道副本已发送", "user_id", userID, "channel_id", bnd.ChannelID, "messages", len(msgIDs))
		if !pin || len(sent) == 0 {
			continue
		}
		if _, err := b.PinChatMessage(ctx, &tgbot.PinChatMessageParams{
			ChatID:              bnd.ChannelID,
			MessageID:           sent[0].ID,
			DisableNotification: true,
		}); err != nil {
			s.log.Warn("频道置顶失败",
				"user_id", userID, "channel_id", bnd.ChannelID, "error", err.Error())
			continue
		}
		outcome.OK++
		outcome.Targets[len(outcome.Targets)-1].Pinned = true
	}
	return outcome
}

// bindingLabel 渲染绑定目标的显示名：标题优先，回退 @用户名，
// 两者皆空（私有且无标题快照）时退化为数字 ID。
func bindingLabel(bnd store.ChannelBinding) string {
	if bnd.Title != "" {
		return bnd.Title
	}
	if bnd.Username != "" {
		return "@" + bnd.Username
	}
	return strconv.FormatInt(bnd.ChannelID, 10)
}

// PinCapabilityHint 检查该用户全部既有绑定目标的置顶可行性并返回软提示
// 文案：逐绑定 × 逐 bot 探测——频道需要「编辑消息」权限（置顶权限在频道
// 里归属它而非 can_post_messages），超级群组需要「置顶消息」权限；普通
// 成员/非管理员同样不可置顶（绑定改为逐 bot 判定后不再保证全部 bot 达标）。
// 任一 bot 在任一目标缺权限即提示。全部可行返回空串。尽力而为：查询失败
// 按可行处理，不阻塞调用方。
func (s *Service) PinCapabilityHint(ctx context.Context, userID int64) string {
	bots := s.currentBots()
	if len(bots) == 0 || userID <= 0 {
		return ""
	}
	bindings, err := s.store.ListChannelBindingsByUser(ctx, userID)
	if err != nil || len(bindings) == 0 {
		return ""
	}
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	for _, bnd := range bindings {
		for _, b := range bots {
			member, err := b.GetChatMember(vctx, &tgbot.GetChatMemberParams{ChatID: bnd.ChannelID, UserID: b.ID()})
			if err != nil {
				continue // 查询失败按可行处理，不阻塞绑定
			}
			if member.Type == models.ChatMemberTypeOwner {
				continue
			}
			if member.Type == models.ChatMemberTypeAdministrator && member.Administrator != nil &&
				(member.Administrator.CanEditMessages || member.Administrator.CanPinMessages) {
				continue
			}
			return "注意：部分绑定目标有机器人缺置顶所需权限（频道「编辑消息」/群组「置顶消息」），置顶将不可用（可解绑后重新绑定并补授权限）。"
		}
	}
	return ""
}

// VerifyChannel 解析并校验频道目标（@username / t.me 链接 / -100 数字 ID），
// 返回数字频道 ID 与标题；不落任何绑定。供 Web 管理端配置缓存频道（重复
// 链接复用）使用：与绑定同一套解析与权限校验（bot 必须在该频道可发帖）。
// 公开频道可直接用链接；私有频道（无 username）用数字 ID——频道公开转
// 私有不改 ID，已配置的数字 ID 继续有效。
func (s *Service) VerifyChannel(ctx context.Context, target string) (int64, string, error) {
	tgt, err := ParseChannelTarget(target)
	if err != nil {
		return 0, "", err
	}
	bots := s.currentBots()
	if len(bots) == 0 {
		return 0, "", apperr.New(apperr.CodeInternal, "Bot 客户端尚未就绪，请稍后重试")
	}
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	chat, err := bots[0].GetChat(vctx, tgt.ChatParams())
	if err != nil {
		return 0, "", apperr.Wrap(apperr.CodeChannelNotPostable, err)
	}
	if chat.Type != models.ChatTypeChannel {
		return 0, "", apperr.New(apperr.CodeChannelTargetInvalid,
			fmt.Sprintf("目标不是频道（type=%s）", chat.Type))
	}
	// 复用副本可能由任一受理 bot 执行复制，全部 bot 都要可发帖
	if err := verifyAllBotsCanPost(vctx, bots, chat.ID); err != nil {
		return 0, "", err
	}
	return chat.ID, chat.Title, nil
}

// verifyBotCanPost 确认机器人是该频道的创建者，或拥有发言权限的管理员。
func verifyBotCanPost(ctx context.Context, b *tgbot.Bot, botID, chatID int64) error {
	member, err := b.GetChatMember(ctx, &tgbot.GetChatMemberParams{ChatID: chatID, UserID: botID})
	if err != nil {
		return apperr.Wrap(apperr.CodeChannelNotPostable, err)
	}
	switch member.Type {
	case models.ChatMemberTypeOwner:
		return nil
	case models.ChatMemberTypeAdministrator:
		if member.Administrator != nil && member.Administrator.CanPostMessages {
			return nil
		}
	}
	return apperr.New(apperr.CodeChannelNotPostable, "")
}

// botReadinessAdvice 对硬校验对象之外的其余 bot 逐台探测目标聊天能力，
// 生成点名提示：每台未就绪 bot 一条（缺什么、影响面），全部就绪返回空串。
// 区分三类影响面：不在群（副本与置顶均不可用）、缺发帖权限（频道，副本与
// 置顶均不可用）、仅缺置顶权限（副本可用、置顶不可用）。
func botReadinessAdvice(ctx context.Context, bots []*tgbot.Bot, hard *tgbot.Bot, chat *models.ChatFullInfo) string {
	noun := "频道"
	if chat.Type == models.ChatTypeSupergroup {
		noun = "超级群组"
	}
	lines := make([]string, 0, len(bots))
	for _, b := range bots {
		if b == hard {
			continue
		}
		member, err := b.GetChatMember(ctx, &tgbot.GetChatMemberParams{ChatID: chat.ID, UserID: b.ID()})
		if err != nil {
			lines = append(lines, fmt.Sprintf("机器人 %d 无法读取成员身份（通常是不在该%s），由它受理的任务无法同步副本或置顶", b.ID(), noun))
			continue
		}
		switch member.Type {
		case models.ChatMemberTypeOwner:
			continue
		case models.ChatMemberTypeAdministrator:
			if member.Administrator == nil {
				continue // 成员数据缺失按就绪处理，不误报
			}
			if chat.Type == models.ChatTypeSupergroup {
				if !member.Administrator.CanPinMessages {
					lines = append(lines, fmt.Sprintf("机器人 %d 缺「置顶消息」权限（副本可用，置顶不可用）", b.ID()))
				}
				continue
			}
			switch {
			case !member.Administrator.CanPostMessages:
				lines = append(lines, fmt.Sprintf("机器人 %d 缺「发帖」权限（由它受理的任务无法同步副本或置顶）", b.ID()))
			case !member.Administrator.CanEditMessages:
				lines = append(lines, fmt.Sprintf("机器人 %d 缺「编辑消息」权限（副本可用，置顶不可用）", b.ID()))
			}
		default:
			// 超级群组普通成员/受限成员：能发消息（副本可用）但不能置顶；
			// 其余形态（left/kicked，及频道的非管理员）按不在群处理。
			if chat.Type == models.ChatTypeSupergroup &&
				(member.Type == models.ChatMemberTypeMember || member.Type == models.ChatMemberTypeRestricted) {
				lines = append(lines, fmt.Sprintf("机器人 %d 是普通成员、缺「置顶消息」权限（副本可用，置顶不可用）", b.ID()))
				continue
			}
			lines = append(lines, fmt.Sprintf("机器人 %d 不在该%s（由它受理的任务无法同步副本或置顶）", b.ID(), noun))
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "提醒：" + strings.Join(lines, "；") + "。"
}

// verifyBotCanPin 确认机器人是该超级群组的创建者，或拥有置顶权限的管理员。
func verifyBotCanPin(ctx context.Context, b *tgbot.Bot, botID, chatID int64) error {
	member, err := b.GetChatMember(ctx, &tgbot.GetChatMemberParams{ChatID: chatID, UserID: botID})
	if err != nil {
		return apperr.Wrap(apperr.CodeChannelNotPinnable, err)
	}
	switch member.Type {
	case models.ChatMemberTypeOwner:
		return nil
	case models.ChatMemberTypeAdministrator:
		if member.Administrator != nil && member.Administrator.CanPinMessages {
			return nil
		}
	}
	return apperr.New(apperr.CodeChannelNotPinnable, "")
}

func actorOf(via string, userID int64) string {
	if via == store.BoundViaBot {
		return fmt.Sprintf("user:%d", userID)
	}
	return "admin"
}

// ChannelTarget 是解析后的频道标识：Username（无 @）或 ChannelID
// （Bot API 的 -100… 数字 ID）。
type ChannelTarget struct {
	Username  string
	ChannelID int64
}

// ChatParams 把标识转成 GetChat 入参。
func (t ChannelTarget) ChatParams() *tgbot.GetChatParams {
	if t.ChannelID != 0 {
		return &tgbot.GetChatParams{ChatID: t.ChannelID}
	}
	return &tgbot.GetChatParams{ChatID: "@" + t.Username}
}

// ParseChannelTarget 解析频道标识（纯函数，Bot 与 Web 共用）：
//   - @username / username：Telegram 用户名形态；
//   - https://t.me/<username>（可带协议与查询串）；
//   - https://t.me/c/<internal_id>：私有频道链接 → -100 数字 ID；
//   - -100… 数字 ID；≥10 位的正整数按频道内部 ID 归一为 -100 形态。
//
// 消息链接（t.me/x/123）与群组等无法判定频道归属的输入一律拒绝。
func ParseChannelTarget(raw string) (ChannelTarget, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ChannelTarget{}, apperr.New(apperr.CodeChannelTargetInvalid, "频道标识为空")
	}
	s = strings.TrimPrefix(s, "@")

	// t.me 链接形态
	if host, rest, ok := splitTMe(s); ok {
		seg := strings.Split(strings.Trim(rest, "/"), "/")
		if host == "c" {
			// 私有频道：t.me/c/<internal_id>[/<message_id>]，取频道段
			if len(seg) < 1 {
				return ChannelTarget{}, apperr.New(apperr.CodeChannelTargetInvalid, "私有频道链接缺少频道 ID")
			}
			id, err := strconv.ParseInt(seg[0], 10, 64)
			if err != nil || id <= 0 {
				return ChannelTarget{}, apperr.New(apperr.CodeChannelTargetInvalid, "私有频道链接的频道 ID 非法")
			}
			return ChannelTarget{ChannelID: BotChannelID(id)}, nil
		}
		if len(seg) != 1 || seg[0] == "" {
			return ChannelTarget{}, apperr.New(apperr.CodeChannelTargetInvalid, "请发送频道链接，而不是消息链接")
		}
		if !isValidUsername(seg[0]) {
			return ChannelTarget{}, apperr.New(apperr.CodeChannelTargetInvalid, "链接中的频道用户名非法")
		}
		return ChannelTarget{Username: seg[0]}, nil
	}

	// 数字 ID 形态
	if id, err := strconv.ParseInt(s, 10, 64); err == nil {
		switch {
		case id < 0 && strings.HasPrefix(s, "-100") && len(s) >= 14:
			return ChannelTarget{ChannelID: id}, nil // 约定 -100… 形态
		case id > 0 && digitsOf(id) >= 10:
			// 正整数按频道内部 ID 处理：Bot API 频道 ID = -100 前缀 + 内部 ID
			return ChannelTarget{ChannelID: BotChannelID(id)}, nil
		}
	}

	// 用户名形态
	if !isValidUsername(s) {
		return ChannelTarget{}, apperr.New(apperr.CodeChannelTargetInvalid, "无法识别频道标识")
	}
	return ChannelTarget{Username: s}, nil
}

// splitTMe 识别 t.me 链接，返回路径首段（c 表示私有频道）与剩余路径。
func splitTMe(s string) (host, rest string, ok bool) {
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "www.")
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	if !strings.HasPrefix(s, "t.me/") {
		return "", "", false
	}
	rest = strings.TrimPrefix(s, "t.me/")
	parts := strings.SplitN(rest, "/", 2)
	if parts[0] != "c" {
		return "", rest, true
	}
	// 私有频道：返回去掉 c 段后的剩余路径（<internal_id>[/<message_id>]）；
	// 只有 "t.me/c" 本身时剩余路径为空，由调用方按 ID 非法拒绝
	if len(parts) < 2 {
		return "c", "", true
	}
	return "c", parts[1], true
}

// isValidUsername 校验 Telegram 用户名形态：字母开头、4-64 位字母数字下划线。
func isValidUsername(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		case r == '_' && i > 0:
		default:
			return false
		}
	}
	return true
}

// BotChannelID 把频道内部 ID（t.me/c/ 链接、MTProto 裸正 ID 共用形态）归一
// 为 Bot API 的频道数字 ID：-100 前缀 + 内部 ID 十进制串
// （如 1234567890 → -1001234567890）。导出供 watch 等把 MTProto 加入结果
// 转为 Bot API ID 的模块共用。
func BotChannelID(internal int64) int64 {
	mul := int64(1)
	for n := internal; n > 0; n /= 10 {
		mul *= 10
	}
	return -(100*mul + internal)
}

func digitsOf(n int64) int {
	count := 0
	for n > 0 {
		count++
		n /= 10
	}
	return count
}
