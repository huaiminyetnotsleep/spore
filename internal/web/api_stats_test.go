package web

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func TestCompleteStatsTrendUsesOperatingDaysAndNullEmptyRate(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	tr := timeRange{SinceDay: "2026-08-27", UntilDay: "2026-08-29"}
	points := []store.TrendPoint{
		{Day: "2026-08-27", Total: 3, Succeeded: 2, Failed: 1},
		{Day: "2026-08-29", Total: 2, Succeeded: 0, Failed: 0},
	}

	got := completeStatsTrend(points, tr, loc)
	if len(got) != 3 {
		t.Fatalf("趋势应按运营日期补齐为 3 天，得到 %d: %+v", len(got), got)
	}
	if got[0].Day != "2026-08-27" || got[0].Total != 3 || got[0].ErrorRate == nil || *got[0].ErrorRate != 1.0/3.0 {
		t.Errorf("首日趋势或错误率不对: %+v", got[0])
	}
	if got[1].Day != "2026-08-28" || got[1].Total != 0 || got[1].Succeeded != 0 || got[1].Failed != 0 || got[1].ErrorRate != nil {
		t.Errorf("缺失日期应补零且错误率为空: %+v", got[1])
	}
	if got[2].Day != "2026-08-29" || got[2].Total != 2 || got[2].ErrorRate != nil {
		t.Errorf("无终态样本日期错误率应为空: %+v", got[2])
	}
}

func TestStatsErrorDistAddsOtherAndRatios(t *testing.T) {
	points := []store.DistPoint{
		{Key: "A", Count: 5},
		{Key: "B", Count: 3},
		{Key: "C", Count: 2},
	}
	got := statsErrorDist(points, 10, 2)
	if len(got) != 3 {
		t.Fatalf("Top 2 之外应合并为其他，得到 %+v", got)
	}
	if got[0].Key != "A" || got[0].Count != 5 || got[0].Ratio != 0.5 {
		t.Errorf("首个错误原因不对: %+v", got[0])
	}
	if got[1].Key != "B" || got[1].Count != 3 || got[1].Ratio != 0.3 {
		t.Errorf("第二个错误原因不对: %+v", got[1])
	}
	if got[2].Key != statsOtherErrorKey || got[2].Count != 2 || got[2].Ratio != 0.2 {
		t.Errorf("其他错误汇总不对: %+v", got[2])
	}
}

// 未认证 401 JSON（与其它 /api/v1 端点一致）。
func TestAPIStatsUnauthenticatedJSON401(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)
	resp := e.do(j, http.MethodGet, "/api/v1/stats", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未认证 GET /api/v1/stats 应返回 401，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeUnauthorized)
}

