package web

// SPA API（/api/v1）契约测试：未认证 401 JSON、CSRF 失败 403 JSON、
// bootstrap 交付会话级 CSRF token 且不泄露敏感值、演示总览端点、
// 未匹配 API 路由 404 JSON 与 apperr 错误映射。旧页面入口不再注册以
// 对照断言覆盖。

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// decodeAPIJSON 解码 JSON 响应体；失败即中止测试。
func decodeAPIJSON(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(body), v); err != nil {
		t.Fatalf("响应不是合法 JSON：%v（body=%s）", err, body)
	}
}

// requireJSONError 断言响应是统一错误信封且错误码匹配。
func requireJSONError(t *testing.T, status int, contentType, body, wantCode string) {
	t.Helper()
	if ct := contentType; !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("错误响应应为 JSON，得到 Content-Type %q（status=%d）", ct, status)
	}
	var env apiErrorEnvelope
	decodeAPIJSON(t, body, &env)
	if env.Error.Code != wantCode {
		t.Errorf("错误码应为 %s，得到 %q（body=%s）", wantCode, env.Error.Code, body)
	}
	if env.Error.Message == "" {
		t.Error("错误响应应包含受控中文文案")
	}
}

// ---- 未认证：JSON 401，绝不返回登录 HTML ----

