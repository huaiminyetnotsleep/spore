package botapi

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestInputPrompterUsageLookupKeepsPending(t *testing.T) {
	p := newInputPrompter()
	p.register(101, "/pin", "pin usage", 7, 7)

	entry, ok := p.get(101, 7)
	if !ok || entry.usage != "pin usage" {
		t.Fatalf("usage lookup = (%+v, %v), want pending pin usage", entry, ok)
	}
	if _, outcome, _ := p.resolve("https://t.me/example/1", 101, 7); outcome != promptExecute {
		t.Fatal("usage lookup should not consume pending prompt")
	}
}

func TestRenderDownloadDestinationSelection(t *testing.T) {
	entry := pendingPrompt{
		stage:              promptAwaitDownloadDestination,
		promptMessageID:    123,
		destinations:       []string{"mega-1", "s3-1", "webdav-1"},
		defaultDestination: "mega-1",
	}
	text, rawMarkup, ok := renderDownloadDestinationSelection(entry)
	if !ok || !strings.Contains(text, "请选择下载目的地") {
		t.Fatalf("render = (%q, %T, %v)", text, rawMarkup, ok)
	}
	markup, ok := rawMarkup.(*models.InlineKeyboardMarkup)
	if !ok {
		t.Fatalf("markup = %T, want InlineKeyboardMarkup", rawMarkup)
	}
	if len(markup.InlineKeyboard) != 3 {
		t.Fatalf("rows = %d, want two destination rows plus controls", len(markup.InlineKeyboard))
	}
	if len(markup.InlineKeyboard[0]) != 2 || len(markup.InlineKeyboard[1]) != 1 {
		t.Fatalf("destination rows = %+v, want two buttons per row", markup.InlineKeyboard[:2])
	}
	if got := markup.InlineKeyboard[0][0].Text; got != "⭐ mega-1（默认）" {
		t.Fatalf("default button = %q", got)
	}
	for _, row := range markup.InlineKeyboard {
		for _, button := range row {
			if strings.Contains(button.CallbackData, "t.me") || len([]byte(button.CallbackData)) > 64 {
				t.Fatalf("unsafe callback data %q", button.CallbackData)
			}
		}
	}
}

func TestRenderDownloadDestinationPagination(t *testing.T) {
	destinations := []string{"d1", "d2", "d3", "d4", "d5", "d6", "d7", "d8", "d9"}
	entry := pendingPrompt{
		stage:           promptAwaitDownloadDestination,
		promptMessageID: 456,
		destinations:    destinations,
		destinationPage: 1,
	}
	text, rawMarkup, ok := renderDownloadDestinationSelection(entry)
	if !ok || !strings.Contains(text, "第 2/2 页") {
		t.Fatalf("pagination text = %q, ok=%v", text, ok)
	}
	markup := rawMarkup.(*models.InlineKeyboardMarkup)
	if len(markup.InlineKeyboard[0]) != 1 || markup.InlineKeyboard[0][0].Text != "d9" {
		t.Fatalf("last page destinations = %+v", markup.InlineKeyboard)
	}
	if !strings.Contains(markup.InlineKeyboard[1][0].CallbackData, promptPageCallbackPrefix) {
		t.Fatalf("missing previous page button: %+v", markup.InlineKeyboard)
	}
}

func TestPromptInputOffersUsageAndCancelButtons(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	prompter := newInputPrompter()
	sendPromptInput(context.Background(), opt, snd, prompter, 7, "/pin", "请输入链接", "粘贴链接", "pin usage")

	if len(snd.markups) != 2 {
		t.Fatalf("markup count = %d, want prompt and control messages", len(snd.markups))
	}
	if _, ok := snd.markups[0].(*models.ForceReply); !ok {
		t.Fatalf("prompt markup = %T, want ForceReply", snd.markups[0])
	}
	control, ok := snd.markups[1].(*models.InlineKeyboardMarkup)
	if !ok || len(control.InlineKeyboard) != 1 || len(control.InlineKeyboard[0]) != 2 {
		t.Fatalf("control markup = %+v, want usage and cancel buttons", snd.markups[1])
	}
	if control.InlineKeyboard[0][0].Text != "📖 查看用法" || control.InlineKeyboard[0][1].Text != "❌ 取消" {
		t.Fatalf("control buttons = %+v", control.InlineKeyboard[0])
	}
}
