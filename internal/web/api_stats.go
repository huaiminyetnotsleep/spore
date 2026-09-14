package web

// GET /api/v1/stats 业务统计 API：承接原总览页的时间范围请求指标与图表
// 数据（趋势/频道排行/用户排行/媒体分布/错误分布/源媒体 DC 分布与日分布）。
// 查询参数 since/until（运营时区 YYYY-MM-DD，缺省近 7 天含当天，与原
// /api/v1/overview 行为一致）；all=1 表示全量统计，忽略时间范围（since_day/
// until_day 回显空串）。聚合全部基于 requests 行（stats DAO），不触发任何
// 频道访问。DTO 只携带原始值；中文标签与格式化由前端共享 util 处理。

import (
	"net/http"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// apiStatsTrend 是按运营时区分桶的统计趋势数据点。
// 无终态请求的日期 ErrorRate 为 nil，避免把无样本误认为零错误。
type apiStatsTrend struct {
	Day       string   `json:"day"`
	Total     int      `json:"total"`
	Succeeded int      `json:"succeeded"`
	Failed    int      `json:"failed"`
	ErrorRate *float64 `json:"error_rate"`
}

// apiStatsChannel 是统计"主要频道"行。
type apiStatsChannel struct {
	Key             string  `json:"key"`
	Total           int     `json:"total"`
	Succeeded       int     `json:"succeeded"`
	Failed          int     `json:"failed"`
	SuccessRate     float64 `json:"success_rate"`
	LastRequestedAt int64   `json:"last_requested_at"`
}

// apiStatsUser 是统计"主要用户"行；展示信息缺失时由前端回退为 ID。
type apiStatsUser struct {
	ID            int64   `json:"id"`
	Username      string  `json:"username"`
	DisplayName   string  `json:"display_name"`
	Total         int     `json:"total"`
	Succeeded     int     `json:"succeeded"`
	Failed        int     `json:"failed"`
	SuccessRate   float64 `json:"success_rate"`
	LastRequested int64   `json:"last_requested_at"`
}

// apiStatsError 是错误原因排行条目；Ratio 为失败请求中的占比。
type apiStatsError struct {
	Key   string  `json:"key"`
	Count int     `json:"count"`
	Ratio float64 `json:"ratio"`
}

// apiDCTrendPoint 是按日源媒体 DC 分布数据点；Dist 的 key 为 DC ID 十进制
// 串，空串表示未记录。
type apiDCTrendPoint struct {
	Day  string       `json:"day"`
	Dist []apiDistRow `json:"dist"`
}

// apiStatsRequests 是时间范围内的请求指标与图表数据。
type apiStatsRequests struct {
	Total       int     `json:"total"`
	Succeeded   int     `json:"succeeded"`
	Failed      int     `json:"failed"`
	Unfinished  int     `json:"unfinished"`
	ActiveUsers int     `json:"active_users"`
	SuccessRate float64 `json:"success_rate"` // [0,1]；无终态时为 0
	ErrorRate   float64 `json:"error_rate"`   // [0,1]；无终态时为 0

	Trend       []apiStatsTrend   `json:"trend"`
	TopChannels []apiStatsChannel `json:"top_channels"`
	TopUsers    []apiStatsUser    `json:"top_users"`
	MediaDist   []apiDistRow      `json:"media_dist"`
	ErrorDist   []apiStatsError   `json:"error_dist"`
	DCDist      []apiDistRow      `json:"dc_dist"`
	DCTrend     []apiDCTrendPoint `json:"dc_trend"`
}

// apiStatsView 是 GET /api/v1/stats 的只读 DTO。
type apiStatsView struct {
	SinceDay string           `json:"since_day"` // 实际生效的时间范围回显（运营时区 YYYY-MM-DD）
	UntilDay string           `json:"until_day"`
	Requests apiStatsRequests `json:"requests"`
}

// handleAPIStats 返回时间范围业务统计。主要计数失败经统一 apperr 映射
// 输出 JSON 错误；分布类查询沿用总览的尽力而为语义（失败记 Warn、留空），
// 不让辅助分布拖垮整页数据。
func (s *Server) handleAPIStats(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.stats"
	ctx := r.Context()
	loc := s.tz(ctx)
	now := s.now()

	tr := timeRange{}
	if r.URL.Query().Get("all") == "" {
		var err error
		if tr, err = parseTimeRange(r, loc); err != nil {
			s.apiBadRequest(w, r, op, err.Error())
			return
		}
		tr = fillDefaultStatsRange(tr, now, loc)
	} // all=1：全量统计，零值 timeRange 不带任何时间界，回显空串。

	view := apiStatsView{
		SinceDay: tr.SinceDay,
		UntilDay: tr.UntilDay,
		Requests: apiStatsRequests{
			Trend:       []apiStatsTrend{},
			TopChannels: []apiStatsChannel{},
			TopUsers:    []apiStatsUser{},
			MediaDist:   []apiDistRow{},
			ErrorDist:   []apiStatsError{},
			DCDist:      []apiDistRow{},
			DCTrend:     []apiDCTrendPoint{},
		},
	}

	// 用户表（Top 10 用户排行的展示信息关联；≤100 行，内存关联）
	users, err := s.st.ListUsers(ctx)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}

	// 范围指标与分布（stats DAO；不触发任何频道访问）
	filter := store.StatsFilter{Since: tr.Since, Until: tr.Until}
	totals, err := s.st.RequestTotals(ctx, filter)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	view.Requests.Total = totals.Total
	view.Requests.Succeeded = totals.Succeeded
	view.Requests.Failed = totals.Failed
	view.Requests.Unfinished = totals.Unfinished
	view.Requests.ActiveUsers = totals.ActiveUsers
	view.Requests.SuccessRate = successRate(totals.Succeeded, totals.Failed)
	view.Requests.ErrorRate = errorRate(totals.Succeeded, totals.Failed)

	if trend, err := s.st.ListRequestTrend(ctx, filter, utcOffsetSec(loc, now)); err == nil {
		view.Requests.Trend = completeStatsTrend(trend, tr, loc)
	} else {
		s.log.Warn("聚合统计请求趋势失败", "op", op, "error", err.Error())
	}
	if chs, err := s.st.ListChannelStats(ctx, store.StatsFilter{Since: tr.Since, Until: tr.Until, Limit: 5}); err == nil {
		for _, c := range chs {
			view.Requests.TopChannels = append(view.Requests.TopChannels, apiStatsChannel{
				Key: c.ChannelKey, Total: c.Total, Succeeded: c.Succeeded, Failed: c.Failed,
				SuccessRate: c.SuccessRate, LastRequestedAt: c.LastRequested})
		}
	} else {
		s.log.Warn("聚合主要频道失败", "op", op, "error", err.Error())
	}

	userByID := make(map[int64]store.User, len(users))
	for _, u := range users {
		userByID[u.ID] = u
	}
	if stats, err := s.st.ListUserRequestStats(ctx, store.StatsFilter{Since: tr.Since, Until: tr.Until, Limit: 10}); err == nil {
		for _, stat := range stats {
			u := userByID[stat.UserID]
			view.Requests.TopUsers = append(view.Requests.TopUsers, apiStatsUser{
				ID: stat.UserID, Username: u.Username, DisplayName: u.DisplayName,
				Total: stat.Total, Succeeded: stat.Succeeded, Failed: stat.Failed,
				SuccessRate: successRate(stat.Succeeded, stat.Failed), LastRequested: stat.LastRequested,
			})
		}
	} else {
		s.log.Warn("聚合主要用户失败", "op", op, "error", err.Error())
	}
	if md, err := s.st.ListMediaTypeDist(ctx, store.StatsFilter{Since: tr.Since, Until: tr.Until, Limit: 5}); err == nil {
		for _, d := range md {
			view.Requests.MediaDist = append(view.Requests.MediaDist, apiDistRow{Key: d.Key, Count: d.Count})
		}
	} else {
		s.log.Warn("聚合媒体分布失败", "op", op, "error", err.Error())
	}
	if ed, err := s.st.ListErrorDist(ctx, filter); err == nil {
		view.Requests.ErrorDist = statsErrorDist(ed, totals.Failed, 5)
	} else {
		s.log.Warn("聚合错误分布失败", "op", op, "error", err.Error())
	}
	if dd, err := s.st.ListDCDist(ctx, filter); err == nil {
		for _, d := range dd {
			view.Requests.DCDist = append(view.Requests.DCDist, apiDistRow{Key: d.Key, Count: d.Count})
		}
	} else {
		s.log.Warn("聚合源媒体 DC 分布失败", "op", op, "error", err.Error())
	}
	if dt, err := s.st.ListDCTrend(ctx, filter, utcOffsetSec(loc, now)); err == nil {
		view.Requests.DCTrend = completeDCTrend(dt, tr, loc)
	} else {
		s.log.Warn("聚合源媒体 DC 日分布失败", "op", op, "error", err.Error())
	}

	w.Header().Set("Cache-Control", "no-store")
	writeAPIJSON(w, http.StatusOK, view)
}

