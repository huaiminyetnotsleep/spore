package web

// 运营设置、事件解决与数据库备份导出/导入的核心逻辑。
// 当前 SSR 设置/事件/审计/备份页面已删除，本文件只保留 API 写 handler、
// api_backup.go 与 cmd/bot 启动装配（LoadQueueCapacity）仍在使用的共享核心。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
	"github.com/huaiminyetnotsleep/spore/internal/transfercfg"
)

// settings 表中由本包持有的运营键（value_json 为 JSON 编码值，与 access/web 认证键约定一致）。
const (
	settingKeyQueueCapacity      = "queue_capacity"        // 全局队列容量（JSON 数字；重启生效）
	settingKeyWorkerCount        = "worker_count"          // 任务并发 worker 数（JSON 数字；重启生效）
	settingKeyLastBackupAt       = syscfg.KeyLastBackupAt  // 最近一次备份时间（JSON 数字，Unix 毫秒；syscfg 单一来源）
	settingKeyMaxFileSize        = "max_file_size"         // 媒体上限（JSON 数字字节；重启生效）
	settingKeyStreamLimit        = "stream_limit"          // 流式阈值（JSON 数字字节；重启生效）
	settingKeyTempDirMaxSize     = "temp_dir_max_size"     // 临时目录总量上限（JSON 数字字节；重启生效）
	settingKeyMemoryBudget       = "memory_budget"         // 内存管道进程级预算（JSON 数字字节；即时生效）
	settingKeyMaxLinksPerMessage = "max_links_per_message" // 单条 Bot 输入最大有效链接数（JSON 数字；即时生效）
	settingKeyChannelCopyEnable  = "channel_copy_enabled"  // 频道副本同步总开关（JSON 布尔；即时生效）
	settingKeyTGReuseEnable      = "tg_reuse_enabled"      // TG 链接复用总开关（JSON 布尔；即时生效）
	// 缓存频道（重复链接复用的干净副本来源）：数字频道 ID 与标题。Web 端
	// 配置（输入 @username / t.me 链接 / -100 数字 ID，经 bot 解析校验后
	// 存数字 ID）；环境变量 DUMP_CHANNEL_ID 仅作 settings 为空时的兜底。
	settingKeyDumpChannelID    = "dump_channel_id"
	settingKeyDumpChannelTitle = "dump_channel_title"
)

// worker 数的 Web 可调范围：上限与传输线程一致，约束多任务并发的内存/连接放大。
const (
	minWorkerCount = 1
	maxWorkerCount = 16
)

// defaultQueueCapacity 是队列容量缺省值（原 main 硬编码值）。
const defaultQueueCapacity = 64

func loadJSONSetting[T any](ctx context.Context, st *store.Store, key string, fallback T, valid func(T) bool) T {
	raw, ok, err := st.GetSetting(ctx, key)
	if err != nil || !ok {
		return fallback
	}
	var value T
	if json.Unmarshal([]byte(raw), &value) != nil || valid != nil && !valid(value) {
		return fallback
	}
	return value
}

// LoadQueueCapacity 读取全局队列容量设置：键缺失或非法时回退默认 64。
// main 装配内存队列时调用；容量在进程生命周期内固定，修改需重启生效。
func LoadQueueCapacity(ctx context.Context, st *store.Store) int {
	return loadJSONSetting(ctx, st, settingKeyQueueCapacity, defaultQueueCapacity,
		func(n int) bool { return n > 0 && n <= 4096 })
}

// loadQueueCapacity 内部版本（带 Server 上下文）。
func (s *Server) loadQueueCapacity(ctx context.Context) int {
	return LoadQueueCapacity(ctx, s.st)
}

// LoadWorkerCount 读取任务并发 worker 数：键缺失、非法或越界时回退环境默认值。
// main 在数据库打开后、队列启动前调用并覆盖 cfg.WorkerCount；worker 数在进程
// 生命周期内固定，修改需重启生效。
func LoadWorkerCount(ctx context.Context, st *store.Store, envDefault int) int {
	return loadJSONSetting(ctx, st, settingKeyWorkerCount, envDefault,
		func(n int) bool { return n >= minWorkerCount && n <= maxWorkerCount })
}

// LoadMemoryBudget 读取内存管道进程级预算（字节）：键缺失、非法或越界时
// 回退环境默认值。闸门在每次媒体打开前经闭包实时调用（main 注入），
// 管理端修改即时生效，无需重启。
func LoadMemoryBudget(ctx context.Context, st *store.Store, envDefault int64) int64 {
	if st == nil {
		return envDefault
	}
	return loadJSONSetting(ctx, st, settingKeyMemoryBudget, envDefault,
		func(n int64) bool { return n >= config.MinMemoryBudget && n <= config.MaxMemoryBudget })
}

