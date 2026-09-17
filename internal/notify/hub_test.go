package notify

// Hub 测试：去重合并、resolved 重开（last_notified_at 清零）、冷却窗口、
// 发送成功记录冷却、失败不阻塞且可重试、SetSender 补发与并发安全（-race）、事件源阈值。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// ---- 测试基础设施 ----

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), testLog())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// withOwner 建立可接收通知的 owner 用户（管理员 Telegram ID = 用户 ID）。
func withOwner(t *testing.T, s *store.Store, id int64) {
	t.Helper()
	if _, err := s.CreateUser(context.Background(), store.User{ID: id, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建 owner 用户失败: %v", err)
	}
	if err := s.SetOwner(context.Background(), id, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}
}

// fakeClock 可推进的注入时钟。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock(start time.Time) *fakeClock { return &fakeClock{t: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// fakeNotifier 可编程的通知通道假实现。
type fakeNotifier struct {
	mu       sync.Mutex
	calls    int
	chats    []int64
	messages []string
	err      error // 非 nil 时全部发送失败
}

func (f *fakeNotifier) SendMessage(_ context.Context, chatID int64, text string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.chats = append(f.chats, chatID)
	f.messages = append(f.messages, text)
	if f.err != nil {
		return 0, f.err
	}
	return 1, nil
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeRuntimeNotifier struct {
	mu     sync.Mutex
	calls  []EventNotification
	result RuntimeDeliveryResult
	err    error
}

func (f *fakeRuntimeNotifier) NotifyEvent(_ context.Context, message EventNotification) (RuntimeDeliveryResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, message)
	return f.result, f.err
}

func (f *fakeRuntimeNotifier) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func newHub(t *testing.T, st *store.Store, clock *fakeClock) *Hub {
	t.Helper()
	h, err := New(Options{Store: st, Log: testLog(), Now: clock.Now})
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	return h
}

func mustEvent(t *testing.T, st *store.Store, key string) store.Event {
	t.Helper()
	e, err := st.GetEvent(context.Background(), key)
	if err != nil {
		t.Fatalf("读取事件 %s 失败: %v", key, err)
	}
	return e
}

// ---- Raise：落库 + 通知 ----

func TestRaiseCreatesEventAndNotifies(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 42)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	snd := &fakeNotifier{}
	h.SetSender(snd)
	snd.calls = 0 // SetSender 触发的补发不计入

	h.Raise(context.Background(), KeySessionOffline, SeverityError, "会话离线")

	e := mustEvent(t, st, KeySessionOffline)
	if e.Status != store.EventOpen || e.Severity != SeverityError || e.Count != 1 {
		t.Fatalf("事件写入不符: %+v", e)
	}
	if snd.count() != 1 {
		t.Fatalf("应通知一次，得到 %d", snd.count())
	}
	if len(snd.chats) != 1 || snd.chats[0] != 42 {
		t.Fatalf("应发往 owner 私聊 42，得到 %v", snd.chats)
	}
	if e.LastNotifiedAt != clock.Now().UnixMilli() {
		t.Fatalf("通知成功应记录 last_notified_at=%d，得到 %d", clock.Now().UnixMilli(), e.LastNotifiedAt)
	}
}

func TestRuntimeNotifierManagedDeliverySkipsLegacyOwnerRoute(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 42)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	runtime := &fakeRuntimeNotifier{result: RuntimeDeliveryResult{Managed: true, Attempted: 1, Delivered: 1}}
	legacy := &fakeNotifier{}
	h.SetRuntimeNotifier(runtime)
	h.SetSender(legacy)
	legacy.calls = 0

	h.Raise(context.Background(), KeySessionOffline, SeverityError, "ignored")

	if runtime.callCount() != 1 {
		t.Fatalf("configured runtime should receive event once, got %d", runtime.callCount())
	}
	if legacy.count() != 0 {
		t.Fatalf("managed runtime must not duplicate legacy owner notification, got %d", legacy.count())
	}
	e := mustEvent(t, st, KeySessionOffline)
	if e.LastNotifiedAt != clock.Now().UnixMilli() {
		t.Fatalf("successful configured delivery should advance cooldown, got %d", e.LastNotifiedAt)
	}
}

