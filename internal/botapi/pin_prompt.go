package botapi

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// pin_prompt.go — 无参 /pin 的 ForceReply 输入模式选择：用户粘贴链接后不
// 直接执行，而是展示「仅置顶 / 置顶+转存网盘」内联键盘。选仅置顶走原
// handlePin 提交链；选置顶+转存经 requireDownloadReady 预检后进入配对提交
// （submitPinWithCloud，云盘固定用默认目的地）。云盘功能未开启时不进面板，
// 行为与历史完全一致。直接命令 /pin <链接> 不经过本流程，保持立即提交。

const expiredPinModeText = "本次操作选择已失效，请重新发送 /pin。"

// handlePinPromptInput 处理无参 /pin 的链接输入：准入与链接校验后，按云盘
// 运行状态分流——不可用时直接按仅置顶提交（附说明），可用时进入模式选择
// 面板。此阶段不调用 Access.Submit，不创建请求或扣额度。
func handlePinPromptInput(ctx context.Context, opt Options, snd delivery.Sender, prompter *inputPrompter,
	from models.User, chatID int64, entry pendingPrompt, linkText string) {
	if !requireEnabled(ctx, opt, snd, from.ID, chatID) {
		discardPrompt(ctx, opt, snd, prompter, entry)
		return
	}
	refs := tmeurl.ParseAll(linkText)
	if len(refs) == 0 {
		discardPrompt(ctx, opt, snd, prompter, entry)
		sendText(ctx, opt, snd, chatID, apperr.UserText(apperr.CodeInvalidURL))
		return
	}
	if len(refs) > effectiveMaxLinks(ctx, opt) {
		discardPrompt(ctx, opt, snd, prompter, entry)
		rejectTooManyLinks(ctx, opt, snd, chatID, refs)
		return
	}

	// 云盘未装配或运行时关闭：与历史行为一致，直接按仅置顶提交
	if opt.CloudStatus == nil || !opt.CloudStatus.Available() || !opt.CloudStatus.Enabled() {
		consumed, ok := prompter.consume(entry.promptMessageID, from.ID)
		if !ok {
			sendText(ctx, opt, snd, chatID, expiredPinModeText)
			return
		}
		finishPromptSubmission(ctx, opt, snd, chatID, consumed,
			"<blockquote>📌 云盘下载未开启，按仅置顶提交。</blockquote>")
		handleUpdate(ctx, opt, snd, from, chatID, "/pin "+strings.TrimSpace(linkText), 0)
		return
	}

	selection, ok := prompter.beginPinMode(entry.promptMessageID, from.ID, linkText)
	if !ok {
		sendText(ctx, opt, snd, chatID, expiredPinModeText)
		return
	}
	showPinModeSelection(ctx, opt, snd, prompter, selection)
}

func renderPinModeSelection(entry pendingPrompt) (string, models.ReplyMarkup, bool) {
	if entry.stage != promptAwaitPinMode {
		return "", nil, false
	}
	promptID := strconv.FormatInt(entry.promptMessageID, 10)
	rows := [][]models.InlineKeyboardButton{
		{
			{Text: "📌 仅置顶", CallbackData: promptPinModeCallbackPrefix + promptID + ":0"},
			{Text: "📌☁️ 置顶+转存网盘", CallbackData: promptPinModeCallbackPrefix + promptID + ":1"},
		},
		{
			{Text: "📖 查看用法", CallbackData: promptUsageCallbackPrefix + promptID},
			{Text: "❌ 取消", CallbackData: promptCancelCallbackPrefix + promptID},
		},
	}
	text := "<blockquote>📌 <b>请选择提交方式</b>\n" +
		"置顶+转存会把媒体同时上传到网盘（默认目的地）；网盘任务将在无其他任务排队时执行。</blockquote>"
	return text, &models.InlineKeyboardMarkup{InlineKeyboard: rows}, true
}

func showPinModeSelection(ctx context.Context, opt Options, snd delivery.Sender,
	prompter *inputPrompter, entry pendingPrompt) {
	text, markup, ok := renderPinModeSelection(entry)
	if !ok {
		discardPrompt(ctx, opt, snd, prompter, entry)
		sendText(ctx, opt, snd, entry.chatID, expiredPinModeText)
		return
	}
	markupSender, ok := snd.(delivery.MarkupSender)
	if !ok {
		discardPrompt(ctx, opt, snd, prompter, entry)
		sendText(ctx, opt, snd, entry.chatID, "当前发送通道不支持按钮选择，请直接发送 /pin 链接。")
		return
	}
	if entry.controlMessageID != 0 {
		if err := markupSender.EditMessageTextWithMarkup(ctx, entry.chatID, entry.controlMessageID, text, markup); err == nil {
			return
		} else {
			if opt.Log != nil {
				opt.Log.Debug("模式选择面板编辑失败，回退新消息", "chat_id", entry.chatID,
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
			opt.Log.Warn("模式选择面板发送失败", "chat_id", entry.chatID, "error", err.Error())
		}
		discardPrompt(ctx, opt, snd, prompter, entry)
		return
	}
	prompter.attachControl(entry.promptMessageID, messageID)
}
