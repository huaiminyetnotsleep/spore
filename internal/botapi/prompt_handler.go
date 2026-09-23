package botapi

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
)

const (
	promptCancelCallbackPrefix = "pi:cancel:"
	promptUsageCallbackPrefix  = "pi:usage:"
)

func sendPromptInput(ctx context.Context, opt Options, snd delivery.Sender, prompter *inputPrompter, chatID int64, command, html, placeholder, usage string) {
	markupSender, ok := snd.(delivery.MarkupSender)
	if !ok {
		sendText(ctx, opt, snd, chatID, html)
		return
	}
	// 提示与按钮说明统一用 blockquote + 图标排版，与普通回复、取消提示在
	// 视觉上区分（Telegram blockquote 无颜色参数，类型区分靠 emoji）。
	promptID, err := markupSender.SendMessageWithMarkup(ctx, chatID,
		"<blockquote>💬 "+html+"</blockquote>", &models.ForceReply{
			ForceReply:            true,
			InputFieldPlaceholder: placeholder,
		})
	if err != nil {
		if opt.Log != nil {
			opt.Log.Warn("输入提示发送失败", "chat_id", chatID, "command", command, "error", err.Error())
		}
		return
	}
	prompter.register(int64(promptID), command, usage, chatID, chatID)

	promptIDText := strconv.FormatInt(int64(promptID), 10)
	controlMarkup := &models.InlineKeyboardMarkup{
		InlineKeyboard: [][]models.InlineKeyboardButton{{
			{Text: "📖 查看用法", CallbackData: promptUsageCallbackPrefix + promptIDText},
			{Text: "❌ 取消", CallbackData: promptCancelCallbackPrefix + promptIDText},
		}},
	}
	controlID, err := markupSender.SendMessageWithMarkup(ctx, chatID,
		"<blockquote>ℹ️ 本次操作可点击下方按钮查看用法或取消。</blockquote>", controlMarkup)
	if err != nil {
		if opt.Log != nil {
			opt.Log.Warn("输入提示取消按钮发送失败", "chat_id", chatID, "prompt_message_id", promptID, "error", err.Error())
		}
		return
	}
	prompter.attachControl(int64(promptID), controlID)
}

func finishPromptCancellation(ctx context.Context, opt Options, snd delivery.Sender, chatID int64, entry pendingPrompt) {
	finishPromptCancellationWithNotice(ctx, opt, snd, chatID, entry,
		fmt.Sprintf("已取消 %s 操作，本次不执行任何操作。", entry.command))
}

func finishPromptCancellationWithNotice(ctx context.Context, opt Options, snd delivery.Sender, chatID int64, entry pendingPrompt, notice string) {
	if chatID == 0 {
		chatID = entry.chatID
	}
	if chatID == 0 {
		chatID = entry.ownerID
	}

	// 删除 ForceReply 目标消息，尽量退出客户端的回复态；Telegram Bot API
	// 没有直接清空用户本地草稿的接口，删除目标消息是可用的最佳兜底。
	if entry.promptMessageID != 0 {
		if err := snd.DeleteMessage(ctx, chatID, int(entry.promptMessageID)); err != nil && opt.Log != nil {
			opt.Log.Debug("取消时删除输入提示失败", "chat_id", chatID, "message_id", entry.promptMessageID, "error", err.Error())
		}
	}
	if entry.controlMessageID != 0 {
		if err := snd.DeleteMessage(ctx, chatID, entry.controlMessageID); err != nil && opt.Log != nil {
			opt.Log.Debug("取消时删除控制消息失败", "chat_id", chatID, "message_id", entry.controlMessageID, "error", err.Error())
		}
	}
	sendText(ctx, opt, snd, chatID, "<blockquote>🚫 "+notice+"</blockquote>")
}

func handlePromptCallback(ctx context.Context, opt Options, snd delivery.Sender, b *tgbot.Bot, callback *models.CallbackQuery, prompter *inputPrompter) {
	if callback == nil {
		return
	}
	if _, err := b.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: callback.ID}); err != nil && opt.Log != nil {
		opt.Log.Debug("输入提示按钮响应失败", "callback_id", callback.ID, "error", err.Error())
	}

	switch {
	case strings.HasPrefix(callback.Data, promptUsageCallbackPrefix):
		promptID, ok := parsePromptCallbackID(callback.Data, promptUsageCallbackPrefix)
		if !ok {
			return
		}
		entry, ok := prompter.get(promptID, callback.From.ID)
		if !ok || entry.usage == "" {
			return
		}
		chatID := entry.chatID
		if callback.Message.Message != nil {
			chatID = callback.Message.Message.Chat.ID
		}
		sendText(ctx, opt, snd, chatID, "<blockquote>📖 "+entry.usage+"</blockquote>")
	case strings.HasPrefix(callback.Data, promptCancelCallbackPrefix):
		promptID, ok := parsePromptCallbackID(callback.Data, promptCancelCallbackPrefix)
		if !ok {
			return
		}
		entry, ok := prompter.cancel(promptID, callback.From.ID)
		if !ok {
			return
		}
		chatID := entry.chatID
		if chatID == 0 {
			chatID = entry.ownerID
		}
		if callback.Message.Message != nil {
			chatID = callback.Message.Message.Chat.ID
			entry.controlMessageID = callback.Message.Message.ID
		}
		finishPromptCancellation(ctx, opt, snd, chatID, entry)
	}
}

func parsePromptCallbackID(data, prefix string) (int64, bool) {
	if !strings.HasPrefix(data, prefix) {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(data, prefix), 10, 64)
	return id, err == nil && id != 0
}
