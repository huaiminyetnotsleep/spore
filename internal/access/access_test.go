package access

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// ---- 测试基础设施 ----

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// mockClock 是可推进的可注入时钟（避免依赖真实时间）。
type mockClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(at time.Time) *mockClock { return &mockClock{t: at} }
func (c *mockClock) Now() time.Time    { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *mockClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeSender 记录审批通知调用；可切换为发送失败。
type fakeSender struct {
	mu   sync.Mutex
	sent []sentMessage
	fail bool
}

type sentMessage struct {
	chatID int64
	text   string
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, html string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return 0, errors.New("send failed")
	}
	f.sent = append(f.sent, sentMessage{chatID: chatID, text: html})
	return len(f.sent), nil
}

func (f *fakeSender) SendMedia(context.Context, int64, message.Media, message.Caption, io.Reader) (int, error) {
	return 1, nil
}
func (f *fakeSender) SendAlbum(context.Context, int64, []delivery.AlbumEntry) ([]int, error) {
	return []int{1, 2}, nil
}
func (f *fakeSender) AlbumGroupable(message.Media) bool { return false }
func (f *fakeSender) CopyMessages(_ context.Context, _, _ int64, messageIDs []int) ([]int, error) {
	return messageIDs, nil
}
func (f *fakeSender) CopyMessage(_ context.Context, _, _ int64, messageID int, _ string) (int, error) {
	return messageID, nil
}

func (f *fakeSender) EditMessageCaption(context.Context, int64, int, string) error { return nil }

func (f *fakeSender) DeleteMessage(context.Context, int64, int) error { return nil }

func (f *fakeSender) EditMessageText(context.Context, int64, int, string) error { return nil }

func (f *fakeSender) messages() []sentMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentMessage(nil), f.sent...)
}

type storeFailureSink struct{ calls atomic.Int32 }

func (s *storeFailureSink) StoreWriteFailed(context.Context) { s.calls.Add(1) }

// newTestService 在临时库上组装服务；返回同队列便于构造饱和等状态。
func newTestService(t *testing.T, capacity int, now func() time.Time) (*Service, *store.Store, *queue.Queue) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), testLog())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	q := queue.New(capacity)
	svc, err := New(Options{Store: st, Queue: q, Log: testLog(), Now: now})
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	return svc, st, q
}

// startWorkers 启动队列消费，把收到的任务送入 channel 供断言。
func startWorkers(t *testing.T, q *queue.Queue) <-chan queue.Job {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	jobs := make(chan queue.Job, 64)
	go q.Run(ctx, 1,
		func(_ context.Context, j queue.Job) { jobs <- j },
		func(_ context.Context, j queue.Job) {})
	return jobs
}

func waitJob(t *testing.T, jobs <-chan queue.Job) queue.Job {
	t.Helper()
	select {
	case j := <-jobs:
		return j
	case <-time.After(2 * time.Second):
		t.Fatal("等待队列任务超时")
		return queue.Job{}
	}
}

// baseTime 是默认测试时钟起点：上海 2026-08-27 12:00（UTC 04:00）。
var baseTime = time.Date(2026, 8, 27, 4, 0, 0, 0, time.UTC)

func pubRef(msgID int) tmeurl.SourceRef {
	return tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example_channel", MessageID: msgID}
}

func privRef(msgID int) tmeurl.SourceRef {
	return tmeurl.SourceRef{Kind: tmeurl.PeerChannelID, ChannelID: 1234567890, MessageID: msgID}
}

// mustSubmit 便捷断言"通过"的提交。
func mustSubmit(t *testing.T, s *Service, in Submission) Decision {
	t.Helper()
	d, err := s.Submit(context.Background(), in)
	if err != nil {
		t.Fatalf("提交不应返回存储错误: %v", err)
	}
	if !d.Allowed {
		t.Fatalf("期望提交通过，被拒绝: %s", d.Reason)
	}
	return d
}

func countRequests(t *testing.T, st *store.Store, userID int64) int {
	t.Helper()
	rows, err := st.ListRequests(context.Background(), store.RequestFilter{UserID: userID})
	if err != nil {
		t.Fatalf("查询请求失败: %v", err)
	}
	return len(rows)
}

// ---- 六步链各路径 ----

func TestSubmitAllowedRecordsAndEnqueues(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 4, clock.Now)
	jobs := startWorkers(t, q)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(42), StatusMsgID: 77})

	// 内存任务已入队且携带占位提示
	job := waitJob(t, jobs)
	if job.UserID != 1 || job.StatusMsgID != 77 || job.Ref.MessageID != 42 {
		t.Errorf("任务字段不符: %+v", job)
	}
	if d.JobID == "" || d.RequestID == 0 {
		t.Errorf("裁定应带任务与请求 ID: %+v", d)
	}

	// requests 行：queued + 频道标识 + 来源类型 + 时间戳（注入时钟）
	rows, _ := st.ListRequests(ctx, store.RequestFilter{UserID: 1})
	if len(rows) != 1 {
		t.Fatalf("应恰好一条请求记录，得到 %d", len(rows))
	}
	r := rows[0]
	if r.Status != store.RequestQueued || r.ChannelKey != "example_channel" ||
		r.SourceKind != store.SourcePublic || r.MessageID != 42 ||
		r.RequestedAt != baseTime.UnixMilli() || r.QueuedAt != baseTime.UnixMilli() {
		t.Errorf("请求记录字段不符: %+v", r)
	}

	// 额度扣减一次 + 使用时间记录
	day := baseTime.In(defaultLoc).Format(dayFormat)
	usage, _ := st.GetUsage(ctx, 1, day)
	if usage.Used != 1 {
		t.Errorf("应扣减 1 次额度，得到 %d", usage.Used)
	}
	got, _ := st.GetUser(ctx, 1)
	if got.FirstUsedAt != baseTime.UnixMilli() || got.LastUsedAt != baseTime.UnixMilli() {
		t.Errorf("使用时间应写入: %+v", got)
	}
}

func TestSubmitBatchContinuationSkipsOnlySubmitInterval(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 4, clock.Now)
	jobs := startWorkers(t, q)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{
		ID: 1, Status: store.UserEnabled, SubmitIntervalSec: 30, DailyLimit: 10, ConcurrentLimit: 3,
	}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(41)})
	waitJob(t, jobs)
	mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(42), BatchContinuation: true})
	waitJob(t, jobs)

	day := baseTime.In(defaultLoc).Format(dayFormat)
	usage, err := st.GetUsage(ctx, 1, day)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Used != 2 || countRequests(t, st, 1) != 2 {
		t.Fatalf("批量续项仍应逐条扣额度并落请求，usage=%d requests=%d", usage.Used, countRequests(t, st, 1))
	}
}

