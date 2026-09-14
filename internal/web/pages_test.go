package web

// SPA-only 管理端路由测试：
//   - 旧 SSR 页面入口和认证 POST 表单路由均不可用；
//   - CSV 与二维码端点仍由会话鉴权保护。
//   - 业务写路径统一经 /api/v1（api_write_test.go / api_privileged_test.go 覆盖）。

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/notify"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// seedUser 建一个用户并返回；失败中止测试。
func seedUser(t *testing.T, e *testEnv, id int64, status string) store.User {
	t.Helper()
	u, err := e.st.CreateUser(context.Background(), store.User{
		ID: id, Status: status, Username: "u" + strconv.FormatInt(id, 10),
	})
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	return u
}

// seedRequest 建一条请求记录；失败中止测试。
func seedRequest(t *testing.T, e *testEnv, userID int64, channel string, msgID int, status string) store.Request {
	t.Helper()
	r, err := e.st.CreateRequest(context.Background(), store.Request{
		UserID: userID, SourceKind: store.SourcePublic, ChannelKey: channel,
		MessageID: msgID, Status: status,
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	if status == store.RequestSucceeded || status == store.RequestFailed {
		if err := e.st.FinishRequest(context.Background(), r.ID, store.RequestResult{
			Status: status, ErrorCode: "MEDIA_DOWNLOAD_FAILED", MediaType: "photo",
			FileSize: 12345, FileName: "cat.jpg",
		}); err != nil {
			t.Fatalf("落库终态失败: %v", err)
		}
		r.Status = status
	}
	return r
}

// form 为变更路由构造带 CSRF 的表单体（已删除表单路由的 404/405 用例使用）。
func form(sessCSRF string, extra url.Values) string {
	v := url.Values{"csrf_token": {sessCSRF}}
	for k, vs := range extra {
		for _, val := range vs {
			v.Add(k, val)
		}
	}
	return v.Encode()
}

// ---- 已删除旧页面入口 ----

func TestDeletedLegacyPageRoutesUnavailable(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	for _, path := range []string{
		"/", "/login", "/applications", "/users", "/users/42",
		"/requests", "/requests/7", "/channels", "/channels/alpha",
		"/events", "/settings", "/settings/oauth", "/audit", "/backup",
		"/mtproto/login", "/mtproto/status",
	} {
		resp := e.do(j, "GET", path, "", "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s 已删除旧入口，应 404，得到 %d", path, resp.StatusCode)
		}
		if location := resp.Header.Get("Location"); location != "" {
			t.Errorf("GET %s 不应保留重定向，得到 Location=%q", path, location)
		}
		bodyOf(t, resp)
	}
}

// 已删除全部认证 POST 表单路由：即使携带有效会话与 CSRF 也不可用。
// 旧页面入口也不再注册，因此所有旧页面/表单路径均返回 404。
func TestDeletedSSRFormRoutesUnavailable(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)

	for _, p := range []string{
		"/users", "/settings", "/settings/oauth", "/logout", "/restart",
		"/users/1/limits", "/users/1/enable", "/users/1/set-owner",
		"/applications/1/approve", "/requests/1/retry", "/events/1/resolve",
		"/backup/export", "/backup/import/confirm", "/mtproto/relogin",
	} {
		resp := e.do(j, "POST", p, "application/x-www-form-urlencoded", form(csrf, nil))
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST %s 已删除，应 404，得到 %d", p, resp.StatusCode)
		}
		bodyOf(t, resp)
	}
}

// CSV 与二维码端点仍由会话鉴权保护：未认证 302 到 SPA 登录壳（当前为
// /admin/login，旧 SSR /login 已删除）。
func TestProtectedRoutesRequireAuth(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	for _, p := range []string{
		"/users/export.csv", "/requests/export.csv", "/channels/export.csv",
		"/mtproto/qr.png",
	} {
		resp := e.do(j, "GET", p, "", "")
		if resp.StatusCode != http.StatusFound {
			t.Errorf("GET %s 未认证应 302，得到 %d", p, resp.StatusCode)
		}
		if loc := resp.Header.Get("Location"); loc != "/admin/login" {
			t.Errorf("GET %s 未认证应重定向到 /admin/login，得到 %q", p, loc)
		}
		bodyOf(t, resp)
	}
}

