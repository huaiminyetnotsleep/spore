package notify

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// 事件严重级别（events.severity）。critical 是封禁类最高级：穿透静音计划
// 与最低严重级别门槛（见 notifycfg.Evaluate），只有渠道不可用或管理员
// 显式关闭该事件才能拦下。
const (
	SeverityCritical = "critical"
	SeverityError    = "error"
	SeverityWarn     = "warn"
	SeverityInfo     = "info"
)

// 事件去重键（events.key，命名 "<域>.<事件>"，跨重启稳定；
// 事件 message 一律使用受控中文描述，不拼入错误原文、链接或任何敏感值）。
const (
	KeySessionOffline     = "mtproto.session_offline"     // MTProto 会话离线，等待重连
	KeyBotSendFailures    = "botapi.send_failures"        // Bot API 连续发送失败
	KeyTaskFailures       = "tasks.consecutive_failures"  // 连续任务失败
	KeyStoreWriteFailed   = "store.write_failed"          // 任务终态等数据库写入失败
	KeyTempDirUsage       = "disk.temp_usage"             // 临时目录占用超过阈值
	KeyStartupRecovered   = "tasks.interrupted_recovered" // 启动恢复：中断任务批量标记失败
	KeyMediaConfigInvalid = "media.config_invalid"        // 数据库媒体覆盖值无效，暂回退环境配置
	KeyBotListInvalid     = "bot.list_invalid"            // bots.json 损坏，文件条目已忽略（仅 env 生效）
	KeyBotInitFailed      = "bot.init_failed"             // 有 bot token 接入失败（多机器人池跳过该 bot）
	KeyBotPollConflict    = "bot.poll_conflict"           // bot token 被其他服务占用（webhook/其他轮询实例），收不到新消息

	// 封禁类事件（Critical）：用户号被封 / 会话被撤销、bot token 失效。
	// 穿透静音计划推送（封禁是罕见高价值事件）；备份失败为 error 级。
	KeyMTProtoBanned = "mtproto.banned" // MTProto 用户号被封禁或会话被撤销（ErrorKind 见 payload）
	KeyBotBanned     = "bot.banned"     // Bot Token 失效（被封禁或撤销），该 bot 已标记停用
	KeyBackupFailed  = "backup.failed"  // 自动/CLI 备份执行失败（含磁盘空间不足跳过）

	// 云盘下载（/download）事件源。
	KeyCloudUploadFailed  = "cloud.upload_failed"  // 云盘任务连续失败（独立计数）
	KeyCloudConfigInvalid = "cloud.config_invalid" // 云盘配置损坏或默认目的地悬空
	KeyCloudDisabled      = "cloud.disabled"       // 已开启云盘但 rclone 二进制不可用

	// 活动通知（CategoryActivity）：逐次即时推送，不写事件中心、无冷却。
	KeyWebAdminLogin      = "web.admin_login"      // 管理后台登录成功
	KeyUserApplication    = "user.application"     // 新用户申请（/start 落 pending）
	KeyChannelJoinRequest = "channel.join_request" // 频道加入申请（/join 进入待审批）
)

// 事件源默认参数（可经环境变量覆盖，见 internal/config）。
const (
	DefaultCooldown           = 30 * time.Minute // 同一事件两次通知的最小间隔
	DefaultBotFailThreshold   = 3                // Bot API 连续发送失败告警阈值
	DefaultTaskFailThreshold  = 5                // 连续任务失败告警阈值
	DefaultCloudFailThreshold = 3                // 云盘任务连续失败告警阈值
	DefaultDiskLimitBytes     = int64(1) << 30   // 临时目录占用告警阈值（1GB）
	diskCheckInterval         = time.Minute      // 临时目录占用检查的最小间隔（抽样节流）
	raiseTimeout              = 10 * time.Second // 单次事件落库/通知的时间上限（不阻塞调用方过久）
)

