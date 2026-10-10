// listener 包 — 监听源消息接收与缓存预热。botapi 把频道帖（channel_post）
// 与超级群组消息（bot 为管理员的源）交给本包；本包按 watch_sources 配置
// 过滤、聚合同一相册（media_group_id 防抖窗口）、然后两级转储：
//
//   - 快路径（源未开"禁止转发"）：受理 bot copyMessages 服务端复制进每个
//     启用的缓存频道（多缓存频道扇出，逐频道查重：已有副本的频道跳过，
//     尚无副本的频道补写，向全量收敛）——不下载不上传、无大小限制、相册
//     保组；成功后按每个成员消息 ID 逐频道落 dump_entries（公开源记
//     username 与 -100 双键，t.me 两种链接形态都命中复用）。
//   - 回退路径（has_protected_content 预判或复制被拒）：经 access.
//     EnqueueSourceDump 特权入队 DumpOnly 任务，走现有 worker 管线
//     （系统账号 fetch → 下载 → 重传进启用缓存频道 → 其余频道从副本
//     服务端复制 → 落条目，>2GB 自动分段）。回退只入队相册首条消息的
//     定位符（fetch 会取回整组，整组重传一次），条目只落单键——受保护的
//     公开源是小众路径，键覆盖缺口由下次正常投递自愈。
//
// 一切尽力而为：任何失败只记日志，不影响监听后续消息。多机器人池下同
// 一源可能多个 bot 都收到帖：转储前逐频道查 dump_entries 去重，漏网的
// 并发重复副本无害（复用按最新条目命中）。
//
// 缓存频道之外可配置监听转发频道（syscfg watch_forward_channels，独立于
// 缓存频道）：非保护源在缓存复制成功后从源直接镜像；保护源由重传管线
// 进缓存后从缓存中转镜像（queue DumpMirror，按 request_id 回写事件目标）。
// 镜像逐目标尽力而为，事件只记实际成功写入的目标（含各启用缓存频道）。
package listener

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/dumpcache"
	"github.com/huaiminyetnotsleep/spore/internal/errlog"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

const (
	// albumWindow 是相册聚合防抖窗口：相册成员逐条到达，每条重置计时，
	// 最后一条到达后静默 window 再整批转储（保组复制）。
	albumWindow = 1500 * time.Millisecond
	// copyTimeout 是一次批次转储（copyMessages + 落条目）的时间窗。
	copyTimeout = time.Minute
	// sourceCacheTTL 是源配置查询缓存时长：bot 所在超级群组的每条消息
	// 都会查询源表，缓存把高频路径的 DB 读降到每源每分钟一次；配置变更
	// （审批通过/暂停）最迟一个 TTL 生效。
	sourceCacheTTL = time.Minute
)

// EnqueueDump 是回退路径的特权入队回调（access.Service.EnqueueSourceDump）：
// 返回关联的 requests 行 ID（0 = 未建行：号主未设置或队列满）。
type EnqueueDump func(ctx context.Context, sourceKind, channelKey string, messageID int) (int64, error)

// Options 构造参数。
type Options struct {
	Log         *slog.Logger
	Store       *store.Store
	Dump        *dumpcache.Service
	EnqueueDump EnqueueDump
	// ErrLog 错误日志写入门面（可选，nil 安全）：转储复制失败与回退入队
	// 失败此前只进日志，接入后逐条落 error_logs 供管理端查询。
	ErrLog *errlog.Service
}

// Service 是监听源消息处理器。ctx 是生命周期上下文（MTProto 就绪作用域）：
// 聚合计时与转储都从它派生，离线时随之停止。OnMessage 由 botapi 轮询回调
// 调用（多 bot 并发），只做缓存查询与聚合登记，网络调用全部在计时器
// goroutine 里执行，不阻塞轮询。
type Service struct {
	ctx         context.Context
	log         *slog.Logger
	st          *store.Store
	dump        *dumpcache.Service
	enqueueDump EnqueueDump
	errLog      *errlog.Service

	mu      sync.Mutex
	pending map[aggKey]*aggGroup // 聚合中的批次（相册/单条）

	srcMu    sync.Mutex
	srcCache map[int64]srcCacheEntry // channel_id → 源配置缓存
}

