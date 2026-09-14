package botapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// fakeChannels 记录频道绑定服务的调用并按配置返回结果。
type fakeChannels struct {
	mu           sync.Mutex
	bindUser     []int64
	bindTarget   []string
	unbindUser   []int64
	unbindTarget []string

	bindResult   store.ChannelBinding
	bindErr      error
	unbindResult store.ChannelBinding
	unbindErr    error
	listResult   []store.ChannelBinding
}

func (f *fakeChannels) BindBot(_ context.Context, userID int64, target string) (store.ChannelBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bindUser = append(f.bindUser, userID)
	f.bindTarget = append(f.bindTarget, target)
	return f.bindResult, f.bindErr
}

func (f *fakeChannels) UnbindBot(_ context.Context, userID int64, target string) (store.ChannelBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unbindUser = append(f.unbindUser, userID)
	f.unbindTarget = append(f.unbindTarget, target)
	return f.unbindResult, f.unbindErr
}

func (f *fakeChannels) ListByUser(_ context.Context, _ int64) ([]store.ChannelBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listResult, nil
}

func channelsHarness(t *testing.T) (Options, *fakeAccess, *fakeChannels, *fakeSender) {
	t.Helper()
	opt, fa, fs := newHarness(t, 4)
	fc := &fakeChannels{}
	opt.Channels = fc
	return opt, fa, fc, fs
}

func TestHandleBindSuccess(t *testing.T) {
	opt, fa, fc, fs := channelsHarness(t)
	fa.usageData.Status = store.UserEnabled
	fc.bindResult = store.ChannelBinding{ChannelID: -1001234567890, UserID: 7,
		Title: "我的频道", Username: "mychan"}

	run(opt, fs, "/bind @mychan")

	if len(fc.bindUser) != 1 || fc.bindUser[0] != 7 || fc.bindTarget[0] != "@mychan" {
		t.Fatalf("应把当前用户与目标传给绑定服务: %v %v", fc.bindUser, fc.bindTarget)
	}
	if got := lastText(t, fs); !strings.Contains(got, "已绑定频道「我的频道」") || !strings.Contains(got, "@mychan") {
		t.Fatalf("绑定成功文案不符: %q", got)
	}
}

func TestHandleBindUsageAndUnavailable(t *testing.T) {
	t.Run("缺参数回复用法", func(t *testing.T) {
		opt, _, fc, fs := channelsHarness(t)
		run(opt, fs, "/bind")
		if got := lastText(t, fs); !strings.Contains(got, "用法：/bind") {
			t.Fatalf("应回复用法: %q", got)
		}
		if len(fc.bindUser) != 0 {
			t.Fatal("缺参数不应调用绑定服务")
		}
	})

	t.Run("服务未装配", func(t *testing.T) {
		opt, _, _, fs := channelsHarness(t)
		opt.Channels = nil
		run(opt, fs, "/bind @mychan")
		if got := lastText(t, fs); got != "此命令当前不可用。" {
			t.Fatalf("服务缺失应回复不可用: %q", got)
		}
	})
}

func TestHandleBindDeniedStatus(t *testing.T) {
	opt, fa, fc, fs := channelsHarness(t)
	fa.usageData.Status = store.UserPending

	run(opt, fs, "/bind @mychan")

	if got := lastText(t, fs); got != apperr.UserText(apperr.CodeUserPending) {
		t.Fatalf("待审批用户应回复状态文案: %q", got)
	}
	if len(fc.bindUser) != 0 {
		t.Fatal("状态拒绝后不应调用绑定服务")
	}
}

func TestHandleBindErrorMapping(t *testing.T) {
	t.Run("业务错误码转用户文案", func(t *testing.T) {
		opt, fa, fc, fs := channelsHarness(t)
		fa.usageData.Status = store.UserEnabled
		fc.bindErr = apperr.New(apperr.CodeChannelNotPostable, "")

		run(opt, fs, "/bind @mychan")

		if got := lastText(t, fs); got != apperr.UserText(apperr.CodeChannelNotPostable) {
			t.Fatalf("应回复 CHANNEL_NOT_POSTABLE 文案: %q", got)
		}
	})
}

func TestHandleUnbind(t *testing.T) {
	t.Run("成功", func(t *testing.T) {
		opt, fa, fc, fs := channelsHarness(t)
		fa.usageData.Status = store.UserEnabled
		fc.unbindResult = store.ChannelBinding{ChannelID: -1001, Title: "我的频道"}

		run(opt, fs, "/unbind @mychan")

		if len(fc.unbindUser) != 1 || fc.unbindTarget[0] != "@mychan" {
			t.Fatalf("应把当前用户与目标传给解绑服务: %v", fc.unbindTarget)
		}
		if got := lastText(t, fs); !strings.Contains(got, "已解除绑定「我的频道」") {
			t.Fatalf("解绑成功文案不符: %q", got)
		}
	})

	t.Run("未绑定或非本人", func(t *testing.T) {
		opt, fa, fc, fs := channelsHarness(t)
		fa.usageData.Status = store.UserEnabled
		fc.unbindErr = store.ErrNotFound

		run(opt, fs, "/unbind @mychan")

		if got := lastText(t, fs); !strings.Contains(got, "没有绑定在你的账号下") {
			t.Fatalf("ErrNotFound 应转明确文案: %q", got)
		}
	})
}

func TestHandleMyChannels(t *testing.T) {
	t.Run("空列表", func(t *testing.T) {
		opt, fa, _, fs := channelsHarness(t)
		fa.usageData.Status = store.UserEnabled
		run(opt, fs, "/channels")
		if got := lastText(t, fs); !strings.Contains(got, "还没有绑定频道") {
			t.Fatalf("空列表应提示绑定方法: %q", got)
		}
	})

	t.Run("列出绑定", func(t *testing.T) {
		opt, fa, fc, fs := channelsHarness(t)
		fa.usageData.Status = store.UserEnabled
		fc.listResult = []store.ChannelBinding{
			{ChannelID: -1001234567890, Title: "频道A", Username: "a"},
			{ChannelID: -1009876543210, Title: "频道B"},
		}
		run(opt, fs, "/channels")
		got := lastText(t, fs)
		if !strings.Contains(got, "绑定了 2 个频道") ||
			!strings.Contains(got, "频道A（@a）") ||
			!strings.Contains(got, "https://t.me/a") ||
			!strings.Contains(got, "频道B（ID -1009876543210）") ||
			!strings.Contains(got, "https://t.me/c/9876543210/1") {
			t.Fatalf("列表文案（含完整路径）不符: %q", got)
		}
	})
}

func TestHandleChannelsAccessError(t *testing.T) {
	opt, fa, _, fs := channelsHarness(t)
	fa.usageData.Status = store.UserEnabled
	// Usage 本身故障（存储不可用）
	opt.Access = errUsageAccess{err: errors.New("db down")}
	run(opt, fs, "/channels")
	if got := lastText(t, fs); got == "" {
		t.Fatal("状态查询失败应回复文案")
	}
}

// errUsageAccess 在 Usage 处返回错误的 Access 假实现。
type errUsageAccess struct {
	*fakeAccess
	err error
}

func (f errUsageAccess) Usage(context.Context, int64) (access.Usage, error) {
	return access.Usage{}, f.err
}