// Notifier 是 Hub 对通知通道的最小依赖（delivery.Sender 天然满足）。
// 单独定义便于用假实现做确定性单测，也避免 Hub 依赖完整投递接口。
type Notifier interface {
	// SendMessage 以 HTML parse_mode 发送文本（语义与 delivery.Sender 一致）。
	SendMessage(ctx context.Context, chatID int64, html string) (int, error)
}

// Options 聚合 Hub 依赖；Store 为必填，其余缺省取默认值。
type Options struct {
	Store              *store.Store // 必填：业务数据库（events / users / audit_log）
	Log                *slog.Logger
	Cooldown           time.Duration                   // 同一事件两次通知的最小间隔；<=0 回落默认 30 分钟
	BotFailThreshold   int                             // Bot API 连续发送失败阈值；<=0 回落默认 3
	TaskFailThreshold  int                             // 连续任务失败阈值；<=0 回落默认 5
	CloudFailThreshold int                             // 云盘任务连续失败阈值；<=0 回落默认 3
	TempDir            string                          // 临时媒体目录（占用检查目标；空串关闭该检查）
	DiskLimitBytes     int64                           // 临时目录占用阈值（字节）；<=0 回落默认 1GB
	DirUsage           func(dir string) (int64, error) // 可注入的目录占用函数（测试用）
	Timezone           func() *time.Location           // 通知时间渲染时区（运营时区；nil 回落 GMT+8）
	Now                func() time.Time                // 可注入时钟（测试用）；缺省 time.Now
}

// Hub 是事件中心：事件写库去重合并 + 冷却窗口内的配置化或兼容 Telegram 管理员通知。
// 零值不可用，经 New 构造；全部方法并发安全。
type Hub struct {
	st                 *store.Store
	log                *slog.Logger
	cooldown           time.Duration
	botFailThreshold   int
	taskFailThreshold  int
	cloudFailThreshold int
	tempDir            string
	diskLimit          int64
	dirUsage           func(dir string) (int64, error)
	tzMu               sync.RWMutex
	timezone           func() *time.Location // 通知时间渲染时区；nil 回落 GMT+8
	now                func() time.Time

	payloadMu sync.RWMutex
	payloads  map[string]any // 各告警事件最近一次 payload（合并后仍展示最新关键信息）

	senderMu sync.RWMutex
	sender   Notifier // 兼容旧业务 Bot owner 私聊通道；nil 表示通道不可用

	runtimeMu sync.RWMutex
	runtime   RuntimeNotifier // 配置化通知通道；由装配层按需注入

	notifyMu sync.Mutex // 串行化通知判定与写回，避免并发 Raise 重复推送

	countMu    sync.Mutex
	botFails   int // Bot API 连续发送失败计数（成功清零）
	taskFails  int // 连续任务失败计数（成功清零）
	cloudFails int // 云盘任务连续失败计数（成功清零；与 TG 任务计数独立）

	diskMu        sync.Mutex
	lastDiskCheck time.Time // 上次临时目录占用检查时间（节流用）
}