// srcCacheEntry 是源配置查询缓存项；active=false 表示"查过但不是生效源"
// （同样缓存负结果，避免非源群组的每条消息都打 DB）。
type srcCacheEntry struct {
	src    store.WatchSource
	active bool
	expire time.Time
}

// aggKey 聚合批次键：同一聊天内同一 media_group_id；无相册标识的单条消息
// 以消息 ID 独立成批。
type aggKey struct {
	chatID int64
	group  string
}

type aggGroup struct {
	src         store.WatchSource
	chat        models.Chat
	snd         delivery.Sender
	botID       int64
	botUsername string
	msgs        []*models.Message
	timer       *time.Timer
}

// New 创建服务。
func New(ctx context.Context, opt Options) *Service {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	return &Service{
		ctx: ctx, log: opt.Log, st: opt.Store, dump: opt.Dump,
		enqueueDump: opt.EnqueueDump, errLog: opt.ErrLog,
		pending:  make(map[aggKey]*aggGroup),
		srcCache: make(map[int64]srcCacheEntry),
	}
}

// OnMessage 接收一条源侧消息（channel_post 或 supergroup 群消息）。
// botID/botUsername 是受理 bot 身份（事件留痕用：多 bot 池下记录哪个 bot
// 收到并转储了本批）。非生效源（未配置/待审批/暂停）与纯文本消息跳过并
// 记跳过日志（观测面：源内消息未转发时可在日志直接定位原因）。
func (s *Service) OnMessage(msg *models.Message, snd delivery.Sender, botID int64, botUsername string) {
	// nil 接收者防御：botapi.Options.OnSourceMessage 是方法值，装配时序
	// 错误会把 nil 接收者永久绑进回调（2026-09-21 SIGSEGV 回归）——崩溃
	// 会带走整个 bot 进程，这里降级为静默忽略。
	if s == nil || msg == nil || msg.Chat.ID == 0 || snd == nil {
		return
	}
	if msg.Chat.Type != models.ChatTypeChannel && msg.Chat.Type != models.ChatTypeSupergroup {
		s.log.Debug("监听消息跳过：聊天类型不支持",
			"chat_id", msg.Chat.ID, "chat_type", msg.Chat.Type, "message_id", msg.ID)
		return
	}
	// 先查源配置（TTL 缓存，非源聊天每分钟最多一次 DB 读），跳过原因才能
	// 区分"未配置源的无媒体消息"（静默）与"已注册源的无媒体消息"（报告）。
	src, active, reason := s.activeSource(msg.Chat.ID)
	if !active {
		s.logSkip(msg, reason)
		return
	}
	if !hasMedia(msg) {
		s.logSkip(msg, "消息无媒体（预热仅针对媒体消息）")
		return
	}
	group := msg.MediaGroupID
	if group == "" {
		// 单条消息以消息 ID 独立成批，避免同聊天多条非相册消息被错误聚合
		group = "m" + strconv.Itoa(msg.ID)
	}
	key := aggKey{chatID: msg.Chat.ID, group: group}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	g, exists := s.pending[key]
	if !exists {
		g = &aggGroup{src: src, chat: msg.Chat, snd: snd, botID: botID, botUsername: botUsername}
		s.pending[key] = g
	}
	g.msgs = append(g.msgs, msg)
	if g.timer != nil {
		g.timer.Reset(albumWindow)
		return
	}
	g.timer = time.AfterFunc(albumWindow, func() { s.flush(key) })
}

// logSkip 打印一条跳过记录（观测面：带发送者身份、实际内容与原因，用户
// 可据此判断是平台限制——如其他 bot 发的消息 bot 收不到——还是配置问题，
// 或内容本身不在预热范围）。
func (s *Service) logSkip(msg *models.Message, reason string) {
	fields := []any{
		"chat_id", msg.Chat.ID, "chat_type", msg.Chat.Type, "message_id", msg.ID,
		"content", describeContent(msg), "reason", reason,
	}
	if msg.From != nil {
		fields = append(fields, "from_id", msg.From.ID, "from_is_bot", msg.From.IsBot)
	}
	s.log.Info("监听消息跳过", fields...)
}

