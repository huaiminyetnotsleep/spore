package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestQueueCancelsActiveRequestWithCause(t *testing.T) {
	q := New(1)
	job := Job{ID: "job", RequestID: 42}
	if err := q.Enqueue(job); err != nil {
		t.Fatal(err)
	}

	root, stop := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		q.Run(root, 1, func(ctx context.Context, got Job) {
			if got.RequestID != job.RequestID {
				t.Errorf("处理的请求 ID 不对: %+v", got)
			}
			close(started)
			<-ctx.Done()
			if !IsRequestCancelled(ctx) || !errors.Is(context.Cause(ctx), ErrRequestCancelled) {
				t.Errorf("活动任务应收到请求取消 cause: %v", context.Cause(ctx))
			}
			close(done)
		}, func(context.Context, Job) {})
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("活动任务未启动")
	}
	if !q.CancelRequest(job.RequestID) {
		t.Fatal("取消应命中活动任务")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("活动任务未收到取消")
	}
	stop()
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("队列 worker 未退出")
	}
	if q.CancelRequest(job.RequestID) {
		t.Fatal("任务结束后不应保留活动取消函数")
	}
}

func TestQueueCancelQueuedRequestMissesActiveWorker(t *testing.T) {
	q := New(1)
	if err := q.Enqueue(Job{RequestID: 7}); err != nil {
		t.Fatal(err)
	}
	if q.CancelRequest(7) {
		t.Fatal("尚未出队的任务不应命中活动 worker")
	}
}

func TestQueueCancelPendingJobInvokesHandler(t *testing.T) {
	q := New(4)
	var mu sync.Mutex
	var hooked []Job
	q.SetPendingCancelHandler(func(j Job) {
		mu.Lock()
		defer mu.Unlock()
		hooked = append(hooked, j)
	})
	job := Job{ID: "job", ChatID: 7, StatusMsgID: 9, RequestID: 42}
	if err := q.Enqueue(job); err != nil {
		t.Fatal(err)
	}

	// 命中排队任务：返回 false（不命中活动 worker），但钩子立即收到任务
	if q.CancelRequest(42) {
		t.Fatal("排队任务不应命中活动 worker")
	}
	mu.Lock()
	fired := len(hooked)
	got := hooked
	mu.Unlock()
	if fired != 1 || got[0].ChatID != 7 || got[0].StatusMsgID != 9 {
		t.Fatalf("取消应触发一次钩子并携带任务信息: %+v", got)
	}

	// 重复取消：钩子至多触发一次（命中即从索引移除）
	if q.CancelRequest(42) {
		t.Fatal("重复取消不应命中任何目标")
	}
	mu.Lock()
	fired = len(hooked)
	mu.Unlock()
	if fired != 1 {
		t.Fatalf("重复取消不应再次触发钩子，得到 %d 次", fired)
	}

	// 无注册钩子 / RequestID 为 0：取消安全返回 false
	q2 := New(1)
	_ = q2.Enqueue(Job{RequestID: 0})
	if q2.CancelRequest(0) {
		t.Fatal("RequestID 为 0 不应命中")
	}
}

func TestQueueDequeueRemovesPendingIndex(t *testing.T) {
	q := New(4)
	q.SetPendingCancelHandler(func(Job) {
		t.Error("任务已出队，取消应走活动 worker 信号，不应触发排队钩子")
	})
	root, stop := context.WithCancel(context.Background())
	started := make(chan struct{})
	block := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Run(root, 1, func(_ context.Context, _ Job) {
			close(started)
			<-block
		}, func(context.Context, Job) {})
	}()
	if err := q.Enqueue(Job{ID: "job", RequestID: 42}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("任务未启动")
	}
	// 出队后取消：命中活动路径（返回 true），排队钩子不应触发
	if !q.CancelRequest(42) {
		t.Error("已出队任务的取消应命中活动 worker")
	}
	close(block)
	stop() // 先停 ctx 让 worker 退出循环，再等 Run 返回
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("队列 worker 未退出")
	}
}
