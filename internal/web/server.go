package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/joinmgr"
	"github.com/huaiminyetnotsleep/spore/internal/monitor"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/notify"
	"github.com/huaiminyetnotsleep/spore/internal/progress"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/transfercfg"
)

// Version 是管理端展示的服务版本（总览信息；后续里程碑接入构建信息）。
const Version = "0.2.0-web-pages"

// QueueStats 是总览页对内存队列的最小依赖（*queue.Queue 天然满足）。
type QueueStats interface {
	Len() int
	Cap() int
}

// MTProtoRelogin 是扫码登录页对 MTProto 登录会话的最小依赖
// （*mtproto.Session 实现；本包只接触状态快照，不依赖任何 gotd 类型）。
type MTProtoRelogin interface {
	Status() mtproto.StatusSnapshot
	TriggerRelogin() error
}

// BotMTProtoStatus 是 Bot MTProto 会话状态的最小 Web 依赖。
type BotMTProtoStatus interface {
	Status() mtproto.BotStatusSnapshot
}

// UserProfileLookup 是 Web 手动刷新用户资料的最小生命周期感知接口。
// 实现方必须自行保证 Telegram 客户端只在有效生命周期内调用。
type UserProfileLookup interface {
	LookupUserProfile(context.Context, int64) (store.UserProfile, error)
}

// ChannelBinder 是频道绑定管理页对绑定服务的最小依赖
// （*binding.Service 实现；业务校验与审计都在服务内完成）。
type ChannelBinder interface {
	Bind(ctx context.Context, in binding.BindInput) (store.ChannelBinding, error)
	Unbind(ctx context.Context, userID int64, target string, anyOwner bool) (store.ChannelBinding, error)
	ListAll(ctx context.Context) ([]store.ChannelBindingWithUser, error)
	// VerifyChannel 解析并校验频道目标（缓存频道配置用）：返回数字频道 ID
	// 与标题；bot 必须在该频道可发帖。
	VerifyChannel(ctx context.Context, target string) (int64, string, error)
}

// TransferConfig is the Web-facing subset of transfercfg.Runtime. It keeps
// settings HTTP handlers independent from transfer consumers while allowing
// the main program to inject the process-wide runtime.
type TransferConfig interface {
	View() transfercfg.View
	Apply(context.Context, map[transfercfg.Key]int, []transfercfg.Key) (transfercfg.Change, error)
}

// ChannelJoinManager 是频道加入管理页对 joinmgr 服务的最小依赖
// （*joinmgr.Service 实现；审批/退出/熔断的业务规则与留痕在服务内完成）。
type ChannelJoinManager interface {
	ListRequests(ctx context.Context, q joinmgr.RequestsQuery) ([]joinmgr.JoinRequestView, int, error)
	Approve(ctx context.Context, actor string, requestID int64) (joinmgr.JoinRequestView, error)
	Reject(ctx context.Context, actor string, requestID int64) (joinmgr.JoinRequestView, error)
	DeleteRequests(ctx context.Context, ids []int64) []joinmgr.RequestDeleteOutcome
	ReconcilePendingJoins(ctx context.Context) error
	ListJoined(ctx context.Context) ([]joinmgr.JoinedChannelView, error)
	Leave(ctx context.Context, channelIDs []int64) []joinmgr.LeaveOutcome
	Enforce(ctx context.Context) error
}

