package notifycfg

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestGenericPayloadAndSignature(t *testing.T) {
	cfg := webhookSecrets{
		URL: "https://example.com/hook", Secret: "generic-secret",
		Config: storedWebhook{GenericSignatureHeader: "X-Custom-Signature"},
	}
	body, headers, target, err := buildGeneric(cfg, "hello", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if target != cfg.URL || string(body) != `{"text":"hello"}` {
		t.Fatalf("unexpected generic request: %q %q", target, body)
	}
	mac := hmac.New(sha256.New, []byte(cfg.Secret))
	_, _ = mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := headers.Get("X-Custom-Signature"); got != expected {
		t.Fatalf("signature mismatch: %q != %q", got, expected)
	}
}

func TestFeishuPayloadAndSignature(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cfg := webhookSecrets{
		URL: "https://example.com/feishu", Secret: "feishu-secret",
		Config: storedWebhook{FeishuOpenIDs: []string{"ou_123"}, FeishuAtAll: true},
	}
	body, _, _, err := buildFeishu(cfg, "hello", now)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Timestamp string            `json:"timestamp"`
		Sign      string            `json:"sign"`
		Content   map[string]string `json:"content"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(payload.Timestamp+"\n"+cfg.Secret))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if payload.Timestamp != strconv.FormatInt(now.Unix(), 10) || payload.Sign != expected {
		t.Fatalf("unexpected feishu signature: %#v", payload)
	}
	if !strings.Contains(payload.Content["text"], `user_id="ou_123"`) || !strings.Contains(payload.Content["text"], `user_id="all"`) {
		t.Fatalf("mentions missing: %q", payload.Content["text"])
	}
}

func TestDingTalkPayloadAndSignature(t *testing.T) {
	now := time.Unix(1_700_000_000, 123_000_000)
	cfg := webhookSecrets{
		URL: "https://example.com/dingtalk?access_token=private", Secret: "ding-secret",
		Config: storedWebhook{DingTalkMobiles: []string{"13800138000"}, DingTalkAtAll: true},
	}
	body, _, target, err := buildDingTalk(cfg, "hello", now)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	timestamp := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(cfg.Secret))
	_, _ = mac.Write([]byte(timestamp + "\n" + cfg.Secret))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if u.Query().Get("timestamp") != timestamp || u.Query().Get("sign") != expected || u.Query().Get("access_token") != "private" {
		t.Fatalf("unexpected dingtalk query: %s", u.RawQuery)
	}
	var payload struct {
		At struct {
			Mobiles []string `json:"atMobiles"`
			AtAll   bool     `json:"isAtAll"`
		} `json:"at"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.At.Mobiles) != 1 || !payload.At.AtAll {
		t.Fatalf("unexpected dingtalk payload: %s", body)
	}
}

func TestDiscordAllowedMentions(t *testing.T) {
	cfg := webhookSecrets{
		URL: "https://example.com/discord",
		Config: storedWebhook{
			DiscordUserIDs: []string{"1"}, DiscordRoleIDs: []string{"2"}, DiscordEveryone: true,
		},
	}
	body, _, _, err := buildDiscord(cfg, "hello", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Allowed struct {
			Parse []string `json:"parse"`
			Users []string `json:"users"`
			Roles []string `json:"roles"`
		} `json:"allowed_mentions"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Allowed.Parse) != 1 || payload.Allowed.Parse[0] != "everyone" || payload.Allowed.Users[0] != "1" || payload.Allowed.Roles[0] != "2" {
		t.Fatalf("unexpected discord allowed_mentions: %s", body)
	}
}

func TestRemoteBusinessErrorDoesNotLeak(t *testing.T) {
	const responseSecret = "remote-secret-body"
	var receivedBody []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":19001,"msg":"` + responseSecret + ` ` + r.URL.String() + `"}`))
	}))
	defer server.Close()

	store := &memorySettings{}
	manager := NewManager(store, testKey('f'), Options{HTTPClient: server.Client()})
	inputSecret := "local-signing-secret"
	if err := manager.SaveWebhook(context.Background(), WebhookInput{
		Enabled: true, Format: FormatFeishu, URL: server.URL + "/hook/private-token", Secret: inputSecret,
	}); err != nil {
		t.Fatal(err)
	}
	err := manager.TestSaved(context.Background(), ChannelWebhook, "Spore 通知测试成功")
	if err == nil || !strings.Contains(err.Error(), "飞书通知服务拒绝") {
		t.Fatalf("expected controlled business error, got %v", err)
	}
	for _, sensitive := range []string{responseSecret, inputSecret, server.URL, "private-token"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("error leaked %q: %v", sensitive, err)
		}
	}
	if len(receivedBody) == 0 {
		t.Fatal("webhook request was not sent")
	}
}

func TestHTTPFailureDoesNotLeakURLOrResponse(t *testing.T) {
	const secretBody = "upstream-private-response"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(secretBody))
	}))
	defer server.Close()
	manager := NewManager(&memorySettings{}, testKey('g'), Options{HTTPClient: server.Client()})
	if err := manager.SaveWebhook(context.Background(), WebhookInput{Enabled: true, Format: FormatGeneric, URL: server.URL + "/secret-path"}); err != nil {
		t.Fatal(err)
	}
	err := manager.TestSaved(context.Background(), ChannelWebhook, "Spore 通知测试成功")
	if err == nil {
		t.Fatal("expected HTTP error")
	}
	if strings.Contains(err.Error(), secretBody) || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "secret-path") {
		t.Fatalf("error leaked remote details: %v", err)
	}
}