func TestRuntimeNotifierDisabledFallsBackToLegacyOwnerRoute(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 7)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	runtime := &fakeRuntimeNotifier{result: RuntimeDeliveryResult{Managed: false}}
	legacy := &fakeNotifier{}
	h.SetRuntimeNotifier(runtime)
	h.SetSender(legacy)
	legacy.calls = 0

	h.Raise(context.Background(), KeySessionOffline, SeverityError, "ignored")

	if runtime.callCount() != 1 || legacy.count() != 1 {
		t.Fatalf("disabled runtime should fall back once: runtime=%d legacy=%d", runtime.callCount(), legacy.count())
	}
}

func TestRaiseUsesControlledNotificationMessage(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 42)
	h := newHub(t, st, newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)))
	snd := &fakeNotifier{}
	h.SetSender(snd)
	snd.calls = 0
	h.Raise(context.Background(), KeySessionOffline, SeverityError,
		"BOT_TOKEN=secret https://t.me/private/123 用户消息正文")

	snd.mu.Lock()
	defer snd.mu.Unlock()
	if len(snd.messages) != 1 {
		t.Fatalf("应发送一条受控通知，得到 %d", len(snd.messages))
	}
	for _, forbidden := range []string{"BOT_TOKEN=secret", "https://t.me/private/123", "用户消息正文"} {
		if strings.Contains(snd.messages[0], forbidden) {
			t.Fatalf("通知不应包含敏感或用户输入 %q：%s", forbidden, snd.messages[0])
		}
	}
}

func TestRaiseMergesByKey(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	snd := &fakeNotifier{}
	h.SetSender(snd)
	snd.calls = 0

	h.Raise(context.Background(), KeyTaskFailures, SeverityError, "x")
	clock.Advance(5 * time.Minute)
	h.Raise(context.Background(), KeyTaskFailures, SeverityError, "y") // 冷却窗口内：合并不重发

	e := mustEvent(t, st, KeyTaskFailures)
	if e.Count != 2 || e.Status != store.EventOpen {
		t.Fatalf("应合并 count=2，得到 %+v", e)
	}
	if snd.count() != 1 {
		t.Fatalf("冷却窗口内不应重发，得到 %d 次发送", snd.count())
	}
}

// ---- 冷却窗口 ----

func TestCooldownSuppressesAndExpires(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h, err := New(Options{Store: st, Log: testLog(), Now: clock.Now, Cooldown: 30 * time.Minute})
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	snd := &fakeNotifier{}
	h.SetSender(snd)
	snd.calls = 0

	h.Raise(context.Background(), "k", SeverityWarn, "首次")
	clock.Advance(29 * time.Minute)
	h.Raise(context.Background(), "k", SeverityWarn, "窗口内")
	if snd.count() != 1 {
		t.Fatalf("29 分钟后仍应被冷却抑制，得到 %d 次发送", snd.count())
	}

	clock.Advance(2 * time.Minute) // 距上次通知 31 分钟
	h.Raise(context.Background(), "k", SeverityWarn, "窗口外")
	if snd.count() != 2 {
		t.Fatalf("超出冷却窗口应重发，得到 %d 次发送", snd.count())
	}
	e := mustEvent(t, st, "k")
	if e.Count != 3 {
		t.Fatalf("三次 raise 应合并 count=3，得到 %d", e.Count)
	}
}

// ---- resolved 重开：恢复后再次发生可重新触发通知 ----