// Options 聚合管理端服务依赖。
type Options struct {
	Store       *store.Store // 必填：业务数据库（settings/web_sessions/audit_log）
	Cfg         config.Config
	Log         *slog.Logger
	Now         func() time.Time  // 可注入时钟（测试用）；缺省 time.Now
	OAuth       OAuthEndpoints    // 可选：整体覆盖 GitHub OAuth 端点（测试注入假服务）
	Client      *http.Client      // 可选：OAuth 出站 HTTP 客户端；缺省带超时的独立客户端
	Access      *access.Service   // 可选：访问控制服务（管理页面的操作入口）；缺失时相关路由报不可用
	Queue       QueueStats        // 可选：内存队列指标（总览页）；缺失时不展示
	MTProto     MTProtoRelogin    // 可选：MTProto 登录会话（状态/扫码/重连）；缺失时显示未接入
	BotMTProto  BotMTProtoStatus  // 可选：Bot MTProto 会话状态（状态/DC）；缺失时显示未接入
	Profile     UserProfileLookup // 可选：Telegram 用户资料刷新上下文
	RestartFunc func() error      // 可选：受控优雅重启；生产实现只发送 SIGTERM
	Hub         *notify.Hub       // 可选：事件中心（resolve 经它统一执行并留审计）；缺失时直写 store
	// Progress 是处理中请求的实时传输进度注册表，与 worker 共享同一实例
	// （internal/progress）；nil 时请求记录不携带进度字段。
	Progress *progress.Registry
	// Monitor 提供系统资源与传输监控查询；nil 时监控端点返回受控不可用。
	Monitor *monitor.Service
	// Bindings 是频道绑定服务（internal/binding.Service）；缺失时相关路由报不可用。
	Bindings ChannelBinder
	// ChannelJoin 是频道加入管理服务（internal/joinmgr.Service）；
	// 缺失时相关路由报不可用。
	ChannelJoin ChannelJoinManager
	// Transfer 是进程级传输并发配置；缺失时仅隐藏四项运行时字段。
	Transfer TransferConfig
	// CloudCfg 是云盘下载配置管理器（internal/cloudarchive.Manager）；
	// 缺失时云盘配置与补存端点返回受控不可用。
	CloudCfg *cloudarchive.Manager
	// CloudSink 是云盘连通性测试通道（*cloudarchive.RcloneSink 天然满足）；
	// 缺失时连通性测试端点返回受控不可用。
	CloudSink cloudarchive.Sink
	// CloudBackupKey 是云盘备份候选的服务端根密钥。生产环境注入
	// cfg.OAuthEncryptionKey，cloudarchive 再经 HKDF 域分离派生候选专用 key。
	CloudBackupKey []byte
	DBPath         string // 可选：数据库文件路径（备份页大小展示）；缺省按 DataDir 推导
}

// Server 是管理端 HTTP 服务：零值不可用，经 New 构造。
// Handler 可直接交给 httptest 或任意 http.Server；Run 提供带优雅退出的监听。
type Server struct {
	st               *store.Store
	cfg              config.Config
	log              *slog.Logger
	now              func() time.Time
	limiter          *loginLimiter
	states           *oauthStateStore
	oauth            OAuthEndpoints
	client           *http.Client
	access           *access.Service
	queue            QueueStats
	mtp              MTProtoRelogin
	botMTP           BotMTProtoStatus
	profile          UserProfileLookup
	restartFunc      func() error
	restartMu        sync.Mutex
	restartScheduled bool
	nonceFunc        func(int) (string, error)
	hub              *notify.Hub
	progress         *progress.Registry
	monitor          *monitor.Service
	bindings         ChannelBinder
	channelJoin      ChannelJoinManager
	transfer         TransferConfig
	cloudCfg         *cloudarchive.Manager
	cloudSink        cloudarchive.Sink
	cloudPending     *cloudarchive.PendingStore
	dbPath           string
	started          time.Time
}

// New 创建服务；Store 为必填。调用方须先经 EnsureAccessKey 完成密钥初始化
// （未初始化时密钥登录通道不可用，但服务与探针仍可运行）。
func New(opt Options) (*Server, error) {
	if opt.Store == nil {
		return nil, errors.New("web: Store 为必填项")
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	endpoints := opt.OAuth
	if endpoints.Authorize == "" || endpoints.Token == "" || endpoints.User == "" {
		endpoints = defaultOAuthEndpoints
	}
	client := opt.Client
	if client == nil {
		client = &http.Client{Timeout: oauthHTTPTimeout}
	}
	dbPath := opt.DBPath
	if dbPath == "" {
		dbPath = filepath.Join(opt.Cfg.DataDir, branding.DatabaseFile)
	}
	var cloudPending *cloudarchive.PendingStore
	if opt.CloudCfg != nil && len(opt.CloudBackupKey) == 32 {
		cloudPending, _ = cloudarchive.NewPendingStore(filepath.Dir(opt.CloudCfg.Path()), opt.CloudBackupKey)
	}
	return &Server{
		st:           opt.Store,
		cfg:          opt.Cfg,
		log:          opt.Log,
		now:          now,
		limiter:      newLoginLimiter(),
		states:       newOAuthStateStore(),
		oauth:        endpoints,
		client:       client,
		access:       opt.Access,
		queue:        opt.Queue,
		mtp:          opt.MTProto,
		botMTP:       opt.BotMTProto,
		profile:      opt.Profile,
		restartFunc:  opt.RestartFunc,
		nonceFunc:    randomToken,
		hub:          opt.Hub,
		progress:     opt.Progress,
		monitor:      opt.Monitor,
		bindings:     opt.Bindings,
		channelJoin:  opt.ChannelJoin,
		transfer:     opt.Transfer,
		cloudCfg:     opt.CloudCfg,
		cloudSink:    opt.CloudSink,
		cloudPending: cloudPending,
		dbPath:       dbPath,
		started:      now(),
	}, nil
}

// Handler 返回完整的 HTTP 处理链：panic 恢复 → 安全响应头 → 路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return s.recoverPanics(s.securityHeaders(mux))
}

