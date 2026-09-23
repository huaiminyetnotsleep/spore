package botapi

import (
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// TestHandlePinSubmitsWithFlag：/pin <链接> 走与裸链接相同的提交链并带 Pin 标记。
func TestHandlePinSubmitsWithFlag(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	fa.usageData.Status = store.UserEnabled
	run(opt, fs, "/pin https://t.me/example_channel/42")

	subs := fa.submitted()
	if len(subs) != 1 {
		t.Fatalf("应恰好一次提交，得到 %d", len(subs))
	}
	if !subs[0].Pin {
		t.Fatalf("/pin 提交应携带置顶标记: %+v", subs[0])
	}
	if subs[0].Ref.MessageID != 42 || subs[0].UserID != 7 {
		t.Fatalf("提交字段不符: %+v", subs[0])
	}
	if lastText(t, fs) != statusPrompt {
		t.Fatalf("应回复普通占位提示")
	}
}

// TestHandlePinZeroBindingsStillSubmitsWithHint：零绑定时任务照常提交并附提示。
func TestHandlePinZeroBindingsStillSubmitsWithHint(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	fa.usageData.Status = store.UserEnabled
	fc := &fakeChannels{listResult: nil} // 无绑定
	opt.Channels = fc

	run(opt, fs, "/pin https://t.me/example_channel/42")

	if subs := fa.submitted(); len(subs) != 1 || !subs[0].Pin {
		t.Fatalf("零绑定仍应提交置顶任务: %+v", fa.submitted())
	}
	if got := lastText(t, fs); !strings.Contains(got, "尚未绑定频道/群组") {
		t.Fatalf("应附零绑定提示，得到: %q", got)
	}
}

// TestHandlePinUsageOnEmptyArgs：空参回简短输入提示，不提交任务。
func TestHandlePinUsageOnEmptyArgs(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	run(opt, fs, "/pin")

	if subs := fa.submitted(); len(subs) != 0 {
		t.Fatalf("空参不应提交任务: %+v", subs)
	}
	if got := lastText(t, fs); !strings.Contains(got, "请回复本消息") || strings.Contains(got, "用法：") {
		t.Fatalf("应回 /pin 简短输入提示，得到: %q", got)
	}
}

// TestHandlePinInvalidLink：非链接参数回无效 URL 文案，不提交。
func TestHandlePinInvalidLink(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	fa.usageData.Status = store.UserEnabled
	run(opt, fs, "/pin not-a-link")

	if subs := fa.submitted(); len(subs) != 0 {
		t.Fatalf("无效链接不应提交任务: %+v", subs)
	}
	if got := lastText(t, fs); got != apperr.UserText(apperr.CodeInvalidURL) {
		t.Fatalf("应回无效链接文案，得到: %q", got)
	}
}

// TestHandlePinRequiresEnabledUser：未授权用户被准入拦截，不提交。
func TestHandlePinRequiresEnabledUser(t *testing.T) {
	opt, fa, fs := newHarness(t, 4)
	fa.usageData.Status = store.UserPending
	run(opt, fs, "/pin https://t.me/example_channel/42")

	if subs := fa.submitted(); len(subs) != 0 {
		t.Fatalf("未授权用户不应提交任务: %+v", subs)
	}
	if got := lastText(t, fs); got == "" {
		t.Fatal("应回复状态引导文案")
	}
}
