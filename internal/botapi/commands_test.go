package botapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
)

func commandTestBot(t *testing.T, handler http.HandlerFunc) *tgbot.Bot {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	b, err := tgbot.New("123:test-token", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func commandRequest(t *testing.T, r *http.Request) commandScopeKey {
	t.Helper()
	if !strings.HasSuffix(r.URL.Path, "/deleteMyCommands") {
		t.Errorf("unexpected method %s", r.URL.Path)
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		t.Error(err)
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	var scope struct {
		Type   string `json:"type"`
		ChatID int64  `json:"chat_id"`
	}
	if err := json.Unmarshal([]byte(r.FormValue("scope")), &scope); err != nil {
		t.Error(err)
	}
	if scope.Type != "chat" {
		t.Errorf("unsafe scope: %q", scope.Type)
	}
	return commandScopeKey{chatID: scope.ChatID, language: r.FormValue("language_code")}
}

func privateStart(chatID int64, code string) *models.Update {
	return &models.Update{Message: &models.Message{Chat: models.Chat{ID: chatID, Type: models.ChatTypePrivate}, From: &models.User{ID: 7, LanguageCode: code}, Text: "/start@bot hello"}}
}

func TestCommandLanguage(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""}, {"zh", "zh"}, {"zh-CN", "zh"}, {"zh-Hant-TW", "zh"}, {"EN-us", "en"}, {"eng", "en"}, {"iw-IL", "he"},
		{"not-a-language", ""}, {"en-!", ""}, {"zz", ""}, {"und", ""}, {"und-CN", ""}, {"x-private", ""}, {"fil", ""}, {"https://token", ""},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := commandLanguage(tc.in); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommandCleanupScopeRetryAndInstanceIsolation(t *testing.T) {
	for _, failedCode := range []string{"", "zh"} {
		t.Run("fail_"+failedCode, func(t *testing.T) {
			opt, _, fs := newHarness(t, 4)
			opt.WrapSender = func(delivery.Sender) delivery.Sender { return fs }
			var mu sync.Mutex
			calls := map[commandScopeKey]int{}
			count := func(key commandScopeKey) int {
				mu.Lock()
				defer mu.Unlock()
				return calls[key]
			}
			b := commandTestBot(t, func(w http.ResponseWriter, r *http.Request) {
				key := commandRequest(t, r)
				if key.chatID != 900 && key.chatID != 901 {
					t.Errorf("used user ID rather than actual chat: %d", key.chatID)
				}
				if len(fs.texts()) == 0 {
					t.Error("cleanup ran before reply")
				}
				mu.Lock()
				calls[key]++
				n := calls[key]
				mu.Unlock()
				if key.language == failedCode && n == 1 {
					io.WriteString(w, `{"ok":true,"result":false}`)
					return
				}
				io.WriteString(w, `{"ok":true,"result":true}`)
			})
			h := updateHandler(opt)
			for range 3 {
				h(context.Background(), b, privateStart(900, "zh-CN"))
			}
			for _, code := range []string{"", "zh"} {
				want := 1
				if code == failedCode {
					want = 2
				}
				if count(commandScopeKey{900, code}) != want {
					t.Fatalf("calls = %v", calls)
				}
			}
			h(context.Background(), b, privateStart(900, "en-US"))
			if count(commandScopeKey{900, "en"}) != 1 {
				t.Fatal("new language not cleaned")
			}
			h(context.Background(), b, privateStart(901, ""))
			if count(commandScopeKey{901, ""}) != 1 {
				t.Fatal("new chat not cleaned")
			}
			updateHandler(opt)(context.Background(), b, privateStart(900, "zh-TW"))
			for _, code := range []string{"", "zh"} {
				want := 2
				if code == failedCode {
					want = 3
				}
				if count(commandScopeKey{900, code}) != want {
					t.Fatalf("instance cache leaked: %v", calls)
				}
			}
			if opt.CleanChatCommands != nil {
				t.Fatal("shared options mutated")
			}
		})
	}
}

func TestCommandCleanupEmptyAndInvalidLanguage(t *testing.T) {
	for _, code := range []string{"", "zh-CN", "zh-Hant-TW", "en-!", "und-CN", "x-private", "fil"} {
		t.Run(code, func(t *testing.T) {
			var got []string
			var mu sync.Mutex
			b := commandTestBot(t, func(w http.ResponseWriter, r *http.Request) {
				key := commandRequest(t, r)
				mu.Lock()
				got = append(got, key.language)
				mu.Unlock()
				io.WriteString(w, `{"ok":true,"result":true}`)
			})
			c := commandCleaner{states: make(map[commandScopeKey]bool)}
			c.clean(context.Background(), b, slog.Default(), 900, code)
			want := []string{""}
			if strings.HasPrefix(code, "zh") {
				want = append(want, "zh")
			}
			mu.Lock()
			defer mu.Unlock()
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
		})
	}
}

func TestCommandCleanupConcurrent(t *testing.T) {
	opt, _, fs := newHarness(t, 4)
	opt.WrapSender = func(delivery.Sender) delivery.Sender { return fs }
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	calls := map[commandScopeKey]int{}
	b := commandTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		key := commandRequest(t, r)
		mu.Lock()
		calls[key]++
		mu.Unlock()
		if key.chatID == 900 && key.language == "" {
			once.Do(func() { close(entered) })
			<-release
		}
		io.WriteString(w, `{"ok":true,"result":true}`)
	})
	h := updateHandler(opt)
	done := make(chan struct{})
	go func() { h(context.Background(), b, privateStart(900, "zh-CN")); close(done) }()
	<-entered
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); h(context.Background(), b, privateStart(900, "zh-TW")) }()
	}
	otherDone := make(chan struct{})
	go func() { h(context.Background(), b, privateStart(901, "en")); close(otherDone) }()
	select {
	case <-otherDone:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("network held shared lock")
	}
	wg.Wait()
	close(release)
	<-done
	mu.Lock()
	defer mu.Unlock()
	for _, key := range []commandScopeKey{{900, ""}, {900, "zh"}, {901, ""}, {901, "en"}} {
		if calls[key] != 1 {
			t.Fatalf("not deduplicated: %v", calls)
		}
	}
}

