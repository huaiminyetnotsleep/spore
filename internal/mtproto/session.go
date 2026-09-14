package mtproto

import (
	"context"
	"errors"
	"sync"
	"time"
)

// MTProto 会话状态（登录会话状态机与 Web 管理端展示使用）。
const (
	StateOffline      = "offline"       // 无有效会话（等待 Web 重连或进程重启）
	StateLoginPending = "login_pending" // 登录进行中（终端或 Web 扫码呈现）
	StateReady        = "ready"         // 会话有效，核心链路（Bot/worker）已启动
)

// ErrNotOffline 表示会话不在离线状态，无法（也无需）触发重连。
var ErrNotOffline = errors.New("mtproto: 会话当前不是离线状态，无法触发重连")

// StatusSnapshot 是登录会话的当前状态快照（Web 轮询展示）。
// QRURL 是扫码登录令牌（tg://login?token=...），属于敏感值：
// 不入日志、不入审计，仅经登录态接口交付浏览器渲染。
type StatusSnapshot struct {
	State     string
	QRURL     string
	LastError string
	UpdatedAt int64 // Unix 毫秒
}

// Session 跟踪 MTProto 会话状态并承接 Web 触发的重连：
// Run 循环异常退出后标记离线并阻塞在 relogin 信号上，管理员经 Web 触发
// TriggerRelogin 唤醒下一轮 client 生命周期（登录模式优先走 Web 扫码呈现）。
// 并发安全（mutex 保护）；由 Client 持有，Web 层经 Client.Session() 访问。
type Session struct {
	mu        sync.Mutex
	state     string
	qrURL     string
	lastErr   string
	updatedAt int64
	hook      func(state string) // 状态观察回调（可空；锁外调用，见 SetStateHook）
	webLogin  bool               // 下一轮登录用 Web 扫码呈现（TriggerRelogin 置位，每轮消费后复位）
	relogin   chan struct{}      // 重连信号（容量 1，非阻塞投递，重复触发合并）
	now       func() time.Time
}

// newSession 创建初始状态为 offline 的会话对象。
func newSession(now func() time.Time) *Session {
	if now == nil {
		now = time.Now
	}
	return &Session{state: StateOffline, relogin: make(chan struct{}, 1),
		updatedAt: now().UnixMilli(), now: now}
}

// Status 返回状态快照（含扫码 URL；调用方负责鉴权与不落日志）。
func (s *Session) Status() StatusSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return StatusSnapshot{State: s.state, QRURL: s.qrURL, LastError: s.lastErr, UpdatedAt: s.updatedAt}
}

// TriggerRelogin 请求重连：仅离线状态接受，信号非阻塞投递（已在等待即合并）。
// 置位 webLogin 后，下一轮登录把扫码 URL 推送到本会话（Web 呈现）而非终端。
func (s *Session) TriggerRelogin() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateOffline {
		return ErrNotOffline
	}
	s.webLogin = true
	select {
	case s.relogin <- struct{}{}:
	default: // 已有挂起信号，无需重复
	}
	return nil
}

// waitRelogin 阻塞等待重连信号或进程退出；返回 false 表示进程退出。
func (s *Session) waitRelogin(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-s.relogin:
		return true
	}
}

// takeWebLogin 读取并复位 webLogin 标志（每轮登录开始时消费一次，
// 之后失败回落终端路径——进程重启即恢复终端扫码，  降级）。
func (s *Session) takeWebLogin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.webLogin
	s.webLogin = false
	return v
}

// setLoginPending 标记进入登录流程；viaWeb 时清除旧错误，等待首个扫码 URL。
func (s *Session) setLoginPending(viaWeb bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = StateLoginPending
	s.qrURL = ""
	if viaWeb {
		s.lastErr = ""
	}
	s.updatedAt = s.now().UnixMilli()
}

// setQR 推送新的扫码登录 URL（约 30 秒刷新一次）。
func (s *Session) setQR(url string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.qrURL = url
	s.updatedAt = s.now().UnixMilli()
}

// SetStateHook 注册状态观察回调：每次转入 offline / ready 时以新状态回调一次
// （每轮重连进入 ready 也会回调，观察方需自行幂等）。回调在会话锁外执行，
// 可安全回读 Status 或写库——mtproto 不感知通知实现，由装配层把状态映射为
// 事件中心条目（依赖倒置）。传 nil 注销。
func (s *Session) SetStateHook(fn func(state string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hook = fn
}

// setReady 标记会话有效（核心链路随 ready 回调启动），清空扫码与错误。
func (s *Session) setReady() {
	s.mu.Lock()
	s.state = StateReady
	s.qrURL = ""
	s.lastErr = ""
	s.updatedAt = s.now().UnixMilli()
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		hook(StateReady) // 锁外回调：观察方可能回读 Status（本方法）
	}
}

// setOffline 标记离线并记录原因；err 为 nil 时仅更新时间戳。
// 错误文本做长度截断，避免把底层超长错误串长期驻留内存并展示到页面。
func (s *Session) setOffline(err error) {
	s.mu.Lock()
	s.state = StateOffline
	s.qrURL = ""
	if err != nil {
		msg := err.Error()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		s.lastErr = msg
	}
	s.updatedAt = s.now().UnixMilli()
	hook := s.hook
	s.mu.Unlock()
	if hook != nil {
		hook(StateOffline) // 锁外回调：观察方可能回读 Status（本方法）
	}
}