// New 创建事件中心；Store 为必填。
func New(opt Options) (*Hub, error) {
	if opt.Store == nil {
		return nil, errors.New("notify: Store 为必填项")
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	cooldown := opt.Cooldown
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	botFail := opt.BotFailThreshold
	if botFail <= 0 {
		botFail = DefaultBotFailThreshold
	}
	taskFail := opt.TaskFailThreshold
	if taskFail <= 0 {
		taskFail = DefaultTaskFailThreshold
	}
	cloudFail := opt.CloudFailThreshold
	if cloudFail <= 0 {
		cloudFail = DefaultCloudFailThreshold
	}
	diskLimit := opt.DiskLimitBytes
	if diskLimit <= 0 {
		diskLimit = DefaultDiskLimitBytes
	}
	usage := opt.DirUsage
	if usage == nil {
		usage = dirSize
	}
	return &Hub{
		st: opt.Store, log: opt.Log,
		cooldown: cooldown, botFailThreshold: botFail, taskFailThreshold: taskFail,
		cloudFailThreshold: cloudFail,
		tempDir:            opt.TempDir, diskLimit: diskLimit, dirUsage: usage, now: now,
		timezone: opt.Timezone, payloads: make(map[string]any),
	}, nil
}

// SetTimezone 注入通知时间渲染时区（装配层接入运营时区设置；变更即生效）。
func (h *Hub) SetTimezone(fn func() *time.Location) {
	h.tzMu.Lock()
	defer h.tzMu.Unlock()
	h.timezone = fn
}

// snapshotTimezone 返回当前时区函数快照；未注入时回落固定 GMT+8。
func (h *Hub) snapshotTimezone() func() *time.Location {
	h.tzMu.RLock()
	defer h.tzMu.RUnlock()
	if h.timezone != nil {
		return h.timezone
	}
	return func() *time.Location { return gmt8Zone }
}

// SetSender 注入通知通道（Bot 就绪后由装配层调用；MTProto 每轮重连都会以
// 新 Bot 重新注入，与 Raise 的读取并发，因此以读写锁保护，取快照后发送）。
// 注入后立即补发冷却窗口外仍未通知的 open 事件：Bot API 不可用期间
// 只入 Web 事件中心的事件，在通道恢复后尽快到达管理员。
func (h *Hub) SetSender(snd Notifier) {
	h.senderMu.Lock()
	h.sender = snd
	h.senderMu.Unlock()
	// 通道刚恢复，用独立于调用方生命周期的短窗口补发（失败仅记日志）
	ctx, cancel := context.WithTimeout(context.Background(), raiseTimeout)
	defer cancel()
	h.FlushPending(ctx)
}

// SetRuntimeNotifier 注入通知设置页配置的运行时通道。
// 配置通道可在业务 Bot 尚未就绪时独立投递；注入后补发仍未成功通知的事件。
func (h *Hub) SetRuntimeNotifier(snd RuntimeNotifier) {
	h.runtimeMu.Lock()
	h.runtime = snd
	h.runtimeMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), raiseTimeout)
	defer cancel()
	h.FlushPending(ctx)
}

// snapshotSender 返回当前兼容 owner 私聊通道快照（可能为 nil：Bot 尚未就绪）。
func (h *Hub) snapshotSender() Notifier {
	h.senderMu.RLock()
	defer h.senderMu.RUnlock()
	return h.sender
}

func (h *Hub) snapshotRuntimeNotifier() RuntimeNotifier {
	h.runtimeMu.RLock()
	defer h.runtimeMu.RUnlock()
	return h.runtime
}

// Raise 记录（或按 key 合并）一次事件，并在冷却窗口外尝试通知管理员：
//   - data 是本次事件的关键信息 payload（见 data.go），渲染为推送消息正文；
//     内存保留每 key 最近一次 payload（事件合并后仍展示最新关键信息），
//     payload 不写入 events 表（事件中心文案仍为目录受控描述）；
//   - 事件写入失败只记日志——事件中心不得阻塞主链路；
//   - 通知成功才推进冷却期（MarkEventNotified）；失败只记日志并保留未通知状态，
//     下次事件发生或通道恢复时可再次尝试，不让失败阻塞事件写入；
//   - Bot 未就绪或未设置 owner 时仅入库，待 SetSender 补发。
//
// Raise 内部把 ctx 剥离取消信号并限时：事件落库属于"尽力完成的收尾写"，
// 进程退出路径上最后一次告警不应因 ctx 已取消而丢失（上限 raiseTimeout 防挂死）。
func (h *Hub) Raise(ctx context.Context, key, severity string, data any) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), raiseTimeout)
	defer cancel()
	if data != nil {
		h.payloadMu.Lock()
		h.payloads[key] = data
		h.payloadMu.Unlock()
	}
	if err := h.st.UpsertEvent(ctx, store.Event{
		Key: key, Severity: severity, Message: controlledEventMessage(key),
	}); err != nil {
		h.log.Error("写入事件失败", "key", key, "error", err.Error())
		return
	}
	e, err := h.st.GetEvent(ctx, key)
	if err != nil {
		h.log.Error("读取事件失败", "key", key, "error", err.Error())
		return
	}
	h.maybeNotify(ctx, e)
}

