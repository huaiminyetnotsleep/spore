package web

import (
	"errors"
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// ---- 消息记录 ----

// handleAPIRequestRetry 处理受控重试（access.Retry：attempt 上限、用户启用、
// 队列满、不扣额度；拒绝分支经 retryErrText 输出与 SSR 相同的受控文案，
// 上限数字取当前 syscfg 配置，保证文案与实际校验口径一致）。
func (s *Server) handleAPIRequestRetry(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.retry"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	if err := s.access.Retry(r.Context(), "admin", id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		ae := apperr.From(err)
		s.log.Warn("重试请求失败", "request_id", id, "code", ae.Code, "error", err.Error())
		status, code := apiAppErrStatus(err)
		writeAPIError(w, status, code, retryErrText(ae, syscfg.LoadMaxRequestAttempts(r.Context(), s.st)))
		return
	}
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}

// handleAPIRequestResetAttempts 处理尝试计数重置（access.ResetAttempts：
// 仅 failed 可重置，attempt 清回 1，不入队；拒绝分支经 resetErrText 输出
// 受控文案）。
func (s *Server) handleAPIRequestResetAttempts(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.reset_attempts"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	if err := s.access.ResetAttempts(r.Context(), "admin", id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		ae := apperr.From(err)
		s.log.Warn("重置请求尝试计数失败", "request_id", id, "code", ae.Code, "error", err.Error())
		status, code := apiAppErrStatus(err)
		writeAPIError(w, status, code, resetErrText(ae))
		return
	}
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}

// ---- 事件中心 ----

// handleAPIEventResolve 把事件标记为已解决（核心经 resolveEventCore：
// 优先经事件中心，与系统自动恢复共用 resolve+审计语义）。
func (s *Server) handleAPIEventResolve(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.events.resolve"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	if err := s.resolveEventCore(r.Context(), id); err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}
