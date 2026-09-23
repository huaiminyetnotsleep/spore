package botapi

import (
	"context"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
)

func TestHandleUpdateNoArgUsesPromptInput(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	opt.Watch = &fakeWatch{}
	var gotCommand, gotText, gotPlaceholder string
	opt.PromptInput = func(_ context.Context, _ delivery.Sender, _ int64, command, text, placeholder, _ string) {
		gotCommand = command
		gotText = text
		gotPlaceholder = placeholder
	}

	run(opt, snd, "/watch")
	if gotCommand != "/watch" {
		t.Fatalf("prompt command = %q, want /watch", gotCommand)
	}
	if gotText == "" || gotPlaceholder == "" {
		t.Fatalf("prompt payload = (%q, %q), want non-empty text and placeholder", gotText, gotPlaceholder)
	}
	if len(snd.texts()) != 0 {
		t.Fatalf("prompt callback should replace direct text send, got %v", snd.texts())
	}
}

func TestHandleUpdateWatchlistDoesNotPrompt(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	opt.Watch = &fakeWatch{}
	prompted := false
	opt.PromptInput = func(context.Context, delivery.Sender, int64, string, string, string, string) {
		prompted = true
	}

	run(opt, snd, "/watchlist")
	if prompted {
		t.Fatal("/watchlist should query directly, not prompt for input")
	}
	if len(snd.texts()) != 1 || snd.texts()[0] != "你还没有监听源。发送 /watch 频道链接添加。" {
		t.Fatalf("watchlist response = %v", snd.texts())
	}
}
