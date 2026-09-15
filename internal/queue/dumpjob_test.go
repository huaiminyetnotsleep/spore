package queue

// dumpjob_test.go — 仅缓存补写任务（DumpOnly）的行为测试：
// 干净副本直达缓存频道（目标 chatID 与无脚注 caption）、文本/媒体/相册
// 全形态落 dump_entries、失败不向用户发送任何消息、执行时复核命中已有
// 副本直接成功（零取数零发送）。

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/dumpcache"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// errFakeSend 是可注入假 Sender 的媒体发送失败错误。
var errFakeSend = errors.New("测试假发送失败")

// chatRecordingSender 在 fakeSender 之上记录各发送调用的目标 chatID
//（fakeSender 本身丢弃该参数），供"目标是缓存频道而非用户"断言使用。
type chatRecordingSender struct {
	*fakeSender
	textChats  []int64
	mediaChats []int64
}

func (c *chatRecordingSender) SendMessage(ctx context.Context, chatID int64, html string) (int, error) {
	c.textChats = append(c.textChats, chatID)
	return c.fakeSender.SendMessage(ctx, chatID, html)
}

func (c *chatRecordingSender) SendMedia(ctx context.Context, chatID int64, m message.Media, caption message.Caption, reader io.Reader) (int, error) {
	c.mediaChats = append(c.mediaChats, chatID)
	return c.fakeSender.SendMedia(ctx, chatID, m, caption, reader)
}

// dumpJob 构造 DumpOnly 任务（用户 1 提交 example/<msgID>）。
func dumpJob(t *testing.T, s *store.Store, msgID int) Job {
	t.Helper()
	job := reuseHarness(t, s, msgID)
	job.DumpOnly = true
	return job
}

// dumpDeps 组装补写任务依赖：Dump 指向测试缓存频道，Sender 记录目标 chatID。
func dumpDeps(t *testing.T, s *store.Store, fetcher Fetcher, sender *chatRecordingSender) Deps {
	t.Helper()
	return Deps{
		Fetcher: fetcher,
		Sender:  sender,
		Media:   mediaOptionsForTest(t),
		Store:   s,
		Log:     testLog(),
		Dump:    dumpcache.New(sender, s, func() int64 { return testDumpChannel }, testLog()),
	}
}

// dumpEntryOf 读取指定链接的最新缓存条目（不存在时 fail）。
func dumpEntryOf(t *testing.T, s *store.Store, msgID int) store.DumpEntry {
	t.Helper()
	e, err := s.LatestDumpEntry(context.Background(), "example", msgID)
	if err != nil {
		t.Fatalf("应已落缓存频道条目: %v", err)
	}
	return e
}

// 单媒体补写：SendMedia 目标为缓存频道、caption 带原链接但不带脚注、
// dump_entries 落坐标、终态 succeeded/dump，用户侧零消息。
func TestDumpJobSingleMedia(t *testing.T) {
	s := openStore(t)
	job := dumpJob(t, s, 7)
	sender := &chatRecordingSender{fakeSender: &fakeSender{}}
	fetcher := fetcherWith(errInvoker{}, docMsg(7, 1201))
	d := dumpDeps(t, s, fetcher, sender)

	runProcess(t, d, job)

	if len(sender.mediaChats) != 1 || sender.mediaChats[0] != testDumpChannel {
		t.Fatalf("媒体应直达缓存频道: %v", sender.mediaChats)
	}
	if len(sender.textChats) != 0 {
		t.Fatalf("补写任务不应向任何聊天发文本: %v", sender.textChats)
	}
	calls := sender.mediaCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("应恰好一次媒体发送: %+v", calls)
	}
	if !strings.Contains(calls[0].Caption.Text, "https://t.me/example/7") {
		t.Errorf("干净副本 caption 应带原链接: %q", calls[0].Caption.Text)
	}
	if len(calls[0].Caption.Channels) != 0 {
		t.Errorf("干净副本 caption 不应带用户脚注: %+v", calls[0].Caption.Channels)
	}

	e := dumpEntryOf(t, s, 7)
	if len(e.DumpIDs) != 1 || e.DumpIDs[0] != 201 {
		t.Errorf("缓存条目坐标应为发送所得消息 ID: %+v", e)
	}

	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestSucceeded || r.DeliveryMode != store.DeliveryModeDump {
		t.Errorf("终态应为 succeeded/dump: %+v", r)
	}
}