func TestAPIUnauthenticatedJSON401(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	for _, path := range []string{"/api/v1/session", "/api/v1/overview"} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("未认证 GET %s 应返回 401，得到 %d", path, resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), apiCodeUnauthorized)
	}

	// 旧页面入口已删除；API 认证中间件与页面入口互不影响。
	resp := e.do(j, http.MethodGet, "/", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("旧首页路径未认证应返回 404，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

// ---- 会话引导：登录态 + 会话级 CSRF token，无第二套安全状态 ----

func TestAPISessionBootstrap(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	resp := e.do(j, http.MethodGet, "/api/v1/session", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bootstrap 应返回 200，得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("bootstrap 应为 JSON，得到 Content-Type %q", ct)
	}
	body := bodyOf(t, resp)

	// 契约字段恰为四个，不夹带会话哈希、IP、UA 等内部字段
	var raw map[string]json.RawMessage
	decodeAPIJSON(t, body, &raw)
	for _, key := range []string{"authenticated", "user", "csrf_token", "expires_at"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("bootstrap 缺少字段 %q（body=%s）", key, body)
		}
	}
	if len(raw) != 4 {
		t.Errorf("bootstrap 应只含 4 个顶层字段，得到 %d（body=%s）", len(raw), body)
	}

	var view apiSessionView
	decodeAPIJSON(t, body, &view)
	if !view.Authenticated {
		t.Error("已登录会话的 bootstrap 应标记 authenticated=true")
	}
	if view.User.Name != "admin" {
		t.Errorf("当前用户应为 admin，得到 %q", view.User.Name)
	}
	if view.CSRFToken == "" {
		t.Fatal("bootstrap 应交付会话级 CSRF token")
	}
	if want := e.clock.Now().Add(sessionTTL).UnixMilli(); view.ExpiresAt != want {
		t.Errorf("expires_at 应为滑动续期后的过期时间 %d，得到 %d", want, view.ExpiresAt)
	}

	// 不泄露任何敏感值：访问密钥、会话 Cookie、登录 CSRF Cookie 不得出现
	for _, secret := range []string{e.key, j.get(sessionCookieName), j.get(loginCSRFCookieName)} {
		if secret != "" && strings.Contains(body, secret) {
			t.Error("bootstrap 响应不得包含密钥或会话凭据")
		}
	}

	// API 下发的 CSRF token 与会话同源（web_sessions.csrf_token）：
	// 直接用它提交受控写端点应通过校验
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/api/v1/session/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(apiCSRFHeader, view.CSRFToken)
	req.Header.Set("Cookie", j.header())
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("bootstrap 的 CSRF token 应可通过 API 写端点校验，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

// ---- API CSRF 中间件：JSON 403，正确 token 放行 ----

func TestAPICSRFMiddleware(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	// 经 bootstrap 取真实会话 CSRF token
	resp := e.do(j, http.MethodGet, "/api/v1/session", "", "")
	var view apiSessionView
	decodeAPIJSON(t, bodyOf(t, resp), &view)

	// 挂载真实生产中间件链验证 CSRF 路径（首个业务写路由随页面相关功能接入）
	h := e.srv.apiAuth(e.srv.apiCSRF(sessionHandler(func(w http.ResponseWriter, _ *http.Request, _ session) {
		writeAPIJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})))
	ts := httptest.NewServer(h)
	defer ts.Close()

	do := func(token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/demo-write", nil)
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		if token != "" {
			req.Header.Set(apiCSRFHeader, token)
		}
		if c := j.header(); c != "" {
			req.Header.Set("Cookie", c)
		}
		hr, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		return hr
	}

	// 缺失 token / 错误 token → 403 JSON CSRF_FAILED
	for name, token := range map[string]string{"缺失": "", "错误": "wrong-token"} {
		hr := do(token)
		if hr.StatusCode != http.StatusForbidden {
			t.Fatalf("%s token 应返回 403，得到 %d", name, hr.StatusCode)
		}
		requireJSONError(t, hr.StatusCode, hr.Header.Get("Content-Type"), bodyOf(t, hr), apiCodeCSRFFailed)
	}

	// bootstrap 交付的正确 token → 200
	hr := do(view.CSRFToken)
	if hr.StatusCode != http.StatusOK {
		t.Fatalf("正确 token 应放行，得到 %d", hr.StatusCode)
	}
	bodyOf(t, hr)
}

// ---- 演示总览端点：完整链路 ----

func TestAPIOverviewEndpoint(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	resp := e.do(j, http.MethodGet, "/api/v1/overview", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview 应返回 200，得到 %d", resp.StatusCode)
	}
	body := bodyOf(t, resp)
	var view apiOverviewView
	decodeAPIJSON(t, body, &view)
	if view.Version != Version {
		t.Errorf("version 应为 %q，得到 %q", Version, view.Version)
	}
	// testEnv 装配 queue.New(8)：队列指标可用且结构完整
	if view.Queue == nil {
		t.Fatal("测试环境应返回队列指标")
	}
	if view.Queue.Cap != 8 {
		t.Errorf("queue.cap 应为 8，得到 %d", view.Queue.Cap)
	}
	if body == "" || strings.Contains(body, "<html") {
		t.Errorf("overview 应为纯 JSON 响应，得到 %s", body)
	}
}

// ---- 未匹配路由/方法：JSON 404 ----

func TestAPIUnknownRouteJSON404(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	cases := []struct {
		name   string
		method string
		path   string
		auth   bool
	}{
		{"API 根路径", http.MethodGet, "/api/v1", true},
		{"未匹配路径", http.MethodGet, "/api/v1/nope", true},
		{"已存在路径的方法不匹配", http.MethodPost, "/api/v1/session", true},
		{"未认证的未匹配路径", http.MethodGet, "/api/v1/nope", false},
	}
	for _, tc := range cases {
		client := j
		if !tc.auth {
			client = newJar(t)
		}
		resp := e.do(client, tc.method, tc.path, "", "")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s：应返回 404，得到 %d", tc.name, resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), apiCodeNotFound)
	}
}

// ---- apperr → API 状态与错误码映射 ----

func TestAPIAppErrStatusMapping(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"查无此行", store.ErrNotFound, http.StatusNotFound, apiCodeNotFound},
		{"包装后的查无此行", fmt.Errorf("读取失败: %w", store.ErrNotFound), http.StatusNotFound, apiCodeNotFound},
		{"存储约束", apperr.New(apperr.CodeStoreConstraint, "测试"), http.StatusConflict, string(apperr.CodeStoreConstraint)},
		{"存储不可用", apperr.Wrap(apperr.CodeStoreUnavailable, errors.New("db down")), http.StatusServiceUnavailable, string(apperr.CodeStoreUnavailable)},
		{"未分类错误", errors.New("boom"), http.StatusInternalServerError, string(apperr.CodeInternal)},
		{"邀请链接无效返回 400", apperr.New(apperr.CodeInvalidURL, "测试"), http.StatusBadRequest, string(apperr.CodeInvalidURL)},
		{"频道邀请无效返回 400", apperr.New(apperr.CodeInvalidInviteURL, "测试"), http.StatusBadRequest, string(apperr.CodeInvalidInviteURL)},
		{"业务错误暂回落内部错误", apperr.New(apperr.CodeMediaUnsupported, "测试"), http.StatusInternalServerError, string(apperr.CodeInternal)},
	}
	for _, tc := range cases {
		status, code := apiAppErrStatus(tc.err)
		if status != tc.wantStatus || code != tc.wantCode {
			t.Errorf("%s：应为 (%d, %s)，得到 (%d, %s)",
				tc.name, tc.wantStatus, tc.wantCode, status, code)
		}
	}
}

// writeAPIAppErr 输出受控文案，不透出底层错误字符串。
func TestAPIAppErrResponseHidesCause(t *testing.T) {
	e := newTestEnv(t, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	e.srv.writeAPIAppErr(rec, req, "test.op", errors.New("敏感底层细节 /secret/db boom"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("未分类错误应映射 500，得到 %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "/secret/db") || strings.Contains(body, "boom") {
		t.Errorf("错误响应不得透出底层错误：%s", body)
	}
	requireJSONError(t, rec.Code, rec.Header().Get("Content-Type"), body, string(apperr.CodeInternal))
}

// 业务存储类错误码的 message 必须沿用 apperr 用户文案（单一来源），
// 而不是回落内部错误的通用提示；状态码与文案语义一致。
func TestAPIAppErrMessageUsesApperrText(t *testing.T) {
	e := newTestEnv(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   apperr.Code
	}{
		{"存储不可用", apperr.Wrap(apperr.CodeStoreUnavailable, errors.New("db down")), http.StatusServiceUnavailable, apperr.CodeStoreUnavailable},
		{"存储约束", apperr.New(apperr.CodeStoreConstraint, "测试"), http.StatusConflict, apperr.CodeStoreConstraint},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		e.srv.writeAPIAppErr(rec, req, "test.op", tc.err)
		if rec.Code != tc.wantStatus {
			t.Errorf("%s：状态应为 %d，得到 %d", tc.name, tc.wantStatus, rec.Code)
		}
		var env apiErrorEnvelope
		decodeAPIJSON(t, rec.Body.String(), &env)
		if env.Error.Code != string(tc.wantCode) {
			t.Errorf("%s：错误码应为 %s，得到 %q", tc.name, tc.wantCode, env.Error.Code)
		}
		if want := apperr.UserText(tc.wantCode); env.Error.Message != want {
			t.Errorf("%s：message 应沿用 apperr 文案 %q，得到 %q", tc.name, want, env.Error.Message)
		}
	}
}
