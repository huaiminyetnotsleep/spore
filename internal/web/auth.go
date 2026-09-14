package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// Cookie 名与生命周期常量。
const (
	sessionCookieName   = branding.SessionCookie   // 管理员会话（值为随机 ID，库内存其哈希）
	loginCSRFCookieName = branding.LoginCSRFCookie // 登录前双提交 CSRF Cookie
	sessionTTL          = 12 * time.Hour           // 会话滑动过期窗口
)

// 登录失败限流参数：窗口内失败达上限后锁定，退避时长逐次翻倍。
const (
	loginFailWindow   = time.Minute // 计数窗口
	loginFailMax      = 5           // 窗口内允许的失败次数
	loginBackoffStart = time.Minute // 首次锁定时长
	loginBackoffMax   = time.Hour   // 退避上限
	// loginFailPruneAfter：条目闲置该时长后可在清理时移除。遗忘超过 1 小时
	// 未再失败的来源（退避档位随之重置）——全局维度的锁定兜底不受影响。
	loginFailPruneAfter = time.Hour
)

// ---- 登录失败限流器（内存实现；key 为 "ip:<addr>" 与 "global"）----

// failCounter 记录一个 key 的失败计数与锁定状态。
type failCounter struct {
	fails       int
	windowStart time.Time
	lockedUntil time.Time
	backoff     time.Duration
}

// loginLimiter 以 IP 与全局两个维度统计登录失败：窗口内失败达上限即锁定，
// 锁定时长按指数退避翻倍。并发安全（mutex 保护），无后台协程。
type loginLimiter struct {
	mu      sync.Mutex
	entries map[string]*failCounter
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{entries: make(map[string]*failCounter)}
}

// Allow 判断 key 当前是否放行；被锁定时返回剩余等待时长。
func (l *loginLimiter) Allow(key string, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	c, exists := l.entries[key]
	if !exists {
		return true, 0
	}
	if c.lockedUntil.After(now) {
		return false, c.lockedUntil.Sub(now)
	}
	return true, 0
}

// RecordFailure 记录一次失败：窗口内累计达上限则锁定并进入下一档退避。
// 顺手清理长时间无活动的条目，避免持续运行下 map 无界增长。
func (l *loginLimiter) RecordFailure(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pruneLocked(now)
	c := l.entries[key]
	if c == nil {
		c = &failCounter{windowStart: now}
		l.entries[key] = c
	}
	if now.Sub(c.windowStart) >= loginFailWindow {
		c.fails = 0
		c.windowStart = now
	}
	c.fails++
	if c.fails < loginFailMax {
		return
	}
	// 触发锁定：退避翻倍（首档 1 分钟），计数清零进入新窗口
	if c.backoff == 0 {
		c.backoff = loginBackoffStart
	} else {
		c.backoff *= 2
		if c.backoff > loginBackoffMax {
			c.backoff = loginBackoffMax
		}
	}
	c.lockedUntil = now.Add(c.backoff)
	c.fails = 0
	c.windowStart = now
}

// Reset 清除 key 的失败计数（登录成功后对来源 IP 调用）；全局计数不清，
// 仅随窗口与退避自然衰减，缓解分布式穷举。
func (l *loginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}

// pruneLocked 移除「未锁定且超过 loginFailPruneAfter 无新失败」的条目
// （须持锁调用）。这类条目下次失败时计数本就会归零重算，删除等价；
// 代价是同来源的退避档位遗忘，1 小时门槛下可接受。
func (l *loginLimiter) pruneLocked(now time.Time) {
	for k, c := range l.entries {
		if c.lockedUntil.After(now) {
			continue // 锁定中的条目保留（其退避仍需生效）
		}
		if now.Sub(c.windowStart) >= loginFailPruneAfter {
			delete(l.entries, k)
		}
	}
}

// ---- 会话 ----

// session 是请求范围内解析出的管理员会话（Cookie 随机 ID 只在此瞬间存在明文）。
type session struct {
	id      string // 明文会话 ID（仅用于回写 Cookie）
	idHash  string
	csrf    string
	expires int64 // Unix 毫秒
}

// authenticate 从请求 Cookie 解析会话：哈希查库并做过期判断。
// 未携带 Cookie、会话不存在或已过期均返回 ok=false（不区分，避免会话探测）。
func (s *Server) authenticate(r *http.Request) (session, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return session{}, false
	}
	idHash := hashValue(c.Value)
	ws, err := s.st.GetWebSession(r.Context(), idHash)
	if err != nil {
		return session{}, false
	}
	if ws.ExpiresAt <= s.now().UnixMilli() {
		// 已过期：顺手清理，行为与不存在一致
		_ = s.st.DeleteWebSession(context.WithoutCancel(r.Context()), idHash)
		return session{}, false
	}
	return session{id: c.Value, idHash: idHash, csrf: ws.CSRFToken, expires: ws.ExpiresAt}, true
}

// renewSession 滑动续期：把过期时间推到 now+TTL 并刷新 Cookie Max-Age，
// 保证「闲置 12 小时过期」语义（距上次活动满 TTL 才失效）。
func (s *Server) renewSession(w http.ResponseWriter, sess session) {
	expires := s.now().Add(sessionTTL)
	if err := s.st.TouchWebSession(context.Background(), sess.idHash, expires.UnixMilli()); err != nil {
		// 续期失败不影响本次请求（旧过期时间仍然有效），仅记日志
		s.log.Warn("会话续期失败", "error", err.Error())
	}
	setSessionCookie(w, sess.id, int(sessionTTL.Seconds()))
}

