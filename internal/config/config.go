// Package config 负责环境变量加载与校验。
// 校验语义移植自旧 src/config.ts，详见 docs/reference/architecture.md 第 6 节。
package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var botTokenPattern = regexp.MustCompile(`^\d+:[A-Za-z0-9_-]{20,}$`)

const (
	defaultWorkerCount = 1
	defaultDataDir     = "data"
	// defaultMaxFileSize 默认发送大小上限：2000MB（MTProto 上传硬上限）。
	// 超过 Bot API 50MB 上限的文件自动经 Bot 号 MTProto 会话直传（见
	// delivery/router.go），未配置本地 Bot API 服务器也可发大文件。
	defaultMaxFileSize    = int64(2000) << 20
	defaultStreamLimit    = int64(20) << 20  // 20MB，流式/内存缓冲分界
	defaultPhotoLimit     = int64(10) << 20  // 10MB，官方服务器 sendPhoto 上限
	defaultTempDirMaxSize = int64(5) << 30   // 5GB，临时媒体目录总量上限
	defaultWebAddr        = "127.0.0.1:8080" // Web 管理端监听地址：默认仅绑回环，公网经反代暴露

	// 传输并发默认值：下载/上传并行分片线程数与内存重排序缓冲上限。
	// 线程数调 1 即回退单线程（现状行为）；内存上限是单个文件的常驻
	// RAM 红线（WORKER_COUNT>1 时按并发任务数放大）。
	defaultTransferThreads = 4
	maxTransferThreads     = 16
	defaultInMemoryLimit   = int64(512) << 20 // 512MB，内存管道缓冲上限

	// 内存预算（MEMORY_BUDGET）：进程级内存管道总额度。预算不足的媒体
	// 自动降级临时文件路径（边下边传），使常驻 RAM 被额度封顶而不随
	// 并发任务数线性放大；管理端 memory_budget 键可热调（即时生效）。
	defaultMemoryBudget = int64(1) << 30 // 1GB
	MinMemoryBudget     = int64(64) << 20
	MaxMemoryBudget     = int64(8) << 30

	// 媒体配置的 Web 可调范围；运行时配置也复用此校验。
	MinMediaFileSize = int64(1) << 20
	// MaxMediaFileSize 与 MTProto 上传硬上限对齐：4000 part × 512KB = 2000MB
	//（gotd uploader 大文件分片规则），再大无通道可承载。
	MaxMediaFileSize    = int64(2000) << 20
	OfficialMaxFileSize = int64(50) << 20 // 官方 Bot API 服务器的上传硬上限
	MinTempDirMaxSize   = int64(1) << 20  // 1MB，临时目录上限最小
	MaxTempDirMaxSize   = int64(1) << 40  // 1TB，临时目录上限最大

	// 事件与通知默认值。
	defaultNotifyCooldownMin      = 30  // 同一事件两次通知的最小间隔（分钟）
	defaultEventBotFailThreshold  = 3   // Bot API 连续发送失败告警阈值
	defaultEventTaskFailThreshold = 5   // 连续任务失败告警阈值
	defaultEventDiskLimitGB       = 1.0 // 临时目录占用告警阈值（GB）
	// time.Duration 的最大值约为 290 年，防止分钟数转时长时溢出。
	maxNotifyCooldownMin = int64(1<<63-1) / int64(60*1e9)
	minDiskLimitGB       = 1.0 / float64(1<<30) // 至少能表示为 1 字节
)

// 登录模式常量。
const (
	LoginModeAuto  = "auto"  // 优先扫码，TG_PHONE 缺失时也不强制手机号流程
	LoginModeQR    = "qr"    // 强制扫码登录
	LoginModePhone = "phone" // 强制验证码登录（TG_PHONE 必填）
)