// flush 取出并转储一个批次（计时器 goroutine 内执行）。
func (s *Service) flush(key aggKey) {
	s.mu.Lock()
	g, ok := s.pending[key]
	delete(s.pending, key)
	s.mu.Unlock()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(s.ctx, copyTimeout)
	defer cancel()
	s.process(ctx, g)
}

// activeSource 查询频道是否为生效监听源（approved 且 enabled），带 TTL
// 缓存（含负结果）。reason 描述非生效原因：源未配置/待审批/已拒绝/已暂停
// /查询失败。跳过日志只在缓存未命中（真实查库）时打印一次，命中负缓存
// 的重复消息不再刷屏。
func (s *Service) activeSource(channelID int64) (store.WatchSource, bool, string) {
	now := time.Now()
	s.srcMu.Lock()
	if e, ok := s.srcCache[channelID]; ok && now.Before(e.expire) {
		s.srcMu.Unlock()
		return e.src, e.active, ""
	}
	s.srcMu.Unlock()

	src, err := s.st.GetWatchSource(s.ctx, channelID)
	var reason string
	active := err == nil && src.Status == store.WatchApproved && src.Enabled
	switch {
	case errors.Is(err, store.ErrNotFound):
		reason = "源未配置"
	case err != nil:
		reason = "查询失败"
		s.log.Warn("查询监听源失败", "channel_id", channelID, "error", err.Error())
	case src.Status == store.WatchPending:
		reason = "源待审批"
	case src.Status == store.WatchRejected:
		reason = "源已拒绝"
	case !src.Enabled:
		reason = "源已暂停"
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return store.WatchSource{}, false, reason
	}
	s.srcMu.Lock()
	s.srcCache[channelID] = srcCacheEntry{src: src, active: active, expire: now.Add(sourceCacheTTL)}
	s.srcMu.Unlock()
	return src, active, reason
}

