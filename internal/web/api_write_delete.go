package web

// POST /api/v1/requests/{id}/delete、POST /api/v1/channels/{key}/delete：
// 记录清理写端点（业务核心在 internal/access/delete.go，事务内安全预检）。
// 单条删除仅接受终态记录；频道删除即删除该频道的全部请求记录
// （频道是 requests 的纯聚合，无独立频道表）。

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// handleAPIRequestDelete 处理删除单条请求记录（仅终态可删，拒绝分支经
// deleteErrText 输出与业务语义一致的受控文案）。
func (s *Server) handleAPIRequestDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.delete"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	if err := s.access.DeleteRequest(r.Context(), "admin", id); err != nil {
		s.writeAPIDeleteErr(w, r, op, err, "该记录尚未结束（排队或处理中），请等待其完成后再删除。")
		return
	}
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}

type apiDeleteResult struct {
	ID        int64  `json:"id"`
	Result    string `json:"result"`
	ErrorCode string `json:"error_code,omitempty"`
}

// handleAPIRequestsDelete 批量删除明确勾选的终态请求，逐条返回受控结果。
func (s *Server) handleAPIRequestsDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.delete_many"
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
	items := s.access.DeleteMany(r.Context(), "admin", in.IDs)
	results := make([]apiDeleteResult, 0, len(items))
	for _, item := range items {
		results = append(results, apiDeleteResult{
			ID: item.RequestID, Result: item.Result, ErrorCode: item.ErrorCode,
		})
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Results []apiDeleteResult `json:"results"`
	}{apiWriteOK{OK: true}, results})
}

// handleAPIChannelDelete 处理删除频道的全部请求记录，响应携带实际删除行数。
func (s *Server) handleAPIChannelDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.channels.delete"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	key, err := url.PathUnescape(r.PathValue("key"))
	if err != nil || key == "" {
		s.apiBadRequest(w, r, op, "频道标识无效。")
		return
	}
	deleted, err := s.access.DeleteChannelRequests(r.Context(), "admin", key)
	if err != nil {
		s.writeAPIDeleteErr(w, r, op, err, "该频道仍有排队或处理中的请求，请等待其完成后再删除。")
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Deleted int `json:"deleted"`
	}{apiWriteOK{OK: true}, deleted})
}

// writeAPIDeleteErr 把删除操作的拒绝错误转为 JSON 错误：查无此行走统一 404
// 链路，前置校验冲突（constraint 文案）按各自端点语义输出，其余复用公共映射。
func (s *Server) writeAPIDeleteErr(w http.ResponseWriter, r *http.Request, op string, err error, constraintText string) {
	if errors.Is(err, store.ErrNotFound) {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	if apperr.From(err).Code == apperr.CodeStoreConstraint {
		s.log.Warn("记录删除被拒绝", "op", op, "path", r.URL.Path, "error", err.Error())
		writeAPIError(w, http.StatusConflict, string(apperr.CodeStoreConstraint), constraintText)
		return
	}
	s.writeAPIAppErr(w, r, op, err)
}
