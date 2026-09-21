package botapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/watch"
)

// fakeWatch 记录调用并按配置返回（/watch、/unwatch 命令路径）。
type fakeWatch struct {
	gotUser    int64
	gotOwner   bool
	gotTarget  string
	gotBotID   int64
	gotBotName string
	unwatchErr error
	out        watch.SubmitOutcome
	rows       []store.WatchSource
}

func (f *fakeWatch) Submit(_ context.Context, userID int64, isOwner bool, target string, botID int64, botUsername string) (watch.SubmitOutcome, error) {
	f.gotUser, f.gotOwner, f.gotTarget = userID, isOwner, target
	f.gotBotID, f.gotBotName = botID, botUsername
	return f.out, nil
}

func (f *fakeWatch) Unwatch(_ context.Context, userID int64, isOwner bool, target string) (store.WatchSource, error) {
	f.gotUser, f.gotOwner, f.gotTarget = userID, isOwner, target
	if f.unwatchErr != nil {
		return store.WatchSource{}, f.unwatchErr
	}
	return store.WatchSource{ChannelID: -1001, Title: "已移除源"}, nil
}

func (f *fakeWatch) ListByUser(context.Context, int64) ([]store.WatchSource, error) {
	return f.rows, nil
}

func TestHandleWatchNoService(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	run(opt, snd, "/watch @chan")
	if got := snd.texts(); len(got) != 1 || !strings.Contains(got[0], "不可用") {
		t.Fatalf("未接入服务应回复不可用: %v", got)
	}
}

func TestHandleWatchUsageWhenNoArgButEmptyList(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	opt.Watch = &fakeWatch{}
	run(opt, snd, "/watch")
	got := snd.texts()
	if len(got) != 1 || !strings.Contains(got[0], "还没有监听源") {
		t.Fatalf("空列表应提示添加: %v", got)
	}
}

func TestHandleWatchListRendersStatus(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	opt.Watch = &fakeWatch{rows: []store.WatchSource{
		{ChannelID: -1001, Title: "频道A", Status: store.WatchPending},
		{ChannelID: -1002, Title: "频道B", Status: store.WatchApproved, Enabled: true},
		{ChannelID: -1003, Username: "c3", Status: store.WatchApproved, Enabled: false},
	}}
	run(opt, snd, "/watch")
	got := snd.texts()
	if len(got) != 1 {
		t.Fatalf("应回复一条列表: %v", got)
	}
	for _, want := range []string{"频道A", "待审批", "频道B", "监听中", "c3", "已暂停"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("列表应包含 %q: %s", want, got[0])
		}
	}
}

func TestHandleWatchOutcomes(t *testing.T) {
	cases := []struct {
		kind watch.SubmitOutcomeKind
		want string
	}{
		{watch.SubmitDisabled, "未开放"},
		{watch.SubmitUserNotAllowed, "/start"},
		{watch.SubmitInvalid, "无法识别目标"},
		{watch.SubmitNotAdmin, "管理员"},
		{watch.SubmitAlreadyMine, "已在你的监听列表"},
		{watch.SubmitAlreadyOthers, "已由其他人添加"},
		{watch.SubmitUserLimit, "每用户监听上限"},
		{watch.SubmitSourceLimit, "总数已达上限"},
		{watch.SubmitPending, "等待管理员审批"},
		{watch.SubmitActive, "已开始监听"},
	}
	for _, tc := range cases {
		opt, _, snd := newHarness(t, 2)
		fake := &fakeWatch{out: watch.SubmitOutcome{Kind: tc.kind, Title: "源X", Count: 3, Limit: 5}}
		opt.Watch = fake
		run(opt, snd, "/watch @somechan")
		got := snd.texts()
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s 应回复含 %q: %v", tc.kind, tc.want, got)
		}
		if fake.gotTarget != "@somechan" || fake.gotUser != 7 {
			t.Errorf("%s 参数未透传: %+v", tc.kind, fake)
		}
	}
}

func TestHandleWatchOwnerFlagPassed(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	fake := &fakeWatch{out: watch.SubmitOutcome{Kind: watch.SubmitActive, Title: "X"}}
	opt.Watch = fake
	opt.IsOwner = func(context.Context, int64) (bool, error) { return true, nil }
	run(opt, snd, "/watch -1001234567890")
	if !fake.gotOwner || fake.gotTarget != "-1001234567890" {
		t.Fatalf("号主与目标应透传: %+v", fake)
	}
}

func TestHandleWatchPassesReceivingBot(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	fake := &fakeWatch{out: watch.SubmitOutcome{Kind: watch.SubmitActive, Title: "X"}}
	opt.Watch = fake
	opt.Bot = NewBotRef(42, "watcher_bot")
	run(opt, snd, "/watch @somechan")
	if fake.gotBotID != 42 || fake.gotBotName != "watcher_bot" {
		t.Fatalf("受理 bot 应透传: id=%d name=%q", fake.gotBotID, fake.gotBotName)
	}
}

func TestHandleUnwatchNotFound(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	opt.Watch = &fakeWatch{unwatchErr: store.ErrNotFound}
	run(opt, snd, "/unwatch @chan")
	got := snd.texts()
	if len(got) != 1 || !strings.Contains(got[0], "没有找到") {
		t.Fatalf("未命中应回复找不到: %v", got)
	}
}

func TestHandleUnwatchErrorPropagatesControlledText(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	opt.Watch = &fakeWatch{unwatchErr: errors.New("boom")}
	run(opt, snd, "/unwatch @chan")
	got := snd.texts()
	if len(got) != 1 || strings.Contains(got[0], "已移除") {
		t.Fatalf("服务错误不应回成功文案: %v", got)
	}
}

func TestHandleUnwatchSuccess(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	opt.Watch = &fakeWatch{}
	run(opt, snd, "/unwatch @chan")
	got := snd.texts()
	if len(got) != 1 || !strings.Contains(got[0], "已移除监听源") {
		t.Fatalf("应回复移除成功: %v", got)
	}
}
