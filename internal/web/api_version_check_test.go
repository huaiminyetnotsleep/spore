package web

// GET /api/v1/version/check 契约测试：当前版本与上游最新发布的三态比较
// （outdated / up_to_date / unknown）、上游查询失败与通道未注入的受控 503，
// 以及 semver 比较与 GitHub 查询器 TTL 缓存的单元行为。

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/config"
)

type fakeReleaseChecker struct {
	info  ReleaseInfo
	force []bool // 记录每次调用收到的 force 标记
}

func (f *fakeReleaseChecker) LatestRelease(_ context.Context, force bool) ReleaseInfo {
	f.force = append(f.force, force)
	return f.info
}

// 三态比较：注入构建期版本 + 假检查器，校验 status/latest_version/release_url。
func TestAPIVersionCheckStatuses(t *testing.T) {
	cases := []struct {
		name       string
		current    string
		latest     string
		wantStatus string
	}{
		{"落后于上游", "v1.0.0", "v1.2.0", "outdated"},
		{"已是最新", "v1.2.0", "v1.2.0", "up_to_date"},
		{"缺 v 前缀等价比较", "1.2.0", "v1.2.0", "up_to_date"},
		{"短段补零比较", "v1.2", "v1.2.0", "up_to_date"},
		{"dev 构建无法比较", "dev", "v1.2.0", "unknown"},
		{"旧格式版本无法比较", "0.2.0-web-pages", "v1.2.0", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeReleaseChecker{info: ReleaseInfo{
				Version:   tc.latest,
				URL:       "https://github.com/huaiminyetnotsleep/spore/releases/tag/" + tc.latest,
				CheckedAt: time.UnixMilli(1700000000000),
			}}
			e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
				opt.Version = tc.current
				opt.ReleaseCheck = fake
			})
			j := e.login(t)
			var view apiVersionCheckView
			body := getAPIJSON(t, e, j, "/api/v1/version/check", &view)
			if view.CurrentVersion != tc.current {
				t.Errorf("current_version 应为 %q，得到 %q", tc.current, view.CurrentVersion)
			}
			if view.Status != tc.wantStatus {
				t.Errorf("status 应为 %q，得到 %q", tc.wantStatus, view.Status)
			}
			if view.LatestVersion != tc.latest {
				t.Errorf("latest_version 应为 %q，得到 %q", tc.latest, view.LatestVersion)
			}
			if view.CheckedAt != 1700000000000 {
				t.Errorf("checked_at 应为 Unix 毫秒 1700000000000，得到 %d", view.CheckedAt)
			}
			if !strings.Contains(body, `"release_url":`) {
				t.Errorf("应下发 release_url：%s", body)
			}
			if got := bodyHeader(t, e, j); got != "no-store" {
				t.Errorf("检查更新应禁止缓存，得到 Cache-Control %q", got)
			}
		})
	}
}

// bodyHeader 单独发一次请求取响应头（复用会话）。
func bodyHeader(t *testing.T, e *testEnv, j *jar) string {
	t.Helper()
	resp := e.do(j, http.MethodGet, "/api/v1/version/check", "", "")
	defer resp.Body.Close()
	return resp.Header.Get("Cache-Control")
}

// force=1 透传给检查器跳过缓存（手动刷新语义）；缺省 false 走缓存。
func TestAPIVersionCheckForceParam(t *testing.T) {
	fake := &fakeReleaseChecker{info: ReleaseInfo{
		Version:   "v1.2.0",
		CheckedAt: time.UnixMilli(1700000000000),
	}}
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		opt.Version = "v1.0.0"
		opt.ReleaseCheck = fake
	})
	j := e.login(t)

	var view apiVersionCheckView
	getAPIJSON(t, e, j, "/api/v1/version/check", &view)
	getAPIJSON(t, e, j, "/api/v1/version/check?force=1", &view)

	if len(fake.force) != 2 || fake.force[0] || !fake.force[1] {
		t.Fatalf("force 标记透传不符: %v", fake.force)
	}
}

// 上游查询失败（Version 空串）→ 受控 503。
func TestAPIVersionCheckUpstreamFailure(t *testing.T) {
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		opt.Version = "v1.0.0"
		opt.ReleaseCheck = &fakeReleaseChecker{}
	})
	j := e.login(t)
	resp := e.do(j, http.MethodGet, "/api/v1/version/check", "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("上游失败应返回 503，得到 %d", resp.StatusCode)
	}
	if !strings.Contains(bodyOf(t, resp), apiCodeUnavailable) {
		t.Fatalf("503 应携带受控错误码 %s", apiCodeUnavailable)
	}
}

