package botapi

import (
	"context"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/go-telegram/bot/models"
)

type mutableCloudStatus struct {
	mu      sync.Mutex
	enabled bool
	avail   bool
	def     string
	dests   []string
}

func (s *mutableCloudStatus) Enabled() bool   { s.mu.Lock(); defer s.mu.Unlock(); return s.enabled }
func (s *mutableCloudStatus) Available() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.avail }
func (s *mutableCloudStatus) DefaultDestination() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.def
}
func (s *mutableCloudStatus) DestinationEnabled(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, destination := range s.dests {
		if destination == name {
			return true
		}
	}
	return false
}
func (s *mutableCloudStatus) EnabledDestinations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.dests...)
}

func TestDownloadDestinationCallbackSubmitsOnce(t *testing.T) {
	opt, access, sender := newHarness(t, 4)
	opt.CloudStatus = fakeCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1", "s3-1"}}
	prompter := newInputPrompter()
	prompter.register(101, "/download", downloadUsage, 7, 7)
	prompter.attachControl(101, 102)
	if _, ok := prompter.beginDownloadSelection(101, 7, "https://t.me/example_channel/7",
		[]string{"mega-1", "s3-1"}, "mega-1"); !ok {
		t.Fatal("begin selection failed")
	}

	bot := commandTestBot(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"result":true}`)
	})
	callback := &models.CallbackQuery{
		ID:   "cb-1",
		From: models.User{ID: 7, Username: "alice"},
		Data: promptDestinationCallbackPrefix + "101:1",
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: 102, Chat: models.Chat{ID: 7, Type: models.ChatTypePrivate},
		}},
	}
	handlePromptCallback(context.Background(), opt, sender, bot, callback, prompter)
	handlePromptCallback(context.Background(), opt, sender, bot, callback, prompter)

	submissions := access.submitted()
	if len(submissions) != 1 || submissions[0].CloudDest != "s3-1" {
		t.Fatalf("submissions = %+v, want one s3-1 submission", submissions)
	}
}

func TestDownloadDestinationCallbackRechecksConfiguration(t *testing.T) {
	opt, access, sender := newHarness(t, 4)
	status := &mutableCloudStatus{enabled: true, avail: true, def: "mega-1", dests: []string{"mega-1", "s3-1"}}
	opt.CloudStatus = status
	prompter := newInputPrompter()
	prompter.register(201, "/download", downloadUsage, 7, 7)
	prompter.attachControl(201, 202)
	if _, ok := prompter.beginDownloadSelection(201, 7, "https://t.me/example_channel/8",
		[]string{"mega-1", "s3-1"}, "mega-1"); !ok {
		t.Fatal("begin selection failed")
	}
	status.mu.Lock()
	status.dests = []string{"mega-1"}
	status.mu.Unlock()

	bot := commandTestBot(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"result":true}`)
	})
	callback := &models.CallbackQuery{
		ID: "cb-2", From: models.User{ID: 7}, Data: promptDestinationCallbackPrefix + "201:1",
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: 202, Chat: models.Chat{ID: 7, Type: models.ChatTypePrivate},
		}},
	}
	handlePromptCallback(context.Background(), opt, sender, bot, callback, prompter)

	if len(access.submitted()) != 0 {
		t.Fatal("disabled destination must not submit")
	}
}
