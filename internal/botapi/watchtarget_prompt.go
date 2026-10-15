package botapi

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/watch"
)

// watchtarget_prompt.go — 监听源专属转发目标的 Bot 选择面板：
//   - /watch 提交成功后（普通源含 pending、私有邀请活动申请）立即弹出；
//   - /watchtarget <源> 随时重开（重选/清除，仅申请人本人）；
//   - 复用 inputPrompter 的单会话单面板、TTL、归属校验与原子消费；
//     面板消息本身即 promptMessageID，回调只带 prompt ID + 候选序号，
//     频道 ID 留在 pending 内，伪造/重放按钮拿不到目标。
//
// 设置/暂存的归属、路由与循环校验全部在 watch.Service（Options.WatchForward）
// 内完成，本层只做候选展示与结果文案。

const (
	watchTargetPageSize    = 8
	expiredWatchTargetText = "本次专属目标选择已失效，请重新发送 /watchtarget。"
)

// watchTargetUsage 是 /watchtarget 的参数与规则提示。
const watchTargetUsage = "用法：/watchtarget 频道\n\n" +
	"为你的监听源选择专属转发目标（你自己的频道/超级群组）：源内新消息除缓存预热外，还会转发一份到该目标。\n" +
	"• 目标需先在本机器人用 /bind 绑定并保持有效\n" +
	"• 目标不能是监听源或缓存频道（防循环转发）\n" +
	"• /watch 提交成功后也会立即弹出选择；私有邀请申请在生效前同样可选择（生效后自动启用）"

// watchTargetCandidate 是面板候选（绑定标题快照 + 数字 ID）。
type watchTargetCandidate struct {
	ChannelID int64
	Title     string
}

// watchTargetSelection 是一次选择面板的输入：源路径（sourceID）与活动
// 邀请申请路径（inviteID）二选一，title 为源/申请展示名。
type watchTargetSelection struct {
	sourceID   int64
	inviteID   int64
	title      string
	current    int64 // 当前专属目标频道 ID（0=未设置；⭐ 标记）
	candidates []watchTargetCandidate
}

func watchTargetPageCount(count int) int {
	if count <= 0 {
		return 0
	}
	return (count + watchTargetPageSize - 1) / watchTargetPageSize
}

func watchTargetCandidateLabel(c watchTargetCandidate) string {
	if c.Title != "" {
		return c.Title
	}
	return strconv.FormatInt(c.ChannelID, 10)
}

// handleWatchTarget 处理 /watchtarget <频道>：解析本人监听源并打开选择面板。
func handleWatchTarget(ctx context.Context, opt Options, snd delivery.Sender, from models.User, chatID int64, text string) {
	if opt.WatchForward == nil {
		sendText(ctx, opt, snd, chatID, "该功能当前未启用，请联系管理员开通。")
		return
	}
	arg := commandArgument(text)
	if arg == "" {
		promptForInput(ctx, opt, snd, chatID, "/watchtarget",
			"请回复本消息粘贴要设置的监听源频道或群组（5 分钟内有效）；如需放弃，请发送“取消”。",
			"粘贴监听源链接", watchTargetUsage)
		return
	}
	row, err := opt.WatchForward.UserSourceByTarget(ctx, from.ID, arg)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			sendText(ctx, opt, snd, chatID, "没有找到你添加的这个监听源（/watchlist 查看列表）。")
			return
		}
		ae := apperr.From(err)
		if opt.Log != nil {
			opt.Log.Warn("/watchtarget 源解析失败", "user_id", from.ID, "code", ae.Code, "error", err.Error())
		}
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	title := row.Title
	if title == "" {
		title = fmt.Sprintf("频道 %d", row.ChannelID)
	}
	offerWatchTargetSelection(ctx, opt, snd, from.ID, chatID, row, 0, title)
}

