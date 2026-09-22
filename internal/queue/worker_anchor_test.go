package queue

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// TestProcessSuccessRecordsMediaAnchor 覆盖成功任务的投递媒体坐标落库
// （sent_messages kind=media）：文本消息发送返回的 fakeSender 自增 ID
// （101）应记录为可回复锚点；失败任务不落 media 行。
func TestProcessSuccessRecordsMediaAnchor(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Log:     testLog(),
	}
	Process(deps)(context.Background(), job)

	m, err := s.FindSentMessage(context.Background(), job.BotID, job.ChatID, 101)
	if err != nil {
		t.Fatalf("成功任务应落投递媒体锚点: %v", err)
	}
	if m.RequestID != job.RequestID || m.Kind != store.SentKindMedia {
		t.Fatalf("媒体锚点不符: %+v", m)
	}

	// 失败任务：fetch 失败，无投递消息发出（101 之外的坐标不存在）；
	// 失败通知是失败路径唯一的 SendMessage（TryDeleteStatus 只删不发），ID=101
	s2 := openStore(t)
	job2, _ := newJobWithRequest(t, s2, 0)
	deps2 := Deps{
		Fetcher: &fakeFetcher{err: apperr.New(apperr.CodeChannelInaccessible, "no access")},
		Sender:  &fakeSender{},
		Store:   s2,
		Log:     testLog(),
	}
	Process(deps2)(context.Background(), job2)
	if _, err := s2.FindSentMessage(context.Background(), job2.BotID, job2.ChatID, 102); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("失败任务不应有投递媒体锚点，得到 %v", err)
	}
	f, err := s2.FindSentMessage(context.Background(), job2.BotID, job2.ChatID, 101)
	if err != nil {
		t.Fatalf("失败任务应落失败通知锚点: %v", err)
	}
	if f.RequestID != job2.RequestID || f.Kind != store.SentKindFailure {
		t.Fatalf("失败通知锚点不符: %+v", f)
	}
}

// TestProcessRetryPromptRecordsStatusAnchor 覆盖重试任务补发占位的坐标
// 落库（kind=status）：NeedsStatusPrompt 且 StatusMsgID=0 时补发的新占位
// 应可回复反查。
func TestProcessRetryPromptRecordsStatusAnchor(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	job.NeedsStatusPrompt = true
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Log:     testLog(),
	}
	Process(deps)(context.Background(), job)

	// 补发占位是本轮第一条 SendMessage（ID=101），随后投递文本是 102
	m, err := s.FindSentMessage(context.Background(), job.BotID, job.ChatID, 101)
	if err != nil {
		t.Fatalf("重试补发占位应落 status 锚点: %v", err)
	}
	if m.RequestID != job.RequestID || m.Kind != store.SentKindStatus {
		t.Fatalf("占位锚点不符: %+v", m)
	}
	media, err := s.FindSentMessage(context.Background(), job.BotID, job.ChatID, 102)
	if err != nil || media.Kind != store.SentKindMedia {
		t.Fatalf("重试任务投递消息应落 media 锚点: %+v err=%v", media, err)
	}
}
