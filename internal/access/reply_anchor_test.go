package access

import (
	"context"
	"errors"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// TestRecordStatusMessageAndResolve 覆盖占位坐标落库与反查：命中返回请求行；
// 坐标未命中、跨 bot、归属不匹配（他人请求）统一 ErrNotFound。
func TestRecordStatusMessageAndResolve(t *testing.T) {
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	ctx := context.Background()
	mustEnabledUser(t, st, 1)
	mustEnabledUser(t, st, 2)
	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(7)})

	if err := svc.RecordStatusMessage(ctx, 9, 1, 501, d.RequestID); err != nil {
		t.Fatalf("占位坐标落库失败: %v", err)
	}
	m, r, err := svc.ResolveOwnSentMessage(ctx, 1, 9, 1, 501)
	if err != nil || r.ID != d.RequestID || m.Kind != store.SentKindStatus {
		t.Fatalf("反查应命中本人请求: m=%+v r=%+v err=%v", m, r, err)
	}
	if r.Status != store.RequestQueued {
		t.Fatalf("刚提交的请求应为 queued，得到 %s", r.Status)
	}

	// 坐标未命中
	if _, _, err := svc.ResolveOwnSentMessage(ctx, 1, 9, 1, 999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("未命中应返回 ErrNotFound，得到 %v", err)
	}
	// 跨 bot：坐标 bot 私有
	if _, _, err := svc.ResolveOwnSentMessage(ctx, 1, 8, 1, 501); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("跨 bot 查询应返回 ErrNotFound，得到 %v", err)
	}
	// 归属不匹配：与未命中同形，不泄露他人请求
	if _, _, err := svc.ResolveOwnSentMessage(ctx, 2, 9, 1, 501); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("他人请求应返回 ErrNotFound，得到 %v", err)
	}
}

// TestCancelOwnByID 覆盖按 ID 取消：在途命中并落审计；终态竞态返回 0 与
// nil；他人请求 ErrNotFound。
func TestCancelOwnByID(t *testing.T) {
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	ctx := context.Background()
	mustEnabledUser(t, st, 1)
	mustEnabledUser(t, st, 2)
	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(7)})
	other := mustSubmit(t, svc, Submission{UserID: 2, ChatID: 2, Ref: pubRef(8)})

	n, err := svc.CancelOwnByID(ctx, 1, d.RequestID)
	if err != nil || n != 1 {
		t.Fatalf("在途请求应取消成功: n=%d err=%v", n, err)
	}
	if got, _ := st.GetRequest(ctx, d.RequestID); got.Status != store.RequestCancelled {
		t.Fatalf("请求应已取消: %+v", got)
	}
	// 审计 actor 记 user:<id>
	audits, err := st.ListAudit(ctx, 5, 0)
	if err != nil || len(audits) != 1 || audits[0].Actor != "user:1" {
		t.Fatalf("取消应写 user:1 审计: n=%d err=%v", len(audits), err)
	}

	// 终态竞态：已取消再取消返回 0 与 nil（命令层按状态出友好文案）
	if n, err := svc.CancelOwnByID(ctx, 1, d.RequestID); err != nil || n != 0 {
		t.Fatalf("终态竞态应返回 0/nil: n=%d err=%v", n, err)
	}
	// 他人请求
	if _, err := svc.CancelOwnByID(ctx, 1, other.RequestID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("他人请求应返回 ErrNotFound，得到 %v", err)
	}
}

// TestMarkOwnRequestPin 覆盖 /pin 回复在途任务的补标路径。
func TestMarkOwnRequestPin(t *testing.T) {
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	ctx := context.Background()
	mustEnabledUser(t, st, 1)
	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(7)})

	if ok, err := svc.MarkOwnRequestPin(ctx, 1, d.RequestID); err != nil || !ok {
		t.Fatalf("在途请求应补标成功: ok=%v err=%v", ok, err)
	}
	if r, _ := st.GetRequest(ctx, d.RequestID); !r.Pin {
		t.Fatalf("补标后 pin 应为 true: %+v", r)
	}
	// 终态后不再命中
	if err := svc.Cancel(ctx, "admin", d.RequestID); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if ok, err := svc.MarkOwnRequestPin(ctx, 1, d.RequestID); err != nil || ok {
		t.Fatalf("终态请求不应补标: ok=%v err=%v", ok, err)
	}
}
