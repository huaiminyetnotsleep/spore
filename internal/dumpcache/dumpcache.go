package dumpcache

// dumpcache — 转存频道（dump channel）：任务成功投递后，向 bot 自有的私有
// 缓存频道同步写一份"干净副本"（caption 无用户绑定频道脚注），并落
// dump_entries。重复链接复用时经 copyMessages 从缓存频道整条复制到目标
// 聊天——服务端复制不受媒体大小限制（2GB 同路径）、相册保组、caption
// 天然无脚注泄露，且副本不因原用户删除消息而失效。
//
// 干净副本构造（给用户的投递 caption 织有脚注，副本必须剥离）：
//   - 单媒体：copyMessage 带 caption 覆盖（干净 caption：引用正文+原链接）；
//   - 单文本：SendMessage 干净渲染（无脚注）；
//   - 多条：copyMessages 整批复制（保组）后逐条 editMessageCaption /
//     editMessageText 清洗（首条带原消息链接，全条不带脚注）。
//
// 全部步骤尽力而为：任一失败只记日志、不落条目（下次成功投递自愈），
// 不影响任务结果。首次失败以 Warn 提示检查 DUMP_CHANNEL_ID 与 bot 频道
// 管理员权限（替代启动期预校验：写失败本身就是最准确的校验）。

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// Service 是缓存频道读写通道；channelID 闭包实时读取当前配置（Web 端
// settings 优先，环境变量兜底，main 注入），返回 0 表示未配置（功能关闭）。
// 多机器人池：已投递消息的坐标是受理 bot 私有的（用户私聊内消息 ID 按 bot
// 隔离），写干净副本与复用投递都必须由受理 bot 执行——sndFor 按任务 bot
// 解析通道，nil 或未命中回退 snd（主 bot，兼容单 bot 部署与 Web 触发路径）。
type Service struct {
	snd       delivery.Sender
	sndFor    func(botID int64) delivery.Sender
	st        *store.Store
	channelID func() int64
	log       *slog.Logger

	hintOnce sync.Once // 首次写失败的配置提示只打一次
}

// New 创建缓存频道服务；channelID 闭包返回 0 时 Enabled() 恒为 false。
// sndFor 可空（单 bot 部署）。
func New(snd delivery.Sender, sndFor func(botID int64) delivery.Sender, st *store.Store,
	channelID func() int64, log *slog.Logger) *Service {
	return &Service{snd: snd, sndFor: sndFor, st: st, channelID: channelID, log: log}
}

// senderFor 解析任务应使用的发送通道：受理 bot 优先，未命中回退 snd。
func (s *Service) senderFor(botID int64) delivery.Sender {
	if s.sndFor != nil {
		if snd := s.sndFor(botID); snd != nil {
			return snd
		}
	}
	return s.snd
}

// Enabled 报告缓存频道是否已配置（配置读取失败按未配置处理）。
func (s *Service) Enabled() bool { return s != nil && s.channelID != nil && s.channelID() != 0 }

// Channel 返回当前配置的缓存频道数字 ID（未配置时 false）。仅缓存补写任务
// 用它作为直接发送目标；一次任务的发送与落条目应使用同一次调用返回值，
// 与 WriteClean 的"一次写入同一频道"一致性语义相同。
func (s *Service) Channel() (int64, bool) {
	if !s.Enabled() {
		return 0, false
	}
	channel := s.channelID()
	return channel, channel != 0
}

// RecordEntry 落缓存频道条目（缓存补写任务在直接发送成功后调用，坐标供
// 同链接复用）；写失败只记日志——下次成功投递自愈，不影响任务结果。
func (s *Service) RecordEntry(ctx context.Context, channelKey string, messageID int, dumpIDs []int) {
	if !s.Enabled() || len(dumpIDs) == 0 {
		return
	}
	if _, err := s.st.InsertDumpEntry(ctx, store.DumpEntry{
		ChannelKey: channelKey, MessageID: messageID, DumpIDs: dumpIDs,
	}); err != nil {
		s.log.Warn("落缓存频道条目失败", "channel_key", channelKey, "message_id", messageID, "error", err.Error())
	}
}

// Entry 取同链接最新干净副本坐标；未配置或无条目返回 false。
func (s *Service) Entry(ctx context.Context, channelKey string, messageID int) (store.DumpEntry, bool) {
	if !s.Enabled() {
		return store.DumpEntry{}, false
	}
	e, err := s.st.LatestDumpEntry(ctx, channelKey, messageID)
	if err != nil {
		s.log.Warn("查询缓存频道条目失败", "channel_key", channelKey, "message_id", messageID, "error", err.Error())
		return store.DumpEntry{}, false
	}
	return e, true
}

// probeTimeout 是试探复制（copyMessage + deleteMessage 两次 Bot API 调用）
// 的独立时间窗：EntryLive 会被管理端 HTTP 预检与 worker 复核同步调用，
// 必须自带上限——调用方 ctx（HTTP 请求/任务级）往往过宽，Telegram 挂起时
// 不能跟随其无限等待。
const probeTimeout = 20 * time.Second

