package queue

import (
	"context"
	"sync"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// fakeCopier 记录频道副本投递调用；outcome 模拟置顶结果返回值。
type fakeCopier struct {
	mu       sync.Mutex
	userIDs  []int64
	chatIDs  []int64
	msgLists [][]int
	pins     []bool
	outcome  PinOutcome
}

func (f *fakeCopier) CopyToChannels(_ context.Context, botID, userID, userChatID int64, msgIDs []int, pin bool) PinOutcome {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userIDs = append(f.userIDs, userID)
	f.chatIDs = append(f.chatIDs, userChatID)
	f.msgLists = append(f.msgLists, append([]int(nil), msgIDs...))
	f.pins = append(f.pins, pin)
	return f.outcome
}

func TestProcessSuccessCopiesToBoundChannels(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	copier := &fakeCopier{}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Copier:  copier,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	if len(copier.msgLists) != 1 {
		t.Fatalf("成功任务应触发一次频道副本投递，得到 %d 次", len(copier.msgLists))
	}
	if copier.userIDs[0] != 1 || copier.chatIDs[0] != job.ChatID {
		t.Fatalf("副本应投递到任务用户的绑定频道: user=%d chat=%d", copier.userIDs[0], copier.chatIDs[0])
	}
	// 文本消息发送返回 fakeSender 的自增 ID（101），应出现在副本消息列表中
	if len(copier.msgLists[0]) != 1 || copier.msgLists[0][0] != 101 {
		t.Fatalf("副本消息 ID 列表不符: %v", copier.msgLists[0])
	}
}

func TestProcessFailureSkipsChannelCopy(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	copier := &fakeCopier{}
	deps := Deps{
		Fetcher: &fakeFetcher{err: apperr.New(apperr.CodeChannelInaccessible, "no access")},
		Sender:  &fakeSender{},
		Store:   s,
		Copier:  copier,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	if len(copier.msgLists) != 0 {
		t.Fatalf("失败任务不应触发频道副本投递: %v", copier.msgLists)
	}
}

func TestProcessWithoutCopierIsNoop(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Log:     testLog(),
	}

	// Copier 为 nil：任务正常完成，不 panic
	Process(deps)(context.Background(), job)
}

func TestProcessSkipsChannelCopyWhenSwitchOff(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	copier := &fakeCopier{}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Copier:  copier,
		// 运行设置的频道同步开关关闭：跳过副本投递（绑定关系保留）
		ChannelCopyEnabled: func() bool { return false },
		Log:                testLog(),
	}

	Process(deps)(context.Background(), job)

	if len(copier.msgLists) != 0 {
		t.Fatalf("开关关闭时不应投递频道副本: %v", copier.msgLists)
	}
	// 任务本身仍应成功（requests 终态 succeeded）
	req, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if req.Status != store.RequestSucceeded {
		t.Fatalf("开关关闭只影响副本投递，任务应成功，得到 %s", req.Status)
	}
}