func TestReopenRetriggersNotification(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 7)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	snd := &fakeNotifier{}
	h.SetSender(snd)
	snd.calls = 0

	h.Raise(context.Background(), KeySessionOffline, SeverityError, "离线")
	if snd.count() != 1 {
		t.Fatalf("首次应通知，得到 %d", snd.count())
	}

	// 管理员标记解决（或 MTProto 重连成功自动恢复）
	e := mustEvent(t, st, KeySessionOffline)
	if err := h.Resolve(context.Background(), e.ID); err != nil {
		t.Fatalf("解决事件失败: %v", err)
	}

	// 紧接着（远未到冷却期）事件再次发生：last_notified_at 已在重开时清零，
	// 必须重新通知（固化修正后的语义）
	clock.Advance(1 * time.Minute)
	h.Raise(context.Background(), KeySessionOffline, SeverityError, "又离线")
	if snd.count() != 2 {
		t.Fatalf("resolved 重开应重新触发通知，得到 %d 次发送", snd.count())
	}
	e = mustEvent(t, st, KeySessionOffline)
	if e.Status != store.EventOpen || e.Count != 2 || e.LastNotifiedAt != clock.Now().UnixMilli() {
		t.Fatalf("重开事件状态不符: %+v", e)
	}
}

// ---- 发送失败不阻塞且保留重试机会 ----

func TestSendFailureDoesNotBlockAndCanRetry(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	snd := &fakeNotifier{err: errors.New("bot api down")}
	h.SetSender(snd)
	snd.calls = 0

	h.Raise(context.Background(), "k", SeverityError, "x") // 发送失败：不 panic、不阻塞

	e := mustEvent(t, st, "k")
	if e.LastNotifiedAt != 0 {
		t.Fatalf("失败不应推进成功通知冷却时间，得到 last_notified_at=%d", e.LastNotifiedAt)
	}

	clock.Advance(10 * time.Minute)
	h.Raise(context.Background(), "k", SeverityError, "y")
	if snd.count() != 2 {
		t.Fatalf("发送失败后下一次事件应可重试，得到 %d 次尝试", snd.count())
	}
	clock.Advance(21 * time.Minute)
	h.Raise(context.Background(), "k", SeverityError, "z")
	if snd.count() != 3 {
		t.Fatalf("失败后仍应保留重试机会，得到 %d 次尝试", snd.count())
	}
}

// ---- 通道未就绪：仅入库，SetSender 后补发 ----

func TestFlushPendingOnSetSender(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 5)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)

	h.Raise(context.Background(), KeyStartupRecovered, SeverityWarn, "启动恢复") // 无 sender：仅入库

	snd := &fakeNotifier{}
	h.SetSender(snd) // Bot 就绪：补发未通知的 open 事件
	if snd.count() != 1 {
		t.Fatalf("SetSender 应补发未通知事件，得到 %d 次发送", snd.count())
	}
	if len(snd.chats) != 1 || snd.chats[0] != 5 {
		t.Fatalf("补发应发往 owner，得到 %v", snd.chats)
	}

	// 已通知且在冷却期内：再次注入（模拟 MTProto 重连）不重复推送
	snd2 := &fakeNotifier{}
	h.SetSender(snd2)
	if snd2.count() != 0 {
		t.Fatalf("冷却窗口内补发应被抑制，得到 %d 次发送", snd2.count())
	}

	// resolved 事件不补发
	e := mustEvent(t, st, KeyStartupRecovered)
	_ = st.ResolveEvent(context.Background(), e.ID)
	snd3 := &fakeNotifier{}
	h.SetSender(snd3)
	if snd3.count() != 0 {
		t.Fatalf("resolved 事件不应补发，得到 %d 次发送", snd3.count())
	}
}

func TestRaiseWithoutSenderOrOwnerKeepsEventOnly(t *testing.T) {
	st := openStore(t)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)

	h.Raise(context.Background(), "k", SeverityWarn, "无通道无 owner") // 不 panic

	e := mustEvent(t, st, "k")
	if e.LastNotifiedAt != 0 {
		t.Fatalf("未投递不应记录通知时间，得到 %d", e.LastNotifiedAt)
	}
}

// ---- 事件源：连续失败阈值与清零 ----

