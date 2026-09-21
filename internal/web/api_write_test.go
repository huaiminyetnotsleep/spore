package web

// 管理操作写 API 契约测试：
// 每个写端点覆盖未认证 401 JSON、CSRF 缺失/错误 403 JSON、非法 body 400、
// 成功路径（审计动作断言）与业务拒绝路径（错误码透出）；
// SSR 写路由零回归由 pages_test.go / task08_test.go 既有断言保障。

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// apiCSRFToken 经 /api/v1/session 取会话级 CSRF token（写请求头用）。
func apiCSRFToken(t *testing.T, e *testEnv, j *jar) string {
	t.Helper()
	resp := e.do(j, http.MethodGet, "/api/v1/session", "", "")
	var view apiSessionView
	decodeAPIJSON(t, bodyOf(t, resp), &view)
	if view.CSRFToken == "" {
		t.Fatal("bootstrap 未交付会话级 CSRF token")
	}
	return view.CSRFToken
}

// apiPost 以 JSON + X-CSRF-Token 发送 API 写请求（不跟随重定向）；
// contentType 为空串时默认 application/json。
func (e *testEnv) apiPost(j *jar, path, csrf, body string) *http.Response {
	return e.apiPostCT(j, path, csrf, "application/json", body)
}

// apiPostCT 是 apiPost 的完整形态，可显式指定 Content-Type。
func (e *testEnv) apiPostCT(j *jar, path, csrf, contentType, body string) *http.Response {
	e.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+path, rdr)
	if err != nil {
		e.t.Fatalf("构造 API 写请求失败: %v", err)
	}
	req.Header.Set("Content-Type", contentType)
	if csrf != "" {
		req.Header.Set(apiCSRFHeader, csrf)
	}
	if h := j.header(); h != "" {
		req.Header.Set("Cookie", h)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatalf("API POST %s 失败: %v", path, err)
	}
	j.store(resp)
	return resp
}

// requireAPIStatus 断言响应状态码并读取关闭响应体。
func requireAPIStatus(t *testing.T, resp *http.Response, path string, want int) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("POST %s 应返回 %d，得到 %d（body=%s）", path, want, resp.StatusCode, bodyOf(t, resp))
	}
	bodyOf(t, resp)
}

// ---- 未认证与 CSRF：JSON 401 / 403，绝不返回 HTML ----

func TestAPIWriteEndpointsUnauthenticated(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)
	for _, path := range []string{
		"/api/v1/applications/1/approve", "/api/v1/users",
		"/api/v1/users/1/enable", "/api/v1/users/1/limits",
		"/api/v1/users/1/reset-quota", "/api/v1/users/1/set-owner",
		"/api/v1/users/1/cloud-download", "/api/v1/users/1/auto-pin",
		"/api/v1/users/1/refresh-profile", "/api/v1/requests/1/retry",
		"/api/v1/requests/1/delete", "/api/v1/requests/delete",
		"/api/v1/channels/example/delete", "/api/v1/audit/delete", "/api/v1/audit/clear",
		"/api/v1/events/1/resolve", "/api/v1/settings", "/api/v1/session/logout",
	} {
		resp := e.do(j, http.MethodPost, path, "application/json", "{}")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("未认证 POST %s 应返回 401，得到 %d", path, resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), apiCodeUnauthorized)
	}
}

func TestAPIWriteEndpointsRequireCSRF(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	for _, path := range []string{
		"/api/v1/applications/1/approve", "/api/v1/users/1/disable",
		"/api/v1/users/1/refresh-profile",
		"/api/v1/requests/1/retry", "/api/v1/requests/1/delete", "/api/v1/requests/delete",
		"/api/v1/channels/example/delete", "/api/v1/audit/delete", "/api/v1/audit/clear",
		"/api/v1/events/1/resolve", "/api/v1/settings", "/api/v1/session/logout",
	} {
		for name, csrf := range map[string]string{"缺失": "", "错误": "wrong-token"} {
			resp := e.apiPost(j, path, csrf, "{}")
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s CSRF %s 应 403，得到 %d", http.MethodPost, path, name, resp.StatusCode)
			}
			requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
				bodyOf(t, resp), apiCodeCSRFFailed)
		}
	}
}

// 写端点方法不匹配回退 405 JSON（Allow: POST）。
func TestAPIWriteMethodNotAllowedJSON405(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	for _, path := range []string{
		"/api/v1/applications/1/approve", "/api/v1/users/1/enable",
		"/api/v1/requests/1/retry", "/api/v1/requests/1/delete", "/api/v1/requests/delete",
		"/api/v1/channels/example/delete", "/api/v1/audit/delete", "/api/v1/audit/clear", "/api/v1/events/1/resolve",
		"/api/v1/session/logout",
	} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s 应返回 405，得到 %d", path, resp.StatusCode)
		}
		if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
			t.Errorf("GET %s 应带 Allow: POST，得到 %q", path, allow)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), apiCodeMethodNotAllowed)
	}
}

// 非法 JSON / 非 JSON Content-Type / 多余内容 → 400 BAD_REQUEST。
func TestAPIWriteMalformedBody(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	resp := e.apiPostCT(j, "/api/v1/users", csrf, "application/x-www-form-urlencoded", "user_id=1")
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeBadRequest)

	resp = e.apiPost(j, "/api/v1/users", csrf, "{not-json")
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeBadRequest)

	resp = e.apiPost(j, "/api/v1/users", csrf, `{"user_id":5} trailing`)
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeBadRequest)
}

// ---- 申请审批 ----