func TestSubmitProfileProvidedCanClearExistingProfile(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 4, clock.Now)
	jobs := startWorkers(t, q)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled, Username: "old_name", DisplayName: "Old Name"}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if d, err := svc.Submit(ctx, Submission{
		UserID: 1, ChatID: 1, Ref: pubRef(42), ProfileProvided: true,
	}); err != nil || !d.Allowed {
		t.Fatalf("携带空资料的提交应通过: decision=%+v err=%v", d, err)
	}
	waitJob(t, jobs)
	got, err := st.GetUser(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Username != "" || got.DisplayName != "" {
		t.Fatalf("Bot 当前资料为空时应清除旧资料: %+v", got)
	}
}

func TestSubmitPrivateLinkChannelKey(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 4, clock.Now)
	jobs := startWorkers(t, q)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: privRef(9)})
	waitJob(t, jobs)
	rows, _ := st.ListRequests(ctx, store.RequestFilter{UserID: 1})
	if len(rows) != 1 || rows[0].ChannelKey != "-1001234567890" || rows[0].SourceKind != store.SourcePrivate {
		t.Fatalf("私有频道标识不符: %+v", rows)
	}
}

func TestSubmitUserStatusDenials(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	for id, status := range map[int64]string{
		2: store.UserPending, 3: store.UserDisabled, 4: store.UserArchived,
	} {
		if _, err := st.CreateUser(ctx, store.User{ID: id, Status: status}); err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}

	cases := []struct {
		userID int64
		reason apperr.Code
	}{
		{999, apperr.CodeUserNotAuthorized}, // 不存在
		{2, apperr.CodeUserPending},
		{3, apperr.CodeUserDisabled},
		{4, apperr.CodeUserDisabled}, // 归档与禁用同文案
	}
	for _, tc := range cases {
		d, err := svc.Submit(ctx, Submission{UserID: tc.userID, ChatID: tc.userID, Ref: pubRef(1)})
		if err != nil {
			t.Fatalf("用户 %d 提交不应返回存储错误: %v", tc.userID, err)
		}
		if d.Allowed || d.Reason != tc.reason {
			t.Errorf("用户 %d 应被拒绝（%s），得到 %+v", tc.userID, tc.reason, d)
		}
		if n := countRequests(t, st, tc.userID); n != 0 {
			t.Errorf("用户 %d 拒绝不应建 requests 行，得到 %d 条", tc.userID, n)
		}
	}

	// 存在用户的拒绝会记录 last_denied_*
	got, _ := st.GetUser(ctx, 2)
	if got.LastDeniedAt != baseTime.UnixMilli() || got.LastDeniedReason != string(apperr.CodeUserPending) {
		t.Errorf("拒绝痕迹应落库: %d %q", got.LastDeniedAt, got.LastDeniedReason)
	}
	// 不存在的用户不产生行
	if _, err := st.GetUser(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("不存在的用户不应被创建: %v", err)
	}
}

func TestSubmitDuplicateWindow(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// 窗口内（5 分钟前）同链接成功记录：命中重复
	seed := func(msgID int, requestedAgo time.Duration) {
		t.Helper()
		r, err := st.CreateRequest(ctx, store.Request{
			UserID: 1, ChannelKey: "example_channel", MessageID: msgID,
			RequestedAt: baseTime.Add(-requestedAgo).UnixMilli(),
			QueuedAt:    baseTime.Add(-requestedAgo).UnixMilli(),
		})
		if err != nil {
			t.Fatalf("种子请求失败: %v", err)
		}
		if err := st.FinishRequest(ctx, r.ID, store.RequestResult{Status: store.RequestSucceeded}); err != nil {
			t.Fatalf("种子终态失败: %v", err)
		}
	}
	seed(42, 5*time.Minute)

	d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(42)})
	if err != nil || d.Allowed || d.Reason != apperr.CodeDuplicateLink {
		t.Fatalf("窗口内重复应拒绝 DUPLICATE_LINK: %+v err=%v", d, err)
	}
	if n := countRequests(t, st, 1); n != 1 {
		t.Errorf("重复提交不应新建 requests 行，得到 %d", n)
	}
	day := baseTime.In(defaultLoc).Format(dayFormat)
	usage, _ := st.GetUsage(ctx, 1, day)
	if usage.Used != 0 {
		t.Errorf("重复提交不应扣额度，得到 %d", usage.Used)
	}

	// 窗口外（15 分钟前）不再重复
	seed(43, 15*time.Minute)
	if d, _ := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(43)}); !d.Allowed {
		t.Errorf("窗口外链接应通过，得到 %+v", d)
	}
	// 同链接不同用户互不影响
	if _, err := st.CreateUser(ctx, store.User{ID: 2, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if d, _ := svc.Submit(ctx, Submission{UserID: 2, ChatID: 2, Ref: pubRef(42)}); !d.Allowed {
		t.Errorf("其他用户同链接应通过，得到 %+v", d)
	}
}

func TestSubmitDedupWindowConfigurable(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	// 7 分钟前的成功记录：默认窗口（10 分钟）内重复，调小到 5 分钟后放行
	r, _ := st.CreateRequest(ctx, store.Request{
		UserID: 1, ChannelKey: "example_channel", MessageID: 42,
		RequestedAt: baseTime.Add(-7 * time.Minute).UnixMilli(),
	})
	if err := st.FinishRequest(ctx, r.ID, store.RequestResult{Status: store.RequestSucceeded}); err != nil {
		t.Fatalf("种子终态失败: %v", err)
	}
	if d, _ := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(42)}); d.Allowed {
		t.Fatalf("默认窗口内应拒绝，得到 %+v", d)
	}
	if err := svc.SetDedupWindow(ctx, 5); err != nil {
		t.Fatalf("调整去重窗口失败: %v", err)
	}
	if d, _ := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(42)}); !d.Allowed {
		t.Errorf("调小窗口后应通过，得到 %+v", d)
	}
}

