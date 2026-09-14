package notify

// CountSender 测试：业务发送结果喂给 Hub（连续失败触发事件、成功清零）、
// 删除类调用不计数。

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// fakeFullSender 实现 delivery.Sender 的可编程假实现。
type fakeFullSender struct {
	sendErr error // SendMessage/SendMedia/SendAlbum 统一失败
	msgID   int
}

func (f *fakeFullSender) SendMessage(context.Context, int64, string) (int, error) {
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	return f.msgID, nil
}

func (f *fakeFullSender) SendMedia(context.Context, int64, message.Media, message.Caption, io.Reader) (int, error) {
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	return 1, nil
}

func (f *fakeFullSender) SendAlbum(context.Context, int64, []delivery.AlbumEntry) ([]int, error) {
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return []int{1, 2}, nil
}

func (f *fakeFullSender) AlbumGroupable(message.Media) bool { return true }
func (f *fakeFullSender) CopyMessage(_ context.Context, _, _ int64, messageID int, _ string) (int, error) {
	if f.sendErr != nil {
		return 0, f.sendErr
	}
	return messageID, nil
}

func (f *fakeFullSender) EditMessageCaption(context.Context, int64, int, string) error { return nil }

func (f *fakeFullSender) CopyMessages(_ context.Context, _, _ int64, messageIDs []int) ([]int, error) {
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return messageIDs, nil
}

func (f *fakeFullSender) DeleteMessage(context.Context, int64, int) error {
	return errors.New("delete failed") // 恒败：验证不计数
}

func (f *fakeFullSender) EditMessageText(context.Context, int64, int, string) error {
	return errors.New("edit failed") // 恒败：验证不计数
}

func TestCountSenderFeedsHub(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h, err := New(Options{Store: st, Log: testLog(), Now: clock.Now, BotFailThreshold: 2})
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	h.SetSender(&fakeNotifier{}) // 事件通知通道（无关本测试断言）

	inner := &fakeFullSender{sendErr: errors.New("send failed")}
	snd := NewCountSender(inner, h)
	var _ delivery.Sender = snd // 包装结果仍是 delivery.Sender
	ctx := context.Background()

	// 文本失败 2 次 → 达到阈值产生事件
	if _, err := snd.SendMessage(ctx, 1, "x"); err == nil {
		t.Fatal("应透传底层错误")
	}
	if _, err := st.GetEvent(ctx, KeyBotSendFailures); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("未达阈值不应产生事件，得到 %v", err)
	}
	if _, err := snd.SendMessage(ctx, 1, "x"); err == nil {
		t.Fatal("应透传底层错误")
	}
	if _, err := st.GetEvent(ctx, KeyBotSendFailures); err != nil {
		t.Fatalf("文本失败达到阈值应产生事件: %v", err)
	}

	// 媒体失败不计数接口缺口（此时已超阈值，仅验证透传与继续合并不 panic）
	if _, err := snd.SendMedia(ctx, 1, message.Media{}, message.Caption{}, nil); err == nil {
		t.Fatal("应透传底层错误")
	}
	if _, err := snd.SendAlbum(ctx, 1, nil); err == nil {
		t.Fatal("应透传底层错误")
	}
	// 删除失败不计入业务发送（避免误报）
	if err := snd.DeleteMessage(ctx, 1, 1); err == nil {
		t.Fatal("DeleteMessage 应透传底层错误")
	}

	// 恢复：成功清零
	inner.sendErr = nil
	if _, err := snd.SendMessage(ctx, 1, "ok"); err != nil {
		t.Fatalf("恢复后应成功: %v", err)
	}
	h.BotSendResult(ctx, false) // 清零后单次失败不应再触发
	e, err := st.GetEvent(ctx, KeyBotSendFailures)
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if e.Count != 3 { // 2 次文本失败 + 1 次媒体失败（合并），删除不计
		t.Fatalf("删除失败不应计入，文本+媒体失败应合并 count=3，得到 %d", e.Count)
	}
}

func TestCountSenderPassthrough(t *testing.T) {
	st := openStore(t)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h, err := New(Options{Store: st, Log: testLog(), Now: clock.Now})
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	inner := &fakeFullSender{msgID: 9}
	snd := NewCountSender(inner, h)
	if id, err := snd.SendMessage(context.Background(), 1, "x"); err != nil || id != 9 {
		t.Fatalf("应透传成功结果，得到 %d, %v", id, err)
	}
}

// EditMessageText 不计数透传：占位进度编辑是高频常态操作，失败不构成
// "Bot API 连续失败"信号（与 DeleteMessage 同姿态）。
func TestCountSenderEditMessageTextNotCounted(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h, err := New(Options{Store: st, Log: testLog(), Now: clock.Now, BotFailThreshold: 1})
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	inner := &fakeFullSender{}
	s := NewCountSender(inner, h)

	if err := s.EditMessageText(context.Background(), 7, 42, "progress"); err == nil {
		t.Fatal("假实现编辑应失败")
	}
	// 阈值为 1：若编辑被计数，botapi.send_failures 事件已产生
	events, err := st.ListEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Key == KeyBotSendFailures {
			t.Fatalf("编辑失败不应计入发送失败事件: %+v", e)
		}
	}
}