// process 转储一个已聚齐的批次：逐启用缓存频道查重 → 受保护分流 → 快路径
// 扇出复制 → 逐频道落条目。
func (s *Service) process(ctx context.Context, g *aggGroup) {
	if s.dump == nil || !s.dump.Enabled() {
		s.log.Info("监听消息跳过：缓存频道未配置",
			"chat_id", g.chat.ID, "message_id", g.msgs[0].ID, "reason", "缓存频道未配置")
		return
	}
	channels, ok := s.dump.Channels()
	if !ok {
		s.log.Info("监听消息跳过：缓存频道不可用",
			"chat_id", g.chat.ID, "message_id", g.msgs[0].ID, "reason", "缓存频道不可用")
		return
	}
	// 循环转发防护：缓存频道不能同时是监听源——预热副本写入缓存频道会
	// 再次触发该"源"的 channel_post，监听再次预热形成死循环。注册侧
	//（watch Submit/AdminAdd 与 Web 缓存频道添加）已双向校验，此处兜底
	// 防御配置改动的中间态与历史脏数据。
	if containsChannel(channels, g.chat.ID) {
		s.log.Info("监听消息跳过：目标聊天是启用的缓存频道（循环转发防护）",
			"chat_id", g.chat.ID, "message_id", g.msgs[0].ID, "reason", "缓存频道与监听源互斥")
		return
	}
	// 启用缓存频道即预热目标（配置顺序，标题取配置快照：settings 驱动的
	// 标题，env 兜底部署无标题快照则展示回退数字 ID）；监听转发频道是
	// 独立于缓存频道的额外镜像目标，随批读取最新配置并过滤与缓存频道
	// 重复的 ID。
	cfg := syscfg.LoadEffectiveDumpChannels(ctx, s.st, 0)
	titles := make(map[int64]string, len(cfg))
	for _, c := range cfg {
		if c.Enabled {
			titles[c.ChannelID] = c.Title
		}
	}
	forwards := forwardTargets(ctx, s.st, channels)
	media := make([]*models.Message, 0, len(g.msgs))
	for _, m := range g.msgs {
		if hasMedia(m) {
			media = append(media, m)
		}
	}
	if len(media) == 0 {
		return
	}
	// 批内规范化：按消息 ID 升序去重。多 bot 池下同一源帖会被多个受理 bot
	// 各投递一遍，共享聚合组可能出现重复或交错乱序成员——copyMessages 要
	// 求 message_ids 严格递增，不规范化会被 Telegram 整批拒绝（批内全部丢
	// 失，且该错误不回退入队）。排序后首条即相册头（回退定位符取它）。
	sort.Slice(media, func(i, j int) bool { return media[i].ID < media[j].ID })
	uniq := media[:0]
	for _, m := range media {
		if len(uniq) == 0 || uniq[len(uniq)-1].ID != m.ID {
			uniq = append(uniq, m)
		}
	}
	media = uniq
	first := media[0]
	numericKey := strconv.FormatInt(g.chat.ID, 10)
	usernameKey := strings.ToLower(g.src.Username)

	// 逐启用频道查重（公开源双键形态任一命中即该频道已缓存）：已有副本的
	// 频道跳过本批复制（也是多 bot 池重复投递的主要防线），尚无副本的频道
	// 补写，向全量收敛；并发窗口漏过的重复副本无害，复用按最新条目命中。
	need := make([]int64, 0, len(channels))
	for _, channel := range channels {
		if s.dump.EntryIn(ctx, channel, numericKey, first.ID) {
			continue
		}
		if usernameKey != "" && s.dump.EntryIn(ctx, channel, usernameKey, first.ID) {
			continue
		}
		need = append(need, channel)
	}
	if len(need) == 0 {
		return
	}
	// 全部启用频道的展示目标（事件 Targets 快照的缓存部分，无论单频道复制
	// 成败先按配置记，复制结果再逐频道确认——尽力而为，失败频道不记）。
	dumpTargets := make([]store.WatchEventTarget, 0, len(channels))
	for _, channel := range channels {
		dumpTargets = append(dumpTargets, store.WatchEventTarget{ChannelID: channel, Title: titles[channel]})
	}

	// 受保护源：服务端复制必被拒，直接走重传管线（相册整组只入队首条）。
	// 标记取消息级 has_protected_content（任一成员受保护即整组回退）。
	protected := false
	for _, m := range media {
		if m.HasProtectedContent {
			protected = true
			break
		}
	}
	if protected {
		s.fallback(ctx, g, dumpTargets, usernameKey, numericKey, first.ID, "源开启禁止转发")
		return
	}

	ids := make([]int, 0, len(media))
	for _, m := range media {
		ids = append(ids, m.ID)
	}
	// 逐启用频道扇出复制（原样完整副本）：单频道失败只记日志与错误日志并
	// 继续其余频道；全部频道都因「禁止转发」被拒时回落重传管线。
	var firstDumpIDs []int
	written := make(map[int64]bool, len(need))
	restricted := 0
	for _, channel := range need {
		dumpIDs, err := g.snd.CopyMessages(ctx, g.chat.ID, channel, ids)
		if err != nil {
			if isForwardsRestricted(err) {
				restricted++
				continue
			}
			s.log.Warn("监听源转储复制失败（跳过该频道，下一条消息自愈）",
				"channel_id", g.chat.ID, "dump_channel", channel, "messages", len(ids),
				"bot_id", g.botID, "bot_username", g.botUsername,
				"hint", "请确认受理 bot 在该缓存频道有发帖权限", "error", err.Error())
			s.errLog.Record(ctx, errlog.Record{
				Source:  store.ErrorSourceWatch,
				Code:    string(apperr.From(err).Code),
				Stage:   "copy",
				Detail:  err.Error(),
				Message: "监听源转储复制失败（跳过该频道）：" + keyOf(usernameKey, numericKey),
				Context: map[string]any{"channel_id": g.chat.ID, "dump_channel": channel,
					"messages": len(ids), "bot_id": g.botID, "bot_username": g.botUsername},
			})
			continue
		}
		// 每个成员消息 ID 都逐频道落条目（用户可能链接相册任意成员）；公开
		// 源双键，username 归一小写（t.me 链接通常小写；混合大小写链接走
		// -100 键兜底）。
		for _, m := range media {
			s.dump.RecordEntryFor(ctx, channel, numericKey, m.ID, dumpIDs)
			if usernameKey != "" {
				s.dump.RecordEntryFor(ctx, channel, usernameKey, m.ID, dumpIDs)
			}
		}
		if firstDumpIDs == nil {
			firstDumpIDs = dumpIDs
		}
		written[channel] = true
	}
	if len(written) == 0 && restricted == len(need) {
		// 所有待补频道都被「禁止转发」拒绝：源侧保护，整组回落重传管线
		s.fallback(ctx, g, dumpTargets, usernameKey, numericKey, first.ID, "复制被拒（禁止转发）")
		return
	}
	if len(written) > 0 {
		s.dump.WriteRecovered(ctx)
	}
	// 转发频道镜像：缓存复制成功后从源逐个服务端复制（原样完整副本），
	// 逐目标尽力而为——单目标失败只记日志与错误日志，不影响缓存结果与
	// 其他目标；事件 targets 只记实际成功写入的目标（启用缓存频道优先：
	// 本批跳过的频道本就已有副本，复制成功的按 written 记）。
	eventTargets := make([]store.WatchEventTarget, 0, len(dumpTargets)+len(forwards))
	for _, t := range dumpTargets {
		if !containsChannel(need, t.ChannelID) || written[t.ChannelID] {
			eventTargets = append(eventTargets, t)
		}
	}
	for _, f := range forwards {
		if _, err := g.snd.CopyMessages(ctx, g.chat.ID, f.ChannelID, ids); err != nil {
			s.log.Warn("监听转发频道复制失败", "channel_id", g.chat.ID,
				"forward_channel", f.ChannelID, "messages", len(ids),
				"bot_id", g.botID, "bot_username", g.botUsername, "error", err.Error())
			s.errLog.Record(ctx, errlog.Record{
				Source:  store.ErrorSourceWatch,
				Code:    string(apperr.From(err).Code),
				Stage:   "copy_forward",
				Detail:  err.Error(),
				Message: "监听转发频道复制失败：" + keyOf(usernameKey, numericKey) + " → " + f.Title,
				Context: map[string]any{"channel_id": g.chat.ID, "forward_channel": f.ChannelID,
					"messages": len(ids), "bot_id": g.botID, "bot_username": g.botUsername},
			})
			continue
		}
		eventTargets = append(eventTargets, store.WatchEventTarget{ChannelID: f.ChannelID, Title: f.Title})
	}
	s.recordEvent(ctx, store.WatchEvent{
		ChannelID: g.chat.ID, Username: g.src.Username, Title: g.src.Title,
		MessageID: first.ID, MemberIDs: ids, DumpIDs: firstDumpIDs,
		Targets: eventTargets,
		BotID:   g.botID, BotUsername: g.botUsername, Path: store.WatchPathCopy,
	})
	s.log.Info("监听源已预热缓存频道", "channel_id", g.chat.ID,
		"channel_key", keyOf(usernameKey, numericKey), "messages", len(ids),
		"dump_channels", fmt.Sprintf("%d/%d", len(written), len(need)),
		"forward_channels", len(forwards))
}