// maybeNotify 按冷却窗口决定是否发送：last_notified_at 距今不足冷却期则跳过
// （resolved 重开时 last_notified_at 已清零，恢复后再次发生必然重新通知）。
func (h *Hub) maybeNotify(ctx context.Context, e store.Event) {
	h.notifyMu.Lock()
	defer h.notifyMu.Unlock()

	// Raise 的查询发生在锁外；重新读取确保并发 Raise 不使用旧的
	// last_notified_at 快照而重复推送。
	current, err := h.st.GetEvent(ctx, e.Key)
	if err != nil {
		h.log.Warn("读取通知事件状态失败", "key", e.Key, "error", err.Error())
		return
	}
	if current.Status != store.EventOpen {
		return
	}
	now := h.now()
	if current.LastNotifiedAt > 0 && now.UnixMilli()-current.LastNotifiedAt < h.cooldown.Milliseconds() {
		return
	}
	h.deliver(ctx, current, now)
}

// deliver 执行一次通知尝试；只有至少一个实际通道发送成功才推进冷却期。
// 事件本身始终先落库，不受策略抑制影响。
func (h *Hub) deliver(ctx context.Context, e store.Event, now time.Time) {
	message := h.eventNotification(ctx, e, false)
	if h.sendViaChannels(ctx, e.Key, message) {
		if err := h.st.MarkEventNotified(ctx, e.Key, now.UnixMilli()); err != nil {
			h.log.Warn("记录事件通知时间失败", "key", e.Key, "error", err.Error())
		}
	}
}

// sendViaChannels 投递一条通知：配置化运行时通道启用后由它接管（Managed），
// 未启用时回退到旧的 owner 私聊，保持旧部署的行为兼容。
// Managed 但全部失败/被策略抑制时不回退（用户显式配置优先），返回 false 由
// 调用方决定后续（告警保留未通知状态待重试；活动通知直接丢弃）。
func (h *Hub) sendViaChannels(ctx context.Context, key string, message EventNotification) bool {
	if runtime := h.snapshotRuntimeNotifier(); runtime != nil {
		result, err := runtime.NotifyEvent(ctx, message)
		if err != nil {
			h.log.Warn("配置化通知通道读取失败，将尝试兼容通知", "key", key, "error", err.Error())
		}
		if result.Managed {
			if result.Delivered > 0 {
				h.log.Info("已通过配置化通知通道推送事件", "key", key,
					"delivered", result.Delivered, "failed", result.Failed)
				return true
			}
			if result.Failed > 0 {
				h.log.Warn("配置化通知通道发送失败，将在后续事件中重试", "key", key,
					"failed", result.Failed)
			}
			return false
		}
	}

	snd := h.snapshotSender()
	if snd == nil {
		// Bot 未就绪：告警事件保留在 Web 事件中心，SetSender 时补发；
		// 活动通知无落库留痕，直接跳过（审计日志另有留痕）。
		h.log.Info("通知通道未就绪，通知跳过", "key", key)
		return false
	}
	// 管理员 Chat ID 取 users 表 owner 用户的 Telegram ID（Bot 私聊 chat_id
	// 与用户 ID 相同）：不新增环境变量，Web 变更 owner 后下次发送即生效。
	chatID, err := h.st.OwnerID(ctx)
	if errors.Is(err, store.ErrNotFound) {
		h.log.Warn("未设置 owner 用户，事件通知暂无法投递", "key", key)
		return false
	}
	if err != nil {
		h.log.Error("查询管理员失败", "key", key, "error", err.Error())
		return false
	}
	if _, err := snd.SendMessage(ctx, chatID, RenderHTML(message)); err != nil {
		h.log.Warn("管理员通知发送失败，将在后续事件中重试", "key", key, "error", err.Error())
		return false
	}
	h.log.Info("已向管理员推送事件通知", "key", key)
	return true
}

