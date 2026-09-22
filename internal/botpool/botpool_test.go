package botpool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// fakeSender 记录最近一次发送目标，用于断言路由结果。
type fakeSender struct {
	last int64 // 最近一次 SendMessage 的 chatID
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, _ string) (int, error) {
	f.last = chatID
	return 1, nil
}

func (f *fakeSender) EditMessageText(context.Context, int64, int, string) error { return nil }
func (f *fakeSender) SendMedia(context.Context, int64, message.Media, message.Caption, io.Reader) (int, error) {
	return 0, nil
}
func (f *fakeSender) SendAlbum(context.Context, int64, []delivery.AlbumEntry) ([]int, error) {
	return nil, nil
}
func (f *fakeSender) AlbumGroupable(message.Media) bool { return false }
func (f *fakeSender) CopyMessages(context.Context, int64, int64, []int) ([]int, error) {
	return nil, nil
}
func (f *fakeSender) CopyMessage(context.Context, int64, int64, int, string) (int, error) {
	return 0, nil
}
func (f *fakeSender) EditMessageCaption(context.Context, int64, int, string) error { return nil }
func (f *fakeSender) DeleteMessage(context.Context, int64, int) error              { return nil }

func member(id int64, counted, raw *fakeSender) *Member {
	return &Member{
		ID:        id,
		Token:     fmt.Sprintf("%d:ABCDEFGHIJKLMNOPqrstuv", id),
		Sender:    counted,
		RawSender: raw,
	}
}

func TestPoolResetAndSenderFor(t *testing.T) {
	pool := New()
	if !pool.Empty() {
		t.Fatal("新池应为空")
	}
	if snd := pool.SenderFor(0); snd != nil {
		t.Fatal("空池 SenderFor 应返回 nil")
	}

	primary := &fakeSender{}
	second := &fakeSender{}
	pool.Reset([]*Member{
		member(111, primary, primary),
		member(222, second, second),
	})

	if snd := pool.SenderFor(222); snd != second {
		t.Fatal("SenderFor(222) 应返回对应成员")
	}
	// 未命中回退主 bot
	if snd := pool.SenderFor(999); snd != primary {
		t.Fatal("未命中 botID 应回退主 bot")
	}
	if snd := pool.SenderFor(0); snd != primary {
		t.Fatal("BotID=0（存量任务）应回退主 bot")
	}

	snaps := pool.Snapshots()
	if len(snaps) != 2 || !snaps[0].Online {
		t.Fatalf("Reset 后快照应在线，得到 %+v", snaps)
	}
	pool.MarkAllOffline()
	snaps = pool.Snapshots()
	if snaps[0].Online || snaps[1].Online {
		t.Fatal("MarkAllOffline 后快照应离线")
	}
}

func TestUserRouterRoutesByLastActive(t *testing.T) {
	pool := New()
	primary := &fakeSender{}
	second := &fakeSender{}
	pool.Reset([]*Member{member(111, primary, primary), member(222, second, second)})

	counted := UserRouter{Pool: pool, Counted: true}
	raw := UserRouter{Pool: pool}

	// 未知用户回退主 bot
	if _, err := counted.SendMessage(context.Background(), 900, "hi"); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if primary.last != 900 {
		t.Fatalf("未知用户应路由到主 bot，得到 %d", primary.last)
	}

	// 记录活跃后路由到对应 bot（Counted 与 Raw 同一映射）
	pool.NoteActive(900, 222)
	if _, err := counted.SendMessage(context.Background(), 900, "hi"); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if second.last != 900 {
		t.Fatalf("活跃用户应路由到其 bot，得到 %d", second.last)
	}
	if _, err := raw.SendMessage(context.Background(), 900, "hi"); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if second.last != 900 {
		t.Fatalf("Raw 通道应遵循同一映射，得到 %d", second.last)
	}

	// 其他用户不受影响
	if _, err := counted.SendMessage(context.Background(), 901, "hi"); err != nil {
		t.Fatalf("发送失败：%v", err)
	}
	if primary.last != 901 {
		t.Fatalf("未活跃用户仍应走主 bot，得到 %d", primary.last)
	}
}

func TestUserRouterNilWhenPoolEmpty(t *testing.T) {
	pool := New()
	router := UserRouter{Pool: pool}
	if _, err := router.SendMessage(context.Background(), 1, "hi"); !errors.Is(err, delivery.ErrPoolUnavailable) {
		t.Fatalf("空池应返回 ErrPoolUnavailable，得到 %v", err)
	}
}

func TestMemberConflictTransitions(t *testing.T) {
	pool := New()
	m := &Member{ID: 111}
	pool.Reset([]*Member{m})

	// 状态转换只在变化时返回 true（调用方据此去重事件）
	if !m.SetConflict(true) {
		t.Fatal("首次置冲突应返回 true")
	}
	if m.SetConflict(true) {
		t.Fatal("重复置冲突应返回 false")
	}
	if !pool.Snapshots()[0].Conflict {
		t.Fatal("快照应携带冲突态")
	}
	if !m.SetConflict(false) {
		t.Fatal("清除冲突应返回 true")
	}
	if m.SetConflict(false) {
		t.Fatal("重复清除应返回 false")
	}
	if pool.Snapshots()[0].Conflict {
		t.Fatal("清除后快照不应携带冲突态")
	}
	// 精确查找：不存在的 botID 返回 nil（不回退主 bot）
	if pool.MemberByID(999) != nil {
		t.Fatal("未命中的 botID 应返回 nil")
	}
	if pool.MemberByID(111) != m {
		t.Fatal("精确查找应返回对应成员")
	}
}

// TestMemberDisabledRoutingFallback 覆盖停用语义：SetDisabled 状态转换、
// 快照透出，以及路由降级——指定 bot 停用回退第一个可用成员、主 bot 停用
// 回退次序成员、全部停用保底返回原命中。
func TestMemberDisabledRoutingFallback(t *testing.T) {
	primary, second, third := &fakeSender{}, &fakeSender{}, &fakeSender{}
	m1, m2, m3 := member(1, primary, primary), member(2, second, second), member(3, third, third)
	pool := New()
	pool.Reset([]*Member{m1, m2, m3})

	// 状态转换只在变化时返回 true
	if !m2.SetDisabled(true) {
		t.Fatal("首次停用应返回 true")
	}
	if m2.SetDisabled(true) {
		t.Fatal("重复停用应返回 false")
	}
	if !pool.Snapshots()[1].Disabled {
		t.Fatal("快照应携带停用态")
	}

	// 指定 bot 停用：回退第一个可用成员（主 bot）
	if snd := pool.SenderFor(2); snd != primary {
		t.Fatal("停用 bot 的路由应回退主 bot")
	}
	// 主 bot 停用：未指定与指定都回退第一个可用（m2 已停用，可用的是 m3）
	m1.SetDisabled(true)
	if snd := pool.SenderFor(0); snd != third {
		t.Fatal("主 bot 停用应回退第一个可用成员")
	}
	if snd := pool.SenderFor(1); snd != third {
		t.Fatal("指定停用主 bot 应回退第一个可用成员")
	}
	// 全部停用：保底返回原命中（上层预检已拦截，此处仅兜底）
	m3.SetDisabled(true)
	if snd := pool.SenderFor(3); snd != third {
		t.Fatal("全部停用时应保底返回原命中成员")
	}
	// 恢复：清除停用后路由回到该 bot
	m1.SetDisabled(false)
	if snd := pool.SenderFor(1); snd != primary {
		t.Fatal("恢复后路由应回到主 bot")
	}
}
