package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
)

// fakeGitHub 模拟 GitHub OAuth 的 token/user 端点：
// code → token 映射与 token → 用户映射均显式声明，未注册的 code/token 返回 401/404。
type fakeGitHub struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	tokenReq []string // 收到的 token 请求的 client_id/secret/code/redirect_uri 快照
}

// newFakeGitHub 启动假 GitHub 服务；users 以 token 为键。
func newFakeGitHub(t *testing.T, codeToToken map[string]string, tokenToUser map[string]githubUser) *fakeGitHub {
	t.Helper()
	fg := &fakeGitHub{t: t}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		fg.mu.Lock()
		fg.tokenReq = append(fg.tokenReq,
			r.PostFormValue("client_id"),
			r.PostFormValue("client_secret"),
			r.PostFormValue("code"),
			r.PostFormValue("redirect_uri"))
		fg.mu.Unlock()
		tok, ok := codeToToken[r.PostFormValue("code")]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": tok})
	})

	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		u, ok := tokenToUser[strings.TrimPrefix(auth, "Bearer ")]
		if !ok || auth == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(u)
	})

	fg.srv = httptest.NewServer(mux)
	t.Cleanup(fg.srv.Close)
	return fg
}

// endpoints 返回指向假服务的端点集合。
func (fg *fakeGitHub) endpoints() OAuthEndpoints {
	return OAuthEndpoints{
		Authorize: fg.srv.URL + "/login/oauth/authorize",
		Token:     fg.srv.URL + "/login/oauth/access_token",
		User:      fg.srv.URL + "/user",
	}
}

// newOAuthServer 构造启用 GitHub OAuth 且指向假服务的测试环境。
func newOAuthServer(t *testing.T, fg *fakeGitHub) *testEnv {
	t.Helper()
	return newTestEnvOpts(t, func(c *config.Config, o *Options) {
		c.GitHubClientID = "test-client-id"
		c.GitHubClientSecret = "test-client-secret"
		o.OAuth = fg.endpoints()
	})
}