// LoadMaxLinksPerMessage 读取单条 Bot 输入允许的有效链接数。handler 每次处理
// 输入时实时读取，管理端保存后即时生效；键缺失或非法时回退环境默认值。
func LoadMaxLinksPerMessage(ctx context.Context, st *store.Store, envDefault int) int {
	if envDefault < config.MinLinksPerMessage || envDefault > config.MaxLinksPerMessage {
		envDefault = config.DefaultMaxLinksPerMessage
	}
	if st == nil {
		return envDefault
	}
	return loadJSONSetting(ctx, st, settingKeyMaxLinksPerMessage, envDefault,
		func(n int) bool { return n >= config.MinLinksPerMessage && n <= config.MaxLinksPerMessage })
}

// LoadChannelCopyEnabled 读取频道副本同步总开关：键缺失或非法时回退开启。
// worker 每次投递副本前实时读取（main 装配闭包注入），管理端修改即时生效；
// 关闭只暂停副本投递，用户绑定关系保留。
func LoadChannelCopyEnabled(ctx context.Context, st *store.Store) bool {
	if st == nil {
		return true
	}
	return loadJSONSetting(ctx, st, settingKeyChannelCopyEnable, true, nil)
}

// LoadTGReuseEnabled 读取 TG 链接复用总开关：键缺失或非法时回退开启。
// worker 处理任务前实时读取（main 装配闭包注入），管理端修改即时生效；
// 关闭后重复链接回到完整"下载+上传"链路，历史坐标保留（重开即恢复复用）。
func LoadTGReuseEnabled(ctx context.Context, st *store.Store) bool {
	if st == nil {
		return true
	}
	return loadJSONSetting(ctx, st, settingKeyTGReuseEnable, true, nil)
}

// LoadDumpChannelID 读取缓存频道数字 ID（settings）：0 表示未配置。
func LoadDumpChannelID(ctx context.Context, st *store.Store) int64 {
	if st == nil {
		return 0
	}
	return loadJSONSetting(ctx, st, settingKeyDumpChannelID, int64(0), nil)
}

// LoadEffectiveDumpChannelID 读取缓存频道的运行时有效值：settings 键存在时
// （包括显式 0=关闭）优先；键缺失/非法时回落环境变量。显式 0 必须覆盖
// env，否则 Web 端「清除配置」无法关闭环境变量预置的频道。
func LoadEffectiveDumpChannelID(ctx context.Context, st *store.Store, envDefault int64) int64 {
	if st == nil {
		return envDefault
	}
	return loadJSONSetting(ctx, st, settingKeyDumpChannelID, envDefault, nil)
}

// LoadDumpChannelTitle 读取缓存频道标题（展示用；未配置为空）。
func LoadDumpChannelTitle(ctx context.Context, st *store.Store) string {
	if st == nil {
		return ""
	}
	return loadJSONSetting(ctx, st, settingKeyDumpChannelTitle, "", nil)
}

// ---- 事件解决 ----

// resolveEventCore 把事件标记为已解决（SPA API 核心）。
// 当前优先经事件中心执行（与系统自动恢复共用 resolve+审计语义：
// 解决后同一事件再次发生会重开为 open 并重新触发通知）；未装配 Hub 时
// （如部分测试环境）回退到直写 store + 本地审计。
func (s *Server) resolveEventCore(ctx context.Context, id int64) error {
	if s.hub != nil {
		return s.hub.Resolve(ctx, id)
	}
	if err := s.st.ResolveEvent(ctx, id); err != nil {
		return err
	}
	s.audit(ctx, "event.resolve", fmt.Sprintf("event:%d", id), nil)
	return nil
}

// ---- 运营设置 ----

// loadMediaSettings 读取数据库中的媒体配置；缺失或非法时回退启动配置。
func (s *Server) loadMediaSettings(ctx context.Context) (int64, int64, int64) {
	maxSize, stream, tempDirMax := s.cfg.MaxFileSize, s.cfg.StreamLimit, s.cfg.TempDirMaxSize
	if maxSize == 0 {
		maxSize = int64(2000) << 20
	}
	if stream == 0 {
		stream = int64(20) << 20
	}
	if tempDirMax == 0 {
		tempDirMax = int64(5) << 30
	}
	positive := func(v int64) bool { return v > 0 }
	return loadJSONSetting(ctx, s.st, settingKeyMaxFileSize, maxSize, positive),
		loadJSONSetting(ctx, s.st, settingKeyStreamLimit, stream, positive),
		loadJSONSetting(ctx, s.st, settingKeyTempDirMaxSize, tempDirMax, positive)
}