// TestSubmitCloudDuplicate 云盘提交的重复判定：成功去重交给 worker 的"已上传
// 跳过"路径（命中会直接返回成功），提交链只拦同链接同目的地的在途任务
// （同路径并发上传会在允许同名对象的网盘产生重复文件）。
func TestSubmitCloudDuplicate(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 8, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled, ConcurrentLimit: 8}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := st.UpdateUserCloudDownload(ctx, 1, store.CloudDownloadAllow); err != nil {
		t.Fatalf("授予云盘下载权限失败: %v", err)
	}

	// 近期普通 TG 提取成功不拦截云盘提交：上传是独立投递动作
	r, err := st.CreateRequest(ctx, store.Request{
		UserID: 1, ChannelKey: "example_channel", MessageID: 42,
		RequestedAt: baseTime.Add(-5 * time.Minute).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("种子 TG 请求失败: %v", err)
	}
	if err := st.FinishRequest(ctx, r.ID, store.RequestResult{Status: store.RequestSucceeded}); err != nil {
		t.Fatalf("种子终态失败: %v", err)
	}
	if d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(42), CloudDest: "mega-1"}); err != nil || !d.Allowed {
		t.Fatalf("近期 TG 成功不应拦截云盘提交: %+v err=%v", d, err)
	}

	// 近期云盘成功同样不在提交链拦截：worker 会核验后跳过重传并返回成功
	cloudDone, err := st.CreateRequest(ctx, store.Request{
		UserID: 1, ChannelKey: "example_channel", MessageID: 43,
		DeliveryMode: store.DeliveryModeCloud, CloudDestination: "mega-1",
		RequestedAt: baseTime.Add(-5 * time.Minute).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("种子云盘请求失败: %v", err)
	}
	if err := st.FinishRequest(ctx, cloudDone.ID, store.RequestResult{
		Status: store.RequestSucceeded, DeliveryMode: store.DeliveryModeCloud,
	}); err != nil {
		t.Fatalf("种子终态失败: %v", err)
	}
	clock.Advance(15 * time.Second)
	if d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(43), CloudDest: "mega-1"}); err != nil || !d.Allowed {
		t.Fatalf("近期云盘成功不应在提交链拦截（由 worker 跳过）: %+v err=%v", d, err)
	}

	// 同链接同目的地在途 → DUPLICATE_LINK，不建行
	if _, err := st.CreateRequest(ctx, store.Request{
		UserID: 1, ChannelKey: "example_channel", MessageID: 44,
		DeliveryMode: store.DeliveryModeCloud, CloudDestination: "mega-1",
	}); err != nil {
		t.Fatalf("种子在途云盘请求失败: %v", err)
	}
	before := countRequests(t, st, 1)
	clock.Advance(15 * time.Second)
	dup, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(44), CloudDest: "mega-1"})
	if err != nil || dup.Allowed || dup.Reason != apperr.CodeDuplicateLink {
		t.Fatalf("同目的地在途应拒绝 DUPLICATE_LINK: %+v err=%v", dup, err)
	}
	if n := countRequests(t, st, 1); n != before {
		t.Errorf("重复提交不应新建 requests 行，得到 %d（之前 %d）", n, before)
	}

	// 同链接不同目的地在途 → 放行（互不相关的远端路径）
	if _, err := st.CreateRequest(ctx, store.Request{
		UserID: 1, ChannelKey: "example_channel", MessageID: 45,
		DeliveryMode: store.DeliveryModeCloud, CloudDestination: "mega-2",
	}); err != nil {
		t.Fatalf("种子在途云盘请求失败: %v", err)
	}
	clock.Advance(15 * time.Second)
	if d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(45), CloudDest: "mega-1"}); err != nil || !d.Allowed {
		t.Fatalf("不同目的地在途应放行: %+v err=%v", d, err)
	}
}

func TestSubmitRateLimited(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// 上次通过提交在 5 秒前（默认间隔 10 秒）：拒绝
	if err := st.TouchUserUsage(ctx, 1, baseTime.Add(-5*time.Second).UnixMilli()); err != nil {
		t.Fatalf("记录使用时间失败: %v", err)
	}
	d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(1)})
	if err != nil || d.Allowed || d.Reason != apperr.CodeSubmitRateLimited {
		t.Fatalf("间隔内应拒绝 RATE_LIMITED: %+v err=%v", d, err)
	}
	// 拒绝不刷新使用时间（间隔自上次通过提交起算）
	got, _ := st.GetUser(ctx, 1)
	if got.LastUsedAt != baseTime.Add(-5*time.Second).UnixMilli() {
		t.Errorf("拒绝不应刷新 last_used_at: %d", got.LastUsedAt)
	}

	// 恰好间隔到期：通过
	if err := st.TouchUserUsage(ctx, 1, baseTime.Add(-10*time.Second).UnixMilli()); err != nil {
		t.Fatalf("记录使用时间失败: %v", err)
	}
	if d, _ := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(2)}); !d.Allowed {
		t.Errorf("间隔到期应通过，得到 %+v", d)
	}

	// 首次提交（无使用时间）：通过
	if _, err := st.CreateUser(ctx, store.User{ID: 2, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if d, _ := svc.Submit(ctx, Submission{UserID: 2, ChatID: 2, Ref: pubRef(3)}); !d.Allowed {
		t.Errorf("首次提交应通过，得到 %+v", d)
	}
}

func TestSubmitQuotaExceededAndReset(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := st.UpdateUserLimits(ctx, 1, 0, 2, 0); err != nil { // 额度调到 2
		t.Fatalf("调整限额失败: %v", err)
	}
	day := baseTime.In(defaultLoc).Format(dayFormat)
	if err := st.IncrementUsage(ctx, 1, day, 2); err != nil {
		t.Fatalf("预置用量失败: %v", err)
	}

	d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(1)})
	if err != nil || d.Allowed || d.Reason != apperr.CodeQuotaExceeded {
		t.Fatalf("额度耗尽应拒绝 QUOTA_EXCEEDED: %+v err=%v", d, err)
	}
	if n := countRequests(t, st, 1); n != 0 {
		t.Errorf("拒绝不应建 requests 行，得到 %d", n)
	}

	// 管理员重置当日用量后恢复
	if err := svc.ResetDailyUsage(ctx, "admin", 1); err != nil {
		t.Fatalf("重置用量失败: %v", err)
	}
	if d, _ := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(1)}); !d.Allowed {
		t.Errorf("重置后应通过，得到 %+v", d)
	}
	usage, _ := st.GetUsage(ctx, 1, day)
	if usage.Used != 1 || usage.ResetCount != 1 {
		t.Errorf("重置后应重新计数，得到 %+v", usage)
	}
}

func TestSubmitConcurrentLimit(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 8, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	for i := 1; i <= 2; i++ { // 默认并发上限 2：预置 2 条未完成
		if _, err := st.CreateRequest(ctx, store.Request{UserID: 1, ChannelKey: "example_channel", MessageID: i}); err != nil {
			t.Fatalf("种子请求失败: %v", err)
		}
	}
	d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(9)})
	if err != nil || d.Allowed || d.Reason != apperr.CodeConcurrentLimited {
		t.Fatalf("并发超限应拒绝 CONCURRENT_LIMIT: %+v err=%v", d, err)
	}

	// 其中一条完成（占位释放）后放行
	rows, _ := st.ListRequests(ctx, store.RequestFilter{UserID: 1})
	if err := st.FinishRequest(ctx, rows[0].ID, store.RequestResult{Status: store.RequestSucceeded}); err != nil {
		t.Fatalf("完成种子请求失败: %v", err)
	}
	if d, _ := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(9)}); !d.Allowed {
		t.Errorf("释放并发后应通过，得到 %+v", d)
	}
}