// 通道未注入 → 受控 503（缺省测试环境不注入 ReleaseCheck）。
func TestAPIVersionCheckNotWired(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	resp := e.do(j, http.MethodGet, "/api/v1/version/check", "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未注入应返回 503，得到 %d", resp.StatusCode)
	}
}

// compareSemver 的解析边界：非法输入一律 ok=false，不猜测结论。
func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v1.0.0", "v1.2.0", -1, true},
		{"v1.10.0", "v1.9.0", 1, true},
		{"v1.2.0", "v1.2", 0, true},
		{"v2.0.0", "v1.99.99", 1, true},
		{"dev", "v1.0.0", 0, false},
		{"v1.0.0", "dev", 0, false},
		{"v1.0.0-rc.1", "v1.0.0", 0, false},
		{"", "v1.0.0", 0, false},
		{"1.0", "1.0.0", 0, true},
	}
	for _, tc := range cases {
		got, ok := compareSemver(tc.a, tc.b)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("compareSemver(%q, %q) = (%d, %v)，期望 (%d, %v)", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
}

// GitHub 查询器：TTL 内复用缓存不打上游；force=true 跳过缓存直接查询并
// 回写缓存（TTL 自新查询时间起算）；过期后重新查询；失败不缓存。
func TestGitHubReleaseCheckerCache(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		if hits == 4 {
			// 第四轮模拟上游故障
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v1.2.0","html_url":"https://example.com/rel"}`))
	}))
	defer srv.Close()

	now := time.UnixMilli(0)
	current := now
	checker := newGitHubReleaseChecker(srv.URL, srv.Client(), time.Hour, func() time.Time { return current })

	// 首次查询打上游
	info := checker.LatestRelease(context.Background(), false)
	if info.Version != "v1.2.0" || info.URL != "https://example.com/rel" {
		t.Fatalf("首次查询结果不符: %+v", info)
	}
	// TTL 内命中缓存
	current = now.Add(30 * time.Minute)
	if info := checker.LatestRelease(context.Background(), false); info.Version != "v1.2.0" {
		t.Fatalf("缓存命中结果不符: %+v", info)
	}
	if hits != 1 {
		t.Fatalf("TTL 内不应再打上游，hits=%d", hits)
	}
	// force=true 跳过缓存：即便 TTL 内也直接查上游，成功后回写
	if info := checker.LatestRelease(context.Background(), true); info.Version != "v1.2.0" {
		t.Fatalf("强制刷新结果不符: %+v", info)
	}
	if hits != 2 {
		t.Fatalf("force 应直接打上游，hits=%d", hits)
	}
	// 缓存自 force 刷新时间起算
	current = now.Add(59 * time.Minute)
	if info := checker.LatestRelease(context.Background(), false); info.Version != "v1.2.0" {
		t.Fatalf("force 后 TTL 内应命中缓存: %+v", info)
	}
	if hits != 2 {
		t.Fatalf("force 刷新后的 TTL 内不应打上游，hits=%d", hits)
	}
	// 过期后重新查询
	current = now.Add(2 * time.Hour)
	if info := checker.LatestRelease(context.Background(), false); info.Version != "v1.2.0" {
		t.Fatalf("过期后查询结果不符: %+v", info)
	}
	if hits != 3 {
		t.Fatalf("过期后应重新查询，hits=%d", hits)
	}
	// 上游故障返回空 Version 且不缓存（下次调用重试上游）
	current = now.Add(3 * time.Hour)
	if info := checker.LatestRelease(context.Background(), false); info.Version != "" {
		t.Fatalf("上游故障应返回空 Version: %+v", info)
	}
	if hits != 4 {
		t.Fatalf("故障时应已打上游一次，hits=%d", hits)
	}
	// 故障未被缓存：下次调用重新打上游（此时上游已恢复）
	current = now.Add(4 * time.Hour)
	if info := checker.LatestRelease(context.Background(), false); info.Version != "v1.2.0" {
		t.Fatalf("故障后重试结果不符: %+v", info)
	}
	if hits != 5 {
		t.Fatalf("故障后再次查询应重试上游，hits=%d", hits)
	}
}
