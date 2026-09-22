package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func newJoinTestStore(t *testing.T) *Store {
	t.Helper()
	return openTestStore(t)
}

func mustJoinUser(t *testing.T, s *Store, id int64) {
	t.Helper()
	_ = mustUser(t, s, id)
}

func TestCreateJoinRequestDedup(t *testing.T) {
	s := newJoinTestStore(t)
	ctx := context.Background()
	mustJoinUser(t, s, 100)

	r1, created, err := s.CreateJoinRequest(ctx, JoinRequest{UserID: 100, InviteHash: "hashaaa11122233344"})
	if err != nil || !created {
		t.Fatalf("首次写入失败: created=%v err=%v", created, err)
	}
	if r1.Status != JoinPending {
		t.Fatalf("初始状态应为 pending: %s", r1.Status)
	}

	// 同用户同 hash 重复提交 → 返回已有记录
	r2, created, err := s.CreateJoinRequest(ctx, JoinRequest{UserID: 100, InviteHash: "hashaaa11122233344"})
	if err != nil || created {
		t.Fatalf("重复提交应幂等: created=%v err=%v", created, err)
	}
	if r2.ID != r1.ID {
		t.Fatalf("应返回已有记录 %d，得到 %d", r1.ID, r2.ID)
	}

	// 不同 hash 正常新建
	_, created, err = s.CreateJoinRequest(ctx, JoinRequest{UserID: 100, InviteHash: "hashbbb55566677788"})
	if err != nil || !created {
		t.Fatalf("不同 hash 应新建: created=%v err=%v", created, err)
	}
}

func TestReviewJoinRequestOnce(t *testing.T) {
	s := newJoinTestStore(t)
	ctx := context.Background()
	mustJoinUser(t, s, 100)

	r, _, err := s.CreateJoinRequest(ctx, JoinRequest{UserID: 100, InviteHash: "hashaaa11122233344"})
	if err != nil {
		t.Fatalf("写入申请失败: %v", err)
	}

	reviewed, err := s.ReviewJoinRequest(ctx, r.ID, JoinApproved, "admin", "", 0)
	if err != nil {
		t.Fatalf("首次审批失败: %v", err)
	}
	if reviewed.Status != JoinApproved || reviewed.ReviewedBy != "admin" || reviewed.ReviewedAt == 0 {
		t.Fatalf("审批结果不符: %+v", reviewed)
	}

	// 已终态再审批 → STORE_CONSTRAINT
	var ae interface{ GetCode() string }
	_ = ae
	if _, err := s.ReviewJoinRequest(ctx, r.ID, JoinRejected, "admin", "", 0); err == nil {
		t.Fatalf("重复审批应失败")
	}
}

func TestListJoinRequestsFilter(t *testing.T) {
	s := newJoinTestStore(t)
	ctx := context.Background()
	mustJoinUser(t, s, 100)
	mustJoinUser(t, s, 200)

	seed := []JoinRequest{
		{UserID: 100, InviteHash: "hashaaa1112223334", ChannelTitle: "私有频道A"},
		{UserID: 200, InviteHash: "hashbbb5556667778", ChannelTitle: "私有频道B"},
		{UserID: 100, InviteHash: "hashccc9998887776", ChannelTitle: "另一个群"},
	}
	for _, r := range seed {
		if _, _, err := s.CreateJoinRequest(ctx, r); err != nil {
			t.Fatalf("写入申请失败: %v", err)
		}
	}

	pending, err := s.ListJoinRequests(ctx, JoinRequestFilter{Status: JoinPending})
	if err != nil || len(pending) != 3 {
		t.Fatalf("待审批应 3 条: %v %d", err, len(pending))
	}
	if pending[0].UserUsername != "" && pending[0].UserDisplayName == "" {
		t.Fatalf("联表资料缺失")
	}

	// 用户筛选
	byUser, err := s.ListJoinRequests(ctx, JoinRequestFilter{UserID: 200})
	if err != nil || len(byUser) != 1 || byUser[0].UserID != 200 {
		t.Fatalf("用户筛选失败: %v %d", err, len(byUser))
	}

	// 标题关键词（含 % 通配转义验证）
	byKw, err := s.ListJoinRequests(ctx, JoinRequestFilter{TitleKeyword: "私有频道"})
	if err != nil || len(byKw) != 2 {
		t.Fatalf("关键词筛选失败: %v %d", err, len(byKw))
	}
	byPercent, err := s.ListJoinRequests(ctx, JoinRequestFilter{TitleKeyword: "%"})
	if err != nil || len(byPercent) != 0 {
		t.Fatalf("通配符应被转义: %v %d", err, len(byPercent))
	}

	// 时间范围
	now := nowMillis()
	inRange, err := s.ListJoinRequests(ctx, JoinRequestFilter{Since: now - 1000, Until: now + 1000})
	if err != nil || len(inRange) != 3 {
		t.Fatalf("时间范围筛选失败: %v %d", err, len(inRange))
	}
	outRange, err := s.ListJoinRequests(ctx, JoinRequestFilter{Until: now - 100000})
	if err != nil || len(outRange) != 0 {
		t.Fatalf("时间外应无记录: %v %d", err, len(outRange))
	}

	// 分页 + 总数
	page1, err := s.ListJoinRequests(ctx, JoinRequestFilter{Limit: 2, Offset: 0})
	if err != nil || len(page1) != 2 {
		t.Fatalf("分页失败: %v %d", err, len(page1))
	}
	total, err := s.CountJoinRequests(ctx, JoinRequestFilter{})
	if err != nil || total != 3 {
		t.Fatalf("总数统计失败: %v %d", err, total)
	}
	totalPending, err := s.CountJoinRequests(ctx, JoinRequestFilter{UserID: 100})
	if err != nil || totalPending != 2 {
		t.Fatalf("条件总数失败: %v %d", err, totalPending)
	}

	if _, err := s.ListJoinRequests(ctx, JoinRequestFilter{Status: "bogus"}); err == nil {
		t.Fatalf("非法状态筛选应报错")
	}
	if _, err := s.CountJoinRequests(ctx, JoinRequestFilter{Status: "bogus"}); err == nil {
		t.Fatalf("Count 非法状态筛选应报错")
	}
}