func TestSubmitQueueFull(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 1, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	// 占满唯一队列槽位
	if err := q.Enqueue(queue.NewJob(9, 9, pubRef(1), 0, 0)); err != nil {
		t.Fatalf("占位入队失败: %v", err)
	}

	d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(2)})
	if err != nil || d.Allowed || d.Reason != apperr.CodeQueueFull {
		t.Fatalf("队列满应拒绝 QUEUE_FULL: %+v err=%v", d, err)
	}
	if n := countRequests(t, st, 1); n != 0 {
		t.Errorf("队列满拒绝不应建 requests 行，得到 %d", n)
	}
	got, _ := st.GetUser(ctx, 1)
	if got.LastDeniedReason != string(apperr.CodeQueueFull) {
		t.Errorf("队列满应记拒绝原因: %q", got.LastDeniedReason)
	}
}

// failingQueue 包装真实队列：预检（Full）不饱和，但入队必然失败，
// 确定性复现"事务提交后入队失败（队列满竞态）"分支——
// Full 预检与 Enqueue 之间被并发提交挤占的窗口在真实队列上不可稳定构造。
type failingQueue struct {
	*queue.Queue
	calls atomic.Int64
}

func (f *failingQueue) Enqueue(queue.Job) error {
	f.calls.Add(1)
	return queue.ErrBusy
}

// TestSubmitEnqueueFailureAfterCommit 验证竞态分支的数据一致性：
// 事务已提交（额度已扣、requests 行已建）但入队失败时，
// 该行按 QUEUE_FULL 失败收尾、额度不返还、用户提示队列满，
// 且不写入 last_denied_*（这是"受理后失败"，不是拒绝）。
func TestSubmitEnqueueFailureAfterCommit(t *testing.T) {
	clock := newClock(baseTime)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), testLog())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fq := &failingQueue{Queue: queue.New(4)}
	svc, err := New(Options{Store: st, Queue: fq, Log: testLog(), Now: clock.Now})
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(42), StatusMsgID: 5})
	if err != nil {
		t.Fatalf("竞态分支不应返回存储错误: %v", err)
	}
	if d.Allowed || d.Reason != apperr.CodeQueueFull {
		t.Fatalf("应按 QUEUE_FULL 失败收尾: %+v", d)
	}
	if d.RequestID == 0 {
		t.Fatal("竞态落库的请求行 ID 应带回调用方")
	}
	if fq.calls.Load() != 1 {
		t.Errorf("应恰好尝试入队一次，得到 %d", fq.calls.Load())
	}

	// requests 行：已建但标记为 failed(QUEUE_FULL)
	rows, _ := st.ListRequests(ctx, store.RequestFilter{UserID: 1})
	if len(rows) != 1 {
		t.Fatalf("应恰好一条请求记录，得到 %d", len(rows))
	}
	if rows[0].Status != store.RequestFailed || rows[0].ErrorCode != string(apperr.CodeQueueFull) {
		t.Errorf("请求行应为 failed/QUEUE_FULL，得到 %s/%s", rows[0].Status, rows[0].ErrorCode)
	}
	// 额度已扣且不返还（失败不返还）
	day := baseTime.In(defaultLoc).Format(dayFormat)
	usage, _ := st.GetUsage(ctx, 1, day)
	if usage.Used != 1 {
		t.Errorf("额度应保持扣减 1 次（不返还），得到 %d", usage.Used)
	}
	// 竞态分支不是拒绝：不写 last_denied_*
	got, _ := st.GetUser(ctx, 1)
	if got.LastDeniedAt != 0 || got.LastDeniedReason != "" {
		t.Errorf("竞态分支不应记拒绝痕迹: %d %q", got.LastDeniedAt, got.LastDeniedReason)
	}

	// 队列恢复后重提同链接可通过：竞态失败行不参与重复检测（只计 succeeded）；
	// 时钟推进过提交间隔（竞态路径已刷新 last_used_at）
	clock.Advance(10 * time.Second)
	svc2, err := New(Options{Store: st, Queue: queue.New(4), Log: testLog(), Now: clock.Now})
	if err != nil {
		t.Fatalf("创建恢复服务失败: %v", err)
	}
	if d2, _ := svc2.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(42)}); !d2.Allowed {
		t.Errorf("队列恢复后重提应通过: %+v", d2)
	}
	usage, _ = st.GetUsage(ctx, 1, day)
	if usage.Used != 2 {
		t.Errorf("重提应再扣 1 次（共 2），得到 %d", usage.Used)
	}
}

func TestSubmitOwnerExempt(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 8, clock.Now)
	jobs := startWorkers(t, q)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := st.SetOwner(ctx, 1, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}

	// 三类约束全部处于超限状态：owner 仍应通过
	if err := st.TouchUserUsage(ctx, 1, baseTime.Add(-1*time.Second).UnixMilli()); err != nil { // 间隔未到
		t.Fatalf("记录使用时间失败: %v", err)
	}
	day := baseTime.In(defaultLoc).Format(dayFormat)
	if err := st.IncrementUsage(ctx, 1, day, 100); err != nil { // 额度远超默认 50
		t.Fatalf("预置用量失败: %v", err)
	}
	for i := 1; i <= 5; i++ { // 并发远超默认 2
		if _, err := st.CreateRequest(ctx, store.Request{UserID: 1, ChannelKey: "example_channel", MessageID: i}); err != nil {
			t.Fatalf("种子请求失败: %v", err)
		}
	}

	d := mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(77)})
	waitJob(t, jobs)
	if d.RequestID == 0 {
		t.Fatalf("owner 请求应完整落库: %+v", d)
	}

	// owner 不扣额度，但请求照常记录
	usage, _ := st.GetUsage(ctx, 1, day)
	if usage.Used != 100 {
		t.Errorf("owner 不应扣减额度，得到 %d", usage.Used)
	}
	if n := countRequests(t, st, 1); n != 6 {
		t.Errorf("owner 请求应完整记录（5 种子 + 1 新），得到 %d", n)
	}
}

// TestSubmitNoOverspendUnderConcurrency 验证检查与扣减同事务：
// 并发提交同一用户时绝不超扣（used ≤ daily_limit），也不产生超额 requests 行。
func TestSubmitNoOverspendUnderConcurrency(t *testing.T) {
	var calls atomic.Int64
	now := func() time.Time {
		return baseTime.Add(time.Duration(2*calls.Add(1)) * time.Second)
	}
	svc, st, _ := newTestService(t, 64, now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := st.UpdateUserLimits(ctx, 1, 1, 5, 100); err != nil { // 额度 5，放宽间隔与并发以聚焦额度
		t.Fatalf("调整限额失败: %v", err)
	}

	const workers = 20
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(1000 + i)})
			if err != nil {
				t.Errorf("提交返回存储错误: %v", err)
				return
			}
			if d.Allowed {
				allowed.Add(1)
			}
		}(i)
	}
	wg.Wait()

	day := baseTime.In(defaultLoc).Format(dayFormat)
	usage, err := st.GetUsage(ctx, 1, day)
	if err != nil {
		t.Fatalf("读取用量失败: %v", err)
	}
	if usage.Used > 5 {
		t.Fatalf("并发提交超扣：used=%d > limit=5", usage.Used)
	}
	rows := countRequests(t, st, 1)
	if rows > 5 {
		t.Fatalf("超额落库：requests=%d > limit=5", rows)
	}
	if int64(rows) != allowed.Load() {
		t.Fatalf("通过数应与落库数一致：allowed=%d rows=%d", allowed.Load(), rows)
	}
}

