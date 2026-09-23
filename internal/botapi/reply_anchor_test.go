package botapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// replyHarness 构造带 bot 身份与频道服务的引用回复测试环境（用户 7、
// chat 7、bot 42）；锚点坐标按 (bot 42, chat 7, replyMsgID) 反查。
func replyHarness(t *testing.T) (Options, *fakeAccess, *fakeChannels, *fakeSender) {
	t.Helper()
	opt, fa, fc, fs := channelsHarness(t)
	fa.usageData.Status = store.UserEnabled
	opt.Bot = NewBotRef(42, "spore_bot")
	return opt, fa, fc, fs
}

// runReply 以用户 7 在 chat 7 发送 text 并回复 replyMsgID（0 = 非回复形态）。
func runReply(opt Options, fs *fakeSender, text string, replyMsgID int64) {
	handleUpdate(context.Background(), opt, fs,
		models.User{ID: 7, FirstName: "Alice", Username: "alice"}, 7, text, replyMsgID)
}

// seedAnchor 在 fakeAccess 里登记一个可反查的锚点与对应请求行。
func seedAnchor(fa *fakeAccess, replyMsgID int64, r store.Request) {
	fa.anchors = map[int64]store.SentMessage{
		replyMsgID: {RequestID: r.ID, BotID: 42, ChatID: 7, MessageID: replyMsgID, Kind: store.SentKindMedia},
	}
	fa.requestsByID = map[int64]store.Request{r.ID: r}
}

func TestCancelReplyMatrix(t *testing.T) {
	t.Run("在途任务取消成功", func(t *testing.T) {
		opt, fa, _, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestProcessing})
		fa.cancelByIDOut = 1

		runReply(opt, fs, "/cancel", 501)

		if got := fa.cancelledByIDs(); len(got) != 1 || got[0] != 9 {
			t.Fatalf("应按锚点请求 ID 取消: %v", got)
		}
		if txt := lastText(t, fs); txt != "已取消该任务。" {
			t.Fatalf("取消文案不符: %q", txt)
		}
	})
	t.Run("终态任务回友好文案", func(t *testing.T) {
		for _, tc := range []struct {
			status string
			want   string
		}{
			{store.RequestSucceeded, cancelReplySucceededText},
			{store.RequestFailed, cancelReplyFailedText},
			{store.RequestCancelled, cancelReplyCancelledText},
		} {
			opt, fa, _, fs := replyHarness(t)
			seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: tc.status})
			runReply(opt, fs, "/cancel", 501)
			if got := lastText(t, fs); got != tc.want {
				t.Fatalf("状态 %s 文案不符: got %q want %q", tc.status, got, tc.want)
			}
			if calls := fa.cancelledByIDs(); len(calls) != 0 {
				t.Fatalf("终态不应调用取消: %v", calls)
			}
		}
	})
	t.Run("取消竞态返回零", func(t *testing.T) {
		opt, fa, _, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestQueued})
		fa.cancelByIDOut = 0 // 状态条件更新未命中

		runReply(opt, fs, "/cancel", 501)

		if txt := lastText(t, fs); txt != cancelReplyJustEndedText {
			t.Fatalf("竞态文案不符: %q", txt)
		}
	})
	t.Run("锚点未命中回引导", func(t *testing.T) {
		opt, fa, _, fs := replyHarness(t)
		runReply(opt, fs, "/cancel", 404)
		if txt := lastText(t, fs); !strings.Contains(txt, "未找到该消息对应的任务") {
			t.Fatalf("应回引导文案: %q", txt)
		}
		if calls := fa.cancelledByIDs(); len(calls) != 0 {
			t.Fatalf("未命中不应取消: %v", calls)
		}
	})
	t.Run("他人请求与未命中同形", func(t *testing.T) {
		opt, fa, _, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 8, Status: store.RequestQueued})
		runReply(opt, fs, "/cancel", 501)
		if txt := lastText(t, fs); !strings.Contains(txt, "未找到该消息对应的任务") {
			t.Fatalf("他人请求应与未命中同文案: %q", txt)
		}
	})
	t.Run("带链接参数时回复被忽略", func(t *testing.T) {
		opt, fa, _, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestQueued})
		fa.cancelOut = 1

		runReply(opt, fs, "/cancel https://t.me/mychan/7", 501)

		if len(fa.cancelRefs) != 1 || len(fa.cancelledByIDs()) != 0 {
			t.Fatalf("带参应走链接路径: refs=%v byID=%v", fa.cancelRefs, fa.cancelledByIDs())
		}
	})
	t.Run("裸命令无回复回输入提示", func(t *testing.T) {
		opt, _, _, fs := replyHarness(t)
		runReply(opt, fs, "/cancel", 0)
		if txt := lastText(t, fs); !strings.Contains(txt, "请回复本消息") || strings.Contains(txt, "用法：") {
			t.Fatalf("应回简短输入提示: %q", txt)
		}
	})
}

