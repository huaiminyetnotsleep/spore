package web

// /api/v1/watch-sources/*：监听源管理端点（列表 / 管理员添加 / 审批 /
// 暂停开关 / 删除）。业务核心在 internal/watch（Bot /watch 与 Web 共用
// 同一服务）；消息接收在 botapi、转储在 internal/listener。源校验
//（GetChat + bot 管理员）依赖 Bot 客户端，Bot 未就绪时由 watch 服务
// 返回受控错误。

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/watch"
)

// apiRequireWatch 是监听源端点的可选依赖守卫（同 apiRequireChannelJoin 姿态）。
func (s *Server) apiRequireWatch(w http.ResponseWriter, r *http.Request, op string) bool {
	if s.watch != nil {
		return true
	}
	s.log.Warn("API 监听源服务未接入", "op", op, "path", r.URL.Path)
	writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
		apiUserMessage(apiCodeUnavailable))
	return false
}

// handleAPIWatchSourcesList 返回全部监听源（含各状态、申请人资料与预热
// 统计）与全部私有邀请链接申请。监听源 pending 优先，其余按添加时间倒序；
// 邀请申请同序规则（store.ListWatchInviteRequests）。
func (s *Server) handleAPIWatchSourcesList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.watch_sources.list"
	if !s.apiRequireWatch(w, r, op) {
		return
	}
	rows, err := s.watch.ListAll(r.Context())
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	invites, err := s.watch.ListInviteRequests(r.Context())
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	// pending 优先展示（审批入口），其余保持时间倒序
	pending := make([]any, 0, len(rows))
	others := make([]any, 0, len(rows))
	for _, row := range rows {
		if row.Status == store.WatchPending {
			pending = append(pending, row)
		} else {
			others = append(others, row)
		}
	}
	writeAPIJSON(w, http.StatusOK, struct {
		Items          []any                      `json:"items"`
		InviteRequests []store.WatchInviteRequest `json:"invite_requests"`
	}{append(pending, others...), invites})
}

