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
	return s.SendMessageWithMarkup(ctx, chatID, html, nil)
}

func (s *telegramSender) SendMessageWithMarkup(ctx context.Context, chatID int64, html string, markup models.ReplyMarkup) (int, error) {
	var sent *models.Message
	err := s.withRateLimitRetry(ctx, func() error {
		m, rerr := s.b.SendMessage(ctx, &tgbot.SendMessageParams{
			ChatID:      chatID,
			Text:        html,
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: markup,
		})
		sent = m
		return rerr
	})
	if err != nil {
		return 0, ClassifyBotError(err)
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
	return ClassifyBotError(err)
}

// errMessageNotModified 是 Telegram 对"编辑后内容与原文相同"的 400 响应：
// 语义上等于编辑成功（文本已是目标内容），按 no-op 处理而非失败。
const errMessageNotModified = "message is not modified"

// CopyMessages 从 fromChatID 整组复制 bot 已发送的消息（无请求体，可安全
// 重放，适用限流重试）。源消息已被删除、源 chat 不可访问等失败经
// ClassifyBotError 分类，由调用方决定回落策略。
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
		return nil, ClassifyBotError(err)
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
	return s.EditMessageTextWithMarkup(ctx, chatID, messageID, html, nil)
}

func (s *telegramSender) EditMessageTextWithMarkup(ctx context.Context, chatID int64, messageID int, html string, markup models.ReplyMarkup) error {
	err := s.withRateLimitRetry(ctx, func() error {
		_, rerr := s.b.EditMessageText(ctx, &tgbot.EditMessageTextParams{
			ChatID:      chatID,
			MessageID:   messageID,
			Text:        html,
			ParseMode:   models.ParseModeHTML,
			ReplyMarkup: markup,
		})
		return rerr
	})
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), errMessageNotModified) {
		return nil
	}
	return ClassifyBotError(err)
}

// CopyMessage 单条复制并覆盖 caption（干净副本构造用；无请求体，可安全
// 重放，适用限流重试）。返回新消息 ID；源消息已删除等失败经 ClassifyBotError
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
		return 0, ClassifyBotError(err)
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
	return ClassifyBotError(err)
}

// asRateLimit 是 429 错误的唯一解包点：retry、分类、等待秒数共用。
func asRateLimit(err error) (*tgbot.TooManyRequestsError, bool) {
	var tooMany *tgbot.TooManyRequestsError
	if errors.As(err, &tooMany) {
		return tooMany, true
	}
	return nil, false
}

// sendTargetPatterns 是"发送目标不可用"的响应描述特征（Bot API 400/403
// description 的小写匹配）：此类失败重试无效，需要用户重新绑定频道或
// 管理员处理机器人权限，与可重试的临时失败语义不同。
var sendTargetPatterns = []string{
	"chat not found",              // 目标聊天不存在（机器人不在频道/已退出）
	"bot was blocked by the user", // 私聊被用户拉黑
	"bot was kicked",              // 机器人被踢出群组/频道
	"bot is not a member",         // 机器人不是频道成员
	"need administrator rights",   // 频道内无发言权限
	"not enough rights",           // 权限不足（发言/发媒体/置顶被限制）
	"have no rights",              // 部分权限错误不含 "not enough rights"
	"upgraded to a supergroup",    // 群升级迁移 chat（原 chat 已失效，需重新绑定）
	"user is deactivated",         // 目标用户已注销
	"chat was deactivated",        // 聊天/频道已被停用（删除或封禁）
	"channel was banned",          // 频道因违规被封禁
}

// channelGonePatterns 是"频道本体已消失"的子集（sendTargetPatterns 内）：
// 命中即频道不存在或已被封——副本永远发不进去，绑定自动解绑（软）；
// 其余目标不可用（被踢/权限不足）只是机器人被移出，重新加回即可恢复，
// 不自动解绑只提醒。
var channelGonePatterns = []string{
	"chat not found",
	"chat was deactivated",
	"channel was banned",
}

// IsChannelGoneError 报告发送失败是否因目标频道本体已消失（不存在/停用/
// 被封禁）：用于频道绑定自动解绑判定。仅对 ClassifyBotError 归为
// SEND_TARGET_INVALID 的错误进一步细分（原始错误文本含 Bot API
// description，与分类同源小写匹配），其余（网络/限流/权限不足等）恒 false。
func IsChannelGoneError(err error) bool {
	if apperr.From(ClassifyBotError(err)).Code != apperr.CodeSendTargetInvalid {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, p := range channelGonePatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// ClassifyBotError 把 go-telegram/bot 的错误归类为 AppError：限流与
// 目标不可用单列，网络传输故障归 NETWORK_ERROR，其余 BOT_SEND_FAILED 兜底。
// 导出供 binding（绑定校验/频道置顶）等 Bot API 调用方复用同一套分类，
// 保证跨包错误码口径一致。
func ClassifyBotError(err error) error {
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
	msg := strings.ToLower(err.Error())
	for _, p := range sendTargetPatterns {
		if strings.Contains(msg, p) {
			return apperr.Wrap(apperr.CodeSendTargetInvalid, err)
		}
	}
	if apperr.IsTransportFailure(err) {
		return apperr.Wrap(apperr.CodeNetworkError, err)
	}
	return apperr.Wrap(apperr.CodeSendFailed, err)
}
