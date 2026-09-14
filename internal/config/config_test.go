package config

import (
	"log/slog"
	"strconv"
	"testing"
)

// baseEnv 返回一份完整合法的环境变量基线，各用例在此基础上覆盖。
func baseEnv(overrides map[string]string) func(string) string {
	env := map[string]string{
		"BOT_TOKEN":        "1234567890:ABCDEFGHIJKLMNOPqrstuv",
		"TG_API_ID":        "1234567",
		"TG_API_HASH":      "abcdef0123456789abcdef0123456789",
		"TG_PHONE":         "+8613800000000",
		"ALLOWED_USER_IDS": "111,222",
	}
	for k, v := range overrides {
		env[k] = v
	}
	return func(key string) string { return env[key] }
}

func TestLoadMinimalDefaults(t *testing.T) {
	cfg, err := Load(baseEnv(nil))
	if err != nil {
		t.Fatalf("期望成功，得到错误：%v", err)
	}
	if cfg.WorkerCount != 1 {
		t.Errorf("WorkerCount 默认应为 1，得到 %d", cfg.WorkerCount)
	}
	if cfg.DataDir != "data" {
		t.Errorf("DataDir 默认应为 data，得到 %q", cfg.DataDir)
	}
	if cfg.TempDir != "data/tmp" {
		t.Errorf("TempDir 默认应为 data/tmp，得到 %q", cfg.TempDir)
	}
	if cfg.MaxFileSize != int64(2000)<<20 {
		t.Errorf("MaxFileSize 默认应为 2000MB，得到 %d", cfg.MaxFileSize)
	}
	if cfg.StreamLimit != 20<<20 {
		t.Errorf("StreamLimit 默认应为 20MB，得到 %d", cfg.StreamLimit)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel 默认应为 info，得到 %v", cfg.LogLevel)
	}
	want := map[int64]struct{}{111: {}, 222: {}}
	if len(cfg.AllowedUserIDs) != 2 {
		t.Fatalf("AllowedUserIDs 应含 2 项，得到 %v", cfg.AllowedUserIDs)
	}
	for id := range want {
		if _, ok := cfg.AllowedUserIDs[id]; !ok {
			t.Errorf("AllowedUserIDs 缺少 %d", id)
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(baseEnv(map[string]string{
		"WORKER_COUNT":    "3",
		"DATA_DIR":        "/var/lib/spore",
		"MAX_FILE_SIZE":   "1048576",
		"STREAM_LIMIT":    "524288",
		"IN_MEMORY_LIMIT": "524288", // 与 STREAM_LIMIT 相等合法（区间退化为空）
		"LOG_LEVEL":       "debug",
	}))
	if err != nil {
		t.Fatalf("期望成功，得到错误：%v", err)
	}
	if cfg.WorkerCount != 3 || cfg.DataDir != "/var/lib/spore" ||
		cfg.TempDir != "/var/lib/spore/tmp" ||
		cfg.MaxFileSize != 1048576 || cfg.StreamLimit != 524288 ||
		cfg.InMemoryLimit != 524288 ||
		cfg.LogLevel != slog.LevelDebug {
		t.Errorf("覆盖值未生效：%+v", cfg)
	}
}

// 传输并发与内存上限：默认值、合法覆盖与非法取值。
func TestLoadTransferTuning(t *testing.T) {
	cfg, err := Load(baseEnv(nil))
	if err != nil {
		t.Fatalf("期望成功，得到错误：%v", err)
	}
	if cfg.DownloadThreads != 4 || cfg.UploadThreads != 4 {
		t.Errorf("线程默认应为 4，得到 dl=%d up=%d", cfg.DownloadThreads, cfg.UploadThreads)
	}
	if cfg.DownloadConnections != 4 || cfg.UploadConnections != 4 {
		t.Errorf("连接池默认应为 4，得到 dl=%d up=%d", cfg.DownloadConnections, cfg.UploadConnections)
	}
	if cfg.InMemoryLimit != int64(512)<<20 {
		t.Errorf("内存上限默认应为 512MB，得到 %d", cfg.InMemoryLimit)
	}
	if cfg.MemoryBudget != int64(1)<<30 {
		t.Errorf("内存预算默认应为 1GB，得到 %d", cfg.MemoryBudget)
	}

	cfg, err = Load(baseEnv(map[string]string{
		"DOWNLOAD_THREADS":     "8",
		"UPLOAD_THREADS":       "1",
		"DOWNLOAD_CONNECTIONS": "2",
		"UPLOAD_CONNECTIONS":   "16",
		"IN_MEMORY_LIMIT":      "1073741824",
		"MEMORY_BUDGET":        "1073741824",
	}))
	if err != nil {
		t.Fatalf("合法覆盖应成功，得到错误：%v", err)
	}
	if cfg.DownloadThreads != 8 || cfg.UploadThreads != 1 || cfg.InMemoryLimit != 1<<30 {
		t.Errorf("覆盖值未生效：%+v", cfg)
	}
	if cfg.MemoryBudget != 1<<30 {
		t.Errorf("内存预算覆盖值未生效：%d", cfg.MemoryBudget)
	}
	if cfg.DownloadConnections != 2 || cfg.UploadConnections != 16 {
		t.Errorf("连接池覆盖值未生效：%+v", cfg)
	}

	for name, overrides := range map[string]map[string]string{
		"线程数为零":      {"DOWNLOAD_THREADS": "0"},
		"线程数超上限":     {"UPLOAD_THREADS": "17"},
		"线程数非整数":     {"DOWNLOAD_THREADS": "abc"},
		"连接数为零":      {"DOWNLOAD_CONNECTIONS": "0"},
		"连接数超上限":     {"UPLOAD_CONNECTIONS": "17"},
		"连接数非整数":     {"DOWNLOAD_CONNECTIONS": "1.5"},
		"内存上限低于流式":   {"IN_MEMORY_LIMIT": "1048576"},    // 默认 STREAM_LIMIT 20MB
		"内存上限超过文件上限": {"IN_MEMORY_LIMIT": "3145728000"}, // 3000MB > 默认 MAX_FILE_SIZE 2000MB
		"内存预算低于下界":   {"MEMORY_BUDGET": "33554432"},     // 32MB < 64MB
		"内存预算超过上界":   {"MEMORY_BUDGET": "9663676416"},   // 9GB > 8GB
		"内存预算非整数":    {"MEMORY_BUDGET": "1.5GB"},
	} {
		if _, err := Load(baseEnv(overrides)); err == nil {
			t.Errorf("%s 应报错", name)
		}
	}
}

func TestLoadLoginModes(t *testing.T) {
	// auto 模式（默认）下 TG_PHONE 可以缺失
	env := baseEnv(map[string]string{"TG_PHONE": "", "LOGIN_MODE": ""})
	cfg, err := Load(env)
	if err != nil || cfg.LoginMode != LoginModeAuto {
		t.Fatalf("auto 模式应允许 TG_PHONE 缺失，got mode=%s err=%v", cfg.LoginMode, err)
	}
	// qr 强制扫码
	cfg, err = Load(baseEnv(map[string]string{"LOGIN_MODE": "qr"}))
	if err != nil || cfg.LoginMode != LoginModeQR {
		t.Fatalf("qr 模式解析失败：err=%v", err)
	}
}

func TestLoadBotAPIServerRules(t *testing.T) {
	// 大文件上限不再要求本地服务器：默认 2000MB，未配置 BOT_API_URL 直接放行
	cfg, err := Load(baseEnv(map[string]string{"MAX_FILE_SIZE": "2097152000"}))
	if err != nil {
		t.Fatalf("MAX_FILE_SIZE 超 50MB 且无 BOT_API_URL 应放行（大文件走 MTProto 直传）：%v", err)
	}
	if cfg.BotAPIUploadCap() != OfficialMaxFileSize {
		t.Errorf("官方服务器上传上限应为 50MB，得到 %d", cfg.BotAPIUploadCap())
	}
	// 配置了本地服务器则上传上限放宽到 MaxFileSize，且 photo 上限同步放宽
	cfg, err = Load(baseEnv(map[string]string{
		"BOT_API_URL":   "http://localhost:8081",
		"MAX_FILE_SIZE": "2097152000",
	}))
	if err != nil {
		t.Fatalf("接本地服务器后应放行：%v", err)
	}
	if cfg.PhotoLimit != 2097152000 {
		t.Errorf("本地服务器下 PhotoLimit 应等于 MAX_FILE_SIZE，得到 %d", cfg.PhotoLimit)
	}
	if cfg.BotAPIUploadCap() != 2097152000 {
		t.Errorf("本地服务器下 BotAPIUploadCap 应等于 MAX_FILE_SIZE，得到 %d", cfg.BotAPIUploadCap())
	}
	// 官方服务器下 PhotoLimit 为默认 10MB；缺省 MaxFileSize 为 2000MB
	cfg, err = Load(baseEnv(nil))
	if err != nil || cfg.PhotoLimit != 10<<20 {
		t.Errorf("官方服务器下 PhotoLimit 应为 10MB，得到 %d err=%v", cfg.PhotoLimit, err)
	}
	if cfg.MaxFileSize != int64(2000)<<20 {
		t.Errorf("MAX_FILE_SIZE 默认应为 2000MB，得到 %d", cfg.MaxFileSize)
	}
	// 超过 MTProto 上传硬上限拒绝
	if _, err := Load(baseEnv(map[string]string{"MAX_FILE_SIZE": strconv.FormatInt(int64(2048)<<20, 10)})); err == nil {
		t.Fatal("MAX_FILE_SIZE 超 2000MB 硬上限应报错")
	}
}

func TestLoadTempDirMaxSize(t *testing.T) {
	// 缺省 5GB
	cfg, err := Load(baseEnv(nil))
	if err != nil || cfg.TempDirMaxSize != 5<<30 {
		t.Fatalf("TempDirMaxSize 默认应为 5GB，得到 %d err=%v", cfg.TempDirMaxSize, err)
	}
	// 覆盖生效
	cfg, err = Load(baseEnv(map[string]string{"TEMP_DIR_MAX_SIZE": "10737418240"}))
	if err != nil || cfg.TempDirMaxSize != 10<<30 {
		t.Fatalf("TempDirMaxSize 覆盖应生效，得到 %d err=%v", cfg.TempDirMaxSize, err)
	}
	// 非法值拒绝
	if _, err := Load(baseEnv(map[string]string{"TEMP_DIR_MAX_SIZE": "-5"})); err == nil {
		t.Fatal("TEMP_DIR_MAX_SIZE 非法值应报错")
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]string
	}{
		{"缺少 BOT_TOKEN", map[string]string{"BOT_TOKEN": ""}},
		{"BOT_TOKEN 格式无效", map[string]string{"BOT_TOKEN": "short"}},
		{"TG_API_ID 非正整数", map[string]string{"TG_API_ID": "abc"}},
		{"缺少 TG_API_HASH", map[string]string{"TG_API_HASH": ""}},
		{"LOGIN_MODE 非法", map[string]string{"LOGIN_MODE": "sms"}},
		{"phone 模式缺 TG_PHONE", map[string]string{"LOGIN_MODE": "phone", "TG_PHONE": ""}},
		{"ALLOWED_USER_IDS 含负数", map[string]string{"ALLOWED_USER_IDS": "-5"}},
		{"WORKER_COUNT 为 0", map[string]string{"WORKER_COUNT": "0"}},
		{"TEMP_DIR 等于 DATA_DIR", map[string]string{"TEMP_DIR": "data"}},
		{"BOT_API_URL 缺协议", map[string]string{"BOT_API_URL": "localhost:8081"}},
		{"MAX_FILE_SIZE 非法", map[string]string{"MAX_FILE_SIZE": "-1"}},
		{"LOG_LEVEL 非法", map[string]string{"LOG_LEVEL": "verbose"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(baseEnv(tc.overrides)); err == nil {
				t.Fatalf("期望报错，实际成功")
			}
		})
	}
}

// 白名单已由数据库接管：ALLOWED_USER_IDS 可空（首启导入用），空值不再阻止启动。
func TestLoadAllowedUserIDsOptional(t *testing.T) {
	cfg, err := Load(baseEnv(map[string]string{"ALLOWED_USER_IDS": ""}))
	if err != nil {
		t.Fatalf("空白名单应允许启动: %v", err)
	}
	if len(cfg.AllowedUserIDs) != 0 {
		t.Errorf("空白名单应得到空表，得到 %v", cfg.AllowedUserIDs)
	}
	// 只含空项同样视为空
	cfg, err = Load(baseEnv(map[string]string{"ALLOWED_USER_IDS": " , "}))
	if err != nil || len(cfg.AllowedUserIDs) != 0 {
		t.Fatalf("空项白名单应得到空表: cfg=%v err=%v", cfg.AllowedUserIDs, err)
	}
}

// Web 管理端配置：默认回环地址 + 双通道可独立留空。
func TestLoadWebDefaults(t *testing.T) {
	cfg, err := Load(baseEnv(nil))
	if err != nil {
		t.Fatalf("期望成功，得到错误：%v", err)
	}
	if cfg.WebAddr != "127.0.0.1:8080" {
		t.Errorf("WebAddr 默认应为 127.0.0.1:8080，得到 %q", cfg.WebAddr)
	}
	if cfg.GitHubClientID != "" || cfg.GitHubClientSecret != "" {
		t.Errorf("GitHub OAuth 默认应未配置，得到 %q/%q", cfg.GitHubClientID, cfg.GitHubClientSecret)
	}
	if cfg.WebTrustedProxy {
		t.Error("WebTrustedProxy 默认应为 false")
	}
}

func TestLoadWebOverrides(t *testing.T) {
	cfg, err := Load(baseEnv(map[string]string{
		"WEB_ADDR":             "127.0.0.1:9000",
		"GITHUB_CLIENT_ID":     "iv1.client",
		"GITHUB_CLIENT_SECRET": "ssh-secret",
		"WEB_TRUSTED_PROXY":    "true",
	}))
	if err != nil {
		t.Fatalf("期望成功，得到错误：%v", err)
	}
	if cfg.WebAddr != "127.0.0.1:9000" || cfg.GitHubClientID != "iv1.client" ||
		cfg.GitHubClientSecret != "ssh-secret" || !cfg.WebTrustedProxy {
		t.Errorf("Web 覆盖值未生效：%+v", cfg)
	}
}

func TestLoadWebRejectsBadValues(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]string
	}{
		{"WEB_ADDR 非 host:port", map[string]string{"WEB_ADDR": "not-an-addr"}},
		{"只配置 GitHub client id", map[string]string{"GITHUB_CLIENT_ID": "iv1.client"}},
		{"只配置 GitHub client secret", map[string]string{"GITHUB_CLIENT_SECRET": "s"}},
		{"WEB_TRUSTED_PROXY 非布尔", map[string]string{"WEB_TRUSTED_PROXY": "maybe"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(baseEnv(tc.overrides)); err == nil {
				t.Fatalf("期望报错，实际成功")
			}
		})
	}
}

