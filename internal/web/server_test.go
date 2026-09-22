package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// testLogger 返回丢弃输出的 logger，保持测试输出干净。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeClock 是可手动推进的时钟（登录限流与滑续期测试的确定性时间源）。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// jar 手工管理 Cookie：net/http/cookiejar 会拒绝在 http 上回传 Secure Cookie，
// 测试里直接解析 Set-Cookie 并在请求头回传，行为等价且可控。
type jar struct {
	t       *testing.T
	mu      sync.Mutex
	cookies map[string]string
}

func newJar(t *testing.T) *jar {
	return &jar{t: t, cookies: make(map[string]string)}
}

// store 吸收响应里的 Set-Cookie（含删除：MaxAge < 0）。
func (j *jar) store(resp *http.Response) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range resp.Cookies() {
		if c.MaxAge < 0 {
			delete(j.cookies, c.Name)
			continue
		}
		j.cookies[c.Name] = c.Value
	}
}

// header 生成应回传的 Cookie 请求头；无 Cookie 时返回空串。
func (j *jar) header() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	parts := make([]string, 0, len(j.cookies))
	for k, v := range j.cookies {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, "; ")
}

// get 取某个 Cookie 的当前值（不存在返回空串）。
func (j *jar) get(name string) string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cookies[name]
}

// testEnv 聚合一个可请求的测试环境：临时数据库 + 服务 + httptest 服务器。
type testEnv struct {
	t     *testing.T
	st    *store.Store
	srv   *Server
	ts    *httptest.Server
	clock *fakeClock
	key   string // EnsureAccessKey 生成并打印的明文密钥（测试持有）
}

// newTestEnv 构造测试环境；mutate 可调整 Config（如启用 GitHub OAuth）。
func newTestEnv(t *testing.T, mutate func(*config.Config)) *testEnv {
	t.Helper()
	return newTestEnvOpts(t, func(c *config.Config, _ *Options) {
		if mutate != nil {
			mutate(c)
		}
	})
}

// newTestEnvOpts 是 newTestEnv 的完整形态：可同时调整 Config 与 Options
// （OAuth 测试注入假 GitHub 端点、管理页测试注入假 MTProto 会话等）。
// 默认装配 access 服务与内存队列（管理页面依赖）。
func newTestEnvOpts(t *testing.T, mutate func(*config.Config, *Options)) *testEnv {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), testLogger())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	keyBuf := &bytes.Buffer{}
	if _, err := EnsureAccessKey(context.Background(), st, keyBuf, testLogger()); err != nil {
		t.Fatalf("初始化访问密钥失败: %v", err)
	}

	cfg := config.Config{WebAddr: "127.0.0.1:8080", WorkerCount: 2}
	opt := Options{Store: st, Cfg: cfg, Log: testLogger()}
	if mutate != nil {
		mutate(&cfg, &opt)
	}
	clock := &fakeClock{now: time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC)} // 上海 12:00
	if opt.Now == nil {
		opt.Now = clock.Now
	}
	if opt.Queue == nil {
		opt.Queue = queue.New(8)
	}
	if opt.Access == nil {
		q, ok := opt.Queue.(access.Enqueuer)
		if !ok {
			t.Fatalf("测试队列须实现 access.Enqueuer，得到 %T", opt.Queue)
		}
		accessSvc, err := access.New(access.Options{Store: st, Queue: q, Log: testLogger(), Now: clock.Now})
		// 缓存补写资格判定按当前缓存频道过滤（v24）：测试环境固定一个频道，
		// 与 api_write_dump_backfill_test 落库条目的 DumpChannelID 对齐。
		accessSvc.SetDumpChannelID(func() int64 { return -100777 })
		if err != nil {
			t.Fatalf("构造访问控制服务失败: %v", err)
		}
		opt.Access = accessSvc
	}
	opt.Store, opt.Cfg, opt.Log = st, cfg, testLogger()
	srv, err := New(opt)
	if err != nil {
		t.Fatalf("构造 Web 服务失败: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &testEnv{t: t, st: st, srv: srv, ts: ts, clock: clock, key: extractKey(t, keyBuf.String())}
}

// extractKey 从密钥打印块中提取明文密钥（第 2 行）。
func extractKey(t *testing.T, printed string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(printed), "\n")
	if len(lines) < 2 {
		t.Fatalf("密钥输出格式异常：%q", printed)
	}
	return strings.TrimSpace(lines[1])
}

