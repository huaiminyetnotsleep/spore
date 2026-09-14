package store

import (
	"context"
	"testing"
)

func TestUsageAccumulateByUserAndDay(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	// 同 (user, day) 多次累加
	for i := 0; i < 3; i++ {
		if err := s.IncrementUsage(ctx, u.ID, "2026-08-27", 1); err != nil {
			t.Fatalf("累加失败: %v", err)
		}
	}
	got, err := s.GetUsage(ctx, u.ID, "2026-08-27")
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Used != 3 || got.ResetCount != 0 {
		t.Fatalf("应累计 3 次、未重置，得到 %+v", got)
	}

	// 不同日各自独立分桶
	if err := s.IncrementUsage(ctx, u.ID, "2026-08-28", 1); err != nil {
		t.Fatalf("跨日累加失败: %v", err)
	}
	next, _ := s.GetUsage(ctx, u.ID, "2026-08-28")
	if next.Used != 1 {
		t.Fatalf("新的一天应从 1 开始，得到 %d", next.Used)
	}
	prev, _ := s.GetUsage(ctx, u.ID, "2026-08-27")
	if prev.Used != 3 {
		t.Fatalf("原分桶不受影响，得到 %d", prev.Used)
	}

	// 无记录的日返回零值
	missing, err := s.GetUsage(ctx, u.ID, "2026-01-01")
	if err != nil {
		t.Fatalf("无记录不应报错: %v", err)
	}
	if missing.Used != 0 || missing.Day != "2026-01-01" {
		t.Fatalf("无记录应返回零值，得到 %+v", missing)
	}
}

func TestUsageReset(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	for i := 0; i < 5; i++ {
		_ = s.IncrementUsage(ctx, u.ID, "2026-08-27", 1)
	}
	if err := s.ResetUsage(ctx, u.ID, "2026-08-27"); err != nil {
		t.Fatalf("重置失败: %v", err)
	}
	got, _ := s.GetUsage(ctx, u.ID, "2026-08-27")
	if got.Used != 0 || got.ResetCount != 1 {
		t.Fatalf("重置后应 used=0/reset_count=1，得到 %+v", got)
	}

	// 重置后可继续累加
	_ = s.IncrementUsage(ctx, u.ID, "2026-08-27", 1)
	got, _ = s.GetUsage(ctx, u.ID, "2026-08-27")
	if got.Used != 1 || got.ResetCount != 1 {
		t.Fatalf("重置后累加应从 1 开始且 reset_count 保持，得到 %+v", got)
	}

	// 重置不存在的日：幂等无副作用
	if err := s.ResetUsage(ctx, u.ID, "2020-01-01"); err != nil {
		t.Fatalf("重置不存在的日不应报错: %v", err)
	}
	if got, _ := s.GetUsage(ctx, u.ID, "2020-01-01"); got.Used != 0 || got.ResetCount != 0 {
		t.Fatalf("不存在的日不应产生行，得到 %+v", got)
	}
}

func TestIncrementUsageRejectsNegative(t *testing.T) {
	s := openTestStore(t)
	if err := s.IncrementUsage(context.Background(), 1, "2026-08-27", -1); err == nil {
		t.Fatal("负增量应被拒绝")
	}
}
