package access

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// newFailedRequest 经完整提交路径构造一条 failed 请求（模拟任务执行失败），
// 保证 attempt / 时间戳 / 额度扣减与真实链路一致。
func newFailedRequest(t *testing.T, s *Service, st *store.Store, msgID int) store.Request {
	t.Helper()
	d := mustSubmit(t, s, Submission{UserID: 1, ChatID: 1, Ref: pubRef(msgID)})
	r, err := st.GetRequest(context.Background(), d.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if err := st.FinishRequest(context.Background(), r.ID, store.RequestResult{
		Status:    store.RequestFailed,
		ErrorCode: "CHANNEL_NOT_ACCESSIBLE",
	}); err != nil {
		t.Fatalf("置为失败失败: %v", err)
	}
	r.Status = store.RequestFailed
	r.ErrorCode = "CHANNEL_NOT_ACCESSIBLE"
	return r
}

// mustEnabledUser 建一个启用用户（默认限额）。
func mustEnabledUser(t *testing.T, st *store.Store, id int64) {
	t.Helper()
	if _, err := st.CreateUser(context.Background(), store.User{ID: id, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
}

func assertRetryCode(t *testing.T, err error, want apperr.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("期望重试被拒绝（%s），实际成功", want)
	}
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != want {
		t.Fatalf("期望错误码 %s，得到 %v", want, err)
	}
}

func TestRetrySuccess(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 8, clock.Now)
	jobs := startWorkers(t, q)
	mustEnabledUser(t, st, 1)

	r := newFailedRequest(t, svc, st, 7)
	// 提交已扣过一次额度
	usage, err := st.GetUsage(context.Background(), 1, "2026-08-27")
	if err != nil || usage.Used != 1 {
		t.Fatalf("前置：当日已用应为 1，得到 %+v err=%v", usage, err)
	}

	if err := svc.Retry(context.Background(), "admin", r.ID); err != nil {
		t.Fatalf("重试不应失败: %v", err)
	}

	got, err := st.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if got.Status != store.RequestQueued || got.Attempt != 2 {
		t.Fatalf("重试后应 queued/attempt=2（复用同一行）: %+v", got)
	}
	if got.ErrorCode != "" || got.StartedAt != 0 || got.FinishedAt != 0 {
		t.Errorf("重试应清空错误与阶段时间: %+v", got)
	}
	if got.QueuedAt != baseTime.UnixMilli() {
		t.Errorf("重试应刷新 queued_at 为当前时钟，得到 %d", got.QueuedAt)
	}

	// 不扣减额度
	usage, _ = st.GetUsage(context.Background(), 1, "2026-08-27")
	if usage.Used != 1 {
		t.Errorf("重试不应扣减额度，已用应为 1，得到 %d", usage.Used)
	}

	// 任务已重新入队并携带请求关联
	job := waitJob(t, jobs)
	if job.RequestID != r.ID || job.UserID != 1 || job.ChatID != 1 {
		t.Errorf("重试任务字段不对: %+v", job)
	}
	if job.Ref.Username != "example_channel" || job.Ref.MessageID != 7 {
		t.Errorf("重试任务应重建来源链接: %+v", job.Ref)
	}

	// 审计已写入
	audit, err := st.ListAudit(context.Background(), 1, 0)
	if err != nil || len(audit) != 1 {
		t.Fatalf("应有一条审计记录，得到 %d err=%v", len(audit), err)
	}
	if audit[0].Action != "request.retry" || audit[0].Actor != "admin" || audit[0].Target != "request:1" {
		t.Errorf("审计字段不对: %+v", audit[0])
	}
}

func TestRetryRejectBranches(t *testing.T) {
	t.Run("超次数", func(t *testing.T) {
		svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
		mustEnabledUser(t, st, 1)
		r := newFailedRequest(t, svc, st, 7)
		// 两次重试把 attempt 推到 3，第三次应被拒
		if err := svc.Retry(context.Background(), "admin", r.ID); err != nil {
			t.Fatalf("第 2 次尝试不应被拒: %v", err)
		}
		if err := st.FinishRequest(context.Background(), r.ID, store.RequestResult{
			Status: store.RequestFailed, ErrorCode: "CHANNEL_NOT_ACCESSIBLE"}); err != nil {
			t.Fatalf("置为失败失败: %v", err)
		}
		if err := svc.Retry(context.Background(), "admin", r.ID); err != nil {
			t.Fatalf("第 3 次尝试不应被拒: %v", err)
		}
		if err := st.FinishRequest(context.Background(), r.ID, store.RequestResult{
			Status: store.RequestFailed, ErrorCode: "CHANNEL_NOT_ACCESSIBLE"}); err != nil {
			t.Fatalf("置为失败失败: %v", err)
		}
		assertRetryCode(t, svc.Retry(context.Background(), "admin", r.ID), apperr.CodeRetryExhausted)
		got, _ := st.GetRequest(context.Background(), r.ID)
		if got.Attempt != 3 || got.Status != store.RequestFailed {
			t.Errorf("被拒后行应保持 failed/attempt=3: %+v", got)
		}
	})

	t.Run("用户禁用", func(t *testing.T) {
		svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
		mustEnabledUser(t, st, 1)
		r := newFailedRequest(t, svc, st, 7)
		if _, err := st.UpdateUserStatus(context.Background(), 1, store.UserDisabled); err != nil {
			t.Fatalf("禁用用户失败: %v", err)
		}
		assertRetryCode(t, svc.Retry(context.Background(), "admin", r.ID), apperr.CodeUserDisabled)
		got, _ := st.GetRequest(context.Background(), r.ID)
		if got.Status != store.RequestFailed || got.Attempt != 1 {
			t.Errorf("被拒后行不应变化: %+v", got)
		}
	})

	t.Run("队列满", func(t *testing.T) {
		// 容量 1 且无 worker：提交路径留下的任务即占满，Full() 稳定为真
		svc, st, q := newTestService(t, 1, newClock(baseTime).Now)
		mustEnabledUser(t, st, 1)
		r := newFailedRequest(t, svc, st, 7)
		if !q.Full() {
			t.Fatal("前置失败：队列应已被提交路径的任务占满")
		}
		assertRetryCode(t, svc.Retry(context.Background(), "admin", r.ID), apperr.CodeQueueFull)
		got, _ := st.GetRequest(context.Background(), r.ID)
		if got.Status != store.RequestFailed || got.Attempt != 1 {
			t.Errorf("队列满被拒后行不应变化: %+v", got)
		}
	})

	t.Run("状态非 failed", func(t *testing.T) {
		svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
		mustEnabledUser(t, st, 1)
		d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(7)})
		if err := st.FinishRequest(context.Background(), d.RequestID, store.RequestResult{
			Status: store.RequestSucceeded}); err != nil {
			t.Fatalf("置为成功失败: %v", err)
		}
		assertRetryCode(t, svc.Retry(context.Background(), "admin", d.RequestID), apperr.CodeStoreConstraint)
	})

	t.Run("请求不存在", func(t *testing.T) {
		svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
		mustEnabledUser(t, st, 1)
		if err := svc.Retry(context.Background(), "admin", 404); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("应返回 ErrNotFound，得到 %v", err)
		}
	})
}