// maybeOfferWatchTarget 在 /watch 成功结果后立即打开选择面板：普通源
// （提交/生效/幂等命中，且源受理 Bot 就是当前 Bot——路由不符时选择无从
// 谈起，交由 /watchtarget 在受理 Bot 上引导）与活动邀请申请（暂存）。
func maybeOfferWatchTarget(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID, botID int64, outcome watch.SubmitOutcome) {
	if opt.WatchForward == nil || opt.ShowWatchTargetSelection == nil {
		return
	}
	switch outcome.Kind {
	case watch.SubmitPending, watch.SubmitActive, watch.SubmitAlreadyMine:
		row := outcome.Channel
		if row.AddedBy == userID && row.BotID != 0 && row.BotID == botID {
			title := outcome.Title
			if title == "" {
				title = row.Title
			}
			offerWatchTargetSelection(ctx, opt, snd, userID, chatID, row, 0, title)
		}
	case watch.SubmitInvitePending, watch.SubmitInviteWaitingTelegram, watch.SubmitInviteWaitingBot:
		if outcome.RequestID != 0 {
			offerWatchTargetSelection(ctx, opt, snd, userID, chatID, store.WatchSource{}, outcome.RequestID, outcome.Title)
		}
	}
}

// offerWatchTargetSelection 查询候选并发送选择面板；候选为空或查询失败时
// 发对应引导/受控文案，不向调用方报错（不打断 /watch 主流程）。
func offerWatchTargetSelection(ctx context.Context, opt Options, snd delivery.Sender,
	userID, chatID int64, src store.WatchSource, inviteID int64, title string) {
	if opt.WatchForward == nil {
		return
	}
	sel := watchTargetSelection{sourceID: src.ChannelID, inviteID: inviteID, title: title}
	if inviteID != 0 {
		candidates, err := opt.WatchForward.EligibleForwardTargetsForInvite(ctx, userID, inviteID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				sendText(ctx, opt, snd, chatID, "该邀请申请已结束或不存在，无法设置专属目标。")
				return
			}
			ae := apperr.From(err)
			if opt.Log != nil {
				opt.Log.Warn("邀请申请专属目标候选查询失败", "user_id", userID, "invite_id", inviteID,
					"code", ae.Code, "error", err.Error())
			}
			sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
			return
		}
		sel.candidates = watchTargetCandidatesOf(candidates)
	} else {
		view := opt.WatchForward.ForwardTargetViewOf(ctx, src)
		sel.current = src.ForwardBindingChannelID
		if view.State == watch.ForwardTargetSet {
			sel.current = view.ChannelID
		}
		candidates, err := opt.WatchForward.EligibleForwardTargets(ctx, userID, src.ChannelID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				sendText(ctx, opt, snd, chatID, "没有找到你添加的这个监听源（/watchlist 查看列表）。")
				return
			}
			ae := apperr.From(err)
			if opt.Log != nil {
				opt.Log.Warn("专属目标候选查询失败", "user_id", userID, "source_channel_id", src.ChannelID,
					"code", ae.Code, "error", err.Error())
			}
			sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
			return
		}
		sel.candidates = watchTargetCandidatesOf(candidates)
	}
	if len(sel.candidates) == 0 {
		if inviteID == 0 && src.BotID == 0 {
			sendText(ctx, opt, snd, chatID,
				"「"+title+"」尚未记录受理 Bot：请重新发送 /watch 该频道更新后再设置专属目标。")
			return
		}
		sendText(ctx, opt, snd, chatID,
			"暂无可选的专属目标：请先用本机器人（受理该源的机器人）发送 /bind 绑定你的频道或超级群组，再发送 /watchtarget 重试。")
		return
	}
	if opt.ShowWatchTargetSelection == nil {
		// 无提示器上下文（回调重放链路）：面板不可用，给出口不静默。
		sendText(ctx, opt, snd, chatID, "当前会话暂时无法弹出选择面板，请在私聊重新发送 /watchtarget。")
		return
	}
	opt.ShowWatchTargetSelection(ctx, snd, sel, userID, chatID)
}

func watchTargetCandidatesOf(bindings []store.ChannelBinding) []watchTargetCandidate {
	out := make([]watchTargetCandidate, 0, len(bindings))
	for _, b := range bindings {
		out = append(out, watchTargetCandidate{ChannelID: b.ChannelID, Title: b.Title})
	}
	return out
}

