package notify

// 源与缓存频道失效事件（source.channel_inaccessible / watch.source_unavailable /
// dump.channel_write_failed）的 Raise 语义：正确的 key/severity 落库并推送、
// 监听源标题封顶折叠、恢复方法解决事件。

import (
	"context"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func newSourceTestHub(t *testing.T) (*Hub, *store.Store, *fakeNotifier) {
	t.Helper()
	st := openStore(t)
	withOwner(t, st, 42)
	clock := newClock(time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	snd := &fakeNotifier{}
	h.SetSender(snd)
	snd.calls = 0 // SetSender 触发的补发不计入
	return h, st, snd
}

func TestSourceInaccessibleRaisesEvent(t *testing.T) {
	h, st, snd := newSourceTestHub(t)
	ctx := context.Background()

	h.SourceInaccessible(ctx, "https://t.me/example/7")

	e := mustEvent(t, st, KeySourceInaccessible)
	if e.Status != store.EventOpen || e.Severity != SeverityError || e.Count != 1 {
		t.Fatalf("事件写入不符: %+v", e)
	}
	if snd.count() != 1 {
		t.Fatalf("应推送管理员一次，得到 %d", snd.count())
	}
}

func TestDumpChannelWriteFailedRaisesAndRecovers(t *testing.T) {
	h, st, _ := newSourceTestHub(t)
	ctx := context.Background()

	h.DumpChannelWriteFailed(ctx, "SEND_TARGET_INVALID", -1001234567890)
	e := mustEvent(t, st, KeyDumpChannelWriteFailed)
	if e.Status != store.EventOpen || e.Severity != SeverityWarn {
		t.Fatalf("写失败事件应为 open/warn: %+v", e)
	}

	h.DumpChannelRecovered(ctx)
	if e := mustEvent(t, st, KeyDumpChannelWriteFailed); e.Status != store.EventResolved {
		t.Fatalf("恢复后事件应为 resolved: %+v", e)
	}
}

func TestWatchSourcesUnavailableRaisesAndRecovers(t *testing.T) {
	h, st, _ := newSourceTestHub(t)
	ctx := context.Background()

	h.WatchSourcesUnavailable(ctx, []string{"源A", "源B"})
	e := mustEvent(t, st, KeyWatchSourceUnavailable)
	if e.Status != store.EventOpen || e.Severity != SeverityError {
		t.Fatalf("不可用事件应为 open/error: %+v", e)
	}

	h.WatchSourcesRecovered(ctx)
	if e := mustEvent(t, st, KeyWatchSourceUnavailable); e.Status != store.EventResolved {
		t.Fatalf("恢复后事件应为 resolved: %+v", e)
	}
}

// 监听源标题超过封顶数量时折叠为前 5 个 + Extra 计数（payload 只进推送，
// 不宜无限拉长）。
func TestWatchSourcesUnavailableCapsTitles(t *testing.T) {
	h, _, _ := newSourceTestHub(t)

	h.WatchSourcesUnavailable(context.Background(),
		[]string{"a", "b", "c", "d", "e", "f", "g"})

	data, ok := h.snapshotPayload(KeyWatchSourceUnavailable).(WatchSourceUnavailableData)
	if !ok {
		t.Fatalf("payload 类型不符: %#v", h.snapshotPayload(KeyWatchSourceUnavailable))
	}
	if len(data.Sources) != watchSourceTitleLimit || data.Extra != 2 {
		t.Fatalf("应折叠为 5 个 + Extra=2，得到 %d/%d", len(data.Sources), data.Extra)
	}
}

// 绑定频道本体消失：error 事件，人工解决（解绑已是终态处置）。
func TestBindingChannelGoneRaisesEvent(t *testing.T) {
	h, st, snd := newSourceTestHub(t)

	h.BindingChannelGone(context.Background(), "我的频道")

	e := mustEvent(t, st, KeyBindingChannelGone)
	if e.Status != store.EventOpen || e.Severity != SeverityError {
		t.Fatalf("失效事件应为 open/error: %+v", e)
	}
	if snd.count() != 1 {
		t.Fatalf("应推送管理员一次，得到 %d", snd.count())
	}
}

// 绑定频道权限事件：warn 事件，副本恢复同步后自动解决。
func TestBindingChannelNoRightsRaisesAndRecovers(t *testing.T) {
	h, st, _ := newSourceTestHub(t)
	ctx := context.Background()

	h.BindingChannelNoRights(ctx, "@mychan")
	e := mustEvent(t, st, KeyBindingChannelNoRights)
	if e.Status != store.EventOpen || e.Severity != SeverityWarn {
		t.Fatalf("权限事件应为 open/warn: %+v", e)
	}

	h.BindingChannelRightsRecovered(ctx)
	if e := mustEvent(t, st, KeyBindingChannelNoRights); e.Status != store.EventResolved {
		t.Fatalf("恢复后事件应为 resolved: %+v", e)
	}
}