// pushActivity 投递一条活动通知：逐次即时推送，不写事件中心、无冷却/去重；
// 投递失败只记日志（活动通知有时效性，不做补发，审计日志已有留痕）。
func (h *Hub) pushActivity(ctx context.Context, key string, payload any) {
	definition, ok := GetDefinition(key)
	if !ok {
		return // 目录外事件不投递，防止误用
	}
	occurred := h.now()
	message := EventNotification{
		SourceName: syscfg.Name(ctx, h.st), TypeCode: definition.Category,
		TypeLabel: definition.TypeLabel, Category: definition.Category,
		Severity: definition.Severity, EventType: key, Title: definition.Title,
		Body: renderEventBody(key, templateData{
			Time: h.formatTime(occurred), Payload: payload,
		}),
		Count: 1, OccurredAt: occurred, Activity: true,
	}
	h.sendViaChannels(ctx, key, message)
}

// pushActivityDetached 为活动通知剥离调用方 ctx 的取消信号并限时（活动通知
// 由请求路径异步触发，handler 返回后仍需完成发送）。
func (h *Hub) pushActivityDetached(ctx context.Context, key string, payload any) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), raiseTimeout)
	defer cancel()
	h.pushActivity(ctx, key, payload)
}

// AdminLogin 推送一次管理后台登录成功活动通知（web.admin_login）。
func (h *Hub) AdminLogin(ctx context.Context, d AdminLoginData) {
	h.pushActivityDetached(ctx, KeyWebAdminLogin, d)
}

// UserApplied 推送一次新用户申请活动通知（user.application）。
func (h *Hub) UserApplied(ctx context.Context, d UserApplicationData) {
	h.pushActivityDetached(ctx, KeyUserApplication, d)
}

// ChannelJoinRequested 推送一次频道加入申请活动通知（channel.join_request）。
func (h *Hub) ChannelJoinRequested(ctx context.Context, d ChannelJoinRequestData) {
	h.pushActivityDetached(ctx, KeyChannelJoinRequest, d)
}

// FlushPending 补发冷却窗口外仍未成功通知的 open 事件（最近发生的先发）。
// SetSender 注入通道后自动调用；发送失败只记日志，不阻塞调用方。
func (h *Hub) FlushPending(ctx context.Context) {
	events, err := h.st.ListEvents(ctx)
	if err != nil {
		h.log.Warn("补发事件通知失败", "error", err.Error())
		return
	}
	for _, e := range events {
		if e.Status != store.EventOpen {
			continue
		}
		h.maybeNotify(ctx, e)
	}
}

// Resolve 把事件标记为已解决并写审计（管理员人工操作，actor=admin）。
// 解决后同一事件再次发生会重开为 open 并清空冷却期（可重新通知）。
func (h *Hub) Resolve(ctx context.Context, id int64) error {
	if err := h.st.ResolveEvent(ctx, id); err != nil {
		return err
	}
	return h.st.AppendAudit(ctx, store.AuditEntry{
		Actor: "admin", Action: "event.resolve", Target: fmt.Sprintf("event:%d", id),
	})
}

// Recover 在系统自动恢复后把事件标记为已解决（如 MTProto 重连成功、
// 临时目录占用回落），写审计（actor=system）；事件不存在（从未发生）时静默忽略。
func (h *Hub) Recover(ctx context.Context, key string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), raiseTimeout)
	defer cancel()
	err := h.st.ResolveEventByKey(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return // 该事件从未发生，无需解决
	}
	if err != nil {
		h.log.Warn("自动解决事件失败", "key", key, "error", err.Error())
		return
	}
	if err := h.st.AppendAudit(ctx, store.AuditEntry{
		Actor: "system", Action: "event.recover", Target: key,
	}); err != nil {
		h.log.Warn("写事件恢复审计失败", "key", key, "error", err.Error())
	}
}

