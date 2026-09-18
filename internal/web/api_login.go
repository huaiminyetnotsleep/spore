package web

// 登录 API：SSR 登录页删除后，访问密钥登录改经 /api/v1 JSON 完成。
// 登录级 CSRF 的 Cookie 与存储语义与旧登录表单一致（spore_login_csrf 双提交
// Cookie），仅传输通道从 HTML 隐藏域改为 X-CSRF-Token 请求头：
//   - GET /api/v1/login/csrf：公开；签发/复用登录 CSRF Cookie 并返回 token；
//   - POST /api/v1/login：公开；校验登录 CSRF 后按访问密钥签发会话。
//
// 两个端点都无会话上下文：响应不包含任何业务数据，密钥与 token 不落日志，
// 失败提示只使用 apperr / apiUserMessage 受控中文文案。

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// apiLoginCSRFView 是 GET /api/v1/login/csrf 的响应 DTO。
// github_enabled 让 SPA 登录页只在通道已配置时展示 GitHub 入口
// （沿用旧 SSR 登录页的显隐语义，避免出现必然失败的链接）。
type apiLoginCSRFView struct {
	CSRFToken     string `json:"csrf_token"`
	GitHubEnabled bool   `json:"github_enabled"`
}

// handleAPILoginCSRF 下发登录级双提交 CSRF token：缺失时生成并种入 Cookie，
// 已有则复用（与旧登录页 ensureLoginCSRF 语义一致）。token 本身不含敏感信息，
// 但响应仍禁止缓存，避免中间环节留存可关联登录行为的值。
func (s *Server) handleAPILoginCSRF(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	csrf := s.ensureLoginCSRF(w, r)
	if csrf == "" {
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal),
			apperr.UserText(apperr.CodeInternal))
		return
	}
	writeAPIJSON(w, http.StatusOK, apiLoginCSRFView{
		CSRFToken:     csrf,
		GitHubEnabled: s.oauthConfigured(),
	})
}

// apiLoginRequest 是 POST /api/v1/login 的请求体。
type apiLoginRequest struct {
	AccessKey string `json:"access_key"`
}

// handleAPILogin 处理访问密钥登录：
// 限流 → 登录 CSRF（头 vs Cookie 常数时间比对）→ JSON body → 密钥哈希
// 常数时间比对 → 签发会话（复用旧 SSR 登录的 issueSession 与审计路径）。
// 失败一律 JSON：429 限流锁定、403 CSRF、400 请求体非法、401 密钥错误、
// 5xx 存储/内部错误；提示不带任何内部细节。
func (s *Server) handleAPILogin(w http.ResponseWriter, r *http.Request) {
	const op = "login"
	ip := s.clientIP(r)

	// 先看限流（IP 与全局任一锁定即拒绝），被拒时不消耗比对与审计
	for _, key := range []string{limiterKeyIP(ip), limiterKeyGlobal} {
		if ok, retryAfter := s.limiter.Allow(key, s.now()); !ok {
			s.log.Warn("登录被限流拒绝", "ip", ip, "retry_after", retryAfter.String())
			writeAPIError(w, http.StatusTooManyRequests, string(apperr.CodeWebLoginLocked),
				apperr.UserText(apperr.CodeWebLoginLocked))
			return
		}
	}

	if !verifyLoginCSRF(r) {
		// CSRF 失败不计入登录失败限流（不是密钥猜测），也不写登录失败审计
		s.log.Warn("登录请求 CSRF 校验失败", "ip", ip)
		writeAPIError(w, http.StatusForbidden, string(apperr.CodeWebCSRFInvalid),
			apiUserMessage(apiCodeCSRFFailed))
		return
	}

	var req apiLoginRequest
	if !s.apiReadJSON(w, r, op, &req) {
		return
	}
	if strings.TrimSpace(req.AccessKey) == "" {
		s.apiBadRequest(w, r, op, "请求缺少访问密钥。")
		return
	}

	stored, ok, err := loadAccessKeyHash(r.Context(), s.st)
	if err != nil {
		s.log.Error("读取访问密钥哈希失败", "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeStoreUnavailable),
			apperr.UserText(apperr.CodeStoreUnavailable))
		return
	}
	submitted := hashValue(strings.TrimSpace(req.AccessKey))
	if !ok || subtle.ConstantTimeCompare([]byte(submitted), []byte(stored)) != 1 {
		s.recordLoginFailure(r, ip, "key")
		writeAPIError(w, http.StatusUnauthorized, string(apperr.CodeWebAuthFailed),
			apperr.UserText(apperr.CodeWebAuthFailed))
		return
	}

	// 登录成功：清 IP 维度计数（全局维度仅自然衰减）、签发会话、审计
	s.limiter.Reset(limiterKeyIP(ip))
	if err := s.issueSession(w, r, ip, r.UserAgent()); err != nil {
		ae := apperr.From(err)
		s.log.Error("创建会话失败", "code", ae.Code, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(ae.Code),
			apperr.UserText(ae.Code))
		return
	}
	// 顺手清理已过期会话（惰性，无定时器）
	if n, err := s.st.CleanExpiredWebSessions(r.Context(), s.now().UnixMilli()); err != nil {
		s.log.Warn("清理过期会话失败", "error", err.Error())
	} else if n > 0 {
		s.log.Info("已清理过期会话", "count", n)
	}
	s.audit(r.Context(), "auth.login", "web", map[string]any{"method": "key", "ip": ip})
	s.notifyAdminLogin(r, "访问密钥", ip)
	s.log.Info("管理端登录成功", "ip", ip, "method", "key")
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}
