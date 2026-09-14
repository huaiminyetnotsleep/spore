package queue

// reuse_test.go — 转存频道复用（copyMessages 直拷）的行为测试：
// 命中零取数、绑频道用户补脚注（fetch+编辑）、复制失败回落全量并自愈重写、
// 开关与未装配关闭、成功投递写干净副本（含复用命中不重写）。

import (
	"context"
	"sync"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/dumpcache"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// countingFetcher 包装 fakeFetcher 并记录取数调用次数（复用命中断言用）。
type countingFetcher struct {
	*fakeFetcher
	calls int
}

func (c *countingFetcher) Fetch(ctx context.Context, ref tmeurl.SourceRef) ([]*tg.Message, error) {
	c.calls++
	return c.fakeFetcher.Fetch(ctx, ref)
}

// fakeChannels 是 ChannelLinksProvider 假实现：按用户返回可配置的公开
// 绑定频道链接（补脚注触发判定用）。
type fakeChannels struct {
	mu    sync.Mutex
	links map[int64][]message.ChannelLink
}

func (f *fakeChannels) PublicChannelLinks(_ context.Context, userID int64) ([]message.ChannelLink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.links[userID], nil
}

// seedSucceededRequest 造一条同链接历史成功行（meta 还原用）。
func seedSucceededRequest(t *testing.T, s *store.Store, userID int64, msgID int) {
	t.Helper()
	r, err := s.CreateRequest(context.Background(), store.Request{
		UserID: userID, SourceKind: store.SourcePublic, ChannelKey: "example", MessageID: msgID,
	})
	if err != nil {
		t.Fatalf("创建历史请求失败: %v", err)
	}
	_ = s.MarkRequestStarted(context.Background(), r.ID, 1)
	if err := s.FinishRequest(context.Background(), r.ID, store.RequestResult{
		Status: store.RequestSucceeded, MediaType: "photo",
	}); err != nil {
		t.Fatalf("落库历史终态失败: %v", err)
	}
}

func seedDumpEntry(t *testing.T, s *store.Store, msgID int, ids []int) {
	t.Helper()
	if _, err := s.InsertDumpEntry(context.Background(), store.DumpEntry{
		ChannelKey: "example", MessageID: msgID, DumpIDs: ids,
	}); err != nil {
		t.Fatalf("落缓存频道条目失败: %v", err)
	}
}

// reuseHarness 组装复用测试夹具：用户 1 提交 example/<msgID> 链接。
func reuseHarness(t *testing.T, s *store.Store, msgID int) Job {
	t.Helper()
	if _, err := s.CreateUser(context.Background(), store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	r, err := s.CreateRequest(context.Background(), store.Request{
		UserID: 1, SourceKind: store.SourcePublic, ChannelKey: "example", MessageID: msgID,
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	return NewJob(1, 1, tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: msgID}, 0, r.ID)
}

const testDumpChannel = int64(-1001234567890)

func runProcess(t *testing.T, d Deps, j Job) {
	t.Helper()
	done := make(chan struct{})
	go func() { Process(d)(context.Background(), j); close(done) }()
	waitDone(t, done)
}

// TestReuseFromDumpHit 命中：不取数（无绑定用户零 fetch）、终态 succeeded +
// delivery_mode=reuse、频道副本拿到复制后的消息、不再重写缓存频道副本。
func TestReuseFromDumpHit(t *testing.T) {
	s := openStore(t)
	job := reuseHarness(t, s, 7)
	seedSucceededRequest(t, s, 1, 7)
	seedDumpEntry(t, s, 7, []int{11, 12})
	sender := &fakeSender{}
	fetcher := &countingFetcher{fakeFetcher: &fakeFetcher{
		msgs: []*tg.Message{{ID: 7, Message: "hello"}}}} // 命中则不会被调用
	copier := &fakeCopier{}
	d := Deps{Fetcher: fetcher, Sender: sender, Store: s, Log: testLog(),
		Dump: dumpcache.New(sender, s, func() int64 { return testDumpChannel }, testLog()), Copier: copier}

	runProcess(t, d, job)

	if fetcher.calls != 0 {
		t.Fatalf("无绑定用户命中复用不应取数，得到 %d 次", fetcher.calls)
	}
	if len(sender.copyCalls) != 1 {
		t.Fatalf("应恰好一次从缓存频道复制，得到 %d", len(sender.copyCalls))
	}
	call := sender.copyCalls[0]
	if call.FromChatID != testDumpChannel || call.ChatID != job.ChatID {
		t.Fatalf("复制应从缓存频道到目标聊天: %+v", call)
	}
	if len(call.MessageIDs) != 2 || call.MessageIDs[0] != 11 || call.MessageIDs[1] != 12 {
		t.Fatalf("复制消息 ID 应为条目坐标 [11 12]: %v", call.MessageIDs)
	}
	req, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if req.Status != store.RequestSucceeded || req.DeliveryMode != store.DeliveryModeReuse {
		t.Fatalf("终态应为 succeeded+reuse: %+v", req)
	}
	if req.MediaType != "photo" {
		t.Fatalf("meta 应从历史行还原（photo）: %s", req.MediaType)
	}
	if len(copier.msgLists) != 1 || len(copier.msgLists[0]) != 2 || copier.msgLists[0][0] != 400 {
		t.Fatalf("频道副本应拿到复制后的消息 ID: %v", copier.msgLists)
	}
	// 复用命中不重写副本：无单条复制、无对缓存频道的编辑
	if len(sender.singleCopyCalls) != 0 || len(sender.captionEdits) != 0 {
		t.Fatalf("复用命中不应重写副本: %+v %+v", sender.singleCopyCalls, sender.captionEdits)
	}
}

// TestReuseFromDumpFootnote 绑定频道的用户命中：fetch 一次并编辑复制出的
// 首条消息补脚注。
func TestReuseFromDumpFootnote(t *testing.T) {
	s := openStore(t)
	job := reuseHarness(t, s, 7)
	seedDumpEntry(t, s, 7, []int{11})
	sender := &fakeSender{}
	fetcher := &countingFetcher{fakeFetcher: &fakeFetcher{
		msgs: []*tg.Message{{ID: 7, Message: "hello"}}}}
	ch := &fakeChannels{links: map[int64][]message.ChannelLink{
		1: {{Label: "用户频道", URL: "https://t.me/userchan"}},
	}}
	d := Deps{Fetcher: fetcher, Sender: sender, Store: s, Log: testLog(),
		Dump: dumpcache.New(sender, s, func() int64 { return testDumpChannel }, testLog()), Channels: ch}

	runProcess(t, d, job)

	if fetcher.calls != 1 {
		t.Fatalf("绑定频道用户应 fetch 一次，得到 %d 次", fetcher.calls)
	}
	// fetch 回来的是文本消息：补脚注走 EditMessageText（记录在 edits）
	if len(sender.edits) != 1 {
		t.Fatalf("应编辑首条复制消息补脚注: %v", sender.edits)
	}
	req, _ := s.GetRequest(context.Background(), job.RequestID)
	if req.DeliveryMode != store.DeliveryModeReuse {
		t.Fatalf("补脚注路径也应标记 reuse: %+v", req)
	}
}

// TestReuseFromDumpCopyFailureSelfHeals 复制失败回落完整下载上传，
// 任务成功且重写缓存频道条目（自愈）。
func TestReuseFromDumpCopyFailureSelfHeals(t *testing.T) {
	s := openStore(t)
	job := reuseHarness(t, s, 7)
	seedDumpEntry(t, s, 7, []int{11}) // 已失效的旧条目
	sender := &fakeSender{copyErr: func(copyCall) error {
		return errCopyFailed
	}}
	fetcher := &countingFetcher{fakeFetcher: &fakeFetcher{
		msgs: []*tg.Message{{ID: 7, Message: "hello"}}}}
	d := Deps{Fetcher: fetcher, Sender: sender, Store: s, Log: testLog(),
		Dump: dumpcache.New(sender, s, func() int64 { return testDumpChannel }, testLog())}

	runProcess(t, d, job)

	if fetcher.calls == 0 {
		t.Fatal("复制失败应回落取数")
	}
	req, _ := s.GetRequest(context.Background(), job.RequestID)
	if req.Status != store.RequestSucceeded || req.DeliveryMode == store.DeliveryModeReuse {
		t.Fatalf("回落后应按正常链路成功: %+v", req)
	}
	// 全量成功后写入新的干净副本条目（文本形态走 SendMessage：用户消息 + 副本共 2 条）
	if len(sender.sent) != 2 {
		t.Fatalf("全量成功后应写干净副本: %d 条", len(sender.sent))
	}
	e, err := s.LatestDumpEntry(context.Background(), "example", 7)
	if err != nil || len(e.DumpIDs) != 1 {
		t.Fatalf("条目应被重写（自愈）: %+v err=%v", e, err)
	}
}

// TestReuseDisabledBySwitch 复用开关关闭：不查条目、走完整链路、也不写副本。
func TestReuseDisabledBySwitch(t *testing.T) {
	s := openStore(t)
	job := reuseHarness(t, s, 7)
	seedDumpEntry(t, s, 7, []int{11})
	sender := &fakeSender{}
	fetcher := &countingFetcher{fakeFetcher: &fakeFetcher{
		msgs: []*tg.Message{{ID: 7, Message: "hello"}}}}
	d := Deps{Fetcher: fetcher, Sender: sender, Store: s, Log: testLog(),
		Dump: dumpcache.New(sender, s, func() int64 { return testDumpChannel }, testLog())}
	d.ReuseEnabled = func() bool { return false }

	runProcess(t, d, job)

	if len(sender.copyCalls) != 0 {
		t.Fatalf("开关关闭不应复制: %+v", sender.copyCalls)
	}
	if fetcher.calls == 0 {
		t.Fatal("开关关闭应走完整链路")
	}
	if len(sender.sent) != 1 || len(sender.singleCopyCalls) != 0 {
		t.Fatalf("开关关闭也不应写副本: sent=%v copy=%v", sender.sent, sender.singleCopyCalls)
	}
}

// TestReuseWithoutDumpService 未配置缓存频道（Dump 为 nil）：无复用，
// 行为与复用引入前一致（其他既有测试即此形态，此处显式断言副本未写）。
func TestReuseWithoutDumpService(t *testing.T) {
	s := openStore(t)
	job := reuseHarness(t, s, 7)
	seedDumpEntry(t, s, 7, []int{11}) // 即使历史库残留条目也不复用
	sender := &fakeSender{}
	fetcher := &countingFetcher{fakeFetcher: &fakeFetcher{
		msgs: []*tg.Message{{ID: 7, Message: "hello"}}}}

	runProcess(t, Deps{Fetcher: fetcher, Sender: sender, Store: s, Log: testLog()}, job)

	if len(sender.copyCalls) != 0 || fetcher.calls == 0 {
		t.Fatalf("未装配缓存频道应无复用: copies=%d fetches=%d", len(sender.copyCalls), fetcher.calls)
	}
	req, _ := s.GetRequest(context.Background(), job.RequestID)
	if req.DeliveryMode == store.DeliveryModeReuse {
		t.Fatalf("未装配不应标记 reuse: %+v", req)
	}
}

// TestWriteCleanAfterFullRun 全量投递成功后写干净副本（单媒体走
// CopyMessage 带干净 caption，坐标落条目）。
func TestWriteCleanAfterFullRun(t *testing.T) {
	s := openStore(t)
	job := reuseHarness(t, s, 7)
	sender := &fakeSender{}
	fetcher := &countingFetcher{fakeFetcher: &fakeFetcher{
		msgs: []*tg.Message{{ID: 7, Message: "hello"}}}}

	runProcess(t, Deps{Fetcher: fetcher, Sender: sender, Store: s, Log: testLog(),
		Dump: dumpcache.New(sender, s, func() int64 { return testDumpChannel }, testLog())}, job)

	if len(sender.sent) != 2 { // 用户投递 1 条 + 缓存频道干净副本 1 条
		t.Fatalf("文本投递与干净副本应各一次 SendMessage: %d 条", len(sender.sent))
	}
	if _, err := s.LatestDumpEntry(context.Background(), "example", 7); err != nil {
		t.Fatalf("全量成功应写干净副本条目: %v", err)
	}
}

var errCopyFailed = &staticErr{}

type staticErr struct{}

func (*staticErr) Error() string { return "message to copy not found" }
