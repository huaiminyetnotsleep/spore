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

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// Service 是缓存频道读写通道；channelID 闭包实时读取当前配置（Web 端
// settings 优先，环境变量兜底，main 注入），返回 0 表示未配置（功能关闭）。
type Service struct {
	snd       delivery.Sender
	st        *store.Store
	channelID func() int64
	log       *slog.Logger

	hintOnce sync.Once // 首次写失败的配置提示只打一次
}

// New 创建缓存频道服务；channelID 闭包返回 0 时 Enabled() 恒为 false。
func New(snd delivery.Sender, st *store.Store, channelID func() int64, log *slog.Logger) *Service {
	return &Service{snd: snd, st: st, channelID: channelID, log: log}
}

// Enabled 报告缓存频道是否已配置（配置读取失败按未配置处理）。
func (s *Service) Enabled() bool { return s != nil && s.channelID != nil && s.channelID() != 0 }

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

// CopyOut 把缓存频道副本整条复制到目标聊天，返回按序新消息 ID。
func (s *Service) CopyOut(ctx context.Context, chatID int64, dumpIDs []int) ([]int, error) {
	return s.snd.CopyMessages(ctx, s.channelID(), chatID, dumpIDs)
}

// WriteClean 在任务成功投递后写干净副本并落 dump_entries（尽力而为）。
// items 与 sentIDs 按序对应（worker 发送顺序）；复用命中（Reused）的任务
// 不再重写（条目即复制来源）。sourceURL 为原消息链接（首条织入）。
func (s *Service) WriteClean(ctx context.Context, chatID int64, channelKey string, messageID int,
	items []message.Item, sentIDs []int, sourceURL string) {
	if !s.Enabled() {
		return
	}
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
		id, err := s.writeSingle(ctx, channel, chatID, items[0], sentIDs[0], sourceURL)
		if err != nil {
			s.failHint(ctx, err)
			return
		}
		dumpIDs = []int{id}
	} else {
		ids, err := s.snd.CopyMessages(ctx, chatID, channel, sentIDs)
		if err != nil {
			s.failHint(ctx, err)
			return
		}
		// 整批复制保留相册分组；caption 带着原用户的脚注，逐条清洗
		for i, it := range items {
			var err error
			if it.Media != nil {
				err = s.snd.EditMessageCaption(ctx, channel, ids[i], CleanCaption(it, i == 0, sourceURL, nil))
			} else {
				err = s.snd.EditMessageText(ctx, channel, ids[i], it.RenderHTMLWithSource(sourceURL, nil))
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
func (s *Service) writeSingle(ctx context.Context, channel, fromChatID int64, it message.Item,
	sentID int, sourceURL string) (int, error) {
	if it.Media == nil {
		return s.snd.SendMessage(ctx, channel, it.RenderHTMLWithSource(sourceURL, nil))
	}
	return s.snd.CopyMessage(ctx, fromChatID, channel, sentID, CleanCaption(it, true, sourceURL, nil))
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
