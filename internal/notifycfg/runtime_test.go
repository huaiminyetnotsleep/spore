package notifycfg

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/notify"
)

func TestNotifyEventUsesConfiguredBotAndControlledHeader(t *testing.T) {
	store := newPolicySettings()
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			http.NotFound(w, r)
			return
		}
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		body = string(payload)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()

	manager := NewManager(store, testKey('r'), Options{HTTPClient: server.Client(), BotAPIBaseURL: server.URL})
	if err := manager.Save(context.Background(), ConfigInput{
		AutomaticEvents: true,
		Bot:             BotInput{Enabled: true, Token: "123456:abcdefghijklmnopqrstuv", ChatID: "42"},
		Webhook:         WebhookInput{Format: FormatGeneric},
	}); err != nil {
		t.Fatal(err)
	}

	result, err := manager.NotifyEvent(context.Background(), notify.EventNotification{
		SourceName: "我的 Spore", TypeCode: notify.CategorySystemAlert, TypeLabel: "系统告警",
		Category: notify.CategorySystemAlert, Severity: notify.SeverityError,
		EventType: notify.KeySessionOffline, Title: "MTProto 会话离线",
		Body: "请在管理端重新登录。", Count: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Managed || result.Attempted != 1 || result.Delivered != 1 || result.Failed != 0 {
		t.Fatalf("unexpected delivery result: %+v", result)
	}
	for _, want := range []string{"我的 Spore", "系统告警", "MTProto 会话离线", "已发生 2 次"} {
		if !strings.Contains(body, want) {
			t.Fatalf("configured message missing %q: %s", want, body)
		}
	}
}

func TestNotifyEventHonorsEventOverride(t *testing.T) {
	store := newPolicySettings()
	manager := NewManager(store, testKey('s'), Options{})
	if err := manager.Save(context.Background(), ConfigInput{
		AutomaticEvents: true,
		Bot:             BotInput{}, Webhook: WebhookInput{Format: FormatGeneric},
	}); err != nil {
		t.Fatal(err)
	}
	policy, err := manager.PolicySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	policy.Events[notify.KeySessionOffline] = EventPolicy{
		AdminBadge: OverrideInherit, Bot: OverrideDisabled,
		Webhook: OverrideInherit, Recovery: OverrideInherit,
	}
	if err := manager.SavePolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}

	result, err := manager.NotifyEvent(context.Background(), notify.EventNotification{
		EventType: notify.KeySessionOffline, Severity: notify.SeverityError,
		Title: "MTProto 会话离线", Body: "受控文案", Count: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Managed || result.Attempted != 0 || result.Delivered != 0 || result.Failed != 0 {
		t.Fatalf("disabled event should not be delivered: %+v", result)
	}
}

func TestNotifyEventFallsBackWhenAutomaticEventsDisabled(t *testing.T) {
	manager := NewManager(&memorySettings{}, testKey('f'), Options{})
	result, err := manager.NotifyEvent(context.Background(), notify.EventNotification{
		EventType: notify.KeySessionOffline, Severity: notify.SeverityError,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Managed || result.Attempted != 0 || result.Delivered != 0 {
		t.Fatalf("automatic events disabled should not manage delivery: %+v", result)
	}
}
