package notify

import "slices"

// Event categories are stable policy keys used by notification settings.
const (
	CategorySystemAlert    = "system_alert"
	CategorySystemRecovery = "system_recovery"
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