// parseMediaInput 把 MB/GB 表单值转换为字节，拒绝小数溢出和未知单位。
func parseMediaInput(raw, unit string) (int64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("大小必须是正数")
	}
	multiplier := float64(1 << 20)
	if strings.EqualFold(strings.TrimSpace(unit), "GB") {
		multiplier = float64(1 << 30)
	} else if !strings.EqualFold(strings.TrimSpace(unit), "MB") && strings.TrimSpace(unit) != "" {
		return 0, fmt.Errorf("单位必须是 MB 或 GB")
	}
	bytes := v * multiplier
	if bytes > float64(^uint64(0)>>1) || bytes != float64(int64(bytes)) {
		return 0, fmt.Errorf("大小超出可表示范围")
	}
	return int64(bytes), nil
}

// settingsUpdateInput 是一次设置变更的原始载荷（SPA API JSON 映射到本结构，
// 再交 applySettingsUpdate 按序应用）；空串字段与 nil 指针表示不变更。
// 数值字段保留原文：applySettingsUpdate 在对应步骤解析，保证任何一项参数
// 非法时与前序已生效项保持既有中止顺序。
type settingsUpdateInput struct {
	Timezone           string
	DedupWindowRaw     string
	QueueCapRaw        string
	MaxLinksPerMessage *int
	// WorkerCount 为任务并发 worker 数；nil 表示不变更（重启生效）。
	WorkerCount        *int
	MaxFileSizeRaw     string
	MaxFileSizeUnit    string
	StreamLimitRaw     string
	StreamLimitUnit    string
	TempDirMaxSizeRaw  string
	TempDirMaxSizeUnit string
	// MemoryBudgetRaw/Unit 为内存管道进程级预算（MB/GB 表单值，内部存字节）；
	// 空串表示不变更。即时生效。
	MemoryBudgetRaw  string
	MemoryBudgetUnit string
	// ChannelCopyEnabled 为频道副本同步总开关；nil 表示不变更。
	ChannelCopyEnabled *bool
	// TGReuseEnabled 为 TG 链接复用总开关（copyMessages 直拷跳过重复
	// 下载上传）；nil 表示不变更。
	TGReuseEnabled *bool
	// DumpChannel 为缓存频道目标（@username / t.me 链接 / -100 数字 ID）；
	// nil 表示不变更；空串清除配置（复用关闭）。
	DumpChannel *string
	// 频道加入（/join）配置；nil 表示不变更，合并当前值后整体写入
	// （syscfg.JoinConfig 单一来源）。
	JoinEnabled           *bool
	JoinAutoLeaveExternal *bool
	JoinRequireApproval   *bool
	JoinMaxChannels       *int
	JoinMuteEnabled       *bool
	JoinArchiveEnabled    *bool
	// 监听源（/watch）配置；nil 表示不变更，合并当前值后整体写入
	//（syscfg.WatchConfig 单一来源）。
	WatchApplyEnabled    *bool
	WatchRequireApproval *bool
	WatchMaxSources      *int
	WatchPerUserLimit    *int
	// MaxRequestAttempts 为单个请求累计尝试上限（含首次）；nil 表示不变更。
	// 即时生效（syscfg 直查，重试校验与详情展示无缓存）。
	MaxRequestAttempts *int
	// BackupIntervalHours / BackupKeepCount 是自动备份配置；nil 表示不变更。
	// 即时生效（定时循环每 tick 重读 syscfg）。
	BackupIntervalHours    *int
	BackupKeepCount        *int
	DownloadThreads        *int
	UploadThreads          *int
	DownloadConnections    *int
	UploadConnections      *int
	ClearTransferOverrides []string
}

// settingsParamError 表示设置变更的参数拒绝：msg 是受控中文文案
// （API 回 400 JSON），不含底层错误细节。
type settingsParamError struct{ msg string }

func (e *settingsParamError) Error() string { return e.msg }

// settingsStoreError 携带存储失败发生阶段的操作名（仅进入服务日志）；
// Unwrap 保留底层错误以走统一错误链路。
type settingsStoreError struct {
	op  string
	err error
}

func (e *settingsStoreError) Error() string { return e.err.Error() }

func (e *settingsStoreError) Unwrap() error { return e.err }

func (s *Server) saveSettingValue(ctx context.Context, key, op string, value any) error {
	raw, err := json.Marshal(value)
	if err == nil {
		err = s.st.SetSetting(ctx, key, string(raw))
	}
	if err != nil {
		return &settingsStoreError{op: op, err: err}
	}
	return nil
}

// settingsApplyResult 汇总设置变更后的生效值；API 成功后重新读取展示。
type settingsApplyResult struct {
	Timezone      string
	DedupWindow   int
	QueueCapacity int
	QueueSame     bool
}

