package web

// 高风险页面迁移：受控重启的 SPA API。
// 核心（restartReason / scheduleRestart）：要求固定二次确认、
// 审计只记固定原因、生产实现只发送 SIGTERM——不执行 Shell、不接受任意参数。

import (
	"net/http"
)

// handleAPIRestart 处理受控重启（JSON 载荷 confirm=restart）。
// RestartFunc 未注入时返回受控不可用状态（含脱敏审计），
// 成功返回 202 摘要；触发失败由后台 goroutine 写 admin.restart.failed 审计。
func (s *Server) handleAPIRestart(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.restart"
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.Confirm != restartConfirmValue {
		s.apiBadRequest(w, r, op, "请确认重启操作")
		return
	}
	if s.restartFunc == nil {
		s.audit(r.Context(), "admin.restart.unavailable", "process", map[string]any{
			"reason": "restart_func_unconfigured"})
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable, restartUnavailableText)
		return
	}
	reason := s.restartReason()
	if !s.scheduleRestart(reason) {
		writeAPIError(w, http.StatusConflict, apiCodeConflict, "重启已在进行中，请勿重复提交。")
		return
	}
	s.audit(r.Context(), "admin.restart", "process", map[string]any{"reason": reason})
	// 返回 202：重启是异步受控动作，响应只代表"已触发"
	writeAPIJSON(w, http.StatusAccepted, struct {
		apiWriteOK
		Message    string `json:"message"`
		Restarting bool   `json:"restarting"`
	}{
		apiWriteOK{OK: true},
		"应用正在优雅退出；若由 Docker Compose 等监管机制运行，进程将自动恢复。直接本地运行时请由运行环境重新启动。",
		true,
	})
}
