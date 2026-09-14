package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// GitHub OAuth 端点（可通过 Options.OAuthEndpoints 整体覆盖，供测试注入假服务）。
var defaultOAuthEndpoints = OAuthEndpoints{
	Authorize: "https://github.com/login/oauth/authorize",
	Token:     "https://github.com/login/oauth/access_token",
	User:      "https://api.github.com/user",
}

// OAuthEndpoints 是 GitHub OAuth 三段流程的端点集合。
type OAuthEndpoints struct {
	Authorize string
	Token     string
	User      string
}

// OAuth 流程常量。
const (
	oauthStateTTL    = 10 * time.Minute // state 有效期
	oauthHTTPTimeout = 10 * time.Second // token/用户信息请求超时
	oauthModeLogin   = "login"
	oauthModeBind    = "bind"
)

// ---- OAuth state 存储（随机、单次使用、带过期）----

// oauthStateStore 保存进行中的 OAuth 流程状态：value 为随机 state，
// 单次使用（取出即删），带过期时间；mutex 保护并发访问。
type oauthStateStore struct {
	mu     sync.Mutex
	states map[string]oauthState
}

type oauthState struct {
	mode      string // login | bind
	expiresAt time.Time
}

func newOAuthStateStore() *oauthStateStore {
	return &oauthStateStore{states: make(map[string]oauthState)}
}

// put 生成并保存一个 state，返回其值（拼进 authorize URL 与回调校验）。
func (ss *oauthStateStore) put(mode string, now time.Time) (string, error) {
	v, err := randomToken(32)
	if err != nil {
		return "", err
	}
	ss.mu.Lock()
	defer ss.mu.Unlock()
	// 顺手清理过期项，避免长期运行累积
	for k, st := range ss.states {
		if st.expiresAt.Before(now) {
			delete(ss.states, k)
		}
	}
	ss.states[v] = oauthState{mode: mode, expiresAt: now.Add(oauthStateTTL)}
	return v, nil
}

// take 取出并删除 state（单次使用）；不存在或已过期返回 ok=false。
func (ss *oauthStateStore) take(v string, now time.Time) (mode string, ok bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	st, exists := ss.states[v]
	if !exists {
		return "", false
	}
	delete(ss.states, v)
	if st.expiresAt.Before(now) {
		return "", false
	}
	return st.mode, true
}

// ---- GitHub API 客户端 ----

// githubUser 是 /user 端点返回的最小子集：数字 ID 是稳定标识与绑定比对依据。
type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

