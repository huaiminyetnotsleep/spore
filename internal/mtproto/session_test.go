package mtproto

// 登录会话状态机测试：状态迁移、重连信号门控、Web 模式标志消费与错误截断。

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionStateTransitions(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	s := newSession(func() time.Time { return now })

	// 初始离线
	if snap := s.Status(); snap.State != StateOffline || snap.UpdatedAt != now.UnixMilli() {
		t.Fatalf("初始应 offline：%+v", snap)
	}

	// 进入登录（Web 模式清旧错误）并推送扫码 URL
	s.setLoginPending(true)
	s.setQR("tg://login?token=abc")
	snap := s.Status()
	if snap.State != StateLoginPending || snap.QRURL != "tg://login?token=abc" {
		t.Fatalf("登录中应带扫码 URL：%+v", snap)
	}

	// 就绪：清空扫码与错误
	s.setReady()
	if snap = s.Status(); snap.State != StateReady || snap.QRURL != "" || snap.LastError != "" {
		t.Fatalf("就绪应清空扫码与错误：%+v", snap)
	}

	// 异常退出：回离线并记录原因
	s.setOffline(errors.New("AUTH_KEY_UNAUTHORIZED: 会话失效"))
	if snap = s.Status(); snap.State != StateOffline || !strings.Contains(snap.LastError, "AUTH_KEY_UNAUTHORIZED") {
		t.Fatalf("离线应带错误：%+v", snap)
	}
	if snap.QRURL != "" {
		t.Fatal("离线应清空扫码 URL")
	}
}

func TestSessionOfflineErrorTruncated(t *testing.T) {
	s := newSession(nil)
	s.setOffline(errors.New(strings.Repeat("x", 500)))
	if got := s.Status().LastError; len(got) > 200 {
		t.Fatalf("错误文本应截断到 200，得到 %d", len(got))
	}
}

func TestTriggerReloginGating(t *testing.T) {
	s := newSession(nil)

	// 非离线状态拒绝触发
	s.setReady()
	if err := s.TriggerRelogin(); !errors.Is(err, ErrNotOffline) {
		t.Fatalf("就绪状态应拒绝重连，得到 %v", err)
	}
	s.setLoginPending(false)
	if err := s.TriggerRelogin(); !errors.Is(err, ErrNotOffline) {
		t.Fatalf("登录中应拒绝重连，得到 %v", err)
	}

	// 离线可触发：置位 Web 模式并投递信号
	s.setOffline(errors.New("down"))
	if err := s.TriggerRelogin(); err != nil {
		t.Fatalf("离线应可触发: %v", err)
	}
	if !s.takeWebLogin() {
		t.Fatal("触发后应置位 Web 登录模式")
	}
	if s.takeWebLogin() {
		t.Fatal("Web 登录标志应一次性消费")
	}

	// 等待方收到信号；重复触发合并为一次
	if err := s.TriggerRelogin(); err != nil {
		t.Fatalf("再次触发不应报错: %v", err)
	}
	if err := s.TriggerRelogin(); err != nil {
		t.Fatalf("重复触发应合并: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-time.After(50 * time.Millisecond)
		cancel()
	}()
	if !s.waitRelogin(ctx) {
		t.Fatal("应先收到重连信号而非 ctx 结束")
	}
	// 信号已消费：再次等待应阻塞到 ctx 结束
	if s.waitRelogin(ctx) {
		t.Fatal("信号已消费，应等到 ctx 结束返回 false")
	}
}

func TestSessionConcurrentTriggerAndWait(t *testing.T) {
	// 并发触发 + 状态读取不竞态（-race 下验证）
	s := newSession(nil)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			_ = s.TriggerRelogin()
			_ = s.Status()
			s.setOffline(nil)
		}
	}()
	<-done
}

func TestSessionStateHook(t *testing.T) {
	var mu sync.Mutex
	var states []string
	record := func(state string) {
		mu.Lock()
		defer mu.Unlock()
		states = append(states, state)
	}
	want := []string{StateOffline, StateReady, StateReady}
	s := newSession(nil)

	// 观察回调在锁外执行：内部可安全回读 Status（事件中心 Raise 的调用形态）
	s.SetStateHook(func(state string) {
		record(state)
		_ = s.Status()
	})

	s.setOffline(errors.New("AUTH_KEY_UNAUTHORIZED"))
	s.setLoginPending(false)
	s.setReady()
	s.setReady() // 每轮重连进 ready 都回调，观察方需自行幂等

	mu.Lock()
	if len(states) != len(want) {
		got := append([]string(nil), states...)
		mu.Unlock()
		t.Fatalf("回调序列应 %v，得到 %v", want, got)
	}
	mu.Unlock()

	// 注销后不再回调
	s.SetStateHook(nil)
	s.setOffline(nil)
	s.setReady()
	mu.Lock()
	defer mu.Unlock()
	if len(states) != len(want) {
		t.Fatalf("注销后不应再回调，得到 %v", states)
	}
}
