package web

import (
	"net/http"
)

// GET /api/v1/session 会话引导：向已登录 SPA 交付登录态、当前用户和会话级
// CSRF token。token 复用 web_sessions.csrf_token（与会话同源），
// 不新增第二套安全状态；响应不含 Session Secret、OAuth Secret、访问密钥或
// 任何 MTProto 敏感值，token 只交付给已通过会话认证的调用方。

// apiSessionUser 是 bootstrap 响应中的当前用户（本系统为单一管理员）。
type apiSessionUser struct {
	Name        string `json:"name"`
	GitHubLogin string `json:"github_login,omitempty"` // 未绑定 GitHub 时省略
}

// apiSessionView 是 GET /api/v1/session 的响应 DTO（独立于 SSR view struct）。
type apiSessionView struct {
	Authenticated bool           `json:"authenticated"`
	User          apiSessionUser `json:"user"`
	CSRFToken     string         `json:"csrf_token"`
	ExpiresAt     int64          `json:"expires_at"` // Unix 毫秒：滑动续期后的会话过期时间
}

// handleAPISession 返回会话引导信息；仅在有效会话下到达（apiAuth 已完成
// 认证与滑动续期）。GitHub 绑定读取失败不阻断 bootstrap，仅省略该字段。
func (s *Server) handleAPISession(w http.ResponseWriter, r *http.Request, sess session) {
	// 响应携带会话级 CSRF token，禁止任何缓存（含共享代理）留存凭据
	w.Header().Set("Cache-Control", "no-store")
	user := apiSessionUser{Name: "admin"}
	if b, ok, err := loadGitHubBinding(r.Context(), s.st); err != nil {
		s.log.Warn("读取 GitHub 绑定失败", "error", err.Error())
	} else if ok {
		user.GitHubLogin = b.Login
	}
	writeAPIJSON(w, http.StatusOK, apiSessionView{
		Authenticated: true,
		User:          user,
		CSRFToken:     sess.csrf,
		// apiAuth 刚完成滑动续期：报告续期后的过期窗口（与 DB 写入值一致）
		ExpiresAt: s.now().Add(sessionTTL).UnixMilli(),
	})
}
