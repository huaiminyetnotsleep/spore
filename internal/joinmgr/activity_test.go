package joinmgr

// 新频道加入申请活动通知测试：仅新建待审批申请触发，重复提交不触发。

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// activitySpy 收集新加入申请活动上报（seen 供异步断言同步）。
type activitySpy struct {
	mu    sync.Mutex
	infos []JoinRequestInfo
	seen  chan struct{}
}

func (s *activitySpy) ChannelJoinRequested(_ context.Context, info JoinRequestInfo) {
	s.mu.Lock()
	s.infos = append(s.infos, info)
	s.mu.Unlock()
	s.seen <- struct{}{}
}

func TestSubmitNotifiesNewJoinRequestOnly(t *testing.T) {
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.CreateUser(context.Background(), store.User{ID: 100, Username: "carol", DisplayName: "Carol"}); err != nil {
		t.Fatal(err)
	}
	spy := &activitySpy{seen: make(chan struct{}, 8)}
	bridge := &fakeBridge{checkInfo: mtproto.InviteInfo{Title: "私有频道", IsChannel: true, Participants: 123}}
	svc, err := New(Options{Store: s, Bridge: bridge, Activity: spy})
	if err != nil {
		t.Fatal(err)
	}
	if err := syscfg.SaveJoinConfig(context.Background(), s, syscfg.JoinConfig{Enabled: true, RequireApproval: true}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	out, err := svc.Submit(ctx, 100, false, baseInviteText)
	if err != nil || out.Kind != SubmitPending {
		t.Fatalf("普通用户应落待审批: %+v %v", out, err)
	}
	select {
	case <-spy.seen:
	case <-time.After(2 * time.Second):
		t.Fatal("新建待审批申请应触发活动通知")
	}
	info := spy.infos[0]
	if info.UserID != 100 || info.Username != "carol" || info.DisplayName != "Carol" ||
		info.ChannelTitle != "私有频道" || info.Participants != 123 {
		t.Fatalf("申请通知 payload 不符: %+v", info)
	}

	// 重复提交（刷新）：不再通知
	if out, err := svc.Submit(ctx, 100, false, baseInviteText); err != nil || out.Kind != SubmitPendingDuplicate {
		t.Fatalf("重复提交应提示重复: %+v %v", out, err)
	}
	select {
	case <-spy.seen:
		t.Fatal("重复提交不应重复通知")
	case <-time.After(100 * time.Millisecond):
	}
}
