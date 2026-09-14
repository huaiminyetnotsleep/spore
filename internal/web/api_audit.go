package web

// GET /api/v1/audit：审计日志查询 API。只读端点（无 CSRF）；分页语义与
// SSR /audit 一致（时间倒序），before/after 保持存储的原始 JSON 值下发，
// 不做字段解释（写入侧已保证不含敏感值），展示形态由前端决定。

import (
	"encoding/json"
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// apiAuditRow 是审计列表行 DTO：时间用 Unix 毫秒，before/after 保持
// 存储中的 JSON 原始值（不缩进、不解释字段）。
type apiAuditRow struct {
	ID        int64           `json:"id"`
	Actor     string          `json:"actor"`
	Action    string          `json:"action"`
	Target    string          `json:"target"` // 可能为空串
	CreatedAt int64           `json:"created_at"`
	Before    json.RawMessage `json:"before"` // 原始 JSON；无快照为 null
	After     json.RawMessage `json:"after"`  // 原始 JSON；无快照为 null
}

// rawJSONValue 把存储的 JSON 文本转为可编码的 RawMessage：空串归一为 nil
// （编码为 null）；写入侧通常存合法 JSON，若出现普通文本则回退为 JSON
// 字符串字面量，保证响应体始终是合法 JSON（沿用 SSR 时代 details 展示的回退语义）。
func rawJSONValue(v string) json.RawMessage {
	if v == "" {
		return nil
	}
	if !json.Valid([]byte(v)) {
		b, err := json.Marshal(v)
		if err != nil {
			return nil
		}
		return b
	}
	return json.RawMessage(v)
}

// handleAPIAuditList 返回审计分页数据。
func (s *Server) handleAPIAuditList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.audit.list"
	ctx := r.Context()
	page, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	tr, err := parseTimeRange(r, s.tz(ctx))
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	filter := store.AuditFilter{
		Since: tr.Since, Until: tr.Until,
		HasSince: tr.SinceDay != "", HasUntil: tr.UntilDay != "",
	}

	total, err := s.st.CountAuditFiltered(ctx, filter)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	entries, err := s.st.ListAuditFiltered(ctx, filter, page.PageSize, page.Offset)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	items := make([]apiAuditRow, 0, len(entries))
	for _, e := range entries {
		items = append(items, apiAuditRow{
			ID: e.ID, Actor: e.Actor, Action: e.Action, Target: e.Target, CreatedAt: e.At,
			Before: rawJSONValue(e.BeforeJSON), After: rawJSONValue(e.AfterJSON),
		})
	}
	writeAPIList(w, newAPIListEnvelope(items, page, total))
}
