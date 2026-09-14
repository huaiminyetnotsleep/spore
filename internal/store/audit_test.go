package store

import (
	"context"
	"testing"
)

func TestAuditAppendAndList(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	entries := []AuditEntry{
		{At: 1000, Actor: "admin", Action: "user.enable", Target: "user:1",
			BeforeJSON: `{"status":"pending"}`, AfterJSON: `{"status":"enabled"}`},
		{At: 2000, Actor: "system", Action: "backup.export"},
		{At: 3000, Actor: "admin", Action: "quota.reset", Target: "user:2"},
	}
	for _, e := range entries {
		if err := s.AppendAudit(ctx, e); err != nil {
			t.Fatalf("写审计失败: %v", err)
		}
	}
	if err := s.AppendAudit(ctx, AuditEntry{At: 1}); err == nil {
		t.Fatal("空 action 应被拒绝")
	}

	got, err := s.ListAudit(ctx, 10, 0)
	if err != nil {
		t.Fatalf("读审计失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("应有 3 条，得到 %d", len(got))
	}
	// 时间倒序
	if got[0].At != 3000 || got[2].At != 1000 {
		t.Fatalf("应按时间倒序，得到 %d..%d", got[2].At, got[0].At)
	}
	first := got[2]
	if first.Actor != "admin" || first.Target != "user:1" ||
		first.BeforeJSON != `{"status":"pending"}` || first.AfterJSON != `{"status":"enabled"}` {
		t.Errorf("审计字段应往返一致: %+v", first)
	}
	// 可空列读回应为空串而非 NULL 报错
	if got[1].Target != "" {
		t.Errorf("可空列应归一空串，得到 %q", got[1].Target)
	}

	// 分页
	page, _ := s.ListAudit(ctx, 2, 0)
	if len(page) != 2 {
		t.Errorf("limit=2 应返回 2 条，得到 %d", len(page))
	}
	page, _ = s.ListAudit(ctx, 2, 2)
	if len(page) != 1 {
		t.Errorf("offset=2 应剩 1 条，得到 %d", len(page))
	}

	// 总数与分页同一口径（审计页分页信封用）
	n, err := s.CountAudit(ctx)
	if err != nil {
		t.Fatalf("统计审计总数失败: %v", err)
	}
	if n != 3 {
		t.Errorf("审计总数应为 3，得到 %d", n)
	}
}

func TestAuditFilterAndDelete(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, e := range []AuditEntry{
		{At: 1000, Actor: "admin", Action: "a"},
		{At: 2000, Actor: "admin", Action: "b"},
		{At: 3000, Actor: "admin", Action: "c"},
	} {
		if err := s.AppendAudit(ctx, e); err != nil {
			t.Fatalf("写审计失败: %v", err)
		}
	}

	epochCount, err := s.CountAuditFiltered(ctx, AuditFilter{Until: 0, HasUntil: true})
	if err != nil || epochCount != 0 {
		t.Fatalf("明确设置 epoch 上界时应返回 0 条，得到 %d err=%v", epochCount, err)
	}

	filter := AuditFilter{Since: 2000, Until: 3000}
	n, err := s.CountAuditFiltered(ctx, filter)
	if err != nil || n != 1 {
		t.Fatalf("过滤总数应为 1，得到 %d err=%v", n, err)
	}
	items, err := s.ListAuditFiltered(ctx, filter, 10, 0)
	if err != nil || len(items) != 1 || items[0].Action != "b" {
		t.Fatalf("过滤列表不符: %+v err=%v", items, err)
	}

	all, err := s.ListAudit(ctx, 10, 0)
	if err != nil {
		t.Fatalf("查询全部审计失败: %v", err)
	}
	deleted, err := s.DeleteAuditByIDs(ctx, []int64{all[0].ID, all[0].ID, 999999})
	if err != nil || deleted != 1 {
		t.Fatalf("按 ID 删除应命中 1 条，得到 %d err=%v", deleted, err)
	}
	deleted, err = s.DeleteAllAudit(ctx)
	if err != nil || deleted != 2 {
		t.Fatalf("全清应删除剩余 2 条，得到 %d err=%v", deleted, err)
	}
}
