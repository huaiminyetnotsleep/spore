package web

// /api/v1/channel-join/*：频道加入管理端点（审批记录 + 已加入频道 + 退出）。
// 业务核心在 internal/joinmgr（Bot /join 与 Web 共用同一服务）；
// 已加入频道列表来自 MTProto 实时对话遍历，读取前先执行总开关熔断
// （Enforce：总开关关闭时自动退出外部拉入的频道）。
// MTProto 离线时相关端点返回受控 503，不暴露底层错误。

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/joinmgr"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
)

// apiRequireChannelJoin 是频道加入端点的可选依赖守卫（同 apiRequireAccess 姿态）。
func (s *Server) apiRequireChannelJoin(w http.ResponseWriter, r *http.Request, op string) bool {
	if s.channelJoin != nil {
		return true
	}
	s.log.Warn("API 频道加入服务未接入", "op", op, "path", r.URL.Path)
	writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
		apiUserMessage(apiCodeUnavailable))
	return false
}

// writeJoinMTProtoError 把 MTProto 离线错误转为受控 503（其余错误走统一链路）。
func (s *Server) writeJoinMTProtoError(w http.ResponseWriter, r *http.Request, op string, err error) bool {
	if errors.Is(err, mtproto.ErrMembershipUnavailable) {
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
			"Telegram 用户号当前离线，请先在「Telegram 连接」页完成登录再操作。")
		return true
	}
	return false
}

// handleAPIJoinRequestsList 返回加入申请（含历史），服务端分页与筛选。
// 查询参数：status（pending/approved/rejected/failed，空为全部）、user_id、
// keyword（频道标题模糊）、since/until（YYYY-MM-DD，按运营时区）、
// page/page_size（parseAPIPageParams 语义）。invite_hash 一律脱敏下发。
func (s *Server) handleAPIJoinRequestsList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.channel_join.requests.list"
	if !s.apiRequireChannelJoin(w, r, op) {
		return
	}
	page, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	tr, err := parseTimeRange(r, s.tz(r.Context()))
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	var userID int64
	if v := r.URL.Query().Get("user_id"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n <= 0 {
			s.apiBadRequest(w, r, op, "user_id 必须为正整数。")
			return
		}
		userID = n
	}
	views, total, err := s.channelJoin.ListRequests(r.Context(), joinmgr.RequestsQuery{
		Status:       r.URL.Query().Get("status"),
		UserID:       userID,
		TitleKeyword: strings.TrimSpace(r.URL.Query().Get("keyword")),
		Since:        tr.Since,
		Until:        tr.Until,
		Page:         page.Page,
		PageSize:     page.PageSize,
	})
	if err != nil {
		if s.writeJoinMTProtoError(w, r, op, err) {
			return
		}
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIList(w, newAPIListEnvelope(views, page, total))
}

// toAnySlice 把具体类型切片转为 []any（列表响应统一 items 形态的小工具）。
func toAnySlice[T any](in []T) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

// handleAPIJoinRequestReview 是申请审批端点（approve=true 同意 / false 拒绝）：
// 同意时实际执行加入；链接失效等加入失败申请置 failed。
func (s *Server) handleAPIJoinRequestReview(approve bool) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess session) {
		op := "api.channel_join.requests.approve"
		action := "channel_join.approve"
		if !approve {
			op = "api.channel_join.requests.reject"
			action = "channel_join.reject"
		}
		if !s.apiRequireChannelJoin(w, r, op) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			s.apiBadRequest(w, r, op, "路径中的申请 ID 无效。")
			return
		}
		var view any
		var rerr error
		if approve {
			view, rerr = s.channelJoin.Approve(r.Context(), sess.idHash, id)
		} else {
			view, rerr = s.channelJoin.Reject(r.Context(), sess.idHash, id)
		}
		if rerr != nil {
			if s.writeJoinMTProtoError(w, r, op, rerr) {
				return
			}
			s.writeAPIAppErr(w, r, op, rerr)
			return
		}
		s.audit(r.Context(), action, "join_request:"+strconv.FormatInt(id, 10), nil)
		writeAPIJSON(w, http.StatusOK, struct {
			apiWriteOK
			Request any `json:"request"`
		}{apiWriteOK{OK: true}, view})
	}
}

