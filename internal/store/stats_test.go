package store

import (
	"context"
	"testing"
	"time"
)

// seedReq 是统计测试的请求构造参数；at 为 requested_at（Unix 毫秒）。
type seedReq struct {
	user    int64
	channel string
	msgID   int
	status  string
	at      int64
	media   string // 终态时落库的 media_type
	errCode string // 终态为 failed 时落库的 error_code
}

func seedRequest(t *testing.T, s *Store, in seedReq) Request {
	t.Helper()
	r, err := s.CreateRequest(context.Background(), Request{
		UserID:      in.user,
		ChannelKey:  in.channel,
		MessageID:   in.msgID,
		RequestedAt: in.at,
		QueuedAt:    in.at,
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	switch in.status {
	case RequestProcessing:
		if err := s.MarkRequestStarted(context.Background(), r.ID, in.at+1); err != nil {
			t.Fatalf("标记开始失败: %v", err)
		}
	case RequestSucceeded, RequestFailed:
		if err := s.FinishRequest(context.Background(), r.ID, RequestResult{
			Status:    in.status,
			MediaType: in.media,
			ErrorCode: in.errCode,
			At:        in.at + 10,
		}); err != nil {
			t.Fatalf("落库终态失败: %v", err)
		}
	}
	return r
}

func TestListChannelStats(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)
	mustUser(t, s, 2)

	for _, in := range []seedReq{
		{1, "alpha", 1, RequestSucceeded, 100, "text", ""},
		{1, "alpha", 2, RequestFailed, 200, "", "CHANNEL_NOT_ACCESSIBLE"},
		{1, "alpha", 3, RequestQueued, 300, "", ""},
		{2, "alpha", 4, RequestSucceeded, 400, "photo", ""},
		{1, "beta", 5, RequestFailed, 150, "", "MESSAGE_NOT_FOUND"},
	} {
		seedRequest(t, s, in)
	}

	got, err := s.ListChannelStats(ctx, StatsFilter{})
	if err != nil {
		t.Fatalf("聚合频道统计失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应有两个频道，得到 %d", len(got))
	}
	a, b := got[0], got[1]
	if a.ChannelKey != "alpha" || b.ChannelKey != "beta" {
		t.Fatalf("应按请求量倒序: %+v", got)
	}
	if a.Total != 4 || a.Succeeded != 2 || a.Failed != 1 {
		t.Errorf("alpha 计数不对: %+v", a)
	}
	if want := 2.0 / 3.0; a.SuccessRate < want-1e-9 || a.SuccessRate > want+1e-9 {
		t.Errorf("alpha 成功率应为 2/3，得到 %f", a.SuccessRate)
	}
	if a.LastRequested != 400 {
		t.Errorf("alpha 最近请求应为 400，得到 %d", a.LastRequested)
	}
	if b.Total != 1 || b.Succeeded != 0 || b.Failed != 1 || b.SuccessRate != 0 || b.LastRequested != 150 {
		t.Errorf("beta 聚合不对: %+v", b)
	}

	// 时间范围：只统计 requested_at >= 250
	got, _ = s.ListChannelStats(ctx, StatsFilter{Since: 250})
	if len(got) != 1 || got[0].ChannelKey != "alpha" || got[0].Total != 2 ||
		got[0].Succeeded != 1 || got[0].Failed != 0 || got[0].SuccessRate != 1 {
		t.Errorf("时间范围聚合不对: %+v", got)
	}

	// 用户维度
	got, _ = s.ListChannelStats(ctx, StatsFilter{UserID: 2})
	if len(got) != 1 || got[0].ChannelKey != "alpha" || got[0].Total != 1 {
		t.Errorf("用户维度聚合不对: %+v", got)
	}

	// 分页
	if got, _ = s.ListChannelStats(ctx, StatsFilter{Limit: 1}); len(got) != 1 || got[0].ChannelKey != "alpha" {
		t.Errorf("第一页应为 alpha: %+v", got)
	}
	if got, _ = s.ListChannelStats(ctx, StatsFilter{Limit: 1, Offset: 1}); len(got) != 1 || got[0].ChannelKey != "beta" {
		t.Errorf("第二页应为 beta: %+v", got)
	}
}

// CountChannelStats 与 ListChannelStats 的分页口径一致：同一筛选下
// 总数应等于全量分组行数，且时间范围/用户维度过滤同样生效。
func TestCountChannelStats(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)
	mustUser(t, s, 2)

	for _, in := range []seedReq{
		{1, "alpha", 1, RequestSucceeded, 100, "text", ""},
		{1, "beta", 2, RequestFailed, 200, "", "MESSAGE_NOT_FOUND"},
		{2, "alpha", 3, RequestQueued, 300, "", ""},
	} {
		seedRequest(t, s, in)
	}

	n, err := s.CountChannelStats(ctx, StatsFilter{})
	if err != nil {
		t.Fatalf("统计频道数失败: %v", err)
	}
	if n != 2 {
		t.Errorf("应有两个频道，得到 %d", n)
	}
	if n, _ = s.CountChannelStats(ctx, StatsFilter{Since: 250}); n != 1 {
		t.Errorf("时间范围后应只有 1 个频道，得到 %d", n)
	}
	if n, _ = s.CountChannelStats(ctx, StatsFilter{ChannelKey: "beta"}); n != 1 {
		t.Errorf("指定频道应只有 1 个，得到 %d", n)
	}
	if n, _ = s.CountChannelStats(ctx, StatsFilter{UserID: 2}); n != 1 {
		t.Errorf("用户维度应只有 1 个频道，得到 %d", n)
	}
}

