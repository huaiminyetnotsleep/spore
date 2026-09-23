package web

// api_error_logs.go — 错误日志中心的管理端查询与清理：按来源/错误码/
// 级别/请求/时间范围筛选分页，按 ID 批量删除与按时间段清理（可叠加
// 来源/错误码条件）。删除只影响留痕记录，自动保留清理在 errlog.Run。

import (
	"net/http"
	"strconv"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// handleAPIErrorLogsList 返回错误日志页（id 倒序，最新在前）。查询参数：
// source（白名单）、code、severity（白名单）、request_id（非零整数）、
// created_after / created_before（Unix 毫秒，可选时间范围）、page/page_size
// （parseAPIPageParams 语义，page_size 上限 100）。
func (s *Server) handleAPIErrorLogsList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.error_logs.list"
	page, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	q := r.URL.Query()
	var query store.ErrorLogsQuery
	if v := q.Get("source"); v != "" {
		if !store.ValidErrorSource(v) {
			s.apiBadRequest(w, r, op, "source 仅支持 request/botapi/cloud/backup/watch/mtproto。")
			return
		}
		query.Source = v
	}
	if v := q.Get("severity"); v != "" {
		if !store.ValidErrorSeverity(v) {
			s.apiBadRequest(w, r, op, "severity 仅支持 error 或 warn。")
			return
		}
		query.Severity = v
	}
	query.Code = q.Get("code")
	if v := q.Get("request_id"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n == 0 {
			s.apiBadRequest(w, r, op, "request_id 必须为非零整数。")
			return
		}
		query.RequestID = n
	}
	for param, dst := range map[string]*int64{
		"created_after": &query.CreatedAfter, "created_before": &query.CreatedBefore,
	} {
		if v := q.Get(param); v != "" {
			n, perr := strconv.ParseInt(v, 10, 64)
			if perr != nil || n < 0 {
				s.apiBadRequest(w, r, op, param+" 必须为非负 Unix 毫秒时间戳。")
				return
			}
			*dst = n
		}
	}
	rows, total, err := s.st.ListErrorLogs(r.Context(), query)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIList(w, newAPIListEnvelope(toAnySlice(rows), page, total))
}

// handleAPIErrorLogsDelete 删除错误日志，两种模式二选一：
//   - {"ids":[...]}：按 ID 批量删（1–100 个正整数）；
//   - {"after":ms,"before":ms,"source":"","code":""}：按时间段删（至少一个
//     时间界；before 为开区间上界；可叠加来源/错误码条件）。
//
// 返回实际删除条数；动作写审计（记录模式与范围，不复制被删内容）。
func (s *Server) handleAPIErrorLogsDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.error_logs.delete"
	var in struct {
		IDs    []int64 `json:"ids"`
		After  int64   `json:"after"`
		Before int64   `json:"before"`
		Source string  `json:"source"`
		Code   string  `json:"code"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if len(in.IDs) > 0 {
		if len(in.IDs) > 100 {
			s.apiBadRequest(w, r, op, "请提供 1–100 个日志 ID。")
			return
		}
		for _, id := range in.IDs {
			if id <= 0 {
				s.apiBadRequest(w, r, op, "日志 ID 必须为正整数。")
				return
			}
		}
		if in.After != 0 || in.Before != 0 || in.Source != "" || in.Code != "" {
			s.apiBadRequest(w, r, op, "按 ID 删除与按时间段删除不能同时提供。")
			return
		}
		deleted, err := s.st.DeleteErrorLogs(r.Context(), in.IDs)
		if err != nil {
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		s.audit(r.Context(), "error_logs.delete", "error_logs",
			map[string]any{"mode": "ids", "requested": len(in.IDs), "deleted": deleted})
		writeAPISingle(w, struct {
			apiWriteOK
			Deleted int64 `json:"deleted"`
		}{apiWriteOK{OK: true}, deleted})
		return
	}
	// 按时间段清理：至少一个时间界（防无条件全表清空）；来源白名单校验
	if in.After == 0 && in.Before == 0 {
		s.apiBadRequest(w, r, op, "按时间段删除必须至少提供 after 或 before 之一。")
		return
	}
	if in.After < 0 || in.Before < 0 {
		s.apiBadRequest(w, r, op, "after/before 必须为非负 Unix 毫秒时间戳。")
		return
	}
	if in.Source != "" && !store.ValidErrorSource(in.Source) {
		s.apiBadRequest(w, r, op, "source 仅支持 request/botapi/cloud/backup/watch/mtproto。")
		return
	}
	if in.After > 0 && in.Before > 0 && in.After >= in.Before {
		s.apiBadRequest(w, r, op, "after 必须早于 before。")
		return
	}
	deleted, err := s.st.DeleteErrorLogsRange(r.Context(), store.ErrorLogsRangeQuery{
		After: in.After, Before: in.Before, Source: in.Source, Code: in.Code,
	})
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.audit(r.Context(), "error_logs.delete", "error_logs",
		map[string]any{"mode": "range", "after": in.After, "before": in.Before,
			"source": in.Source, "code": in.Code, "deleted": deleted})
	writeAPISingle(w, struct {
		apiWriteOK
		Deleted int64 `json:"deleted"`
	}{apiWriteOK{OK: true}, deleted})
}