// TestSubmitCrossUserConcurrency 验证多用户并发提交在单连接上不死锁、不串账。
func TestSubmitCrossUserConcurrency(t *testing.T) {
	var calls atomic.Int64
	now := func() time.Time { return baseTime.Add(time.Duration(2*calls.Add(1)) * time.Second) }
	svc, st, _ := newTestService(t, 64, now)
	ctx := context.Background()
	const users = 8
	for id := int64(1); id <= users; id++ {
		if _, err := st.CreateUser(ctx, store.User{ID: id, Status: store.UserEnabled}); err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}
	var wg sync.WaitGroup
	for id := int64(1); id <= users; id++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			if d, err := svc.Submit(ctx, Submission{UserID: id, ChatID: id, Ref: pubRef(int(id))}); err != nil || !d.Allowed {
				t.Errorf("用户 %d 提交应通过: %+v err=%v", id, d, err)
			}
		}(id)
	}
	wg.Wait()

	day := baseTime.In(defaultLoc).Format(dayFormat)
	for id := int64(1); id <= users; id++ {
		usage, err := st.GetUsage(ctx, id, day)
		if err != nil {
			t.Fatalf("读取用量失败: %v", err)
		}
		if usage.Used != 1 {
			t.Fatalf("用户 %d 应恰好扣减 1 次，得到 %d", id, usage.Used)
		}
		if n := countRequests(t, st, id); n != 1 {
			t.Fatalf("用户 %d 应恰好 1 条记录，得到 %d", id, n)
		}
	}
}

// ---- 时区与日切分 ----

func TestSubmitDayBucketingAndTimezoneSwitch(t *testing.T) {
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("加载时区失败: %v", err)
	}
	// 起点：上海 2026-08-27 23:30
	clock := newClock(time.Date(2026, 8, 27, 23, 30, 0, 0, shanghai))
	svc, st, _ := newTestService(t, 8, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := st.UpdateUserLimits(ctx, 1, 0, 1, 10); err != nil { // 每日额度 1；放宽并发（本测试无 worker 收尾）
		t.Fatalf("调整限额失败: %v", err)
	}

	// 当日（08-27）第一次：通过
	mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(1)})
	// 同日第二次（推进提交间隔后触发额度上限）：额度耗尽
	clock.Advance(10 * time.Second)
	if d, _ := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(2)}); d.Allowed || d.Reason != apperr.CodeQuotaExceeded {
		t.Fatalf("同日应额度耗尽: %+v", d)
	}
	// 跨过上海 00:00：新的一天重新计数
	clock.Advance(45 * time.Minute)
	if d, _ := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(2)}); !d.Allowed {
		t.Fatalf("跨日应重新计数: %+v", d)
	}
	for day, want := range map[string]int{"2026-08-27": 1, "2026-08-28": 1} {
		usage, _ := st.GetUsage(ctx, 1, day)
		if usage.Used != want {
			t.Errorf("日分桶 %s 应为 %d，得到 %d", day, want, usage.Used)
		}
	}

	// 切换运营时区为 UTC：后续按 UTC 日期分桶（取一个两侧日期都不冲突的时点）
	if err := svc.SetTimezone(ctx, "UTC"); err != nil {
		t.Fatalf("切换时区失败: %v", err)
	}
	clock.Advance(48 * time.Hour) // 上海 08-30 01:15 = UTC 08-29 17:15
	if d, _ := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(3)}); !d.Allowed {
		t.Fatalf("UTC 新分桶应通过: %+v", d)
	}
	usage, _ := st.GetUsage(ctx, 1, "2026-08-29")
	if usage.Used != 1 {
		t.Errorf("UTC 日分桶应为 1，得到 %d", usage.Used)
	}
	// /usage 的重置时间按新时区计算：下一 UTC 00:00
	u, err := svc.Usage(ctx, 1)
	if err != nil {
		t.Fatalf("查询用量失败: %v", err)
	}
	want := time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)
	if !u.ResetAt.Equal(want) {
		t.Errorf("重置时间应为 UTC 08-30 00:00，得到 %v", u.ResetAt)
	}
}

func TestSettingsFallbackToDefaults(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// 非法时区 JSON / 非法时区名 / 非法去重窗口：回退默认值，不阻断提交
	for key, val := range map[string]string{
		settingKeyTimezone:       "not-json",
		settingKeyDedupWindowMin: `"10"`, // JSON 字符串而非数字
	} {
		if err := st.SetSetting(ctx, key, val); err != nil {
			t.Fatalf("写入设置失败: %v", err)
		}
	}
	if d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(1)}); err != nil || !d.Allowed {
		t.Fatalf("非法设置应回退默认并放行: %+v err=%v", d, err)
	}
	day := baseTime.In(defaultLoc).Format(dayFormat)
	usage, _ := st.GetUsage(ctx, 1, day)
	if usage.Used != 1 {
		t.Errorf("应按默认时区分桶扣减，得到 %d", usage.Used)
	}

	// 合法设置项校验
	if err := svc.SetTimezone(ctx, "Bogus/Zone"); err == nil {
		t.Error("非法时区名应报错")
	}
	if err := svc.SetDedupWindow(ctx, 0); err == nil {
		t.Error("非正数窗口应报错")
	}
	if err := svc.SetTimezone(ctx, "UTC"); err != nil {
		t.Errorf("合法时区不应报错: %v", err)
	}
	if err := svc.SetDedupWindow(ctx, 5); err != nil {
		t.Errorf("合法窗口不应报错: %v", err)
	}
}

// ---- /start 与 /usage ----

