package access

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// TestDeleteRequestRejectsUnfinished 锁定"仅终态可删"约束：删除永远不触碰
// 在途任务，因此删除路径无需清理实时进度（Registry）与占位消息——那些清理
// 全部由 worker 收尾负责。若该约束被放开，删除与取消的清理语义将不再等价。
func TestDeleteRequestRejectsUnfinished(t *testing.T) {
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	ctx := context.Background()
	mustEnabledUser(t, st, 1)
	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(9)})

	// queued：拒绝删除
	err := svc.DeleteRequest(ctx, "admin", d.RequestID)
	if apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatalf("queued 记录不可删除，得到 err=%v", err)
	}

	// processing：同样拒绝
	if err := st.MarkRequestStarted(ctx, d.RequestID, 1); err != nil {
		t.Fatal(err)
	}
	err = svc.DeleteRequest(ctx, "admin", d.RequestID)
	if apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatalf("processing 记录不可删除，得到 err=%v", err)
	}

	// 取消成终态后可删除，行消失
	if err := svc.Cancel(ctx, "admin", d.RequestID); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if err := svc.DeleteRequest(ctx, "admin", d.RequestID); err != nil {
		t.Fatalf("终态记录应可删除: %v", err)
	}
	if _, err := st.GetRequest(ctx, d.RequestID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("删除后请求应不存在，得到 err=%v", err)
	}
}

func TestDeleteAuditEntriesWritesCleanupAudit(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 1, clock.Now)
	ctx := context.Background()
	for _, action := range []string{"user.enable", "request.delete", "backup.export"} {
		if err := st.AppendAudit(ctx, auditEntry(action)); err != nil {
			t.Fatalf("写审计失败: %v", err)
		}
	}
	entries, err := st.ListAudit(ctx, 10, 0)
	if err != nil {
		t.Fatalf("查询审计失败: %v", err)
	}

	deleted, err := svc.DeleteAuditEntries(ctx, "admin", []int64{entries[0].ID, entries[1].ID})
	if err != nil || deleted != 2 {
		t.Fatalf("批量删除失败: deleted=%d err=%v", deleted, err)
	}
	remaining, err := st.ListAudit(ctx, 10, 0)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("删除后应剩原记录和清理审计: %+v err=%v", remaining, err)
	}
	if remaining[0].Action != "audit.delete" {
		t.Fatalf("最新记录应为 audit.delete，得到 %+v", remaining[0])
	}
	var after map[string]any
	if err := json.Unmarshal([]byte(remaining[0].AfterJSON), &after); err != nil {
		t.Fatalf("解析清理审计失败: %v", err)
	}
	if after["deleted"] != float64(2) || after["requested"] != float64(2) {
		t.Fatalf("清理审计计数不符: %+v", after)
	}
}

func TestClearAuditRetainsOnlyCleanupAudit(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 1, clock.Now)
	ctx := context.Background()
	for _, action := range []string{"a", "b"} {
		if err := st.AppendAudit(ctx, auditEntry(action)); err != nil {
			t.Fatalf("写审计失败: %v", err)
		}
	}

	deleted, err := svc.ClearAudit(ctx, "admin")
	if err != nil || deleted != 2 {
		t.Fatalf("清除审计失败: deleted=%d err=%v", deleted, err)
	}
	remaining, err := st.ListAudit(ctx, 10, 0)
	if err != nil || len(remaining) != 1 || remaining[0].Action != "audit.clear" {
		t.Fatalf("清除后应只剩 audit.clear: %+v err=%v", remaining, err)
	}
}

func auditEntry(action string) store.AuditEntry {
	return store.AuditEntry{At: baseTime.Add(-time.Hour).UnixMilli(), Actor: "admin", Action: action}
}