// do 发出请求并回传/吸收 Cookie；不自动跟随重定向（便于断言 302）。
func (e *testEnv) do(j *jar, method, path, contentType, body string) *http.Response {
	e.t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, e.ts.URL+path, rdr)
	if err != nil {
		e.t.Fatalf("构造请求失败: %v", err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if h := j.header(); h != "" {
		req.Header.Set("Cookie", h)
	}
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatalf("请求 %s %s 失败: %v", method, path, err)
	}
	j.store(resp)
	return resp
}

// bodyOf 读取并关闭响应体。
func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应体失败: %v", err)
	}
	return string(b)
}

// sessionCSRF 从 GET /api/v1/session 取会话级 CSRF token。认证 GET 页面
// 已统一重定向，写请求测试经 bootstrap 取 token；该 token 与原表单
// 隐藏域同源（web_sessions.csrf_token），POST 表单路由可直接使用。
func (e *testEnv) sessionCSRF(t *testing.T, j *jar) string {
	t.Helper()
	resp := e.do(j, http.MethodGet, "/api/v1/session", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("会话 bootstrap 应 200，得到 %d", resp.StatusCode)
	}
	var view apiSessionView
	decodeAPIJSON(t, bodyOf(t, resp), &view)
	if view.CSRFToken == "" {
		t.Fatal("bootstrap 应交付会话级 CSRF token")
	}
	return view.CSRFToken
}

// loginCSRF 经 GET /api/v1/login/csrf 取登录级 CSRF token 并吸收
// Set-Cookie（spore_login_csrf）；返回 token 供 POST /api/v1/login 回传。
func (e *testEnv) loginCSRF(j *jar) string {
	e.t.Helper()
	resp := e.do(j, http.MethodGet, "/api/v1/login/csrf", "", "")
	if resp.StatusCode != http.StatusOK {
		e.t.Fatalf("登录 CSRF 端点应 200，得到 %d", resp.StatusCode)
	}
	var view apiLoginCSRFView
	decodeAPIJSON(e.t, bodyOf(e.t, resp), &view)
	if view.CSRFToken == "" {
		e.t.Fatal("登录 CSRF 端点应返回 token")
	}
	if j.get(loginCSRFCookieName) == "" {
		e.t.Fatal("登录 CSRF 端点未种下双提交 Cookie")
	}
	return view.CSRFToken
}

// attemptLogin 执行一次密钥登录（自动取登录 CSRF，POST /api/v1/login JSON）。
func (e *testEnv) attemptLogin(j *jar, key string) *http.Response {
	e.t.Helper()
	csrf := e.loginCSRF(j)
	body, err := json.Marshal(apiLoginRequest{AccessKey: key})
	if err != nil {
		e.t.Fatalf("编码登录请求失败: %v", err)
	}
	return e.apiPost(j, "/api/v1/login", csrf, string(body))
}

// login 成功登录并返回携带会话的 jar；失败即中止测试。
func (e *testEnv) login(t *testing.T) *jar {
	t.Helper()
	j := newJar(t)
	resp := e.attemptLogin(j, e.key)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录应返回 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if j.get(sessionCookieName) == "" {
		t.Fatal("登录后应设置会话 Cookie")
	}
	return j
}

// auditActions 返回当前全部审计动作名（时间倒序）。
func (e *testEnv) auditActions() []string {
	e.t.Helper()
	entries, err := e.st.ListAudit(context.Background(), 100, 0)
	if err != nil {
		e.t.Fatalf("读取审计失败: %v", err)
	}
	actions := make([]string, 0, len(entries))
	for _, en := range entries {
		actions = append(actions, en.Action)
	}
	return actions
}

// containsAction 判断审计中是否出现某动作。
func (e *testEnv) containsAction(action string) bool {
	e.t.Helper()
	for _, a := range e.auditActions() {
		if a == action {
			return true
		}
	}
	return false
}

// ---- 未认证与安全头 ----

