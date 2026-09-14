package web

import (
	"errors"
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// ---- 消息记录 ----

// handleAPIRequestRetry 处理受控重试（access.Retry：attempt 上限、用户启用、
// 队列满、不扣额度；拒绝分支经 retryErrText 输出与 SSR 相同的受控文案）。
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
		writeAPIError(w, status, code, retryErrText(ae))
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