func TestListRequestTrend(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	shanghai := 8 * 3600 // Asia/Shanghai 恒定偏移
	// 当地日边界：UTC 16:00 即上海次日 00:00
	localDay1 := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)  // 上海 08-27 18:00
	localDay2a := time.Date(2026, 8, 27, 20, 0, 0, 0, time.UTC) // 上海 08-28 04:00
	localDay2b := time.Date(2026, 8, 28, 5, 0, 0, 0, time.UTC)  // 上海 08-28 13:00

	for i, in := range []seedReq{
		{1, "alpha", 1, RequestSucceeded, localDay1.UnixMilli(), "text", ""},
		{1, "alpha", 2, RequestFailed, localDay1.UnixMilli(), "", "MESSAGE_NOT_FOUND"},
		{1, "alpha", 3, RequestSucceeded, localDay2a.UnixMilli(), "photo", ""},
		{1, "beta", 4, RequestSucceeded, localDay2b.UnixMilli(), "text", ""},
	} {
		in.msgID = i + 1
		seedRequest(t, s, in)
	}

	// 按运营时区（UTC+8）分桶：两天四个点按日合并为两点
	got, err := s.ListRequestTrend(ctx, StatsFilter{}, shanghai)
	if err != nil {
		t.Fatalf("聚合趋势失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("上海时区应聚为两天，得到 %d 个点: %+v", len(got), got)
	}
	if got[0].Day != "2026-08-27" || got[0].Total != 2 || got[0].Succeeded != 1 || got[0].Failed != 1 {
		t.Errorf("第一天聚合不对: %+v", got[0])
	}
	if got[1].Day != "2026-08-28" || got[1].Total != 2 || got[1].Succeeded != 2 || got[1].Failed != 0 {
		t.Errorf("第二天聚合不对: %+v", got[1])
	}

	// 按 UTC（偏移 0）分桶：20:00 与 10:00 同属 08-27，验证偏移参与边界
	got, _ = s.ListRequestTrend(ctx, StatsFilter{}, 0)
	if len(got) != 2 || got[0].Day != "2026-08-27" || got[0].Total != 3 || got[1].Day != "2026-08-28" || got[1].Total != 1 {
		t.Errorf("UTC 分桶不对: %+v", got)
	}

	// 单频道过滤
	got, _ = s.ListRequestTrend(ctx, StatsFilter{ChannelKey: "beta"}, shanghai)
	if len(got) != 1 || got[0].Day != "2026-08-28" || got[0].Total != 1 {
		t.Errorf("频道过滤趋势不对: %+v", got)
	}
}