// seedStatsRequest 建一条带显式请求时间的终态请求（对齐服务注入的
// fakeClock 缺省范围，避免种子时间与时钟不同源）。
func seedStatsRequest(t *testing.T, e *testEnv, userID int64, channel string, msgID int, status string, requestedAt int64) store.Request {
	t.Helper()
	r, err := e.st.CreateRequest(context.Background(), store.Request{
		UserID: userID, SourceKind: store.SourcePublic, ChannelKey: channel,
		MessageID: msgID, RequestedAt: requestedAt,
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	if status == store.RequestSucceeded || status == store.RequestFailed {
		if err := e.st.FinishRequest(context.Background(), r.ID, store.RequestResult{
			Status: status, ErrorCode: "MEDIA_DOWNLOAD_FAILED", MediaType: "photo",
			FileSize: 12345, FileName: "cat.jpg", At: requestedAt + 60000,
		}); err != nil {
			t.Fatalf("落库终态失败: %v", err)
		}
		r.Status = status
	}
	return r
}

func TestCompleteDCTrendFillsEmptyDays(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	tr := timeRange{SinceDay: "2026-08-27", UntilDay: "2026-08-29"}
	points := []store.DCTrendPoint{
		{Day: "2026-08-27", Dist: []store.DistPoint{{Key: "2", Count: 3}}},
		{Day: "2026-08-29", Dist: []store.DistPoint{{Key: "", Count: 1}}},
	}

	got := completeDCTrend(points, tr, loc)
	if len(got) != 3 {
		t.Fatalf("DC 日分布应按运营日期补齐为 3 天，得到 %d: %+v", len(got), got)
	}
	if got[0].Day != "2026-08-27" || len(got[0].Dist) != 1 || got[0].Dist[0].Key != "2" {
		t.Errorf("首日 DC 分布不对: %+v", got[0])
	}
	if got[1].Day != "2026-08-28" || len(got[1].Dist) != 0 {
		t.Errorf("缺失日期应补齐为空分布: %+v", got[1])
	}
	if got[2].Day != "2026-08-29" || len(got[2].Dist) != 1 || got[2].Dist[0].Key != "" {
		t.Errorf("未记录键应原样透传: %+v", got[2])
	}
}

// seedDCStatsRequest 建一条带源媒体 DC 的成功请求（对齐服务注入的 fakeClock）。
func seedDCStatsRequest(t *testing.T, e *testEnv, userID int64, channel string, msgID int, requestedAt int64, dcs []int) store.Request {
	t.Helper()
	r, err := e.st.CreateRequest(context.Background(), store.Request{
		UserID: userID, SourceKind: store.SourcePublic, ChannelKey: channel,
		MessageID: msgID, RequestedAt: requestedAt,
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	if err := e.st.FinishRequest(context.Background(), r.ID, store.RequestResult{
		Status: store.RequestSucceeded, SourceMediaDCIDs: dcs, At: requestedAt + 60000,
	}); err != nil {
		t.Fatalf("落库终态失败: %v", err)
	}
	r.Status = store.RequestSucceeded
	return r
}

// 源媒体 DC 分布/日分布口径（跨 DC 双计、空日补齐）；all=1 忽略时间范围并回显空串。
func TestAPIStatsDCDistTrendAndAllMode(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	u := seedUser(t, e, 302, store.UserEnabled)
	// fakeClock 固定上海 2026-08-27 12:00（= UTC 04:00）
	day := time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC).UnixMilli()
	old := time.Date(2026, 8, 1, 4, 0, 0, 0, time.UTC).UnixMilli() // 缺省近 7 天之外
	seedDCStatsRequest(t, e, u.ID, "alpha", 1, day, []int{2, 4})   // 跨 DC：各计一次
	seedDCStatsRequest(t, e, u.ID, "alpha", 2, day, []int{2})
	seedDCStatsRequest(t, e, u.ID, "beta", 3, old, []int{1})

	// 缺省近 7 天：dc_dist 只含当天请求；dc_trend 补齐 7 天
	var view apiStatsView
	getAPIJSON(t, e, j, "/api/v1/stats", &view)
	dist := view.Requests.DCDist
	if len(dist) != 2 || dist[0].Key != "2" || dist[0].Count != 2 || dist[1].Key != "4" || dist[1].Count != 1 {
		t.Errorf("DC 分布不对: %+v", dist)
	}
	trend := view.Requests.DCTrend
	if len(trend) != 7 {
		t.Fatalf("DC 日分布应补齐 7 天，得到 %d", len(trend))
	}
	if first := trend[0]; first.Day != "2026-08-21" || len(first.Dist) != 0 {
		t.Errorf("空数据日应补齐为空分布: %+v", first)
	}
	if last := trend[6]; last.Day != "2026-08-27" || len(last.Dist) != 2 ||
		last.Dist[0].Key != "2" || last.Dist[0].Count != 2 || last.Dist[1].Key != "4" {
		t.Errorf("当天 DC 日分布不对: %+v", last)
	}

	// all=1：忽略时间范围（含 7 天外请求）、回显空串；dc_trend 只含有数据日期
	var allView apiStatsView
	getAPIJSON(t, e, j, "/api/v1/stats?all=1", &allView)
	if allView.SinceDay != "" || allView.UntilDay != "" {
		t.Errorf("全量模式应回显空范围: %+v", allView)
	}
	if allView.Requests.Total != 3 {
		t.Errorf("全量模式应统计全部请求，得到 %d", allView.Requests.Total)
	}
	allDist := allView.Requests.DCDist
	if len(allDist) != 3 || allDist[0].Key != "2" || allDist[0].Count != 2 ||
		allDist[1].Key != "1" || allDist[2].Key != "4" {
		t.Errorf("全量 DC 分布不对: %+v", allDist)
	}
	allTrend := allView.Requests.DCTrend
	if len(allTrend) != 2 || allTrend[0].Day != "2026-08-01" || allTrend[1].Day != "2026-08-27" {
		t.Errorf("全量 DC 日分布日期不对: %+v", allTrend)
	}
}

// 缺省近 7 天：时间范围回显与趋势补齐；显式范围过滤生效。
func TestAPIStatsRangeMetricsAndDefault7Days(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	u := seedUser(t, e, 301, store.UserEnabled)
	// fakeClock 固定上海 2026-08-27 12:00（= UTC 04:00）：种子落在当天。
	day := time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC).UnixMilli()
	seedStatsRequest(t, e, u.ID, "alpha", 1, store.RequestSucceeded, day)
	seedStatsRequest(t, e, u.ID, "alpha", 2, store.RequestFailed, day)
	seedStatsRequest(t, e, u.ID, "beta", 3, store.RequestSucceeded, day)

	// 缺省：运营时区近 7 天含当天（fakeClock 固定在上海 2026-08-27 12:00）
	var view apiStatsView
	getAPIJSON(t, e, j, "/api/v1/stats", &view)
	if view.SinceDay != "2026-08-21" || view.UntilDay != "2026-08-27" {
		t.Errorf("缺省范围应为近 7 天，得到 %s ~ %s", view.SinceDay, view.UntilDay)
	}
	if view.Requests.Total != 3 || view.Requests.Succeeded != 2 || view.Requests.Failed != 1 ||
		view.Requests.Unfinished != 0 || view.Requests.ActiveUsers != 1 {
		t.Errorf("范围指标不对: %+v", view.Requests)
	}
	if view.Requests.SuccessRate != 2.0/3.0 || view.Requests.ErrorRate != 1.0/3.0 {
		t.Errorf("比率不对: success=%v error=%v", view.Requests.SuccessRate, view.Requests.ErrorRate)
	}
	if len(view.Requests.Trend) != 7 {
		t.Errorf("趋势应补齐 7 天，得到 %d 点", len(view.Requests.Trend))
	}
	if last := view.Requests.Trend[6]; last.Day != "2026-08-27" || last.Total != 3 {
		t.Errorf("当天趋势应为 3 条请求: %+v", last)
	}
	if len(view.Requests.TopChannels) != 2 || view.Requests.TopChannels[0].Key != "alpha" {
		t.Errorf("频道排行不对: %+v", view.Requests.TopChannels)
	}
	if len(view.Requests.TopUsers) != 1 || view.Requests.TopUsers[0].ID != u.ID {
		t.Errorf("用户排行不对: %+v", view.Requests.TopUsers)
	}
	if len(view.Requests.MediaDist) != 1 || view.Requests.MediaDist[0].Key != "photo" {
		t.Errorf("媒体分布不对: %+v", view.Requests.MediaDist)
	}
	if len(view.Requests.ErrorDist) != 1 || view.Requests.ErrorDist[0].Key != "MEDIA_DOWNLOAD_FAILED" {
		t.Errorf("错误分布不对: %+v", view.Requests.ErrorDist)
	}

	// 显式无请求范围：指标为 0、分布为空，趋势保留范围日期（until 含当天）
	var empty apiStatsView
	getAPIJSON(t, e, j, "/api/v1/stats?since=2026-01-01&until=2026-01-02", &empty)
	if empty.SinceDay != "2026-01-01" || empty.UntilDay != "2026-01-02" {
		t.Errorf("显式范围应原样回显，得到 %s ~ %s", empty.SinceDay, empty.UntilDay)
	}
	if empty.Requests.Total != 0 || len(empty.Requests.Trend) != 2 || empty.Requests.Trend[0].Day != "2026-01-01" ||
		empty.Requests.Trend[0].ErrorRate != nil || empty.Requests.Trend[1].Total != 0 {
		t.Errorf("空范围指标/趋势不对: %+v", empty.Requests)
	}
	if len(empty.Requests.TopChannels) != 0 || len(empty.Requests.ErrorDist) != 0 {
		t.Errorf("空范围分布应为空: %+v", empty.Requests)
	}

	// 非法日期 400；since > until 400
	for _, q := range []string{"?since=bad", "?since=2026-01-02&until=2026-01-01"} {
		resp := e.do(j, http.MethodGet, "/api/v1/stats"+q, "", "")
		requireBadRequest(t, resp, "/api/v1/stats"+q)
	}

	// 响应禁止缓存
	if resp := e.do(j, http.MethodGet, "/api/v1/stats", "", ""); resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("stats 应禁止缓存，得到 Cache-Control %q", resp.Header.Get("Cache-Control"))
	} else {
		bodyOf(t, resp)
	}
}
