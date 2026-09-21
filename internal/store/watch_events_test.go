package store

import (
	"context"
	"testing"
)

func TestWatchEventsListAndStatsFilters(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 两个归属用户与两个源，覆盖按源/Bot/用户和时间过滤。
	if _, err := s.CreateUser(ctx, User{ID: 7, Status: UserEnabled, Username: "u7"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, User{ID: 8, Status: UserEnabled, Username: "u8"}); err != nil {
		t.Fatal(err)
	}
	upsertWatch(t, s, WatchSource{ChannelID: -1001, Status: WatchApproved, Enabled: true, AddedBy: 7})
	upsertWatch(t, s, WatchSource{ChannelID: -1002, Status: WatchApproved, Enabled: true, AddedBy: 8})

	events := []WatchEvent{
		{ChannelID: -1001, Title: "源一", MessageID: 11, MemberIDs: []int{11}, DumpIDs: []int{101}, BotID: 42, BotUsername: "b42", Path: WatchPathCopy, CreatedAt: 1000},
		{ChannelID: -1001, Title: "源一", MessageID: 12, MemberIDs: []int{12, 13}, RequestID: 99, BotID: 42, BotUsername: "b42", Path: WatchPathFallback, CreatedAt: 2000},
		{ChannelID: -1002, Title: "源二", MessageID: 21, MemberIDs: []int{21}, DumpIDs: []int{201}, BotID: 43, BotUsername: "b43", Path: WatchPathCopy, CreatedAt: 3000},
	}
	for _, event := range events {
		if _, err := s.InsertWatchEvent(ctx, event); err != nil {
			t.Fatalf("写事件失败: %v", err)
		}
	}

	rows, total, err := s.ListWatchEvents(ctx, WatchEventsQuery{ChannelID: -1001, Page: 1, PageSize: 1})
	if err != nil || total != 2 || len(rows) != 1 || rows[0].MessageID != 12 {
		t.Fatalf("分页/源筛选不符: total=%d rows=%+v err=%v", total, rows, err)
	}
	if rows[0].Path != WatchPathFallback || rows[0].RequestID != 99 || len(rows[0].MemberIDs) != 2 {
		t.Fatalf("JSON 字段/路径应正确回读: %+v", rows[0])
	}

	bySource, err := s.WatchStatsBySource(ctx, 1500, 3500, 0)
	if err != nil || len(bySource) != 2 {
		t.Fatalf("按源统计失败: %+v err=%v", bySource, err)
	}
	// -1001 在范围内仅 fallback 事件，但相册成员数=2。
	var source1 WatchSourceStat
	for _, row := range bySource {
		if row.ChannelID == -1001 {
			source1 = row
		}
	}
	if source1.Events != 1 || source1.Messages != 2 || source1.LastAt != 2000 {
		t.Fatalf("按源时间过滤不符: %+v", source1)
	}

	byBot, err := s.WatchStatsByBot(ctx, 0, 2500)
	if err != nil || len(byBot) != 1 || byBot[0].BotID != 42 || byBot[0].Events != 2 {
		t.Fatalf("按 Bot 时间过滤不符: %+v err=%v", byBot, err)
	}
	byUser, err := s.WatchStatsByUser(ctx, 0, 0, 43)
	if err != nil || len(byUser) != 1 || byUser[0].AddedBy != 8 || byUser[0].Events != 1 {
		t.Fatalf("按用户/Bot 过滤不符: %+v err=%v", byUser, err)
	}
}

func TestWatchEventRejectsInvalidPath(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.InsertWatchEvent(context.Background(), WatchEvent{
		ChannelID: -1001, MessageID: 1, Path: "unknown",
	}); err == nil {
		t.Fatal("非法 path 应拒绝")
	}
}

func TestListWatchEventsPathFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	upsertWatch(t, s, WatchSource{ChannelID: -1001, Status: WatchApproved, Enabled: true})
	for _, e := range []WatchEvent{
		{ChannelID: -1001, Title: "源一", MessageID: 11, MemberIDs: []int{11}, DumpIDs: []int{101}, BotID: 42, BotUsername: "b42", Path: WatchPathCopy, CreatedAt: 1000},
		{ChannelID: -1001, Title: "源一", MessageID: 12, MemberIDs: []int{12}, RequestID: 99, BotID: 42, BotUsername: "b42", Path: WatchPathFallback, CreatedAt: 2000},
	} {
		if _, err := s.InsertWatchEvent(ctx, e); err != nil {
			t.Fatalf("写入事件失败: %v", err)
		}
	}

	rows, total, err := s.ListWatchEvents(ctx, WatchEventsQuery{Path: WatchPathFallback, Page: 1, PageSize: 10})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].MessageID != 12 {
		t.Fatalf("按路径筛选应只命中 fallback: rows=%+v total=%d err=%v", rows, total, err)
	}
	if _, _, err := s.ListWatchEvents(ctx, WatchEventsQuery{Path: "bogus", Page: 1, PageSize: 10}); err == nil {
		t.Fatal("非法路径筛选应报受控错误")
	}
}

func TestDeleteWatchEventsBatch(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	upsertWatch(t, s, WatchSource{ChannelID: -1001, Status: WatchApproved, Enabled: true})
	ids := make([]int64, 0, 3)
	for i := int64(1); i <= 3; i++ {
		e, err := s.InsertWatchEvent(ctx, WatchEvent{
			ChannelID: -1001, Title: "源一", MessageID: int(10 + i), MemberIDs: []int{int(10 + i)},
			BotID: 42, BotUsername: "b42", Path: WatchPathCopy,
		})
		if err != nil {
			t.Fatalf("写入事件失败: %v", err)
		}
		ids = append(ids, e.ID)
	}

	deleted, err := s.DeleteWatchEvents(ctx, []int64{ids[0], ids[2], 99999})
	if err != nil || deleted != 2 {
		t.Fatalf("应删除 2 行（不存在不计入）: deleted=%d err=%v", deleted, err)
	}
	_, total, err := s.ListWatchEvents(ctx, WatchEventsQuery{Page: 1, PageSize: 10})
	if err != nil || total != 1 {
		t.Fatalf("删除后应剩 1 行: total=%d err=%v", total, err)
	}
	if n, err := s.DeleteWatchEvents(ctx, nil); err != nil || n != 0 {
		t.Fatalf("空列表应幂等: n=%d err=%v", n, err)
	}
}
