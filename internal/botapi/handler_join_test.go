package botapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/joinmgr"
)

// fakeChannelJoin 记录 Submit 调用并按配置返回。
type fakeChannelJoin struct {
	mu       chan struct{}
	gotUser  int64
	gotOwner bool
	gotText  string
	out      joinmgr.SubmitOutcome
	err      error
}

func (f *fakeChannelJoin) Submit(_ context.Context, userID int64, isOwner bool, text string) (joinmgr.SubmitOutcome, error) {
	f.gotUser, f.gotOwner, f.gotText = userID, isOwner, text
	return f.out, f.err
}

func joinUser(id int64) models.User { return models.User{ID: id, FirstName: "测试"} }

func TestHandleJoinNoService(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	handleUpdate(context.Background(), opt, snd, joinUser(100), 100, "/join https://t.me/+AbCdEfGh12345678", 0)
	if got := snd.texts(); len(got) != 1 || !strings.Contains(got[0], "不可用") {
		t.Fatalf("未接入服务应回复不可用: %v", got)
	}
}

func TestHandleJoinUsage(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	fake := &fakeChannelJoin{}
	opt.ChannelJoin = fake
	handleUpdate(context.Background(), opt, snd, joinUser(100), 100, "/join", 0)
	got := snd.texts()
	if len(got) != 1 || !strings.Contains(got[0], "用法") || !strings.Contains(got[0], "t.me/+") {
		t.Fatalf("无参数应回用法: %v", got)
	}
	if fake.gotText != "" {
		t.Fatalf("无参数不应触达服务")
	}
}

func TestHandleJoinOutcomes(t *testing.T) {
	cases := []struct {
		name string
		out  joinmgr.SubmitOutcome
		want []string
	}{
		{"已加入", joinmgr.SubmitOutcome{Kind: joinmgr.SubmitJoined, Title: "私有频道"},
			[]string{"已加入频道「私有频道」", "消息链接"}},
		{"已在频道", joinmgr.SubmitOutcome{Kind: joinmgr.SubmitAlreadyJoined, Title: "X"},
			[]string{"已在频道「X」"}},
		{"待审核", joinmgr.SubmitOutcome{Kind: joinmgr.SubmitPending, Title: "X"},
			[]string{"等待号主审核"}},
		{"重复申请", joinmgr.SubmitOutcome{Kind: joinmgr.SubmitPendingDuplicate},
			[]string{"等待审核"}},
		{"功能关闭", joinmgr.SubmitOutcome{Kind: joinmgr.SubmitDisabled},
			[]string{"已关闭"}},
		{"达上限", joinmgr.SubmitOutcome{Kind: joinmgr.SubmitLimitReached, Requests: 20},
			[]string{"上限", "20"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opt, _, snd := newHarness(t, 2)
			fake := &fakeChannelJoin{out: c.out}
			opt.ChannelJoin = fake
			handleUpdate(context.Background(), opt, snd, joinUser(100), 100, "/join https://t.me/+AbCdEfGh12345678", 0)
			got := snd.texts()
			if len(got) != 1 {
				t.Fatalf("应回复一条: %v", got)
			}
			for _, want := range c.want {
				if !strings.Contains(got[0], want) {
					t.Fatalf("文案应含 %q: %s", want, got[0])
				}
			}
		})
	}
}

func TestHandleJoinPassesOnlyInviteArgument(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	fake := &fakeChannelJoin{}
	opt.ChannelJoin = fake
	handleUpdate(context.Background(), opt, snd, joinUser(100), 100, "/JOIN@spore_bot AbCdEfGh12345678", 0)
	if fake.gotText != "AbCdEfGh12345678" {
		t.Fatalf("应只传递邀请参数，得到 %q", fake.gotText)
	}
}

func TestHandleJoinOwnerFlag(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	fake := &fakeChannelJoin{}
	opt.ChannelJoin = fake
	opt.IsOwner = func(ctx context.Context, userID int64) (bool, error) {
		return userID == 1, nil
	}
	handleUpdate(context.Background(), opt, snd, joinUser(1), 1, "/join https://t.me/+AbCdEfGh12345678", 0)
	if !fake.gotOwner {
		t.Fatalf("owner 应即时提交")
	}
	handleUpdate(context.Background(), opt, snd, joinUser(2), 2, "/join https://t.me/+AbCdEfGh12345678", 0)
	if fake.gotOwner {
		t.Fatalf("非 owner 应走审核")
	}
	if fake.gotUser != 2 {
		t.Fatalf("用户 ID 未传递: %d", fake.gotUser)
	}
}

func TestHandleJoinOwnerCheckFailureFallback(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	fake := &fakeChannelJoin{}
	opt.ChannelJoin = fake
	opt.IsOwner = func(ctx context.Context, userID int64) (bool, error) {
		return false, errors.New("db down")
	}
	handleUpdate(context.Background(), opt, snd, joinUser(1), 1, "/join https://t.me/+AbCdEfGh12345678", 0)
	if fake.gotOwner {
		t.Fatalf("判定失败应按普通用户处理（审核兜底）")
	}
}

func TestHandleJoinInvalidInviteUsesInvite文案(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	fake := &fakeChannelJoin{err: apperr.New(apperr.CodeInvalidInviteURL, "邀请链接格式无效")}
	opt.ChannelJoin = fake
	handleUpdate(context.Background(), opt, snd, joinUser(100), 100, "/join https://t.me/+invalid", 0)
	got := snd.texts()
	if len(got) != 1 || !strings.Contains(got[0], "频道邀请链接") || strings.Contains(got[0], "消息链接") {
		t.Fatalf("邀请链接错误不应显示消息链接文案: %v", got)
	}
}

func TestHandleJoinServiceError(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	fake := &fakeChannelJoin{err: apperr.New(apperr.CodeInvalidURL, "链接已失效")}
	opt.ChannelJoin = fake
	handleUpdate(context.Background(), opt, snd, joinUser(100), 100, "/join https://t.me/+AbCdEfGh12345678", 0)
	got := snd.texts()
	if len(got) != 1 || !strings.Contains(got[0], apperr.UserText(apperr.CodeInvalidURL)) {
		t.Fatalf("服务错误应转为用户文案: %v", got)
	}
}