// admin 子命令只解析数据目录，不要求 Telegram 凭据。
func TestLoadAdminCLI(t *testing.T) {
	cfg, err := LoadAdminCLI(func(string) string { return "" })
	if err != nil {
		t.Fatalf("空环境应成功：%v", err)
	}
	if cfg.DataDir != "data" || cfg.TempDir != "data/tmp" {
		t.Errorf("默认目录不符：%+v", cfg)
	}
	cfg, err = LoadAdminCLI(func(key string) string {
		if key == "DATA_DIR" {
			return "/srv/spore"
		}
		return ""
	})
	if err != nil || cfg.DataDir != "/srv/spore" || cfg.TempDir != "/srv/spore/tmp" {
		t.Errorf("覆盖目录未生效：%+v err=%v", cfg, err)
	}
}

// ---- 事件与通知----

func TestLoadNotifyDefaults(t *testing.T) {
	cfg, err := Load(baseEnv(nil))
	if err != nil {
		t.Fatalf("期望成功，得到错误：%v", err)
	}
	if cfg.NotifyCooldownMin != 30 {
		t.Errorf("NotifyCooldownMin 默认应为 30，得到 %d", cfg.NotifyCooldownMin)
	}
	if cfg.EventBotFailThreshold != 3 {
		t.Errorf("EventBotFailThreshold 默认应为 3，得到 %d", cfg.EventBotFailThreshold)
	}
	if cfg.EventTaskFailThreshold != 5 {
		t.Errorf("EventTaskFailThreshold 默认应为 5，得到 %d", cfg.EventTaskFailThreshold)
	}
	if cfg.EventDiskLimitGB != 1 {
		t.Errorf("EventDiskLimitGB 默认应为 1，得到 %v", cfg.EventDiskLimitGB)
	}
}

