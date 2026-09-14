package web

// 高风险页面迁移：GitHub OAuth 配置与
// 绑定的 SPA API（/api/v1，当前为唯一配置入口）。业务规则核心（updateOAuthSettings、
// oauthStateStore 与既有 /auth/github 回调链路），本文件只负责 JSON 契约。
//
// 敏感边界：Client Secret 永不出现在任何响应、日志或审计（审计只记
// enabled / client_id_changed / secret_changed 布尔状态）；bind 只返回一次性
// 授权跳转 URL，state 仍由服务端单次签发与校验，回调沿用既有
// /auth/github/callback 路由（解绑后全部会话失效，前端需引导重新登录）。

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// apiOAuthSettingsView 是 GET /api/v1/oauth/settings 的响应 DTO
// （Secret 任何情况下不出现）。
type apiOAuthSettingsView struct {
	// GitHubConfigured 是当前生效的通道可用状态（数据库配置优先于环境变量）。
	GitHubConfigured bool `json:"github_configured"`
	// Configured 表示 Client ID 与 Secret 已完整配置（数据库或环境变量）。
	Configured bool   `json:"configured"`
	Enabled    bool   `json:"enabled"`
	ClientID   string `json:"client_id"`
	SecretSet  bool   `json:"secret_set"`
	// 绑定账号信息；未绑定时 Bound 为 false 且其余字段为零值。
	Bound       bool   `json:"bound"`
	GitHubLogin string `json:"github_login,omitempty"`
	GitHubID    int64  `json:"github_id,omitempty"`
	BoundAt     int64  `json:"bound_at,omitempty"` // Unix 毫秒；0 表示未绑定
	// ReadError 是配置/绑定读取失败的受控提示（不含底层错误细节）；正常为空。
	ReadError string `json:"read_error,omitempty"`
}

// buildAPIOAuthSettingsView 组装 OAuth 配置读取响应（数据源
// loadOAuthSettingsData；错误只映射为受控提示）。
func (s *Server) buildAPIOAuthSettingsView(ctx context.Context) apiOAuthSettingsView {
	data := s.loadOAuthSettingsData(ctx)
	view := apiOAuthSettingsView{
		GitHubConfigured: s.oauthConfigured(),
		Configured:       data.Configured,
		Enabled:          data.Enabled,
		ClientID:         data.ClientID,
		SecretSet:        data.SecretSet,
		Bound:            data.Bound,
	}
	if data.Bound {
		view.GitHubLogin = data.Binding.Login
		view.GitHubID = data.Binding.ID
		view.BoundAt = data.Binding.BoundAt
	}
	switch {
	case data.BindingErr != nil:
		view.ReadError = "读取 GitHub 绑定失败。"
	case data.ConfigErr != nil:
		view.ReadError = "读取 GitHub 登录配置失败。"
	}
	return view
}

// handleAPIOAuthSettingsGet 返回 GitHub 登录配置与绑定状态。
func (s *Server) handleAPIOAuthSettingsGet(w http.ResponseWriter, r *http.Request, _ session) {
	writeAPISingle(w, s.buildAPIOAuthSettingsView(r.Context()))
}

// handleAPIOAuthSettingsPost 处理 save / enable / disable / clear（JSON 载荷，
// 经 updateOAuthSettings 共用核心：校验、加密、写库与审计完全同源）。
// 参数/配置拒绝回 400 受控文案；存储失败走统一错误链路。
func (s *Server) handleAPIOAuthSettingsPost(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.oauth.settings.update"
	var in struct {
		Action       string `json:"action"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Enabled      bool   `json:"enabled"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	switch in.Action {
	case "save", "enable", "disable", "clear":
	default:
		s.apiBadRequest(w, r, op, "未知操作。")
		return
	}
	err := s.updateOAuthSettings(r.Context(), in.Action, oauthSettingsUpdate{
		ClientID:     in.ClientID,
		ClientSecret: in.ClientSecret,
		Enabled:      in.Enabled,
	})
	if err != nil {
		var paramErr *oauthSettingsParamError
		if errors.As(err, &paramErr) {
			s.log.Warn("GitHub OAuth 配置变更被拒绝", "op", op, "action", in.Action)
			writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, paramErr.msg)
			return
		}
		// 存储类失败及其他服务端错误：走统一 apperr 错误映射
		// （500/503），不透出底层细节。
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Settings apiOAuthSettingsView `json:"settings"`
	}{apiWriteOK{OK: true}, s.buildAPIOAuthSettingsView(r.Context())})
}

// handleAPIOAuthBind 发起绑定模式 OAuth：签发单次 state 并返回受控跳转 URL，
// 由前端 window.location 跳转；回调沿用既有 /auth/github/callback（bind 模式）。
// 不返回任何 token 或 Secret。
func (s *Server) handleAPIOAuthBind(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.oauth.bind"
	if !s.oauthConfigured() {
		s.log.Warn("GitHub 登录通道未配置，拒绝发起绑定", "op", op)
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest,
			"未配置 GITHUB_CLIENT_ID / GITHUB_CLIENT_SECRET。")
		return
	}
	state, err := s.states.put(oauthModeBind, s.now())
	if err != nil {
		s.log.Error("生成 OAuth state 失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal),
			apperr.UserText(apperr.CodeInternal))
		return
	}
	clientID, _, _ := s.oauthCredentials(r.Context())
	q := url.Values{
		"client_id":    {clientID},
		"redirect_uri": {s.externalBase(r) + "/auth/github/callback"},
		"scope":        {"read:user"},
		"state":        {state},
	}
	// no-store 避免一次性 state 被缓存
	writeAPISingle(w, struct {
		apiWriteOK
		AuthorizeURL string `json:"authorize_url"`
	}{apiWriteOK{OK: true}, s.oauth.Authorize + "?" + q.Encode()})
}

// handleAPIOAuthUnbind 解除 GitHub 绑定：
// 删除绑定、失效全部会话并审计；成功后前端需引导重新登录。
func (s *Server) handleAPIOAuthUnbind(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.oauth.unbind"
	ctx := r.Context()
	if _, bound, err := loadGitHubBinding(ctx, s.st); err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	} else if !bound {
		s.apiBadRequest(w, r, op, "当前未绑定 GitHub 账号。")
		return
	}
	if err := s.st.DeleteSetting(ctx, settingKeyGitHubBinding); err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	if err := s.st.DeleteAllWebSessions(ctx); err != nil {
		s.log.Error("失效全部会话失败", "op", op, "error", err.Error())
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	clearSessionCookie(w)
	s.audit(ctx, "oauth.unbind", "web", map[string]any{"ip": s.clientIP(r)})
	s.log.Info("GitHub 账号已解绑，全部会话已失效")
	writeAPISingle(w, struct {
		apiWriteOK
		Relogin bool `json:"relogin"` // 全部会话已失效，前端应跳转 SPA 登录页（/admin/login）
	}{apiWriteOK{OK: true}, true})
}
