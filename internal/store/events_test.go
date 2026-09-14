package store

import (
	"context"
	"errors"
	"testing"
)

func TestEventUpsertDedup(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	first := Event{
		Key: "mtproto.session_invalid", Severity: "error",
		Message: "会话失效", FirstAt: 1000, LastAt: 1000,
	}
	if err := s.UpsertEvent(ctx, first); err != nil {
		t.Fatalf("首次写事件失败: %v", err)
	}
	// 同 key 再发生：合并 count、刷新 last_at 与文案
	again := Event{
		Key: "mtproto.session_invalid", Severity: "error",
		Message: "会话再次失效", LastAt: 5000,
	}
	if err := s.UpsertEvent(ctx, again); err != nil {
		t.Fatalf("合并事件失败: %v", err)
	}

	events, err := s.ListEvents(ctx)
	if err != nil {
		t.Fatalf("列事件失败: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("同 key 应合并为 1 条，得到 %d", len(events))
	}
	e := events[0]
	if e.Count != 2 {
		t.Errorf("count 应合并为 2，得到 %d", e.Count)
	}
	if e.FirstAt != 1000 {
		t.Errorf("first_at 应保持首次时间，得到 %d", e.FirstAt)
	}
	if e.LastAt != 5000 {
		t.Errorf("last_at 应刷新为 5000，得到 %d", e.LastAt)
	}
	if e.Message != "会话再次失效" {
		t.Errorf("message 应更新，得到 %q", e.Message)
	}
	if e.Status != EventOpen {
		t.Errorf("新事件应为 open，得到 %s", e.Status)
	}
	if e.LastNotifiedAt != 0 {
		t.Errorf("未通知过应为 0，得到 %d", e.LastNotifiedAt)
	}

	// 不同 key 独立成行
	if err := s.UpsertEvent(ctx, Event{Key: "db.error", Severity: "warn", Message: "x"}); err != nil {
		t.Fatalf("写第二类事件失败: %v", err)
	}
	if events, _ = s.ListEvents(ctx); len(events) != 2 {
		t.Fatalf("不同 key 应 2 条，得到 %d", len(events))
	}

	// 空 key 拒绝
	if err := s.UpsertEvent(ctx, Event{}); err == nil {
		t.Fatal("空 key 应被拒绝")
	}
}

func TestEventResolveAndReopen(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.UpsertEvent(ctx, Event{Key: "botapi.down", Severity: "warn", Message: "x"}); err != nil {
		t.Fatalf("写事件失败: %v", err)
	}
	events, _ := s.ListEvents(ctx)

	// 通知过一次（模拟冷却窗口内已推送）
	if err := s.MarkEventNotified(ctx, "botapi.down", 9000); err != nil {
		t.Fatalf("记录通知时间失败: %v", err)
	}

	// resolve
	if err := s.ResolveEvent(ctx, events[0].ID); err != nil {
		t.Fatalf("解决事件失败: %v", err)
	}
	events, _ = s.ListEvents(ctx)
	if events[0].Status != EventResolved {
		t.Fatalf("解决后应为 resolved，得到 %s", events[0].Status)
	}
	if err := s.ResolveEvent(ctx, 404); !errors.Is(err, ErrNotFound) {
		t.Fatalf("解决不存在的事件应返回 ErrNotFound，得到 %v", err)
	}

	// 恢复后再次发生：重新置为 open，count 继续，且 last_notified_at 清零
	// （旧通知的冷却期不得抑制"恢复后再次发生"的新通知）
	if err := s.UpsertEvent(ctx, Event{Key: "botapi.down", Severity: "warn", Message: "y"}); err != nil {
		t.Fatalf("再次写事件失败: %v", err)
	}
	events, _ = s.ListEvents(ctx)
	if events[0].Status != EventOpen || events[0].Count != 2 {
		t.Fatalf("再次发生应重开并累计，得到 status=%s count=%d", events[0].Status, events[0].Count)
	}
	if events[0].LastNotifiedAt != 0 {
		t.Fatalf("重开应清空 last_notified_at，得到 %d", events[0].LastNotifiedAt)
	}

	// 未重开场景：已 open 且通知过的事件再次合并时通知时间保持不变
	if err := s.MarkEventNotified(ctx, "botapi.down", 20000); err != nil {
		t.Fatalf("记录通知时间失败: %v", err)
	}
	if err := s.UpsertEvent(ctx, Event{Key: "botapi.down", Severity: "warn", Message: "z"}); err != nil {
		t.Fatalf("合并事件失败: %v", err)
	}
	events, _ = s.ListEvents(ctx)
	if events[0].LastNotifiedAt != 20000 {
		t.Fatalf("open 状态合并应保留 last_notified_at，得到 %d", events[0].LastNotifiedAt)
	}
}

func TestGetEvent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.GetEvent(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的 key 应返回 ErrNotFound，得到 %v", err)
	}
	if err := s.UpsertEvent(ctx, Event{Key: "k", Severity: "warn", Message: "x"}); err != nil {
		t.Fatalf("写事件失败: %v", err)
	}
	e, err := s.GetEvent(ctx, "k")
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if e.Key != "k" || e.Severity != "warn" || e.Message != "x" || e.Status != EventOpen {
		t.Fatalf("事件内容不符: %+v", e)
	}
}

func TestMarkEventNotified(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.MarkEventNotified(ctx, "absent", 1000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的事件应返回 ErrNotFound，得到 %v", err)
	}
	if err := s.UpsertEvent(ctx, Event{Key: "k", Severity: "warn", Message: "x"}); err != nil {
		t.Fatalf("写事件失败: %v", err)
	}
	if err := s.MarkEventNotified(ctx, "k", 12345); err != nil {
		t.Fatalf("记录通知时间失败: %v", err)
	}
	e, _ := s.GetEvent(ctx, "k")
	if e.LastNotifiedAt != 12345 {
		t.Fatalf("last_notified_at 应为 12345，得到 %d", e.LastNotifiedAt)
	}
}

func TestResolveEventByKey(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.ResolveEventByKey(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的事件应返回 ErrNotFound，得到 %v", err)
	}
	if err := s.UpsertEvent(ctx, Event{Key: "mtproto.session_offline", Severity: "error", Message: "x"}); err != nil {
		t.Fatalf("写事件失败: %v", err)
	}
	if err := s.ResolveEventByKey(ctx, "mtproto.session_offline"); err != nil {
		t.Fatalf("按 key 解决事件失败: %v", err)
	}
	e, _ := s.GetEvent(ctx, "mtproto.session_offline")
	if e.Status != EventResolved {
		t.Fatalf("应为 resolved，得到 %s", e.Status)
	}
}
