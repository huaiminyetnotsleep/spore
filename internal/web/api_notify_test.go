package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/notifycfg"
)

func TestAPINotificationConfigFlow(t *testing.T) {
	const token = "123456:ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const secret = "generic-signing-secret"

	var mu sync.Mutex
	var paths []string
	var bodies []string
	remote := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		if raw, err := io.ReadAll(r.Body); err == nil {
			bodies = append(bodies, string(raw))
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getUpdates"):
			_, _ = io.WriteString(w, `{"ok":true,"result":[{"update_id":9,"message":{"chat":{"id":-1009988,"type":"supergroup","title":"通知群"}}}]}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer remote.Close()

	e := newTestEnvOpts(t, func(c *config.Config, o *Options) {
		c.OAuthEncryptionKey = []byte("0123456789abcdef0123456789abcdef")
		o.Notification = notifycfg.NewManager(o.Store, c.OAuthEncryptionKey, notifycfg.Options{
			HTTPClient: remote.Client(), BotAPIBaseURL: remote.URL,
		})
	})
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	var initial notifycfg.View
	getAPIJSON(t, e, j, "/api/v1/notification/config", &initial)
	if initial.Version != 1 || initial.Bot.HasToken || initial.Webhook.HasURL {
		t.Fatalf("缺省通知配置不对: %+v", initial)
	}

	body := `{"bot":{"enabled":true,"token":"` + token + `","chat_id":"-1009988"},` +
		`"webhook":{"enabled":true,"format":"generic","url":"` + remote.URL + `/hook","secret":"` + secret + `","generic_signature_header":"X-Spore-Signature"}}`
	put := func(csrfToken string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, e.ts.URL+"/api/v1/notification/config", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if csrfToken != "" {
			req.Header.Set(apiCSRFHeader, csrfToken)
		}
		if h := j.header(); h != "" {
			req.Header.Set("Cookie", h)
		}
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := put("")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT 缺 CSRF 应 403，得到 %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp = put(csrf)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存通知配置应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var saved struct {
		OK      bool           `json:"ok"`
		Message string         `json:"message"`
		Config  notifycfg.View `json:"config"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &saved)
	if !saved.OK || !saved.Config.Bot.HasToken || !saved.Config.Webhook.HasURL || !saved.Config.Webhook.HasSecret {
		t.Fatalf("保存响应不对: %+v", saved)
	}
	if !e.containsAction("settings.notification") {
		t.Error("保存通知配置应写 settings.notification 审计")
	}

	raw, ok, err := e.st.GetSetting(t.Context(), notifycfg.SettingKey)
	if err != nil || !ok {
		t.Fatalf("读取通知 settings 失败: ok=%v err=%v", ok, err)
	}
	for _, sensitive := range []string{token, secret, remote.URL} {
		if strings.Contains(raw, sensitive) {
			t.Fatalf("settings 不得含敏感明文 %q", sensitive)
		}
	}

	getResp := e.do(j, http.MethodGet, "/api/v1/notification/config", "", "")
	getBody := bodyOf(t, getResp)
	if strings.Contains(getBody, token) || strings.Contains(getBody, secret) || strings.Contains(getBody, remote.URL) {
		t.Fatalf("GET 响应泄露敏感值: %s", getBody)
	}

	resp = e.apiPost(j, "/api/v1/notification/test", csrf, `{"channel":"bot"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Bot 测试应成功: %d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	resp = e.apiPost(j, "/api/v1/notification/test", csrf, `{"channel":"webhook"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Webhook 测试应成功: %d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	resp = e.apiPost(j, "/api/v1/notification/bot/chat-id", csrf, `{}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("获取 Chat ID 应成功: %d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	var chat struct {
		ChatID string `json:"chat_id"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &chat)
	if chat.ChatID != "-1009988" {
		t.Fatalf("Chat ID 不对: %+v", chat)
	}

	mu.Lock()
	defer mu.Unlock()
	joined, _ := json.Marshal(paths)
	for _, want := range []string{"/bot" + token + "/sendMessage", "/hook", "/bot" + token + "/getUpdates"} {
		if !strings.Contains(string(joined), want) {
			t.Errorf("远端未收到 %s，请求=%s", want, joined)
		}
	}
	// 测试消息正文必须携带系统设置中的品牌名（缺省 Spore），不得硬编码
	bodiesJoined, _ := json.Marshal(bodies)
	if !strings.Contains(string(bodiesJoined), "Spore 通知测试成功") {
		t.Errorf("测试消息应携带系统名称，请求体=%s", bodiesJoined)
	}
}

func TestAPINotificationRequiresManager(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	resp := e.do(j, http.MethodGet, "/api/v1/notification/config", "", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未注入通知管理器应 503，得到 %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}