// Config 保存全部运行时配置。
type Config struct {
	BotToken        string
	BotAPIURL       string // 自定义 Bot API 服务器（本地模式）；空 = 官方服务器
	TGAPIID         int
	TGAPIHash       string
	TGPhone         string             // 仅 phone 模式必填；auto 下作为回退凭据
	LoginMode       string             // auto / qr / phone
	AllowedUserIDs  map[int64]struct{} // 旧白名单：仅首次启动导入数据库（users 表为空时），此后以数据库为准
	WorkerCount     int
	DataDir         string // session.json / peers.json 存放目录
	TempDir         string // 临时媒体目录
	MaxFileSize     int64  // 发送大小上限（字节）
	StreamLimit     int64  // 流式下载上限（字节），超过走内存缓冲或临时文件
	InMemoryLimit   int64  // 内存重排序缓冲上限（字节）；超过走临时文件多线程落盘
	MemoryBudget    int64  // 进程级内存管道总额度（字节）；预算不足自动降级临时文件路径
	DownloadThreads int    // 并行下载分片线程数（流式路径固定单流，不受影响）
	UploadThreads   int    // MTProto 大文件上传并发分片线程数
	// MTProto 连接池上限（下载在用户会话、上传在 Bot 会话）：分片线程默认
	// 全部复用单条 TCP 连接，带宽富余时单连接是吞吐瓶颈；池让并发分片各走
	// 独立连接（并行 TCP 窗口 + 并行加解密）。与线程数解耦——线程是
	// goroutine、连接是 TCP 资源；设 1 即回退单连接现状。
	DownloadConnections int   // 用户会话下载连接池上限
	UploadConnections   int   // Bot 会话上传连接池上限
	TempDirMaxSize      int64 // 临时媒体目录总量上限（字节），超过拒绝落盘下载
	PhotoLimit          int64 // 图片经 sendPhoto 的上限：官方 10MB；本地服务器 = MaxFileSize
	LogLevel            slog.Level

	// DumpChannelID 是缓存频道（转存频道复用）的频道 ID（-100 前缀）：
	// 任务成功投递后写无脚注干净副本，同链接后续提交直接整条复制。
	// 0 = 功能关闭（无复用）。一次性配置：建私有频道 → bot 设为管理员 → 填 ID。
	DumpChannelID int64

	// ---- Web 管理端（internal/web）----
	WebAddr            string // 监听地址，默认 127.0.0.1:8080；公网经宿主机反向代理暴露
	GitHubClientID     string // GitHub OAuth App client id；与 secret 成对配置，留空则该通道隐藏
	GitHubClientSecret string // GitHub OAuth App client secret；仅存于环境变量，不入库不入日志
	OAuthEncryptionKey []byte // Web OAuth Secret 的独立 AES-256-GCM 主密钥，不落库
	WebTrustedProxy    bool   // 信任反代转发头（X-Forwarded-*）：仅影响审计来源 IP 与回调地址推导

	// ---- 事件与通知（internal/notify）----
	NotifyCooldownMin      int     // 同一事件两次 Telegram 管理员通知的最小间隔（分钟）
	EventBotFailThreshold  int     // Bot API 连续发送失败产生事件的阈值
	EventTaskFailThreshold int     // 任务连续失败产生事件的阈值
	EventDiskLimitGB       float64 // 临时目录占用告警阈值（GB）
}