type startErrorAccess struct{ *fakeAccess }

func (f startErrorAccess) HandleStart(context.Context, access.StartInput) (access.StartOutcome, error) {
	return 0, apperr.Wrap(apperr.CodeStoreUnavailable, errors.New("db down"))
}

func TestCommandCleanupPreservesStartRepliesAndInjection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome access.StartOutcome
		fail    bool
		want    string
	}{
		{"enabled", access.StartWelcome, false, helpText("Spore")},
		{"pending", access.StartPending, false, apperr.UserText(apperr.CodeUserPending)},
		{"disabled", access.StartDisabled, false, apperr.UserText(apperr.CodeUserDisabled)},
		{"archived", access.StartDisabled, false, apperr.UserText(apperr.CodeUserDisabled)},
		{"store-error", 0, true, apperr.UserText(apperr.CodeStoreUnavailable)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt, fa, fs := newHarness(t, 4)
			fa.startOut = tc.outcome
			if tc.fail {
				opt.Access = startErrorAccess{fa}
			}
			opt.SystemName = func(context.Context) string { return "Spore" }
			opt.WrapSender = func(delivery.Sender) delivery.Sender { return fs }
			calls := 0
			opt.CleanChatCommands = func(_ context.Context, id int64, code string) {
				calls++
				if id != 900 || code != "zh-CN" {
					t.Errorf("incorrect cleanup arguments %d %q", id, code)
				}
				if got := fs.texts(); len(got) != calls || got[calls-1] != tc.want {
					t.Errorf("reply changed or cleanup ran first: %v", got)
				}
			}
			h := updateHandler(opt)
			for range 2 {
				h(context.Background(), nil, privateStart(900, "zh-CN"))
			}
			if calls != 2 {
				t.Fatalf("injected cleanup replaced: %d", calls)
			}
		})
	}
}

func TestCommandCleanupFiltersUpdates(t *testing.T) {
	opt, _, fs := newHarness(t, 4)
	opt.WrapSender = func(delivery.Sender) delivery.Sender { return fs }
	opt.CleanChatCommands = func(context.Context, int64, string) { t.Error("unexpected cleanup") }
	h := updateHandler(opt)
	for _, typ := range []models.ChatType{models.ChatTypeGroup, models.ChatTypeSupergroup, models.ChatTypeChannel} {
		u := privateStart(900, "en")
		u.Message.Chat.Type = typ
		h(context.Background(), nil, u)
	}
	h(context.Background(), nil, &models.Update{})
	u := privateStart(900, "en")
	u.Message.From = nil
	h(context.Background(), nil, u)
	if len(fs.texts()) != 0 {
		t.Fatal("non-private or missing sender replied")
	}
	for _, text := range []string{"/help", "/status", "", "hello", "/starting"} {
		u := privateStart(900, "en")
		u.Message.Text = text
		h(context.Background(), nil, u)
	}
}