func TestListMediaTypeDist(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)
	mustUser(t, s, 2)

	for i, in := range []seedReq{
		{1, "alpha", 0, RequestSucceeded, 100, "photo", ""},
		{1, "alpha", 0, RequestSucceeded, 200, "photo", ""},
		{1, "alpha", 0, RequestSucceeded, 300, "video", ""},
		{1, "alpha", 0, RequestFailed, 400, "", "CHANNEL_NOT_ACCESSIBLE"}, // 早失败：无媒体类型
		{2, "beta", 0, RequestSucceeded, 500, "text", ""},
	} {
		in.msgID = i + 1
		seedRequest(t, s, in)
	}

	got, err := s.ListMediaTypeDist(ctx, StatsFilter{})
	if err != nil {
		t.Fatalf("聚合媒体分布失败: %v", err)
	}
	want := []DistPoint{{"photo", 2}, {"", 1}, {"text", 1}, {"video", 1}}
	if len(got) != len(want) {
		t.Fatalf("媒体分布应有 %d 组，得到 %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("第 %d 组应为 %+v，得到 %+v", i, w, got[i])
		}
	}

	// 频道 + 时间范围过滤
	got, _ = s.ListMediaTypeDist(ctx, StatsFilter{ChannelKey: "alpha", Since: 250})
	if len(got) != 2 {
		t.Fatalf("过滤后应剩 2 组，得到 %+v", got)
	}
	if got[0].Key != "" || got[0].Count != 1 || got[1].Key != "video" || got[1].Count != 1 {
		t.Errorf("过滤后分布不对: %+v", got)
	}
}

func TestListErrorDist(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	for i, in := range []seedReq{
		{1, "alpha", 0, RequestFailed, 100, "", "CHANNEL_NOT_ACCESSIBLE"},
		{1, "alpha", 0, RequestFailed, 200, "video", "FILE_TOO_LARGE"},
		{1, "alpha", 0, RequestFailed, 300, "", "CHANNEL_NOT_ACCESSIBLE"},
		{1, "alpha", 0, RequestSucceeded, 400, "text", ""}, // 成功不参与错误分布
		{1, "alpha", 0, RequestQueued, 500, "", ""},        // 未完成不参与
	} {
		in.msgID = i + 1
		seedRequest(t, s, in)
	}

	got, err := s.ListErrorDist(ctx, StatsFilter{})
	if err != nil {
		t.Fatalf("聚合错误分布失败: %v", err)
	}
	want := []DistPoint{{"CHANNEL_NOT_ACCESSIBLE", 2}, {"FILE_TOO_LARGE", 1}}
	if len(got) != len(want) {
		t.Fatalf("错误分布应有 %d 组，得到 %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("第 %d 组应为 %+v，得到 %+v", i, w, got[i])
		}
	}
}

// seedDCRequest 落一条带源媒体 DC 的成功请求（at 为 requested_at，Unix 毫秒）。
func seedDCRequest(t *testing.T, s *Store, user int64, channel string, msgID int, at int64, dcs []int) {
	t.Helper()
	r, err := s.CreateRequest(context.Background(), Request{
		UserID: user, ChannelKey: channel, MessageID: msgID, RequestedAt: at, QueuedAt: at,
	})
	if err != nil {
		t.Fatalf("创建 DC 请求失败: %v", err)
	}
	if err := s.FinishRequest(context.Background(), r.ID, RequestResult{
		Status: RequestSucceeded, SourceMediaDCIDs: dcs, At: at + 10,
	}); err != nil {
		t.Fatalf("落库 DC 终态失败: %v", err)
	}
}

