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
	promptCancelCallbackPrefix      = "pi:cancel:"
	promptUsageCallbackPrefix       = "pi:usage:"
	promptDestinationCallbackPrefix = "pi:dest:"
	promptPageCallbackPrefix        = "pi:page:"
	downloadDestinationPageSize     = 8
	expiredDownloadSelectionText    = "本次目的地选择已失效，请重新发送 /download。"
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

func showDownloadDestinationSelection(ctx context.Context, opt Options, snd delivery.Sender,
	prompter *inputPrompter, entry pendingPrompt) {
	text, markup, ok := renderDownloadDestinationSelection(entry)
	if !ok {
		discardPrompt(ctx, opt, snd, prompter, entry)
		sendText(ctx, opt, snd, entry.chatID, expiredDownloadSelectionText)
		return
	}
	markupSender, ok := snd.(delivery.MarkupSender)
	if !ok {
		discardPrompt(ctx, opt, snd, prompter, entry)
		sendText(ctx, opt, snd, entry.chatID, "当前发送通道不支持目的地选择，请直接发送 /download 目的地 链接。")
		return
	}
	if entry.controlMessageID != 0 {
		if err := markupSender.EditMessageTextWithMarkup(ctx, entry.chatID, entry.controlMessageID, text, markup); err == nil {
			return
		} else {
			if opt.Log != nil {
				opt.Log.Debug("目的地选择面板编辑失败，回退新消息", "chat_id", entry.chatID,
					"message_id", entry.controlMessageID, "error", err.Error())
			}
			if deleteErr := snd.DeleteMessage(ctx, entry.chatID, entry.controlMessageID); deleteErr != nil && opt.Log != nil {
				opt.Log.Debug("旧控制消息清理失败", "chat_id", entry.chatID,
					"message_id", entry.controlMessageID, "error", deleteErr.Error())
			}
		}
	}
	messageID, err := markupSender.SendMessageWithMarkup(ctx, entry.chatID, text, markup)
	if err != nil {
		if opt.Log != nil {
			opt.Log.Warn("目的地选择面板发送失败", "chat_id", entry.chatID, "error", err.Error())
		}
		discardPrompt(ctx, opt, snd, prompter, entry)
		return
	}
	prompter.attachControl(entry.promptMessageID, messageID)
}

func renderDownloadDestinationSelection(entry pendingPrompt) (string, models.ReplyMarkup, bool) {
	pageCount := downloadDestinationPageCount(len(entry.destinations))
	if entry.stage != promptAwaitDownloadDestination || pageCount == 0 || entry.destinationPage < 0 || entry.destinationPage >= pageCount {
		return "", nil, false
	}
	start := entry.destinationPage * downloadDestinationPageSize
	end := min(start+downloadDestinationPageSize, len(entry.destinations))
	rows := make([][]models.InlineKeyboardButton, 0, 6)
	for i := start; i < end; i += 2 {
		row := make([]models.InlineKeyboardButton, 0, 2)
		for j := i; j < min(i+2, end); j++ {
			destination := entry.destinations[j]
			label := destination
			if destination == entry.defaultDestination {
				label = "⭐ " + destination + "（默认）"
			}
			row = append(row, models.InlineKeyboardButton{
				Text:         label,
				CallbackData: promptDestinationCallbackPrefix + strconv.FormatInt(entry.promptMessageID, 10) + ":" + strconv.Itoa(j),
			})
		}
		rows = append(rows, row)
	}
	if pageCount > 1 {
		nav := make([]models.InlineKeyboardButton, 0, 2)
		if entry.destinationPage > 0 {
			nav = append(nav, models.InlineKeyboardButton{Text: "◀️ 上一页",
				CallbackData: promptPageCallbackPrefix + strconv.FormatInt(entry.promptMessageID, 10) + ":" + strconv.Itoa(entry.destinationPage-1)})
		}
		if entry.destinationPage+1 < pageCount {
			nav = append(nav, models.InlineKeyboardButton{Text: "下一页 ▶️",
				CallbackData: promptPageCallbackPrefix + strconv.FormatInt(entry.promptMessageID, 10) + ":" + strconv.Itoa(entry.destinationPage+1)})
		}
		if len(nav) > 0 {
			rows = append(rows, nav)
		}
	}
	promptID := strconv.FormatInt(entry.promptMessageID, 10)
	rows = append(rows, []models.InlineKeyboardButton{
		{Text: "📖 查看用法", CallbackData: promptUsageCallbackPrefix + promptID},
		{Text: "❌ 取消", CallbackData: promptCancelCallbackPrefix + promptID},
	})
	text := "<blockquote>☁️ <b>请选择下载目的地</b>"
	if pageCount > 1 {
		text += fmt.Sprintf("\n第 %d/%d 页", entry.destinationPage+1, pageCount)
	}
	text += "</blockquote>"
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: rows}, true
}

func downloadDestinationPageCount(destinationCount int) int {
	if destinationCount <= 0 {
		return 0
	}
	return (destinationCount + downloadDestinationPageSize - 1) / downloadDestinationPageSize
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
	clearPromptMessages(ctx, opt, snd, chatID, entry)
	sendText(ctx, opt, snd, chatID, "<blockquote>🚫 "+notice+"</blockquote>")
}