// statsDaySequence 返回明确起止范围内的日期序列（运营时区，升序）；
// 范围不完整或非法时返回 nil，由调用方按 DAO 返回顺序透传。
func statsDaySequence(tr timeRange, loc *time.Location) []string {
	if tr.SinceDay == "" || tr.UntilDay == "" {
		return nil
	}
	start, startErr := time.ParseInLocation(dayInputFormat, tr.SinceDay, loc)
	end, endErr := time.ParseInLocation(dayInputFormat, tr.UntilDay, loc)
	if startErr != nil || endErr != nil || start.After(end) {
		return nil
	}
	days := make([]string, 0, int(end.Sub(start)/(24*time.Hour))+1)
	for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
		days = append(days, day.Format(dayInputFormat))
	}
	return days
}

// completeStatsTrend 补齐有明确起止日期范围内的空日期；单边范围沿用 DAO 返回的已有日期。
func completeStatsTrend(points []store.TrendPoint, tr timeRange, loc *time.Location) []apiStatsTrend {
	byDay := make(map[string]store.TrendPoint, len(points))
	for _, p := range points {
		byDay[p.Day] = p
	}

	toView := func(p store.TrendPoint) apiStatsTrend {
		view := apiStatsTrend{Day: p.Day, Total: p.Total, Succeeded: p.Succeeded, Failed: p.Failed}
		if done := p.Succeeded + p.Failed; done > 0 {
			rate := errorRate(p.Succeeded, p.Failed)
			view.ErrorRate = &rate
		}
		return view
	}

	if days := statsDaySequence(tr, loc); days != nil {
		out := make([]apiStatsTrend, 0, len(days))
		for _, day := range days {
			if p, ok := byDay[day]; ok {
				out = append(out, toView(p))
			} else {
				out = append(out, apiStatsTrend{Day: day})
			}
		}
		return out
	}

	out := make([]apiStatsTrend, 0, len(points))
	for _, p := range points {
		out = append(out, toView(p))
	}
	return out
}

