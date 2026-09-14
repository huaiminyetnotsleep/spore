package notify

import (
	"context"
	"io"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// CountSender 包装业务发送通道，把 Bot API 发送结果喂给 Hub：
// 连续失败达到阈值时产生（或合并）botapi.send_failures 事件，成功清零。
// 装配层用包装后的通道服务 queue / access 等业务路径；
// Hub 自身的通知发送使用原始通道——通知失败是"通知"这一动作的结果而非业务
// 发送，不计数可避免"通知失败 → 产生事件 → 再通知失败"的自激回路。
type CountSender struct {
	delivery.Sender      // 未覆盖的方法（DeleteMessage / AlbumGroupable）原样透传
	hub             *Hub // 可空：便于仅复用 Sender 行为的测试/装配场景
}

// NewCountSender 创建计数包装；包装结果仍是 delivery.Sender，可直接替换原通道。
func NewCountSender(snd delivery.Sender, h *Hub) *CountSender {
	return &CountSender{Sender: snd, hub: h}
}

// SendMessage 发送文本并计数成败（占位提示、结果回传等文本路径）。
func (s *CountSender) SendMessage(ctx context.Context, chatID int64, html string) (int, error) {
	id, err := s.Sender.SendMessage(ctx, chatID, html)
	if s.hub != nil {
		s.hub.BotSendResult(ctx, err == nil)
	}
	return id, err
}

// SendMedia 发送媒体并计数成败。
func (s *CountSender) SendMedia(ctx context.Context, chatID int64, m message.Media, caption message.Caption, reader io.Reader) (int, error) {
	id, err := s.Sender.SendMedia(ctx, chatID, m, caption, reader)
	if s.hub != nil {
		s.hub.BotSendResult(ctx, err == nil)
	}
	return id, err
}

// SendAlbum 整组发送相册并计数成败。
func (s *CountSender) SendAlbum(ctx context.Context, chatID int64, entries []delivery.AlbumEntry) ([]int, error) {
	ids, err := s.Sender.SendAlbum(ctx, chatID, entries)
	if s.hub != nil {
		s.hub.BotSendResult(ctx, err == nil)
	}
	return ids, err
}

// DeleteMessage 不计数透传：状态提示可能已被用户删除，删除失败是常态，
// 不构成"Bot API 连续失败"信号（误报会淹没真实故障）。
func (s *CountSender) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	return s.Sender.DeleteMessage(ctx, chatID, messageID)
}

// EditMessageText 不计数透传：占位消息的实时进度编辑是高频常态操作，
// 编辑失败（占位被用户删除、瞬时限流）同样不是"连续发送失败"信号。
func (s *CountSender) EditMessageText(ctx context.Context, chatID int64, messageID int, html string) error {
	return s.Sender.EditMessageText(ctx, chatID, messageID, html)
}

// AlbumGroupable 不计数透传（纯本地判定，无 Bot API 调用）。
func (s *CountSender) AlbumGroupable(m message.Media) bool {
	return s.Sender.AlbumGroupable(m)
}

// CopyMessages 不计数透传：复用路径的 copy 失败（如源消息已被删除）是
// 预期内的软回落信号（调用方回落完整下载上传），不构成"Bot API 连续
// 发送失败"的健康信号。
func (s *CountSender) CopyMessages(ctx context.Context, fromChatID, chatID int64, messageIDs []int) ([]int, error) {
	return s.Sender.CopyMessages(ctx, fromChatID, chatID, messageIDs)
}

// CopyMessage 不计数透传（缓存频道干净副本构造，软失败语义同 CopyMessages）。
func (s *CountSender) CopyMessage(ctx context.Context, fromChatID, chatID int64, messageID int, captionHTML string) (int, error) {
	return s.Sender.CopyMessage(ctx, fromChatID, chatID, messageID, captionHTML)
}

// EditMessageCaption 不计数透传：caption 清洗/补脚注的编辑是低风险常态
// 操作，失败有各自回落语义，不构成连续发送失败信号（同 EditMessageText）。
func (s *CountSender) EditMessageCaption(ctx context.Context, chatID int64, messageID int, captionHTML string) error {
	return s.Sender.EditMessageCaption(ctx, chatID, messageID, captionHTML)
}
