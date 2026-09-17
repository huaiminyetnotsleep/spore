// Package notifycfg 管理通知通道配置、凭据加密和测试发送。
package notifycfg

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	// SettingKey 是通知配置在 settings KV 中使用的键。
	SettingKey = "notification_channels"

	ChannelBot     = "bot"
	ChannelWebhook = "webhook"

	FormatGeneric  = "generic"
	FormatFeishu   = "feishu"
	FormatDingTalk = "dingtalk"
	FormatDiscord  = "discord"
)

var (
	// ErrKeyUnavailable 表示通知凭据主密钥缺失或无效。
	ErrKeyUnavailable = errors.New("通知凭据加密密钥不可用")
	// ErrKeyMismatch 表示密文由另一把主密钥生成。
	ErrKeyMismatch = errors.New("通知凭据加密密钥不匹配")
	// ErrCredentialCorrupt 表示凭据信封或密文已损坏。
	ErrCredentialCorrupt = errors.New("通知凭据数据损坏")
)

// SettingsStore 是 Manager 所需的最小 settings KV 接口。
type SettingsStore interface {
	GetSetting(ctx context.Context, key string) (value string, ok bool, err error)
	SetSetting(ctx context.Context, key, valueJSON string) error
}

// Options 配置 Manager 的可注入依赖。
type Options struct {
	HTTPClient    *http.Client
	BotAPIBaseURL string
	Now           func() time.Time
}

// ConfigInput 是 PUT 全量替换通知配置的输入。两个通道会原子校验并保存。
type ConfigInput struct {
	Bot     BotInput     `json:"bot"`
	Webhook WebhookInput `json:"webhook"`
}

// BotInput 是 Telegram Bot 通道的保存输入。Token 为空时沿用已保存凭据。
type BotInput struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
	ChatID  string `json:"chat_id"`
}

// WebhookInput 是 Webhook 通道的保存输入。URL 和 Secret 为空时沿用已保存凭据。
type WebhookInput struct {
	Enabled bool   `json:"enabled"`
	Format  string `json:"format"`
	URL     string `json:"url"`
	Secret  string `json:"secret"`

	GenericSignatureHeader string   `json:"generic_signature_header,omitempty"`
	FeishuOpenIDs          []string `json:"feishu_open_ids,omitempty"`
	FeishuAtAll            bool     `json:"feishu_at_all,omitempty"`
	DingTalkMobiles        []string `json:"dingtalk_mobiles,omitempty"`
	DingTalkAtAll          bool     `json:"dingtalk_at_all,omitempty"`
	DiscordUserIDs         []string `json:"discord_user_ids,omitempty"`
	DiscordRoleIDs         []string `json:"discord_role_ids,omitempty"`
	DiscordEveryone        bool     `json:"discord_everyone,omitempty"`
}

// CredentialState 是脱敏视图中的凭据状态。
type CredentialState struct {
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`
}

// BotView 是 Bot 通道的脱敏读取视图。
type BotView struct {
	Enabled    bool            `json:"enabled"`
	ChatID     string          `json:"chat_id"`
	HasToken   bool            `json:"has_token"`
	Credential CredentialState `json:"credential"`
}

// WebhookView 是 Webhook 通道的脱敏读取视图。
type WebhookView struct {
	Enabled    bool            `json:"enabled"`
	Format     string          `json:"format"`
	HasURL     bool            `json:"has_url"`
	HasSecret  bool            `json:"has_secret"`
	Credential CredentialState `json:"credential"`

	GenericSignatureHeader string   `json:"generic_signature_header,omitempty"`
	FeishuOpenIDs          []string `json:"feishu_open_ids,omitempty"`
	FeishuAtAll            bool     `json:"feishu_at_all,omitempty"`
	DingTalkMobiles        []string `json:"dingtalk_mobiles,omitempty"`
	DingTalkAtAll          bool     `json:"dingtalk_at_all,omitempty"`
	DiscordUserIDs         []string `json:"discord_user_ids,omitempty"`
	DiscordRoleIDs         []string `json:"discord_role_ids,omitempty"`
	DiscordEveryone        bool     `json:"discord_everyone,omitempty"`
}

// View 是 notification_channels 的完整脱敏视图。
type View struct {
	Version int         `json:"version"`
	Bot     BotView     `json:"bot"`
	Webhook WebhookView `json:"webhook"`
}

// RecentChat 描述 getUpdates 中最近出现的会话。
type RecentChat struct {
	ID    string `json:"id"`
	Type  string `json:"type,omitempty"`
	Title string `json:"title,omitempty"`
}

// FieldDescriptor 描述某个格式可用的非敏感表单字段。
type FieldDescriptor struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// Descriptor 描述一个已注册的 Webhook 格式。
type Descriptor struct {
	Format         string            `json:"format"`
	Label          string            `json:"label"`
	SupportsSecret bool              `json:"supports_secret"`
	Fields         []FieldDescriptor `json:"fields,omitempty"`
}
