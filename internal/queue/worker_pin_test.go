package queue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// newPinJobWithRequest 在测试库中建好用户与 pin 标记的 queued 请求行，
// 返回关联任务（置顶收尾流程专用）。
func newPinJobWithRequest(t *testing.T, s *store.Store) (Job, store.Request) {
	t.Helper()
	if _, err := s.CreateUser(context.Background(), store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	r, err := s.CreateRequest(context.Background(), store.Request{
		UserID: 1, SourceKind: store.SourcePublic, ChannelKey: "example", MessageID: 7, Pin: true,
	})
	if err != nil {
		t.Fatalf("创建置顶请求失败: %v", err)
	}
	return NewJob(1, 1, tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: 7},
		0, r.ID), r
}

// TestProcessPinTaskBypassesSwitchAndRecordsResult：pin 任务绕过频道同步
// 开关，置顶结果（成功数/总数）回写请求行并给用户发确认文案。
func TestProcessPinTaskBypassesSwitchAndRecordsResult(t *testing.T) {
	s := openStore(t)
	job, r := newPinJobWithRequest(t, s)
	copier := &fakeCopier{outcome: PinOutcome{OK: 2, Total: 3, Targets: []PinTarget{
		{Label: "我的频道", Pinned: true}, {Label: "我的群组", Pinned: true}, {Label: "第三个", Pinned: false},
	}}}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  &fakeSender{},
		Store:   s,
		Copier:  copier,
		// 同步开关关闭：pin 任务必须仍然投递副本并置顶（显式意图优先）
		ChannelCopyEnabled: func() bool { return false },
		Log:                testLog(),
	}

	Process(deps)(context.Background(), job)

	if len(copier.pins) != 1 || !copier.pins[0] {
		t.Fatalf("pin 任务应带置顶标记投递副本（绕过开关）: %v", copier.pins)
	}
	got, err := s.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if !got.Pin || got.PinOK != 2 || got.PinTotal != 3 {
		t.Fatalf("置顶结果应回写请求行: %+v", got)
	}
}

// TestProcessPinTaskPartialResultText：部分置顶失败时确认文案带原消息链接、
// 成功目标名与失败目标名。
func TestProcessPinTaskPartialResultText(t *testing.T) {
	s := openStore(t)
	job, _ := newPinJobWithRequest(t, s)
	sender := &fakeSender{}
	copier := &fakeCopier{outcome: PinOutcome{OK: 1, Total: 2, Targets: []PinTarget{
		{Label: "我的频道", URL: "https://t.me/mychan", Pinned: true}, {Label: "我的群组", Pinned: false},
	}}}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Store:   s,
		Copier:  copier,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	var confirm string
	for _, text := range sender.texts() {
		if strings.Contains(text, "置顶") {
			confirm = text
		}
	}
	// 置顶目标（带跳转链接、逐行）+ 失败目标 + 原消息来源卡片缺一不可
	if confirm == "" ||
		!strings.Contains(confirm, "已置顶到：") ||
		!strings.Contains(confirm, `<a href="https://t.me/mychan">我的频道</a>`) ||
		!strings.Contains(confirm, "置顶失败：\n我的群组") ||
		!strings.Contains(confirm, `<blockquote><b>🔗 原消息</b>`+"\n"+`<a href="https://t.me/example/7">`) {
		t.Fatalf("确认文案应含置顶明细与原消息来源卡片，得到 %q", sender.texts())
	}
}

// TestProcessPinTaskZeroBindingsHint：完成时无绑定（0/0）提示绑定前提与
// 原消息链接，结果照常回写。
func TestProcessPinTaskZeroBindingsHint(t *testing.T) {
	s := openStore(t)
	job, r := newPinJobWithRequest(t, s)
	sender := &fakeSender{}
	copier := &fakeCopier{}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Store:   s,
		Copier:  copier,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	got, err := s.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if !got.Pin || got.PinOK != 0 || got.PinTotal != 0 {
		t.Fatalf("无绑定应回写 0/0: %+v", got)
	}
	var confirm string
	for _, text := range sender.texts() {
		if strings.Contains(text, "尚未绑定") {
			confirm = text
		}
	}
	if confirm == "" || !strings.Contains(confirm, "https://t.me/example/7") {
		t.Fatalf("应回复零绑定提示并带原消息链接，得到 %q", sender.texts())
	}
}

// TestProcessPinTaskSkippedBindingsHint：路由不匹配（绑定属于其他受理 bot）
// 的目标单独成行提示，不计入置顶失败。
func TestProcessPinTaskSkippedBindingsHint(t *testing.T) {
	s := openStore(t)
	job, _ := newPinJobWithRequest(t, s)
	sender := &fakeSender{}
	copier := &fakeCopier{outcome: PinOutcome{OK: 1, Total: 1, Targets: []PinTarget{
		{Label: "我的频道", Pinned: true},
	}, Skipped: []PinTarget{{Label: "他bot的群"}}}}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Store:   s,
		Copier:  copier,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	var confirm string
	for _, text := range sender.texts() {
		if strings.Contains(text, "置顶") {
			confirm = text
		}
	}
	if confirm == "" ||
		!strings.Contains(confirm, "另有 1 个绑定属于其他机器人") ||
		!strings.Contains(confirm, "他bot的群") ||
		strings.Contains(confirm, "置顶失败") {
		t.Fatalf("确认文案应提示被路由跳过的绑定且不计入失败，得到 %q", sender.texts())
	}
}

