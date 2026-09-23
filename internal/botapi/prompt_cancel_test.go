package botapi

import (
	"context"
	"strings"
	"testing"
)

func TestFinishPromptCancellationClearsPromptAndShowsNotice(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	finishPromptCancellation(context.Background(), opt, snd, 7, pendingPrompt{
		command:          "/pin",
		ownerID:          7,
		chatID:           7,
		promptMessageID:  101,
		controlMessageID: 102,
	})

	deleted := snd.deletedIDs()
	if len(deleted) != 2 || deleted[0] != 101 || deleted[1] != 102 {
		t.Fatalf("deleted IDs = %v, want prompt and control messages", deleted)
	}
	got := lastText(t, snd)
	if !strings.Contains(got, "已取消") || !strings.Contains(got, "/pin") {
		t.Fatalf("cancellation notice = %q, want command name included", got)
	}
}