// exchangeCode 用授权码换 access token（标准库 HTTP 客户端，带超时）。
// token 只在本次回调内使用，不落库、不入日志。
func (s *Server) exchangeCode(ctx context.Context, code, redirectURI string) (string, error) {
	clientID, clientSecret, enabled := s.oauthCredentials(ctx)
	if !enabled {
		return "", apperr.New(apperr.CodeOAuthExchangeFailed, "GitHub OAuth 未启用")
	}
	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.oauth.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return "", apperr.Wrap(apperr.CodeOAuthExchangeFailed, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", apperr.Wrap(apperr.CodeOAuthExchangeFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", apperr.Wrap(apperr.CodeOAuthExchangeFailed, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", apperr.Wrap(apperr.CodeOAuthExchangeFailed,
			fmt.Errorf("token 端点返回 %d", resp.StatusCode))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AccessToken == "" {
		return "", apperr.Wrap(apperr.CodeOAuthExchangeFailed, errors.New("token 响应缺失 access_token"))
	}
	return out.AccessToken, nil
}

// fetchGitHubUser 用 access token 取 GitHub 数字 ID 与登录名。
func (s *Server) fetchGitHubUser(ctx context.Context, token string) (githubUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.oauth.User, nil)
	if err != nil {
		return githubUser{}, apperr.Wrap(apperr.CodeOAuthExchangeFailed, err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return githubUser{}, apperr.Wrap(apperr.CodeOAuthExchangeFailed, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return githubUser{}, apperr.Wrap(apperr.CodeOAuthExchangeFailed, err)
	}
	if resp.StatusCode != http.StatusOK {
		return githubUser{}, apperr.Wrap(apperr.CodeOAuthExchangeFailed,
			fmt.Errorf("user 端点返回 %d", resp.StatusCode))
	}
	var u githubUser
	if err := json.Unmarshal(body, &u); err != nil || u.ID == 0 {
		return githubUser{}, apperr.Wrap(apperr.CodeOAuthExchangeFailed, errors.New("user 响应缺失数字 ID"))
	}
	return u, nil
}

// externalBase 推导对外可见的基础 URL（用于 OAuth redirect_uri）：
// 仅在信任反代时采用 X-Forwarded-Proto/Host，否则取请求自身（TLS 判定 https）。
func (s *Server) externalBase(r *http.Request) string {
	scheme, host := "http", r.Host
	if s.cfg.WebTrustedProxy {
		if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
			scheme = p
		}
		if h := r.Header.Get("X-Forwarded-Host"); h != "" {
			host = h
		}
	} else if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host
}

// ---- OAuth 处理器 ----

// OAuth 回调结果查询参数（结果页改为 SPA 横幅）：只允许受控枚举值，
// 不透传任何错误细节、账号信息或 GitHub 返回的原始参数。
const (
	oauthResultBound = "bound" // 绑定成功（全部会话已失效）
	oauthResultError = "error" // 授权/交换/绑定或登录失败（通用提示）
)

// redirectOAuthResult 把 OAuth 回调结果 302 到 GitHub 设置页的受控横幅
// （SPA 读取 oauth 查询参数展示文案）。目标为固定路径 + 受控枚举值，
// 不携带 code/state/token 等任何流程值。
func redirectOAuthResult(w http.ResponseWriter, r *http.Request, result string) {
	u := url.URL{
		Path:     "/admin/settings/oauth",
		RawQuery: "oauth=" + result,
	}
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// handleOAuthStart 发起 GitHub OAuth（登录模式）：生成单次 state 并重定向。
// 通道未配置时回到 GitHub 设置页错误横幅（未登录会被 requireAuth 引导到登录壳）。
func (s *Server) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if !s.oauthConfigured() {
		s.log.Warn("GitHub 登录通道未配置，拒绝发起")
		redirectOAuthResult(w, r, oauthResultError)
		return
	}
	state, err := s.states.put(oauthModeLogin, s.now())
	if err != nil {
		s.log.Error("生成 OAuth state 失败", "error", err.Error())
		redirectOAuthResult(w, r, oauthResultError)
		return
	}
	clientID, _, _ := s.oauthCredentials(r.Context())
	q := url.Values{
		"client_id":    {clientID},
		"redirect_uri": {s.externalBase(r) + "/auth/github/callback"},
		"scope":        {"read:user"},
		"state":        {state},
	}
	http.Redirect(w, r, s.oauth.Authorize+"?"+q.Encode(), http.StatusFound)
}

// handleOAuthCallback 处理 GitHub 回调：state 校验（单次使用）→ 换 token →
// 取数字 ID → 按流程模式分流：
//   - login：仅已绑定的 GitHub 账号可建立会话，其余拒绝并审计（响应不回显账号信息）；
//   - bind：把账号写入 settings.github_binding，失效全部会话并审计。
func (s *Server) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if !s.oauthConfigured() {
		redirectOAuthResult(w, r, oauthResultError)
		return
	}
	ip := s.clientIP(r)

	stateVal := r.URL.Query().Get("state")
	mode, ok := s.states.take(stateVal, s.now())
	if stateVal == "" || !ok {
		s.log.Warn("OAuth state 校验失败", "ip", ip)
		redirectOAuthResult(w, r, oauthResultError)
		return
	}
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		// GitHub 侧返回的错误（如用户拒绝授权）：只提示通用失败，不回显细节
		s.log.Warn("GitHub 授权被拒", "ip", ip)
		redirectOAuthResult(w, r, oauthResultError)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		redirectOAuthResult(w, r, oauthResultError)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), oauthHTTPTimeout)
	defer cancel()
	token, err := s.exchangeCode(ctx, code, s.externalBase(r)+"/auth/github/callback")
	if err != nil {
		ae := apperr.From(err)
		s.log.Warn("OAuth token 交换失败", "ip", ip, "code", ae.Code, "error", err.Error())
		redirectOAuthResult(w, r, oauthResultError)
		return
	}
	user, err := s.fetchGitHubUser(ctx, token)
	if err != nil {
		ae := apperr.From(err)
		s.log.Warn("获取 GitHub 账号信息失败", "ip", ip, "code", ae.Code, "error", err.Error())
		redirectOAuthResult(w, r, oauthResultError)
		return
	}

	switch mode {
	case oauthModeBind:
		s.finishBind(w, r, user)
	default:
		s.finishLogin(w, r, user, ip)
	}
}