// fallback 走特权入队重传管线（受保护内容；相册整组一次）。targets 为全部
// 启用缓存频道的展示目标（重传管线内部扇出写入，事件快照按配置记）。
func (s *Service) fallback(ctx context.Context, g *aggGroup, targets []store.WatchEventTarget,
	usernameKey, numericKey string, messageID int, reason string) {
	if s.enqueueDump == nil {
		s.log.Warn("监听源回退入队未装配", "channel_id", g.src.ChannelID, "reason", reason)
		return
	}
	kind, key := store.SourcePrivate, numericKey
	if usernameKey != "" {
		kind, key = store.SourcePublic, usernameKey
	}
	requestID, err := s.enqueueDump(ctx, kind, key, messageID)
	if err != nil {
		s.log.Warn("监听源回退入队失败", "channel_id", g.src.ChannelID,
			"message_id", messageID, "reason", reason, "error", err.Error())
		s.errLog.Record(ctx, errlog.Record{
			Source:  store.ErrorSourceWatch,
			Code:    string(apperr.From(err).Code),
			Stage:   "enqueue",
			Detail:  err.Error(),
			Message: "监听源回退入队失败（" + reason + "）",
			Context: map[string]any{"channel_id": g.src.ChannelID, "message_id": messageID,
				"bot_id": g.botID, "bot_username": g.botUsername},
		})
	}
	s.recordEvent(ctx, store.WatchEvent{
		ChannelID: g.src.ChannelID, Username: g.src.Username, Title: g.src.Title,
		MessageID: messageID, RequestID: requestID,
		// 目标快照先记启用缓存频道；重传镜像完成后由 queue DumpMirror 按
		// request_id 回写实际成功的转发频道。
		Targets: targets,
		BotID:   g.botID, BotUsername: g.botUsername,
		Path: store.WatchPathFallback,
	})
	if err != nil {
		return
	}
	s.log.Info("监听源回退重传入队", "channel_id", g.src.ChannelID,
		"message_id", messageID, "request_id", requestID, "reason", reason)
}

