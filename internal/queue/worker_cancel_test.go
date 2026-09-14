package queue

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

func TestProcessRequestCancellationSkipsExecution(t *testing.T) {
	s := openStore(t)
	job, request := newJobWithRequest(t, s, 55)
	sender := &fakeSender{}
	fetcher := &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "must not fetch"}}}
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(ErrRequestCancelled)

	Process(Deps{Fetcher: fetcher, Sender: sender, Store: s, Log: testLog()})(ctx, job)

	got, err := s.GetRequest(context.Background(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "queued" {
		t.Fatalf("直接收到取消 cause 时不应改变请求状态: %+v", got)
	}
	if len(sender.texts()) != 0 {
		t.Fatal("用户取消不应发送失败提示")
	}
	// 占位消息不删除，改为取消终态文案（带来源链接与删除线样式）
	if len(sender.edits) != 1 || sender.edits[0] != CancelledStatusHTML(job.Ref) {
		t.Fatalf("占位消息应编辑为取消文案: %v", sender.edits)
	}
	if len(sender.deleted) != 0 {
		t.Fatalf("取消不应删除占位消息: %v", sender.deleted)
	}
}

func TestCancelledStatusHTML(t *testing.T) {
	// 正常链接：文案 + 删除线样式的来源消息链接
	ref := tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: 7}
	want := "任务已取消。\n<s><a href=\"https://t.me/example/7\">https://t.me/example/7</a></s>"
	if got := CancelledStatusHTML(ref); got != want {
		t.Fatalf("取消文案应带删除线链接:\n want %q\n got  %q", want, got)
	}

	// 链接不可重建（如私有频道键数据异常）：退化为纯取消文案
	if got := CancelledStatusHTML(tmeurl.SourceRef{}); got != StatusCancelledHTML {
		t.Fatalf("无链接应退化为纯取消文案，得到 %q", got)
	}
}

// reachedFetcher 在 Fetch 入口发信号后阻塞到 ctx 取消，模拟"执行中途被取消"。
type reachedFetcher struct {
	reached chan struct{}
}

func (f *reachedFetcher) Fetch(ctx context.Context, _ tmeurl.SourceRef) ([]*tg.Message, error) {
	close(f.reached)
	<-ctx.Done()
	return nil, ctx.Err()
}

func (f *reachedFetcher) RefreshMedia(context.Context, tmeurl.SourceRef, int) (message.Media, bool, error) {
	return message.Media{}, false, nil
}

func (f *reachedFetcher) API() *tg.Client { return nil }

func TestProcessActiveCancellationEditsStatus(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 55)
	// 从 queued 正常出队 claim，取消发生在 Fetch 执行中途
	sender := &fakeSender{}
	fetcher := &reachedFetcher{reached: make(chan struct{})}
	ctx, cancel := context.WithCancelCause(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		Process(Deps{Fetcher: fetcher, Sender: sender, Store: s, Log: testLog()})(ctx, job)
	}()
	<-fetcher.reached
	cancel(ErrRequestCancelled)
	<-done

	// 占位消息编辑为取消文案（带来源链接），且不发送失败提示、不删除占位
	if len(sender.edits) != 1 || sender.edits[0] != CancelledStatusHTML(job.Ref) {
		t.Fatalf("活动任务取消应把占位消息编辑为取消文案: %v", sender.edits)
	}
	if len(sender.deleted) != 0 || len(sender.texts()) != 0 {
		t.Fatalf("取消不应删除占位或发送失败提示: deleted=%v texts=%v", sender.deleted, sender.texts())
	}
}