func TestListDCDist(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	seedDCRequest(t, s, 1, "alpha", 1, 100, []int{2, 4})                 // 跨 DC：每个 DC 各计一次
	seedDCRequest(t, s, 1, "alpha", 2, 150, []int{2})                    // 单 DC
	seedRequest(t, s, seedReq{1, "beta", 3, RequestFailed, 200, "", ""}) // 终态但无媒体 → 未记录
	seedRequest(t, s, seedReq{1, "beta", 4, RequestQueued, 250, "", ""}) // 未完成（NULL 列）→ 未记录
	seedDCRequest(t, s, 1, "alpha", 5, 500, []int{1})                    // 时间过滤外

	got, err := s.ListDCDist(ctx, StatsFilter{})
	if err != nil {
		t.Fatalf("聚合 DC 分布失败: %v", err)
	}
	// 计数倒序、同数按 Key 升序（空串在前）
	want := []DistPoint{{"", 2}, {"2", 2}, {"1", 1}, {"4", 1}}
	if len(got) != len(want) {
		t.Fatalf("DC 分布应有 %d 组，得到 %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("第 %d 组应为 %+v，得到 %+v", i, w, got[i])
		}
	}

	// 时间范围：只统计 requested_at >= 300（仅 DC 1 那条）
	got, _ = s.ListDCDist(ctx, StatsFilter{Since: 300})
	want = []DistPoint{{"1", 1}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("Since 过滤后应为 %+v，得到 %+v", want, got)
	}

	// 时间范围：只统计 requested_at < 300（未记录 2 + DC 2 双计 2 + DC 4）
	got, _ = s.ListDCDist(ctx, StatsFilter{Until: 300})
	want = []DistPoint{{"", 2}, {"2", 2}, {"4", 1}}
	if len(got) != len(want) {
		t.Fatalf("Until 过滤后应有 %d 组，得到 %d: %+v", len(want), len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("Until 过滤第 %d 组应为 %+v，得到 %+v", i, w, got[i])
		}
	}
}

func TestListDCTrend(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	shanghai := 8 * 3600
	localDay1 := time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC)  // 上海 08-27 18:00
	localDay2a := time.Date(2026, 8, 27, 20, 0, 0, 0, time.UTC) // 上海 08-28 04:00

	seedDCRequest(t, s, 1, "alpha", 1, localDay1.UnixMilli(), []int{2, 4})
	seedDCRequest(t, s, 1, "alpha", 2, localDay1.UnixMilli(), []int{2})
	seedDCRequest(t, s, 1, "alpha", 3, localDay2a.UnixMilli(), []int{4})
	seedRequest(t, s, seedReq{1, "beta", 4, RequestQueued, localDay2a.UnixMilli(), "", ""}) // 未记录

	got, err := s.ListDCTrend(ctx, StatsFilter{}, shanghai)
	if err != nil {
		t.Fatalf("聚合 DC 日分布失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("上海时区应聚为两天，得到 %d: %+v", len(got), got)
	}
	// 日内排序：Key 升序
	wantDay1 := []DistPoint{{"2", 2}, {"4", 1}}
	if got[0].Day != "2026-08-27" || len(got[0].Dist) != len(wantDay1) {
		t.Fatalf("第一天不对: %+v", got[0])
	}
	for i, w := range wantDay1 {
		if got[0].Dist[i] != w {
			t.Errorf("第一天第 %d 组应为 %+v，得到 %+v", i, w, got[0].Dist[i])
		}
	}
	// 空串（未记录）固定末位
	wantDay2 := []DistPoint{{"4", 1}, {"", 1}}
	if got[1].Day != "2026-08-28" || len(got[1].Dist) != len(wantDay2) {
		t.Fatalf("第二天不对: %+v", got[1])
	}
	for i, w := range wantDay2 {
		if got[1].Dist[i] != w {
			t.Errorf("第二天第 %d 组应为 %+v，得到 %+v", i, w, got[1].Dist[i])
		}
	}

	// 按 UTC（偏移 0）分桶：两批时间同属 08-27
	got, _ = s.ListDCTrend(ctx, StatsFilter{}, 0)
	if len(got) != 1 || got[0].Day != "2026-08-27" {
		t.Fatalf("UTC 分桶不对: %+v", got)
	}
	wantUTC := []DistPoint{{"2", 2}, {"4", 2}, {"", 1}}
	if len(got[0].Dist) != len(wantUTC) {
		t.Fatalf("UTC 日内分布组数不对: %+v", got[0].Dist)
	}
	for i, w := range wantUTC {
		if got[0].Dist[i] != w {
			t.Errorf("UTC 日内第 %d 组应为 %+v，得到 %+v", i, w, got[0].Dist[i])
		}
	}

	// 单频道过滤：beta 只有未完成（未记录）一行
	got, _ = s.ListDCTrend(ctx, StatsFilter{ChannelKey: "beta"}, shanghai)
	if len(got) != 1 || got[0].Day != "2026-08-28" || len(got[0].Dist) != 1 || got[0].Dist[0].Key != "" {
		t.Errorf("频道过滤 DC 日分布不对: %+v", got)
	}
}