func TestAPIApplicationsListAndReview(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	seedUser(t, e, 42, store.UserPending)
	seedUser(t, e, 7, store.UserPending)

	var list struct {
		Items []apiApplicationRow `json:"items"`
	}
	getAPIJSON(t, e, j, "/api/v1/applications", &list)
	if len(list.Items) != 2 || list.Items[0].ID != 7 || list.Items[1].ID != 42 {
		t.Fatalf("申请列表应按 ID 升序含 2 条: %+v", list.Items)
	}

	// 批准：状态生效 + 审计；通知通道未注入 → notified=false（不误报已通知）
	resp := e.apiPost(j, "/api/v1/applications/42/approve", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批准应 200，得到 %d", resp.StatusCode)
	}
	var out struct {
		OK       bool   `json:"ok"`
		Notified bool   `json:"notified"`
		Status   string `json:"status"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || out.Notified || out.Status != store.UserEnabled {
		t.Errorf("批准结果不对: %+v", out)
	}
	u, _ := e.st.GetUser(context.Background(), 42)
	if u.Status != store.UserEnabled {
		t.Fatalf("批准后应 enabled，得到 %s", u.Status)
	}
	if !e.containsAction("user.approve") {
		t.Error("批准应写审计")
	}

	// 拒绝：disabled + 审计
	resp = e.apiPost(j, "/api/v1/applications/7/reject", csrf, "")
	requireAPIStatus(t, resp, "reject", http.StatusOK)
	u, _ = e.st.GetUser(context.Background(), 7)
	if u.Status != store.UserDisabled {
		t.Fatalf("拒绝后应 disabled，得到 %s", u.Status)
	}
	if !e.containsAction("user.reject") {
		t.Error("拒绝应写审计")
	}

	// 不存在 → 404 JSON
	resp = e.apiPost(j, "/api/v1/applications/999/approve", csrf, "")
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeNotFound)
}

// ---- 用户管理 ----

func TestAPIUserAddAndUpdate(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	// 成功添加：默认启用 + 审计
	resp := e.apiPost(j, "/api/v1/users", csrf, `{"user_id":88,"note":" 手工添加 "}`)
	requireAPIStatus(t, resp, "add", http.StatusOK)
	u, err := e.st.GetUser(context.Background(), 88)
	if err != nil || u.Status != store.UserEnabled || u.Note != "手工添加" {
		t.Fatalf("添加结果不符：%+v err=%v", u, err)
	}
	if !e.containsAction("user.add") {
		t.Error("添加用户应写审计")
	}

	// 重复添加 → 409 STORE_CONSTRAINT（受控提示，不误报成功）
	resp = e.apiPost(j, "/api/v1/users", csrf, `{"user_id":88}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("重复添加应 409，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), string(apperr.CodeStoreConstraint))

	// 非法 ID / 缺失字段 → 400
	for _, body := range []string{`{"user_id":0}`, `{"user_id":-5}`, `{}`} {
		resp = e.apiPost(j, "/api/v1/users", csrf, body)
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeBadRequest)
	}

	// 状态变更：禁用 → 归档 → 恢复，审计一致
	postAction := func(action, body string) *http.Response {
		return e.apiPost(j, "/api/v1/users/88/"+action, csrf, body)
	}
	requireAPIStatus(t, postAction("disable", ""), "disable", http.StatusOK)
	requireAPIStatus(t, postAction("archive", ""), "archive", http.StatusOK)
	u, _ = e.st.GetUser(context.Background(), 88)
	if u.Status != store.UserArchived || u.ArchivedAt == 0 {
		t.Fatalf("归档后状态与时间不符：%+v", u)
	}
	requireAPIStatus(t, postAction("restore", ""), "restore", http.StatusOK)
	u, _ = e.st.GetUser(context.Background(), 88)
	if u.Status != store.UserEnabled {
		t.Fatalf("恢复后应 enabled，得到 %s", u.Status)
	}
	if !e.containsAction("user.set_status") {
		t.Error("状态变更应写审计")
	}

	// owner 不允许停用：409 STORE_CONSTRAINT + SSR 同源文案
	requireAPIStatus(t, postAction("set-owner", `{"owner":true}`), "set-owner", http.StatusOK)
	u, _ = e.st.GetUser(context.Background(), 88)
	if !u.IsOwner {
		t.Fatal("应已设为 owner")
	}
	if !e.containsAction("user.set_owner") {
		t.Error("设置 owner 应写审计")
	}
	resp = postAction("disable", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("停用 owner 应 409，得到 %d", resp.StatusCode)
	}
	var env apiErrorEnvelope
	decodeAPIJSON(t, bodyOf(t, resp), &env)
	if env.Error.Code != string(apperr.CodeStoreConstraint) ||
		env.Error.Message != userOpText(apperr.New(apperr.CodeStoreConstraint, "")) {
		t.Errorf("停用 owner 错误响应不对: %+v", env.Error)
	}
	u, _ = e.st.GetUser(context.Background(), 88)
	if u.Status != store.UserEnabled {
		t.Fatal("owner 不应被停用")
	}

	// set-owner 缺少布尔字段 → 400
	resp = postAction("set-owner", `{}`)
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeBadRequest)

	// 限额：合法值生效 + 审计；非法值 400；缺省字段保持原值
	limitsBody := `{"submit_interval_sec":30,"daily_limit":200,"concurrent_limit":5}`
	requireAPIStatus(t, postAction("limits", limitsBody), "limits", http.StatusOK)
	u, _ = e.st.GetUser(context.Background(), 88)
	if u.SubmitIntervalSec != 30 || u.DailyLimit != 200 || u.ConcurrentLimit != 5 {
		t.Fatalf("限额未生效：%+v", u)
	}
	if !e.containsAction("user.set_limits") {
		t.Error("调整限额应写审计")
	}
	for _, body := range []string{
		`{"daily_limit":-3}`, `{"daily_limit":100001}`,
		`{"submit_interval_sec":86401}`, `{"concurrent_limit":101}`,
	} {
		resp = postAction("limits", body)
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), apiCodeBadRequest)
	}
	requireAPIStatus(t, postAction("limits", `{"daily_limit":300}`), "limits", http.StatusOK)
	u, _ = e.st.GetUser(context.Background(), 88)
	if u.SubmitIntervalSec != 30 || u.ConcurrentLimit != 5 {
		t.Fatalf("缺省限额字段应保持原值：%+v", u)
	}

	// 重置当日用量：生效 + 审计
	day := e.srv.now().In(e.srv.tz(context.Background())).Format(dayInputFormat)
	if err := e.st.IncrementUsage(context.Background(), 88, day, 5); err != nil {
		t.Fatalf("累加用量失败: %v", err)
	}
	requireAPIStatus(t, postAction("reset-quota", ""), "reset-quota", http.StatusOK)
	usage, _ := e.st.GetUsage(context.Background(), 88, day)
	if usage.Used != 0 {
		t.Fatalf("重置后应为 0，得到 %d", usage.Used)
	}
	if !e.containsAction("user.reset_usage") {
		t.Error("重置用量应写审计")
	}

	// 不存在用户 → 404
	resp = e.apiPost(j, "/api/v1/users/999/enable", csrf, "")
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeNotFound)
}

