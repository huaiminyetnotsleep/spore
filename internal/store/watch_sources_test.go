package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func upsertWatch(t *testing.T, s *Store, src WatchSource) WatchSource {
	t.Helper()
	out, err := s.UpsertWatchSource(context.Background(), src)
	if err != nil {
		t.Fatalf("写入监听源失败: %v", err)
	}
	return out
}

func TestWatchSourceCRUDRoundtrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	row := upsertWatch(t, s, WatchSource{
		ChannelID: -1001234, Kind: "supergroup", Username: "mychan", Title: "频道",
		Status: WatchApproved, Enabled: true, AddedBy: 7, BotID: 42, BotUsername: "watcher_bot",
	})
	if row.CreatedAt == 0 || row.UpdatedAt == 0 {
		t.Fatal("时间戳应自动填充")
	}
	got, err := s.GetWatchSource(ctx, -1001234)
	if err != nil || got.Username != "mychan" || got.Status != WatchApproved {
		t.Fatalf("读取应回写入场: %+v err=%v", got, err)
	}
	if got.Kind != "supergroup" || got.BotID != 42 || got.BotUsername != "watcher_bot" {
		t.Fatalf("类型与受理 bot 应回写: %+v", got)
	}
	// 重复 upsert 幂等刷新，保留 created_at
	row2 := upsertWatch(t, s, WatchSource{
		ChannelID: -1001234, Title: "新标题", Status: WatchApproved, Enabled: false, AddedBy: 7,
	})
	if row2.CreatedAt != row.CreatedAt {
		t.Fatalf("重复 upsert 不应重置 created_at: %d vs %d", row2.CreatedAt, row.CreatedAt)
	}
	// 重复 upsert 是整行覆盖语义（服务层总是带全量字段）：缺省字段被清空
	if row2.Username != "" || row2.Title != "新标题" || row2.Enabled {
		t.Fatalf("重复 upsert 应整行覆盖: %+v", row2)
	}
	rows, err := s.ListWatchSources(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("列表应含一行: %v err=%v", rows, err)
	}
	removed, err := s.DeleteWatchSource(ctx, -1001234)
	if err != nil || removed.ChannelID != -1001234 {
		t.Fatalf("删除应返回被删行: %+v err=%v", removed, err)
	}
	if _, err := s.GetWatchSource(ctx, -1001234); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应 ErrNotFound: %v", err)
	}
}

func TestWatchSourceUpsertRejectsUnknownStatus(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.UpsertWatchSource(context.Background(), WatchSource{
		ChannelID: -1001, Status: "bogus",
	}); err == nil {
		t.Fatal("非法状态应拒绝写入")
	}
}

func TestWatchSourceReviewTransitions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	upsertWatch(t, s, WatchSource{ChannelID: -1001, Status: WatchPending, AddedBy: 7})
	// 非 pending 目标状态拒绝
	if _, err := s.ReviewWatchSource(ctx, -1001, WatchPending, "admin"); err == nil {
		t.Fatal("审批目标状态只能是 approved/rejected")
	}
	approved, err := s.ReviewWatchSource(ctx, -1001, WatchApproved, "admin-hash")
	if err != nil || approved.Status != WatchApproved || approved.ReviewedBy != "admin-hash" {
		t.Fatalf("审批应流转 pending→approved: %+v err=%v", approved, err)
	}
	// 重复审批（已非 pending）拒绝，防覆盖既有结论
	if _, err := s.ReviewWatchSource(ctx, -1001, WatchRejected, "admin-hash"); err == nil {
		t.Fatal("已审行应拒绝再次审批")
	}
}

func TestWatchSourceCountFilters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	upsertWatch(t, s, WatchSource{ChannelID: -1001, Status: WatchPending, AddedBy: 7})
	upsertWatch(t, s, WatchSource{ChannelID: -1002, Status: WatchApproved, AddedBy: 7})
	upsertWatch(t, s, WatchSource{ChannelID: -1003, Status: WatchApproved, AddedBy: 9})
	upsertWatch(t, s, WatchSource{ChannelID: -1004, Status: WatchRejected, AddedBy: 7})
	upsertWatch(t, s, WatchSource{ChannelID: -1005, Status: WatchApproved, AddedBy: 0})
	active := []string{WatchPending, WatchApproved}
	if n, err := s.CountWatchSources(ctx, active); err != nil || n != 4 {
		t.Fatalf("生效+待审批应计 4: n=%d err=%v", n, err)
	}
	if n, err := s.CountWatchSources(ctx, nil); err != nil || n != 5 {
		t.Fatalf("全量应计 5: n=%d err=%v", n, err)
	}
	if n, err := s.CountWatchSourcesByUser(ctx, 7, active); err != nil || n != 2 {
		t.Fatalf("用户 7 生效应计 2（rejected 不计）: n=%d err=%v", n, err)
	}
	if n, err := s.CountWatchSourcesByUser(ctx, 0, active); err != nil || n != 1 {
		t.Fatalf("管理员行应计 1: n=%d err=%v", n, err)
	}
}

func TestWatchSourceWithUserJoin(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, User{ID: 7, Status: UserEnabled, DisplayName: "张三"}); err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	upsertWatch(t, s, WatchSource{ChannelID: -1001, Status: WatchPending, AddedBy: 7})
	upsertWatch(t, s, WatchSource{ChannelID: -1002, Status: WatchApproved, AddedBy: 0})
	rows, err := s.ListWatchSourcesWithUser(ctx)
	if err != nil || len(rows) != 2 {
		t.Fatalf("联表应返回两行: %v err=%v", rows, err)
	}
	var byUser, byAdmin WatchSourceWithUser
	for _, r := range rows {
		if r.AddedBy == 7 {
			byUser = r
		} else {
			byAdmin = r
		}
	}
	if byUser.UserDisplayName != "张三" {
		t.Fatalf("申请人资料应联出: %+v", byUser)
	}
	if byAdmin.UserDisplayName != "" || byAdmin.UserUsername != "" {
		t.Fatalf("管理员行资料应为空: %+v", byAdmin)
	}
}

func TestDumpEntryStatsByKeys(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for i, key := range []string{"-1001234", "mychan", "-1005678"} {
		if _, err := s.InsertDumpEntry(ctx, DumpEntry{
			ChannelKey: key, MessageID: 10 + i, DumpIDs: []int{100 + i},
			DumpChannelID: -100555,
		}); err != nil {
			t.Fatalf("预置条目失败: %v", err)
		}
	}
	// 再给 -1001234 补一条更新的，验证 MAX(created_at)
	time.Sleep(2 * time.Millisecond) // 保证毫秒时间戳递增
	if _, err := s.InsertDumpEntry(ctx, DumpEntry{
		ChannelKey: "-1001234", MessageID: 99, DumpIDs: []int{199},
		DumpChannelID: -100555,
	}); err != nil {
		t.Fatalf("预置条目失败: %v", err)
	}
	stats, err := s.DumpEntryStatsByKeys(ctx, []string{"-1001234", "mychan"})
	if err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("应聚出两键: %+v", stats)
	}
	if stats["-1001234"].Count != 2 || stats["-1001234"].LastAt <= stats["mychan"].LastAt {
		t.Fatalf("-1001234 应计 2 条且时间最新: %+v", stats["-1001234"])
	}
	if stats["mychan"].Count != 1 {
		t.Fatalf("mychan 应计 1 条: %+v", stats["mychan"])
	}
	if empty, err := s.DumpEntryStatsByKeys(ctx, nil); err != nil || len(empty) != 0 {
		t.Fatalf("空键集应返回空映射: %+v err=%v", empty, err)
	}
}