// Load 从 getenv 读取配置（注入 os.Getenv 以便测试），校验失败返回错误。
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		BotToken:  strings.TrimSpace(getenv("BOT_TOKEN")),
		TGAPIHash: strings.TrimSpace(getenv("TG_API_HASH")),
		TGPhone:   strings.TrimSpace(getenv("TG_PHONE")),
		DataDir:   strings.TrimSpace(getenv("DATA_DIR")),
		TempDir:   strings.TrimSpace(getenv("TEMP_DIR")),
	}

	if cfg.BotToken == "" {
		return cfg, fmt.Errorf("缺少环境变量：BOT_TOKEN")
	}
	if !botTokenPattern.MatchString(cfg.BotToken) {
		return cfg, fmt.Errorf("BOT_TOKEN 格式无效")
	}

	apiID, err := positiveInt(getenv("TG_API_ID"), "TG_API_ID")
	if err != nil {
		return cfg, err
	}
	cfg.TGAPIID = apiID

	// 本地 Bot API 服务器地址（--local 模式）：可选路线。配置后全部上传走
	// 该服务器（上限 MaxFileSize，sendPhoto 上限同步放宽）；未配置时超过
	// 50MB 的媒体自动经 Bot 号 MTProto 会话直传，同样可达 2000MB。
	cfg.BotAPIURL = strings.TrimRight(strings.TrimSpace(getenv("BOT_API_URL")), "/")

	if cfg.TGAPIHash == "" {
		return cfg, fmt.Errorf("缺少环境变量：TG_API_HASH")
	}

	// 登录模式：缺省 auto（优先扫码）；仅 phone 模式强制要求 TG_PHONE
	cfg.LoginMode = LoginModeAuto
	switch strings.ToLower(strings.TrimSpace(getenv("LOGIN_MODE"))) {
	case "", LoginModeAuto:
	case LoginModeQR:
		cfg.LoginMode = LoginModeQR
	case LoginModePhone:
		cfg.LoginMode = LoginModePhone
	default:
		return cfg, fmt.Errorf("LOGIN_MODE 必须是 auto/qr/phone")
	}
	if cfg.LoginMode == LoginModePhone && cfg.TGPhone == "" {
		return cfg, fmt.Errorf("LOGIN_MODE=phone 时必须配置 TG_PHONE")
	}

	cfg.AllowedUserIDs, err = parseAllowedUserIDs(getenv("ALLOWED_USER_IDS"))
	if err != nil {
		return cfg, err
	}

	cfg.WorkerCount = defaultWorkerCount
	if v := strings.TrimSpace(getenv("WORKER_COUNT")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("WORKER_COUNT 必须是不小于 1 的整数")
		}
		cfg.WorkerCount = n
	}

	if v := strings.TrimSpace(getenv("DUMP_CHANNEL_ID")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n == 0 {
			return cfg, fmt.Errorf("DUMP_CHANNEL_ID 必须是 -100 前缀的频道数字 ID（或留空关闭）")
		}
		cfg.DumpChannelID = n
	}

	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir
	}
	if cfg.TempDir == "" {
		cfg.TempDir = filepath.Join(cfg.DataDir, "tmp")
	}
	if filepath.Clean(cfg.TempDir) == filepath.Clean(cfg.DataDir) {
		return cfg, fmt.Errorf("TEMP_DIR 不能与 DATA_DIR 相同（会威胁 session 等数据文件）")
	}

	maxSize, err := optionalPositiveInt64(getenv("MAX_FILE_SIZE"), "MAX_FILE_SIZE", defaultMaxFileSize)
	if err != nil {
		return cfg, err
	}
	if maxSize > MaxMediaFileSize {
		return cfg, fmt.Errorf("MAX_FILE_SIZE 不能超过 %d 字节（MTProto 上传硬上限）", MaxMediaFileSize)
	}
	cfg.MaxFileSize = maxSize

	if cfg.BotAPIURL != "" {
		u, uerr := url.Parse(cfg.BotAPIURL)
		if uerr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return cfg, fmt.Errorf("BOT_API_URL 必须是形如 http://host:port 的 URL，得到 %q", cfg.BotAPIURL)
		}
		// 本地服务器模式下 sendPhoto 上限同样放宽到 MaxFileSize
		cfg.PhotoLimit = cfg.MaxFileSize
	} else {
		cfg.PhotoLimit = defaultPhotoLimit
	}

	streamLimit, err := optionalPositiveInt64(getenv("STREAM_LIMIT"), "STREAM_LIMIT", defaultStreamLimit)
	if err != nil {
		return cfg, err
	}
	cfg.StreamLimit = streamLimit

	// 内存重排序缓冲上限：StreamLimit 与 MaxFileSize 之间的"管道化内存
	// 流式"区间；校验放 Load 内而不动 ValidateMediaLimits（其签名被 Web
	// 运行时覆盖链复用，新键暂不进入运行时设置）。
	inMemoryLimit, err := optionalPositiveInt64(getenv("IN_MEMORY_LIMIT"), "IN_MEMORY_LIMIT", defaultInMemoryLimit)
	if err != nil {
		return cfg, err
	}
	if inMemoryLimit < streamLimit {
		return cfg, fmt.Errorf("IN_MEMORY_LIMIT 不能小于 STREAM_LIMIT（%d < %d）", inMemoryLimit, streamLimit)
	}
	if inMemoryLimit > maxSize {
		return cfg, fmt.Errorf("IN_MEMORY_LIMIT 不能大于 MAX_FILE_SIZE（%d > %d）", inMemoryLimit, maxSize)
	}
	cfg.InMemoryLimit = inMemoryLimit

	// 进程级内存预算：独立于单文件上限（IN_MEMORY_LIMIT）的总量闸门，
	// 低于该预算可承受的单文件大小时全部走磁盘管道，语义自洽无需跨字段校验。
	memoryBudget, err := optionalPositiveInt64(getenv("MEMORY_BUDGET"), "MEMORY_BUDGET", defaultMemoryBudget)
	if err != nil {
		return cfg, err
	}
	if memoryBudget < MinMemoryBudget || memoryBudget > MaxMemoryBudget {
		return cfg, fmt.Errorf("MEMORY_BUDGET 必须在 64MB–8GB 之间（得到 %d 字节）", memoryBudget)
	}
	cfg.MemoryBudget = memoryBudget

	cfg.DownloadThreads, err = optionalThreads(getenv("DOWNLOAD_THREADS"), "DOWNLOAD_THREADS")
	if err != nil {
		return cfg, err
	}
	cfg.UploadThreads, err = optionalThreads(getenv("UPLOAD_THREADS"), "UPLOAD_THREADS")
	if err != nil {
		return cfg, err
	}

	// MTProto 连接池上限（默认 4，1–16；1 即回退单连接现状）。
	cfg.DownloadConnections, err = optionalConnections(getenv("DOWNLOAD_CONNECTIONS"), "DOWNLOAD_CONNECTIONS")
	if err != nil {
		return cfg, err
	}
	cfg.UploadConnections, err = optionalConnections(getenv("UPLOAD_CONNECTIONS"), "UPLOAD_CONNECTIONS")
	if err != nil {
		return cfg, err
	}

	tempDirMaxSize, err := optionalPositiveInt64(getenv("TEMP_DIR_MAX_SIZE"), "TEMP_DIR_MAX_SIZE", defaultTempDirMaxSize)
	if err != nil {
		return cfg, err
	}
	cfg.TempDirMaxSize = tempDirMaxSize

	cfg.LogLevel = slog.LevelInfo
	switch v := strings.ToLower(strings.TrimSpace(getenv("LOG_LEVEL"))); v {
	case "", "info":
	case "debug":
		cfg.LogLevel = slog.LevelDebug
	case "warn":
		cfg.LogLevel = slog.LevelWarn
	case "error":
		cfg.LogLevel = slog.LevelError
	default:
		return cfg, fmt.Errorf("LOG_LEVEL 必须是 debug/info/warn/error")
	}

	// ---- Web 管理端 ----
	cfg.WebAddr = strings.TrimSpace(getenv("WEB_ADDR"))
	if cfg.WebAddr == "" {
		cfg.WebAddr = defaultWebAddr
	}
	if _, _, err := net.SplitHostPort(cfg.WebAddr); err != nil {
		return cfg, fmt.Errorf("WEB_ADDR 必须是 host:port 形式（如 127.0.0.1:8080），得到 %q", cfg.WebAddr)
	}

	cfg.GitHubClientID = strings.TrimSpace(getenv("GITHUB_CLIENT_ID"))
	cfg.GitHubClientSecret = strings.TrimSpace(getenv("GITHUB_CLIENT_SECRET"))
	if (cfg.GitHubClientID == "") != (cfg.GitHubClientSecret == "") {
		return cfg, fmt.Errorf("GITHUB_CLIENT_ID 与 GITHUB_CLIENT_SECRET 必须同时配置或同时留空")
	}
	if raw := strings.TrimSpace(getenv("WEB_OAUTH_ENCRYPTION_KEY")); raw != "" {
		key, err := ParseOAuthEncryptionKey(raw)
		if err != nil {
			return cfg, err
		}
		cfg.OAuthEncryptionKey = key
	}

	trusted, err := optionalBool(getenv("WEB_TRUSTED_PROXY"), "WEB_TRUSTED_PROXY")
	if err != nil {
		return cfg, err
	}
	cfg.WebTrustedProxy = trusted

	// ---- 事件与通知----
	cfg.NotifyCooldownMin = defaultNotifyCooldownMin
	if v := strings.TrimSpace(getenv("NOTIFY_COOLDOWN_MIN")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || int64(n) > maxNotifyCooldownMin {
			return cfg, fmt.Errorf("NOTIFY_COOLDOWN_MIN 必须是可表示为时长的正整数分钟")
		}
		cfg.NotifyCooldownMin = n
	}

	cfg.EventBotFailThreshold = defaultEventBotFailThreshold
	if v := strings.TrimSpace(getenv("EVENT_BOT_FAIL_THRESHOLD")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 {
			return cfg, fmt.Errorf("EVENT_BOT_FAIL_THRESHOLD 必须是不小于 1 的整数")
		}
		cfg.EventBotFailThreshold = n
	}

	cfg.EventTaskFailThreshold = defaultEventTaskFailThreshold
	if v := strings.TrimSpace(getenv("EVENT_TASK_FAIL_THRESHOLD")); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 {
			return cfg, fmt.Errorf("EVENT_TASK_FAIL_THRESHOLD 必须是不小于 1 的整数")
		}
		cfg.EventTaskFailThreshold = n
	}

	cfg.EventDiskLimitGB = defaultEventDiskLimitGB
	if v := strings.TrimSpace(getenv("EVENT_DISK_LIMIT_GB")); v != "" {
		f, perr := strconv.ParseFloat(v, 64)
		if perr != nil || f < minDiskLimitGB || math.IsInf(f, 0) || math.IsNaN(f) ||
			f >= float64(math.MaxInt64)/(1<<30) {
			return cfg, fmt.Errorf("EVENT_DISK_LIMIT_GB 必须是可表示为字节的有限正数")
		}
		cfg.EventDiskLimitGB = f
	}

	return cfg, nil
}

