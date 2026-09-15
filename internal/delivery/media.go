package delivery

import (
	"bytes"
	"context"
	"io"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// AlbumGroupable 判断媒体能否进入 sendMediaGroup 整组发送：
// 组内仅允许 photo/video，且 photo 不得超过 sendPhoto 上限（组内无法降级为 document）。
// SendAlbum 内部判定与 worker 的发送前预检共用此方法，单一事实来源。
// 超过 Bot API 上传上限的媒体不可整组（router 的预检会先排除，逐条走大文件直传）。
func (s *telegramSender) AlbumGroupable(m message.Media) bool {
	if m.Kind == message.KindVideo {
		return true
	}
	return m.Kind == message.KindPhoto && m.Size <= s.photoLimit
}

// SendMedia 实现 Sender 的 Bot API 上传路径；超过 Bot API 上限的大文件
// 由 routerSender 分派给 Bot 号 MTProto 会话直传，不会到达这里。
//
// 媒体数据源契约：reader 必须非 nil，违例返回内部防御错误；reader 单次消费
// （流式管道），429 不重试——已开始传输后无法安全重放，由用户重发链接兜底。
// 成功时返回新消息 ID（worker 据此把刚发出的消息复制到用户绑定的频道）。
func (s *telegramSender) SendMedia(ctx context.Context, chatID int64, m message.Media, caption message.Caption, reader io.Reader) (int, error) {
	if reader == nil {
		return 0, apperr.New(apperr.CodeInternal,
			"Bot API 上传路径要求 reader 非空（大文件直传应经路由分派）")
	}
	return s.sendMediaByUpload(ctx, chatID, m, caption.RenderHTML(), reader)
}

// sendMediaByUpload 上传发送：reader 为媒体数据源（流或文件句柄），单次消费。
func (s *telegramSender) sendMediaByUpload(ctx context.Context, chatID int64, m message.Media, captionHTML string, reader io.Reader) (int, error) {
	upload := &models.InputFileUpload{Filename: m.FileName, Data: reader}

	// 发送前的统一归一化：photo 超 sendPhoto 上限 → 以 document 形式发送。
	// 该约束属于 Bot API 传输层知识，收敛在此一处（相册路径见 album.go）。
	kind := m.Kind
	if kind == message.KindPhoto && m.Size > s.photoLimit {
		kind = message.KindDocument
	}

	switch kind {
	case message.KindPhoto:
		sent, err := s.b.SendPhoto(ctx, &tgbot.SendPhotoParams{
			ChatID:    chatID,
			Photo:     upload,
			Caption:   captionHTML,
			ParseMode: models.ParseModeHTML,
		})
		return sentMessageID(sent, err)

	case message.KindVideo:
		params := &tgbot.SendVideoParams{
			ChatID:            chatID,
			Video:             upload,
			Caption:           captionHTML,
			ParseMode:         models.ParseModeHTML,
			SupportsStreaming: true,
		}
		if meta := m.Video; meta != nil {
			params.Width = meta.Width
			params.Height = meta.Height
			params.Duration = meta.Duration
		}
		// 服务器端自动生成封面仅覆盖部分容器/编码：worker 已解析好的
		// 缩略图（源缩略图或 ffmpeg 抽帧）直接随上传携带
		if len(m.ThumbJPEG) > 0 {
			params.Thumbnail = &models.InputFileUpload{
				Filename: message.ThumbFileName,
				Data:     bytes.NewReader(m.ThumbJPEG),
			}
		}
		sent, err := s.b.SendVideo(ctx, params)
		return sentMessageID(sent, err)

	case message.KindVoice:
		params := &tgbot.SendVoiceParams{
			ChatID:    chatID,
			Voice:     upload,
			Caption:   captionHTML,
			ParseMode: models.ParseModeHTML,
		}
		if m.Audio != nil {
			params.Duration = m.Audio.Duration
		}
		sent, err := s.b.SendVoice(ctx, params)
		return sentMessageID(sent, err)

	case message.KindAudio:
		params := &tgbot.SendAudioParams{
			ChatID:    chatID,
			Audio:     upload,
			Caption:   captionHTML,
			ParseMode: models.ParseModeHTML,
		}
		if a := m.Audio; a != nil {
			params.Title = a.Title
			params.Performer = a.Performer
			params.Duration = a.Duration
		}
		sent, err := s.b.SendAudio(ctx, params)
		return sentMessageID(sent, err)

	default: // Document 及兜底
		sent, err := s.b.SendDocument(ctx, &tgbot.SendDocumentParams{
			ChatID:    chatID,
			Document:  upload,
			Caption:   captionHTML,
			ParseMode: models.ParseModeHTML,
		})
		return sentMessageID(sent, err)
	}
}

// sentMessageID 提取发送结果的消息 ID：失败统一走 classifyBotError；
// 成功但未返回消息对象按内部防御错误处理（调用方无法对无 ID 的消息做频道副本）。
func sentMessageID(m *models.Message, err error) (int, error) {
	if err != nil {
		return 0, classifyBotError(err)
	}
	if m == nil {
		return 0, apperr.New(apperr.CodeInternal, "发送成功但未返回消息对象")
	}
	return m.ID, nil
}