// completeDCTrend 补齐明确起止日期范围内的空日期（空日无任何 DC 计数）；
// 单边或全量范围沿用 DAO 返回的已有日期。
func completeDCTrend(points []store.DCTrendPoint, tr timeRange, loc *time.Location) []apiDCTrendPoint {
	byDay := make(map[string]store.DCTrendPoint, len(points))
	for _, p := range points {
		byDay[p.Day] = p
	}

	toView := func(p store.DCTrendPoint) apiDCTrendPoint {
		dist := make([]apiDistRow, 0, len(p.Dist))
		for _, d := range p.Dist {
			dist = append(dist, apiDistRow{Key: d.Key, Count: d.Count})
		}
		return apiDCTrendPoint{Day: p.Day, Dist: dist}
	}

	if days := statsDaySequence(tr, loc); days != nil {
		out := make([]apiDCTrendPoint, 0, len(days))
		for _, day := range days {
			if p, ok := byDay[day]; ok {
				out = append(out, toView(p))
			} else {
				out = append(out, apiDCTrendPoint{Day: day, Dist: []apiDistRow{}})
			}
		}
		return out
	}

	out := make([]apiDCTrendPoint, 0, len(points))
	for _, p := range points {
		out = append(out, toView(p))
	}
	return out
}

const statsOtherErrorKey = "__other__"

// statsErrorDist 限制错误原因排行并把剩余错误合并为 Other。
func statsErrorDist(points []store.DistPoint, failed, limit int) []apiStatsError {
	if limit <= 0 || len(points) == 0 {
		return []apiStatsError{}
	}
	if limit > len(points) {
		limit = len(points)
	}
	out := make([]apiStatsError, 0, limit+1)
	for _, p := range points[:limit] {
		ratio := float64(0)
		if failed > 0 {
			ratio = float64(p.Count) / float64(failed)
		}
		out = append(out, apiStatsError{Key: p.Key, Count: p.Count, Ratio: ratio})
	}
	if len(points) == limit {
		return out
	}

	other := 0
	for _, p := range points[limit:] {
		other += p.Count
	}
	ratio := float64(0)
	if failed > 0 {
		ratio = float64(other) / float64(failed)
	}
	return append(out, apiStatsError{Key: statsOtherErrorKey, Count: other, Ratio: ratio})
}