// keyOf 返回日志展示键（优先 username）。
func keyOf(usernameKey, numericKey string) string {
	if usernameKey != "" {
		return usernameKey
	}
	return numericKey
}

// forwardTargets 读取监听转发频道配置（随批取最新值），过滤与任一启用
// 缓存频道重复的项——启用缓存频道始终单独作为预热目标，不重复复制。
func forwardTargets(ctx context.Context, st *store.Store, cacheChannels []int64) []syscfg.WatchForwardChannel {
	cache := make(map[int64]bool, len(cacheChannels))
	for _, c := range cacheChannels {
		cache[c] = true
	}
	all := syscfg.LoadWatchForwardChannels(ctx, st)
	out := make([]syscfg.WatchForwardChannel, 0, len(all))
	for _, f := range all {
		if !cache[f.ChannelID] {
			out = append(out, f)
		}
	}
	return out
}

// containsChannel 报告频道是否在列表中（预热事件目标确认用）。
func containsChannel(channels []int64, id int64) bool {
	for _, c := range channels {
		if c == id {
			return true
		}
	}
	return false
}

// hasMedia 判断消息是否携带可预热的媒体。贴纸/实况照片按图片内容对待
// （copyMessages 均支持，源内发出的贴纸同样有预热价值）。
func hasMedia(m *models.Message) bool {
	return len(m.Photo) > 0 || m.Video != nil || m.Animation != nil ||
		m.Document != nil || m.Audio != nil || m.Voice != nil ||
		m.VideoNote != nil || m.Sticker != nil || m.LivePhoto != nil
}

// describeContent 描述消息实际携带的内容（跳过日志观测用）：让"看着有图
// 却没预热"的差异有据可查——典型如图片以链接预览形态出现在纯文本里
// （Bot API 视为 text，无 photo 字段）。
func describeContent(m *models.Message) string {
	parts := make([]string, 0, 4)
	if len(m.Photo) > 0 {
		parts = append(parts, "photo")
	}
	if m.Video != nil {
		parts = append(parts, "video")
	}
	if m.Animation != nil {
		parts = append(parts, "gif")
	}
	if m.Document != nil {
		parts = append(parts, "file")
	}
	if m.Audio != nil {
		parts = append(parts, "audio")
	}
	if m.Voice != nil {
		parts = append(parts, "voice")
	}
	if m.VideoNote != nil {
		parts = append(parts, "video-note")
	}
	if m.Sticker != nil {
		parts = append(parts, "sticker")
	}
	if m.LivePhoto != nil {
		parts = append(parts, "live-photo")
	}
	if m.Text != "" {
		parts = append(parts, "text")
	}
	if m.Caption != "" {
		parts = append(parts, "caption")
	}
	if len(parts) == 0 {
		return "empty"
	}
	return strings.Join(parts, "+")
}

// isForwardsRestricted 识别 Telegram 对受保护内容聊天的复制拒绝
// （Bot API 错误描述 "chat forwards restricted"）。
func isForwardsRestricted(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "forwards restricted") ||
		strings.Contains(msg, "forwards_restricted")
}

// recordEvent 落预热事件留痕（尽力而为：失败只记日志，不影响转储结果——
// 事件是观测面不是业务面）。
func (s *Service) recordEvent(ctx context.Context, e store.WatchEvent) {
	if s.st == nil {
		return
	}
	if _, err := s.st.InsertWatchEvent(ctx, e); err != nil {
		s.log.Warn("预热事件留痕失败", "channel_id", e.ChannelID,
			"message_id", e.MessageID, "path", e.Path, "error", err.Error())
	}
}
