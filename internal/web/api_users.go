package web

// GET /api/v1/users、GET /api/v1/users/{id}：用户列表与详情查询 API。
// 筛选字段与 SSR /users 页面一一对应（q 搜索、status 状态）；容量目标 ≤100，
// 沿用 SSR 的内存过滤与切片分页（复用 ListUsers DAO，不新增 SQL）。
// DTO 独立于 SSR view struct：时间一律 Unix 毫秒，0 表示尚未发生。

import (
	"net/http"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// apiUserRow 是用户列表行 DTO（raw 状态码交由前端做中文标签）。
type apiUserRow struct {
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	IsOwner     bool   `json:"is_owner"`
	Note        string `json:"note"`
	LastUsedAt  int64  `json:"last_used_at"`
	// 累计请求数（不限时间范围）；stats 聚合无该用户行时 HasTotalRequests=false
	TotalRequests    int  `json:"total_requests"`
	HasTotalRequests bool `json:"has_total_requests"`
	// 来源 bot（首次 /start 的受理 bot）；0 = 存量行/Web 手动添加，前端显示"—"
	SourceBotID       int64  `json:"source_bot_id"`
	SourceBotUsername string `json:"source_bot_username,omitempty"`
	// CloudDownload 是用户级云盘下载权限 raw 三态（0=跟随角色默认 1=允许
	// 2=拒绝）；EffectiveCloudDownload 是生效 bool（raw 0 时按角色回退
	// owner true / 普通用户 false），供前端直接回显。
	CloudDownload          int  `json:"cloud_download"`
	EffectiveCloudDownload bool `json:"effective_cloud_download"`
}

// apiUserDetail 是用户详情 DTO：覆盖 SSR 用户详情页展示的业务字段
// （资料、限额、今日用量、最近限流与累计请求）。写操作能力标记与
// last_denied 中文文案由前端/后端各自职责渲染：拒绝码原文保留在前端展示，
// 文案转换复用 deniedText 单一来源。
type apiUserDetail struct {
	ID                int64  `json:"id"`
	Username          string `json:"username"`
	DisplayName       string `json:"display_name"`
	Status            string `json:"status"`
	IsOwner           bool   `json:"is_owner"`
	Note              string `json:"note"`
	CreatedAt         int64  `json:"created_at"`
	FirstUsedAt       int64  `json:"first_used_at"`
	LastUsedAt        int64  `json:"last_used_at"`
	ArchivedAt        int64  `json:"archived_at"`
	LastDeniedAt      int64  `json:"last_denied_at"`
	LastDeniedReason  string `json:"last_denied_reason"`
	LastDeniedText    string `json:"last_denied_text"` // 受控中文（deniedText），apperr 文案只在 Go 侧
	UsedToday         int    `json:"used_today"`
	DailyLimit        int    `json:"daily_limit"`
	RemainingToday    int    `json:"remaining_today"`
	SubmitIntervalSec int    `json:"submit_interval_sec"`
	ConcurrentLimit   int    `json:"concurrent_limit"`
	// BindLimit 是用户级频道绑定数量上限 raw 值（0 = 跟随角色默认）；
	// 编辑语义（0 = 恢复跟随默认、缺省保持不变）由前端处理。
	BindLimit int `json:"bind_limit"`
	// EffectiveBindLimit 是生效值（User.EffectiveBindLimit() 解析：raw 0 时
	// 按角色回退普通 1 / owner 3），供前端直接回显，避免前端复刻角色默认常量。
	EffectiveBindLimit int `json:"effective_bind_limit"`
	// CloudDownload / EffectiveCloudDownload 同列表行语义（raw 三态 + 生效 bool）。
	CloudDownload          int  `json:"cloud_download"`
	EffectiveCloudDownload bool `json:"effective_cloud_download"`
	TotalRequests          int  `json:"total_requests"`
	// 来源 bot（首次 /start 的受理 bot）；0 = 存量行/Web 手动添加。
	SourceBotID       int64  `json:"source_bot_id"`
	SourceBotUsername string `json:"source_bot_username,omitempty"`
}

// handleAPIUsersList 渲染用户列表数据：搜索（ID/用户名/显示名/备注）、状态
// 筛选与分页。筛选与累计请求数沿用 SSR 时代页面确立的口径。
func (s *Server) handleAPIUsersList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.users.list"
	ctx := r.Context()
	page, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	status := r.URL.Query().Get("status")

	users, err := s.st.ListUsers(ctx)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	matched := make([]store.User, 0, len(users))
	for _, u := range users {
		if status != "" && u.Status != status {
			continue
		}
		if query != "" && !userMatches(u, query) {
			continue
		}
		matched = append(matched, u)
	}

	// 累计请求数（一次性聚合后内存关联，与 SSR 相同的尽力而为语义）
	totals := make(map[int64]int, len(users))
	if stats, err := s.st.ListUserRequestStats(ctx, store.StatsFilter{Limit: len(users) + 1}); err != nil {
		s.log.Warn("聚合用户累计请求数失败", "op", op, "error", err.Error())
	} else {
		for _, st := range stats {
			totals[st.UserID] = st.Total
		}
	}

	start, end := page.Offset, page.Offset+page.PageSize
	if start > len(matched) {
		start = len(matched)
	}
	if end > len(matched) {
		end = len(matched)
	}
	items := make([]apiUserRow, 0, end-start)
	for _, u := range matched[start:end] {
		total, ok := totals[u.ID]
		items = append(items, apiUserRow{
			ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
			Status: u.Status, IsOwner: u.IsOwner, Note: u.Note,
			LastUsedAt: u.LastUsedAt, TotalRequests: total, HasTotalRequests: ok,
			CloudDownload: u.CloudDownload, EffectiveCloudDownload: u.EffectiveCloudDownload(),
			SourceBotID: u.SourceBotID, SourceBotUsername: u.SourceBotUsername,
		})
	}
	writeAPIList(w, newAPIListEnvelope(items, page, len(matched)))
}

// handleAPIUserDetail 返回用户详情；不存在经统一链路输出 404 JSON。
func (s *Server) handleAPIUserDetail(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.users.detail"
	ctx := r.Context()
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	u, err := s.st.GetUser(ctx, id)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	day := s.now().In(s.tz(ctx)).Format(dayInputFormat)
	usage, err := s.st.GetUsage(ctx, id, day)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	totals, err := s.st.RequestTotals(ctx, store.StatsFilter{UserID: id})
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	remaining := u.DailyLimit - usage.Used
	if remaining < 0 {
		remaining = 0
	}
	writeAPISingle(w, apiUserDetail{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
		Status: u.Status, IsOwner: u.IsOwner, Note: u.Note,
		CreatedAt: u.CreatedAt, FirstUsedAt: u.FirstUsedAt, LastUsedAt: u.LastUsedAt,
		ArchivedAt: u.ArchivedAt, LastDeniedAt: u.LastDeniedAt,
		LastDeniedReason: u.LastDeniedReason, LastDeniedText: deniedText(u.LastDeniedReason),
		UsedToday: usage.Used, DailyLimit: u.DailyLimit, RemainingToday: remaining,
		SubmitIntervalSec: u.SubmitIntervalSec, ConcurrentLimit: u.ConcurrentLimit,
		BindLimit:              u.BindLimit,
		EffectiveBindLimit:     u.EffectiveBindLimit(),
		CloudDownload:          u.CloudDownload,
		EffectiveCloudDownload: u.EffectiveCloudDownload(),
		TotalRequests:          totals.Total,
		SourceBotID:            u.SourceBotID,
		SourceBotUsername:      u.SourceBotUsername,
	})
}
