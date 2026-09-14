package mtproto

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func TestChatsOfUpdates(t *testing.T) {
	ch := &tg.Channel{ID: 42, Title: "目标频道"}
	updates := &tg.Updates{Chats: []tg.ChatClass{ch, &tg.Chat{ID: 7}}}
	got := chatsOfUpdates(updates)
	if len(got) != 2 {
		t.Fatalf("期望提取 2 个 chat，得到 %d", len(got))
	}

	if got := chatsOfUpdates(&tg.UpdatesTooLong{}); got != nil {
		t.Fatalf("UpdatesTooLong 应返回 nil，得到 %v", got)
	}
	if got := chatsOfUpdates(&tg.UpdatesCombined{Chats: []tg.ChatClass{ch}}); len(got) != 1 {
		t.Fatalf("UpdatesCombined 提取失败")
	}
}

func TestFirstChannel(t *testing.T) {
	ch, ok := firstChannel([]tg.ChatClass{&tg.Chat{ID: 1}, &tg.Channel{ID: 42, Title: "X"}})
	if !ok || ch.ID != 42 {
		t.Fatalf("期望取到 ID=42 的频道，得到 ok=%v id=%d", ok, ch.ID)
	}
	if _, ok := firstChannel([]tg.ChatClass{&tg.Chat{ID: 1}}); ok {
		t.Fatalf("无频道时应返回 false")
	}
}

func TestClassifyMembershipError(t *testing.T) {
	cases := []struct {
		err  error
		code apperr.Code
	}{
		{tgerr.New(500, "INVITE_HASH_EXPIRED"), apperr.CodeInvalidURL},
		{tgerr.New(400, "CHANNEL_PRIVATE"), apperr.CodeChannelInaccessible},
		{tgerr.New(400, "USER_ALREADY_PARTICIPANT"), apperr.CodeChannelInaccessible},
		{tgerr.New(400, "FLOOD_WAIT_X"), apperr.CodeRateLimited},
		{errors.New("别的错误"), apperr.CodeInternal},
	}
	for _, c := range cases {
		ae := classifyMembershipError(c.err)
		if ae.Code != c.code {
			t.Fatalf("错误 %v 归类为 %s，期望 %s", c.err, ae.Code, c.code)
		}
	}
}

func TestMembershipBridgeLifecycle(t *testing.T) {
	b := NewMembershipBridge()
	if b.Available() {
		t.Fatalf("初始状态应为不可用")
	}
	if _, err := b.CheckInvite(context.Background(), "hash"); !errors.Is(err, ErrMembershipUnavailable) {
		t.Fatalf("未绑定时应返回 ErrMembershipUnavailable，得到 %v", err)
	}
	if _, err := b.ListJoined(context.Background()); !errors.Is(err, ErrMembershipUnavailable) {
		t.Fatalf("未绑定时 ListJoined 应返回 ErrMembershipUnavailable")
	}
	if err := b.LeaveChannel(context.Background(), 1); !errors.Is(err, ErrMembershipUnavailable) {
		t.Fatalf("未绑定时 LeaveChannel 应返回 ErrMembershipUnavailable")
	}

	b.SetAPI(nil, nil) // api/fetcher 为 nil 同样视为不可用
	if b.Available() {
		t.Fatalf("nil 绑定应为不可用")
	}
	b.Clear()
	if b.Available() {
		t.Fatalf("Clear 后应为不可用")
	}
}
