package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/notify"
	"github.com/huaiminyetnotsleep/spore/internal/notifycfg"
)

func TestAPINotificationPolicyAndMuteFlow(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	e := newTestEnvOpts(t, func(c *config.Config, o *Options) {
		c.OAuthEncryptionKey = []byte("0123456789abcdef0123456789abcdef")
		o.Notification = notifycfg.NewManager(o.Store, c.OAuthEncryptionKey, notifycfg.Options{Now: func() time.Time { return now }})
	})
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	unauth := e.do(newJar(t), http.MethodGet, "/api/v1/notification/event-catalog", "", "")
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("catalog without auth=%d", unauth.StatusCode)
	}
	_ = unauth.Body.Close()

	var catalog []notify.EventDefinition
	getAPIJSON(t, e, j, "/api/v1/notification/event-catalog", &catalog)
	if len(catalog) != len(notify.Catalog()) {
		t.Fatalf("catalog size=%d want=%d", len(catalog), len(notify.Catalog()))
	}
	foundNeverRaised := false
	for _, item := range catalog {
		if item.Type == notify.KeyBotInitFailed && item.Category != "" && item.TypeLabel != "" && item.Description != "" {
			foundNeverRaised = true
		}
	}
	if !foundNeverRaised {
		t.Fatal("catalog missing complete bot.init_failed definition")
	}

	var policy notifycfg.Policy
	getAPIJSON(t, e, j, "/api/v1/notification/policy", &policy)
	if policy.Version != notifycfg.PolicyVersion || policy.MinimumSeverity != notify.SeverityWarn {
		t.Fatalf("unexpected default policy: %+v", policy)
	}
	policy.MinimumSeverity = notify.SeverityError
	policy.Events[notify.KeyTempDirUsage] = notifycfg.EventPolicy{
		AdminBadge: notifycfg.OverrideInherit, Bot: notifycfg.OverrideDisabled,
		Webhook: notifycfg.OverrideEnabled, Recovery: notifycfg.OverrideInherit,
	}
	policyJSON, _ := json.Marshal(policy)
	resp := notificationPolicyRequest(t, e, j, csrf, http.MethodPut, "/api/v1/notification/policy", string(policyJSON))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save policy=%d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	_ = resp.Body.Close()
	if !e.containsAction("settings.notification_policy") {
		t.Fatal("policy save audit missing")
	}

	badPolicy := policy
	badPolicy.MinimumSeverity = "fatal"
	badJSON, _ := json.Marshal(badPolicy)
	resp = notificationPolicyRequest(t, e, j, csrf, http.MethodPut, "/api/v1/notification/policy", string(badJSON))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid policy=%d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	_ = resp.Body.Close()

	muteBody := `{"id":"client-value-ignored","name":"云盘维护","match_mode":"category","category":"system_alert","event_types":[],"channels":["bot","webhook"],"starts_at":0,"ends_at":` +
		jsonNumber(now.Add(time.Hour).UnixMilli()) + `,"permanent":false,"enabled":true}`
	resp = notificationPolicyRequest(t, e, j, "", http.MethodPost, "/api/v1/notification/mutes", muteBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("create mute without csrf=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()
	resp = notificationPolicyRequest(t, e, j, csrf, http.MethodPost, "/api/v1/notification/mutes", muteBody)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create mute=%d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	var created notifycfg.MuteSchedule
	decodeAPIJSON(t, bodyOf(t, resp), &created)
	if created.ID == "" || created.ID == "client-value-ignored" {
		t.Fatalf("server must assign mute id: %+v", created)
	}
	if !e.containsAction("notification.mute.create") {
		t.Fatal("create mute audit missing")
	}

	created.Name = "云盘维护延长"
	created.Permanent = true
	created.EndsAt = 0
	updateJSON, _ := json.Marshal(created)
	resp = notificationPolicyRequest(t, e, j, csrf, http.MethodPut, "/api/v1/notification/mutes/"+created.ID, string(updateJSON))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update mute=%d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	_ = resp.Body.Close()
	if !e.containsAction("notification.mute.update") {
		t.Fatal("update mute audit missing")
	}

	var mutes []notifycfg.MuteSchedule
	getAPIJSON(t, e, j, "/api/v1/notification/mutes", &mutes)
	if len(mutes) != 1 || mutes[0].Name != "云盘维护延长" {
		t.Fatalf("unexpected mute list: %+v", mutes)
	}

	resp = notificationPolicyRequest(t, e, j, csrf, http.MethodDelete, "/api/v1/notification/mutes/"+created.ID, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete mute=%d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	_ = resp.Body.Close()
	if !e.containsAction("notification.mute.delete") {
		t.Fatal("delete mute audit missing")
	}
	resp = notificationPolicyRequest(t, e, j, csrf, http.MethodDelete, "/api/v1/notification/mutes/"+created.ID, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete missing mute=%d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestAPINotificationPolicyRequiresManager(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	for _, path := range []string{"/api/v1/notification/policy", "/api/v1/notification/mutes"} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s without manager=%d", path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func notificationPolicyRequest(t *testing.T, e *testEnv, j *jar, csrf, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if csrf != "" {
		req.Header.Set(apiCSRFHeader, csrf)
	}
	if cookie := j.header(); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func jsonNumber(value int64) string {
	b, _ := json.Marshal(value)
	return string(b)
}
