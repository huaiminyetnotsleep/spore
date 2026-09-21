package queue

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// newPinJobWithRequest 在测试库中建好用户与 pin 标记的 queued 请求行，
// 返回关联任务（置顶收尾流程专用）。
func newPinJobWithRequest(t *testing.T, s *store.Store) (Job, store.Request) {
	t.Helper()
	if _, err := s.CreateUser(context.Background(), store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	r, err := s.CreateRequest(context.Background(), store.Request{
		UserID: 1, SourceKind: store.SourcePublic, ChannelKey: "example", MessageID: 7, Pin: true,
	})
	if err != nil {
		t.Fatalf("创建置顶请求失败: %v", err)
	}
	return NewJob(1, 1, tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: 7},
		0, r.ID), r
}

// TestProcessPinTaskBypassesSwitchAndRecordsResult：pin 任务绕过频道同步
// 开关，置顶结果（成功数/总数）回写请求行并给用户发确认文案。
func TestProcessPinTaskBypassesSwitchAndRecordsResult(t *testing.T) {
	s := openStore(t)
	job, r := newPinJobWithRequest(t, s)
	copier := &fakeCopier{pinOK: 2, pinTotal: 3}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Copier:  copier,
		// 同步开关关闭：pin 任务必须仍然投递副本并置顶（显式意图优先）
		ChannelCopyEnabled: func() bool { return false },
		Log:                testLog(),
	}

	Process(deps)(context.Background(), job)

	if len(copier.pins) != 1 || !copier.pins[0] {
		t.Fatalf("pin 任务应带置顶标记投递副本（绕过开关）: %v", copier.pins)
	}
	got, err := s.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if !got.Pin || got.PinOK != 2 || got.PinTotal != 3 {
		t.Fatalf("置顶结果应回写请求行: %+v", got)
	}
}

// TestProcessPinTaskPartialResultText：置顶部分失败时确认文案带计数与失败提示。
func TestProcessPinTaskPartialResultText(t *testing.T) {
	s := openStore(t)
	job, _ := newPinJobWithRequest(t, s)
	sender := &fakeSender{}
	copier := &fakeCopier{pinOK: 1, pinTotal: 2}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Store:   s,
		Copier:  copier,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	var confirm string
	for _, text := range sender.texts() {
		if strings.Contains(text, "置顶") {
			confirm = text
		}
	}
	if confirm == "" || !strings.Contains(confirm, "已置顶到 1/2") {
		t.Fatalf("应回复带计数的置顶确认，得到 %q", sender.texts())
	}
}

// TestProcessPinTaskZeroBindingsHint：完成时无绑定（0/0）提示绑定前提，
// 结果照常回写。
func TestProcessPinTaskZeroBindingsHint(t *testing.T) {
	s := openStore(t)
	job, r := newPinJobWithRequest(t, s)
	sender := &fakeSender{}
	copier := &fakeCopier{}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Store:   s,
		Copier:  copier,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	got, err := s.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if !got.Pin || got.PinOK != 0 || got.PinTotal != 0 {
		t.Fatalf("无绑定应回写 0/0: %+v", got)
	}
	var confirm string
	for _, text := range sender.texts() {
		if strings.Contains(text, "尚未绑定") {
			confirm = text
		}
	}
	if confirm == "" {
		t.Fatalf("应回复零绑定提示，得到 %q", sender.texts())
	}
}
