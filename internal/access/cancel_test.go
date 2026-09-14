package access

import (
	"context"
	"errors"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func TestCancelRequestAuditsAndKeepsQuota(t *testing.T) {
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	ctx := context.Background()
	mustEnabledUser(t, st, 1)
	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(7)})

	if err := svc.Cancel(ctx, "admin", d.RequestID); err != nil {
		t.Fatalf("取消请求失败: %v", err)
	}
	got, err := st.GetRequest(ctx, d.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.RequestCancelled || got.ErrorCode != string(apperr.CodeRequestCancelled) {
		t.Fatalf("取消状态不对: %+v", got)
	}
	usage, err := st.GetUsage(ctx, 1, baseTime.In(defaultLoc).Format(dayFormat))
	if err != nil || usage.Used != 1 {
		t.Fatalf("取消不应返还额度: %+v err=%v", usage, err)
	}
	audits, err := st.ListAudit(ctx, 10, 0)
	if err != nil || len(audits) != 1 {
		t.Fatalf("取消应写一条审计: %v err=%v", len(audits), err)
	}
	if audits[0].Action != "request.cancel" || audits[0].Actor != "admin" {
		t.Fatalf("取消审计不对: %+v", audits[0])
	}
	err = svc.Cancel(ctx, "admin", d.RequestID)
	if err == nil {
		t.Fatal("重复取消应被拒绝")
	}
	if code := apperr.From(err).Code; code != apperr.CodeStoreConstraint {
		t.Fatalf("重复取消应为 STORE_CONSTRAINT，得到 %v", err)
	}
}

func TestCancelledRequestCanBeDeletedAsTerminal(t *testing.T) {
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	mustEnabledUser(t, st, 1)
	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(8)})
	if err := svc.Cancel(context.Background(), "admin", d.RequestID); err != nil {
		t.Fatalf("取消请求失败: %v", err)
	}
	if err := svc.DeleteRequest(context.Background(), "admin", d.RequestID); err != nil {
		t.Fatalf("已取消终态应可按终态规则删除: %v", err)
	}
	if _, err := st.GetRequest(context.Background(), d.RequestID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("删除后请求应不存在，得到 err=%v", err)
	}
}

func TestCancelOwnByLink(t *testing.T) {
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	ctx := context.Background()
	mustEnabledUser(t, st, 1)
	mustEnabledUser(t, st, 2)

	// 自己名下同链接两条在途：一条 queued（正常提交），一条 processing（直建后标记）
	queued := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(7)})
	processing, err := st.CreateRequest(ctx, store.Request{UserID: 1, ChannelKey: "example_channel", MessageID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.MarkRequestStarted(ctx, processing.ID, newClock(baseTime).Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// 他人同链接在途、自己同链接已结束：都不应被取消
	other := mustSubmit(t, svc, Submission{UserID: 2, ChatID: 2, Ref: pubRef(7)})
	finished, err := st.CreateRequest(ctx, store.Request{UserID: 1, ChannelKey: "example_channel", MessageID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRequest(ctx, finished.ID, store.RequestResult{Status: store.RequestSucceeded}); err != nil {
		t.Fatal(err)
	}

	n, err := svc.CancelOwnByLink(ctx, 1, pubRef(7))
	if err != nil {
		t.Fatalf("自助取消失败: %v", err)
	}
	if n != 2 {
		t.Fatalf("应取消 2 条在途请求，得到 %d", n)
	}
	for _, id := range []int64{queued.RequestID, processing.ID} {
		got, err := st.GetRequest(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != store.RequestCancelled {
			t.Errorf("请求 %d 应为 cancelled，得到 %s", id, got.Status)
		}
	}
	for _, id := range []int64{other.RequestID, finished.ID} {
		got, err := st.GetRequest(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == store.RequestCancelled {
			t.Errorf("请求 %d 不应被他人取消波及，得到 %s", id, got.Status)
		}
	}

	// 审计 actor 记为 user:<id>，与管理员取消区分
	audits, err := st.ListAudit(ctx, 10, 0)
	if err != nil || len(audits) != 2 {
		t.Fatalf("应写 2 条取消审计: %d err=%v", len(audits), err)
	}
	for _, a := range audits {
		if a.Action != "request.cancel" || a.Actor != "user:1" {
			t.Errorf("自助取消审计不对: %+v", a)
		}
	}

	// 无匹配或已全部取消：返回 0 且不报错
	if n, err = svc.CancelOwnByLink(ctx, 1, pubRef(7)); err != nil || n != 0 {
		t.Fatalf("无在途请求应返回 0/nil，得到 %d err=%v", n, err)
	}
}

func TestCancelManyReturnsPerItemResults(t *testing.T) {
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	ctx := context.Background()
	mustEnabledUser(t, st, 1)
	first := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(1)})
	second, err := st.CreateRequest(ctx, store.Request{UserID: 1, ChannelKey: "example", MessageID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.FinishRequest(ctx, second.ID, store.RequestResult{Status: store.RequestSucceeded}); err != nil {
		t.Fatal(err)
	}

	got := svc.CancelMany(ctx, "admin", []int64{first.RequestID, second.ID, 404})
	if len(got) != 3 {
		t.Fatalf("批量结果应有 3 条，得到 %+v", got)
	}
	want := []string{"cancelled", "conflict", "not_found"}
	for i, item := range got {
		if item.Result != want[i] {
			t.Errorf("第 %d 条结果应为 %s，得到 %+v", i, want[i], item)
		}
	}
}
