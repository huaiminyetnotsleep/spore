package queue

// worker 事件回调测试：任务结果上报（连续失败事件源）、
// 终态落库失败上报（数据库错误事件源）、开始前抽样点。

import (
	"context"
	"sync"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// recordingSink 记录 worker 全部事件回调的可编程假实现。
type recordingSink struct {
	mu      sync.Mutex
	tasks   []bool   // TaskResult 的 succeeded 序列
	clouds  []bool   // CloudResult 的 succeeded 序列（云盘任务）
	details []string // TaskResult/CloudResult 的 detail 序列（失败上下文）
	stores  int      // StoreWriteFailed 次数
	disks   int      // CheckTempDir 次数
}

func (r *recordingSink) TaskResult(_ context.Context, succeeded bool, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tasks = append(r.tasks, succeeded)
	r.details = append(r.details, detail)
}

func (r *recordingSink) CloudResult(_ context.Context, succeeded bool, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.clouds = append(r.clouds, succeeded)
	r.details = append(r.details, detail)
}

func (r *recordingSink) StoreWriteFailed(_ context.Context, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stores++
}

func (r *recordingSink) CheckTempDir(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.disks++
}

// newRequestJob 建一条 queued 请求行并返回关联任务（用户复用，供多任务测试）。
func newRequestJob(t *testing.T, s *store.Store) Job {
	t.Helper()
	return newRequestJobMsgID(t, s, 7)
}

// newRequestJobMsgID 以指定源消息 ID 建请求与任务（复用引入后，测试里
// 需要不同链接隔离"成功投递坐标被后续任务复用"的路径）。
func newRequestJobMsgID(t *testing.T, s *store.Store, msgID int) Job {
	t.Helper()
	r, err := s.CreateRequest(context.Background(), store.Request{
		UserID: 1, SourceKind: store.SourcePublic, ChannelKey: "example", MessageID: msgID,
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	return NewJob(1, 1, tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: msgID}, 0, r.ID)
}

func TestDiscardReportsInterruptedTask(t *testing.T) {
	s := openStore(t)
	job, req := newJobWithRequest(t, s, 0)
	sink := &recordingSink{}
	d := Deps{Sender: &fakeSender{}, Store: s, Events: sink, Log: testLog()}

	Discard(d)(context.Background(), job)

	got, err := s.GetRequest(context.Background(), req.ID)
	if err != nil {
		t.Fatalf("读取中断请求失败: %v", err)
	}
	if got.Status != store.RequestFailed || got.ErrorCode != string(apperr.CodeInterrupted) {
		t.Fatalf("丢弃任务应标记 INTERRUPTED，得到 %+v", got)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.tasks) != 1 || sink.tasks[0] {
		t.Fatalf("丢弃任务应上报失败结果，得到 %v", sink.tasks)
	}
}

func TestWorkerReportsTaskResults(t *testing.T) {
	s := openStore(t)
	if _, err := s.CreateUser(context.Background(), store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	job := newRequestJob(t, s)
	sink := &recordingSink{}

	// 成功任务：上报 true
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Events:  sink,
		Log:     testLog(),
	}
	done := make(chan struct{})
	go func() { Process(deps)(context.Background(), job); close(done) }()
	waitDone(t, done)

	// 失败任务：上报 false（用不同链接，避免命中上一任务的成功投递坐标复用）
	job2 := newRequestJobMsgID(t, s, 8)
	deps2 := Deps{
		Fetcher: &fakeFetcher{err: apperr.New(apperr.CodeChannelInaccessible, "no access")},
		Sender:  &fakeSender{},
		Store:   s,
		Events:  sink,
		Log:     testLog(),
	}
	done2 := make(chan struct{})
	go func() { Process(deps2)(context.Background(), job2); close(done2) }()
	waitDone(t, done2)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.tasks) != 2 || !sink.tasks[0] || sink.tasks[1] {
		t.Fatalf("应依次上报 true/false，得到 %v", sink.tasks)
	}
	if sink.disks != 2 {
		t.Fatalf("每个任务开始前应提供抽样点，得到 %d", sink.disks)
	}
	if sink.stores != 0 {
		t.Fatalf("正常落库不应上报写失败，得到 %d", sink.stores)
	}
}

func TestWorkerReportsStoreWriteFailed(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	sink := &recordingSink{}

	// 预先关闭数据库：终态落库失败 → 触发 StoreWriteFailed
	if err := s.Close(); err != nil {
		t.Fatalf("关闭测试库失败: %v", err)
	}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Events:  sink,
		Log:     testLog(),
	}
	done := make(chan struct{})
	go func() { Process(deps)(context.Background(), job); close(done) }()
	waitDone(t, done)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.stores == 0 {
		t.Fatal("终态落库失败应上报 StoreWriteFailed")
	}
	if len(sink.tasks) != 1 || !sink.tasks[0] {
		t.Fatalf("落库失败不影响任务结果上报，得到 %v", sink.tasks)
	}
}