// 文本补写：干净渲染发送到缓存频道一次（原链接有、脚注无），用户侧零消息。
func TestDumpJobTextOnly(t *testing.T) {
	s := openStore(t)
	job := dumpJob(t, s, 7)
	sender := &chatRecordingSender{fakeSender: &fakeSender{}}
	fetcher := &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}}
	d := dumpDeps(t, s, fetcher, sender)

	runProcess(t, d, job)

	if len(sender.textChats) != 1 || sender.textChats[0] != testDumpChannel {
		t.Fatalf("文本应直达缓存频道一次: %v", sender.textChats)
	}
	texts := sender.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "hello") ||
		!strings.Contains(texts[0], "https://t.me/example/7") {
		t.Fatalf("干净渲染应含正文与原链接: %q", texts)
	}
	dumpEntryOf(t, s, 7)

	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestSucceeded || r.DeliveryMode != store.DeliveryModeDump {
		t.Errorf("终态应为 succeeded/dump: %+v", r)
	}
}

// 相册补写：整组原子发送到缓存频道（保组），坐标落条目。
func TestDumpJobAlbumGroup(t *testing.T) {
	s := openStore(t)
	job := dumpJob(t, s, 7)
	sender := &chatRecordingSender{fakeSender: &fakeSender{
		groupable: func(message.Media) bool { return true },
	}}
	msgs := []*tg.Message{docMsg(7, 1201), docMsg(8, 1202)}
	msgs[0].SetGroupedID(42)
	msgs[1].SetGroupedID(42)
	d := dumpDeps(t, s, fetcherWith(errInvoker{}, msgs...), sender)

	runProcess(t, d, job)

	calls := sender.albumCallsSnapshot()
	if len(calls) != 1 || len(calls[0].Kinds) != 2 {
		t.Fatalf("相册应整组发送一次且含两个成员: %+v", calls)
	}
	for _, cap := range calls[0].Captions {
		if len(cap.Channels) != 0 {
			t.Errorf("相册成员 caption 不应带用户脚注: %+v", cap.Channels)
		}
	}
	e := dumpEntryOf(t, s, 7)
	if len(e.DumpIDs) != 2 {
		t.Errorf("缓存条目应记录整组坐标: %+v", e)
	}
}

// 发送失败：终态 failed，用户侧零消息零编辑（失败只落库，Web 列表可见）。
func TestDumpJobFailureNotifiesNobody(t *testing.T) {
	s := openStore(t)
	job := dumpJob(t, s, 7)
	sender := &chatRecordingSender{fakeSender: &fakeSender{
		mediaErr: func(mediaCall) error { return errFakeSend },
	}}
	d := dumpDeps(t, s, fetcherWith(errInvoker{}, docMsg(7, 1201)), sender)

	runProcess(t, d, job)

	if len(sender.textChats) != 0 || len(sender.edits) != 0 {
		t.Fatalf("失败补写不应向用户发送提示或编辑占位: texts=%v edits=%v",
			sender.textChats, sender.edits)
	}
	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.DeliveryMode != store.DeliveryModeDump {
		t.Errorf("终态应为 failed/dump: %+v", r)
	}
	if _, err := s.LatestDumpEntry(context.Background(), "example", 7); err == nil {
		t.Error("失败补写不应落缓存条目")
	}
}

// 执行时复核命中已有副本：零取数零发送直接成功（覆盖提交入口预检之后的
// 并发窗口），终态 succeeded/dump。
func TestDumpJobExistingEntrySkips(t *testing.T) {
	s := openStore(t)
	job := dumpJob(t, s, 7)
	seedDumpEntry(t, s, 7, []int{11})
	sender := &chatRecordingSender{fakeSender: &fakeSender{}}
	fetcher := &countingFetcher{fakeFetcher: &fakeFetcher{
		msgs: []*tg.Message{{ID: 7, Message: "hello"}}}}
	d := dumpDeps(t, s, fetcher, sender)

	runProcess(t, d, job)

	if fetcher.calls != 0 {
		t.Fatalf("命中已有副本不应取数，得到 %d 次", fetcher.calls)
	}
	if len(sender.textChats) != 0 || len(sender.mediaChats) != 0 {
		t.Fatalf("命中已有副本不应发送: %v %v", sender.textChats, sender.mediaChats)
	}
	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestSucceeded || r.DeliveryMode != store.DeliveryModeDump {
		t.Errorf("跳过补写应落 succeeded/dump: %+v", r)
	}
}

// 未配置缓存频道（Dump 未装配）：明确失败落库，不静默降级、不打扰用户。
func TestDumpJobWithoutDumpFails(t *testing.T) {
	s := openStore(t)
	job := dumpJob(t, s, 7)
	sender := &chatRecordingSender{fakeSender: &fakeSender{}}
	d := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Media:   mediaOptionsForTest(t),
		Store:   s,
		Log:     testLog(),
	}

	runProcess(t, d, job)

	if len(sender.textChats) != 0 {
		t.Fatalf("装配缺失失败不应打扰用户: %v", sender.textChats)
	}
	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.ErrorCode == "" {
		t.Errorf("装配缺失应明确失败: %+v", r)
	}
}
