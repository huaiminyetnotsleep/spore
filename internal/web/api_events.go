package web

// GET /api/v1/events：事件中心列表查询 API。SSR /events 页面无筛选字段，
// API 额外提供 status（open|resolved）可选筛选；事件量小（按 key 去重合并），
// 沿用 ListEvents 全量读取后内存筛选与切片分页。

import (
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// apiEventRow 是事件列表行 DTO（raw 状态/级别交由前端做中文标签与配色）。
type apiEventRow struct {
	ID             int64  `json:"id"`
	Key            string `json:"key"`
	Severity       string `json:"severity"`
	Message        string `json:"message"`
	Count          int    `json:"count"`
	FirstAt        int64  `json:"first_at"`
	LastAt         int64  `json:"last_at"`
	LastNotifiedAt int64  `json:"last_notified_at"` // 0 表示从未通知
	Status         string `json:"status"`           // open | resolved
}

// handleAPIEventsList 返回事件分页数据。
func (s *Server) handleAPIEventsList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.events.list"
	ctx := r.Context()
	page, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	statusFilter := r.URL.Query().Get("status")

	events, err := s.st.ListEvents(ctx)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	matched := make([]store.Event, 0, len(events))
	for _, e := range events {
		if statusFilter != "" && e.Status != statusFilter {
			continue
		}
		matched = append(matched, e)
	}

	start, end := page.Offset, page.Offset+page.PageSize
	if start > len(matched) {
		start = len(matched)
	}
	if end > len(matched) {
		end = len(matched)
	}
	items := make([]apiEventRow, 0, end-start)
	for _, e := range matched[start:end] {
		items = append(items, apiEventRow{
			ID: e.ID, Key: e.Key, Severity: e.Severity, Message: e.Message, Count: e.Count,
			FirstAt: e.FirstAt, LastAt: e.LastAt, LastNotifiedAt: e.LastNotifiedAt, Status: e.Status,
		})
	}
	writeAPIList(w, newAPIListEnvelope(items, page, len(matched)))
}