func TestTaskResultStreak(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h, err := New(Options{Store: st, Log: testLog(), Now: clock.Now, TaskFailThreshold: 3})
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	ctx := context.Background()

	h.TaskResult(ctx, false)
	h.TaskResult(ctx, false)
	if _, err := st.GetEvent(ctx, KeyTaskFailures); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("未达阈值不应产生事件，得到 %v", err)
	}

	h.TaskResult(ctx, false) // 第 3 次：触发
	e := mustEvent(t, st, KeyTaskFailures)
	if e.Count != 1 || e.Severity != SeverityError {
		t.Fatalf("达到阈值应产生事件: %+v", e)
	}

	// 成功清零并自动恢复事件：之后 2 次失败不触发
	h.TaskResult(ctx, true)
	e = mustEvent(t, st, KeyTaskFailures)
	if e.Status != store.EventResolved {
		t.Fatalf("任务恢复后事件应自动解决，得到 %s", e.Status)
	}
	h.TaskResult(ctx, false)
	h.TaskResult(ctx, false)
	e = mustEvent(t, st, KeyTaskFailures)
	if e.Count != 1 {
		t.Fatalf("清零后未达阈值不应合并，得到 count=%d", e.Count)
	}

	// 清零后再连败 3 次：再次触发并合并（通知受冷却窗口约束）
	h.TaskResult(ctx, false)
	e = mustEvent(t, st, KeyTaskFailures)
	if e.Count != 2 {
		t.Fatalf("再次达到阈值应合并事件，得到 count=%d", e.Count)
	}
}

func TestBotSendResultStreak(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h, err := New(Options{Store: st, Log: testLog(), Now: clock.Now, BotFailThreshold: 2})
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	ctx := context.Background()

	h.BotSendResult(ctx, false)
	if _, err := st.GetEvent(ctx, KeyBotSendFailures); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("未达阈值不应产生事件，得到 %v", err)
	}
	h.BotSendResult(ctx, false)
	if _, err := st.GetEvent(ctx, KeyBotSendFailures); err != nil {
		t.Fatalf("达到阈值应产生事件: %v", err)
	}

	h.BotSendResult(ctx, true) // 恢复：清零并自动解决事件
	e := mustEvent(t, st, KeyBotSendFailures)
	if e.Status != store.EventResolved {
		t.Fatalf("Bot API 恢复后事件应自动解决，得到 %s", e.Status)
	}
	h.BotSendResult(ctx, false)
	e = mustEvent(t, st, KeyBotSendFailures)
	if e.Count != 1 {
		t.Fatalf("清零后未达阈值不应合并，得到 count=%d", e.Count)
	}
}

func TestStoreWriteFailedRaises(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)

	h.StoreWriteFailed(context.Background())
	e := mustEvent(t, st, KeyStoreWriteFailed)
	if e.Severity != SeverityError {
		t.Fatalf("数据库写失败应为 error 级别，得到 %s", e.Severity)
	}
}

// ---- 临时目录占用（可注入检查函数）----

func TestCheckTempDirThresholdAndRecover(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	usage := int64(0)
	h, err := New(Options{
		Store: st, Log: testLog(), Now: clock.Now,
		TempDir: t.TempDir(), DiskLimitBytes: 100,
		DirUsage: func(string) (int64, error) { return usage, nil },
	})
	if err != nil {
		t.Fatalf("构造 Hub 失败: %v", err)
	}
	ctx := context.Background()

	// 低于阈值：无事件
	usage = 50
	h.CheckTempDir(ctx)
	if _, err := st.GetEvent(ctx, KeyTempDirUsage); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("低于阈值不应产生事件，得到 %v", err)
	}

	// 超过阈值：产生事件（越过节流窗口后才执行下一次检查）
	usage = 150
	clock.Advance(2 * diskCheckInterval)
	h.CheckTempDir(ctx)
	e := mustEvent(t, st, KeyTempDirUsage)
	if e.Status != store.EventOpen || e.Severity != SeverityWarn {
		t.Fatalf("超阈值应产生 warn 事件: %+v", e)
	}

	// 节流：同一时间窗内的重复检查不执行（不重复统计）
	usage = 0 // 即使占用已回落，窗口内也不应检查
	h.CheckTempDir(ctx)
	e = mustEvent(t, st, KeyTempDirUsage)
	if e.Status != store.EventOpen {
		t.Fatal("节流窗口内的检查不应执行（事件不应被恢复）")
	}

	// 越过节流窗口后占用回落：自动恢复
	clock.Advance(2 * diskCheckInterval)
	h.CheckTempDir(ctx)
	e = mustEvent(t, st, KeyTempDirUsage)
	if e.Status != store.EventResolved {
		t.Fatalf("占用回落应自动解决事件，得到 %s", e.Status)
	}

	// 再次超限：resolved 重开（count 累计、冷却清零）
	usage = 999
	clock.Advance(2 * diskCheckInterval)
	h.CheckTempDir(ctx)
	e = mustEvent(t, st, KeyTempDirUsage)
	if e.Status != store.EventOpen || e.Count != 2 {
		t.Fatalf("再次超限应重开事件，得到 status=%s count=%d", e.Status, e.Count)
	}
}

