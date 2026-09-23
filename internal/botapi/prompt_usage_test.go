package botapi

import (
	"context"
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
