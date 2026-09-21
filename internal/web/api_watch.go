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
// 统计）。待审批（pending）排最前，其余按添加时间倒序。
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
		Items []any `json:"items"`
	}{append(pending, others...)})
}

// handleAPIWatchSourcesAdd 管理员直接添加监听源：body 为 {"target": "...",
// "enabled": true}。不走用户准入/上限/审批（天然 approved）；源校验失败
// （bot 不在源内或非管理员）返回受控 400。
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
	row, err := s.watch.AdminAdd(r.Context(), sess.idHash, in.Target, enabled)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.audit(r.Context(), "watch_source.add", "channel:"+strconv.FormatInt(row.ChannelID, 10),
		map[string]any{"title": row.Title, "username": row.Username, "enabled": row.Enabled})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Source store.WatchSource `json:"source"`
	}{apiWriteOK{OK: true}, row})
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
	views, total, err := s.watch.ListEvents(r.Context(), watch.EventsQuery{
		ChannelID: channelID, Page: page.Page, PageSize: page.PageSize,
	})
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIList(w, newAPIListEnvelope(toAnySlice(views), page, total))
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