// showWatchTargetSelection 发送选择面板并把上下文注册进输入提示器
// （updateHandler 注入的默认实现）。与下载目的地选择同构：先发一条锚点
// 消息作为 promptMessageID（提交/取消时的清理对象与回调 key），再发按钮
// 面板作为控制消息——回调数据只含锚点 ID 与候选序号。
func showWatchTargetSelection(ctx context.Context, opt Options, snd delivery.Sender,
	prompter *inputPrompter, sel watchTargetSelection, userID, chatID int64) {
	markupSender, ok := snd.(delivery.MarkupSender)
	if !ok {
		sendText(ctx, opt, snd, chatID, "当前发送通道不支持按钮选择，请在管理端或稍后重试。")
		return
	}
	anchorID, err := markupSender.SendMessageWithMarkup(ctx, chatID,
		"<blockquote>🎯 「"+html.EscapeString(sel.title)+"」专属转发目标</blockquote>", nil)
	if err != nil {
		if opt.Log != nil {
			opt.Log.Warn("专属目标面板锚点发送失败", "chat_id", chatID, "error", err.Error())
		}
		return
	}
	prompter.register(int64(anchorID), "/watchtarget", watchTargetUsage, userID, chatID)
	entry, ok := prompter.beginWatchTargetSelection(int64(anchorID), userID, sel)
	if !ok {
		// 注册后被并发取消等极端窗口：清理锚点，保持单面板语义
		_ = snd.DeleteMessage(ctx, chatID, anchorID)
		return
	}
	entry.chatID = chatID
	showWatchTargetPanel(ctx, opt, snd, prompter, entry)
}

// showWatchTargetPanel 渲染并发送/更新按钮面板（控制消息），与
// showDownloadDestinationSelection 同构。
func showWatchTargetPanel(ctx context.Context, opt Options, snd delivery.Sender,
	prompter *inputPrompter, entry pendingPrompt) {
	text, markup, ok := renderWatchTargetSelection(entry)
	if !ok {
		discardPrompt(ctx, opt, snd, prompter, entry)
		sendText(ctx, opt, snd, entry.chatID, expiredWatchTargetText)
		return
	}
	markupSender, ok := snd.(delivery.MarkupSender)
	if !ok {
		discardPrompt(ctx, opt, snd, prompter, entry)
		sendText(ctx, opt, snd, entry.chatID, "当前发送通道不支持按钮选择，请直接重发 /watchtarget。")
		return
	}
	if entry.controlMessageID != 0 {
		if err := markupSender.EditMessageTextWithMarkup(ctx, entry.chatID, entry.controlMessageID, text, markup); err == nil {
			return
		} else if opt.Log != nil {
			opt.Log.Debug("专属目标面板编辑失败，回退新消息", "chat_id", entry.chatID,
				"message_id", entry.controlMessageID, "error", err.Error())
			if deleteErr := snd.DeleteMessage(ctx, entry.chatID, entry.controlMessageID); deleteErr != nil {
				opt.Log.Debug("旧面板清理失败", "chat_id", entry.chatID,
					"message_id", entry.controlMessageID, "error", deleteErr.Error())
			}
		}
	}
	messageID, err := markupSender.SendMessageWithMarkup(ctx, entry.chatID, text, markup)
	if err != nil {
		if opt.Log != nil {
			opt.Log.Warn("专属目标面板发送失败", "chat_id", entry.chatID, "error", err.Error())
		}
		discardPrompt(ctx, opt, snd, prompter, entry)
		return
	}
	prompter.attachControl(entry.promptMessageID, messageID)
}