// issueSession 创建会话（库内存哈希）并设置 Cookie，返回错误表示存储故障。
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, ip, userAgent string) error {
	id, err := randomToken(32)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, err)
	}
	csrf, err := randomToken(32)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, err)
	}
	expires := s.now().Add(sessionTTL)
	if err := s.st.CreateWebSession(r.Context(), store.WebSession{
		IDHash:    hashValue(id),
		ExpiresAt: expires.UnixMilli(),
		CSRFToken: csrf,
		IP:        ip,
		UserAgent: userAgent,
	}); err != nil {
		return err
	}
	setSessionCookie(w, id, int(sessionTTL.Seconds()))
	return nil
}

// setSessionCookie 写会话 Cookie；Secure 属性要求公网 HTTPS（本地回环调试时
// 现代浏览器同样接受 localhost 上的 Secure Cookie）。
func setSessionCookie(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie 删除客户端会话 Cookie（登出）。
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

// verifyLoginCSRF 校验登录请求的双提交 CSRF：随机 Cookie 值与 X-CSRF-Token
// 请求头常数时间比对（当前传输通道从 SSR 隐藏域改为 JSON 请求头，Cookie 与
// 比对语义不变）。攻击者页面无法跨域读取该 Cookie，也就无法构造同时满足
// 两者的提交（SameSite=Lax 已是第一道防线，此为纵深防御）。
func verifyLoginCSRF(r *http.Request) bool {
	c, err := r.Cookie(loginCSRFCookieName)
	if err != nil || c.Value == "" {
		return false
	}
	tok := r.Header.Get(apiCSRFHeader)
	return tok != "" &&
		subtle.ConstantTimeCompare([]byte(tok), []byte(c.Value)) == 1
}

// ensureLoginCSRF 确保客户端持有登录 CSRF Cookie：缺失时生成并种入。
// 返回应注入表单的值（与 Cookie 相同）。
func (s *Server) ensureLoginCSRF(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(loginCSRFCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	tok, err := randomToken(32)
	if err != nil {
		// 随机源故障极罕见：退化为固定无效值，后续校验必然失败
		s.log.Error("生成登录 CSRF token 失败", "error", err.Error())
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     loginCSRFCookieName,
		Value:    tok,
		Path:     "/",
		MaxAge:   int((2 * time.Hour).Seconds()), // 覆盖一次登录会话的时长即可
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
	return tok
}

// clientIP 推导审计来源 IP：仅在信任反代时取 X-Forwarded-For 末段，
// 否则直接使用连接对端地址（转发头只用于审计，不做鉴权依据）。
//
// 取末段而非首段：可信代理（如 Caddy）总是把真实对端追加在 XFF 尾部，
// 客户端可以 prepend 伪造前缀但不能篡改尾部；首段完全由客户端控制，
// 会被用来轮换假 IP 绕过登录限流的 IP 维度（全局维度仍兜底）。
// 本项目部署形态是单一可信反代，末段即真实客户端地址。
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.WebTrustedProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			last := xff
			if i := strings.LastIndexByte(xff, ','); i >= 0 {
				last = xff[i+1:]
			}
			if ip := strings.TrimSpace(last); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---- 登录处理器 ----

// 当前 SSR 登录页与其表单端点已删除：访问密钥登录统一走
// /api/v1/login/csrf + /api/v1/login（api_login.go）；本文件保留会话签发、
// 登录 CSRF 与限流等两个登录通道（密钥 / GitHub OAuth）共用的底层实现。

// recordLoginFailure 记录一次登录失败：限流计数（IP + 全局）与审计。
func (s *Server) recordLoginFailure(r *http.Request, ip, method string) {
	now := s.now()
	s.limiter.RecordFailure(limiterKeyIP(ip), now)
	s.limiter.RecordFailure(limiterKeyGlobal, now)
	s.auditAs(r.Context(), "anonymous", "auth.login_failed", "web",
		map[string]any{"method": method, "ip": ip})
	s.log.Warn("管理端登录失败", "ip", ip, "method", method)
}

// limiterKeyIP 构造按来源 IP 的限流计数键；limiterKeyGlobal 是全局维度的键。
func limiterKeyIP(ip string) string { return "ip:" + ip }

const limiterKeyGlobal = "global"

// logoutSession 登出核心（/api/v1/session/logout）：仅删除当前会话
// （其他设备不受影响），清除会话 Cookie 并写审计。
func (s *Server) logoutSession(w http.ResponseWriter, r *http.Request, sess session) {
	if err := s.st.DeleteWebSession(r.Context(), sess.idHash); err != nil {
		s.log.Warn("删除会话失败", "error", err.Error())
	}
	clearSessionCookie(w)
	s.audit(r.Context(), "auth.logout", "web", map[string]any{"ip": s.clientIP(r)})
	s.log.Info("管理端登出")
}

// audit 写一条以管理员为来源的审计记录（登录成功、绑定变更等已认证动作）。
func (s *Server) audit(ctx context.Context, action, target string, detail map[string]any) {
	s.auditAs(ctx, "admin", action, target, detail)
}

// auditAs 写一条审计记录；detail 序列化为 after_json，失败仅记日志
// （审计不可用不应阻断业务响应）。敏感值（密钥、token、会话哈希）不得进入 detail。
func (s *Server) auditAs(ctx context.Context, actor, action, target string, detail map[string]any) {
	after, err := json.Marshal(detail)
	if err != nil {
		s.log.Warn("审计详情序列化失败", "action", action, "error", err.Error())
		after = nil
	}
	if err := s.st.AppendAudit(ctx, store.AuditEntry{
		Actor:     actor,
		Action:    action,
		Target:    target,
		AfterJSON: string(after),
	}); err != nil {
		s.log.Warn("写审计记录失败", "action", action, "error", err.Error())
	}
}
