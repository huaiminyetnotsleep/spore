package botapi

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
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
	invites    []store.WatchInviteRequest
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

func (f *fakeWatch) ListInvitesByUser(context.Context, int64) ([]store.WatchInviteRequest, error) {
	return f.invites, nil
}

func TestHandleWatchNoService(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	run(opt, snd, "/watch @chan")
	if got := snd.texts(); len(got) != 1 || !strings.Contains(got[0], "未启用") {
		t.Fatalf("未接入服务应回复未启用: %v", got)
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

func TestHandleWatchListRendersInvites(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	opt.Watch = &fakeWatch{rows: []store.WatchSource{
		{ChannelID: -1001, Title: "频道A", Status: store.WatchApproved, Enabled: true},
	}, invites: []store.WatchInviteRequest{
		{ID: 1, Title: "私有频道", MaskedHash: "AbCd…5678", Status: store.WatchInviteWaitingBot},
		{ID: 2, MaskedHash: "zzzz…yyyy", Status: store.WatchInvitePending},
	}}
	run(opt, snd, "/watch")
	got := snd.texts()
	if len(got) != 1 {
		t.Fatalf("应回复一条列表: %v", got)
	}
	for _, want := range []string{"邀请链接申请", "私有频道", "待设 Bot 管理员", "待审批", "zzzz…yyyy"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("列表应包含 %q: %s", want, got[0])
		}
	}
}

func TestHandleWatchInviteListOnlyNoSources(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	opt.Watch = &fakeWatch{invites: []store.WatchInviteRequest{
		{ID: 1, Title: "私有频道", Status: store.WatchInviteWaitingTelegram},
	}}
	run(opt, snd, "/watch")
	got := snd.texts()
	if len(got) != 1 || !strings.Contains(got[0], "等待加入频道") {
		t.Fatalf("仅有邀请申请也应渲染列表: %v", got)
	}
}

func TestHandleWatchInviteOutcomes(t *testing.T) {
	cases := []struct {
		kind watch.SubmitOutcomeKind
		want string
	}{
		{watch.SubmitInvitePending, "等待管理员审批"},
		{watch.SubmitInviteWaitingTelegram, "等待频道侧审核"},
		{watch.SubmitInviteWaitingBot, "管理员"},
		{watch.SubmitInviteInvalid, "链接无效"},
		{watch.SubmitReaderUnavailable, "读取账号暂时不可用"},
	}
	for _, tc := range cases {
		opt, _, snd := newHarness(t, 2)
		opt.Bot = NewBotRef(42, "watcher_bot")
		opt.Watch = &fakeWatch{out: watch.SubmitOutcome{Kind: tc.kind, Title: "私有源"}}
		run(opt, snd, "/watch https://t.me/+AbCdEfGh12345678")
		got := snd.texts()
		if len(got) != 1 || !strings.Contains(got[0], tc.want) {
			t.Errorf("%s 应回复含 %q: %v", tc.kind, tc.want, got)
		}
	}
}

func TestHandleWatchInviteOutcomeNoHashLeak(t *testing.T) {
	opt, _, snd := newHarness(t, 2)
	opt.Bot = NewBotRef(42, "watcher_bot")
	opt.Watch = &fakeWatch{out: watch.SubmitOutcome{
		Kind: watch.SubmitInviteWaitingBot, Title: "私有源",
	}}
	run(opt, snd, "/watch https://t.me/+AbCdEfGh12345678")
	for _, got := range snd.texts() {
		if strings.Contains(got, "AbCdEfGh12345678") {
			t.Fatalf("Bot 回复不得包含完整邀请 hash: %s", got)
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

// 普通群组消息在路由层跳过并打原因日志（不进 listener、不触发私聊分流）。
func TestUpdateHandlerBasicGroupSkipLogged(t *testing.T) {
	capture := &sliceLogCapture{}
	var forwarded bool
	opt := Options{
		Log: slog.New(capture),
		OnSourceMessage: func(_ *models.Message, _ delivery.Sender, _ int64, _ string) {
			forwarded = true
		},
	}
	updateHandler(opt)(context.Background(), nil, &models.Update{Message: &models.Message{
		ID:    1,
		Chat:  models.Chat{ID: -99, Type: models.ChatTypeGroup},
		From:  &models.User{ID: 5, IsBot: true},
		Video: &models.Video{},
	}})
	if forwarded {
		t.Fatal("普通群组消息不应进入监听回调")
	}
	if !capture.contains("普通群组不支持监听") || !capture.contains("from_is_bot=true") {
		t.Fatalf("应记录跳过原因与发送者身份: %v", capture.lines)
	}
}

// sliceLogCapture 聚合日志行（跳过日志断言用）。
type sliceLogCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *sliceLogCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *sliceLogCapture) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.Key + "=" + a.Value.String())
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, b.String())
	return nil
}
func (c *sliceLogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *sliceLogCapture) WithGroup(string) slog.Handler      { return c }

func (c *sliceLogCapture) contains(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}