// LoadAdminCLI 供 admin 子命令（如 spore admin reset-key）使用：
// 只解析数据目录相关配置，不要求 Bot Token 等 Telegram 凭据——
// 运维环境（如只剩数据库的恢复场景）可能没有完整凭据。
func LoadAdminCLI(getenv func(string) string) (Config, error) {
	cfg := Config{
		DataDir: strings.TrimSpace(getenv("DATA_DIR")),
		TempDir: strings.TrimSpace(getenv("TEMP_DIR")),
	}
	if cfg.DataDir == "" {
		cfg.DataDir = defaultDataDir
	}
	if cfg.TempDir == "" {
		cfg.TempDir = filepath.Join(cfg.DataDir, "tmp")
	}
	return cfg, nil
}

// optionalBool 解析可选布尔环境变量：空 = 缺省 false，接受 1/true/yes 与 0/false/no。
func optionalBool(value, name string) (bool, error) {
	v := strings.ToLower(strings.TrimSpace(value))
	switch v {
	case "", "0", "false", "no":
		return false, nil
	case "1", "true", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("%s 必须是布尔值（1/true/yes 或 0/false/no）", name)
	}
}

func positiveInt(value, name string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, fmt.Errorf("缺少环境变量：%s", name)
	}
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s 必须是正整数", name)
	}
	return n, nil
}

