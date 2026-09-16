package web

// GET /api/v1/channels、GET /api/v1/channels/{key}：频道统计排行与详情查询
// API。全部来自 requests 行聚合（stats DAO，不触发任何频道访问）；
// since/until 筛选与 SSR /channels 页面一致（运营时区 YYYY-MM-DD）。
// 详情页头部统计与 SSR 同口径：全时段聚合；趋势与分布才应用时间范围。

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// apiChannelRow 是频道排行行 DTO；SuccessRate 为 [0,1] 浮点，展示格式由前端处理。
type apiChannelRow struct {
	Key             string  `json:"key"`
	Total           int     `json:"total"`
	Succeeded       int     `json:"succeeded"`
	Failed          int     `json:"failed"`
	SuccessRate     float64 `json:"success_rate"`
	LastRequestedAt int64   `json:"last_requested_at"`
}

// apiTrendPoint 是按日趋势数据点（Day 为运营时区当地日 YYYY-MM-DD）。
type apiTrendPoint struct {
	Day       string `json:"day"`
	Total     int    `json:"total"`
	Succeeded int    `json:"succeeded"`
	Failed    int    `json:"failed"`
}

// apiChannelBotRow 是频道详情内按受理 bot 的分布条目（多机器人池）。
// BotID=0（存量行）照常返回，前端显示"未知"。
type apiChannelBotRow struct {
	BotID       int64  `json:"bot_id"`
	BotUsername string `json:"bot_username,omitempty"`
	Total       int    `json:"total"`
	Succeeded   int    `json:"succeeded"`
	Failed      int    `json:"failed"`
}

// apiChannelDetail 是频道详情 DTO：头部统计（全时段）+ 时间范围内的趋势、
// 分布与按 bot 分布。
type apiChannelDetail struct {
	Key       string             `json:"key"`
	Stats     apiChannelRow      `json:"stats"`
	Trend     []apiTrendPoint    `json:"trend"`
	MediaDist []apiDistRow       `json:"media_dist"`
	ErrorDist []apiDistRow       `json:"error_dist"`
	BotDist   []apiChannelBotRow `json:"bot_dist"`
	SinceDay  string             `json:"since_day"`
	UntilDay  string             `json:"until_day"`
}

// handleAPIChannelsList 返回频道排行分页数据；总数经 CountChannelStats DAO。
func (s *Server) handleAPIChannelsList(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.channels.list"
	ctx := r.Context()
	tr, err := parseTimeRange(r, s.tz(ctx))
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	page, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	rangeFilter := store.StatsFilter{Since: tr.Since, Until: tr.Until}
	// bot_id 筛选（多机器人池）：限定只统计该受理 bot 的请求
	if raw := strings.TrimSpace(r.URL.Query().Get("bot_id")); raw != "" {
		botID, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil || botID <= 0 {
			s.apiBadRequest(w, r, op, "机器人 ID 必须为正整数")
			return
		}
		rangeFilter.BotID = botID
	}
	total, err := s.st.CountChannelStats(ctx, rangeFilter)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	pageFilter := rangeFilter
	pageFilter.Limit, pageFilter.Offset = page.PageSize, page.Offset
	stats, err := s.st.ListChannelStats(ctx, pageFilter)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	items := make([]apiChannelRow, 0, len(stats))
	for _, c := range stats {
		items = append(items, apiChannelRow{
			Key: c.ChannelKey, Total: c.Total, Succeeded: c.Succeeded, Failed: c.Failed,
			SuccessRate: c.SuccessRate, LastRequestedAt: c.LastRequested,
		})
	}
	writeAPIList(w, newAPIListEnvelope(items, page, total))
}

// handleAPIChannelDetail 返回频道详情：头部聚合 + 按日趋势 + 媒体/错误分布。
// 频道在请求记录中无数据时输出 404 JSON（SSR 渲染"无数据"页面的 API 对应态）。
func (s *Server) handleAPIChannelDetail(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.channels.detail"
	ctx := r.Context()
	key, err := url.PathUnescape(r.PathValue("key"))
	if err != nil || key == "" {
		s.apiBadRequest(w, r, op, "频道标识无效。")
		return
	}
	tr, err := parseTimeRange(r, s.tz(ctx))
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	stats, err := s.st.ListChannelStats(ctx, store.StatsFilter{ChannelKey: key})
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	if len(stats) == 0 {
		s.apiNotFound(w, r, op)
		return
	}
	c := stats[0]
	view := apiChannelDetail{
		Key: key, SinceDay: tr.SinceDay, UntilDay: tr.UntilDay,
		Stats: apiChannelRow{
			Key: c.ChannelKey, Total: c.Total, Succeeded: c.Succeeded, Failed: c.Failed,
			SuccessRate: c.SuccessRate, LastRequestedAt: c.LastRequested,
		},
		Trend:     []apiTrendPoint{},
		MediaDist: []apiDistRow{},
		ErrorDist: []apiDistRow{},
		BotDist:   []apiChannelBotRow{},
	}
	rangeFilter := store.StatsFilter{ChannelKey: key, Since: tr.Since, Until: tr.Until}
	// 趋势按运营时区当前偏移对齐当地日（沿用 SSR 时代频道详情页的口径）
	if trend, err := s.st.ListRequestTrend(ctx, rangeFilter, utcOffsetSec(s.tz(ctx), s.now())); err != nil {
		s.log.Warn("聚合频道趋势失败", "op", op, "error", err.Error())
	} else {
		for _, p := range trend {
			view.Trend = append(view.Trend, apiTrendPoint{
				Day: p.Day, Total: p.Total, Succeeded: p.Succeeded, Failed: p.Failed})
		}
	}
	if md, err := s.st.ListMediaTypeDist(ctx, rangeFilter); err != nil {
		s.log.Warn("聚合频道媒体分布失败", "op", op, "error", err.Error())
	} else {
		for _, d := range md {
			view.MediaDist = append(view.MediaDist, apiDistRow{Key: d.Key, Count: d.Count})
		}
	}
	if ed, err := s.st.ListErrorDist(ctx, rangeFilter); err != nil {
		s.log.Warn("聚合频道错误分布失败", "op", op, "error", err.Error())
	} else {
		for _, d := range ed {
			view.ErrorDist = append(view.ErrorDist, apiDistRow{Key: d.Key, Count: d.Count})
		}
	}
	// 按受理 bot 分布（多机器人池；尽力而为，失败不缺整页）
	if bd, err := s.st.ListChannelBotStats(ctx, rangeFilter); err != nil {
		s.log.Warn("聚合频道机器人分布失败", "op", op, "error", err.Error())
	} else {
		for _, b := range bd {
			view.BotDist = append(view.BotDist, apiChannelBotRow{
				BotID: b.BotID, BotUsername: b.BotUsername,
				Total: b.Total, Succeeded: b.Succeeded, Failed: b.Failed,
			})
		}
	}
	writeAPISingle(w, view)
}