// 装配事件中心时 resolve 经 Hub 执行，审计语义一致
// （写路径统一走 /api/v1）。
func TestEventResolveViaHub(t *testing.T) {
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		h, err := notify.New(notify.Options{Store: opt.Store, Log: testLogger()})
		if err != nil {
			t.Fatalf("构造事件中心失败: %v", err)
		}
		opt.Hub = h
	})
	j := e.login(t)
	ctx := context.Background()
	if err := e.st.UpsertEvent(ctx, store.Event{Key: "k", Severity: "warn", Message: "x"}); err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}
	csrf := apiCSRFToken(t, e, j)

	events, _ := e.st.ListEvents(ctx)
	path := "/api/v1/events/" + strconv.FormatInt(events[0].ID, 10) + "/resolve"
	resp := e.apiPost(j, path, csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("经 Hub 解决事件应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}

	ev, err := e.st.GetEvent(ctx, "k")
	if err != nil || ev.Status != store.EventResolved {
		t.Fatalf("事件应已解决，得到 %+v err=%v", ev, err)
	}
	if !e.containsAction("event.resolve") {
		t.Error("经 Hub 解决事件应写审计")
	}
}

// ---- 时间范围解析 ----

func TestParseTimeRangeUntilExclusive(t *testing.T) {
	loc := time.UTC
	r, _ := http.NewRequest(http.MethodGet, "/x?since=2026-08-01&until=2026-08-02", nil)
	tr, err := parseTimeRange(r, loc)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	wantSince := time.Date(2026, 8, 1, 0, 0, 0, 0, loc).UnixMilli()
	wantUntil := time.Date(2026, 8, 3, 0, 0, 0, 0, loc).UnixMilli()
	if tr.Since != wantSince || tr.Until != wantUntil {
		t.Errorf("范围不符：since=%d until=%d（want %d/%d）", tr.Since, tr.Until, wantSince, wantUntil)
	}
}

func TestParseTimeRangeRejectsAdjacentReverseRange(t *testing.T) {
	r, _ := http.NewRequest(http.MethodGet, "/x?since=2026-08-03&until=2026-08-02", nil)
	if _, err := parseTimeRange(r, time.UTC); err == nil {
		t.Fatal("开始日期晚于结束日期时应拒绝")
	}
}

func TestParseTimeRangeUsesCalendarDayAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	r, _ := http.NewRequest(http.MethodGet, "/x?until=2026-03-08", nil)
	tr, err := parseTimeRange(r, loc)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := time.Date(2026, 3, 9, 0, 0, 0, 0, loc).UnixMilli()
	if tr.Until != want {
		t.Fatalf("DST 日期结束边界应为当地次日零点，得到 %d want %d", tr.Until, want)
	}
}

// ---- 并发访问（配合 -race 验证共享状态无竞态）----

func TestConcurrentPageAccess(t *testing.T) {
	e := newTestEnv(t, nil)
	seedUser(t, e, 1, store.UserEnabled)
	seedRequest(t, e, 1, "example_channel", 1, store.RequestSucceeded)

	// CSV 端点并发 200，验证保留功能端点的共享服务访问安全。
	var wg sync.WaitGroup
	for _, p := range []string{"/users/export.csv", "/requests/export.csv", "/channels/export.csv"} {
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func(path string) {
				defer wg.Done()
				j := e.login(t)
				resp := e.do(j, "GET", path, "", "")
				if resp.StatusCode != http.StatusOK {
					t.Errorf("并发访问 %s 应 200，得到 %d", path, resp.StatusCode)
				}
				bodyOf(t, resp)
			}(p)
		}
	}
	wg.Wait()
}