// applySettingsUpdate 应用运营设置变更（SPA API 核心）：
// 按时区 → 去重窗口 → 队列容量 → worker 数 → 媒体传输顺序逐项校验
// （IANA 时区名、去重窗口 1..1440 分钟、队列容量 1..4096、worker 数 1..16、
// 媒体 1MB–2000MB 且 stream_limit <= max_file_size）、写库并各自留审计；
// 空缺字段保持不变，值未变时跳过（不写审计）。时区与去重窗口
// 即时生效，队列容量、worker 数与媒体参数重启生效（审计 effect 字段一致）。
// 参数非法返回 *settingsParamError；存储失败返回底层错误（调用方统一转
// 500/503），前序已生效项不回滚。
func (s *Server) applySettingsUpdate(ctx context.Context, in settingsUpdateInput) (settingsApplyResult, error) {
	res := settingsApplyResult{
		Timezone:      s.access.TimezoneName(ctx),
		DedupWindow:   s.access.DedupWindowMinutes(ctx),
		QueueCapacity: s.loadQueueCapacity(ctx),
	}
	if s.queue != nil {
		res.QueueSame = s.queue.Cap() == res.QueueCapacity
	}

	// 时区（即时生效）
	if v := strings.TrimSpace(in.Timezone); v != "" && v != res.Timezone {
		if err := s.access.SetTimezone(ctx, v); err != nil {
			return res, &settingsParamError{"时区名称无效（须为 IANA 名称，如 Asia/Shanghai）。"}
		}
		s.audit(ctx, "settings.timezone", "settings", map[string]any{
			"before": res.Timezone, "after": v, "effect": "即时生效"})
		res.Timezone = v
	}

	// 去重窗口（即时生效）
	if v := strings.TrimSpace(in.DedupWindowRaw); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1440 {
			return res, &settingsParamError{"去重窗口必须为 1–1440 的整数分钟。"}
		}
		if n != res.DedupWindow {
			before := res.DedupWindow
			if err := s.access.SetDedupWindow(ctx, n); err != nil {
				return res, &settingsStoreError{op: "保存去重窗口", err: err}
			}
			s.audit(ctx, "settings.dedup_window", "settings", map[string]any{
				"before_min": before, "after_min": n, "effect": "即时生效"})
			res.DedupWindow = n
		}
	}

	// 单条 Bot 输入最大有效链接数（即时生效）
	if in.MaxLinksPerMessage != nil {
		n := *in.MaxLinksPerMessage
		if n < config.MinLinksPerMessage || n > config.MaxLinksPerMessage {
			return res, &settingsParamError{fmt.Sprintf("单次最大链接数必须为 %d–%d 的整数。", config.MinLinksPerMessage, config.MaxLinksPerMessage)}
		}
		current := LoadMaxLinksPerMessage(ctx, s.st, s.cfg.MaxLinksPerMessage)
		if n != current {
			if err := s.saveSettingValue(ctx, settingKeyMaxLinksPerMessage, "保存单次最大链接数", n); err != nil {
				return res, err
			}
			s.audit(ctx, "settings.max_links_per_message", "settings", map[string]any{
				"before": current, "after": n, "effect": "即时生效"})
		}
	}

	// 频道副本同步开关（即时生效）：关闭只暂停副本投递，绑定关系保留
	if in.ChannelCopyEnabled != nil {
		current := LoadChannelCopyEnabled(ctx, s.st)
		if current != *in.ChannelCopyEnabled {
			if err := s.saveSettingValue(ctx, settingKeyChannelCopyEnable, "保存频道同步开关", *in.ChannelCopyEnabled); err != nil {
				return res, err
			}
			s.audit(ctx, "settings.channel_copy_enabled", "settings", map[string]any{
				"before": current, "after": *in.ChannelCopyEnabled, "effect": "即时生效"})
		}
	}

	// TG 链接复用开关（即时生效）：关闭回到完整"下载+上传"链路，
	// 已落库的投递坐标保留（重开即恢复复用）
	if in.TGReuseEnabled != nil {
		current := LoadTGReuseEnabled(ctx, s.st)
		if current != *in.TGReuseEnabled {
			if err := s.saveSettingValue(ctx, settingKeyTGReuseEnable, "保存链接复用开关", *in.TGReuseEnabled); err != nil {
				return res, err
			}
			s.audit(ctx, "settings.tg_reuse_enabled", "settings", map[string]any{
				"before": current, "after": *in.TGReuseEnabled, "effect": "即时生效"})
		}
	}

	// 缓存频道（即时生效）：非空目标经 bot 解析校验（频道存在且 bot 可
	// 发帖）后保存数字 ID 与标题（保存的是数字 ID，频道随后公开转私有
	// 不影响使用）；空串清除配置（复用关闭）。settings 优先于环境变量
	// DUMP_CHANNEL_ID 兜底。
	if in.DumpChannel != nil {
		target := strings.TrimSpace(*in.DumpChannel)
		before := LoadEffectiveDumpChannelID(ctx, s.st, s.cfg.DumpChannelID)
		if target == "" {
			if before != 0 {
				if err := s.st.SetSetting(ctx, settingKeyDumpChannelID, "0"); err != nil {
					return res, &settingsStoreError{op: "清除缓存频道", err: err}
				}
				_ = s.st.SetSetting(ctx, settingKeyDumpChannelTitle, `""`)
				s.audit(ctx, "settings.dump_channel", "settings", map[string]any{
					"before": before, "after": int64(0), "effect": "即时生效"})
			}
		} else {
			if s.bindings == nil {
				return res, &settingsParamError{"频道服务未接入，无法校验缓存频道。"}
			}
			id, title, err := s.bindings.VerifyChannel(ctx, target)
			if err != nil {
				if apperr.From(err).Code == apperr.CodeChannelNotPostable {
					return res, &settingsParamError{"缓存频道校验失败：请确认频道存在且机器人已被设为管理员（多机器人部署时需把全部机器人都设为该频道的管理员；公开频道填 @用户名 或 t.me 链接，私有频道填 -100 数字 ID）。"}
				}
				return res, &settingsParamError{"缓存频道校验失败：" + apperr.UserText(apperr.From(err).Code)}
			}
			if id != before {
				if err := s.saveSettingValue(ctx, settingKeyDumpChannelID, "保存缓存频道", id); err != nil {
					return res, err
				}
				if err := s.saveSettingValue(ctx, settingKeyDumpChannelTitle, "保存缓存频道标题", title); err != nil {
					return res, err
				}
				s.audit(ctx, "settings.dump_channel", "settings", map[string]any{
					"before": before, "after": id, "title": title, "effect": "即时生效"})
			}
		}
	}

	// 频道加入配置（即时生效）：合并当前值整体写入；任一项变更即审计
	if in.JoinEnabled != nil || in.JoinAutoLeaveExternal != nil || in.JoinRequireApproval != nil ||
		in.JoinMaxChannels != nil || in.JoinMuteEnabled != nil || in.JoinArchiveEnabled != nil {
		before := syscfg.LoadJoinConfig(ctx, s.st)
		after := before
		changed := false
		if in.JoinEnabled != nil && *in.JoinEnabled != before.Enabled {
			after.Enabled = *in.JoinEnabled
			changed = true
		}
		if in.JoinAutoLeaveExternal != nil && *in.JoinAutoLeaveExternal != before.AutoLeaveExternal {
			after.AutoLeaveExternal = *in.JoinAutoLeaveExternal
			changed = true
		}
		if in.JoinRequireApproval != nil && *in.JoinRequireApproval != before.RequireApproval {
			after.RequireApproval = *in.JoinRequireApproval
			changed = true
		}
		if in.JoinMaxChannels != nil && *in.JoinMaxChannels != before.MaxChannels {
			after.MaxChannels = *in.JoinMaxChannels
			changed = true
		}
		if in.JoinMuteEnabled != nil && *in.JoinMuteEnabled != before.MuteEnabled {
			after.MuteEnabled = *in.JoinMuteEnabled
			changed = true
		}
		if in.JoinArchiveEnabled != nil && *in.JoinArchiveEnabled != before.ArchiveEnabled {
			after.ArchiveEnabled = *in.JoinArchiveEnabled
			changed = true
		}
		if changed {
			if err := syscfg.SaveJoinConfig(ctx, s.st, after); err != nil {
				return res, &settingsParamError{err.Error()}
			}
			s.audit(ctx, "settings.channel_join", "settings", map[string]any{
				"before": before, "after": after, "effect": "即时生效"})
		}
	}

	// 监听源配置（即时生效）：合并当前值整体写入；任一项变更即审计
	if in.WatchApplyEnabled != nil || in.WatchRequireApproval != nil ||
		in.WatchMaxSources != nil || in.WatchPerUserLimit != nil {
		before := syscfg.LoadWatchConfig(ctx, s.st)
		after := before
		changed := false
		if in.WatchApplyEnabled != nil && *in.WatchApplyEnabled != before.ApplyEnabled {
			after.ApplyEnabled = *in.WatchApplyEnabled
			changed = true
		}
		if in.WatchRequireApproval != nil && *in.WatchRequireApproval != before.RequireApproval {
			after.RequireApproval = *in.WatchRequireApproval
			changed = true
		}
		if in.WatchMaxSources != nil && *in.WatchMaxSources != before.MaxSources {
			after.MaxSources = *in.WatchMaxSources
			changed = true
		}
		if in.WatchPerUserLimit != nil && *in.WatchPerUserLimit != before.PerUserLimit {
			after.PerUserLimit = *in.WatchPerUserLimit
			changed = true
		}
		if changed {
			if err := syscfg.SaveWatchConfig(ctx, s.st, after); err != nil {
				return res, &settingsParamError{err.Error()}
			}
			s.audit(ctx, "settings.watch", "settings", map[string]any{
				"before": before, "after": after, "effect": "即时生效"})
		}
	}

	// 最大尝试次数（即时生效）：重试校验、详情展示与拒绝文案调用点直查，
	// 无进程内缓存
	if in.MaxRequestAttempts != nil {
		n := *in.MaxRequestAttempts
		if err := syscfg.ValidateMaxRequestAttempts(n); err != nil {
			return res, &settingsParamError{err.Error()}
		}
		current := syscfg.LoadMaxRequestAttempts(ctx, s.st)
		if n != current {
			if err := syscfg.SetMaxRequestAttempts(ctx, s.st, n); err != nil {
				return res, &settingsStoreError{op: "保存最大尝试次数", err: err}
			}
			s.audit(ctx, "settings.request_retry", "settings", map[string]any{
				"before": current, "after": n, "effect": "即时生效"})
		}
	}

	// 自动备份配置（即时生效：定时循环每 tick 重读 syscfg）
	if in.BackupIntervalHours != nil {
		n := *in.BackupIntervalHours
		if err := syscfg.ValidateBackupIntervalHours(n); err != nil {
			return res, &settingsParamError{err.Error()}
		}
		current := syscfg.LoadBackupIntervalHours(ctx, s.st)
		if n != current {
			if err := syscfg.SetBackupIntervalHours(ctx, s.st, n); err != nil {
				return res, &settingsStoreError{op: "保存自动备份间隔", err: err}
			}
			s.audit(ctx, "settings.backup_interval", "settings", map[string]any{
				"before": current, "after": n, "effect": "即时生效"})
		}
	}
	if in.BackupKeepCount != nil {
		n := *in.BackupKeepCount
		if err := syscfg.ValidateBackupKeepCount(n); err != nil {
			return res, &settingsParamError{err.Error()}
		}
		current := syscfg.LoadBackupKeepCount(ctx, s.st)
		if n != current {
			if err := syscfg.SetBackupKeepCount(ctx, s.st, n); err != nil {
				return res, &settingsStoreError{op: "保存备份保留份数", err: err}
			}
			s.audit(ctx, "settings.backup_keep", "settings", map[string]any{
				"before": current, "after": n, "effect": "即时生效"})
		}
	}

	// 队列容量（重启生效）
	if v := strings.TrimSpace(in.QueueCapRaw); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 4096 {
			return res, &settingsParamError{"队列容量必须为 1–4096 的整数。"}
		}
		if n != res.QueueCapacity {
			before := res.QueueCapacity
			if err := s.saveSettingValue(ctx, settingKeyQueueCapacity, "保存队列容量", n); err != nil {
				return res, err
			}
			s.audit(ctx, "settings.queue_capacity", "settings", map[string]any{
				"before": before, "after": n, "effect": "重启生效"})
			res.QueueCapacity = n
			if s.queue != nil {
				res.QueueSame = s.queue.Cap() == n
			}
		}
	}

	// 任务并发 worker 数（重启生效）：进程启动时经 LoadWorkerCount 覆盖 env 值
	if in.WorkerCount != nil {
		n := *in.WorkerCount
		if n < minWorkerCount || n > maxWorkerCount {
			return res, &settingsParamError{fmt.Sprintf("任务并发 worker 数必须为 %d–%d 的整数。", minWorkerCount, maxWorkerCount)}
		}
		current := LoadWorkerCount(ctx, s.st, s.cfg.WorkerCount)
		if n != current {
			if err := s.saveSettingValue(ctx, settingKeyWorkerCount, "保存任务并发 worker 数", n); err != nil {
				return res, err
			}
			s.audit(ctx, "settings.worker_count", "settings", map[string]any{
				"before": current, "after": n, "effect": "重启生效"})
		}
	}

	// 媒体传输参数（重启生效）：输入单位由调用方明确传入，内部只保存字节数。
	// 临时目录上限为独立字段（可单独填写），与 max/stream 共同做一次整体校验。
	mediaMax, mediaStream, mediaTempDir := s.loadMediaSettings(ctx)
	maxRaw, streamRaw := strings.TrimSpace(in.MaxFileSizeRaw), strings.TrimSpace(in.StreamLimitRaw)
	tempDirRaw := strings.TrimSpace(in.TempDirMaxSizeRaw)

	newMax, newStream, newTempDir := mediaMax, mediaStream, mediaTempDir
	if tempDirRaw != "" {
		parsed, err := parseMediaInput(tempDirRaw, in.TempDirMaxSizeUnit)
		if err != nil {
			return res, &settingsParamError{"临时目录最大大小必须是合法的 MB/GB 数值。"}
		}
		newTempDir = parsed
	}
	if maxRaw != "" || streamRaw != "" {
		if maxRaw == "" || streamRaw == "" {
			return res, &settingsParamError{"文件大小上限与流式阈值必须同时填写。"}
		}
		var err1, err2 error
		newMax, err1 = parseMediaInput(maxRaw, in.MaxFileSizeUnit)
		newStream, err2 = parseMediaInput(streamRaw, in.StreamLimitUnit)
		if err1 != nil || err2 != nil {
			return res, &settingsParamError{"媒体大小必须是合法的 MB/GB 数值。"}
		}
	}

	if maxRaw != "" || streamRaw != "" || tempDirRaw != "" {
		if err := config.ValidateMediaLimits(newMax, newStream, newTempDir); err != nil {
			return res, &settingsParamError{err.Error()}
		}
	}

	if newMax != mediaMax {
		if err := s.saveSettingValue(ctx, settingKeyMaxFileSize, "保存文件大小上限", newMax); err != nil {
			return res, err
		}
	}
	if newStream != mediaStream {
		if err := s.saveSettingValue(ctx, settingKeyStreamLimit, "保存流式阈值", newStream); err != nil {
			return res, err
		}
	}
	if newTempDir != mediaTempDir {
		if err := s.saveSettingValue(ctx, settingKeyTempDirMaxSize, "保存临时目录最大大小", newTempDir); err != nil {
			return res, err
		}
	}

	if maxRaw != "" || streamRaw != "" || tempDirRaw != "" {
		s.audit(ctx, "settings.media", "settings", map[string]any{
			"before_max_bytes": mediaMax, "after_max_bytes": newMax,
			"before_stream_bytes": mediaStream, "after_stream_bytes": newStream,
			"before_temp_dir_bytes": mediaTempDir, "after_temp_dir_bytes": newTempDir,
			"effect": "重启生效"})
	}

	// 内存预算（即时生效）：闸门在每次媒体打开前实时读库，保存后立即约束
	// 新打开的媒体；在途下载不受影响（软上限，随任务收尾自然回收）。
	if raw := strings.TrimSpace(in.MemoryBudgetRaw); raw != "" {
		parsed, err := parseMediaInput(raw, in.MemoryBudgetUnit)
		if err != nil {
			return res, &settingsParamError{"内存预算必须是合法的 MB/GB 数值。"}
		}
		if parsed < config.MinMemoryBudget || parsed > config.MaxMemoryBudget {
			return res, &settingsParamError{fmt.Sprintf("内存预算必须在 %dMB–%dGB 之间。",
				config.MinMemoryBudget>>20, config.MaxMemoryBudget>>30)}
		}
		current := LoadMemoryBudget(ctx, s.st, s.cfg.MemoryBudget)
		if parsed != current {
			if err := s.saveSettingValue(ctx, settingKeyMemoryBudget, "保存内存预算", parsed); err != nil {
				return res, err
			}
			s.audit(ctx, "settings.memory_budget", "settings", map[string]any{
				"before_bytes": current, "after_bytes": parsed, "effect": "即时生效"})
		}
	}

	// 传输并发是独立的运行时配置组：Set/Clear 在 Runtime 内以一个事务
	// 持久化并以一个原子快照发布，避免四个字段出现混合生效状态。
	if in.DownloadThreads != nil || in.UploadThreads != nil ||
		in.DownloadConnections != nil || in.UploadConnections != nil || len(in.ClearTransferOverrides) > 0 {
		if s.transfer == nil {
			return res, apperr.New(apperr.CodeStoreUnavailable, "传输运行时配置未接入")
		}
		set := make(map[transfercfg.Key]int)
		if in.DownloadThreads != nil {
			set[transfercfg.DownloadThreads] = *in.DownloadThreads
		}
		if in.UploadThreads != nil {
			set[transfercfg.UploadThreads] = *in.UploadThreads
		}
		if in.DownloadConnections != nil {
			set[transfercfg.DownloadConnections] = *in.DownloadConnections
		}
		if in.UploadConnections != nil {
			set[transfercfg.UploadConnections] = *in.UploadConnections
		}
		clear := make([]transfercfg.Key, 0, len(in.ClearTransferOverrides))
		for _, raw := range in.ClearTransferOverrides {
			clear = append(clear, transfercfg.Key(raw))
		}
		change, err := s.transfer.Apply(ctx, set, clear)
		if err != nil {
			if errors.Is(err, transfercfg.ErrUnknownKey) || errors.Is(err, transfercfg.ErrInvalidValue) || errors.Is(err, transfercfg.ErrSetClearConflict) {
				return res, &settingsParamError{"传输并发配置必须为 1–16 的整数，且不能同时设置和恢复同一项。"}
			}
			return res, &settingsStoreError{op: "保存传输并发配置", err: err}
		}
		if change.Changed {
			s.auditTransferChange(ctx, change)
		}
	}

	return res, nil
}

