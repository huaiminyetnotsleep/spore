package notify

// 活动通知与模板渲染测试：三种活动事件逐次即时投递（不落 events 表、无冷却）、
// 配置化通道接管 / 回退 owner 私聊 / 无通道三条路径、告警 payload 最新值渲染、
// 模板缺气回退与时间格式化（运营时区 / GMT 偏移标签）。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// waitChannel 等待信号或超时失败（异步活动上报的确定性等待）。
func waitChannel(t *testing.T, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("等待活动通知超时")
	}
}

func TestAdminLoginDeliveredViaRuntimeNotifier(t *testing.T) {
	st := openStore(t)
	clock := newClock(time.Date(2026, 9, 17, 12, 8, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	rt := &fakeRuntimeNotifier{result: RuntimeDeliveryResult{Managed: true, Delivered: 1}}
	h.SetRuntimeNotifier(rt)

	h.AdminLogin(context.Background(), AdminLoginData{
		Method: "访问密钥", IP: "103.173.224.102", OS: "macOS", Browser: "Chrome",
	})
	if rt.callCount() != 1 {
		t.Fatalf("活动通知应经配置化通道投递 1 次，得到 %d", rt.callCount())
	}
	message := rt.calls[0]
	if message.EventType != KeyWebAdminLogin || message.Category != CategoryActivity ||
		message.Severity != SeverityInfo || !message.Activity {
		t.Fatalf("活动通知元数据不符: %+v", message)
	}
	for _, want := range []string{
		"🕐 时间：", "🌐 IP：103.173.224.102", "💻 操作系统：macOS", "🧭 浏览器：Chrome", "🔑 方式：访问密钥",
	} {
		if !strings.Contains(message.Body, want) {
			t.Errorf("通知正文缺少关键信息 %q，正文=%q", want, message.Body)
		}
	}
	if _, err := st.GetEvent(context.Background(), KeyWebAdminLogin); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("活动通知不应写入事件中心，得到 %v", err)
	}
}

func TestActivityFallsBackToOwnerDM(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 42)
	clock := newClock(time.Date(2026, 9, 17, 12, 8, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	snd := &fakeNotifier{}
	h.SetSender(snd)

	h.UserApplied(context.Background(), UserApplicationData{
		UserID: 100, Username: "user", DisplayName: "<b>昵称</b>", SourceBotUsername: "mybot",
	})
	if snd.count() != 1 {
		t.Fatalf("未配置通道时应回退 owner 私聊 1 次，得到 %d", snd.count())
	}
	message := snd.messages[0]
	// HTML 通道：payload 中的用户可控串必须整体转义，且无"已发生"脚注。
	if !strings.Contains(message, "&lt;b&gt;昵称&lt;/b&gt;") || strings.Contains(message, "<b>昵称") {
		t.Errorf("HTML 通知未转义用户可控串: %q", message)
	}
	if strings.Contains(message, "已发生") {
		t.Errorf("活动通知不应携带告警计数脚注: %q", message)
	}
	if !strings.Contains(message, "🆔 用户 ID：100") || !strings.Contains(message, "🤖 来源 Bot：mybot") {
		t.Errorf("申请通知缺少关键信息: %q", message)
	}
}

func TestChannelJoinRequestedActivity(t *testing.T) {
	st := openStore(t)
	clock := newClock(time.Date(2026, 9, 17, 12, 8, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	rt := &fakeRuntimeNotifier{result: RuntimeDeliveryResult{Managed: true, Delivered: 1}}
	h.SetRuntimeNotifier(rt)

	h.ChannelJoinRequested(context.Background(), ChannelJoinRequestData{
		UserID: 100, Username: "alice", ChannelTitle: "示例频道", Participants: 0,
	})
	if rt.callCount() != 1 {
		t.Fatalf("应投递 1 次，得到 %d", rt.callCount())
	}
	body := rt.calls[0].Body
	for _, want := range []string{"📺 频道：示例频道", "（@alice）", "👥 链接人数：未知"} {
		if !strings.Contains(body, want) {
			t.Errorf("频道加入申请通知缺少 %q，正文=%q", want, body)
		}
	}
}

func TestActivitySuppressedWhenPolicyDisables(t *testing.T) {
	st := openStore(t)
	clock := newClock(time.Date(2026, 9, 17, 12, 8, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	// Managed 但被策略抑制（Delivered=0）：不回退 owner，也不报错。
	rt := &fakeRuntimeNotifier{result: RuntimeDeliveryResult{Managed: true}}
	h.SetRuntimeNotifier(rt)
	snd := &fakeNotifier{}
	h.SetSender(snd)

	h.AdminLogin(context.Background(), AdminLoginData{Method: "GitHub", IP: "1.1.1.1", OS: "macOS", Browser: "Chrome"})
	if rt.callCount() != 1 || snd.count() != 0 {
		t.Errorf("Managed 抑制后不应回退兼容通道: runtime=%d owner=%d", rt.callCount(), snd.count())
	}
}

func TestRaiseRendersLatestPayload(t *testing.T) {
	st := openStore(t)
	clock := newClock(time.Date(2026, 9, 17, 12, 8, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	rt := &fakeRuntimeNotifier{result: RuntimeDeliveryResult{Managed: true, Delivered: 1}}
	h.SetRuntimeNotifier(rt)
	ctx := context.Background()

	h.Raise(ctx, KeyTaskFailures, SeverityError, StreakData{Count: 5, Threshold: 5, Detail: "@user 消息 12"})
	if rt.callCount() != 1 {
		t.Fatalf("首次告警应投递，得到 %d", rt.callCount())
	}
	body := rt.calls[0].Body
	for _, want := range []string{
		"🕐 时间：", "🔢 连续失败：5 次（阈值 5）", "🔗 最近失败：@user 消息 12",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("告警正文缺少 %q，正文=%q", want, body)
		}
	}

	// 冷却窗口外的第二次发生：事件合并，通知正文展示最新 payload。
	clock.Advance(2 * DefaultCooldown)
	h.Raise(ctx, KeyTaskFailures, SeverityError, StreakData{Count: 9, Threshold: 5, Detail: "@other 消息 3"})
	if rt.callCount() != 2 {
		t.Fatalf("冷却窗口外应再次投递，得到 %d", rt.callCount())
	}
	body = rt.calls[1].Body
	if !strings.Contains(body, "🔢 连续失败：9 次") || !strings.Contains(body, "@other 消息 3") {
		t.Errorf("合并告警应展示最新 payload，正文=%q", body)
	}
	if e := mustEvent(t, st, KeyTaskFailures); e.Count != 2 {
		t.Errorf("事件合并次数应累计为 2，得到 %d", e.Count)
	}
}

func TestRenderTextAlertKeepsFooter(t *testing.T) {
	message := EventNotification{
		SourceName: "Spore", TypeLabel: "系统告警", Severity: SeverityWarn,
		EventType: KeyTempDirUsage, Title: "临时目录占用超限", Count: 3,
		Body: "🕐 时间：2026/09/17 20:08（GMT+8）\n💾 当前占用：2.0GB",
	}
	text := RenderText(message)
	if !strings.Contains(text, "已发生 3 次，请前往管理端事件中心查看。") {
		t.Errorf("告警文本应保留计数脚注: %q", text)
	}
	activity := EventNotification{
		SourceName: "Spore", TypeLabel: "活动通知", Severity: SeverityInfo,
		EventType: KeyWebAdminLogin, Title: "管理后台登录成功", Activity: true,
		Body: "🕐 时间：2026/09/17 20:08（GMT+8）",
	}
	if strings.Contains(RenderText(activity), "已发生") {
		t.Errorf("活动通知不应携带计数脚注: %q", RenderText(activity))
	}
}

func TestRenderEventBodyFallsBackToDescription(t *testing.T) {
	// 未知事件 key：无模板可渲染，回退目录受控描述兜底文案。
	if got := renderEventBody("unknown.key", templateData{Time: "x"}); got != "系统异常事件，请查看管理端事件中心。" {
		t.Errorf("未知事件应回退兜底文案，得到 %q", got)
	}
	// 已知但无专属 payload 的事件（如 session_offline）：模板仅含时间行。
	got := renderEventBody(KeySessionOffline, templateData{Time: "2026/09/17 20:08（GMT+8）"})
	if !strings.Contains(got, "🕐 时间：2026/09/17 20:08（GMT+8）") {
		t.Errorf("时间行缺失: %q", got)
	}
}

func TestFormatTimeUsesInjectedTimezone(t *testing.T) {
	st := openStore(t)
	clock := newClock(time.Date(2026, 9, 17, 12, 8, 0, 0, time.UTC))
	h := newHub(t, st, clock)

	// 缺省：固定 GMT+8。
	if got := h.formatTime(clock.Now()); got != "2026/09/17 20:08（GMT+8）" {
		t.Errorf("缺省时区渲染=%q", got)
	}
	// 注入 UTC（GMT 偏移为 0）与负偏移。
	h.SetTimezone(func() *time.Location { return time.UTC })
	if got := h.formatTime(clock.Now()); got != "2026/09/17 12:08（GMT+0）" {
		t.Errorf("UTC 渲染=%q", got)
	}
	zone := time.FixedZone("test", -5*3600-30*60)
	h.SetTimezone(func() *time.Location { return zone })
	if got := h.formatTime(clock.Now()); got != "2026/09/17 06:38（GMT-5:30）" {
		t.Errorf("负半时区渲染=%q", got)
	}
}

func TestTemplateFuncs(t *testing.T) {
	if got := humanBytes(2 << 30); got != "2.0GB" {
		t.Errorf("humanBytes(2GB)=%q", got)
	}
	if got := displayOrUnknown(""); got != "未知" {
		t.Errorf("displayOrUnknown(\"\")=%q", got)
	}
	if got := firstNonEmpty("", " ", "x"); got != "x" {
		t.Errorf("firstNonEmpty=%q", got)
	}
}