// ---- 事件源计数（由 worker / 发送包装喂入）----

// TaskResult 记录一次任务结果：连续失败达到阈值时产生（或合并）事件，
// 成功清零。达到阈值后仍持续失败时每次都合并进同一事件（count 累计），
// 通知仍受冷却窗口约束。detail 为最近一次失败任务的可读上下文（来源链接）。
func (h *Hub) TaskResult(ctx context.Context, succeeded bool, detail string) {
	h.recordStreak(ctx, succeeded, &h.taskFails, h.taskFailThreshold, KeyTaskFailures, detail)
}

// BotSendResult 记录一次 Bot API 发送结果：连续失败达到阈值时产生（或合并）事件，
// 成功清零。语义与 TaskResult 一致。
func (h *Hub) BotSendResult(ctx context.Context, succeeded bool) {
	h.recordStreak(ctx, succeeded, &h.botFails, h.botFailThreshold, KeyBotSendFailures, "")
}

// CloudResult 记录一次云盘任务（/download）结果：独立于 TG 任务的连续失败
// 计数，达到阈值时产生（或合并）cloud.upload_failed 事件，
// 成功清零并自动恢复（管理员 TG 通知 + 事件页）。detail 语义同 TaskResult。
func (h *Hub) CloudResult(ctx context.Context, succeeded bool, detail string) {
	h.recordStreak(ctx, succeeded, &h.cloudFails, h.cloudFailThreshold, KeyCloudUploadFailed, detail)
}

// CloudConfigInvalid 记录云盘配置损坏或默认目的地悬空（启动加载或保存后
// 校验发现）。事件按 key 去重合并，通知受冷却窗口约束。
func (h *Hub) CloudConfigInvalid(ctx context.Context) {
	h.Raise(ctx, KeyCloudConfigInvalid, SeverityError, "")
}

// CloudDisabled 记录"云盘下载已开启但 rclone 二进制不可用"（启动探测与
// 低频周期复查触发；功能按禁用处理，/download 回暂不可用）。
func (h *Hub) CloudDisabled(ctx context.Context) {
	h.Raise(ctx, KeyCloudDisabled, SeverityError, "")
}

// CloudDisabledRecovered 在 rclone 探测恢复可用后自动解决 cloud.disabled
// 事件（事件从未发生时为静默 no-op）。
func (h *Hub) CloudDisabledRecovered(ctx context.Context) {
	h.Recover(ctx, KeyCloudDisabled)
}

// StoreWriteFailed 记录一次数据库写入失败；scene 为写入场景的可读标签。
func (h *Hub) StoreWriteFailed(ctx context.Context, scene string) {
	h.Raise(ctx, KeyStoreWriteFailed, SeverityError, StoreWriteData{Scene: scene})
}

// CheckTempDir 抽样检查临时目录占用：超过阈值时产生（或合并）事件，
// 回落到阈值以下时自动解决对应事件。
//
// 取舍说明：选择"任务处理路径低频抽样"而非定时轮询——本项目不引入定时任务
// 机制，事件频度随任务流量自然伸缩，且检查在 Hub 内部
// 按 diskCheckInterval 节流，不会对每个任务重复遍历目录。
func (h *Hub) CheckTempDir(ctx context.Context) {
	if h.tempDir == "" {
		return
	}
	h.diskMu.Lock()
	now := h.now()
	if !h.lastDiskCheck.IsZero() && now.Sub(h.lastDiskCheck) < diskCheckInterval {
		h.diskMu.Unlock()
		return
	}
	h.lastDiskCheck = now
	h.diskMu.Unlock()

	used, err := h.dirUsage(h.tempDir)
	if err != nil {
		h.log.Debug("临时目录占用检查失败", "error", err.Error())
		return
	}
	if used > h.diskLimit {
		h.Raise(ctx, KeyTempDirUsage, SeverityWarn, TempUsageData{Used: used, Limit: h.diskLimit})
		return
	}
	// 未超限：若历史事件仍处于 open（如占用刚回落、或上次进程中断遗留），自动恢复
	h.Recover(ctx, KeyTempDirUsage)
}