func TestUnauthenticatedAccess(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	for _, path := range []string{"/", "/login", "/settings/oauth"} {
		resp := e.do(j, "GET", path, "", "")
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("旧入口 %s 未认证应返回 404，得到 %d", path, resp.StatusCode)
		}
		bodyOf(t, resp)
	}
	// 登录门槛移到 SPA 入口：未认证 /admin 重定向到 SPA 登录壳
	resp := e.do(j, "GET", "/admin", "", "")
	if loc := resp.Header.Get("Location"); loc != "/admin/login" {
		t.Errorf("未认证 GET /admin 应重定向到 /admin/login，得到 %q", loc)
	}
	bodyOf(t, resp)
	// /admin/login 是免认证公开壳：未认证也返回 200
	resp = e.do(j, "GET", "/admin/login", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("未认证 GET /admin/login 应返回公开 SPA 壳 200，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

func TestSecurityHeaders(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)
	for _, path := range []string{"/admin/login", "/healthz"} {
		resp := e.do(j, "GET", path, "", "")
		h := resp.Header
		if got := h.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s：X-Frame-Options 应为 DENY，得到 %q", path, got)
		}
		if got := h.Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s：Referrer-Policy 应为 no-referrer，得到 %q", path, got)
		}
		csp := h.Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
			t.Errorf("%s：CSP 缺少基础指令，得到 %q", path, csp)
		}
		if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s：X-Content-Type-Options 应为 nosniff，得到 %q", path, got)
		}
		bodyOf(t, resp)
	}
}

var spaNonceMetaRe = regexp.MustCompile(`<meta[^>]+name="csp-nonce"[^>]+content="([^"]+)"`)

// 当前 Go 侧唯一的 HTML 输出是 SPA 应用壳：公开登录壳 /admin/login 与受保护
// 入口共用 serveSPAShell，均须注入每请求 nonce 且不放宽 style 元素策略。
func TestAdminSPACSPNonce(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	var previous string
	for _, tc := range []struct {
		path string
		auth bool
	}{
		{"/admin", true},
		{"/admin/settings", true},
		{"/admin/login", false}, // 公开登录壳：未认证也走同一 nonce/CSP 机制
	} {
		var resp *http.Response
		if tc.auth {
			resp = e.do(j, http.MethodGet, tc.path, "", "")
		} else {
			resp = e.do(newJar(t), http.MethodGet, tc.path, "", "")
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s 应返回 SPA 入口 200，得到 %d", tc.path, resp.StatusCode)
		}
		body := bodyOf(t, resp)
		match := spaNonceMetaRe.FindStringSubmatch(body)
		if match == nil || match[1] == "" {
			t.Fatalf("%s 缺少 CSP nonce 元数据：%s", tc.path, body)
		}
		nonce := match[1]
		csp := resp.Header.Get("Content-Security-Policy")
		if !strings.Contains(csp, "style-src 'self' 'nonce-"+nonce+"'") ||
			!strings.Contains(csp, "style-src-elem 'self' 'nonce-"+nonce+"'") ||
			!strings.Contains(csp, "style-src-attr 'unsafe-inline'") {
			t.Fatalf("%s 的 CSP 未包含预期的 nonce/最小 style 属性兼容策略：%q", tc.path, csp)
		}
		if strings.Contains(csp, "style-src 'self' 'nonce-"+nonce+"' 'unsafe-inline'") ||
			strings.Contains(csp, "style-src-elem 'self' 'nonce-"+nonce+"' 'unsafe-inline'") {
			t.Fatalf("%s 不应放开 style 元素的 unsafe-inline：%q", tc.path, csp)
		}
		if nonce == previous {
			t.Fatalf("SPA 每次响应都应生成不同 nonce：%q", nonce)
		}
		previous = nonce
	}
}

func TestAdminSPACSPNonceFailure(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.nonceFunc = func(int) (string, error) { return "", io.EOF }
	j := e.login(t)

	resp := e.do(j, http.MethodGet, "/admin", "", "")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("nonce 生成失败应返回 500，得到 %d", resp.StatusCode)
	}
	if body := bodyOf(t, resp); strings.Contains(body, cspNoncePlaceholder) {
		t.Fatalf("失败响应不应返回未注入的 nonce 占位符：%s", body)
	}
}

// ---- 探针 ----