func transferAuditView(v transfercfg.View) map[string]any {
	field := func(f transfercfg.Field) map[string]any {
		return map[string]any{"effective": f.Effective, "env_default": f.EnvDefault, "overridden": f.Overridden}
	}
	return map[string]any{
		"download_threads": field(v.DownloadThreads), "upload_threads": field(v.UploadThreads),
		"download_connections": field(v.DownloadConnections), "upload_connections": field(v.UploadConnections),
	}
}

func (s *Server) auditTransferChange(ctx context.Context, change transfercfg.Change) {
	before, errBefore := json.Marshal(transferAuditView(change.Before))
	after, errAfter := json.Marshal(map[string]any{
		"values": transferAuditView(change.After), "effect": "即时生效",
	})
	if errBefore != nil || errAfter != nil {
		s.log.Warn("传输配置审计序列化失败", "error", errors.Join(errBefore, errAfter))
		return
	}
	if err := s.st.AppendAudit(ctx, store.AuditEntry{
		Actor: "admin", Action: "settings.transfer", Target: "settings",
		BeforeJSON: string(before), AfterJSON: string(after),
	}); err != nil {
		s.log.Warn("写传输配置审计失败", "error", err.Error())
	}
}

// ---- 数据库备份导出 ----

// backupExportError 携带导出失败阶段的操作名（仅进入服务日志）；
// Unwrap 保留底层错误以走统一错误链路。
type backupExportError struct {
	op  string
	err error
}