// handleAPIJoinRequestsDelete 批量删除审批记录：body 为 {"ids": [...]}。
// 仅终态（approved/rejected/failed）可删，pending 返回逐条失败原因；
// 部分失败不影响其余条目。动作与规模留审计。
func (s *Server) handleAPIJoinRequestsDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.channel_join.requests.delete"
	if !s.apiRequireChannelJoin(w, r, op) {
		return
	}
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if len(in.IDs) == 0 {
		s.apiBadRequest(w, r, op, "请至少选择一条要删除的记录。")
		return
	}
	if len(in.IDs) > 100 {
		s.apiBadRequest(w, r, op, "单次最多删除 100 条记录。")
		return
	}
	outcomes := s.channelJoin.DeleteRequests(r.Context(), in.IDs)
	deleted := 0
	for _, o := range outcomes {
		if o.OK {
			deleted++
		}
	}
	if deleted > 0 {
		s.audit(r.Context(), "channel_join.requests.delete", "join_requests",
			map[string]any{"requested": len(in.IDs), "deleted": deleted})
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Outcomes []any `json:"outcomes"`
	}{apiWriteOK{OK: deleted > 0}, toAnySlice(outcomes)})
}

// handleAPIJoinedChannelsList 返回用户号当前加入的频道（实时列表 + 留痕
// 来源标注）。读取前执行一次熔断（总开关关闭时自动退出外部拉入的频道），
// 管理员因此能在本页看到熔断效果。
func (s *Server) handleAPIJoinedChannelsList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.channel_join.channels.list"
	if !s.apiRequireChannelJoin(w, r, op) {
		return
	}
	// 熔断与请求制频道懒对账均尽力而为：失败（如 MTProto 离线）继续走列表，
	// 由列表错误统一反馈
	if err := s.channelJoin.Enforce(r.Context()); err != nil {
		s.log.Info("频道加入熔断执行失败（继续读取列表）", "op", op, "error", err.Error())
	}
	if err := s.channelJoin.ReconcilePendingJoins(r.Context()); err != nil {
		s.log.Info("请求制频道懒对账失败（继续读取列表）", "op", op, "error", err.Error())
	}
	views, err := s.channelJoin.ListJoined(r.Context())
	if err != nil {
		if s.writeJoinMTProtoError(w, r, op, err) {
			return
		}
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		Items []any `json:"items"`
	}{toAnySlice(views)})
}

// handleAPIJoinedChannelsLeave 批量退出频道：body 为 {"channel_ids": [...]}。
// 逐条返回结果（creator 频道与未加入的 ID 记为失败），部分失败不影响其余。
func (s *Server) handleAPIJoinedChannelsLeave(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.channel_join.channels.leave"
	if !s.apiRequireChannelJoin(w, r, op) {
		return
	}
	var in struct {
		ChannelIDs []int64 `json:"channel_ids"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if len(in.ChannelIDs) == 0 {
		s.apiBadRequest(w, r, op, "请至少选择一个要退出的频道。")
		return
	}
	if len(in.ChannelIDs) > 100 {
		s.apiBadRequest(w, r, op, "单次最多退出 100 个频道。")
		return
	}
	outcomes := s.channelJoin.Leave(r.Context(), in.ChannelIDs)
	succeeded := 0
	for _, o := range outcomes {
		if o.OK {
			succeeded++
		}
	}
	if succeeded > 0 {
		s.audit(r.Context(), "channel_join.leave", "joined_channels",
			map[string]any{"requested": len(in.ChannelIDs), "left": succeeded})
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Outcomes []any `json:"outcomes"`
	}{apiWriteOK{OK: succeeded > 0}, toAnySlice(outcomes)})
}