// startLoginFlow 发起登录模式 OAuth，返回 GitHub 授权 URL。
func (e *testEnv) startLoginFlow(j *jar) *url.URL {
	e.t.Helper()
	resp := e.do(j, "GET", "/auth/github", "", "")
	if resp.StatusCode != http.StatusFound {
		e.t.Fatalf("发起 OAuth 应 302，得到 %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	bodyOf(e.t, resp)
	u, err := url.Parse(loc)
	if err != nil {
		e.t.Fatalf("授权地址解析失败: %v", err)
	}
	return u
}

// stateOf 从授权 URL 提取 state 参数。
func stateOf(t *testing.T, u *url.URL) string {
	t.Helper()
	v := u.Query().Get("state")
	if v == "" {
		t.Fatal("授权地址应携带 state")
	}
	return v
}

// callback 模拟 GitHub 把浏览器重定向回回调地址。
func (e *testEnv) callback(j *jar, code, state string) *http.Response {
	e.t.Helper()
	q := url.Values{"code": {code}, "state": {state}}
	return e.do(j, "GET", "/auth/github/callback?"+q.Encode(), "", "")
}

// ---- 通道配置边界 ----

// 未配置 GitHub 凭据时通道隐藏：SPA 登录页不展示入口（login/csrf 的
// github_enabled=false），直接发起 OAuth 重定向到设置页错误横幅。
func TestOAuthUnconfigured(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	resp := e.do(j, "GET", "/api/v1/login/csrf", "", "")
	var view apiLoginCSRFView
	decodeAPIJSON(t, bodyOf(t, resp), &view)
	if view.GitHubEnabled {
		t.Error("未配置时 login/csrf 应标记 github_enabled=false")
	}
	resp = e.do(j, "GET", "/auth/github", "", "")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("未配置时发起 OAuth 应 302，得到 %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/settings/oauth?oauth=error" {
		t.Errorf("未配置时发起 OAuth 应重定向到错误横幅，得到 %q", loc)
	}
	bodyOf(t, resp)
}

// ---- 登录模式 ----

// state 无效/单次使用；未绑定与错误账号被拒并审计；已绑定账号可登录。
// 当前失败分支不再渲染 HTML，统一 302 到设置页错误横幅（受控枚举值）。
func TestOAuthLoginFlow(t *testing.T) {
	const (
		adminID    = int64(4242)
		adminLogin = "spore-admin"
	)
	fg := newFakeGitHub(t,
		map[string]string{"good-code": "tok-ok"},
		map[string]githubUser{"tok-ok": {ID: adminID, Login: adminLogin}})
	e := newOAuthServer(t, fg)
	j := newJar(t)

	// state 缺失/伪造：拒绝
	if resp := e.callback(j, "good-code", "forged-state"); resp.StatusCode != http.StatusFound {
		t.Fatalf("伪造 state 应 302，得到 %d", resp.StatusCode)
	} else if loc := resp.Header.Get("Location"); loc != "/admin/settings/oauth?oauth=error" {
		t.Fatalf("伪造 state 应重定向到错误横幅，得到 %q", loc)
	} else {
		bodyOf(t, resp)
	}

	// 未绑定任何账号：完成交换后仍拒绝，不回显账号信息，写失败审计
	authURL := e.startLoginFlow(j)
	state := stateOf(t, authURL)
	resp := e.callback(j, "good-code", state)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("未绑定时应 302，得到 %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/admin/settings/oauth?oauth=error" {
		t.Errorf("拒绝应重定向到错误横幅，得到 %q", loc)
	}
	if strings.Contains(loc, adminLogin) || strings.Contains(loc, "4242") {
		t.Errorf("重定向不得携带 GitHub 账号信息：%q", loc)
	}
	if !e.containsAction("auth.login_failed") {
		t.Error("OAuth 拒绝应写登录失败审计")
	}
	// state 已消费：重放失败
	if resp := e.callback(j, "good-code", state); resp.StatusCode != http.StatusFound {
		t.Fatalf("state 单次使用，重放应 302，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}

	// 绑定了别的账号（数字 ID 不一致）：拒绝
	if err := saveGitHubBinding(context.Background(), e.st,
		GitHubBinding{ID: 9999, Login: "someone-else", BoundAt: 1}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	state = stateOf(t, e.startLoginFlow(j))
	if resp := e.callback(j, "good-code", state); resp.StatusCode != http.StatusFound {
		t.Fatalf("非绑定账号应 302，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}

	// 绑定正确账号：登录成功并建立会话（直达 SPA 管理端）
	if err := saveGitHubBinding(context.Background(), e.st,
		GitHubBinding{ID: adminID, Login: adminLogin, BoundAt: 1}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	state = stateOf(t, e.startLoginFlow(j))
	resp = e.callback(j, "good-code", state)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("绑定账号应 302 进入 SPA 管理端，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/admin" {
		t.Errorf("登录后应跳转 /admin，得到 %q", loc)
	}
	if j.get(sessionCookieName) == "" {
		t.Fatal("OAuth 登录应签发会话 Cookie")
	}
	bodyOf(t, resp)

	// token 交换确实携带 client 凭据与回调地址
	fg.mu.Lock()
	last := fg.tokenReq
	fg.mu.Unlock()
	if len(last) < 4 {
		t.Fatalf("token 端点应被调用，记录：%v", last)
	}
	if last[0] != "test-client-id" || last[1] != "test-client-secret" || last[2] != "good-code" {
		t.Errorf("token 请求参数异常：%v", last[:3])
	}
	if !strings.HasSuffix(last[3], "/auth/github/callback") {
		t.Errorf("redirect_uri 应指向回调地址，得到 %q", last[3])
	}

	// 会话可访问受保护的 SPA 入口
	if resp := e.do(j, "GET", "/admin", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("OAuth 会话应可访问 SPA 入口，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	if !e.containsAction("auth.login") {
		t.Error("OAuth 登录成功应写审计")
	}
}

// ---- 通道独立性 ----

// 任一通道故障不影响另一通道：GitHub 交换失败后密钥登录仍可用；
// 密钥未初始化时已绑定账号的 OAuth 登录仍可用。
func TestChannelIndependence(t *testing.T) {
	const (
		adminID    = int64(31415)
		adminLogin = "independent-admin"
	)
	// 假 GitHub 不注册任何有效授权码：token 交换必然失败（模拟 GitHub 故障）
	fg := newFakeGitHub(t,
		map[string]string{},
		map[string]githubUser{})
	e := newOAuthServer(t, fg)
	j := newJar(t)

	state := stateOf(t, e.startLoginFlow(j))
	if resp := e.callback(j, "any-code", state); resp.StatusCode != http.StatusFound {
		t.Fatalf("GitHub 故障时 OAuth 应失败，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	// 密钥通道不受影响
	if resp := e.attemptLogin(j, e.key); resp.StatusCode != http.StatusOK {
		t.Fatalf("GitHub 故障不应影响密钥登录，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}

	// 反向：密钥通道不可用（删除哈希）时，已绑定账号仍可 OAuth 登录。
	// OAuth 端点在服务构造时固定，换一套带有效授权码的假 GitHub 重建环境验证。
	fg2 := newFakeGitHub(t,
		map[string]string{"ok-code": "tok-2"},
		map[string]githubUser{"tok-2": {ID: adminID, Login: adminLogin}})
	e2 := newTestEnvOpts(t, func(c *config.Config, o *Options) {
		c.GitHubClientID = "test-client-id"
		c.GitHubClientSecret = "test-client-secret"
		o.OAuth = fg2.endpoints()
	})
	if err := saveGitHubBinding(context.Background(), e2.st,
		GitHubBinding{ID: adminID, Login: adminLogin, BoundAt: 1}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	if err := e2.st.DeleteSetting(context.Background(), settingKeyAccessKeyHash); err != nil {
		t.Fatalf("删除密钥哈希失败: %v", err)
	}
	if resp := e2.attemptLogin(newJar(t), e2.key); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("密钥已删除应 401，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	j2 := newJar(t)
	state = stateOf(t, e2.startLoginFlow(j2))
	if resp := e2.callback(j2, "ok-code", state); resp.StatusCode != http.StatusFound {
		t.Fatalf("密钥通道不可用时 OAuth 登录仍应成功，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	if j2.get(sessionCookieName) == "" {
		t.Fatal("OAuth 登录应签发会话 Cookie")
	}
	if resp := e2.do(j2, "GET", "/admin", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("OAuth 会话应可访问 SPA 入口，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
}

// ---- 绑定/解绑管理 ----

func TestOAuthBindAndUnbind(t *testing.T) {
	const (
		adminID    = int64(777)
		adminLogin = "binding-admin"
	)
	fg := newFakeGitHub(t,
		map[string]string{"bind-code": "tok-bind"},
		map[string]githubUser{"tok-bind": {ID: adminID, Login: adminLogin}})
	e := newOAuthServer(t, fg)
	j := e.login(t) // 先用密钥登录

	// 发起绑定：API 返回一次性授权跳转 URL，携带 bind 模式 state
	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/oauth/bind", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("发起绑定应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var bindOut struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &bindOut)
	loc, err := url.Parse(bindOut.AuthorizeURL)
	if err != nil || !strings.HasPrefix(loc.String(), fg.srv.URL+"/login/oauth/authorize") {
		t.Fatalf("绑定应返回 GitHub 授权地址，得到 %q err=%v", bindOut.AuthorizeURL, err)
	}
	state := stateOf(t, loc)

	// 回调完成绑定：写入绑定、全部会话失效；结果由设置页横幅承接（302 受控枚举）
	resp = e.callback(j, "bind-code", state)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("绑定回调应 302，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/settings/oauth?oauth=bound" {
		t.Fatalf("绑定成功应重定向到设置页横幅，得到 %q", loc)
	}
	bodyOf(t, resp)
	binding, ok, err := loadGitHubBinding(context.Background(), e.st)
	if err != nil || !ok || binding.ID != adminID {
		t.Fatalf("绑定应写入 settings：ok=%v binding=%+v err=%v", ok, binding, err)
	}
	if !e.containsAction("oauth.bind") {
		t.Error("绑定应写审计")
	}
	// 原会话已全部失效
	if resp := e.do(j, "GET", "/admin", "", ""); resp.StatusCode != http.StatusFound {
		t.Fatalf("绑定后全部会话应失效，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}

	// 解绑：重新登录后操作，成功后全部会话再次失效
	j2 := e.login(t)
	csrf = apiCSRFToken(t, e, j2)
	resp = e.apiPost(j2, "/api/v1/oauth/unbind", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("解绑应成功，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if _, ok, _ := loadGitHubBinding(context.Background(), e.st); ok {
		t.Fatal("解绑后应删除绑定")
	}
	if !e.containsAction("oauth.unbind") {
		t.Error("解绑应写审计")
	}
	if resp := e.do(j2, "GET", "/admin", "", ""); resp.StatusCode != http.StatusFound {
		t.Fatalf("解绑后全部会话应失效，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	// 解绑后 OAuth 登录回到“未绑定”拒绝路径
	j3 := newJar(t)
	state = stateOf(t, e.startLoginFlow(j3))
	if resp := e.callback(j3, "bind-code", state); resp.StatusCode != http.StatusFound {
		t.Fatalf("解绑后 OAuth 登录应被拒绝，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
}

// GitHub 侧交换失败（无效授权码）的边界（未知 action 的 400 语义由
// api_privileged_test.go 的 API 用例覆盖）。
func TestOAuthEdgeCases(t *testing.T) {
	fg := newFakeGitHub(t,
		map[string]string{"good-code": "tok-ok"},
		map[string]githubUser{"tok-ok": {ID: 1, Login: "u"}})
	e := newOAuthServer(t, fg)

	// 无效授权码：交换失败不再渲染错误页，重定向到受控错误横幅
	j := newJar(t)
	state := stateOf(t, e.startLoginFlow(j))
	resp := e.callback(j, "expired-code", state)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("无效授权码应 302，得到 %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if loc != "/admin/settings/oauth?oauth=error" {
		t.Fatalf("交换失败应重定向到错误横幅，得到 %q", loc)
	}
	// 重定向不得携带 code/state 等流程值
	if strings.Contains(loc, "expired-code") || strings.Contains(loc, state) {
		t.Errorf("重定向不得携带流程值：%q", loc)
	}
	bodyOf(t, resp)
}
