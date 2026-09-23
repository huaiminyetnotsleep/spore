package botapi

// reply_anchor.go — 引用回复交互：命令作为对 bot 消息的回复发送时
//（Telegram 原生命令用法），按消息坐标（bot_id+chat_id+message_id，
// 坐标 bot 私有）反查 sent_messages 锚点定位请求，让 /pin、/cancel 直接
// 作用于被回复消息对应的任务，无需用户复制链接。锚点覆盖：提交占位、
// 重试补发占位（status）、投递到私聊的媒体（media）、失败通知（failure）；
// 频道副本坐标（channel_copy）不作回复入口（bot 收频道消息依赖管理员
// 权限与 privacy 设置），仅供 /pin 事后补置顶定位。

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// 引用回复交互的受控中文文案（与 apperr 文案同风格）。
const (
	replyAnchorMissText = "未找到该消息对应的任务。\n\n请回复本机器人发出的任务消息（进度提示、投递结果或失败通知）再使用命令；也可直接使用命令加链接，如 /pin 消息链接。"

	cancelReplyJustEndedText = "该任务刚刚已结束，未能取消。"
	cancelReplySucceededText = "该任务已完成，无法取消。"
	cancelReplyFailedText    = "该任务此前已失败，无需取消。"
	cancelReplyCancelledText = "该任务已取消。"

	pinReplyMarkedText        = "已标记置顶：该任务完成后，副本将置顶到您绑定的频道/群组。"
	pinReplyAlreadyMarkedText = "该任务已标记置顶，完成时会置顶到绑定频道/群组。"
	pinReplyRaceText          = "任务状态刚刚发生变化，请重新回复该消息使用命令。"
	pinReplyAlreadyPinnedText = "该任务已置顶。"
	pinReplyNoCopiesText      = "该任务完成时未产生频道副本（当时无绑定或频道同步关闭），无法事后置顶。"
	pinReplyFailedText        = "该任务未成功完成，无法置顶。"
	pinReplyCancelledText     = "该任务已取消，无法置顶。"
	pinReplyUnavailableText   = "置顶功能当前不可用，请稍后重试。"
)

// resolveReplyAnchor 把引用回复解析为请求行：按（接收 bot、chat、消息 ID）
// 反查 sent_messages 锚点。未命中（历史消息、跨 bot、他人请求、用户自己
// 的消息）回引导文案；存储故障回受控错误文案。均返回 ok=false。
func resolveReplyAnchor(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID, replyMsgID int64) (store.Request, bool) {
	botID := opt.Bot.Get().ID
	_, r, err := opt.Access.ResolveOwnSentMessage(ctx, userID, botID, chatID, replyMsgID)
	if err == nil {
		return r, true
	}
	if errors.Is(err, store.ErrNotFound) {
		opt.Log.Info("引用回复锚点未命中", "user_id", userID, "chat_id", chatID,
			"bot_id", botID, "message_id", replyMsgID)
		sendText(ctx, opt, snd, chatID, replyAnchorMissText)
		return store.Request{}, false
	}
	ae := apperr.From(err)
	opt.Log.Error("引用回复锚点解析失败", "user_id", userID, "message_id", replyMsgID,
		"code", ae.Code, "error", err.Error())
	sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
	return store.Request{}, false
}

// handleCancelReply 处理 /cancel 引用回复：取消被回复消息对应的在途任务。
// 准入与链接形态一致（access 层权威校验，不另做 requireEnabled）。
func handleCancelReply(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID, replyMsgID int64) {
	r, ok := resolveReplyAnchor(ctx, opt, snd, userID, chatID, replyMsgID)
	if !ok {
		return
	}
	switch r.Status {
	case store.RequestQueued, store.RequestProcessing:
		n, err := opt.Access.CancelOwnByID(ctx, userID, r.ID)
		if err != nil {
			ae := apperr.From(err)
			opt.Log.Error("/cancel 引用回复处理失败", "user_id", userID,
				"request_id", r.ID, "code", ae.Code, "error", err.Error())
			sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
			return
		}
		if n == 1 {
			sendText(ctx, opt, snd, chatID, "已取消该任务。")
		} else {
			sendText(ctx, opt, snd, chatID, cancelReplyJustEndedText)
		}
	case store.RequestSucceeded:
		sendText(ctx, opt, snd, chatID, cancelReplySucceededText)
	case store.RequestFailed:
		sendText(ctx, opt, snd, chatID, cancelReplyFailedText)
	case store.RequestCancelled:
		sendText(ctx, opt, snd, chatID, cancelReplyCancelledText)
	}
}