func (e *backupExportError) Error() string { return e.err.Error() }

func (e *backupExportError) Unwrap() error { return e.err }

// backupExportOp 提取导出失败发生的阶段名（仅进入服务日志；
// 缺省为整体动作名"导出数据库备份"）。
func backupExportOp(err error) string {
	var opErr *backupExportError
	if errors.As(err, &opErr) {
		return opErr.op
	}
	return "导出数据库备份"
}

// runBackupExport 生成一致快照并流式输出（/api/v1/backup/export 的核心）：
// VACUUM INTO 生成快照到随机临时文件（与数据库同目录，保证同分区与权限），
// 流式下载（文件名含日期）后删除临时文件；动作写审计并更新最近备份时间。
// 导出内容仅业务数据库，不含 session.json / peers.json。
// 失败时响应尚未写出，由调用方决定 JSON 呈现方式。
func (s *Server) runBackupExport(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	dir := filepath.Dir(s.dbPath)

	// VACUUM INTO 要求目标不存在：先占位随机名再让出
	tmp, err := os.CreateTemp(dir, branding.StoragePrefix+"-backup-*.tmp")
	if err != nil {
		return &backupExportError{op: "创建备份临时文件", err: apperr.Wrap(apperr.CodeInternal, err)}
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpName)

	if err := s.st.BackupTo(ctx, tmpName); err != nil {
		_ = os.Remove(tmpName)
		return &backupExportError{op: "生成数据库快照", err: err}
	}
	f, err := os.Open(tmpName)
	if err != nil {
		_ = os.Remove(tmpName)
		return &backupExportError{op: "打开数据库快照", err: apperr.Wrap(apperr.CodeInternal, err)}
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(tmpName) // 下载完成（或中断）后删除临时文件
	}()

	// 先落审计与最近备份时间（下载被中断也不丢记录）
	now := s.now()
	if raw, err := json.Marshal(now.UnixMilli()); err == nil {
		if err := s.st.SetSetting(ctx, settingKeyLastBackupAt, string(raw)); err != nil {
			s.log.Warn("记录最近备份时间失败", "error", err.Error())
		}
	}
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	s.audit(ctx, "backup.export", "store", map[string]any{"bytes": size})
	s.log.Info("数据库备份已导出", "bytes", size)

	name := branding.StoragePrefix + "-backup-" + now.In(s.tz(ctx)).Format("20060102-150405") + ".db"
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", `attachment; filename="`+name+`"`)
	h.Set("Content-Length", strconv.FormatInt(size, 10))
	// 备份含全部业务行为记录：禁止任何一层缓存（敏感文件不应被错误缓存）
	h.Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, f); err != nil {
		// 头已写出：客户端中断/断连，仅记日志（临时文件仍会被 defer 清理）
		s.log.Warn("备份数据流发送中断", "error", err.Error())
	}
	return nil
}