// EntryLive 报告同链接最新副本是否仍然可用：条目存在且其消息仍可访问。
// 缓存频道里的消息可能被管理员在客户端直接删除——条目坐标不感知删除，
// 预检与执行时复核都应经本方法判定，避免把失效条目误当成"已有副本"。
//
// 判定走"试探复制"，与私聊复用（tryReuseFromDump 的 CopyOut 回落）同源，
// 用生产已验证的 Bot API copyMessage 而非 MTProto 读消息（后者依赖频道
// access_hash 反查，对 Bot 会话不可靠且故障时静默）：把条目首条消息复制
// 到缓存频道自身——消息已被删除时该调用以明确错误失败；成功即删除试探
// 副本（删除失败只留一条无害的重复副本，记日志）。
//
// 返回值：无条目或试探失败（副本已删，或 Bot API 瞬时故障——放行后无非
// 是重复补写，无害）返回 false 放行补写自愈；试探成功返回 true 跳过补写。
func (s *Service) EntryLive(ctx context.Context, channelKey string, messageID int) bool {
	e, ok := s.Entry(ctx, channelKey, messageID)
	if !ok {
		return false
	}
	if !s.Enabled() {
		return false
	}
	tctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	channel := s.channelID()
	id, err := s.snd.CopyMessage(tctx, channel, channel, e.DumpIDs[0], "")
	if err != nil {
		s.log.Info("缓存副本试探复制失败，判定条目失效（放行补写自愈）",
			"channel_key", channelKey, "message_id", messageID,
			"dump_id", e.DumpIDs[0], "error", err.Error())
		return false
	}
	if derr := s.snd.DeleteMessage(tctx, channel, id); derr != nil {
		s.log.Warn("缓存副本试探消息删除失败（缓存频道残留一条重复副本，无害）",
			"channel_id", channel, "message_id", id, "error", derr.Error())
	}
	return true
}

// CopyOut 把缓存频道副本整条复制到目标聊天，返回按序新消息 ID。botID 为
// 任务的受理 bot：复制落到用户私聊即以该 bot 身份投递。
func (s *Service) CopyOut(ctx context.Context, botID, chatID int64, dumpIDs []int) ([]int, error) {
	return s.senderFor(botID).CopyMessages(ctx, s.channelID(), chatID, dumpIDs)
}

// WriteClean 在任务成功投递后写干净副本并落 dump_entries（尽力而为）。
// botID 为任务的受理 bot：已发送消息坐标是该 bot 私有的，必须由同一 bot
// 复制。items 与 sentIDs 按序对应（worker 发送顺序）；复用命中（Reused）
// 的任务不再重写（条目即复制来源）。sourceURL 为原消息链接（首条织入）。
func (s *Service) WriteClean(ctx context.Context, botID, chatID int64, channelKey string, messageID int,
	items []message.Item, sentIDs []int, sourceURL string) {
	if !s.Enabled() {
		return
	}
	snd := s.senderFor(botID)
	if len(items) == 0 || len(items) != len(sentIDs) {
		s.log.Debug("缓存频道副本跳过：条目与已发送消息数不一致",
			"channel_key", channelKey, "message_id", messageID,
			"items", len(items), "sent", len(sentIDs))
		return
	}
	// 一次写入全程使用同一频道 ID：中途经 Web 改配置不影响本批一致性
	channel := s.channelID()

	var dumpIDs []int
	if len(sentIDs) == 1 {
		id, err := s.writeSingle(ctx, snd, channel, chatID, items[0], sentIDs[0], sourceURL)
		if err != nil {
			s.failHint(ctx, err)
			return
		}
		dumpIDs = []int{id}
	} else {
		ids, err := snd.CopyMessages(ctx, chatID, channel, sentIDs)
		if err != nil {
			s.failHint(ctx, err)
			return
		}
		// 整批复制保留相册分组；caption 带着原用户的脚注，逐条清洗
		for i, it := range items {
			var err error
			if it.Media != nil {
				err = snd.EditMessageCaption(ctx, channel, ids[i], CleanCaption(it, i == 0, sourceURL, nil))
			} else {
				err = snd.EditMessageText(ctx, channel, ids[i], it.RenderHTMLWithSource(sourceURL, nil))
			}
			if err != nil {
				s.failHint(ctx, err)
				return
			}
		}
		dumpIDs = ids
	}

	if _, err := s.st.InsertDumpEntry(ctx, store.DumpEntry{
		ChannelKey: channelKey, MessageID: messageID, DumpIDs: dumpIDs,
	}); err != nil {
		s.log.Warn("落缓存频道条目失败", "channel_key", channelKey, "message_id", messageID, "error", err.Error())
	}
	s.log.Info("缓存频道干净副本已写入", "channel_key", channelKey,
		"message_id", messageID, "messages", len(dumpIDs))
}

// writeSingle 写单条副本：媒体走 copyMessage 带 caption 覆盖（一步到位），
// 文本直接干净渲染发送。
func (s *Service) writeSingle(ctx context.Context, snd delivery.Sender, channel, fromChatID int64,
	it message.Item, sentID int, sourceURL string) (int, error) {
	if it.Media == nil {
		return snd.SendMessage(ctx, channel, it.RenderHTMLWithSource(sourceURL, nil))
	}
	return snd.CopyMessage(ctx, fromChatID, channel, sentID, CleanCaption(it, true, sourceURL, nil))
}

// CleanCaption 构造干净 caption：引用正文 +（首条）原消息链接，不织频道
// 脚注。links 非空时织入（复用命中后为绑频道用户补脚注的编辑路径使用）。
func CleanCaption(it message.Item, first bool, sourceURL string, links []message.ChannelLink) string {
	caption := it.MediaCaption().WithQuotedBody()
	if first && sourceURL != "" {
		caption = caption.WithSourceLink(sourceURL)
		if links != nil {
			caption = caption.WithChannels(links)
		}
	}
	return caption.RenderHTML()
}

// failHint 记录写入失败；首次附带配置检查提示（频道 ID 是否正确、bot 是否
// 为频道管理员）。
func (s *Service) failHint(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return // 任务收尾窗口取消不算配置问题
	}
	s.log.Warn("缓存频道副本写入失败", "error", err.Error())
	s.hintOnce.Do(func() {
		s.log.Warn("缓存频道首次写入失败：请检查 DUMP_CHANNEL_ID 是否正确、bot 是否为该频道管理员（复制仍会回落，不影响任务）")
	})
}