// handleAPIWatchSourcesAdd 管理员直接添加监听源：body 为 {"target": "...",
// "enabled": true}。不走用户准入/上限/审批；普通标识天然 approved，私有
// 邀请链接返回申请（可能处于等待态或已随激活产出 source）。源校验失败
// 返回受控 4xx，读取账号离线返回受控 503。
func (s *Server) handleAPIWatchSourcesAdd(w http.ResponseWriter, r *http.Request, sess session) {
	const op = "api.watch_sources.add"
	if !s.apiRequireWatch(w, r, op) {
		return
	}
	var in struct {
		Target  string `json:"target"`
		Enabled *bool  `json:"enabled"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.Target == "" {
		s.apiBadRequest(w, r, op, "请填写频道/超级群组标识（@username、t.me 链接或 -100 ID）。")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	out, err := s.watch.AdminAdd(r.Context(), sess.idHash, in.Target, enabled)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	// 审计不含完整邀请链接：邀请路径只记录申请 ID、脱敏 hash 与状态
	if out.Source != nil {
		s.audit(r.Context(), "watch_source.add", "channel:"+strconv.FormatInt(out.Source.ChannelID, 10),
			map[string]any{"title": out.Source.Title, "username": out.Source.Username, "enabled": out.Source.Enabled})
	} else if out.InviteRequest != nil {
		s.audit(r.Context(), "watch_source.add_invite", "watch_invite_request:"+strconv.FormatInt(out.InviteRequest.ID, 10),
			map[string]any{"masked_hash": out.InviteRequest.MaskedHash, "status": out.InviteRequest.Status, "enabled": enabled})
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Source        *store.WatchSource        `json:"source,omitempty"`
		InviteRequest *store.WatchInviteRequest `json:"invite_request,omitempty"`
	}{apiWriteOK{OK: true}, out.Source, out.InviteRequest})
}

// handleAPIWatchInviteReview 是邀请申请审批端点（approve=true 同意并推进
// 状态机 / false 拒绝）。响应返回最新申请与激活产出的源（未激活为 null）。
func (s *Server) handleAPIWatchInviteReview(approve bool) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess session) {
		op := "api.watch_invite_requests.approve"
		if !approve {
			op = "api.watch_invite_requests.reject"
		}
		if !s.apiRequireWatch(w, r, op) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id == 0 {
			s.apiBadRequest(w, r, op, "路径中的申请 ID 无效。")
			return
		}
		var (
			req     store.WatchInviteRequest
			src     *store.WatchSource
			auditOp string
		)
		if approve {
			req, src, err = s.watch.ApproveInviteRequest(r.Context(), sess.idHash, id)
			auditOp = "watch_invite_request.approve"
		} else {
			req, err = s.watch.RejectInviteRequest(r.Context(), sess.idHash, id)
			auditOp = "watch_invite_request.reject"
		}
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				s.apiBadRequest(w, r, op, "邀请申请不存在或已被处理。")
				return
			}
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		s.audit(r.Context(), auditOp, "watch_invite_request:"+strconv.FormatInt(id, 10),
			map[string]any{"status": req.Status})
		writeAPIJSON(w, http.StatusOK, struct {
			apiWriteOK
			InviteRequest store.WatchInviteRequest `json:"invite_request"`
			Source        *store.WatchSource       `json:"source,omitempty"`
		}{apiWriteOK{OK: true}, req, src})
	}
}

// handleAPIWatchInviteRetry 重试等待/失败中的邀请申请（立即推进一轮）。
func (s *Server) handleAPIWatchInviteRetry(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.watch_invite_requests.retry"
	if !s.apiRequireWatch(w, r, op) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		s.apiBadRequest(w, r, op, "路径中的申请 ID 无效。")
		return
	}
	req, src, err := s.watch.RetryInviteRequest(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.apiBadRequest(w, r, op, "邀请申请不存在。")
			return
		}
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.audit(r.Context(), "watch_invite_request.retry", "watch_invite_request:"+strconv.FormatInt(id, 10),
		map[string]any{"status": req.Status})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		InviteRequest store.WatchInviteRequest `json:"invite_request"`
		Source        *store.WatchSource       `json:"source,omitempty"`
	}{apiWriteOK{OK: true}, req, src})
}

// handleAPIWatchInviteDelete 删除邀请申请记录（任意状态硬删除）。
func (s *Server) handleAPIWatchInviteDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.watch_invite_requests.delete"
	if !s.apiRequireWatch(w, r, op) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		s.apiBadRequest(w, r, op, "路径中的申请 ID 无效。")
		return
	}
	if err := s.watch.DeleteInviteRequest(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.apiBadRequest(w, r, op, "邀请申请不存在。")
			return
		}
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.audit(r.Context(), "watch_invite_request.delete", "watch_invite_request:"+strconv.FormatInt(id, 10), nil)
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}

// handleAPIWatchSourceReview 是申请审批端点（approve=true 同意 / false 拒绝）。
func (s *Server) handleAPIWatchSourceReview(approve bool) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess session) {
		op := "api.watch_sources.approve"
		if !approve {
			op = "api.watch_sources.reject"
		}
		if !s.apiRequireWatch(w, r, op) {
			return
		}
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id == 0 {
			s.apiBadRequest(w, r, op, "路径中的频道 ID 无效。")
			return
		}
		var row store.WatchSource
		if approve {
			row, err = s.watch.Approve(r.Context(), sess.idHash, id)
		} else {
			row, err = s.watch.Reject(r.Context(), sess.idHash, id)
		}
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				s.apiBadRequest(w, r, op, "监听源不存在或不是待审批状态。")
				return
			}
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		s.audit(r.Context(), "watch_source.review", "channel:"+strconv.FormatInt(id, 10),
			map[string]any{"approved": approve})
		writeAPIJSON(w, http.StatusOK, struct {
			apiWriteOK
			Source store.WatchSource `json:"source"`
		}{apiWriteOK{OK: true}, row})
	}
}

// handleAPIWatchSourceToggle 切换 approved 行的暂停开关：body {"enabled": bool}。
func (s *Server) handleAPIWatchSourceToggle(w http.ResponseWriter, r *http.Request, sess session) {
	const op = "api.watch_sources.toggle"
	if !s.apiRequireWatch(w, r, op) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		s.apiBadRequest(w, r, op, "路径中的频道 ID 无效。")
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.Enabled == nil {
		s.apiBadRequest(w, r, op, "请提供 enabled 字段。")
		return
	}
	row, err := s.watch.SetEnabled(r.Context(), id, *in.Enabled)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.apiBadRequest(w, r, op, "监听源不存在。")
			return
		}
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.audit(r.Context(), "watch_source.toggle", "channel:"+strconv.FormatInt(id, 10),
		map[string]any{"enabled": row.Enabled})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Source store.WatchSource `json:"source"`
	}{apiWriteOK{OK: true}, row})
}

// handleAPIWatchSourceDelete 删除监听源行（任意状态，含待审批）。
func (s *Server) handleAPIWatchSourceDelete(w http.ResponseWriter, r *http.Request, sess session) {
	const op = "api.watch_sources.delete"
	if !s.apiRequireWatch(w, r, op) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		s.apiBadRequest(w, r, op, "路径中的频道 ID 无效。")
		return
	}
	row, err := s.watch.Delete(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.apiBadRequest(w, r, op, "监听源不存在。")
			return
		}
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.audit(r.Context(), "watch_source.delete", "channel:"+strconv.FormatInt(id, 10),
		map[string]any{"title": row.Title, "status": row.Status})
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}

// handleAPIWatchEventsList 返回预热事件页（监听记录）：哪个 bot 在哪个源
// 转发了哪些消息（含源消息链接）、走哪条路径（copy=服务端复制 /
// fallback=受保护重传，带关联 requests 行）。查询参数：channel_id（可选
// 源筛选）、page/page_size（parseAPIPageParams 语义，page_size 上限 100）。
func (s *Server) handleAPIWatchEventsList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.watch_events.list"
	if !s.apiRequireWatch(w, r, op) {
		return
	}
	page, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	var channelID int64
	if v := r.URL.Query().Get("channel_id"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n == 0 {
			s.apiBadRequest(w, r, op, "channel_id 必须为非零整数。")
			return
		}
		channelID = n
	}
	path := r.URL.Query().Get("path")
	if path != "" && path != store.WatchPathCopy && path != store.WatchPathFallback {
		s.apiBadRequest(w, r, op, "path 仅支持 copy 或 fallback。")
		return
	}
	views, total, err := s.watch.ListEvents(r.Context(), watch.EventsQuery{
		ChannelID: channelID, Path: path, Page: page.Page, PageSize: page.PageSize,
	})
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIList(w, newAPIListEnvelope(toAnySlice(views), page, total))
}

// handleAPIWatchEventsDelete 删除预热事件（单条/批量共用）：body
// {"ids":[...]}（1–100 个正整数 ID）。删除只影响留痕记录，不影响缓存副本。
func (s *Server) handleAPIWatchEventsDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.watch_events.delete"
	if !s.apiRequireWatch(w, r, op) {
		return
	}
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if len(in.IDs) == 0 || len(in.IDs) > 100 {
		s.apiBadRequest(w, r, op, "请提供 1–100 个事件 ID。")
		return
	}
	for _, id := range in.IDs {
		if id <= 0 {
			s.apiBadRequest(w, r, op, "事件 ID 必须为正整数。")
			return
		}
	}
	deleted, err := s.watch.DeleteEvents(r.Context(), in.IDs)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.audit(r.Context(), "watch_events.delete", "watch_events",
		map[string]any{"requested": len(in.IDs), "deleted": deleted})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Deleted int64 `json:"deleted"`
	}{apiWriteOK{OK: true}, deleted})
}

// handleAPIWatchStats 返回监听模块统计（业务统计页）：源状态计数（当前
// 态）、按源/按 bot/按用户聚合与按日趋势。查询参数与 /api/v1/stats 同
// 款：since_day/until_day（运营时区）、all=1（全量，忽略时间界）、
// bot_id（限定受理 bot；0 = 全部）。趋势升序返回，缺日由前端容器按
// 序补零。
func (s *Server) handleAPIWatchStats(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.watch_stats.get"
	if !s.apiRequireWatch(w, r, op) {
		return
	}
	tr := timeRange{}
	if r.URL.Query().Get("all") == "" {
		var err error
		if tr, err = parseTimeRange(r, s.tz(r.Context())); err != nil {
			s.apiBadRequest(w, r, op, err.Error())
			return
		}
		tr = fillDefaultStatsRange(tr, s.now(), s.tz(r.Context()))
	}
	var botID int64
	if raw := strings.TrimSpace(r.URL.Query().Get("bot_id")); raw != "" {
		var perr error
		if botID, perr = strconv.ParseInt(raw, 10, 64); perr != nil || botID <= 0 {
			s.apiBadRequest(w, r, op, "机器人 ID 必须为正整数")
			return
		}
	}
	view, err := s.watch.Stats(r.Context(), watch.StatsQuery{
		Since: tr.Since, Until: tr.Until, BotID: botID,
	}, int64(utcOffsetSec(s.tz(r.Context()), s.now())))
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPISingle(w, struct {
		watch.WatchStatsView
		SinceDay string `json:"since_day"`
		UntilDay string `json:"until_day"`
	}{view, tr.SinceDay, tr.UntilDay})
}

// handleAPIWatchSourceLeave 把池内全部 bot 退出源聊天（频道即放弃管理员）
// 并删除监听源行；逐 bot 尽力而为，结果带成功/失败计数。
func (s *Server) handleAPIWatchSourceLeave(w http.ResponseWriter, r *http.Request, sess session) {
	const op = "api.watch_sources.leave"
	if !s.apiRequireWatch(w, r, op) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id == 0 {
		s.apiBadRequest(w, r, op, "路径中的频道 ID 无效。")
		return
	}
	out, err := s.watch.Leave(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.apiBadRequest(w, r, op, "监听源不存在。")
			return
		}
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.audit(r.Context(), "watch_source.leave", "channel:"+strconv.FormatInt(id, 10),
		map[string]any{"left": out.Left, "failed": out.Failed})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		watch.LeaveOutcome `json:"outcome"`
	}{apiWriteOK{OK: true}, out})
}
