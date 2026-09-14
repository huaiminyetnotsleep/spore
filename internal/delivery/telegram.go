package delivery

import (
	"context"
	"errors"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// defaultPhotoLimit 官方 Bot API 服务器 sendPhoto 的图片上限（字节）；
// 作为 Config.PhotoLimit 缺省值（config.Load 正常路径下总会显式给出）。
const defaultPhotoLimit = int64(10) << 20

// telegramSender 基于 go-telegram/bot 的 Sender 实现（无状态，仅持 bot 与配置）。
type telegramSender struct {
	b          *tgbot.Bot
	photoLimit int64
}

// New 创建 Sender 实例。
func New(b *tgbot.Bot, cfg Config) Sender {
	limit := cfg.PhotoLimit
	if limit <= 0 {
		limit = defaultPhotoLimit
	}
	return &telegramSender{b: b, photoLimit: limit}
}

func (s *telegramSender) SendMessage(ctx context.Context, chatID int64, html string) (int, error) {
	var sent *models.Message
	err := s.withRateLimitRetry(ctx, func() error {
		m, rerr := s.b.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:    chatID,
			Text:      html,
			ParseMode: models.ParseModeHTML,
		})
		sent = m
		return rerr
	})
	if err != nil {
		return 0, classifyBotError(err)
	}
	if sent == nil {
		return 0, apperr.New(apperr.CodeInternal, "SendMessage 未返回消息对象")
	}
	return sent.ID, nil
}

func (s *telegramSender) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	err := s.withRateLimitRetry(ctx, func() error {
		_, rerr := s.b.DeleteMessage(ctx, &tgbot.DeleteMessageParams{
			ChatID:    chatID,
			MessageID: messageID,
		})
		return rerr
	})
	if err == nil {
		return nil
	}
	return classifyBotError(err)
}

// errMessageNotModified 是 Telegram 对"编辑后内容与原文相同"的 400 响应：
// 语义上等于编辑成功（文本已是目标内容），按 no-op 处理而非失败。
const errMessageNotModified = "message is not modified"

// CopyMessages 从 fromChatID 整组复制 bot 已发送的消息（无请求体，可安全
// 重放，适用限流重试）。源消息已被删除、源 chat 不可访问等失败经
// classifyBotError 分类，由调用方决定回落策略。
func (s *telegramSender) CopyMessages(ctx context.Context, fromChatID, chatID int64, messageIDs []int) ([]int, error) {
	if len(messageIDs) == 0 {
		return nil, apperr.New(apperr.CodeInternal, "CopyMessages 要求 messageIDs 非空")
	}
	var sent []models.MessageID
	err := s.withRateLimitRetry(ctx, func() error {
		m, rerr := s.b.CopyMessages(ctx, &tgbot.CopyMessagesParams{
			ChatID:     chatID,
			FromChatID: fromChatID,
			MessageIDs: messageIDs,
		})
		sent = m
		return rerr
	})
	if err != nil {
		return nil, classifyBotError(err)
	}
	ids := make([]int, 0, len(sent))
	for _, m := range sent {
		ids = append(ids, m.ID)
	}
	if len(ids) == 0 {
		return nil, apperr.New(apperr.CodeInternal, "CopyMessages 成功但未返回消息 ID")
	}
	return ids, nil
}

func (s *telegramSender) EditMessageText(ctx context.Context, chatID int64, messageID int, html string) error {
	err := s.withRateLimitRetry(ctx, func() error {
		_, rerr := s.b.EditMessageText(ctx, &tgbot.EditMessageTextParams{
			ChatID:    chatID,
			MessageID: messageID,
			Text:      html,
			ParseMode: models.ParseModeHTML,
		})
		return rerr
	})
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), errMessageNotModified) {
		return nil
	}
	return classifyBotError(err)
}

// CopyMessage 单条复制并覆盖 caption（干净副本构造用；无请求体，可安全
// 重放，适用限流重试）。返回新消息 ID；源消息已删除等失败经 classifyBotError
// 分类，由调用方决定回落策略。
func (s *telegramSender) CopyMessage(ctx context.Context, fromChatID, chatID int64, messageID int, captionHTML string) (int, error) {
	var sent *models.MessageID
	err := s.withRateLimitRetry(ctx, func() error {
		m, rerr := s.b.CopyMessage(ctx, &tgbot.CopyMessageParams{
			ChatID:     chatID,
			FromChatID: fromChatID,
			MessageID:  messageID,
			Caption:    captionHTML,
			ParseMode:  models.ParseModeHTML,
		})
		sent = m
		return rerr
	})
	if err != nil {
		return 0, classifyBotError(err)
	}
	if sent == nil || sent.ID == 0 {
		return 0, apperr.New(apperr.CodeInternal, "CopyMessage 成功但未返回消息 ID")
	}
	return sent.ID, nil
}

// EditMessageCaption 编辑既有媒体消息的 caption（清洗与补脚注用）。
// "message is not modified"（相同内容编辑）按成功 no-op 处理，语义同
// EditMessageText。
func (s *telegramSender) EditMessageCaption(ctx context.Context, chatID int64, messageID int, captionHTML string) error {
	err := s.withRateLimitRetry(ctx, func() error {
		_, rerr := s.b.EditMessageCaption(ctx, &tgbot.EditMessageCaptionParams{
			ChatID:    chatID,
			MessageID: messageID,
			Caption:   captionHTML,
			ParseMode: models.ParseModeHTML,
		})
		return rerr
	})
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), errMessageNotModified) {
		return nil
	}
	return classifyBotError(err)
}

// asRateLimit 是 429 错误的唯一解包点：retry、分类、等待秒数共用。
func asRateLimit(err error) (*tgbot.TooManyRequestsError, bool) {
	var tooMany *tgbot.TooManyRequestsError
	if errors.As(err, &tooMany) {
		return tooMany, true
	}
	return nil, false
}

// classifyBotError 把 go-telegram/bot 的错误归类为 AppError。
func classifyBotError(err error) error {
	if err == nil {
		return nil
	}
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		return ae
	}
	if _, limited := asRateLimit(err); limited {
		return apperr.Wrap(apperr.CodeRateLimited, err)
	}
	return apperr.Wrap(apperr.CodeSendFailed, err)
}