// handlePinReply 处理 /pin 引用回复：在途任务补置顶标记（worker 收尾读
// 请求行，天然继承）；已完成任务按落库的频道副本组首坐标事后补置顶。
// 准入与链接形态一致（requireEnabled）。
func handlePinReply(ctx context.Context, opt Options, snd delivery.Sender, from models.User, chatID, replyMsgID int64) {
	if !requireEnabled(ctx, opt, snd, from.ID, chatID) {
		return
	}
	r, ok := resolveReplyAnchor(ctx, opt, snd, from.ID, chatID, replyMsgID)
	if !ok {
		return
	}
	switch r.Status {
	case store.RequestQueued, store.RequestProcessing:
		if r.Pin {
			sendText(ctx, opt, snd, chatID, pinReplyAlreadyMarkedText)
			return
		}
		marked, err := opt.Access.MarkOwnRequestPin(ctx, from.ID, r.ID)
		if err != nil {
			ae := apperr.From(err)
			opt.Log.Error("/pin 引用回复补标失败", "user_id", from.ID,
				"request_id", r.ID, "code", ae.Code, "error", err.Error())
			sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
			return
		}
		if marked {
			sendText(ctx, opt, snd, chatID, pinReplyMarkedText)
		} else {
			// 竞态：任务恰在补标前落终态（条件更新 affected=0）
			sendText(ctx, opt, snd, chatID, pinReplyRaceText)
		}
	case store.RequestSucceeded:
		if r.Pin && r.PinTotal > 0 && r.PinOK == r.PinTotal {
			sendText(ctx, opt, snd, chatID, pinReplyAlreadyPinnedText)
			return
		}
		if opt.Channels == nil {
			sendText(ctx, opt, snd, chatID, pinReplyUnavailableText)
			return
		}
		outcome, found, err := opt.Channels.PinExistingCopies(ctx, from.ID, r.ID)
		if err != nil {
			ae := apperr.From(err)
			opt.Log.Error("/pin 引用回复补置顶失败", "user_id", from.ID,
				"request_id", r.ID, "code", ae.Code, "error", err.Error())
			sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
			return
		}
		if !found {
			sendText(ctx, opt, snd, chatID, pinReplyNoCopiesText)
			return
		}
		sendText(ctx, opt, snd, chatID, pinExistingResultText(outcome))
	case store.RequestFailed:
		sendText(ctx, opt, snd, chatID, pinReplyFailedText)
	case store.RequestCancelled:
		sendText(ctx, opt, snd, chatID, pinReplyCancelledText)
	}
}

// pinExistingResultText 渲染事后补置顶结果文案（HTML：目标名可能含特殊
// 字符，一律转义；与 queue.pinResultText 同风格，但被回复的消息本身就是
// 上下文，不再附原消息链接）。目标名带跳转链接（与脚注同源，已解绑退化
// 数字 ID 时无链接），多个目标逐行展示。失败目标按错误码给出具体处置指引。
func pinExistingResultText(o queue.PinOutcome) string {
	var pinned, failed []string
	for _, t := range o.Targets {
		label := pinTargetHTML(t)
		if t.Pinned {
			pinned = append(pinned, label)
		} else {
			failed = append(failed, label+"（"+queue.PinFailureHint(t.ErrCode)+"）")
		}
	}
	var b strings.Builder
	if len(pinned) > 0 {
		fmt.Fprintf(&b, "📌 已置顶到：\n%s", strings.Join(pinned, "\n"))
	}
	if len(failed) > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "置顶失败：\n%s", strings.Join(failed, "\n"))
	}
	return b.String()
}

// pinTargetHTML 渲染单个置顶目标为 HTML：URL 非空时展示名即跳转链接
// （与 queue 侧 pinTargetHTML 同语义，已解绑退化数字 ID 无链接时为纯文本）。
func pinTargetHTML(t queue.PinTarget) string {
	label := html.EscapeString(t.Label)
	if t.URL == "" {
		return label
	}
	return `<a href="` + html.EscapeString(t.URL) + `">` + label + `</a>`
}
