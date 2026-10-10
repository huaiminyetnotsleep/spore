package binding

// 绑定频道失效/权限事件上报测试：本体消失自动解绑上报（幂等——并发或
// 重复发现只报一次）、无权限提醒上报、未注入出口时安全跳过。

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// recordingBindingEvents 记录绑定频道失效/权限事件上报。
type recordingBindingEvents struct {
	mu        sync.Mutex
	gone      []string // BindingChannelGone 的 title 序列
	noRights  []string // BindingChannelNoRights 的 title 序列
	recovered int      // BindingChannelRightsRecovered 次数
}

func (r *recordingBindingEvents) BindingChannelGone(_ context.Context, title string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gone = append(r.gone, title)
}

func (r *recordingBindingEvents) BindingChannelNoRights(_ context.Context, title string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.noRights = append(r.noRights, title)
}

func (r *recordingBindingEvents) BindingChannelRightsRecovered(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recovered++
}

func (r *recordingBindingEvents) snapshot() (gone, noRights []string, recovered int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.gone, r.noRights, r.recovered
}

func newBindingServiceWithEvents(t *testing.T) (*Service, *store.Store, *recordingBindingEvents) {
	t.Helper()
	s, err := New(Options{Store: openStore(t), Log: testLog()})
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	ev := &recordingBindingEvents{}
	s.SetEvents(ev)
	return s, s.store, ev
}

// seedBinding 落绑定用户行与绑定行（channel_bindings 对 users 有外键约束）。
func seedBinding(t *testing.T, st *store.Store, bnd store.ChannelBinding) store.ChannelBinding {
	t.Helper()
	if _, err := st.CreateUser(context.Background(), store.User{ID: bnd.UserID, Status: store.UserEnabled}); err != nil {
		t.Fatalf("落用户失败: %v", err)
	}
	out, err := st.UpsertChannelBinding(context.Background(), bnd)
	if err != nil {
		t.Fatalf("落绑定失败: %v", err)
	}
	return out
}

// 本体消失：状态转换成功才上报；重复发现（并发/已解绑）不再重复上报。
func TestAutoUnbindRaisesEventOnce(t *testing.T) {
	svc, st, ev := newBindingServiceWithEvents(t)
	ctx := context.Background()
	bnd := seedBinding(t, st, store.ChannelBinding{
		ChannelID: testChannelD, UserID: 7, BotID: testBotID, Title: "我的频道",
		BoundVia: store.BoundViaBot,
	})

	svc.autoUnbindDeadChannel(ctx, testBotID, 7, bnd)
	svc.autoUnbindDeadChannel(ctx, testBotID, 7, bnd) // 已解绑：幂等跳过

	gone, _, _ := ev.snapshot()
	if len(gone) != 1 || gone[0] != "我的频道" {
		t.Fatalf("自动解绑应上报一次展示名，得到 %v", gone)
	}
}

// 无权限：每次发现都上报（事件中心按 key 合并、受冷却窗口约束）；
// 提醒发送失败不影响上报。
func TestNotifyChannelNoRightsRaisesEvent(t *testing.T) {
	svc, _, ev := newBindingServiceWithEvents(t)
	// 全部方法返回 500 的假 Bot API：sendMessage 失败只记日志，不影响上报
	b, _ := newTestBot(t, `{}`, `{}`, http.StatusInternalServerError)
	bnd := store.ChannelBinding{ChannelID: testChannelD, UserID: 7, Username: "mychan"}

	svc.notifyChannelNoRights(context.Background(), b, 7, bnd)

	_, noRights, _ := ev.snapshot()
	if len(noRights) != 1 || noRights[0] != "@mychan" {
		t.Fatalf("无权限应上报展示名（标题缺省回退 @用户名），得到 %v", noRights)
	}
}

// 未注入事件出口：上报路径不 panic（nil 跳过）。
func TestBindingEventsWithoutSink(t *testing.T) {
	s, err := New(Options{Store: openStore(t), Log: testLog()})
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	ctx := context.Background()
	bnd := seedBinding(t, s.store, store.ChannelBinding{
		ChannelID: testChannelD, UserID: 7, Title: "频道",
		BoundVia: store.BoundViaBot,
	})

	s.autoUnbindDeadChannel(ctx, 0, 7, bnd) // 不 panic 即通过
	s.rightsRecovered(ctx)
}