func TestHealthzReadyz(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	resp := e.do(j, "GET", "/healthz", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz 应 200，得到 %d", resp.StatusCode)
	}
	body := bodyOf(t, resp)
	if !strings.Contains(body, `"ok"`) {
		t.Errorf("healthz 应返回 ok，得到 %s", body)
	}
	for _, secret := range []string{e.key, "BOT_TOKEN", "session"} {
		if strings.Contains(body, secret) {
			t.Errorf("healthz 响应不应包含敏感信息 %q", secret)
		}
	}

	resp = e.do(j, "GET", "/readyz", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("readyz 应 200，得到 %d", resp.StatusCode)
	}
	if body := bodyOf(t, resp); !strings.Contains(body, `"ready"`) {
		t.Errorf("readyz 应返回 ready，得到 %s", body)
	}
}

// 数据库不可用时 readyz 应 503（healthz 仍 200）。
func TestReadyzWhenStoreDown(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)
	if err := e.st.Close(); err != nil {
		t.Fatalf("关闭数据库失败: %v", err)
	}
	resp := e.do(j, "GET", "/readyz", "", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("数据库关闭后 readyz 应 503，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
	resp = e.do(j, "GET", "/healthz", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz 不依赖数据库，应 200，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

// ---- panic 恢复 ----

// 处理器 panic 被中间件转为 500，不外溢（Bot 主链路所在的其它 goroutine 不受影响）。
func TestPanicRecovery(t *testing.T) {
	e := newTestEnv(t, nil)
	done := make(chan struct{})
	h := e.srv.recoverPanics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(done)
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/panic", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 应被兜底为 500，得到 %d", rec.Code)
	}
	select {
	case <-done:
	default:
		t.Fatal("panic 处理器未被执行")
	}
}

// ---- 密钥生命周期 ----

func TestEnsureAccessKeyPrintsOnce(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "k.db"), testLogger())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer st.Close()

	buf := &bytes.Buffer{}
	created, err := EnsureAccessKey(context.Background(), st, buf, testLogger())
	if err != nil || !created {
		t.Fatalf("首次应生成密钥：created=%v err=%v", created, err)
	}
	first := buf.String()
	key := extractKey(t, first)
	// 32 字节熵 base64url = 43 字符（≥256 位）
	if len(key) < 43 {
		t.Errorf("密钥应至少 43 字符（256 位熵），得到 %d 字符", len(key))
	}
	// 库内只存哈希：settings 原文不含明文
	v, ok, err := st.GetSetting(context.Background(), settingKeyAccessKeyHash)
	if err != nil || !ok {
		t.Fatalf("应写入密钥哈希: ok=%v err=%v", ok, err)
	}
	if strings.Contains(v, key) {
		t.Error("settings 中不得出现明文密钥")
	}

	created, err = EnsureAccessKey(context.Background(), st, buf, testLogger())
	if err != nil || created {
		t.Fatalf("第二次不应重新生成：created=%v err=%v", created, err)
	}
	if buf.String() != first {
		t.Error("已存在时不应再次打印明文密钥")
	}
}

func TestResetAccessKeyInvalidatesSessions(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	// 重置前会话有效
	if resp := e.do(j, "GET", "/admin", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("重置前会话应有效，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}

	buf := &bytes.Buffer{}
	if err := ResetAccessKey(context.Background(), e.st, buf, testLogger()); err != nil {
		t.Fatalf("重置密钥失败: %v", err)
	}
	newKey := extractKey(t, buf.String())
	if newKey == e.key {
		t.Fatal("重置应生成新密钥")
	}

	// 旧会话立即失效
	if resp := e.do(j, "GET", "/admin", "", ""); resp.StatusCode != http.StatusFound {
		t.Fatalf("重置后旧会话应失效，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	// 旧密钥不能登录，新密钥可以
	j2 := newJar(t)
	if resp := e.attemptLogin(j2, e.key); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("旧密钥应被拒绝，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	j3 := newJar(t)
	if resp := e.attemptLogin(j3, newKey); resp.StatusCode != http.StatusOK {
		t.Fatalf("新密钥应能登录，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	if !e.containsAction("access_key.reset") {
		t.Error("密钥重置应写审计")
	}
}