// finishLogin 完成登录模式回调：与绑定比对，仅绑定账号可建立会话。
// 拒绝时记审计并计入登录失败限流，重定向与日志不回显任何 GitHub 账号信息。
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, user githubUser, ip string) {
	binding, bound, err := loadGitHubBinding(r.Context(), s.st)
	if err != nil {
		s.log.Error("读取 GitHub 绑定失败", "error", err.Error())
		redirectOAuthResult(w, r, oauthResultError)
		return
	}
	if !bound || binding.ID != user.ID {
		// 未绑定或不是绑定的账号：拒绝并审计（target 不含账号标识，防枚举）
		s.recordLoginFailure(r, ip, "github")
		s.log.Warn("GitHub 账号未绑定，拒绝登录", "ip", ip)
		redirectOAuthResult(w, r, oauthResultError)
		return
	}

	s.limiter.Reset(limiterKeyIP(ip))
	if err := s.issueSession(w, r, ip, r.UserAgent()); err != nil {
		ae := apperr.From(err)
		s.log.Error("创建会话失败", "code", ae.Code, "error", err.Error())
		redirectOAuthResult(w, r, oauthResultError)
		return
	}
	if n, err := s.st.CleanExpiredWebSessions(r.Context(), s.now().UnixMilli()); err != nil {
		s.log.Warn("清理过期会话失败", "error", err.Error())
	} else if n > 0 {
		s.log.Info("已清理过期会话", "count", n)
	}
	s.audit(r.Context(), "auth.login", "web", map[string]any{"method": "github", "ip": ip})
	s.log.Info("管理端登录成功", "ip", ip, "method", "github")
	// GitHub 登录成功后与密钥登录一致，落地 SPA 管理端入口
	http.Redirect(w, r, "/admin", http.StatusFound)
}

// finishBind 完成绑定模式回调：写入绑定、失效全部会话并审计。
// 响应是无会话的重定向（所有会话已按安全设计失效，需重新登录），
// 由 GitHub 设置页横幅展示结果。
func (s *Server) finishBind(w http.ResponseWriter, r *http.Request, user githubUser) {
	binding := GitHubBinding{ID: user.ID, Login: user.Login, BoundAt: s.now().UnixMilli()}
	if err := saveGitHubBinding(r.Context(), s.st, binding); err != nil {
		s.log.Error("保存 GitHub 绑定失败", "error", err.Error())
		redirectOAuthResult(w, r, oauthResultError)
		return
	}
	if err := s.st.DeleteAllWebSessions(r.Context()); err != nil {
		s.log.Error("失效全部会话失败", "error", err.Error())
		redirectOAuthResult(w, r, oauthResultError)
		return
	}
	clearSessionCookie(w)
	// 绑定审计只记录动作结果，不记录 GitHub 账号标识。
	s.audit(r.Context(), "oauth.bind", "web", map[string]any{"status": "bound"})
	s.log.Info("GitHub 账号已绑定，全部会话已失效")
	redirectOAuthResult(w, r, oauthResultBound)
}
