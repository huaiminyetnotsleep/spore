package web

import (
	"net/http"
	"sort"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// ---- 申请审批 ----

// apiApplicationRow 是待审批申请行 DTO（与 SSR 申请列表字段一致）。
// SourceBot 是申请来源 bot（首次 /start 的受理 bot）；0 = 存量行。
type apiApplicationRow struct {
	ID                int64  `json:"id"`
	Username          string `json:"username"`
	DisplayName       string `json:"display_name"`
	AppliedAt         int64  `json:"applied_at"` // Unix 毫秒（users.created_at）
	SourceBotID       int64  `json:"source_bot_id"`
	SourceBotUsername string `json:"source_bot_username,omitempty"`
}

// handleAPIApplicationsList 返回待审批申请（pending 用户，按 ID 升序）。
// 容量目标 ≤100，与 SSR 申请页同一内存过滤口径，不分页。
func (s *Server) handleAPIApplicationsList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.applications.list"
	ctx := r.Context()
	users, err := s.st.ListUsers(ctx)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	items := make([]apiApplicationRow, 0)
	for _, u := range users {
		if u.Status != store.UserPending {
			continue
		}
		items = append(items, apiApplicationRow{
			ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, AppliedAt: u.CreatedAt,
			SourceBotID: u.SourceBotID, SourceBotUsername: u.SourceBotUsername,
		})
	}
	// 与 SSR 相同的稳定顺序：按申请先后（ID 升序）
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	writeAPISingle(w, struct {
		Items []apiApplicationRow `json:"items"`
	}{Items: items})
}

// handleAPIApplicationReview 处理批准/拒绝（经 access.Approve/Reject：状态
// 流转 + 审计 + Bot 通知）。通知失败不回滚审批：notified=false 让前端提示
// 管理员手动跟进（与 SSR ?notify=failed 同语义）。
func (s *Server) handleAPIApplicationReview(approve bool) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess session) {
		const op = "api.applications.review"
		if !s.apiRequireAccess(w, r, op) {
			return
		}
		id, ok := s.apiPathID(w, r, op)
		if !ok {
			return
		}
		var notified bool
		var err error
		if approve {
			notified, err = s.access.Approve(r.Context(), "admin", id)
		} else {
			notified, err = s.access.Reject(r.Context(), "admin", id)
		}
		if err != nil {
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		status := store.UserDisabled
		if approve {
			status = store.UserEnabled
		}
		writeAPIJSON(w, http.StatusOK, struct {
			apiWriteOK
			Notified bool   `json:"notified"`
			Status   string `json:"status"`
		}{apiWriteOK{OK: true}, notified, status})
	}
}