// ---- Resolve / Recover 审计 ----

func TestResolveAndRecoverAudit(t *testing.T) {
	st := openStore(t)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	ctx := context.Background()

	h.Raise(ctx, "k", SeverityWarn, "x")
	e := mustEvent(t, st, "k")
	if err := h.Resolve(ctx, e.ID); err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if err := h.Resolve(ctx, 404); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("解决不存在的事件应返回 ErrNotFound，得到 %v", err)
	}

	// 系统自动恢复：只对仍处于 open 的事件写恢复审计；不存在或已解决时静默。
	h.Raise(ctx, "recover-key", SeverityWarn, "x")
	h.Recover(ctx, "absent-key") // 不应 panic 或写审计
	h.Recover(ctx, "recover-key")

	entries, err := st.ListAudit(ctx, 10, 0)
	if err != nil {
		t.Fatalf("列审计失败: %v", err)
	}
	actions := map[string]string{} // action → actor
	for _, a := range entries {
		actions[a.Action] = a.Actor
	}
	if actions["event.resolve"] != "admin" {
		t.Fatalf("人工解决应留 admin 审计，得到 %v", actions)
	}
	if actions["event.recover"] != "system" {
		t.Fatalf("自动恢复应留 system 审计，得到 %v", actions)
	}
}

// ---- 并发安全（-race）----

func TestRecoverOnlyAuditsStateTransition(t *testing.T) {
	st := openStore(t)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	ctx := context.Background()

	h.Raise(ctx, KeySessionOffline, SeverityError, "离线")
	h.Recover(ctx, KeySessionOffline)
	h.Recover(ctx, KeySessionOffline) // 已恢复时不应重复写审计

	entries, err := st.ListAudit(ctx, 10, 0)
	if err != nil {
		t.Fatalf("列审计失败: %v", err)
	}
	count := 0
	for _, entry := range entries {
		if entry.Action == "event.recover" && entry.Target == KeySessionOffline {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("同一恢复状态迁移应只写一次审计，得到 %d 条", count)
	}
}

func TestRaiseConcurrentWithSetSender(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				h.Raise(context.Background(), fmt.Sprintf("k%d", i%3), SeverityWarn, "并发")
				h.SetSender(&fakeNotifier{})
				h.TaskResult(context.Background(), j%2 == 0)
				h.CloudResult(context.Background(), j%2 == 0)
				h.CheckTempDir(context.Background())
			}
		}(i)
	}
	wg.Wait()
}

// ---- 云盘事件源----

// CloudResult：独立连续失败计数（默认阈值 3），成功清零并自动恢复事件。
func TestCloudResultStreak(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock) // 未注入 CloudFailThreshold：默认阈值 3
	ctx := context.Background()

	h.CloudResult(ctx, false)
	h.CloudResult(ctx, false)
	if _, err := st.GetEvent(ctx, KeyCloudUploadFailed); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("未达阈值不应产生事件，得到 %v", err)
	}

	h.CloudResult(ctx, false) // 第 3 次：触发
	e := mustEvent(t, st, KeyCloudUploadFailed)
	if e.Count != 1 || e.Severity != SeverityError || e.Status != store.EventOpen {
		t.Fatalf("达到阈值应产生 error 事件: %+v", e)
	}

	// 成功清零并自动恢复事件
	h.CloudResult(ctx, true)
	e = mustEvent(t, st, KeyCloudUploadFailed)
	if e.Status != store.EventResolved {
		t.Fatalf("云盘任务恢复后事件应自动解决，得到 %s", e.Status)
	}
	h.CloudResult(ctx, false)
	h.CloudResult(ctx, false)
	e = mustEvent(t, st, KeyCloudUploadFailed)
	if e.Count != 1 {
		t.Fatalf("清零后未达阈值不应合并，得到 count=%d", e.Count)
	}

	// 清零后再连败 3 次：再次触发并合并
	h.CloudResult(ctx, false)
	e = mustEvent(t, st, KeyCloudUploadFailed)
	if e.Count != 2 {
		t.Fatalf("再次达到阈值应合并事件，得到 count=%d", e.Count)
	}
}