func TestJoinedChannelsLifecycle(t *testing.T) {
	s := newJoinTestStore(t)
	ctx := context.Background()
	mustJoinUser(t, s, 100)

	if err := s.UpsertJoinedChannel(ctx, JoinedChannelRecord{
		ChannelID: 42, Title: "频道A", Kind: "channel",
		JoinedVia: JoinedViaCommand, JoinedBy: 100,
	}); err != nil {
		t.Fatalf("写入留痕失败: %v", err)
	}
	// 外部拉入（无归属用户）
	if err := s.UpsertJoinedChannel(ctx, JoinedChannelRecord{
		ChannelID: 43, Title: "频道B", Kind: "supergroup", JoinedVia: JoinedViaExternal,
	}); err != nil {
		t.Fatalf("写入外部留痕失败: %v", err)
	}

	n, err := s.CountActiveJoinedChannels(ctx)
	if err != nil || n != 2 {
		t.Fatalf("活跃数应为 2: %v %d", err, n)
	}

	records, err := s.ListActiveJoinedChannels(ctx)
	if err != nil || len(records) != 2 {
		t.Fatalf("列表应为 2 条: %v %d", err, len(records))
	}

	// 退出一个
	if n, err := s.MarkJoinedChannelsLeft(ctx, []int64{42}, 0); err != nil || n != 1 {
		t.Fatalf("标记退出失败: %v %d", err, n)
	}
	n, _ = s.CountActiveJoinedChannels(ctx)
	if n != 1 {
		t.Fatalf("退出后活跃数应为 1: %d", n)
	}
	// 重复标记为 0 行
	if n, _ := s.MarkJoinedChannelsLeft(ctx, []int64{42}, 0); n != 0 {
		t.Fatalf("重复退出应为 0: %d", n)
	}
	// 空列表安全
	if n, err := s.MarkJoinedChannelsLeft(ctx, nil, 0); err != nil || n != 0 {
		t.Fatalf("空列表应安全: %v %d", err, n)
	}

	// 重新加入：left_at 清零
	if err := s.UpsertJoinedChannel(ctx, JoinedChannelRecord{
		ChannelID: 42, Title: "频道A2", Kind: "channel",
		JoinedVia: JoinedViaApproved, JoinedBy: 100,
	}); err != nil {
		t.Fatalf("重新加入写入失败: %v", err)
	}
	records, _ = s.ListActiveJoinedChannels(ctx)
	if len(records) != 2 {
		t.Fatalf("重新加入后应回到 2 条: %d", len(records))
	}
	for _, r := range records {
		if r.ChannelID == 42 && (r.Title != "频道A2" || r.JoinedVia != JoinedViaApproved) {
			t.Fatalf("重新加入应更新展示字段: %+v", r)
		}
	}
}

func TestGetJoinRequestNotFound(t *testing.T) {
	s := newJoinTestStore(t)
	if _, err := s.GetJoinRequest(context.Background(), 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("期望 ErrNotFound，得到 %v", err)
	}
}

