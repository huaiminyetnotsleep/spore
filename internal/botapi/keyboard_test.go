package botapi

import (
	"testing"

	"github.com/go-telegram/bot/models"
)

func TestPersistentCommandKeyboard(t *testing.T) {
	markup, ok := persistentCommandKeyboard().(*models.ReplyKeyboardMarkup)
	if !ok {
		t.Fatalf("keyboard type = %T, want ReplyKeyboardMarkup", persistentCommandKeyboard())
	}
	if !markup.IsPersistent || !markup.ResizeKeyboard || markup.OneTimeKeyboard {
		t.Fatalf("keyboard flags = %+v, want persistent resized non-one-time keyboard", markup)
	}
	if len(markup.Keyboard) != 1 || len(markup.Keyboard[0]) != 3 {
		t.Fatalf("keyboard layout = %+v, want one row with three buttons", markup.Keyboard)
	}
	want := []string{"/pin", "/download", "/cancel"}
	for i, button := range markup.Keyboard[0] {
		if button.Text != want[i] {
			t.Errorf("button %d = %q, want %q", i, button.Text, want[i])
		}
	}
}

func TestHandleHelpUsesPersistentCommandKeyboard(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	run(opt, snd, "/help")
	if len(snd.markups) != 1 {
		t.Fatalf("help markup count = %d, want 1", len(snd.markups))
	}
	if _, ok := snd.markups[0].(*models.ReplyKeyboardMarkup); !ok {
		t.Fatalf("help markup type = %T, want ReplyKeyboardMarkup", snd.markups[0])
	}
}