func clearPromptMessages(ctx context.Context, opt Options, snd delivery.Sender, chatID int64, entry pendingPrompt) {
	if chatID == 0 {
		chatID = entry.chatID
	}
	// 删除 ForceReply 目标消息，尽量退出客户端的回复态；Telegram Bot API
	// 没有直接清空用户本地草稿的接口，删除目标消息是可用的最佳兜底。
	if entry.promptMessageID != 0 {
		if err := snd.DeleteMessage(ctx, chatID, int(entry.promptMessageID)); err != nil && opt.Log != nil {
			opt.Log.Debug("提示消息清理失败", "chat_id", chatID, "message_id", entry.promptMessageID, "error", err.Error())
		}
	}
	if entry.controlMessageID != 0 {
		if err := snd.DeleteMessage(ctx, chatID, entry.controlMessageID); err != nil && opt.Log != nil {
			opt.Log.Debug("控制消息清理失败", "chat_id", chatID, "message_id", entry.controlMessageID, "error", err.Error())
		}
	}
}

func finishPromptSubmission(ctx context.Context, opt Options, snd delivery.Sender, chatID int64, entry pendingPrompt, text string) {
	if entry.promptMessageID != 0 {
		if err := snd.DeleteMessage(ctx, chatID, int(entry.promptMessageID)); err != nil && opt.Log != nil {
			opt.Log.Debug("提交时删除输入提示失败", "chat_id", chatID, "message_id", entry.promptMessageID, "error", err.Error())
		}
	}
	if entry.controlMessageID == 0 {
		sendText(ctx, opt, snd, chatID, text)
		return
	}
	if markupSender, ok := snd.(delivery.MarkupSender); ok {
		if err := markupSender.EditMessageTextWithMarkup(ctx, chatID, entry.controlMessageID, text,
			&models.InlineKeyboardMarkup{}); err == nil {
			return
		} else if opt.Log != nil {
			opt.Log.Debug("提交状态消息更新失败", "chat_id", chatID, "message_id", entry.controlMessageID, "error", err.Error())
		}
	}
	sendText(ctx, opt, snd, chatID, text)
}

func handlePromptCallback(ctx context.Context, opt Options, snd delivery.Sender, b *tgbot.Bot, callback *models.CallbackQuery, prompter *inputPrompter) {
	if callback == nil {
		return
	}
	if _, err := b.AnswerCallbackQuery(ctx, &tgbot.AnswerCallbackQueryParams{CallbackQueryID: callback.ID}); err != nil && opt.Log != nil {
		opt.Log.Debug("输入提示按钮响应失败", "callback_id", callback.ID, "error", err.Error())
	}

	switch {
	case strings.HasPrefix(callback.Data, promptDestinationCallbackPrefix):
		promptID, index, ok := parsePromptIndexedCallback(callback.Data, promptDestinationCallbackPrefix)
		if !ok {
			return
		}
		entry, destination, ok := prompter.selectDownloadDestination(promptID, callback.From.ID, index)
		if !ok {
			sendPromptCallbackNotice(ctx, opt, snd, callback, expiredDownloadSelectionText)
			return
		}
		chatID := callbackChatID(callback, entry.chatID)
		if callback.Message.Message != nil {
			entry.controlMessageID = callback.Message.Message.ID
		}
		finishPromptSubmission(ctx, opt, snd, chatID, entry,
			"<blockquote>☁️ 已选择目的地 <b>"+destination+"</b>，正在提交。</blockquote>")
		handleUpdate(ctx, opt, snd, callback.From, chatID,
			"/download "+destination+" "+entry.input, 0)
	case strings.HasPrefix(callback.Data, promptPageCallbackPrefix):
		promptID, page, ok := parsePromptIndexedCallback(callback.Data, promptPageCallbackPrefix)
		if !ok {
			return
		}
		entry, ok := prompter.setDownloadPage(promptID, callback.From.ID, page)
		if !ok {
			sendPromptCallbackNotice(ctx, opt, snd, callback, expiredDownloadSelectionText)
			return
		}
		if callback.Message.Message != nil {
			entry.controlMessageID = callback.Message.Message.ID
		}
		showDownloadDestinationSelection(ctx, opt, snd, prompter, entry)
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
			if entry.controlMessageID == 0 {
				entry.controlMessageID = callback.Message.Message.ID
			}
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

func parsePromptIndexedCallback(data, prefix string) (int64, int, bool) {
	if !strings.HasPrefix(data, prefix) {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(data, prefix), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	messageID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || messageID == 0 {
		return 0, 0, false
	}
	index, err := strconv.Atoi(parts[1])
	return messageID, index, err == nil && index >= 0
}

func callbackChatID(callback *models.CallbackQuery, fallback int64) int64 {
	if callback != nil && callback.Message.Message != nil {
		return callback.Message.Message.Chat.ID
	}
	return fallback
}

func sendPromptCallbackNotice(ctx context.Context, opt Options, snd delivery.Sender,
	callback *models.CallbackQuery, text string) {
	chatID := int64(0)
	if callback != nil && callback.Message.Message != nil {
		chatID = callback.Message.Message.Chat.ID
	}
	if chatID != 0 {
		sendText(ctx, opt, snd, chatID, text)
	}
}
