package notify

import "slices"

// Event categories are stable policy keys used by notification settings.
const (
	CategorySystemAlert    = "system_alert"
	CategorySystemRecovery = "system_recovery"
	// CategoryActivity 是逐次即时活动通知（登录、申请等）：不写事件中心、
	// 不受告警冷却约束，每次发生都独立投递；仍受通知策略按类别/事件控制。
	CategoryActivity = "activity"
)

// EventDefinition is the compile-time metadata for one event type.
type EventDefinition struct {
	Type             string `json:"type"`
	Category         string `json:"category"`
	TypeLabel        string `json:"type_label"`
	Severity         string `json:"severity"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	SupportsRecovery bool   `json:"supports_recovery"`
}

var eventCatalog = []EventDefinition{
	{Type: KeySessionOffline, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "MTProto 会话离线", Description: "MTProto 会话已离线，请在管理端重新登录。", SupportsRecovery: true},
	{Type: KeyBotSendFailures, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "Bot API 连续发送失败", Description: "Bot API 连续发送失败，请检查网络与 Bot 配置。", SupportsRecovery: true},
	{Type: KeyTaskFailures, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "任务连续失败", Description: "任务连续失败，请到管理端消息记录页查看失败原因。", SupportsRecovery: true},
	{Type: KeyStoreWriteFailed, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "数据库写入失败", Description: "数据库写入失败，请检查磁盘空间与数据库文件。"},
	{Type: KeyTempDirUsage, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityWarn, Title: "临时目录占用超限", Description: "临时目录占用超过配置阈值，请及时清理或扩容。", SupportsRecovery: true},
	{Type: KeyStartupRecovered, Category: CategorySystemRecovery, TypeLabel: "系统恢复", Severity: SeverityWarn, Title: "启动恢复中断任务", Description: "启动恢复发现上次运行遗留的未完成任务，已标记为中断失败。"},
	{Type: KeyMediaConfigInvalid, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "媒体配置无效", Description: "数据库中的媒体传输配置无效，当前暂使用环境配置；请在管理端修正后重启。"},
	{Type: KeyBotListInvalid, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityWarn, Title: "机器人列表配置无效", Description: "机器人列表配置损坏，文件中的机器人条目已忽略，请在管理端修正。"},
	{Type: KeyBotInitFailed, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "机器人接入失败", Description: "部分机器人接入失败，当前已跳过不可用机器人，请检查机器人配置。"},
	{Type: KeyBotPollConflict, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "机器人轮询冲突", Description: "机器人 Token 正被其他服务占用，当前无法接收新消息。", SupportsRecovery: true},
	{Type: KeyCloudUploadFailed, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "云盘任务连续失败", Description: "云盘下载任务连续失败，请到管理端消息记录页查看失败原因。", SupportsRecovery: true},
	{Type: KeyCloudConfigInvalid, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "云盘配置无效", Description: "云盘下载配置无效（文件损坏或默认目的地悬空），功能暂按未配置处理；请在管理端修正。"},
	{Type: KeyCloudDisabled, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "云盘功能不可用", Description: "云盘下载已开启但 rclone 不可用，/download 暂不可用；请安装或修复 rclone 后重试。", SupportsRecovery: true},
	{Type: KeyMTProtoBanned, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityCritical, Title: "用户号被封禁或会话被撤销", Description: "MTProto 用户号已被封禁或会话被撤销，请导出备份后清理会话文件并用新号扫码登录。", SupportsRecovery: true},
	{Type: KeyBotBanned, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityCritical, Title: "Bot Token 已失效", Description: "Bot Token 已失效（被封禁或撤销），该 Bot 已停用；请在 @BotFather 检查状态或替换 Token。", SupportsRecovery: true},
	{Type: KeyBackupFailed, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "备份执行失败", Description: "自动或手动备份执行失败，请检查磁盘空间与数据目录。", SupportsRecovery: true},
	{Type: KeySourceInaccessible, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "提取源频道无法访问", Description: "提取源频道无法访问（可能已被封禁、删除或读取账号未加入），相关任务已失败；请检查源频道状态。"},
	{Type: KeyWatchSourceUnavailable, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "监听源不可用", Description: "监听源无法访问（受理 bot 可能已被移出或源已被封禁），该源新消息将无法预热缓存；请在管理端确认源状态。", SupportsRecovery: true},
	{Type: KeyDumpChannelWriteFailed, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityWarn, Title: "缓存频道写入失败", Description: "缓存频道写入失败（可能已被封禁、bot 失去发帖权限或配置有误）；任务投递不受影响，但同链接暂无法秒回复用。", SupportsRecovery: true},
	{Type: KeyBindingChannelGone, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityError, Title: "绑定频道已失效", Description: "用户绑定的转发频道/群组已不存在或被封禁，已自动软解绑并私聊通知该用户；详情见管理端频道绑定页。"},
	{Type: KeyBindingChannelNoRights, Category: CategorySystemAlert, TypeLabel: "系统告警", Severity: SeverityWarn, Title: "Bot 失去绑定频道权限", Description: "机器人被移出用户绑定的频道/群组或失去发言权限，副本暂无法同步；未解绑，用户重新加回管理员后自愈。", SupportsRecovery: true},

	// 活动通知：逐次即时推送，不写事件中心（审计已有 audit_log 覆盖）。
	{Type: KeyWebAdminLogin, Category: CategoryActivity, TypeLabel: "活动通知", Severity: SeverityInfo, Title: "管理后台登录成功", Description: "有新的管理后台登录。"},
	{Type: KeyUserApplication, Category: CategoryActivity, TypeLabel: "活动通知", Severity: SeverityInfo, Title: "新用户申请", Description: "有用户提交了使用申请，等待审批。"},
	{Type: KeyChannelJoinRequest, Category: CategoryActivity, TypeLabel: "活动通知", Severity: SeverityInfo, Title: "频道加入申请", Description: "有用户提交频道邀请链接，进入待审批。"},
}

var eventDefinitions = func() map[string]EventDefinition {
	out := make(map[string]EventDefinition, len(eventCatalog))
	for _, definition := range eventCatalog {
		out[definition.Type] = definition
	}
	return out
}()

// Catalog returns a copy of the stable event catalog. Callers cannot mutate the
// package's compile-time definitions through the returned slice.
func Catalog() []EventDefinition {
	return slices.Clone(eventCatalog)
}

// GetDefinition returns the definition for eventType.
func GetDefinition(eventType string) (EventDefinition, bool) {
	definition, ok := eventDefinitions[eventType]
	return definition, ok
}