// 资料刷新：Profile 未接入 → 503 不可用；成功覆盖资料；查询失败保留旧资料。
func TestAPIUserRefreshProfile(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	seedUser(t, e, 9, store.UserEnabled)

	// 未注入 Profile：SSR 同款"不可用"语义
	resp := e.apiPost(j, "/api/v1/users/9/refresh-profile", csrf, "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未接入 Profile 应 503，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), "TELEGRAM_UNAVAILABLE")

	lookup := &fakeProfileLookup{profile: store.UserProfile{ID: 9, Username: "new_name", DisplayName: "New Name"}}
	e2 := newTestEnvOpts(t, func(_ *config.Config, opt *Options) { opt.Profile = lookup })
	j2 := e2.login(t)
	csrf2 := apiCSRFToken(t, e2, j2)
	seedUser(t, e2, 9, store.UserEnabled)
	if err := e2.st.UpdateUserProfile(context.Background(), 9, "old_name", "Old Name"); err != nil {
		t.Fatal(err)
	}

	resp = e2.apiPost(j2, "/api/v1/users/9/refresh-profile", csrf2, "")
	requireAPIStatus(t, resp, "refresh-profile", http.StatusOK)
	u, _ := e2.st.GetUser(context.Background(), 9)
	if u.Username != "new_name" || u.DisplayName != "New Name" {
		t.Fatalf("成功刷新应覆盖资料: %+v", u)
	}
	if !e2.containsAction("user.profile_refresh") {
		t.Error("成功刷新应写审计")
	}

	lookup.err = context.DeadlineExceeded
	resp = e2.apiPost(j2, "/api/v1/users/9/refresh-profile", csrf2, "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("查询失败应 503，得到 %d", resp.StatusCode)
	}
	u, _ = e2.st.GetUser(context.Background(), 9)
	if u.Username != "new_name" {
		t.Fatalf("查询失败不应覆盖旧资料: %+v", u)
	}
	if !e2.containsAction("user.profile_refresh_failed") {
		t.Error("刷新失败应写脱敏审计")
	}
}

// Access 未注入时，资料刷新与事件解决均不得执行变更；已认证但 CSRF
// 无效时仍由 API 公共中间件返回受控 403。
func TestAPIUserRefreshProfileAndEventResolveRequireAccess(t *testing.T) {
	t.Run("refresh profile", func(t *testing.T) {
		lookup := &fakeProfileLookup{profile: store.UserProfile{ID: 9, Username: "new_name", DisplayName: "New Name"}}
		e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) { opt.Profile = lookup })
		seedUser(t, e, 9, store.UserEnabled)
		if err := e.st.UpdateUserProfile(context.Background(), 9, "old_name", "Old Name"); err != nil {
			t.Fatal(err)
		}
		j := e.login(t)
		csrf := apiCSRFToken(t, e, j)
		path := "/api/v1/users/9/refresh-profile"

		resp := e.apiPost(j, path, "wrong-token", "")
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeCSRFFailed)

		e.srv.access = nil
		resp = e.apiPost(j, path, csrf, "")
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("Access 未注入时资料刷新应 503，得到 %d", resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeUnavailable)
		u, err := e.st.GetUser(context.Background(), 9)
		if err != nil {
			t.Fatal(err)
		}
		if u.Username != "old_name" || u.DisplayName != "Old Name" {
			t.Fatalf("Access 未注入时不得刷新资料: %+v", u)
		}
	})

	t.Run("resolve event", func(t *testing.T) {
		e := newTestEnv(t, nil)
		ctx := context.Background()
		if err := e.st.UpsertEvent(ctx, store.Event{Key: "mtproto.session_invalid", Severity: "error", Message: "会话失效"}); err != nil {
			t.Fatal(err)
		}
		events, err := e.st.ListEvents(ctx)
		if err != nil || len(events) != 1 {
			t.Fatalf("读取测试事件失败: %v, events=%+v", err, events)
		}
		path := "/api/v1/events/" + strconv.FormatInt(events[0].ID, 10) + "/resolve"
		j := e.login(t)
		csrf := apiCSRFToken(t, e, j)

		resp := e.apiPost(j, path, "wrong-token", "")
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeCSRFFailed)

		e.srv.access = nil
		resp = e.apiPost(j, path, csrf, "")
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("Access 未注入时解决事件应 503，得到 %d", resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeUnavailable)
		got, err := e.st.GetEvent(ctx, "mtproto.session_invalid")
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.EventOpen {
			t.Fatalf("Access 未注入时不得解决事件: %+v", got)
		}
	})
}

// ---- 请求受控重试 ----

func TestAPIRequestRetry(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	seedUser(t, e, 1, store.UserEnabled)
	req := seedRequest(t, e, 1, "example_channel", 10, store.RequestFailed)
	path := "/api/v1/requests/" + strconv.FormatInt(req.ID, 10) + "/retry"

	resp := e.apiPost(j, path, csrf, "")
	requireAPIStatus(t, resp, "retry", http.StatusOK)
	got, _ := e.st.GetRequest(context.Background(), req.ID)
	if got.Status != store.RequestQueued || got.Attempt != 2 {
		t.Fatalf("重试后应 queued 且 attempt=2：%+v", got)
	}
	if !e.containsAction("request.retry") {
		t.Error("重试应写审计")
	}

	// 非 failed 状态 → 409 STORE_CONSTRAINT（与 SSR 同源文案）
	resp = e.apiPost(j, path, csrf, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("非 failed 重试应 409，得到 %d", resp.StatusCode)
	}
	var env apiErrorEnvelope
	decodeAPIJSON(t, bodyOf(t, resp), &env)
	if env.Error.Code != string(apperr.CodeStoreConstraint) || env.Error.Message != retryErrText(apperr.New(apperr.CodeStoreConstraint, ""), syscfg.DefaultMaxRequestAttempts) {
		t.Errorf("非 failed 重试错误响应不对: %+v", env.Error)
	}

	// 不存在 → 404
	resp = e.apiPost(j, "/api/v1/requests/999/retry", csrf, "")
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeNotFound)
}