func TestTallyJoinRequestsMixedStatus(t *testing.T) {
	s := newJoinTestStore(t)
	ctx := context.Background()
	mustJoinUser(t, s, 100)

	// pending ×3（含同用户不同 hash）
	for i := 0; i < 3; i++ {
		if _, _, err := s.CreateJoinRequest(ctx, JoinRequest{
			UserID: 100, InviteHash: fmt.Sprintf("hashaaa11122233%02d", i),
		}); err != nil {
			t.Fatalf("写入申请失败: %v", err)
		}
	}
	// approved / rejected / failed 各 1（直接审批迁移 pending 终态）
	r, _, err := s.CreateJoinRequest(ctx, JoinRequest{UserID: 100, InviteHash: "hashbbb55566677788"})
	if err != nil {
		t.Fatalf("写入待审批申请失败: %v", err)
	}
	if _, err := s.ReviewJoinRequest(ctx, r.ID, JoinApproved, "admin", "", 0); err != nil {
		t.Fatalf("审批通过失败: %v", err)
	}
	r, _, err = s.CreateJoinRequest(ctx, JoinRequest{UserID: 100, InviteHash: "hashccc99988877766"})
	if err != nil {
		t.Fatalf("写入待拒绝申请失败: %v", err)
	}
	if _, err := s.ReviewJoinRequest(ctx, r.ID, JoinRejected, "admin", "", 0); err != nil {
		t.Fatalf("拒绝申请失败: %v", err)
	}
	r, _, err = s.CreateJoinRequest(ctx, JoinRequest{UserID: 100, InviteHash: "hashddd44455566677"})
	if err != nil {
		t.Fatalf("写入失败申请失败: %v", err)
	}
	if _, err := s.ReviewJoinRequest(ctx, r.ID, JoinFailed, "admin", "", 0); err != nil {
		t.Fatalf("置为失败失败: %v", err)
	}

	got, err := s.TallyJoinRequests(ctx)
	if err != nil {
		t.Fatalf("统计加入申请失败: %v", err)
	}
	want := JoinRequestTally{Pending: 3, Approved: 1, Rejected: 1, Failed: 1}
	if got != want {
		t.Fatalf("状态计数不符: 得到 %+v，期望 %+v", got, want)
	}
}

func TestTallyJoinedChannelsActiveLeftBySource(t *testing.T) {
	s := newJoinTestStore(t)
	ctx := context.Background()
	mustJoinUser(t, s, 100)

	seed := []JoinedChannelRecord{
		{ChannelID: 1, JoinedVia: JoinedViaCommand, JoinedBy: 100},      // 在加入
		{ChannelID: 2, JoinedVia: JoinedViaCommand, JoinedBy: 100},      // 退出后留痕
		{ChannelID: 3, JoinedVia: JoinedViaApproved, JoinedBy: 100},     // 在加入
		{ChannelID: 4, JoinedVia: JoinedViaApproved, JoinedBy: 100},     // 退出
		{ChannelID: 5, JoinedVia: JoinedViaApproved, JoinedBy: 100},     // 退出
		{ChannelID: 6, JoinedVia: JoinedViaExternal},                    // 外部在加入
		{ChannelID: 7, JoinedVia: JoinedViaWatchSource, JoinedBy: 100},  // 监听源在加入
		{ChannelID: 8, JoinedVia: JoinedViaWatchSource, JoinedBy: 100},  // 监听源退出
		{ChannelID: 9, JoinedVia: JoinedViaBindResolve, JoinedBy: 100},  // 绑定解析在加入
		{ChannelID: 10, JoinedVia: JoinedViaBindResolve, JoinedBy: 100}, // 绑定解析退出
	}
	for _, r := range seed {
		if err := s.UpsertJoinedChannel(ctx, r); err != nil {
			t.Fatalf("写入留痕失败: %v", err)
		}
	}
	if _, err := s.MarkJoinedChannelsLeft(ctx, []int64{2, 4, 5, 8, 10}, nowMillis()); err != nil {
		t.Fatalf("标记退出失败: %v", err)
	}

	got, err := s.TallyJoinedChannels(ctx)
	if err != nil {
		t.Fatalf("统计已加入频道来源失败: %v", err)
	}
	want := JoinedChannelTally{
		CommandActive: 1, CommandLeft: 1,
		ApprovedActive: 1, ApprovedLeft: 2,
		ExternalActive: 1, ExternalLeft: 0,
		WatchSourceActive: 1, WatchSourceLeft: 1,
		BindResolveActive: 1, BindResolveLeft: 1,
	}
	if got != want {
		t.Fatalf("来源计数不符: 得到 %+v，期望 %+v", got, want)
	}
	if got.Active() != 5 || got.Left() != 5 {
		t.Fatalf("合计口径不符: active=%d left=%d", got.Active(), got.Left())
	}
}

func TestTallyJoinEmptyStore(t *testing.T) {
	s := newJoinTestStore(t)
	ctx := context.Background()

	if got, err := s.TallyJoinRequests(ctx); err != nil || got != (JoinRequestTally{}) {
		t.Fatalf("空库申请计数应为零值: got=%+v err=%v", got, err)
	}
	if got, err := s.TallyJoinedChannels(ctx); err != nil || got != (JoinedChannelTally{}) {
		t.Fatalf("空库频道计数应为零值: got=%+v err=%v", got, err)
	}
}