// renderWatchTargetSelection 渲染面板（候选 2/行、分页、当前项 ⭐、清除
// 与用法/取消行）。回调数据只含锚点消息 ID 与序号，不含频道 ID。
func renderWatchTargetSelection(entry pendingPrompt) (string, models.ReplyMarkup, bool) {
	pageCount := watchTargetPageCount(len(entry.watchCandidates))
	if entry.stage != promptAwaitWatchTarget || pageCount == 0 || entry.watchPage < 0 || entry.watchPage >= pageCount {
		return "", nil, false
	}
	start := entry.watchPage * watchTargetPageSize
	end := min(start+watchTargetPageSize, len(entry.watchCandidates))
	rows := make([][]models.InlineKeyboardButton, 0, 7)
	for i := start; i < end; i += 2 {
		row := make([]models.InlineKeyboardButton, 0, 2)
		for j := i; j < min(i+2, end); j++ {
			label := watchTargetCandidateLabel(entry.watchCandidates[j])
			if entry.watchCandidates[j].ChannelID == entry.watchCurrent {
				label = "⭐ " + label
			}
			row = append(row, models.InlineKeyboardButton{
				Text:         label,
				CallbackData: promptWatchTargetCallbackPrefix + strconv.FormatInt(entry.promptMessageID, 10) + ":" + strconv.Itoa(j),
			})
		}
		rows = append(rows, row)
	}
	promptID := strconv.FormatInt(entry.promptMessageID, 10)
	if pageCount > 1 {
		nav := make([]models.InlineKeyboardButton, 0, 2)
		if entry.watchPage > 0 {
			nav = append(nav, models.InlineKeyboardButton{Text: "◀️ 上一页",
				CallbackData: promptWatchTargetPageCallbackPrefix + promptID + ":" + strconv.Itoa(entry.watchPage-1)})
		}
		if entry.watchPage+1 < pageCount {
			nav = append(nav, models.InlineKeyboardButton{Text: "下一页 ▶️",
				CallbackData: promptWatchTargetPageCallbackPrefix + promptID + ":" + strconv.Itoa(entry.watchPage+1)})
		}
		if len(nav) > 0 {
			rows = append(rows, nav)
		}
	}
	clearLabel := "🗑 清除专属目标"
	if entry.watchSourceID == 0 {
		clearLabel = "🗑 清除已暂存目标"
	}
	rows = append(rows,
		[]models.InlineKeyboardButton{{Text: clearLabel, CallbackData: promptWatchTargetClearCallbackPrefix + promptID}},
		[]models.InlineKeyboardButton{
			{Text: "📖 查看用法", CallbackData: promptUsageCallbackPrefix + promptID},
			{Text: "❌ 取消", CallbackData: promptCancelCallbackPrefix + promptID},
		})
	var text strings.Builder
	text.WriteString("<blockquote>🎯 <b>「" + html.EscapeString(entry.watchTitle) + "」专属转发目标</b>\n")
	if entry.watchSourceID != 0 {
		text.WriteString("选择一个目标：源内新消息除缓存预热外，会转发一份到该目标。\n")
	} else {
		text.WriteString("选择一个目标：邀请申请生效后自动启用（生效前可随时重选）。\n")
	}
	if pageCount > 1 {
		fmt.Fprintf(&text, "第 %d/%d 页\n", entry.watchPage+1, pageCount)
	}
	text.WriteString("</blockquote>")
	return text.String(), &models.InlineKeyboardMarkup{InlineKeyboard: rows}, true
}

// applyWatchTargetSelection 消费选择后落库（源设置/邀请暂存），结果与受控
// 错误都以面板收尾文案回显。
func applyWatchTargetSelection(ctx context.Context, opt Options, snd delivery.Sender,
	userID, chatID int64, entry pendingPrompt, candidate watchTargetCandidate) {
	label := html.EscapeString(watchTargetCandidateLabel(candidate))
	if entry.watchSourceID != 0 {
		row, err := opt.WatchForward.SetForwardTarget(ctx, userID, entry.watchSourceID, candidate.ChannelID)
		if err != nil {
			if opt.Log != nil {
				ae := apperr.From(err)
				opt.Log.Warn("专属目标设置失败", "user_id", userID, "source_channel_id", entry.watchSourceID,
					"target_channel_id", candidate.ChannelID, "code", ae.Code, "error", err.Error())
			}
			finishPromptSubmission(ctx, opt, snd, chatID, entry,
				"<blockquote>⚠️ "+html.EscapeString(watchTargetErrorText(err))+"</blockquote>")
			return
		}
		title := row.Title
		if title == "" {
			title = entry.watchTitle
		}
		finishPromptSubmission(ctx, opt, snd, chatID, entry,
			"<blockquote>✅ 已设置「"+html.EscapeString(title)+"」的专属转发目标：<b>"+label+"</b>。源内新消息会自动转发一份到该目标。</blockquote>")
		return
	}
	if _, err := opt.WatchForward.StageInviteForwardTarget(ctx, userID, entry.watchInviteID, candidate.ChannelID); err != nil {
		if opt.Log != nil {
			ae := apperr.From(err)
			opt.Log.Warn("邀请申请专属目标暂存失败", "user_id", userID, "invite_id", entry.watchInviteID,
				"target_channel_id", candidate.ChannelID, "code", ae.Code, "error", err.Error())
		}
		finishPromptSubmission(ctx, opt, snd, chatID, entry,
			"<blockquote>⚠️ "+html.EscapeString(watchTargetErrorText(err))+"</blockquote>")
		return
	}
	finishPromptSubmission(ctx, opt, snd, chatID, entry,
		"<blockquote>✅ 已暂存「"+html.EscapeString(entry.watchTitle)+"」的专属转发目标：<b>"+label+"</b>。邀请申请生效后自动启用。</blockquote>")
}