func TestHandleStartLifecycle(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()

	// 不存在：创建 pending 行并携带身份信息
	out, err := svc.HandleStart(ctx, StartInput{UserID: 1, Username: "alice", DisplayName: "Alice"})
	if err != nil || out != StartPending {
		t.Fatalf("陌生 /start 应落 pending: %v err=%v", out, err)
	}
	u, err := st.GetUser(ctx, 1)
	if err != nil || u.Status != store.UserPending || u.Username != "alice" || u.DisplayName != "Alice" {
		t.Fatalf("pending 行不符: %+v err=%v", u, err)
	}

	// 幂等：重复 /start 仍是 pending，且刷新申请时间
	clock.Advance(time.Minute)
	out, err = svc.HandleStart(ctx, StartInput{UserID: 1, Username: "alice"})
	if err != nil || out != StartPending {
		t.Fatalf("重复 /start 应保持 pending: %v err=%v", out, err)
	}
	users, _ := st.ListUsers(ctx)
	if len(users) != 1 {
		t.Fatalf("重复 /start 不应新建行，得到 %d 行", len(users))
	}
	if users[0].CreatedAt <= u.CreatedAt {
		t.Errorf("重复 /start 应刷新申请时间: %d <= %d", users[0].CreatedAt, u.CreatedAt)
	}

	// 批准后：/start 返回欢迎
	if _, err := svc.Approve(ctx, "admin", 1); err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if out, _ := svc.HandleStart(ctx, StartInput{UserID: 1}); out != StartWelcome {
		t.Errorf("已启用 /start 应欢迎，得到 %v", out)
	}
	// 拒绝（置 disabled）后：提示停用
	if _, err := svc.Reject(ctx, "admin", 1); err != nil {
		t.Fatalf("拒绝失败: %v", err)
	}
	if out, _ := svc.HandleStart(ctx, StartInput{UserID: 1}); out != StartDisabled {
		t.Errorf("停用 /start 应提示停用，得到 %v", out)
	}
	// 归档用户：提示停用
	if _, err := st.CreateUser(ctx, store.User{ID: 2, Status: store.UserArchived}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if out, _ := svc.HandleStart(ctx, StartInput{UserID: 2}); out != StartDisabled {
		t.Errorf("归档 /start 应提示停用，得到 %v", out)
	}
}

func TestUsageQuery(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	day := baseTime.In(defaultLoc).Format(dayFormat)
	if err := st.IncrementUsage(ctx, 1, day, 3); err != nil {
		t.Fatalf("预置用量失败: %v", err)
	}

	u, err := svc.Usage(ctx, 1)
	if err != nil {
		t.Fatalf("查询用量失败: %v", err)
	}
	if u.Status != store.UserEnabled || u.Used != 3 || u.DailyLimit != store.DefaultDailyLimit || u.Remaining != store.DefaultDailyLimit-3 {
		t.Errorf("用量字段不符: %+v", u)
	}
	// 重置时间：下一个上海 00:00
	want := time.Date(2026, 8, 28, 0, 0, 0, 0, defaultLoc)
	if !u.ResetAt.Equal(want) {
		t.Errorf("重置时间不符: want %v got %v", want, u.ResetAt)
	}

	// owner：不限额
	if err := st.SetOwner(ctx, 1, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}
	u, _ = svc.Usage(ctx, 1)
	if !u.IsOwner || u.Remaining != -1 {
		t.Errorf("owner 应不限额: %+v", u)
	}

	// 不存在用户：空状态
	u, err = svc.Usage(ctx, 404)
	if err != nil || u.Status != "" {
		t.Errorf("不存在用户应返回空状态: %+v err=%v", u, err)
	}
}

// ---- 管理操作 ----

func TestApproveRejectWithNotification(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	snd := &fakeSender{}
	svc.SetSender(snd)
	ctx := context.Background()

	mkPending := func(id int64) {
		t.Helper()
		if _, err := st.CreateUser(ctx, store.User{ID: id, Status: store.UserPending}); err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}
	mkPending(1)
	mkPending(2)

	// 批准：状态 + 审计 + 通知
	notified, err := svc.Approve(ctx, "admin", 1)
	if err != nil {
		t.Fatalf("批准失败: %v", err)
	}
	if !notified {
		t.Error("通知成功时 notified 应为 true")
	}
	u, _ := st.GetUser(ctx, 1)
	if u.Status != store.UserEnabled {
		t.Errorf("批准后应 enabled，得到 %s", u.Status)
	}
	msgs := snd.messages()
	if len(msgs) != 1 || msgs[0].chatID != 1 || msgs[0].text != approveNotifyText(ctx, st) {
		t.Fatalf("应向申请用户发送批准通知: %+v", msgs)
	}
	audits, err := st.ListAudit(ctx, 5, 0)
	if err != nil || len(audits) != 1 {
		t.Fatalf("应有一条审计: %v err=%v", len(audits), err)
	}
	if audits[0].Action != "user.approve" || audits[0].Actor != "admin" || audits[0].Target != "user:1" {
		t.Errorf("审计字段不符: %+v", audits[0])
	}

	// 拒绝：置 disabled + 通知
	if notified, err := svc.Reject(ctx, "admin", 2); err != nil || !notified {
		t.Fatalf("拒绝失败: notified=%v err=%v", notified, err)
	}
	u, _ = st.GetUser(ctx, 2)
	if u.Status != store.UserDisabled {
		t.Errorf("拒绝后应 disabled，得到 %s", u.Status)
	}
	msgs = snd.messages()
	if len(msgs) != 2 || msgs[1].chatID != 2 || msgs[1].text != rejectNotifyText {
		t.Fatalf("应向申请用户发送拒绝通知: %+v", msgs)
	}

	// 通知发送失败：不回滚、不报错
	snd.mu.Lock()
	snd.fail = true
	snd.mu.Unlock()
	mkPending(3)
	if notified, err := svc.Approve(ctx, "admin", 3); err != nil || notified {
		t.Fatalf("通知失败不应报错但 notified 应为 false: notified=%v err=%v", notified, err)
	}
	u, _ = st.GetUser(ctx, 3)
	if u.Status != store.UserEnabled {
		t.Errorf("通知失败不应回滚: %s", u.Status)
	}

	// 目标不存在：ErrNotFound
	if _, err := svc.Approve(ctx, "admin", 404); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("不存在用户应返回 ErrNotFound，得到 %v", err)
	}
}

func TestSetUserLimitsAndAudit(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := svc.SetUserLimits(ctx, "admin", 1, 30, 200, 5, nil); err != nil {
		t.Fatalf("调整限额失败: %v", err)
	}
	u, _ := st.GetUser(ctx, 1)
	if u.SubmitIntervalSec != 30 || u.DailyLimit != 200 || u.ConcurrentLimit != 5 {
		t.Errorf("限额未生效: %+v", u)
	}
	audits, _ := st.ListAudit(ctx, 5, 0)
	if len(audits) != 1 || audits[0].Action != "user.set_limits" {
		t.Fatalf("应留限额审计: %+v", audits)
	}
	// 限额即时生效：间隔 30 秒内拒绝
	if err := st.TouchUserUsage(ctx, 1, baseTime.Add(-10*time.Second).UnixMilli()); err != nil {
		t.Fatalf("记录使用时间失败: %v", err)
	}
	if d, _ := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(1)}); d.Allowed || d.Reason != apperr.CodeSubmitRateLimited {
		t.Errorf("新间隔应即时生效: %+v", d)
	}
}