func TestCommandCleanupCancellationAndTotalBudget(t *testing.T) {
	for _, mode := range []string{"cancelled", "parent-cancel", "parent-deadline", "total-budget"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			stop := make(chan struct{})
			defer close(stop)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancelled" {
				cancel()
			}
			if mode == "parent-deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 80*time.Millisecond)
				defer deadlineCancel()
			}
			b := commandTestBot(t, func(w http.ResponseWriter, r *http.Request) {
				commandRequest(t, r)
				n := calls.Add(1)
				if mode == "parent-cancel" {
					cancel()
				}
				if mode == "total-budget" && n == 1 {
					time.Sleep(1200 * time.Millisecond)
					io.WriteString(w, `{"ok":true,"result":true}`)
					return
				}
				select {
				case <-r.Context().Done():
				case <-stop:
				}
			})
			c := commandCleaner{states: make(map[commandScopeKey]bool)}
			started := time.Now()
			c.clean(ctx, b, slog.New(slog.NewTextHandler(io.Discard, nil)), 900, "zh-CN")
			elapsed := time.Since(started)
			switch mode {
			case "cancelled":
				if calls.Load() != 0 || len(c.states) != 0 {
					t.Fatal("cancelled context made request or cached success")
				}
			case "parent-cancel", "parent-deadline":
				if calls.Load() != 1 || elapsed > time.Second || len(c.states) != 0 {
					t.Fatalf("parent deadline ignored: %v calls=%d", elapsed, calls.Load())
				}
			case "total-budget":
				if calls.Load() != 2 || elapsed < 1800*time.Millisecond || elapsed > 2800*time.Millisecond {
					t.Fatalf("budget not shared: %v calls=%d", elapsed, calls.Load())
				}
				if !c.states[commandScopeKey{900, ""}] || len(c.states) != 1 {
					t.Fatalf("partial success cache: %v", c.states)
				}
			}
		})
	}
}

func TestCommandCleanupLogsRedactedFailures(t *testing.T) {
	for _, response := range []string{`{"ok":false,"error_code":400,"description":"https://api.telegram.org/bot123:test-token/deleteMyCommands"}`, `{"ok":true,"result":false}`, ""} {
		t.Run(response, func(t *testing.T) {
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			var calls atomic.Int32
			b := commandTestBot(t, func(w http.ResponseWriter, r *http.Request) {
				commandRequest(t, r)
				calls.Add(1)
				if response == "" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
					return
				}
				io.WriteString(w, response)
			})
			c := commandCleaner{states: make(map[commandScopeKey]bool)}
			c.clean(context.Background(), b, log, 900, "zh-CN")
			lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
			if len(lines) != 2 {
				t.Fatalf("want two warnings: %s", logs.String())
			}
			for i, line := range lines {
				var fields map[string]any
				if err := json.Unmarshal([]byte(line), &fields); err != nil {
					t.Fatal(err)
				}
				want, wantCode := "chat", ""
				if i == 1 {
					want, wantCode = "language", "zh"
				}
				if fields["level"] != "WARN" || fields["category"] != want ||
					fields["chat_id"] != float64(900) || fields["language_code"] != wantCode || len(fields) != 6 {
					t.Fatalf("unexpected warning: %s", line)
				}
			}
			for _, secret := range []string{"https://", "test-token", "telegram.org", "zh-CN"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatalf("leaked %q", secret)
				}
			}
			if len(c.states) != 0 {
				t.Fatal("failed cleanup cached")
			}
			c.clean(context.Background(), b, log, 900, "zh-CN")
			if calls.Load() != 4 {
				t.Fatalf("failed scopes not retried: %d", calls.Load())
			}
		})
	}
}

func TestRegisterCommandsDefaultNineUnchanged(t *testing.T) {
	var calls atomic.Int32
	b := commandTestBot(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			t.Errorf("unexpected endpoint %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		if r.FormValue("scope") != "" || r.FormValue("language_code") != "" {
			t.Error("default registration changed scope")
		}
		var commands []models.BotCommand
		if err := json.Unmarshal([]byte(r.FormValue("commands")), &commands); err != nil {
			t.Error(err)
		}
		want := []string{"start", "help", "status", "health", "usage", "cancel", "download", "bind", "unbind", "channels", "join", "watch", "unwatch"}
		var got []string
		for _, cmd := range commands {
			got = append(got, cmd.Command)
			if cmd.Description == "" {
				t.Error("empty description")
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("commands changed: %v", got)
		}
		io.WriteString(w, `{"ok":true,"result":true}`)
	})
	if err := RegisterCommands(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("registration calls %d", calls.Load())
	}
}