func optionalPositiveInt64(value, name string, fallback int64) (int64, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s 必须是正整数", name)
	}
	return n, nil
}

// optionalThreads 解析传输线程数（默认 4，1–16；1 即回退单线程现状行为）。
func optionalThreads(value, name string) (int, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return defaultTransferThreads, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxTransferThreads {
		return 0, fmt.Errorf("%s 必须是 1–16 的整数", name)
	}
	return n, nil
}

// optionalConnections 解析 MTProto 连接池上限（默认 4，1–16；1 即回退
// 单连接现状行为）。与线程数同区间：连接数一般不超过分片线程数——更多
// 连接没有对应并发去填充，反而放大服务端限流概率。
func optionalConnections(value, name string) (int, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return defaultTransferThreads, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > maxTransferThreads {
		return 0, fmt.Errorf("%s 必须是 1–16 的整数", name)
	}
	return n, nil
}

// parseAllowedUserIDs 解析 "123,456" 形式的白名单。可空：白名单已由数据库
// （users 表）接管，env 只在首次启动（users 表为空）时导入；空库 + 空 env 时
// Bot 对链接按未授权处理。含无效 ID 时仍报错，避免拼写错误静默丢人。
// ParseOAuthEncryptionKey 解析 OAuth Secret 加密主密钥。
// 支持原始 32 字节、64 位十六进制或解码后为 32 字节的 base64。
func ParseOAuthEncryptionKey(raw string) ([]byte, error) {
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	if decoded, err := hex.DecodeString(raw); err == nil && len(decoded) == 32 {
		return decoded, nil
	}
	for _, encoding := range []*base64.Encoding{base64.RawStdEncoding, base64.StdEncoding, base64.RawURLEncoding, base64.URLEncoding} {
		if decoded, err := encoding.DecodeString(raw); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("WEB_OAUTH_ENCRYPTION_KEY 必须是 32 字节原文、64 位十六进制或 base64")
}

// BotAPIUploadCap 返回"经 Bot API 上传"路径的大小上限，超出该上限的媒体
// 路由到 Bot 号 MTProto 大文件直传：本地服务器（BOT_API_URL）上限即
// MaxFileSize；官方服务器为 50MB 硬上限。
func (c Config) BotAPIUploadCap() int64 {
	if c.BotAPIURL != "" {
		return c.MaxFileSize
	}
	return OfficialMaxFileSize
}

// ValidateMediaLimits 校验 Web 设置和启动覆盖使用的媒体边界。
func ValidateMediaLimits(maxFileSize, streamLimit, tempDirMaxSize int64) error {
	if maxFileSize < MinMediaFileSize || maxFileSize > MaxMediaFileSize {
		return fmt.Errorf("文件大小上限必须在 1MB–2000MB 之间")
	}
	if streamLimit < MinMediaFileSize || streamLimit > MaxMediaFileSize {
		return fmt.Errorf("流式阈值必须在 1MB–2000MB 之间")
	}
	if streamLimit > maxFileSize {
		return fmt.Errorf("流式阈值不能大于文件大小上限")
	}
	if tempDirMaxSize < MinTempDirMaxSize || tempDirMaxSize > MaxTempDirMaxSize {
		return fmt.Errorf("临时目录最大大小必须在 1MB–1TB 之间")
	}
	if tempDirMaxSize < maxFileSize {
		return fmt.Errorf("临时目录最大大小不能小于文件大小上限")
	}
	return nil
}

func parseAllowedUserIDs(raw string) (map[int64]struct{}, error) {
	out := make(map[int64]struct{})
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		id, err := strconv.ParseInt(item, 10, 64)
		if err != nil || id <= 0 {
			return out, fmt.Errorf("ALLOWED_USER_IDS 含无效用户 ID：%q", item)
		}
		out[id] = struct{}{}
	}
	return out, nil
}
