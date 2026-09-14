package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/config"
)

// ---- 密钥登录（POST /api/v1/login JSON 通道）----

func TestLoginSuccessCookieFlagsAndAudit(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)
	resp := e.attemptLogin(j, e.key)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("正确密钥应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out apiWriteOK
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK {
		t.Errorf("登录成功响应应为 {\"ok\":true}，得到 %q", "见上")
	}
	var sess *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			sess = c
		}
	}
	if sess == nil {
		t.Fatal("登录响应应设置会话 Cookie")
	}
	if !sess.HttpOnly || !sess.Secure || sess.SameSite != http.SameSiteLaxMode || sess.Path != "/" {
		t.Errorf("会话 Cookie 缺少安全属性：%+v", sess)
	}
	if !e.containsAction("auth.login") {
		t.Error("登录成功应写审计")
	}
	// 已登录可访问 SPA 管理端入口
	next := e.do(j, "GET", "/admin", "", "")
	if next.StatusCode != http.StatusOK {
		t.Fatalf("登录后 SPA 入口应 200，得到 %d", next.StatusCode)
	}
	body := bodyOf(t, next)
	if !strings.Contains(body, `id="root"`) {
		t.Errorf("SPA 入口内容异常：%q", body)
	}
	// 审计与响应不得包含明文密钥
	entries, err := e.st.ListAudit(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	for _, en := range entries {
		if strings.Contains(en.AfterJSON, e.key) {
			t.Fatal("审计详情不得出现明文访问密钥")
		}
	}
	if strings.Contains(body, e.key) {
		t.Error("SPA 壳响应不得出现明文访问密钥")
	}
}

// ---- 登录失败与限流 ----

func TestLoginWrongKeyRejected(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)
	resp := e.attemptLogin(j, "wrong-key")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误密钥应 401，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), string(apperr.CodeWebAuthFailed))
	if !e.containsAction("auth.login_failed") {
		t.Error("登录失败应写审计")
	}
	// 审计详情只允许 method/ip：提交的密钥（含错误密钥）不得进入审计
	entries, err := e.st.ListAudit(context.Background(), 10, 0)
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.AfterJSON, "wrong-key") {
			t.Error("登录失败审计不得包含提交的密钥")
		}
	}
}

