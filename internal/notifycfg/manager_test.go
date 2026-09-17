package notifycfg

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type memorySettings struct {
	mu     sync.Mutex
	value  string
	writes int
}

func (s *memorySettings) GetSetting(_ context.Context, key string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != SettingKey || s.value == "" {
		return "", false, nil
	}
	return s.value, true, nil
}

func (s *memorySettings) SetSetting(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if key != SettingKey {
		return errors.New("unexpected key")
	}
	s.value = value
	s.writes++
	return nil
}

func testKey(seed byte) []byte {
	return []byte(strings.Repeat(string([]byte{seed}), 32))
}

func TestEncryptionRoundTripAndKeyStates(t *testing.T) {
	ctx := context.Background()
	store := &memorySettings{}
	key := testKey('a')
	manager := NewManager(store, key, Options{})
	token := "123456:abcdefghijklmnopqrstuv"
	secretURL := "https://example.com/hooks/private-token"
	secret := "signing-secret"

	if err := manager.SaveBot(ctx, BotInput{Enabled: true, Token: token, ChatID: "-100123"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveWebhook(ctx, WebhookInput{Enabled: true, Format: FormatGeneric, URL: secretURL, Secret: secret}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(store.value, token) || strings.Contains(store.value, secretURL) || strings.Contains(store.value, secret) {
		t.Fatalf("sensitive plaintext leaked into settings: %s", store.value)
	}
	var persisted map[string]any
	if err := json.Unmarshal([]byte(store.value), &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted["version"] != float64(1) {
		t.Fatalf("unexpected document version: %#v", persisted["version"])
	}

	view, err := manager.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Bot.HasToken || !view.Bot.Credential.Available || !view.Webhook.HasURL || !view.Webhook.HasSecret || !view.Webhook.Credential.Available {
		t.Fatalf("unexpected view: %#v", view)
	}

	rotated := NewManager(store, testKey('b'), Options{})
	rotatedView, err := rotated.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rotatedView.Bot.Credential.Available || !strings.Contains(rotatedView.Bot.Credential.Message, "其他密钥") {
		t.Fatalf("key mismatch not expressed: %#v", rotatedView.Bot.Credential)
	}
	if _, err := rotated.loadBotSecrets(ctx); !errors.Is(err, ErrKeyMismatch) {
		t.Fatalf("expected ErrKeyMismatch, got %v", err)
	}

	missing := NewManager(store, nil, Options{})
	missingView, err := missing.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if missingView.Webhook.Credential.Available || !strings.Contains(missingView.Webhook.Credential.Message, "密钥不可用") {
		t.Fatalf("missing key not expressed: %#v", missingView.Webhook.Credential)
	}
}

func TestMissingKeyAndEmptySecretReuse(t *testing.T) {
	ctx := context.Background()
	store := &memorySettings{}
	withoutKey := NewManager(store, nil, Options{})
	err := withoutKey.SaveBot(ctx, BotInput{Token: "123456:abcdefghijklmnopqrstuv", ChatID: "1"})
	if err == nil || !strings.Contains(err.Error(), "WEB_OAUTH_ENCRYPTION_KEY") {
		t.Fatalf("expected controlled missing-key error, got %v", err)
	}

	key := testKey('c')
	manager := NewManager(store, key, Options{})
	if err := manager.SaveBot(ctx, BotInput{Enabled: true, Token: "123456:abcdefghijklmnopqrstuv", ChatID: "1"}); err != nil {
		t.Fatal(err)
	}
	before := store.value
	if err := manager.SaveBot(ctx, BotInput{Enabled: true, ChatID: "2"}); err != nil {
		t.Fatal(err)
	}
	bot, err := manager.loadBotSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bot.Token != "123456:abcdefghijklmnopqrstuv" || bot.ChatID != "2" {
		t.Fatalf("credential was not reused: %#v", bot)
	}
	if before == store.value {
		t.Fatal("non-sensitive update was not persisted")
	}
}

func TestSaveIsAtomicWhenWebhookFails(t *testing.T) {
	ctx := context.Background()
	store := &memorySettings{}
	manager := NewManager(store, testKey('z'), Options{})

	err := manager.Save(ctx, ConfigInput{
		Bot:     BotInput{Enabled: true, Token: "123456:abcdefghijklmnopqrstuv", ChatID: "-100"},
		Webhook: WebhookInput{Enabled: true, Format: FormatGeneric, URL: "http://invalid.example/hook"},
	})
	if err == nil {
		t.Fatal("expected webhook validation error")
	}
	if store.writes != 0 || store.value != "" {
		t.Fatalf("partial settings write occurred: writes=%d value=%s", store.writes, store.value)
	}
	view, err := manager.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if view.Bot.HasToken || view.Bot.ChatID != "" {
		t.Fatalf("bot was partially saved: %#v", view.Bot)
	}
}

func TestSaveWritesBothChannelsOnce(t *testing.T) {
	ctx := context.Background()
	store := &memorySettings{}
	manager := NewManager(store, testKey('y'), Options{})
	if err := manager.Save(ctx, ConfigInput{
		Bot:     BotInput{Enabled: true, Token: "123456:abcdefghijklmnopqrstuv", ChatID: "-100"},
		Webhook: WebhookInput{Enabled: true, Format: FormatGeneric, URL: "https://example.com/hook", Secret: "secret"},
	}); err != nil {
		t.Fatal(err)
	}
	if store.writes != 1 {
		t.Fatalf("expected one SetSetting call, got %d", store.writes)
	}
	view, err := manager.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Bot.HasToken || !view.Webhook.HasURL || !view.Webhook.HasSecret {
		t.Fatalf("both channels were not saved: %#v", view)
	}
}

func TestValidation(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(&memorySettings{}, testKey('d'), Options{})
	cases := []struct {
		name string
		fn   func() error
	}{
		{"bot token", func() error { return manager.SaveBot(ctx, BotInput{Token: "bad"}) }},
		{"chat id", func() error { return manager.SaveBot(ctx, BotInput{ChatID: "abc"}) }},
		{"http webhook", func() error {
			return manager.SaveWebhook(ctx, WebhookInput{Format: FormatGeneric, URL: "http://example.com"})
		}},
		{"userinfo webhook", func() error {
			return manager.SaveWebhook(ctx, WebhookInput{Format: FormatGeneric, URL: "https://user:pass@example.com/hook"})
		}},
		{"unknown format", func() error { return manager.SaveWebhook(ctx, WebhookInput{Format: "unknown"}) }},
		{"discord secret", func() error {
			return manager.SaveWebhook(ctx, WebhookInput{Format: FormatDiscord, Secret: "not-supported"})
		}},
		{"invalid header", func() error {
			return manager.SaveWebhook(ctx, WebhookInput{Format: FormatGeneric, GenericSignatureHeader: "Bad Header", URL: "https://example.com"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestUnknownChannelIsPreserved(t *testing.T) {
	store := &memorySettings{value: `{"version":1,"channels":[{"id":"future","type":"webhook","format":"bark","config":{"future_secret":{"opaque":true}}}]}`}
	manager := NewManager(store, testKey('u'), Options{})
	if err := manager.SaveBot(context.Background(), BotInput{Token: "123456:abcdefghijklmnopqrstuv", ChatID: "1"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(store.value, `"id":"future"`) || !strings.Contains(store.value, `"opaque":true`) {
		t.Fatalf("future channel was not preserved: %s", store.value)
	}
	if got := len(Descriptors()); got != 4 {
		t.Fatalf("unexpected descriptor count: %d", got)
	}
}

func TestBotSendAndRecentChat(t *testing.T) {
	ctx := context.Background()
	store := &memorySettings{}
	token := "123456:abcdefghijklmnopqrstuv"
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":2,"message":{"chat":{"id":-200,"type":"group","title":"Recent"}}},{"update_id":1,"message":{"chat":{"id":100,"type":"private","first_name":"Old"}}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	manager := NewManager(store, testKey('e'), Options{HTTPClient: server.Client(), BotAPIBaseURL: server.URL})
	if err := manager.SaveBot(ctx, BotInput{Enabled: true, Token: token, ChatID: "-200"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.TestSaved(ctx, ChannelBot, "Spore 通知测试成功"); err != nil {
		t.Fatal(err)
	}
	chat, err := manager.RecentBotChat(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if chat.ID != "-200" || chat.Title != "Recent" || len(methods) != 2 {
		t.Fatalf("unexpected bot result: chat=%#v methods=%v", chat, methods)
	}
}