// controlledEventMessage 返回事件中心允许对外展示的固定中文文案。
// 事件消息不接收底层错误、链接、消息正文或凭据，避免通知和 Web 页面越界泄露。
func controlledEventMessage(key string) string {
	if definition, ok := GetDefinition(key); ok {
		return definition.Description
	}
	return "系统异常事件，请查看管理端事件中心。"
}

func (h *Hub) eventNotification(ctx context.Context, e store.Event, recovery bool) EventNotification {
	definition, ok := GetDefinition(e.Key)
	if !ok {
		definition = EventDefinition{
			Type: e.Key, Category: CategorySystemAlert, TypeLabel: "系统告警",
			Severity: e.Severity, Title: "系统异常", Description: controlledEventMessage(e.Key),
		}
	}
	severity := e.Severity
	if severity == "" {
		severity = definition.Severity
	}
	occurred := time.UnixMilli(e.LastAt)
	return EventNotification{
		SourceName: syscfg.Name(ctx, h.st), TypeCode: definition.Category,
		TypeLabel: definition.TypeLabel, Category: definition.Category,
		Severity: severity, EventType: e.Key, Title: definition.Title,
		Body: renderEventBody(e.Key, templateData{
			Time:    h.formatTime(occurred),
			Payload: h.snapshotPayload(e.Key),
		}),
		Count: e.Count, OccurredAt: occurred,
		Recovery: recovery,
	}
}

// snapshotPayload 返回事件最近一次 payload 快照（事件合并后仍展示最新关键信息）。
func (h *Hub) snapshotPayload(key string) any {
	h.payloadMu.RLock()
	defer h.payloadMu.RUnlock()
	return h.payloads[key]
}

// formatTime 把时间渲染为通知正文用的人可读形式：运营时区的
// "2006/01/02 15:04（GMT+8）"样式，GMT 偏移按实际时区动态生成。
func (h *Hub) formatTime(t time.Time) string {
	loc := h.snapshotTimezone()()
	t = t.In(loc)
	_, offset := t.Zone()
	sign, hours, minutes := "+", offset/3600, offset%3600/60
	if offset < 0 {
		sign, hours, minutes = "-", -hours, -minutes
	}
	label := fmt.Sprintf("GMT%s%d", sign, hours)
	if minutes > 0 {
		label = fmt.Sprintf("GMT%s%d:%02d", sign, hours, minutes)
	}
	return fmt.Sprintf("%s（%s）", t.Format("2006/01/02 15:04"), label)
}

// notifyHTML 渲染通知正文（HTML parse_mode）：只使用事件目录中的受控文案、
// 系统名称和计数，不含链接、错误原文、消息正文或任何敏感值。
func (h *Hub) notifyHTML(ctx context.Context, e store.Event) string {
	return RenderHTML(h.eventNotification(ctx, e, false))
}

// humanBytes 把字节数格式化为人类可读大小（事件文案用）。
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	value := float64(n)
	units := []string{"KB", "MB", "GB", "TB"}
	for _, u := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f%s", value, u)
		}
	}
	return fmt.Sprintf("%.1fPB", value/unit)
}

// dirSize 汇总目录树的总字节数（默认占用函数）；目录不可读时返回错误，
// 由调用方决定跳过本轮检查。符号链接不跟随（临时目录内不存在）。
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // 目录不可读等结构性错误：跳过本轮检查
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // 竞态删除的临时文件：跳过不计
		}
		total += info.Size()
		return nil
	})
	return total, err
}