// halfFullQueue 模拟"预检未满、入队失败"的竞态队列。
type halfFullQueue struct {
	delegate Enqueuer
	fail     bool
}

func (h *halfFullQueue) Full() bool { return false }
func (h *halfFullQueue) Enqueue(job queue.Job) error {
	if h.fail {
		return queue.ErrBusy
	}
	return h.delegate.Enqueue(job)
}

func TestRetryEnqueueFailureFinishesRow(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 8, clock.Now)
	mustEnabledUser(t, st, 1)
	r := newFailedRequest(t, svc, st, 9)

	// 替换服务队列为竞态桩：事务内 Full() 通过，提交后 Enqueue 失败
	q2 := queue.New(8)
	stub := &halfFullQueue{delegate: q2, fail: true}
	svc.queue = stub

	// 入队失败即返回 QUEUE_FULL 错误（提交后竞态分支）
	assertRetryCode(t, svc.Retry(context.Background(), "admin", r.ID), apperr.CodeQueueFull)

	got, err := st.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if got.Status != store.RequestFailed || got.ErrorCode != "QUEUE_FULL" {
		t.Fatalf("竞态收尾应落 failed(QUEUE_FULL): %+v", got)
	}
	if got.Attempt != 2 {
		t.Errorf("入队失败前 attempt 已递增为 2: %+v", got)
	}
}