func TestPinReplyMatrix(t *testing.T) {
	t.Run("在途任务补标", func(t *testing.T) {
		opt, fa, _, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestQueued})
		fa.pinMarkedOut = true

		runReply(opt, fs, "/pin", 501)

		if got := fa.pinMarks(); len(got) != 1 || got[0] != 9 {
			t.Fatalf("应补标锚点请求: %v", got)
		}
		if txt := lastText(t, fs); txt != pinReplyMarkedText {
			t.Fatalf("补标文案不符: %q", txt)
		}
	})
	t.Run("已带置顶标记的在途任务", func(t *testing.T) {
		opt, fa, _, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestProcessing, Pin: true})
		runReply(opt, fs, "/pin", 501)
		if txt := lastText(t, fs); txt != pinReplyAlreadyMarkedText {
			t.Fatalf("已标记文案不符: %q", txt)
		}
		if calls := fa.pinMarks(); len(calls) != 0 {
			t.Fatalf("不应重复补标: %v", calls)
		}
	})
	t.Run("补标竞态", func(t *testing.T) {
		opt, fa, _, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestQueued})
		fa.pinMarkedOut = false
		runReply(opt, fs, "/pin", 501)
		if txt := lastText(t, fs); txt != pinReplyRaceText {
			t.Fatalf("竞态文案不符: %q", txt)
		}
	})
	t.Run("已完成任务事后补置顶", func(t *testing.T) {
		opt, fa, fc, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestSucceeded})
		fc.pinCopiesOut = queue.PinOutcome{OK: 1, Total: 1, Targets: []queue.PinTarget{{Label: "我的频道", Pinned: true}}}
		fc.pinCopiesFnd = true

		runReply(opt, fs, "/pin", 501)

		if got := fc.pinCopyCalls(); len(got) != 1 || got[0] != 9 {
			t.Fatalf("应对锚点请求补置顶: %v", got)
		}
		if txt := lastText(t, fs); !strings.Contains(txt, "📌 已置顶到：\n我的频道") {
			t.Fatalf("补置顶文案不符: %q", txt)
		}
	})
	t.Run("已全部置顶的任务", func(t *testing.T) {
		opt, fa, fc, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestSucceeded,
			Pin: true, PinOK: 2, PinTotal: 2})
		runReply(opt, fs, "/pin", 501)
		if txt := lastText(t, fs); txt != pinReplyAlreadyPinnedText {
			t.Fatalf("已置顶文案不符: %q", txt)
		}
		if calls := fc.pinCopyCalls(); len(calls) != 0 {
			t.Fatalf("不应重复置顶: %v", calls)
		}
	})
	t.Run("部分失败点名目标", func(t *testing.T) {
		opt, fa, fc, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestSucceeded})
		fc.pinCopiesOut = queue.PinOutcome{OK: 1, Total: 2, Targets: []queue.PinTarget{
			{Label: "我的频道", Pinned: true}, {Label: "-100999", Pinned: false}}}
		fc.pinCopiesFnd = true
		runReply(opt, fs, "/pin", 501)
		txt := lastText(t, fs)
		if !strings.Contains(txt, "📌 已置顶到：\n我的频道") || !strings.Contains(txt, "置顶失败：\n-100999") {
			t.Fatalf("部分失败应点名目标: %q", txt)
		}
	})
	t.Run("无频道副本", func(t *testing.T) {
		opt, fa, fc, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestSucceeded})
		fc.pinCopiesFnd = false
		runReply(opt, fs, "/pin", 501)
		if txt := lastText(t, fs); txt != pinReplyNoCopiesText {
			t.Fatalf("无副本文案不符: %q", txt)
		}
	})
	t.Run("失败与取消终态", func(t *testing.T) {
		for _, tc := range []struct {
			status, want string
		}{
			{store.RequestFailed, pinReplyFailedText},
			{store.RequestCancelled, pinReplyCancelledText},
		} {
			opt, fa, fc, fs := replyHarness(t)
			seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: tc.status})
			runReply(opt, fs, "/pin", 501)
			if got := lastText(t, fs); got != tc.want {
				t.Fatalf("状态 %s 文案不符: got %q want %q", tc.status, got, tc.want)
			}
			if len(fc.pinCopyCalls()) != 0 || len(fa.pinMarks()) != 0 {
				t.Fatalf("终态不应有动作: pins=%v marks=%v", fc.pinCopyCalls(), fa.pinMarks())
			}
		}
	})
	t.Run("带链接参数时走提交链且落占位锚点", func(t *testing.T) {
		opt, fa, fc, fs := replyHarness(t)
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestQueued})

		runReply(opt, fs, "/pin https://t.me/mychan/7", 501)

		if subs := fa.submitted(); len(subs) != 1 || !subs[0].Pin {
			t.Fatalf("带参应走链接提交链并标记置顶: %+v", subs)
		}
		if len(fa.pinMarks()) != 0 || len(fc.pinCopyCalls()) != 0 {
			t.Fatalf("带参不应触发引用分支: marks=%v copies=%v", fa.pinMarks(), fc.pinCopyCalls())
		}
		// 提交链顺手落库占位锚点（botapi fakeSender 首条消息 ID=1）
		anchors := fa.recordedAnchors()
		if len(anchors) != 1 || anchors[0].Kind != store.SentKindStatus || anchors[0].MessageID != 1 {
			t.Fatalf("提交应记录占位锚点: %+v", anchors)
		}
	})
	t.Run("未启用用户被准入拦截", func(t *testing.T) {
		opt, fa, _, fs := replyHarness(t)
		fa.usageData.Status = store.UserPending
		seedAnchor(fa, 501, store.Request{ID: 9, UserID: 7, Status: store.RequestQueued})
		runReply(opt, fs, "/pin", 501)
		if calls := fa.pinMarks(); len(calls) != 0 {
			t.Fatalf("未启用不应补标: %v", calls)
		}
	})
	t.Run("裸命令无回复回输入提示", func(t *testing.T) {
		opt, _, _, fs := replyHarness(t)
		runReply(opt, fs, "/pin", 0)
		if txt := lastText(t, fs); !strings.Contains(txt, "请回复本消息") || strings.Contains(txt, "用法：") {
			t.Fatalf("应回简短输入提示: %q", txt)
		}
	})
	t.Run("反查存储故障回受控文案", func(t *testing.T) {
		opt, fa, _, fs := replyHarness(t)
		fa.resolveErr = apperr.Wrap(apperr.CodeStoreUnavailable, errors.New("db down"))
		runReply(opt, fs, "/cancel", 501)
		if txt := lastText(t, fs); !strings.Contains(txt, "稍后重试") {
			t.Fatalf("存储故障应回受控文案: %q", txt)
		}
	})
}