func TestListUserRequestStats(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)
	mustUser(t, s, 2)

	for i, in := range []seedReq{
		{1, "alpha", 0, RequestSucceeded, 100, "text", ""},
		{1, "alpha", 0, RequestSucceeded, 200, "photo", ""},
		{1, "beta", 0, RequestFailed, 300, "", "MESSAGE_NOT_FOUND"},
		{2, "alpha", 0, RequestSucceeded, 400, "text", ""},
	} {
		in.msgID = i + 1
		seedRequest(t, s, in)
	}

	got, err := s.ListUserRequestStats(ctx, StatsFilter{})
	if err != nil {
		t.Fatalf("聚合用户用量失败: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("应有两个用户，得到 %d", len(got))
	}
	if got[0].UserID != 1 || got[0].Total != 3 || got[0].Succeeded != 2 || got[0].Failed != 1 || got[0].LastRequested != 300 {
		t.Errorf("用户 1 聚合不对: %+v", got[0])
	}
	if got[1].UserID != 2 || got[1].Total != 1 || got[1].LastRequested != 400 {
		t.Errorf("用户 2 聚合不对: %+v", got[1])
	}

	// 时间范围 + 分页
	if got, _ = s.ListUserRequestStats(ctx, StatsFilter{Since: 350}); len(got) != 1 || got[0].UserID != 2 {
		t.Errorf("时间范围聚合不对: %+v", got)
	}
	if got, _ = s.ListUserRequestStats(ctx, StatsFilter{Limit: 1}); len(got) != 1 || got[0].UserID != 1 {
		t.Errorf("分页聚合不对: %+v", got)
	}
}

func TestRequestTotals(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)
	mustUser(t, s, 2)

	for i, in := range []seedReq{
		{1, "alpha", 0, RequestSucceeded, 100, "text", ""},
		{1, "alpha", 0, RequestFailed, 200, "", "MESSAGE_NOT_FOUND"},
		{1, "alpha", 0, RequestQueued, 300, "", ""},
		{2, "beta", 0, RequestProcessing, 400, "", ""},
		{2, "beta", 0, RequestSucceeded, 500, "photo", ""},
	} {
		in.msgID = i + 1
		seedRequest(t, s, in)
	}

	got, err := s.RequestTotals(ctx, StatsFilter{})
	if err != nil {
		t.Fatalf("汇总总量失败: %v", err)
	}
	if got.Total != 5 || got.Succeeded != 2 || got.Failed != 1 || got.Unfinished != 2 || got.ActiveUsers != 2 {
		t.Errorf("总量聚合不对: %+v", got)
	}

	// 时间范围：仅 [150, 550) 的四条（200/300/400/500）
	got, _ = s.RequestTotals(ctx, StatsFilter{Since: 150, Until: 550})
	if got.Total != 4 || got.Succeeded != 1 || got.Failed != 1 || got.Unfinished != 2 || got.ActiveUsers != 2 {
		t.Errorf("时间范围总量不对: %+v", got)
	}

	// 空范围（同时验证 SUM 对零行的 COALESCE 回退）
	s2 := openTestStore(t)
	got, err = s2.RequestTotals(ctx, StatsFilter{})
	if err != nil {
		t.Fatalf("空库汇总失败: %v", err)
	}
	if got != (RequestTotals{}) {
		t.Errorf("空库应为零值，得到 %+v", got)
	}
}