func TestImportLegacyWhitelist(t *testing.T) {
	clock := newClock(baseTime)
	_, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()

	// 空库 + env 白名单：导入为 enabled
	ids := map[int64]struct{}{111: {}, 222: {}}
	if err := ImportLegacyWhitelist(ctx, st, ids, testLog()); err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	users, _ := st.ListUsers(ctx)
	if len(users) != 2 {
		t.Fatalf("应导入 2 个用户，得到 %d", len(users))
	}
	for _, u := range users {
		if u.Status != store.UserEnabled {
			t.Errorf("导入用户应 enabled，得到 %s", u.Status)
		}
	}

	// 再次启动（env 变化）：数据库已有用户，不再导入
	changed := map[int64]struct{}{333: {}}
	if err := ImportLegacyWhitelist(ctx, st, changed, testLog()); err != nil {
		t.Fatalf("二次导入失败: %v", err)
	}
	users, _ = st.ListUsers(ctx)
	if len(users) != 2 {
		t.Fatalf("数据库接管后不应再导入，得到 %d", len(users))
	}

	// 空 env：无操作
	if err := ImportLegacyWhitelist(ctx, st, nil, testLog()); err != nil {
		t.Fatalf("空 env 不应报错: %v", err)
	}
}

func TestSubmitStoreFailureReportsEvent(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 4, clock.Now)
	sink := &storeFailureSink{}
	var err error
	svc, err = New(Options{Store: st, Queue: q, Events: sink, Log: testLog(), Now: clock.Now})
	if err != nil {
		t.Fatalf("重建访问控制服务失败: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("关闭测试库失败: %v", err)
	}

	_, err = svc.Submit(context.Background(), Submission{
		UserID: 1, ChatID: 1,
		Ref: tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: 1},
	})
	if err == nil {
		t.Fatal("数据库关闭后提交应返回错误")
	}
	if got := sink.calls.Load(); got != 1 {
		t.Fatalf("存储故障应上报一次数据库事件，得到 %d", got)
	}
}

// ---- /download 云盘提交（Submission.CloudDest 透传）----

// 云盘提交与裸链接同链路：额度扣减照常，requests 行落 cloud 占位与目的地
// 名称，入队任务带同名 CloudDest。
func TestSubmitCloudDestPassthrough(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 4, clock.Now)
	jobs := startWorkers(t, q)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := st.UpdateUserCloudDownload(ctx, 1, store.CloudDownloadAllow); err != nil {
		t.Fatalf("授予云盘下载权限失败: %v", err)
	}

	d := mustSubmit(t, svc, Submission{
		UserID: 1, ChatID: 1, Ref: pubRef(42), StatusMsgID: 77, CloudDest: "mega-1",
	})
	if d.RequestID == 0 {
		t.Fatal("云盘提交应带回请求行 ID")
	}

	job := waitJob(t, jobs)
	if job.CloudDest != "mega-1" || job.StatusMsgID != 77 || job.Ref.MessageID != 42 {
		t.Errorf("云盘任务应携带目的地与占位: %+v", job)
	}

	rows, _ := st.ListRequests(ctx, store.RequestFilter{UserID: 1})
	if len(rows) != 1 {
		t.Fatalf("应恰好一条请求记录，得到 %d", len(rows))
	}
	r := rows[0]
	if r.DeliveryMode != store.DeliveryModeCloud || r.CloudDestination != "mega-1" {
		t.Errorf("请求行应落 cloud 占位与目的地名称: %+v", r)
	}
	// 额度与使用时间与裸链接一致
	day := baseTime.In(defaultLoc).Format(dayFormat)
	usage, _ := st.GetUsage(ctx, 1, day)
	if usage.Used != 1 {
		t.Errorf("云盘提交同样扣减 1 次额度，得到 %d", usage.Used)
	}

	// 裸链接提交（CloudDest 零值）：行为零值兼容，不落 cloud 标记
	clock.Advance(time.Minute)
	mustSubmit(t, svc, Submission{UserID: 1, ChatID: 1, Ref: pubRef(43)})
	waitJob(t, jobs)
	rows, _ = st.ListRequests(ctx, store.RequestFilter{UserID: 1})
	if rows[0].CloudDestination != "" || rows[0].DeliveryMode != store.DeliveryModeUpload {
		t.Errorf("裸链接请求不应带云盘标记: %+v", rows[0])
	}
}

// 云盘提交的拒绝语义与裸链接一致（状态/频率/配额/并发/队列满逐项同码）。
func TestSubmitCloudDenialsSameAsBareLink(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 2, Status: store.UserPending}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	d, err := svc.Submit(ctx, Submission{UserID: 2, ChatID: 2, Ref: pubRef(1), CloudDest: "mega-1"})
	if err != nil || d.Allowed || d.Reason != apperr.CodeUserPending {
		t.Fatalf("待审用户云盘提交应拒绝 USER_PENDING: %+v err=%v", d, err)
	}
	if got := countRequests(t, st, 2); got != 0 {
		t.Errorf("拒绝不应建 requests 行，得到 %d", got)
	}

	// 队列满竞态收尾：云盘请求行保持 cloud 标记
	raceSt, err := store.Open(ctx, filepath.Join(t.TempDir(), "race.db"), testLog())
	if err != nil {
		t.Fatalf("打开竞态测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = raceSt.Close() })
	fq := &failingQueue{Queue: queue.New(4)}
	raceSvc, err := New(Options{Store: raceSt, Queue: fq, Log: testLog(), Now: clock.Now})
	if err != nil {
		t.Fatalf("创建竞态服务失败: %v", err)
	}
	mustEnabledUser(t, raceSt, 1)
	if err := raceSt.UpdateUserCloudDownload(ctx, 1, store.CloudDownloadAllow); err != nil {
		t.Fatalf("授予云盘下载权限失败: %v", err)
	}
	d2, err := raceSvc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(9), CloudDest: "mega-1"})
	if err != nil || d2.Allowed || d2.Reason != apperr.CodeQueueFull {
		t.Fatalf("队列满应拒绝 QUEUE_FULL: %+v err=%v", d2, err)
	}
	r, err := raceSt.GetRequest(ctx, d2.RequestID)
	if err != nil {
		t.Fatalf("读取竞态落库行失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.ErrorCode != string(apperr.CodeQueueFull) {
		t.Fatalf("竞态行应 failed(QUEUE_FULL): %+v", r)
	}
	if r.DeliveryMode != store.DeliveryModeCloud || r.CloudDestination != "mega-1" {
		t.Fatalf("竞态收尾也应保持 cloud 标记与目的地: %+v", r)
	}
}