func TestAPIRequestRetryBusinessRejections(t *testing.T) {
	t.Run("所属用户未启用", func(t *testing.T) {
		e := newTestEnv(t, nil)
		j := e.login(t)
		csrf := apiCSRFToken(t, e, j)
		seedUser(t, e, 1, store.UserEnabled)
		req := seedRequest(t, e, 1, "alpha", 1, store.RequestFailed)
		if _, err := e.st.UpdateUserStatus(context.Background(), 1, store.UserDisabled); err != nil {
			t.Fatal(err)
		}
		resp := e.apiPost(j, "/api/v1/requests/"+strconv.FormatInt(req.ID, 10)+"/retry", csrf, "")
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("禁用用户重试应 409，得到 %d", resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), string(apperr.CodeUserDisabled))
	})

	t.Run("已达尝试上限", func(t *testing.T) {
		e := newTestEnv(t, nil)
		j := e.login(t)
		csrf := apiCSRFToken(t, e, j)
		seedUser(t, e, 1, store.UserEnabled)
		req := seedRequest(t, e, 1, "alpha", 1, store.RequestFailed)
		// 两次 RetryRequest 把 attempt 抬到上限 3，再落回 failed 终态
		for i := 0; i < 2; i++ {
			if err := e.st.RetryRequest(context.Background(), req.ID, 0); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.st.FinishRequest(context.Background(), req.ID, store.RequestResult{
			Status: store.RequestFailed, ErrorCode: "MEDIA_DOWNLOAD_FAILED",
		}); err != nil {
			t.Fatal(err)
		}
		resp := e.apiPost(j, "/api/v1/requests/"+strconv.FormatInt(req.ID, 10)+"/retry", csrf, "")
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("超上限重试应 409，得到 %d", resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), string(apperr.CodeRetryExhausted))
	})

	t.Run("队列已满", func(t *testing.T) {
		var q *queue.Queue
		e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
			q = queue.New(1)
			opt.Queue = q
		})
		j := e.login(t)
		csrf := apiCSRFToken(t, e, j)
		seedUser(t, e, 1, store.UserEnabled)
		req := seedRequest(t, e, 1, "alpha", 1, store.RequestFailed)
		if err := q.Enqueue(queue.NewJob(1, 1, reqRef(req), 0, req.ID)); err != nil {
			t.Fatalf("占满队列失败: %v", err)
		}
		resp := e.apiPost(j, "/api/v1/requests/"+strconv.FormatInt(req.ID, 10)+"/retry", csrf, "")
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("队列满重试应 503，得到 %d", resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), string(apperr.CodeQueueFull))
	})
}

// reqRef 从请求行重建来源引用（入队占位任务用）。
func reqRef(r store.Request) tmeurl.SourceRef {
	ref, _ := access.RefFromRequest(r)
	return ref
}

// ---- 记录删除 ----

func TestAPIRequestDelete(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()
	seedUser(t, e, 1, store.UserEnabled)
	req := seedRequest(t, e, 1, "example_channel", 10, store.RequestFailed)
	path := "/api/v1/requests/" + strconv.FormatInt(req.ID, 10) + "/delete"

	resp := e.apiPost(j, path, csrf, "")
	requireAPIStatus(t, resp, "delete", http.StatusOK)
	if _, err := e.st.GetRequest(ctx, req.ID); err != store.ErrNotFound {
		t.Fatalf("删除后读取应 ErrNotFound，得到 %v", err)
	}
	if !e.containsAction("request.delete") {
		t.Error("删除请求应写审计")
	}

	// 重复删除（行已不存在）→ 404
	resp = e.apiPost(j, path, csrf, "")
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeNotFound)
}

func TestAPIRequestDeleteRejectsUnfinished(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	seedUser(t, e, 1, store.UserEnabled)
	for _, status := range []string{store.RequestQueued, store.RequestProcessing} {
		r := store.Request{UserID: 1, SourceKind: store.SourcePublic, ChannelKey: "alpha", MessageID: 1}
		created, err := e.st.CreateRequest(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		if status == store.RequestProcessing {
			if err := e.st.MarkRequestStarted(context.Background(), created.ID, 0); err != nil {
				t.Fatal(err)
			}
		}
		resp := e.apiPost(j, "/api/v1/requests/"+strconv.FormatInt(created.ID, 10)+"/delete", csrf, "")
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s 状态删除应 409，得到 %d", status, resp.StatusCode)
		}
		var env apiErrorEnvelope
		decodeAPIJSON(t, bodyOf(t, resp), &env)
		if env.Error.Code != string(apperr.CodeStoreConstraint) {
			t.Errorf("未终态删除错误码应为 STORE_CONSTRAINT，得到 %s", env.Error.Code)
		}
		if env.Error.Message != "该记录尚未结束（排队或处理中），请等待其完成后再删除。" {
			t.Errorf("未终态删除文案不对: %q", env.Error.Message)
		}
		if _, err := e.st.GetRequest(context.Background(), created.ID); err != nil {
			t.Fatalf("被拒绝的记录不应被删除: %v", err)
		}
	}
}