func TestLoadNotifyOverrides(t *testing.T) {
	cfg, err := Load(baseEnv(map[string]string{
		"NOTIFY_COOLDOWN_MIN":       "60",
		"EVENT_BOT_FAIL_THRESHOLD":  "5",
		"EVENT_TASK_FAIL_THRESHOLD": "10",
		"EVENT_DISK_LIMIT_GB":       "0.5",
	}))
	if err != nil {
		t.Fatalf("期望成功，得到错误：%v", err)
	}
	if cfg.NotifyCooldownMin != 60 || cfg.EventBotFailThreshold != 5 ||
		cfg.EventTaskFailThreshold != 10 || cfg.EventDiskLimitGB != 0.5 {
		t.Errorf("事件通知覆盖值未生效：%+v", cfg)
	}
}

func TestLoadNotifyRejectsBadValues(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]string
	}{
		{"NOTIFY_COOLDOWN_MIN 非数字", map[string]string{"NOTIFY_COOLDOWN_MIN": "x"}},
		{"NOTIFY_COOLDOWN_MIN 为零", map[string]string{"NOTIFY_COOLDOWN_MIN": "0"}},
		{"EVENT_BOT_FAIL_THRESHOLD 为负", map[string]string{"EVENT_BOT_FAIL_THRESHOLD": "-1"}},
		{"EVENT_TASK_FAIL_THRESHOLD 非数字", map[string]string{"EVENT_TASK_FAIL_THRESHOLD": "many"}},
		{"EVENT_DISK_LIMIT_GB 为零", map[string]string{"EVENT_DISK_LIMIT_GB": "0"}},
		{"EVENT_DISK_LIMIT_GB 为负", map[string]string{"EVENT_DISK_LIMIT_GB": "-2"}},
		{"EVENT_DISK_LIMIT_GB 非数字", map[string]string{"EVENT_DISK_LIMIT_GB": "1GB"}},
		{"EVENT_DISK_LIMIT_GB 溢出", map[string]string{"EVENT_DISK_LIMIT_GB": "1e10"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(baseEnv(tc.overrides)); err == nil {
				t.Fatalf("期望报错，实际成功")
			}
		})
	}
}