// Run 在 cfg.WebAddr 上监听直到 ctx 取消（随后 5 秒内优雅关闭）。
// 监听失败立即返回错误（如端口被占用）；调用方应以独立 goroutine 运行并只
// 记录日志，不让 Web 故障影响 Bot 主链路（本服务与 MTProto 生命周期完全
// 解耦，）。
func (s *Server) Run(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.cfg.WebAddr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second, // 限制慢速头部攻击
	}
	// 同步监听：绑定失败（端口占用/权限）立刻返回，避免误报"已启动"
	ln, err := net.Listen("tcp", s.cfg.WebAddr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", s.cfg.WebAddr, err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	s.log.Info("Web 管理端已启动", "addr", s.cfg.WebAddr)
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("关闭 Web 管理端: %w", err)
		}
		return nil
	}
}

// ---- 中间件 ----

// sessionHandler 是带会话上下文的处理器签名。
type sessionHandler func(http.ResponseWriter, *http.Request, session)

// requireAuth 要求有效会话：GET 重定向到 SPA 登录壳（当前为 /admin/login，
// 旧 SSR /login 已删除），非 GET 返回 401（避免变更类请求被重定向后丢失
// 语义）。通过后执行滑动续期。
func (s *Server) requireAuth(next sessionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.authenticate(r)
		if !ok {
			if r.Method == http.MethodGet {
				http.Redirect(w, r, "/admin/login", http.StatusFound)
			} else {
				http.Error(w, "未登录", http.StatusUnauthorized)
			}
			return
		}
		s.renewSession(w, sess)
		next(w, r, sess)
	})
}

// mountAPIWrite 注册 API 写端点：POST 走认证 + CSRF + JSON 业务链；
// GET 回退 405 JSON（避免 fetch 误收 net/http 纯文本 405）。
// pattern 须为只注册过 POST 的路径；GET 另有语义的路径（如 /api/v1/users）
// 不得经本方法注册。
func (s *Server) mountAPIWrite(mux *http.ServeMux, pattern string, next sessionHandler) {
	mux.Handle("POST "+pattern, s.apiAuth(s.apiCSRF(next)))
	mux.Handle("GET "+pattern, http.HandlerFunc(s.writeAPIMethodNotAllowed))
}

// securityHeaders 为所有响应设置安全头。SPA 壳的内联 <style> 由
// serveSPAShell 生成的 nonce 放行（会覆盖此处的基础 CSP），其余资源仅允许同源。
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"form-action 'self'; frame-ancestors 'none'; base-uri 'self'")
		next.ServeHTTP(w, r)
	})
}

// recoverPanics 兜底恢复处理器 panic：输出 500 并记日志。
// net/http 本身会按连接隔离 panic，这里进一步转为受控响应，
// 保证 Web 侧任何异常都不外溢到进程或其他 goroutine（Bot 主链路不受影响）。
func (s *Server) recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("Web 处理器 panic", "path", r.URL.Path, "panic", fmt.Sprint(rec))
				// 响应头可能已写出；此处尽力而为，写失败可忽略
				http.Error(w, "服务器内部错误", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---- 渲染 ----

// 当前 Go 侧不再渲染任何 HTML：SSR 模板、render 与 nonce 注入已随 SSR 登录页
// 一并删除。唯一的 HTML 输出是 SPA 应用壳（frontend.go 的 serveSPAShell，
// 含每请求 CSP nonce）；旧 SSR 页面入口不再注册兼容重定向。

// ---- 探针 ----

// handleHealthz 进程存活探针：无鉴权、无状态、不含敏感信息。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeProbe(w, http.StatusOK, "ok")
}

// handleReadyz 业务就绪探针：配置已加载 + 数据库可读写（用一次 settings
// 读取验证连通性）；不可用时 503，仍不含敏感信息。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if _, _, err := s.st.GetSetting(ctx, "readyz_probe"); err != nil {
		s.log.Warn("就绪探针失败", "error", err.Error())
		writeProbe(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	writeProbe(w, http.StatusOK, "ready")
}

// writeProbe 输出探针 JSON。
func writeProbe(w http.ResponseWriter, status int, state string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": state})
}