// 5 次/分钟失败后锁定；退避期满恢复；连续触发退避时长翻倍。
func TestLoginRateLimitAndBackoff(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	// 窗口内 5 次失败：前 4 次 401，第 5 次触发锁定（仍 401）
	for i := 1; i <= 5; i++ {
		resp := e.attemptLogin(j, "wrong-key")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("第 %d 次失败应 401，得到 %d", i, resp.StatusCode)
		}
		bodyOf(t, resp)
	}
	// 第 6 次：即使密钥正确也被限流（IP 维度锁定）
	if resp := e.attemptLogin(j, e.key); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("锁定期内正确密钥也应 429，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}

	// 首档退避 1 分钟：59 秒后仍锁定，61 秒后恢复
	e.clock.Advance(59 * time.Second)
	if resp := e.attemptLogin(j, e.key); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("退避期内应 429，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	e.clock.Advance(2 * time.Second)
	if resp := e.attemptLogin(j, e.key); resp.StatusCode != http.StatusOK {
		t.Fatalf("退避期满后正确密钥应可登录，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}

	// 再次触发锁定：全局维度退避翻倍为 2 分钟（成功登录只重置 IP 维度）
	j2 := newJar(t)
	for i := 0; i < 5; i++ {
		resp := e.attemptLogin(j2, "wrong-key")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("第二轮第 %d 次失败应 401，得到 %d", i, resp.StatusCode)
		}
		bodyOf(t, resp)
	}
	e.clock.Advance(61 * time.Second) // 超过 IP 维度首档 1 分钟
	if resp := e.attemptLogin(j2, e.key); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("全局退避（2 分钟）未到期应 429，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	e.clock.Advance(61 * time.Second) // 累计超过 2 分钟
	if resp := e.attemptLogin(j2, e.key); resp.StatusCode != http.StatusOK {
		t.Fatalf("全局退避到期后应可登录，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
}

// ---- CSRF ----

// 登录请求双提交 CSRF：Cookie 缺失或与 X-CSRF-Token 头不一致均拒绝
// （403 JSON），且不计入登录失败限流。
func TestLoginCSRFFailures(t *testing.T) {
	e := newTestEnv(t, nil)
	body, err := json.Marshal(apiLoginRequest{AccessKey: e.key})
	if err != nil {
		t.Fatalf("编码登录请求失败: %v", err)
	}

	// 完全不携带 Cookie（无 token 也不行）
	j := newJar(t)
	resp := e.apiPost(j, "/api/v1/login", "some-token", string(body))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("缺 Cookie 的双提交应 403，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), string(apperr.CodeWebCSRFInvalid))

	// Cookie 与请求头不一致
	j2 := newJar(t)
	e.loginCSRF(j2) // 种下 Cookie
	resp = e.apiPost(j2, "/api/v1/login", "mismatched", string(body))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("不一致的双提交应 403，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	// CSRF 失败不计入限流：正确提交仍可成功登录
	resp = e.attemptLogin(j2, e.key)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CSRF 失败不应触发限流，正确登录应 200，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

// 登录 CSRF 端点：no-store、双提交 Cookie 安全属性、同一 Cookie 复用同一 token。
func TestLoginCSRFEndpoint(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	resp := e.do(j, http.MethodGet, "/api/v1/login/csrf", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登录 CSRF 端点应 200，得到 %d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("登录 CSRF 响应应 no-store，得到 %q", cc)
	}
	var view apiLoginCSRFView
	decodeAPIJSON(t, bodyOf(t, resp), &view)
	if view.CSRFToken == "" {
		t.Fatal("应返回 csrf_token")
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == loginCSRFCookieName {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("应种下登录 CSRF Cookie")
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/" {
		t.Errorf("登录 CSRF Cookie 缺少安全属性：%+v", cookie)
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("登录 CSRF Cookie 应为 SameSite=Lax，得到 %v", cookie.SameSite)
	}

	// 携带已有 Cookie 再次请求：复用同一 token（不轮换，避免并发登录页失配）
	resp = e.do(j, http.MethodGet, "/api/v1/login/csrf", "", "")
	var view2 apiLoginCSRFView
	decodeAPIJSON(t, bodyOf(t, resp), &view2)
	if view2.CSRFToken != view.CSRFToken {
		t.Errorf("同一 Cookie 应复用同一 token：%q vs %q", view.CSRFToken, view2.CSRFToken)
	}

	// 登录 API 的方法回退：GET /api/v1/login 应 405 JSON
	resp = e.do(j, http.MethodGet, "/api/v1/login", "", "")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/v1/login 应 405，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

// 非法请求体：非 JSON、空密钥均 400，且不写登录失败审计（不是密钥猜测）。
func TestLoginBadRequestBody(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)
	e.loginCSRF(j) // 种下登录 CSRF Cookie 与 token

	resp := e.apiPost(j, "/api/v1/login", j.get(loginCSRFCookieName), "{not-json")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON 应 400，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	resp = e.apiPost(j, "/api/v1/login", j.get(loginCSRFCookieName), `{"access_key":"  "}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空密钥应 400，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	if e.containsAction("auth.login_failed") {
		t.Error("请求体非法不应写登录失败审计")
	}
}

// 已登录变更请求缺/错 CSRF token 一律 403（当前会话级 CSRF 由 /api/v1
// 写端点经 X-CSRF-Token 头校验；以登出端点为例）。
func TestSessionCSRFRequired(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	// 缺 token → 403
	resp := e.apiPost(j, "/api/v1/session/logout", "", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("缺 CSRF 应 403，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	// 错误 token → 403
	resp = e.apiPost(j, "/api/v1/session/logout", "wrong-token", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("错误 CSRF 应 403，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	// 带正确 token 的登出可用（token 取自 API bootstrap，与会话同源）
	csrf := e.sessionCSRF(t, j)
	resp = e.apiPost(j, "/api/v1/session/logout", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("带正确 CSRF 的登出应 200，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

// ---- 会话生命周期 ----

// 12 小时滑动过期：活动会续期，闲置满 TTL 失效。
func TestSessionSlidingAndExpiry(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	start := e.clock.Now()

	// 8 小时后访问：仍有效，并把过期时间推到此刻 +12h
	e.clock.Advance(8 * time.Hour)
	resp := e.do(j, "GET", "/admin", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("8 小时后会话应有效，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
	ws, err := e.st.GetWebSession(context.Background(), hashValue(j.get(sessionCookieName)))
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	wantExpiry := start.Add(8 * time.Hour).Add(sessionTTL).UnixMilli()
	if ws.ExpiresAt != wantExpiry {
		t.Errorf("滑动续期应把过期推到 %d，得到 %d", wantExpiry, ws.ExpiresAt)
	}

	// 再过 13 小时（距上次活动 13h > 12h）：失效
	e.clock.Advance(13 * time.Hour)
	resp = e.do(j, "GET", "/admin", "", "")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("闲置超过 12 小时应失效，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

// 登出只结束当前设备（经 /api/v1/session/logout）。
func TestLogoutCurrentDeviceOnly(t *testing.T) {
	e := newTestEnv(t, nil)
	j1 := e.login(t)
	j2 := e.login(t)

	csrf := e.sessionCSRF(t, j1)
	resp := e.apiPost(j1, "/api/v1/session/logout", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("登出应 200，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	if resp := e.do(j1, "GET", "/admin", "", ""); resp.StatusCode != http.StatusFound {
		t.Fatalf("登出设备应失效，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	if resp := e.do(j2, "GET", "/admin", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("另一设备会话应保持有效，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	if !e.containsAction("auth.logout") {
		t.Error("登出应写审计")
	}
}

// ---- 限流器与 state 存储单元测试 ----

func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()

	for i := 0; i < 4; i++ {
		l.RecordFailure("k", now)
	}
	if ok, _ := l.Allow("k", now); !ok {
		t.Fatal("4 次失败不应锁定")
	}
	l.RecordFailure("k", now) // 第 5 次：触发首档锁定
	if ok, retry := l.Allow("k", now); ok || retry != time.Minute {
		t.Fatalf("第 5 次失败应锁定 1 分钟，得到 ok=%v retry=%v", ok, retry)
	}
	// 退避期满后放行
	if ok, _ := l.Allow("k", now.Add(time.Minute)); !ok {
		t.Fatal("退避期满应放行")
	}
	// 窗口外的新失败重新计数：第 5 次失败锁定时长翻倍为 2 分钟
	later := now.Add(10 * time.Minute)
	for i := 0; i < 5; i++ {
		l.RecordFailure("k", later)
	}
	if ok, retry := l.Allow("k", later); ok || retry != 2*time.Minute {
		t.Fatalf("第二次锁定应退避翻倍为 2 分钟，得到 ok=%v retry=%v", ok, retry)
	}
	// Reset 清除计数与锁定
	l.Reset("k")
	if ok, _ := l.Allow("k", later); !ok {
		t.Fatal("Reset 后应放行")
	}
}

// 超过清理门槛的闲置条目会被移除，锁定中的条目保留。
func TestLoginLimiterPrunesStaleEntries(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()

	// stale：未锁定、窗口早已过期 1 小时以上 → 清理
	l.entries["stale"] = &failCounter{fails: 2, windowStart: now.Add(-2 * time.Hour)}
	// active：窗口新鲜 → 保留
	l.entries["active"] = &failCounter{fails: 2, windowStart: now.Add(-time.Minute)}
	// locked：仍在锁定期 → 保留
	l.entries["locked"] = &failCounter{fails: 0, windowStart: now.Add(-2 * time.Hour),
		lockedUntil: now.Add(10 * time.Minute), backoff: time.Hour}

	l.RecordFailure("fresh", now)

	for _, key := range []string{"stale"} {
		if _, exists := l.entries[key]; exists {
			t.Errorf("闲置条目 %q 应被清理", key)
		}
	}
	for _, key := range []string{"active", "locked", "fresh"} {
		if _, exists := l.entries[key]; !exists {
			t.Errorf("条目 %q 不应被清理", key)
		}
	}
}

func TestOAuthStateStoreSingleUse(t *testing.T) {
	ss := newOAuthStateStore()
	now := time.Now()

	v, err := ss.put(oauthModeLogin, now)
	if err != nil {
		t.Fatalf("生成 state 失败: %v", err)
	}
	mode, ok := ss.take(v, now)
	if !ok || mode != oauthModeLogin {
		t.Fatalf("首次取出应成功且模式正确：ok=%v mode=%s", ok, mode)
	}
	if _, ok := ss.take(v, now); ok {
		t.Fatal("state 应为单次使用，第二次取出必须失败")
	}
	// 过期失效
	v2, _ := ss.put(oauthModeBind, now)
	if _, ok := ss.take(v2, now.Add(oauthStateTTL+time.Second)); ok {
		t.Fatal("过期 state 应失效")
	}
}

// ---- 审计来源 IP ----

func TestClientIPTrustedProxy(t *testing.T) {
	// 模拟真实链路：客户端 prepend 伪造前缀，可信代理在末尾追加真实对端
	mkReq := func(xff string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.0.0.1:51234"
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	eTrust := newTestEnv(t, func(c *config.Config) { c.WebTrustedProxy = true })
	// 末段是代理追加的真实客户端 IP：即使前面有伪造前缀也取末段
	if got := eTrust.srv.clientIP(mkReq("198.51.100.7, 203.0.113.9")); got != "203.0.113.9" {
		t.Errorf("信任反代时应取 X-Forwarded-For 末段（代理追加的真实对端），得到 %q", got)
	}
	// 单段（无伪造前缀）同样取该值
	if got := eTrust.srv.clientIP(mkReq("203.0.113.9")); got != "203.0.113.9" {
		t.Errorf("单段 XFF 应取该段，得到 %q", got)
	}

	// 非信任时无论伪造什么都使用 RemoteAddr
	ePlain := newTestEnv(t, nil)
	if got := ePlain.srv.clientIP(mkReq("198.51.100.7, 203.0.113.9")); got != "10.0.0.1" {
		t.Errorf("非信任时应取 RemoteAddr 主机，得到 %q", got)
	}
	if got := ePlain.srv.clientIP(mkReq("")); got != "10.0.0.1" {
		t.Errorf("无 XFF 时应取 RemoteAddr 主机，得到 %q", got)
	}
}

// ---- 并发登录失败计数（配合 -race）----

func TestConcurrentLoginFailures(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)
	// 预先取一次登录 CSRF 种下 Cookie，随后并发提交错误密钥
	e.loginCSRF(j)

	const workers, perWorker = 8, 3 // 共 24 次 > 5 次，必然触发锁定
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				csrf := j.get(loginCSRFCookieName)
				resp := e.apiPost(j, "/api/v1/login", csrf, `{"access_key":"bad"}`)
				bodyOf(t, resp)
			}
		}()
	}
	wg.Wait()

	// 锁定后正确密钥也被拒绝（IP 与全局维度计数均准确达到阈值）
	if resp := e.attemptLogin(j, e.key); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("并发失败累计后应锁定（429），得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
}
