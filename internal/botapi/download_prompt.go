package botapi

import (
	"context"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// handleDownloadPromptInput 处理无参 /download 的 ForceReply 链接输入。
// 直接命令 /download 链接仍走默认目的地；只有本交互路径会按启用目的地数量
// 自动选择或展示按钮。此阶段不调用 Access.Submit，不创建请求或扣额度。
func handleDownloadPromptInput(ctx context.Context, opt Options, snd delivery.Sender, prompter *inputPrompter,
	from models.User, chatID int64, entry pendingPrompt, linkText string) {
	cs, ok := requireDownloadReady(ctx, opt, snd, from.ID, chatID)
	if !ok {
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

	destinations := orderedDownloadDestinations(cs.EnabledDestinations(), cs.DefaultDestination())
	switch len(destinations) {
	case 0:
		discardPrompt(ctx, opt, snd, prompter, entry)
		sendText(ctx, opt, snd, chatID, cloudDisabledText)
	case 1:
		consumed, ok := prompter.consume(entry.promptMessageID, from.ID)
		if !ok {
			sendText(ctx, opt, snd, chatID, expiredDownloadSelectionText)
			return
		}
		destination := destinations[0]
		finishPromptSubmission(ctx, opt, snd, chatID, consumed,
			"<blockquote>☁️ 已自动选择唯一目的地 <b>"+destination+"</b>，正在提交。</blockquote>")
		handleUpdate(ctx, opt, snd, from, chatID,
			"/download "+destination+" "+strings.TrimSpace(linkText), 0)
	default:
		selection, ok := prompter.beginDownloadSelection(entry.promptMessageID, from.ID,
			linkText, destinations, cs.DefaultDestination())
		if !ok {
			sendText(ctx, opt, snd, chatID, expiredDownloadSelectionText)
			return
		}
		showDownloadDestinationSelection(ctx, opt, snd, prompter, selection)
	}
}

func orderedDownloadDestinations(destinations []string, defaultDestination string) []string {
	seen := make(map[string]struct{}, len(destinations))
	ordered := make([]string, 0, len(destinations))
	for _, destination := range destinations {
		destination = strings.TrimSpace(destination)
		if destination == "" {
			continue
		}
		if _, ok := seen[destination]; ok {
			continue
		}
		seen[destination] = struct{}{}
		ordered = append(ordered, destination)
	}
	if defaultDestination == "" {
		return ordered
	}
	for i, destination := range ordered {
		if destination != defaultDestination || i == 0 {
			continue
		}
		copy(ordered[1:i+1], ordered[0:i])
		ordered[0] = destination
		break
	}
	return ordered
}

func discardPrompt(ctx context.Context, opt Options, snd delivery.Sender, prompter *inputPrompter, entry pendingPrompt) {
	if _, ok := prompter.consume(entry.promptMessageID, entry.ownerID); ok {
		clearPromptMessages(ctx, opt, snd, entry.chatID, entry)
	}
}
