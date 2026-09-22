package queue

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// TestProcessBotDisabledSkipsFetch 受理 bot 停用（401）预检：任务出队即
// 标记 failed(BOT_DISABLED)，不调用取数与发送主链路，失败提示经回退通道
// 送达用户。
func TestProcessBotDisabledSkipsFetch(t *testing.T) {
	s := openStore(t)
	job, r := newJobWithRequest(t, s, 0)
	job.BotID = 42
	fetcher := &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}}
	sender := &fakeSender{}
	deps := Deps{
		Fetcher:     fetcher,
		Sender:      sender,
		Store:       s,
		Log:         testLog(),
		BotDisabled: func(botID int64) bool { return botID == 42 },
	}

	done := make(chan struct{})
	go func() { Process(deps)(context.Background(), job); close(done) }()
	waitDone(t, done)

	if fetcher.refreshCalls != 0 {
		t.Fatalf("停用 bot 的任务不应触发媒体刷新: calls=%d", fetcher.refreshCalls)
	}
	got, err := s.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if got.Status != store.RequestFailed || got.ErrorCode != string(apperr.CodeBotDisabled) {
		t.Fatalf("终态应为 failed(BOT_DISABLED): %+v", got)
	}
	if got.ErrorDetail == "" {
		t.Fatalf("error_detail 应携带根因: %+v", got)
	}
	texts := sender.texts()
	if len(texts) != 1 {
		t.Fatalf("应向用户发送一条停用提示: %v", texts)
	}
	want := failureNoticeHTML(apperr.CodeBotDisabled, job.Ref)
	if texts[0] != want {
		t.Fatalf("停用提示文案不符:\nwant: %q\ngot:  %q", want, texts[0])
	}

}