// UserDownloadStatus：/download 的只读预检——状态维度与裸链接同码（未授权/
// 待审/停用），状态通过后追加用户级下载权限维度（默认 owner 允许、普通用户
// 拒绝，显式值优先）；不扣额度、不落拒绝痕迹。
func TestUserDownloadStatus(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	for id, status := range map[int64]string{
		1: store.UserEnabled, 2: store.UserPending, 3: store.UserDisabled, 4: store.UserArchived,
	} {
		if _, err := st.CreateUser(ctx, store.User{ID: id, Status: status}); err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}
	// owner（默认允许）与显式允许的普通用户
	if _, err := st.CreateUser(ctx, store.User{ID: 5, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建 owner 失败: %v", err)
	}
	if err := st.SetOwner(ctx, 5, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}
	if _, err := st.CreateUser(ctx, store.User{ID: 6, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := st.UpdateUserCloudDownload(ctx, 6, store.CloudDownloadAllow); err != nil {
		t.Fatalf("授予云盘下载权限失败: %v", err)
	}

	cases := []struct {
		userID int64
		want   apperr.Code
	}{
		{1, apperr.CodeCloudDownloadDenied}, // 普通用户默认拒绝
		{2, apperr.CodeUserPending},
		{3, apperr.CodeUserDisabled},
		{4, apperr.CodeUserDisabled},
		{5, ""},                             // owner 默认允许
		{6, ""},                             // 显式允许
		{999, apperr.CodeUserNotAuthorized}, // 不存在同样按未授权
	}
	for _, tc := range cases {
		got, err := svc.UserDownloadStatus(ctx, tc.userID)
		if err != nil {
			t.Fatalf("%d：查询不应报错: %v", tc.userID, err)
		}
		if got != tc.want {
			t.Errorf("%d：期望拒绝码 %q，得到 %q", tc.userID, tc.want, got)
		}
	}

	// owner 被显式拒绝后同样返回权限拒绝
	if err := st.UpdateUserCloudDownload(ctx, 5, store.CloudDownloadDeny); err != nil {
		t.Fatalf("显式拒绝 owner 失败: %v", err)
	}
	if got, _ := svc.UserDownloadStatus(ctx, 5); got != apperr.CodeCloudDownloadDenied {
		t.Errorf("显式拒绝的 owner 应返回 CLOUD_DOWNLOAD_DENIED，得到 %q", got)
	}

	// 只读：不留拒绝痕迹、不动额度
	if u, _ := st.GetUser(ctx, 2); u.LastDeniedAt != 0 || u.LastDeniedReason != "" {
		t.Errorf("状态预检不应落拒绝痕迹: %+v", u)
	}
	if u, _ := st.GetUser(ctx, 1); u.LastDeniedAt != 0 || u.LastDeniedReason != "" {
		t.Errorf("权限预检不应落拒绝痕迹: %+v", u)
	}
	if usage, _ := st.GetUsage(ctx, 1, baseTime.In(defaultLoc).Format(dayFormat)); usage.Used != 0 {
		t.Errorf("状态预检不应扣额度，得到 %d", usage.Used)
	}
}

// 云盘提交的用户级权限权威复核：无权限（默认普通用户、显式拒绝的 owner）
// 在 Submit 内拒绝 CLOUD_DOWNLOAD_DENIED（不建行、不扣额度、落 last_denied_*），
// 裸链接不受影响。
func TestSubmitCloudPermissionDenied(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, q := newTestService(t, 4, clock.Now)
	jobs := startWorkers(t, q)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if _, err := st.CreateUser(ctx, store.User{ID: 2, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建 owner 失败: %v", err)
	}
	if err := st.SetOwner(ctx, 2, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}

	// 普通用户默认拒绝
	d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(1), CloudDest: "mega-1"})
	if err != nil || d.Allowed || d.Reason != apperr.CodeCloudDownloadDenied {
		t.Fatalf("普通用户默认应拒绝 CLOUD_DOWNLOAD_DENIED: %+v err=%v", d, err)
	}
	if got := countRequests(t, st, 1); got != 0 {
		t.Errorf("拒绝不应建 requests 行，得到 %d", got)
	}
	if u, _ := st.GetUser(ctx, 1); u.LastDeniedReason != string(apperr.CodeCloudDownloadDenied) {
		t.Errorf("权威复核应落拒绝痕迹，得到 %q", u.LastDeniedReason)
	}

	// owner 默认放行；显式拒绝后同样被拦
	d, err = svc.Submit(ctx, Submission{UserID: 2, ChatID: 2, Ref: pubRef(2), CloudDest: "mega-1"})
	if err != nil || !d.Allowed {
		t.Fatalf("owner 默认应放行云盘提交: %+v err=%v", d, err)
	}
	waitJob(t, jobs)
	if err := st.UpdateUserCloudDownload(ctx, 2, store.CloudDownloadDeny); err != nil {
		t.Fatalf("显式拒绝 owner 失败: %v", err)
	}
	clock.Advance(time.Minute)
	d, err = svc.Submit(ctx, Submission{UserID: 2, ChatID: 2, Ref: pubRef(3), CloudDest: "mega-1"})
	if err != nil || d.Allowed || d.Reason != apperr.CodeCloudDownloadDenied {
		t.Fatalf("显式拒绝的 owner 应被拦: %+v err=%v", d, err)
	}

	// 裸链接不受权限影响：普通用户照常通过
	clock.Advance(time.Minute)
	if d, err := svc.Submit(ctx, Submission{UserID: 1, ChatID: 1, Ref: pubRef(4)}); err != nil || !d.Allowed {
		t.Fatalf("裸链接不应受云盘权限影响: %+v err=%v", d, err)
	}
	waitJob(t, jobs)
}

// SetUserCloudDownload：更新三态并写审计，幂等不写审计，非法值拒绝。
func TestSetUserCloudDownload(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	if err := svc.SetUserCloudDownload(ctx, "admin", 1, store.CloudDownloadAllow); err != nil {
		t.Fatalf("设置显式允许失败: %v", err)
	}
	u, _ := st.GetUser(ctx, 1)
	if u.CloudDownload != store.CloudDownloadAllow || !u.EffectiveCloudDownload() {
		t.Fatalf("应落显式允许并生效: %+v", u)
	}
	if !containsAuditAction(st, "user.set_cloud_download") {
		t.Fatal("设置云盘下载权限应写审计")
	}

	// 幂等：同值再设不写审计
	n := countAudit(st)
	if err := svc.SetUserCloudDownload(ctx, "admin", 1, store.CloudDownloadAllow); err != nil {
		t.Fatalf("幂等设置失败: %v", err)
	}
	if got := countAudit(st); got != n {
		t.Errorf("幂等设置不应新增审计：%d → %d", n, got)
	}

	if err := svc.SetUserCloudDownload(ctx, "admin", 1, store.CloudDownloadDeny); err != nil {
		t.Fatalf("设置显式拒绝失败: %v", err)
	}
	if got := countAudit(st); got != n+1 {
		t.Errorf("变更应新增一条审计：%d → %d", n, got)
	}
	if err := svc.SetUserCloudDownload(ctx, "admin", 1, 3); err == nil {
		t.Fatal("非法值应被拒绝")
	}
	if err := svc.SetUserCloudDownload(ctx, "admin", 404, store.CloudDownloadAllow); err == nil {
		t.Fatal("不存在的用户应报错")
	}
}