// TestProcessPinTaskOnlySkippedHint：全部绑定都被路由跳过（0/0 + Skipped）
// 时给出点名提示，而不是"尚未绑定"。
func TestProcessPinTaskOnlySkippedHint(t *testing.T) {
	s := openStore(t)
	job, r := newPinJobWithRequest(t, s)
	sender := &fakeSender{}
	copier := &fakeCopier{outcome: PinOutcome{Skipped: []PinTarget{{Label: "他bot的群"}}}}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Store:   s,
		Copier:  copier,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	got, err := s.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if got.PinOK != 0 || got.PinTotal != 0 {
		t.Fatalf("被跳过的绑定不计入置顶结果: %+v", got)
	}
	var confirm string
	for _, text := range sender.texts() {
		if strings.Contains(text, "置顶") {
			confirm = text
		}
	}
	if confirm == "" ||
		!strings.Contains(confirm, "受理机器人名下暂无绑定") ||
		!strings.Contains(confirm, "他bot的群") ||
		strings.Contains(confirm, "尚未绑定") {
		t.Fatalf("应提示路由归属而非零绑定，得到 %q", sender.texts())
	}
}

// TestProcessPinTaskFailureHints：置顶失败目标带错误码时，确认文案按码
// 给出具体处置指引（被移出→重新绑定、限流→稍后重试），未知码回落通用
// 权限提示。
func TestProcessPinTaskFailureHints(t *testing.T) {
	s := openStore(t)
	job, _ := newPinJobWithRequest(t, s)
	sender := &fakeSender{}
	copier := &fakeCopier{outcome: PinOutcome{OK: 0, Total: 3, Targets: []PinTarget{
		{Label: "被移出的频道", ErrCode: "SEND_TARGET_INVALID"},
		{Label: "限流的群", ErrCode: "TELEGRAM_RATE_LIMIT"},
		{Label: "未分类目标"},
	}}}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Store:   s,
		Copier:  copier,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), job)

	var confirm string
	for _, text := range sender.texts() {
		if strings.Contains(text, "置顶失败") {
			confirm = text
		}
	}
	if confirm == "" ||
		!strings.Contains(confirm, "被移出的频道（机器人已被移出该频道/群组或权限被收回，请解绑后重新绑定）") ||
		!strings.Contains(confirm, "限流的群（Telegram 限流，稍后可用 /pin 重试）") ||
		!strings.Contains(confirm, "未分类目标（请检查机器人的置顶权限）") {
		t.Fatalf("置顶失败应按错误码给具体指引，得到 %q", sender.texts())
	}
}

// TestPinFailureHint：错误码 → 处置指引映射；未知码与空码回落通用权限提示。
func TestPinFailureHint(t *testing.T) {
	cases := map[string]string{
		"SEND_TARGET_INVALID": "机器人已被移出该频道/群组或权限被收回，请解绑后重新绑定",
		"MESSAGE_NOT_FOUND":   "副本消息已被删除，无法置顶",
		"TELEGRAM_RATE_LIMIT": "Telegram 限流，稍后可用 /pin 重试",
		"PEER_FLOOD":          "Telegram 对账号的临时限制，请稍后再试",
		"":                    "请检查机器人的置顶权限",
		"SOME_UNKNOWN":        "请检查机器人的置顶权限",
	}
	for code, want := range cases {
		if got := PinFailureHint(code); got != want {
			t.Errorf("PinFailureHint(%q) = %q，期望 %q", code, got, want)
		}
	}
}

// TestErrorDetailText：根因提取优先 cause（保留错误链文本）、无 cause 用
// 内部描述，超长按字符截断加省略号。
func TestErrorDetailText(t *testing.T) {
	if got := errorDetailText(apperr.Wrap(apperr.CodeRateLimited, errors.New("FLOOD_WAIT_X: 3000"))); got != "FLOOD_WAIT_X: 3000" {
		t.Errorf("应取 cause 文本，得到 %q", got)
	}
	if got := errorDetailText(apperr.New(apperr.CodeInterrupted, "进程退出中断任务")); got != "进程退出中断任务" {
		t.Errorf("无 cause 应用内部描述，得到 %q", got)
	}
	long := strings.Repeat("错", 400)
	got := errorDetailText(apperr.Wrap(apperr.CodeInternal, errors.New(long)))
	if runes := []rune(got); len(runes) != errorDetailLimit+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("超长应截断到 %d 字符加省略号，得到 %d 字符", errorDetailLimit, len(runes))
	}
}
