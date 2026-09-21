// listener 包 — 监听源消息接收与缓存预热。botapi 把频道帖（channel_post）
// 与超级群组消息（bot 为管理员的源）交给本包；本包按 watch_sources 配置
// 过滤、聚合同一相册（media_group_id 防抖窗口）、然后两级转储：
//
//   - 快路径（源未开"禁止转发"）：受理 bot copyMessages 服务端复制进缓存
//     频道——不下载不上传、无大小限制、相册保组；成功后按每个成员消息
//     ID 落 dump_entries（公开源记 username 与 -100 双键，t.me 两种链接
//     形态都命中复用）。
//   - 回退路径（has_protected_content 预判或复制被拒）：经 access.
//     EnqueueSourceDump 特权入队 DumpOnly 任务，走现有 worker 管线
//     （系统账号 fetch → 下载 → 重传进缓存频道 → 落条目，>2GB 自动分段）。
//     回退只入队相册首条消息的定位符（fetch 会取回整组，整组重传一次），
//     条目只落单键——受保护的公开源是小众路径，键覆盖缺口由下次正常
//     投递自愈。
//
// 一切尽力而为：任何失败只记日志，不影响监听后续消息。多机器人池下同
// 一源可能多个 bot 都收到帖：转储前查 dump_entries 去重，漏网的并发重复
// 副本无害（复用按最新条目命中）。
package listener

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/dumpcache"
	"github.com/huaiminyetnotsleep/spore/internal/store"
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
		enqueueDump: opt.EnqueueDump,
		pending:     make(map[aggKey]*aggGroup),
		srcCache:    make(map[int64]srcCacheEntry),
	}
}

// OnMessage 接收一条源侧消息（channel_post 或 supergroup 群消息）。
// botID/botUsername 是受理 bot 身份（事件留痕用：多 bot 池下记录哪个 bot
// 收到并转储了本批）。非频道/超级群组类型直接忽略；非生效源（未配置/
// 待审批/暂停）静默丢弃。纯文本等无媒体消息也在此丢弃（预热只对媒体有
// 意义）。
func (s *Service) OnMessage(msg *models.Message, snd delivery.Sender, botID int64, botUsername string) {
	if msg == nil || msg.Chat.ID == 0 || snd == nil {
		return
	}
	if msg.Chat.Type != models.ChatTypeChannel && msg.Chat.Type != models.ChatTypeSupergroup {
		return
	}
	if !hasMedia(msg) {
		return
	}
	src, ok := s.activeSource(msg.Chat.ID)
	if !ok {
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
// 缓存（含负结果）。
func (s *Service) activeSource(channelID int64) (store.WatchSource, bool) {
	now := time.Now()
	s.srcMu.Lock()
	if e, ok := s.srcCache[channelID]; ok && now.Before(e.expire) {
		s.srcMu.Unlock()
		return e.src, e.active
	}
	s.srcMu.Unlock()

	src, err := s.st.GetWatchSource(s.ctx, channelID)
	active := err == nil && src.Status == store.WatchApproved && src.Enabled
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("查询监听源失败", "channel_id", channelID, "error", err.Error())
		return store.WatchSource{}, false
	}
	s.srcMu.Lock()
	s.srcCache[channelID] = srcCacheEntry{src: src, active: active, expire: now.Add(sourceCacheTTL)}
	s.srcMu.Unlock()
	return src, active
}

// process 转储一个已聚齐的批次：查重 → 受保护分流 → 快路径复制 → 落条目。
func (s *Service) process(ctx context.Context, g *aggGroup) {
	if s.dump == nil || !s.dump.Enabled() {
		return
	}
	channel, ok := s.dump.Channel()
	if !ok {
		return
	}
	media := make([]*models.Message, 0, len(g.msgs))
	for _, m := range g.msgs {
		if hasMedia(m) {
			media = append(media, m)
		}
	}
	if len(media) == 0 {
		return
	}
	first := media[0]
	numericKey := strconv.FormatInt(g.chat.ID, 10)
	usernameKey := strings.ToLower(g.src.Username)

	// 查重：任一键（公开源双键形态）已有条目即整批跳过——相册整体命中，
	// 也是多 bot 池重复投递的主要防线；并发窗口漏过的重复副本无害，
	// 复用按最新条目命中。
	if _, ok := s.dump.Entry(ctx, numericKey, first.ID); ok {
		return
	}
	if usernameKey != "" {
		if _, ok := s.dump.Entry(ctx, usernameKey, first.ID); ok {
			return
		}
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
		s.fallback(ctx, g, usernameKey, numericKey, first.ID, "源开启禁止转发")
		return
	}

	ids := make([]int, 0, len(media))
	for _, m := range media {
		ids = append(ids, m.ID)
	}
	dumpIDs, err := g.snd.CopyMessages(ctx, g.chat.ID, channel, ids)
	if err != nil {
		if isForwardsRestricted(err) {
			s.fallback(ctx, g, usernameKey, numericKey, first.ID, "复制被拒（禁止转发）")
			return
		}
		s.log.Warn("监听源转储复制失败（跳过本批，下一条消息自愈）",
			"channel_id", g.chat.ID, "messages", len(ids), "error", err.Error())
		return
	}
	// 每个成员消息 ID 都落条目（用户可能链接相册任意成员）；公开源双键，
	// username 归一小写（t.me 链接通常小写；混合大小写链接走 -100 键兜底）。
	for _, m := range media {
		s.dump.RecordEntry(ctx, numericKey, m.ID, dumpIDs)
		if usernameKey != "" {
			s.dump.RecordEntry(ctx, usernameKey, m.ID, dumpIDs)
		}
	}
	s.recordEvent(ctx, store.WatchEvent{
		ChannelID: g.chat.ID, Username: g.src.Username, Title: g.src.Title,
		MessageID: first.ID, MemberIDs: ids, DumpIDs: dumpIDs,
		BotID: g.botID, BotUsername: g.botUsername, Path: store.WatchPathCopy,
	})
	s.log.Info("监听源已预热缓存频道", "channel_id", g.chat.ID,
		"channel_key", keyOf(usernameKey, numericKey), "messages", len(dumpIDs))
}

// fallback 走特权入队重传管线（受保护内容；相册整组一次）。
func (s *Service) fallback(ctx context.Context, g *aggGroup, usernameKey, numericKey string, messageID int, reason string) {
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
	}
	s.recordEvent(ctx, store.WatchEvent{
		ChannelID: g.src.ChannelID, Username: g.src.Username, Title: g.src.Title,
		MessageID: messageID, RequestID: requestID,
		BotID: g.botID, BotUsername: g.botUsername,
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

// hasMedia 判断消息是否携带可预热的媒体（纯文本无预热价值）。
func hasMedia(m *models.Message) bool {
	return m.Photo != nil || m.Video != nil || m.Animation != nil ||
		m.Document != nil || m.Audio != nil || m.Voice != nil || m.VideoNote != nil
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
