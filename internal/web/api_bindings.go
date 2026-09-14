package web

// GET /api/v1/channel-bindings、POST /api/v1/channel-bindings、
// POST /api/v1/channel-bindings/{id}/delete：频道绑定管理端点。
// 业务核心在 internal/binding（Bot 指令与 Web 共用同一服务）：
// 绑定校验（机器人为频道管理员且有发言权限）、归属约束（同一频道只归属
// 一个用户）与审计都由服务完成；Web 端可绑定/解绑任意用户的绑定。

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// apiChannelBindingRow 是频道绑定列表行 DTO：绑定信息 + 所属用户展示资料
// （用户可能已被硬删除，资料为空串，前端退化为仅展示 user_id）。
type apiChannelBindingRow struct {
	ChannelID       int64  `json:"channel_id"`
	UserID          int64  `json:"user_id"`
	Username        string `json:"username"`  // 频道公开用户名（无 @），私有频道为空
	Title           string `json:"title"`     // 绑定时取得的频道标题
	BoundVia        string `json:"bound_via"` // bot | web
	CreatedAt       int64  `json:"created_at"`
	UserUsername    string `json:"user_username"`
	UserDisplayName string `json:"user_display_name"`
}

// apiRequireBindings 是绑定端点的可选依赖守卫（同 apiRequireAccess 姿态）。
func (s *Server) apiRequireBindings(w http.ResponseWriter, r *http.Request, op string) bool {
	if s.bindings != nil {
		return true
	}
	s.log.Warn("API 频道绑定服务未接入", "op", op, "path", r.URL.Path)
	writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
		apiUserMessage(apiCodeUnavailable))
	return false
}

// handleAPIChannelBindingsList 返回全部频道绑定（含所属用户资料），按绑定
// 时间倒序。绑定记录量级与用户同阶（≤ 数百），不分页。
func (s *Server) handleAPIChannelBindingsList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.channel_bindings.list"
	if !s.apiRequireBindings(w, r, op) {
		return
	}
	rows, err := s.bindings.ListAll(r.Context())
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	items := make([]apiChannelBindingRow, 0, len(rows))
	for _, row := range rows {
		items = append(items, apiChannelBindingRow{
			ChannelID:       row.ChannelID,
			UserID:          row.UserID,
			Username:        row.Username,
			Title:           row.Title,
			BoundVia:        row.BoundVia,
			CreatedAt:       row.CreatedAt,
			UserUsername:    row.UserUsername,
			UserDisplayName: row.UserDisplayName,
		})
	}
	writeAPIJSON(w, http.StatusOK, struct {
		Items []apiChannelBindingRow `json:"items"`
	}{items})
}

// apiChannelBindRequest 是绑定请求体：绑定归属用户 + 频道标识
// （@username / t.me 链接 / -100… 数字 ID）。
type apiChannelBindRequest struct {
	UserID int64  `json:"user_id"`
	Target string `json:"target"`
}

// handleAPIChannelBindingAdd 以管理员身份为指定用户绑定频道。
// 绑定校验经 binding.Service（Bot API GetChat/GetChatMember）；频道已被
// 其他用户绑定时返回 409 CHANNEL_ALREADY_BOUND。
func (s *Server) handleAPIChannelBindingAdd(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.channel_bindings.add"
	if !s.apiRequireBindings(w, r, op) {
		return
	}
	var in apiChannelBindRequest
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.UserID <= 0 {
		s.apiBadRequest(w, r, op, "所属用户 ID 必须为正整数。")
		return
	}
	if in.Target == "" {
		s.apiBadRequest(w, r, op, "请填写频道用户名、t.me 链接或频道 ID。")
		return
	}
	if _, err := s.st.GetUser(r.Context(), in.UserID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.apiBadRequest(w, r, op, "该用户不存在。")
			return
		}
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	bound, err := s.bindings.Bind(r.Context(), binding.BindInput{
		UserID: in.UserID,
		Target: in.Target,
		Via:    store.BoundViaWeb,
	})
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Binding apiChannelBindingRow `json:"binding"`
	}{apiWriteOK{OK: true}, apiChannelBindingRow{
		ChannelID: bound.ChannelID,
		UserID:    bound.UserID,
		Username:  bound.Username,
		Title:     bound.Title,
		BoundVia:  bound.BoundVia,
		CreatedAt: bound.CreatedAt,
	}})
}

// handleAPIChannelBindingDelete 解除指定频道 ID 的绑定（管理端可解绑任意
// 用户的绑定）。频道 ID 形如 -1001234567890，为负数，不能复用正整数
// apiPathID，单独解析。
func (s *Server) handleAPIChannelBindingDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.channel_bindings.delete"
	if !s.apiRequireBindings(w, r, op) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		s.apiBadRequest(w, r, op, "路径中的频道 ID 无效。")
		return
	}
	removed, err := s.bindings.Unbind(r.Context(), 0, strconv.FormatInt(id, 10), true)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Binding apiChannelBindingRow `json:"binding"`
	}{apiWriteOK{OK: true}, apiChannelBindingRow{
		ChannelID: removed.ChannelID,
		UserID:    removed.UserID,
		Username:  removed.Username,
		Title:     removed.Title,
		BoundVia:  removed.BoundVia,
		CreatedAt: removed.CreatedAt,
	}})
}