func TestRefFromRequest(t *testing.T) {
	pub := store.Request{SourceKind: store.SourcePublic, ChannelKey: "example_channel", MessageID: 7}
	if got, ok := RefFromRequest(pub); !ok ||
		got.Kind != tmeurl.PeerUsername || got.Username != "example_channel" || got.MessageID != 7 {
		t.Errorf("公开频道重建不对: %+v ok=%v", got, ok)
	}
	// 与 ChannelKey/SourceKind 互为逆变换
	ref := privRef(12)
	priv := store.Request{SourceKind: SourceKind(ref), ChannelKey: ChannelKey(ref), MessageID: 12}
	got, ok := RefFromRequest(priv)
	if !ok || got.Kind != tmeurl.PeerChannelID || got.ChannelID != 1234567890 || got.MessageID != 12 {
		t.Errorf("私有频道重建不对: %+v ok=%v", got, ok)
	}
	// 非法频道键
	for _, bad := range []store.Request{
		{SourceKind: store.SourcePrivate, ChannelKey: "-100", MessageID: 1},
		{SourceKind: store.SourcePrivate, ChannelKey: "-100abc", MessageID: 1},
		{SourceKind: store.SourcePrivate, ChannelKey: "example", MessageID: 1},
		{SourceKind: store.SourcePublic, ChannelKey: "", MessageID: 1},
	} {
		if _, ok := RefFromRequest(bad); ok {
			t.Errorf("非法频道键 %q 不应重建成功", bad.ChannelKey)
		}
	}
}

func TestRetryClockAdvancesQueuedAt(t *testing.T) {
	// 可注入时钟下重试刷新 queued_at，不依赖真实时间
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 8, clock.Now)
	startWorkers(t, q)
	mustEnabledUser(t, st, 1)
	r := newFailedRequest(t, svc, st, 7)

	clock.Advance(90 * time.Minute)
	if err := svc.Retry(context.Background(), "admin", r.ID); err != nil {
		t.Fatalf("重试失败: %v", err)
	}
	got, _ := st.GetRequest(context.Background(), r.ID)
	if want := baseTime.Add(90 * time.Minute).UnixMilli(); got.QueuedAt != want {
		t.Errorf("queued_at 应取推进后的时钟 %d，得到 %d", want, got.QueuedAt)
	}
}

func TestRetryConcurrentSingleEnqueue(t *testing.T) {
	// 并发重复重试同一请求：单连接下事务即互斥，事务内复核保证
	// 恰好一次成功、行只前进一次、无双重入队
	svc, st, _ := newTestService(t, 8, newClock(baseTime).Now)
	mustEnabledUser(t, st, 1)
	r := newFailedRequest(t, svc, st, 7)

	const n = 8
	var success atomic.Int32
	var constraint atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.Retry(context.Background(), "admin", r.ID)
			var ae *apperr.AppError
			switch {
			case err == nil:
				success.Add(1)
			case errors.As(err, &ae) && ae.Code == apperr.CodeStoreConstraint:
				constraint.Add(1)
			}
		}()
	}
	wg.Wait()

	if success.Load() != 1 {
		t.Fatalf("并发重试应恰好一个成功，得到 %d", success.Load())
	}
	if constraint.Load() != n-1 {
		t.Errorf("其余 %d 次应按状态冲突拒绝（STORE_CONSTRAINT），得到 %d", n-1, constraint.Load())
	}
	got, err := st.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if got.Attempt != 2 {
		t.Errorf("attempt 应只前进一次到 2，得到 %d", got.Attempt)
	}
}

// 云盘请求重试：目的地名称从 requests 行恢复到 Job.CloudDest（重试仍走云盘
// 上传路径），普通请求重试保持零值不变。
func TestRetryRestoresCloudDest(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 8, clock.Now)
	jobs := startWorkers(t, q)
	mustEnabledUser(t, st, 1)
	if err := st.UpdateUserCloudDownload(context.Background(), 1, store.CloudDownloadAllow); err != nil {
		t.Fatalf("授予云盘下载权限失败: %v", err)
	}

	// 经完整提交路径构造 failed 的云盘请求
	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(7), CloudDest: "mega-1"})
	waitJob(t, jobs)
	r, err := st.GetRequest(context.Background(), d.RequestID)
	if err != nil {
		t.Fatalf("读取云盘请求失败: %v", err)
	}
	if err := st.FinishRequest(context.Background(), r.ID, store.RequestResult{
		Status: store.RequestFailed, ErrorCode: "CLOUD_NETWORK",
	}); err != nil {
		t.Fatalf("置为失败失败: %v", err)
	}

	if err := svc.Retry(context.Background(), "admin", r.ID); err != nil {
		t.Fatalf("云盘请求重试不应失败: %v", err)
	}
	job := waitJob(t, jobs)
	if job.RequestID != r.ID || job.CloudDest != "mega-1" {
		t.Fatalf("重试任务应恢复云盘目的地: %+v", job)
	}

	// 普通请求重试：CloudDest 保持零值
	clock.Advance(time.Minute)
	plain := newFailedRequest(t, svc, st, 8)
	if err := svc.Retry(context.Background(), "admin", plain.ID); err != nil {
		t.Fatalf("普通请求重试不应失败: %v", err)
	}
	job2 := waitJob(t, jobs)
	if job2.CloudDest != "" {
		t.Fatalf("普通请求重试不应携带云盘目的地: %+v", job2)
	}
}
