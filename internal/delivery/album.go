package delivery

import (
	"context"
	"fmt"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// SendAlbum 实现 Sender 的 Bot API 上传路径：以 sendMediaGroup 原子发送整组。
// 超过 Bot API 上限的大文件由 router 的整组预检排除（分流 MTProto 整组
// 直传），不会到达这里。
//
// 上传项的 Media 填 "attach://<名字>"，同一名字的数据经 MediaAttachment 进
// multipart；caption 逐成员绑定（各自渲染 HTML、各自挂 parse_mode），
// 保持源相册中文字与媒体的对应关系——与 MTProto 整组路径同语义。
func (s *telegramSender) SendAlbum(ctx context.Context, chatID int64, entries []AlbumEntry) ([]int, error) {
	if len(entries) == 0 || len(entries) > message.AlbumMaxItems {
		return nil, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("相册条目数不合法：%d", len(entries)))
	}

	for i, e := range entries {
		if e.Reader == nil {
			return nil, apperr.New(apperr.CodeInternal,
				fmt.Sprintf("相册第 %d 项应走上传路径（Reader 非空）", i))
		}
	}

	media := make([]models.InputMedia, 0, len(entries))
	for i, e := range entries {
		attachName := fmt.Sprintf("%s-media-%d", branding.StoragePrefix, i)
		captionHTML := e.Caption.RenderHTML()

		switch e.Media.Kind {
		case message.KindPhoto:
			if !s.AlbumGroupable(e.Media) {
				return nil, ErrAlbumNotSupported // 超限图片无法在组内降级，由调用方逐条发送
			}
			im := &models.InputMediaPhoto{
				Media:           "attach://" + attachName,
				MediaAttachment: e.Reader,
			}
			if captionHTML != "" {
				im.Caption = captionHTML
				im.ParseMode = models.ParseModeHTML
			}
			media = append(media, im)

		case message.KindVideo:
			im := &models.InputMediaVideo{
				Media:             "attach://" + attachName,
				MediaAttachment:   e.Reader,
				SupportsStreaming: true,
			}
			// 缩略图说明：go-telegram/bot 的表单构造器只为主媒体
			//（MediaAttachment）生成附件，InputMediaVideo.Thumbnail 引用的
			// attach:// 名字没有对应 form part，带上会被服务器拒绝——本路径
			// 依赖 Bot API 服务器的自动封面生成（≤50MB 视频基本可用）；
			// 带确定封面的整组（含超限成员）走 MTProto SendAlbum。
			if meta := e.Media.Video; meta != nil {
				im.Width = meta.Width
				im.Height = meta.Height
				im.Duration = meta.Duration
			}
			if captionHTML != "" {
				im.Caption = captionHTML
				im.ParseMode = models.ParseModeHTML
			}
			media = append(media, im)

		default:
			return nil, ErrAlbumNotSupported
		}
	}

	// 上传整组的数据源是单次消费的流式附件，不可重放：429 不等待重试
	//（与单媒体上传路径姿态一致）。逐成员消息 ID 按发送顺序返回，
	// 供 worker 把整组消息经 copyMessages 复制到用户绑定的频道。
	msgs, err := s.b.SendMediaGroup(ctx, &tgbot.SendMediaGroupParams{
		ChatID: chatID,
		Media:  media,
	})
	if err != nil {
		return nil, classifyBotError(err)
	}
	ids := make([]int, 0, len(msgs))
	for i := range msgs {
		ids = append(ids, msgs[i].ID)
	}
	return ids, nil
}
