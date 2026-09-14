package web

import (
	"errors"
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

const cancelConflictText = "该请求已结束或已取消，请刷新后重试。"

// handleAPIRequestCancel 取消单条 queued/processing 请求；认证、CSRF 和 JSON
// 响应由 mountAPIWrite 统一提供。
func (s *Server) handleAPIRequestCancel(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.cancel"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	if err := s.access.Cancel(r.Context(), "admin", id); err != nil {
		s.writeAPICancelErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}

type apiCancelResult struct {
	ID        int64  `json:"id"`
	Result    string `json:"result"`
	ErrorCode string `json:"error_code,omitempty"`
}

// handleAPIRequestsCancel 批量取消调用方明确勾选的当前页请求，不扩展筛选范围。
// 单条结果独立记录，冲突/不存在不回滚已成功取消的其他请求。
func (s *Server) handleAPIRequestsCancel(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.cancel_many"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	var in apiIDsRequest
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > maxBatchRequestIDs {
		s.apiBadRequest(w, r, op, "请选择 1 到 50 条请求记录。")
		return
	}
	for _, id := range in.IDs {
		if id <= 0 {
			s.apiBadRequest(w, r, op, "请求 ID 必须为正整数。")
			return
		}
	}
	if !s.access.CanCancel() {
		s.writeAPIAppErr(w, r, op, apperr.New(apperr.CodeStoreUnavailable, "请求取消控制器未接入"))
		return
	}
	items := s.access.CancelMany(r.Context(), "admin", in.IDs)
	results := make([]apiCancelResult, 0, len(items))
	for _, item := range items {
		results = append(results, apiCancelResult{
			ID: item.RequestID, Result: item.Result, ErrorCode: item.ErrorCode,
		})
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Results []apiCancelResult `json:"results"`
	}{apiWriteOK{OK: true}, results})
}

const maxBatchRequestIDs = 50

func (s *Server) writeAPICancelErr(w http.ResponseWriter, r *http.Request, op string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	ae := apperr.From(err)
	if ae.Code == apperr.CodeStoreConstraint {
		s.log.Warn("取消请求被拒绝", "op", op, "path", r.URL.Path, "code", ae.Code)
		writeAPIError(w, http.StatusConflict, string(ae.Code), cancelConflictText)
		return
	}
	s.writeAPIAppErr(w, r, op, err)
}
