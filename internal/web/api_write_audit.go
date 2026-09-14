package web

import "net/http"

const maxBatchAuditIDs = 200

// handleAPIAuditDelete 删除明确勾选的审计行，并在同一事务中留下清理审计。
func (s *Server) handleAPIAuditDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.audit.delete"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	var in apiIDsRequest
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > maxBatchAuditIDs {
		s.apiBadRequest(w, r, op, "请选择 1 到 200 条审计记录。")
		return
	}
	for _, id := range in.IDs {
		if id <= 0 {
			s.apiBadRequest(w, r, op, "审计 ID 必须为正整数。")
			return
		}
	}
	deleted, err := s.access.DeleteAuditEntries(r.Context(), "admin", in.IDs)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, apiDeletedResult{apiWriteOK{OK: true}, deleted})
}

type apiAuditClearRequest struct {
	Confirm string `json:"confirm"`
}

// handleAPIAuditClear 清除全部历史审计；成功后仅保留本次 audit.clear。
func (s *Server) handleAPIAuditClear(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.audit.clear"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	var in apiAuditClearRequest
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.Confirm != "clear_audit" {
		s.apiBadRequest(w, r, op, "请确认清除全部审计日志。")
		return
	}
	deleted, err := s.access.ClearAudit(r.Context(), "admin")
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, apiDeletedResult{apiWriteOK{OK: true}, deleted})
}