func TestAPIChannelDelete(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()
	seedUser(t, e, 1, store.UserEnabled)
	seedRequest(t, e, 1, "gone_channel", 1, store.RequestSucceeded)
	seedRequest(t, e, 1, "gone_channel", 2, store.RequestFailed)
	kept := seedRequest(t, e, 1, "kept_channel", 1, store.RequestSucceeded)
	path := "/api/v1/channels/gone_channel/delete"

	resp := e.apiPost(j, path, csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST %s 应返回 200，得到 %d（body=%s）", path, resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK      bool `json:"ok"`
		Deleted int  `json:"deleted"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || out.Deleted != 2 {
		t.Fatalf("频道删除应 ok=true deleted=2，得到 %+v", out)
	}
	// 频道聚合随记录消失，其他频道不受影响
	if stats, _ := e.st.ListChannelStats(ctx, store.StatsFilter{ChannelKey: "gone_channel"}); len(stats) != 0 {
		t.Fatalf("删除后频道聚合应为空，得到 %d 行", len(stats))
	}
	if _, err := e.st.GetRequest(ctx, kept.ID); err != nil {
		t.Fatalf("其他频道的记录不应被删除: %v", err)
	}
	if !e.containsAction("channel.delete") {
		t.Error("删除频道记录应写审计")
	}

	// 无记录频道 → 404
	resp = e.apiPost(j, path, csrf, "")
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeNotFound)
}

func TestAPIChannelDeleteRejectsUnfinished(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	seedUser(t, e, 1, store.UserEnabled)
	seedRequest(t, e, 1, "alpha", 1, store.RequestSucceeded)
	if _, err := e.st.CreateRequest(context.Background(), store.Request{
		UserID: 1, SourceKind: store.SourcePublic, ChannelKey: "alpha", MessageID: 2,
	}); err != nil {
		t.Fatal(err)
	}

	resp := e.apiPost(j, "/api/v1/channels/alpha/delete", csrf, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("有未完成请求的频道删除应 409，得到 %d", resp.StatusCode)
	}
	var env apiErrorEnvelope
	decodeAPIJSON(t, bodyOf(t, resp), &env)
	if env.Error.Code != string(apperr.CodeStoreConstraint) ||
		env.Error.Message != "该频道仍有排队或处理中的请求，请等待其完成后再删除。" {
		t.Errorf("频道未完成拒绝响应不对: %+v", env.Error)
	}
	if n, _ := e.st.CountRequests(context.Background(), store.RequestFilter{ChannelKey: "alpha"}); n != 2 {
		t.Fatalf("被拒绝的频道记录不应被删除，仍有 %d 行", n)
	}
}

func TestAPIRequestsDeleteMany(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	seedUser(t, e, 1, store.UserEnabled)
	terminal := seedRequest(t, e, 1, "alpha", 1, store.RequestSucceeded)
	active, err := e.st.CreateRequest(context.Background(), store.Request{
		UserID: 1, SourceKind: store.SourcePublic, ChannelKey: "alpha", MessageID: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp := e.apiPost(j, "/api/v1/requests/delete", csrf,
		fmt.Sprintf(`{"ids":[%d,%d,999999]}`, terminal.ID, active.ID))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批量删除应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK      bool              `json:"ok"`
		Results []apiDeleteResult `json:"results"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || len(out.Results) != 3 || out.Results[0].Result != "deleted" ||
		out.Results[1].Result != "conflict" || out.Results[2].Result != "not_found" {
		t.Fatalf("批量删除结果不符: %+v", out)
	}
	if _, err := e.st.GetRequest(context.Background(), active.ID); err != nil {
		t.Fatalf("活动请求不应被删除: %v", err)
	}

	resp = e.apiPost(j, "/api/v1/requests/delete", csrf, `{"ids":[]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空批量删除应 400，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

func TestAPIAuditDeleteAndClear(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()
	if err := e.st.AppendAudit(ctx, store.AuditEntry{Actor: "admin", Action: "test.one"}); err != nil {
		t.Fatal(err)
	}
	entries, err := e.st.ListAudit(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	id := entries[0].ID

	resp := e.apiPost(j, "/api/v1/audit/delete", csrf, `{"ids":[]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空审计批量删除应 400，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
	resp = e.apiPost(j, "/api/v1/audit/delete", csrf, `{"ids":[0]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法审计 ID 应 400，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	resp = e.apiPost(j, "/api/v1/audit/delete", csrf, fmt.Sprintf(`{"ids":[%d]}`, id))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("审计批量删除应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var deleted apiDeletedResult
	decodeAPIJSON(t, bodyOf(t, resp), &deleted)
	if !deleted.OK || deleted.Deleted != 1 || !e.containsAction("audit.delete") {
		t.Fatalf("审计批量删除响应或清理审计不符: %+v", deleted)
	}

	resp = e.apiPost(j, "/api/v1/audit/clear", csrf, `{"confirm":"wrong"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("错误确认值应 400，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	before, err := e.st.CountAudit(ctx)
	if err != nil || before == 0 {
		t.Fatalf("清除前应有审计: %d err=%v", before, err)
	}
	resp = e.apiPost(j, "/api/v1/audit/clear", csrf, `{"confirm":"clear_audit"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("清除审计应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	decodeAPIJSON(t, bodyOf(t, resp), &deleted)
	if deleted.Deleted != before {
		t.Fatalf("清除数应为 %d，得到 %d", before, deleted.Deleted)
	}
	remaining, err := e.st.ListAudit(ctx, 10, 0)
	if err != nil || len(remaining) != 1 || remaining[0].Action != "audit.clear" {
		t.Fatalf("清除后应只剩 audit.clear: %+v err=%v", remaining, err)
	}
}

// ---- 事件解决 ----

func TestAPIEventResolve(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()
	if err := e.st.UpsertEvent(ctx, store.Event{Key: "mtproto.session_invalid", Severity: "error", Message: "会话失效"}); err != nil {
		t.Fatal(err)
	}
	events, _ := e.st.ListEvents(ctx)
	path := "/api/v1/events/" + strconv.FormatInt(events[0].ID, 10) + "/resolve"

	resp := e.apiPost(j, path, csrf, "")
	requireAPIStatus(t, resp, "resolve", http.StatusOK)
	got, _ := e.st.GetEvent(ctx, "mtproto.session_invalid")
	if got.Status != store.EventResolved {
		t.Fatalf("事件应已解决，得到 %s", got.Status)
	}
	if !e.containsAction("event.resolve") {
		t.Error("解决事件应写审计")
	}

	resp = e.apiPost(j, "/api/v1/events/999/resolve", csrf, "")
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeNotFound)
}

// ---- 运营设置 ----

func TestAPISettingsReadAndSave(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	var before apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &before)
	if before.Timezone != "Asia/Shanghai" || before.MaxLinksPerMessage != config.DefaultMaxLinksPerMessage ||
		before.QueueCapacity != 64 || before.MaxFileSizeBytes == 0 {
		t.Fatalf("设置读取不对: %+v", before)
	}

	// 合法保存：时区 + 去重窗口 + 队列容量 + 媒体参数
	body := `{"timezone":"UTC","dedup_window_min":20,"max_links_per_message":12,"queue_capacity":128,` +
		`"max_file_size":"40","max_file_unit":"MB","stream_limit":"10","stream_limit_unit":"MB"}`
	resp := e.apiPost(j, "/api/v1/settings", csrf, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("设置保存应 200，得到 %d", resp.StatusCode)
	}
	var out struct {
		OK       bool            `json:"ok"`
		Settings apiSettingsView `json:"settings"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || out.Settings.Timezone != "UTC" || out.Settings.DedupWindowMin != 20 ||
		out.Settings.MaxLinksPerMessage != 12 || out.Settings.QueueCapacity != 128 ||
		out.Settings.MaxFileSizeBytes != 40<<20 {
		t.Errorf("保存结果摘要不对: %+v", out)
	}
	ctx := context.Background()
	if name := e.srv.access.TimezoneName(ctx); name != "UTC" {
		t.Errorf("时区应即时生效，得到 %s", name)
	}
	if m := e.srv.access.DedupWindowMinutes(ctx); m != 20 {
		t.Errorf("去重窗口应即时生效，得到 %d", m)
	}
	if got := LoadMaxLinksPerMessage(ctx, e.st, config.DefaultMaxLinksPerMessage); got != 12 {
		t.Errorf("单次最大链接数应即时生效，得到 %d", got)
	}
	if cap := LoadQueueCapacity(ctx, e.st); cap != 128 {
		t.Errorf("队列容量应已保存，得到 %d", cap)
	}
	if raw, ok, _ := e.st.GetSetting(ctx, settingKeyMaxFileSize); !ok || raw != "41943040" {
		t.Errorf("媒体上限应按字节保存: %q %v", raw, ok)
	}
	for _, action := range []string{"settings.timezone", "settings.dedup_window", "settings.max_links_per_message", "settings.queue_capacity", "settings.media"} {
		if !e.containsAction(action) {
			t.Errorf("设置变更应写审计 %s", action)
		}
	}

	// 参数拒绝：非法时区 / 去重窗口 / 队列容量 / 媒体规则，settings 不变
	//（大文件上限 2000MB 内不再要求本地 Bot API 服务器）
	for name, bad := range map[string]string{
		"非法时区":      `{"timezone":"Mars/Olympus"}`,
		"去重窗口越界":    `{"dedup_window_min":0}`,
		"单次链接数越界":   `{"max_links_per_message":51}`,
		"队列容量越界":    `{"queue_capacity":4097}`,
		"媒体只填一项":    `{"max_file_size":"40","max_file_unit":"MB"}`,
		"媒体单位非法":    `{"max_file_size":"40","max_file_unit":"TB","stream_limit":"10","stream_limit_unit":"MB"}`,
		"阈值大于上限":    `{"max_file_size":"10","max_file_unit":"MB","stream_limit":"20","stream_limit_unit":"MB"}`,
		"上限超2000MB": `{"max_file_size":"2048","max_file_unit":"MB","stream_limit":"20","stream_limit_unit":"MB"}`,
	} {
		resp = e.apiPost(j, "/api/v1/settings", csrf, bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s 应 400，得到 %d（body=%s）", name, resp.StatusCode, bodyOf(t, resp))
			continue
		}
		var env apiErrorEnvelope
		decodeAPIJSON(t, bodyOf(t, resp), &env)
		if env.Error.Code != apiCodeBadRequest || env.Error.Message == "" {
			t.Errorf("%s 错误响应不对: %+v", name, env.Error)
		}
	}
	if name := e.srv.access.TimezoneName(ctx); name != "UTC" {
		t.Errorf("被拒请求不应改时区，得到 %s", name)
	}
}

// LoadWorkerCount：DB 覆盖优先，键缺失/非法/越界回退 env 默认（重启生效语义）。
func TestLoadWorkerCount(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()

	if got := LoadWorkerCount(ctx, e.st, 3); got != 3 {
		t.Fatalf("键缺失应回退 env 默认，得到 %d", got)
	}
	for name, raw := range map[string]string{
		"非法 JSON": `not-a-number`,
		"低于下界":    `0`,
		"高于上界":    `17`,
	} {
		if err := e.st.SetSetting(ctx, settingKeyWorkerCount, raw); err != nil {
			t.Fatal(err)
		}
		if got := LoadWorkerCount(ctx, e.st, 3); got != 3 {
			t.Errorf("%s 应回退 env 默认，得到 %d", name, got)
		}
	}
	if err := e.st.SetSetting(ctx, settingKeyWorkerCount, `4`); err != nil {
		t.Fatal(err)
	}
	if got := LoadWorkerCount(ctx, e.st, 3); got != 4 {
		t.Errorf("合法覆盖应生效，得到 %d", got)
	}
}

// worker 数设置（测试环境进程值固定为 2）：保存写库+审计（重启生效），
// 越界 400，读取展示配置值/进程值/待重启状态。
func TestAPISettingsWorkerCount(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	var before apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &before)
	if before.WorkerCount != 2 || before.WorkerCountRuntime != 2 || !before.WorkerCountSame {
		t.Fatalf("无覆盖时 worker 数视图不对: %+v", before)
	}

	resp := e.apiPost(j, "/api/v1/settings", csrf, `{"worker_count":4}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("worker 数保存应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK       bool            `json:"ok"`
		Settings apiSettingsView `json:"settings"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if out.Settings.WorkerCount != 4 || out.Settings.WorkerCountRuntime != 2 || out.Settings.WorkerCountSame {
		t.Errorf("保存后视图应显示待重启差异: %+v", out.Settings)
	}
	ctx := context.Background()
	if got := LoadWorkerCount(ctx, e.st, 2); got != 4 {
		t.Errorf("worker 数应已保存，得到 %d", got)
	}
	if !e.containsAction("settings.worker_count") {
		t.Error("worker 数变更应写审计")
	}

	// 值未变不写审计
	beforeAudit := len(e.auditActions())
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"worker_count":4}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重复保存应 200，得到 %d", resp.StatusCode)
	}
	if after := len(e.auditActions()); after != beforeAudit {
		t.Errorf("值未变不应写审计：before=%d after=%d", beforeAudit, after)
	}

	// 越界拒绝
	for name, bad := range map[string]string{
		"低于下界": `{"worker_count":0}`,
		"高于上界": `{"worker_count":17}`,
	} {
		resp = e.apiPost(j, "/api/v1/settings", csrf, bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s 应 400，得到 %d（body=%s）", name, resp.StatusCode, bodyOf(t, resp))
			continue
		}
		var env apiErrorEnvelope
		decodeAPIJSON(t, bodyOf(t, resp), &env)
		if env.Error.Code != apiCodeBadRequest || env.Error.Message == "" {
			t.Errorf("%s 错误响应不对: %+v", name, env.Error)
		}
	}
	if got := LoadWorkerCount(ctx, e.st, 2); got != 4 {
		t.Errorf("被拒请求不应改 worker 数，得到 %d", got)
	}
}

// LoadMemoryBudget：DB 覆盖优先，nil Store/键缺失/非法/越界回退 env 默认。
func TestLoadMemoryBudget(t *testing.T) {
	e := newTestEnv(t, nil)
	ctx := context.Background()
	const envDefault = int64(512) << 20

	if got := LoadMemoryBudget(ctx, nil, envDefault); got != envDefault {
		t.Fatalf("nil Store 应回退 env 默认，得到 %d", got)
	}
	if got := LoadMemoryBudget(ctx, e.st, envDefault); got != envDefault {
		t.Fatalf("键缺失应回退 env 默认，得到 %d", got)
	}
	for name, raw := range map[string]string{
		"非法 JSON": `not-a-number`,
		"低于下界":    `33554432`,   // 32MB < 64MB
		"高于上界":    `9663676416`, // 9GB > 8GB
	} {
		if err := e.st.SetSetting(ctx, settingKeyMemoryBudget, raw); err != nil {
			t.Fatal(err)
		}
		if got := LoadMemoryBudget(ctx, e.st, envDefault); got != envDefault {
			t.Errorf("%s 应回退 env 默认，得到 %d", name, got)
		}
	}
	if err := e.st.SetSetting(ctx, settingKeyMemoryBudget, `268435456`); err != nil { // 256MB
		t.Fatal(err)
	}
	if got := LoadMemoryBudget(ctx, e.st, envDefault); got != 256<<20 {
		t.Errorf("合法覆盖应生效，得到 %d", got)
	}
}

// 内存预算设置（即时生效）：保存写库+审计，非法/越界 400，读取展示生效值。
func TestAPISettingsMemoryBudget(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.MemoryBudget = 512 << 20 })
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	var before apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &before)
	if before.MemoryBudgetBytes != 512<<20 {
		t.Fatalf("无覆盖时应显示 env 默认预算，得到 %d", before.MemoryBudgetBytes)
	}

	resp := e.apiPost(j, "/api/v1/settings", csrf, `{"memory_budget":"256","memory_budget_unit":"MB"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("内存预算保存应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK       bool            `json:"ok"`
		Settings apiSettingsView `json:"settings"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if out.Settings.MemoryBudgetBytes != 256<<20 {
		t.Errorf("保存后视图应显示新预算，得到 %d", out.Settings.MemoryBudgetBytes)
	}
	ctx := context.Background()
	if got := LoadMemoryBudget(ctx, e.st, 512<<20); got != 256<<20 {
		t.Errorf("内存预算应已保存，得到 %d", got)
	}
	if !e.containsAction("settings.memory_budget") {
		t.Error("内存预算变更应写审计")
	}

	// 值未变不写审计
	beforeAudit := len(e.auditActions())
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"memory_budget":"256","memory_budget_unit":"MB"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重复保存应 200，得到 %d", resp.StatusCode)
	}
	if after := len(e.auditActions()); after != beforeAudit {
		t.Errorf("值未变不应写审计：before=%d after=%d", beforeAudit, after)
	}

	// 参数拒绝：越界与非法单位，预算保持不变
	for name, bad := range map[string]string{
		"低于下界": `{"memory_budget":"32","memory_budget_unit":"MB"}`,
		"高于上界": `{"memory_budget":"9","memory_budget_unit":"GB"}`,
		"单位非法": `{"memory_budget":"256","memory_budget_unit":"TB"}`,
		"非数字":  `{"memory_budget":"abc","memory_budget_unit":"MB"}`,
	} {
		resp = e.apiPost(j, "/api/v1/settings", csrf, bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s 应 400，得到 %d（body=%s）", name, resp.StatusCode, bodyOf(t, resp))
			continue
		}
		var env apiErrorEnvelope
		decodeAPIJSON(t, bodyOf(t, resp), &env)
		if env.Error.Code != apiCodeBadRequest || env.Error.Message == "" {
			t.Errorf("%s 错误响应不对: %+v", name, env.Error)
		}
	}
	if got := LoadMemoryBudget(ctx, e.st, 512<<20); got != 256<<20 {
		t.Errorf("被拒请求不应改内存预算，得到 %d", got)
	}
}

// ---- 登出 ----

func TestAPISessionLogout(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	resp := e.apiPost(j, "/api/v1/session/logout", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登出应 200，得到 %d", resp.StatusCode)
	}
	var out apiWriteOK
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK {
		t.Error("登出应返回 ok=true")
	}

	// 会话已失效：API 返回 401，SPA 入口回到 302 → /login
	resp = e.do(j, http.MethodGet, "/api/v1/session", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("登出后 API 会话应 401，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
	resp = e.do(j, http.MethodGet, "/admin", "", "")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("登出后 SPA 入口应 302，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
	if !e.containsAction("auth.logout") {
		t.Error("API 登出应写审计")
	}
}

// 用户级频道绑定上限：随限额端点保存（0 = 跟随角色默认），详情回显 raw 值。
func TestAPIUserLimitsBindLimit(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	// 准备一个已启用用户
	if _, err := e.st.CreateUser(context.Background(), store.User{ID: 33, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	resp := e.apiPost(j, "/api/v1/users/33/limits", csrf, `{"bind_limit":4}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if u, err := e.st.GetUser(context.Background(), 33); err != nil || u.BindLimit != 4 {
		t.Fatalf("bind_limit 应落库为 4: %v %v", u, err)
	}

	// 0 = 恢复跟随角色默认
	resp = e.apiPost(j, "/api/v1/users/33/limits", csrf, `{"bind_limit":0}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("归零应 200，得到 %d", resp.StatusCode)
	}
	if u, _ := e.st.GetUser(context.Background(), 33); u.BindLimit != 0 || u.EffectiveBindLimit() != 1 {
		t.Fatalf("归零后应跟随默认 1: %+v", u)
	}

	// 越界拒绝
	resp = e.apiPost(j, "/api/v1/users/33/limits", csrf, `{"bind_limit":21}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("21 应 400，得到 %d", resp.StatusCode)
	}

	// 详情回显 raw 值，并同步给出生效值
	if err := e.st.UpdateUserBindLimit(context.Background(), 33, 2); err != nil {
		t.Fatalf("写入 bind_limit 失败: %v", err)
	}
	var detail apiUserDetail
	getAPIJSON(t, e, j, "/api/v1/users/33", &detail)
	if detail.BindLimit != 2 || detail.EffectiveBindLimit != 2 {
		t.Fatalf("详情应回显 bind_limit=2 且生效值=2，得到 %d/%d", detail.BindLimit, detail.EffectiveBindLimit)
	}

	// raw 0 时详情给出按角色解析的默认值（普通用户 1）
	if err := e.st.UpdateUserBindLimit(context.Background(), 33, 0); err != nil {
		t.Fatalf("写入 bind_limit 失败: %v", err)
	}
	getAPIJSON(t, e, j, "/api/v1/users/33", &detail)
	if detail.BindLimit != 0 || detail.EffectiveBindLimit != 1 {
		t.Fatalf("raw 0 时详情应给出生效值 1，得到 %d/%d", detail.BindLimit, detail.EffectiveBindLimit)
	}
}

// 云盘下载权限端点：三态设置矩阵、owner 显式拒绝、审计与详情回显。
func TestAPIUserSetCloudDownload(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	if _, err := e.st.CreateUser(context.Background(), store.User{ID: 44, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if _, err := e.st.CreateUser(context.Background(), store.User{ID: 45, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建 owner 失败: %v", err)
	}
	if err := e.st.SetOwner(context.Background(), 45, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}

	post := func(id int64, body string) *http.Response {
		return e.apiPost(j, fmt.Sprintf("/api/v1/users/%d/cloud-download", id), csrf, body)
	}

	// 默认态：普通用户拒绝、owner 允许（详情回显）
	var detail apiUserDetail
	getAPIJSON(t, e, j, "/api/v1/users/44", &detail)
	if detail.CloudDownload != store.CloudDownloadDefault || detail.EffectiveCloudDownload {
		t.Fatalf("普通用户默认应为 raw 0 / 生效拒绝: %d/%v", detail.CloudDownload, detail.EffectiveCloudDownload)
	}
	getAPIJSON(t, e, j, "/api/v1/users/45", &detail)
	if detail.CloudDownload != store.CloudDownloadDefault || !detail.EffectiveCloudDownload {
		t.Fatalf("owner 默认应为 raw 0 / 生效允许: %d/%v", detail.CloudDownload, detail.EffectiveCloudDownload)
	}

	// 显式允许普通用户 → 200 + 回显生效
	resp := post(44, `{"cloud_download":1}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("设置允许应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK                     bool `json:"ok"`
		CloudDownload          int  `json:"cloud_download"`
		EffectiveCloudDownload bool `json:"effective_cloud_download"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || out.CloudDownload != 1 || !out.EffectiveCloudDownload {
		t.Fatalf("响应应回显 raw 1 / 生效 true: %+v", out)
	}
	if u, _ := e.st.GetUser(context.Background(), 44); u.CloudDownload != store.CloudDownloadAllow {
		t.Fatalf("允许应落库: %+v", u)
	}

	// owner 显式拒绝 → 200 + 生效 false（owner 无豁免）
	resp = post(45, `{"cloud_download":2}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner 显式拒绝应 200，得到 %d", resp.StatusCode)
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if out.CloudDownload != 2 || out.EffectiveCloudDownload {
		t.Fatalf("owner 拒绝应回显生效 false: %+v", out)
	}

	// 审计与非法值/缺字段/不存在
	if !e.containsAction("user.set_cloud_download") {
		t.Error("设置云盘下载权限应写审计")
	}
	for _, body := range []string{`{"cloud_download":3}`, `{"cloud_download":-1}`, `{}`} {
		resp = post(44, body)
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), apiCodeBadRequest)
	}
	resp = post(404, `{"cloud_download":1}`)
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeNotFound)

	// 列表行同步带出新字段
	var list apiListEnvelope[apiUserRow]
	getAPIJSON(t, e, j, "/api/v1/users?q=44", &list)
	if len(list.Items) != 1 || list.Items[0].CloudDownload != 1 || !list.Items[0].EffectiveCloudDownload {
		t.Fatalf("列表应回显云盘下载字段: %+v", list.Items)
	}
}

// ---- 请求尝试计数重置 ----

func TestAPIRequestResetAttempts(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()
	seedUser(t, e, 1, store.UserEnabled)
	req := seedRequest(t, e, 1, "example_channel", 10, store.RequestFailed)
	// 两次 RetryRequest + 落失败终态，把 attempt 抬到 3
	for i := 0; i < 2; i++ {
		if err := e.st.RetryRequest(ctx, req.ID, 0); err != nil {
			t.Fatal(err)
		}
		if err := e.st.FinishRequest(ctx, req.ID, store.RequestResult{
			Status: store.RequestFailed, ErrorCode: "MEDIA_DOWNLOAD_FAILED"}); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/v1/requests/" + strconv.FormatInt(req.ID, 10) + "/reset_attempts"

	resp := e.apiPost(j, path, csrf, "")
	requireAPIStatus(t, resp, "reset_attempts", http.StatusOK)
	got, _ := e.st.GetRequest(ctx, req.ID)
	if got.Status != store.RequestFailed || got.Attempt != 1 || got.ErrorCode != "MEDIA_DOWNLOAD_FAILED" {
		t.Fatalf("重置后应保持 failed/attempt=1/错误码不动：%+v", got)
	}
	if !e.containsAction("request.reset_attempts") {
		t.Error("重置应写审计 request.reset_attempts")
	}

	// 非 failed 状态 → 409 STORE_CONSTRAINT（重置后的 failed/attempt=1 行
	// 幂等返回 200，另造 succeeded 行验证拒绝分支）
	succ := seedRequest(t, e, 1, "alpha", 2, store.RequestQueued)
	if err := e.st.FinishRequest(ctx, succ.ID, store.RequestResult{Status: store.RequestSucceeded}); err != nil {
		t.Fatal(err)
	}
	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/requests/"+strconv.FormatInt(succ.ID, 10)+"/reset_attempts", csrf, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("非 failed 重置应 409，得到 %d", resp.StatusCode)
	}
	var env apiErrorEnvelope
	decodeAPIJSON(t, bodyOf(t, resp), &env)
	if env.Error.Code != string(apperr.CodeStoreConstraint) || env.Error.Message != resetErrText(apperr.New(apperr.CodeStoreConstraint, "")) {
		t.Errorf("非 failed 重置错误响应不对: %+v", env.Error)
	}

	// 不存在 → 404
	resp = e.apiPost(j, "/api/v1/requests/999/reset_attempts", csrf, "")
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeNotFound)
}

// 配置动态生效：调低上限后，attempt 低于旧缺省值的 failed 行也按
// RETRY_EXHAUSTED 拒绝（409），拒绝文案携带当前配置值。
func TestAPIRequestRetryDynamicLimit(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	seedUser(t, e, 1, store.UserEnabled)
	req := seedRequest(t, e, 1, "alpha", 1, store.RequestFailed)
	if err := syscfg.SetMaxRequestAttempts(context.Background(), e.st, 1); err != nil {
		t.Fatal(err)
	}

	resp := e.apiPost(j, "/api/v1/requests/"+strconv.FormatInt(req.ID, 10)+"/retry", csrf, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("上限调到 1 后重试应 409，得到 %d", resp.StatusCode)
	}
	var env apiErrorEnvelope
	decodeAPIJSON(t, bodyOf(t, resp), &env)
	if env.Error.Code != string(apperr.CodeRetryExhausted) {
		t.Fatalf("应返回 RETRY_EXHAUSTED: %+v", env.Error)
	}
	if !strings.Contains(env.Error.Message, "1 次") {
		t.Errorf("拒绝文案应携带当前上限值: %q", env.Error.Message)
	}

	// 详情接口 attempt_max 下发当前配置值
	var detail apiRequestDetail
	getAPIJSON(t, e, j, "/api/v1/requests/"+strconv.FormatInt(req.ID, 10), &detail)
	if detail.AttemptMax != 1 {
		t.Errorf("详情 attempt_max 应为配置值 1，得到 %d", detail.AttemptMax)
	}
}
