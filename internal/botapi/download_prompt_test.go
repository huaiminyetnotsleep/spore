package botapi

import (
	"context"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func downloadPromptHarness(t *testing.T, destinations []string, defaultDestination string) (Options, *fakeAccess, *fakeSender, *inputPrompter, pendingPrompt) {
	t.Helper()
	opt, access, sender := newHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: defaultDestination, dests: destinations}
	prompter := newInputPrompter()
	prompter.register(101, "/download", downloadUsage, 7, 7)
	prompter.attachControl(101, 102)
	entry, ok := prompter.get(101, 7)
	if !ok {
		t.Fatal("prompt registration failed")
	}
	return opt, access, sender, prompter, entry
}

func TestDownloadPromptNoDestination(t *testing.T) {
	opt, access, sender, prompter, entry := downloadPromptHarness(t, nil, "")
	handleDownloadPromptInput(context.Background(), opt, sender, prompter,
		models.User{ID: 7}, 7, entry, "https://t.me/example_channel/1")

	if len(access.submitted()) != 0 {
		t.Fatal("no destination must not submit")
	}
	if got := lastText(t, sender); got != cloudDisabledText {
		t.Fatalf("response = %q, want cloud disabled", got)
	}
	if _, ok := prompter.get(101, 7); ok {
		t.Fatal("failed destination preparation should consume prompt")
	}
}

func TestDownloadPromptSingleDestinationAutoSubmits(t *testing.T) {
	opt, access, sender, prompter, entry := downloadPromptHarness(t, []string{"mega-2"}, "mega-1")
	handleDownloadPromptInput(context.Background(), opt, sender, prompter,
		models.User{ID: 7, Username: "alice"}, 7, entry, "https://t.me/example_channel/7")

	submissions := access.submitted()
	if len(submissions) != 1 || submissions[0].CloudDest != "mega-2" || submissions[0].Ref.MessageID != 7 {
		t.Fatalf("submissions = %+v, want unique destination", submissions)
	}
	if _, ok := prompter.get(101, 7); ok {
		t.Fatal("auto-submitted prompt should be consumed")
	}
}

func TestDownloadPromptMultipleDestinationsShowsSelection(t *testing.T) {
	opt, access, sender, prompter, entry := downloadPromptHarness(t,
		[]string{"s3-1", "mega-1", "webdav-1"}, "mega-1")
	handleDownloadPromptInput(context.Background(), opt, sender, prompter,
		models.User{ID: 7}, 7, entry, "https://t.me/example_channel/8")

	if len(access.submitted()) != 0 {
		t.Fatal("multiple destinations must wait for selection")
	}
	selection, ok := prompter.get(101, 7)
	if !ok || selection.stage != promptAwaitDownloadDestination || selection.input == "" {
		t.Fatalf("selection = (%+v, %v)", selection, ok)
	}
	if got := strings.Join(selection.destinations, ","); got != "mega-1,s3-1,webdav-1" {
		t.Fatalf("ordered destinations = %q", got)
	}
	if len(sender.markupEdits) != 1 {
		t.Fatalf("markup edits = %d, want selector edit", len(sender.markupEdits))
	}
	markup, ok := sender.markupEdits[0].(*models.InlineKeyboardMarkup)
	if !ok || len(markup.InlineKeyboard) < 2 {
		t.Fatalf("selector markup = %+v", sender.markupEdits[0])
	}
}

func TestDownloadPromptRejectsBeforeExposingDestinations(t *testing.T) {
	opt, access, sender, prompter, entry := downloadPromptHarness(t, []string{"secret-1", "secret-2"}, "secret-1")
	access.statusCode = apperr.CodeCloudDownloadDenied
	handleDownloadPromptInput(context.Background(), opt, sender, prompter,
		models.User{ID: 7}, 7, entry, "https://t.me/example_channel/9")

	if len(access.submitted()) != 0 || len(sender.markupEdits) != 0 {
		t.Fatalf("permission denial must not submit or show destinations")
	}
	for _, text := range sender.texts() {
		if strings.Contains(text, "secret-") {
			t.Fatalf("destination leaked in response %q", text)
		}
	}
}