// applyWatchTargetClear 清除专属目标（源清除幂等；邀请暂存清除）。
func applyWatchTargetClear(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID int64, entry pendingPrompt) {
	if entry.watchSourceID != 0 {
		if _, err := opt.WatchForward.ClearForwardTarget(ctx, userID, entry.watchSourceID); err != nil {
			if opt.Log != nil {
				ae := apperr.From(err)
				opt.Log.Warn("专属目标清除失败", "user_id", userID,
					"source_channel_id", entry.watchSourceID, "code", ae.Code, "error", err.Error())
			}
			finishPromptSubmission(ctx, opt, snd, chatID, entry,
				"<blockquote>⚠️ "+html.EscapeString(watchTargetErrorText(err))+"</blockquote>")
			return
		}
		finishPromptSubmission(ctx, opt, snd, chatID, entry,
			"<blockquote>✅ 已清除「"+html.EscapeString(entry.watchTitle)+"」的专属转发目标。监听继续（仅缓存预热）。</blockquote>")
		return
	}
	if _, err := opt.WatchForward.ClearInviteForwardTarget(ctx, userID, entry.watchInviteID); err != nil {
		if opt.Log != nil {
			ae := apperr.From(err)
			opt.Log.Warn("邀请申请专属目标暂存清除失败", "user_id", userID,
				"invite_id", entry.watchInviteID, "code", ae.Code, "error", err.Error())
		}
		finishPromptSubmission(ctx, opt, snd, chatID, entry,
			"<blockquote>⚠️ "+html.EscapeString(watchTargetErrorText(err))+"</blockquote>")
		return
	}
	finishPromptSubmission(ctx, opt, snd, chatID, entry,
		"<blockquote>✅ 已清除「"+html.EscapeString(entry.watchTitle)+"」暂存的专属转发目标。邀请申请继续，生效后仅缓存预热。</blockquote>")
}

// watchTargetErrorText 提取面向用户的错误文案：哨兵链文案由服务层维护
// （中文、无内部细节），AppError 外壳的 Error() 带 code 前缀不可直显，
// 先剥壳再按哨兵匹配；其余一律回落码表，绝不透出原始错误文本。
func watchTargetErrorText(err error) string {
	cause := err
	var ae *apperr.AppError
	if errors.As(err, &ae) && ae.Cause != nil {
		cause = ae.Cause
	}
	switch {
	case errors.Is(cause, watch.ErrForwardTargetInvalid),
		errors.Is(cause, watch.ErrForwardRouteMismatch),
		errors.Is(cause, watch.ErrForwardTargetLoop),
		errors.Is(cause, watch.ErrForwardTargetConflict),
		errors.Is(cause, watch.ErrInviteRequestClosed):
		return cause.Error()
	case errors.Is(cause, store.ErrNotFound):
		return "没有找到你添加的这个监听源（/watchlist 查看列表）。"
	default:
		return apperr.UserText(apperr.From(err).Code)
	}
}