func TestWorkerWorksWithoutEventSink(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Log:     testLog(),
	}
	done := make(chan struct{})
	go func() { Process(deps)(context.Background(), job); close(done) }()
	waitDone(t, done) // Events 为 nil 时正常完成，不 panic
}

// 云盘任务的结果同时进入 TaskResult 与独立的 CloudResult 计数；
// 普通（TG 投递）任务不触发 CloudResult。
func TestWorkerReportsCloudResults(t *testing.T) {
	s := openStore(t)
	sink := &recordingSink{}
	job := cloudJob(t, s)
	d := cloudDeps(t, s,
		fetcherWith(chunkInvoker{payload: []byte("0123456789")},
			cloudDocMsgSized(7, "", 10)),
		&fakeCloudSink{readReaders: true})
	d.Events = sink

	done := make(chan struct{})
	go func() { Process(d)(context.Background(), job); close(done) }()
	waitDone(t, done)

	sink.mu.Lock()
	tasks, clouds := len(sink.tasks), len(sink.clouds)
	firstCloud := len(sink.clouds) > 0 && sink.clouds[0]
	firstTask := len(sink.tasks) > 0 && sink.tasks[0]
	sink.mu.Unlock()
	if !firstTask || tasks != 1 {
		t.Fatalf("云盘任务成功应上报 TaskResult(true)，得到 %d 次", tasks)
	}
	if !firstCloud || clouds != 1 {
		t.Fatalf("云盘任务成功应上报 CloudResult(true)，得到 %d 次", clouds)
	}

	// 失败的云盘任务：两路都计失败（复用已建用户，另建请求行）
	r2, err := s.CreateRequest(context.Background(), store.Request{
		UserID: 1, SourceKind: store.SourcePublic, ChannelKey: "example", MessageID: 8,
	})
	if err != nil {
		t.Fatalf("创建第二条请求失败: %v", err)
	}
	job2 := NewJob(1, 1, tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: 8}, 0, r2.ID)
	job2.CloudDest = "mega-1"
	d2 := cloudDeps(t, s, &fakeFetcher{err: apperr.New(apperr.CodeChannelInaccessible, "no access")}, &fakeCloudSink{})
	d2.Events = sink
	done2 := make(chan struct{})
	go func() { Process(d2)(context.Background(), job2); close(done2) }()
	waitDone(t, done2)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.clouds) != 2 || sink.clouds[1] {
		t.Fatalf("云盘任务失败应上报 CloudResult(false)，得到 %v", sink.clouds)
	}
}

// 普通任务只走 TaskResult，不触碰云盘计数。
func TestWorkerPlainJobSkipsCloudResult(t *testing.T) {
	s := openStore(t)
	if _, err := s.CreateUser(context.Background(), store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	job := newRequestJob(t, s)
	sink := &recordingSink{}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Events:  sink,
		Log:     testLog(),
	}
	done := make(chan struct{})
	go func() { Process(deps)(context.Background(), job); close(done) }()
	waitDone(t, done)

	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.clouds) != 0 {
		t.Fatalf("普通任务不应上报 CloudResult，得到 %v", sink.clouds)
	}
}