// CloudResult 与 TaskResult 计数独立：TG 任务成功不清零云盘计数，反之亦然。
func TestCloudResultIndependentFromTaskResult(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	h := newHub(t, st, newClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)))
	ctx := context.Background()

	h.CloudResult(ctx, false)
	h.CloudResult(ctx, false)
	h.TaskResult(ctx, true)   // TG 任务成功：不影响云盘计数
	h.CloudResult(ctx, false) // 云盘第 3 连败：仍应触发
	e := mustEvent(t, st, KeyCloudUploadFailed)
	if e.Count != 1 {
		t.Fatalf("TG 任务结果不应清零云盘计数，得到 count=%d", e.Count)
	}
	if _, err := st.GetEvent(ctx, KeyTaskFailures); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("云盘结果不应产生 TG 任务事件，得到 %v", err)
	}
}

// CloudConfigInvalid：产生 error 事件；冷却窗口内重复上报按 key 合并不重发。
func TestCloudConfigInvalidDedupAndCooldown(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	snd := &fakeNotifier{}
	h.SetSender(snd)
	snd.calls = 0

	ctx := context.Background()
	h.CloudConfigInvalid(ctx)
	e := mustEvent(t, st, KeyCloudConfigInvalid)
	if e.Severity != SeverityError || e.Count != 1 {
		t.Fatalf("配置无效应为 error 事件: %+v", e)
	}
	if snd.count() != 1 {
		t.Fatalf("首次应通知管理员，得到 %d 次", snd.count())
	}

	// 冷却窗口内再次发现（如保存后校验）：事件合并，不重复通知
	clock.Advance(5 * time.Minute)
	h.CloudConfigInvalid(ctx)
	e = mustEvent(t, st, KeyCloudConfigInvalid)
	if e.Count != 2 || e.Status != store.EventOpen {
		t.Fatalf("冷却窗口内应按 key 合并: %+v", e)
	}
	if snd.count() != 1 {
		t.Fatalf("冷却窗口内不应重发通知，得到 %d 次", snd.count())
	}
}

// CloudDisabled：产生事件；rclone 恢复后经 CloudDisabledRecovered 自动解决，
// 事件从未发生时恢复为静默 no-op。
func TestCloudDisabledRaiseAndRecover(t *testing.T) {
	st := openStore(t)
	withOwner(t, st, 1)
	clock := newClock(time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC))
	h := newHub(t, st, clock)
	snd := &fakeNotifier{}
	h.SetSender(snd)
	snd.calls = 0

	ctx := context.Background()
	h.CloudDisabledRecovered(ctx) // 事件从未发生：静默 no-op
	if _, err := st.GetEvent(ctx, KeyCloudDisabled); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("恢复不应凭空创建事件，得到 %v", err)
	}

	h.CloudDisabled(ctx)
	e := mustEvent(t, st, KeyCloudDisabled)
	if e.Severity != SeverityError || e.Status != store.EventOpen {
		t.Fatalf("rclone 缺失应产生 error 事件: %+v", e)
	}
	if snd.count() != 1 {
		t.Fatalf("应通知管理员，得到 %d 次", snd.count())
	}

	// 周期复查恢复可用：自动解决（冷却期内恢复语义与 CheckTempDir 一致）
	h.CloudDisabledRecovered(ctx)
	e = mustEvent(t, st, KeyCloudDisabled)
	if e.Status != store.EventResolved {
		t.Fatalf("恢复后事件应自动解决，得到 %s", e.Status)
	}
}
