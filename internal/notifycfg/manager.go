package notifycfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/config"
)

const (
	documentVersion  = 1
	botChannelID     = "telegram_bot"
	webhookChannelID = "webhook"
)

type document struct {
	Version         int               `json:"version"`
	AutomaticEvents bool              `json:"automatic_events"`
	Channels        []json.RawMessage `json:"channels"`
}

type channelHeader struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Format string `json:"format,omitempty"`
}

type storedChannel[T any] struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Format string `json:"format,omitempty"`
	Config T      `json:"config"`
}

type storedBot struct {
	Enabled bool      `json:"enabled"`
	ChatID  string    `json:"chat_id"`
	Token   *envelope `json:"token,omitempty"`
}

type storedWebhook struct {
	Enabled bool      `json:"enabled"`
	URL     *envelope `json:"url,omitempty"`
	Secret  *envelope `json:"secret,omitempty"`

	GenericSignatureHeader string   `json:"generic_signature_header,omitempty"`
	FeishuOpenIDs          []string `json:"feishu_open_ids,omitempty"`
	FeishuAtAll            bool     `json:"feishu_at_all,omitempty"`
	DingTalkMobiles        []string `json:"dingtalk_mobiles,omitempty"`
	DingTalkAtAll          bool     `json:"dingtalk_at_all,omitempty"`
	DiscordUserIDs         []string `json:"discord_user_ids,omitempty"`
	DiscordRoleIDs         []string `json:"discord_role_ids,omitempty"`
	DiscordEveryone        bool     `json:"discord_everyone,omitempty"`
}

// Manager 管理版本化通知配置及使用已保存凭据的出站操作。
type Manager struct {
	store      SettingsStore
	crypt      cryptor
	client     *http.Client
	botAPIBase string
	now        func() time.Time
	saveMu     sync.Mutex
	policy     *PolicyManager
}

// NewManager 创建通知配置管理器。HTTPClient 为空时使用 10 秒总超时客户端。
func NewManager(store SettingsStore, rootKey []byte, opts Options) *Manager {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	base := strings.TrimRight(opts.BotAPIBaseURL, "/")
	if base == "" {
		base = "https://api.telegram.org"
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		store: store, crypt: newCryptor(rootKey), client: client, botAPIBase: base, now: now,
		policy: NewPolicyManager(store, now),
	}
}

// Descriptors 返回已注册 Webhook 格式的稳定描述符列表。
func Descriptors() []Descriptor {
	return webhookDescriptors()
}

// Snapshot 加载通知配置并返回不含任何敏感值的视图。
func (m *Manager) Snapshot(ctx context.Context) (View, error) {
	doc, err := m.load(ctx)
	if err != nil {
		return View{}, err
	}
	view := View{Version: documentVersion, AutomaticEvents: doc.AutomaticEvents}
	if bot, ok, err := findChannel[storedBot](doc, botChannelID, ChannelBot); err != nil {
		return View{}, err
	} else if ok {
		_, credErr := m.crypt.decrypt(bot.Config.Token, "bot.token")
		view.Bot = BotView{
			Enabled: bot.Config.Enabled, ChatID: bot.Config.ChatID,
			HasToken: bot.Config.Token != nil, Credential: credentialState(credErr),
		}
	} else {
		view.Bot.Credential = CredentialState{Available: true}
	}
	if webhook, ok, err := findChannel[storedWebhook](doc, webhookChannelID, ChannelWebhook); err != nil {
		return View{}, err
	} else if ok {
		_, urlErr := m.crypt.decrypt(webhook.Config.URL, "webhook.url")
		_, secretErr := m.crypt.decrypt(webhook.Config.Secret, "webhook.secret")
		credErr := urlErr
		if credErr == nil {
			credErr = secretErr
		}
		cfg := webhook.Config
		view.Webhook = WebhookView{
			Enabled: cfg.Enabled, Format: webhook.Format, HasURL: cfg.URL != nil,
			HasSecret: cfg.Secret != nil, Credential: credentialState(credErr),
			GenericSignatureHeader: cfg.GenericSignatureHeader,
			FeishuOpenIDs:          slices.Clone(cfg.FeishuOpenIDs), FeishuAtAll: cfg.FeishuAtAll,
			DingTalkMobiles: slices.Clone(cfg.DingTalkMobiles), DingTalkAtAll: cfg.DingTalkAtAll,
			DiscordUserIDs: slices.Clone(cfg.DiscordUserIDs), DiscordRoleIDs: slices.Clone(cfg.DiscordRoleIDs),
			DiscordEveryone: cfg.DiscordEveryone,
		}
	} else {
		view.Webhook.Credential = CredentialState{Available: true}
	}
	return view, nil
}

// Save 原子校验、加密并全量保存 Bot 与 Webhook 配置。任一通道失败时不写入 settings。
func (m *Manager) Save(ctx context.Context, input ConfigInput) error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	doc, err := m.load(ctx)
	if err != nil {
		return err
	}
	bot, err := m.buildBotChannel(doc, input.Bot)
	if err != nil {
		return err
	}
	webhook, err := m.buildWebhookChannel(doc, input.Webhook)
	if err != nil {
		return err
	}
	if err := replaceChannel(&doc, botChannelID, bot); err != nil {
		return err
	}
	if err := replaceChannel(&doc, webhookChannelID, webhook); err != nil {
		return err
	}
	doc.AutomaticEvents = input.AutomaticEvents
	return m.saveDocument(ctx, doc)
}

// SaveBot 校验并保存 Bot 配置；Token 为空时保留已有密文。
func (m *Manager) SaveBot(ctx context.Context, input BotInput) error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	doc, err := m.load(ctx)
	if err != nil {
		return err
	}
	channel, err := m.buildBotChannel(doc, input)
	if err != nil {
		return err
	}
	if err := replaceChannel(&doc, botChannelID, channel); err != nil {
		return err
	}
	return m.saveDocument(ctx, doc)
}

// SaveWebhook 校验并保存 Webhook 配置；URL/Secret 为空时保留已有密文。
func (m *Manager) SaveWebhook(ctx context.Context, input WebhookInput) error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()

	doc, err := m.load(ctx)
	if err != nil {
		return err
	}
	channel, err := m.buildWebhookChannel(doc, input)
	if err != nil {
		return err
	}
	if err := replaceChannel(&doc, webhookChannelID, channel); err != nil {
		return err
	}
	return m.saveDocument(ctx, doc)
}

func (m *Manager) buildBotChannel(doc document, input BotInput) (storedChannel[storedBot], error) {
	input.Token = strings.TrimSpace(input.Token)
	input.ChatID = strings.TrimSpace(input.ChatID)
	if input.Token != "" && !config.IsValidBotToken(input.Token) {
		return storedChannel[storedBot]{}, errors.New("Bot Token 格式无效")
	}
	if input.ChatID != "" && !validChatID(input.ChatID) {
		return storedChannel[storedBot]{}, errors.New("Chat ID 格式无效")
	}
	old, found, err := findChannel[storedBot](doc, botChannelID, ChannelBot)
	if err != nil {
		return storedChannel[storedBot]{}, err
	}
	cfg := storedBot{Enabled: input.Enabled, ChatID: input.ChatID}
	if found {
		cfg.Token = old.Config.Token
	}
	if input.Token != "" {
		cfg.Token, err = m.crypt.encrypt(input.Token, "bot.token")
		if err != nil {
			return storedChannel[storedBot]{}, secretSaveError(err)
		}
	}
	if input.Enabled && (cfg.Token == nil || cfg.ChatID == "") {
		return storedChannel[storedBot]{}, errors.New("启用 Bot 通知前请填写 Token 和 Chat ID")
	}
	return storedChannel[storedBot]{ID: botChannelID, Type: ChannelBot, Format: "telegram", Config: cfg}, nil
}

func (m *Manager) buildWebhookChannel(doc document, input WebhookInput) (storedChannel[storedWebhook], error) {
	input.Format = strings.ToLower(strings.TrimSpace(input.Format))
	adapter, ok := lookupWebhook(input.Format)
	if !ok {
		return storedChannel[storedWebhook]{}, errors.New("Webhook 格式不受支持")
	}
	input.URL = strings.TrimSpace(input.URL)
	input.Secret = strings.TrimSpace(input.Secret)
	if input.URL != "" {
		if err := validateWebhookURL(input.URL); err != nil {
			return storedChannel[storedWebhook]{}, err
		}
	}
	if input.Secret != "" && !adapter.descriptor.SupportsSecret {
		return storedChannel[storedWebhook]{}, errors.New("当前 Webhook 格式不支持签名密钥")
	}
	if err := validateWebhookFields(input); err != nil {
		return storedChannel[storedWebhook]{}, err
	}
	old, found, err := findChannel[storedWebhook](doc, webhookChannelID, ChannelWebhook)
	if err != nil {
		return storedChannel[storedWebhook]{}, err
	}
	cfg := storedWebhook{
		Enabled: input.Enabled, GenericSignatureHeader: strings.TrimSpace(input.GenericSignatureHeader),
		FeishuOpenIDs: cleanList(input.FeishuOpenIDs), FeishuAtAll: input.FeishuAtAll,
		DingTalkMobiles: cleanList(input.DingTalkMobiles), DingTalkAtAll: input.DingTalkAtAll,
		DiscordUserIDs: cleanList(input.DiscordUserIDs), DiscordRoleIDs: cleanList(input.DiscordRoleIDs),
		DiscordEveryone: input.DiscordEveryone,
	}
	if found {
		cfg.URL = old.Config.URL
		if adapter.descriptor.SupportsSecret {
			cfg.Secret = old.Config.Secret
		}
	}
	if input.URL != "" {
		cfg.URL, err = m.crypt.encrypt(input.URL, "webhook.url")
		if err != nil {
			return storedChannel[storedWebhook]{}, secretSaveError(err)
		}
	}
	if input.Secret != "" {
		cfg.Secret, err = m.crypt.encrypt(input.Secret, "webhook.secret")
		if err != nil {
			return storedChannel[storedWebhook]{}, secretSaveError(err)
		}
	}
	if input.Enabled && cfg.URL == nil {
		return storedChannel[storedWebhook]{}, errors.New("启用 Webhook 通知前请填写 HTTPS URL")
	}
	return storedChannel[storedWebhook]{ID: webhookChannelID, Type: ChannelWebhook, Format: input.Format, Config: cfg}, nil
}

// TestSaved 使用已保存配置向指定通道发送调用方提供的测试消息。
func (m *Manager) TestSaved(ctx context.Context, channel, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return errors.New("测试消息不能为空")
	}
	switch channel {
	case ChannelBot:
		bot, err := m.loadBotSecrets(ctx)
		if err != nil {
			return err
		}
		return m.sendBotMessage(ctx, bot.Token, bot.ChatID, message)
	case ChannelWebhook:
		cfg, err := m.loadWebhookSecrets(ctx)
		if err != nil {
			return err
		}
		adapter, _ := lookupWebhook(cfg.Format)
		return adapter.send(ctx, m.client, cfg, message, m.now())
	default:
		return errors.New("通知通道不受支持")
	}
}

// RecentBotChat 使用已保存 Bot Token 调用 getUpdates，返回最近会话。
func (m *Manager) RecentBotChat(ctx context.Context) (RecentChat, error) {
	bot, err := m.loadBotSecrets(ctx)
	if err != nil {
		return RecentChat{}, err
	}
	return m.getRecentChat(ctx, bot.Token)
}

type botSecrets struct {
	Token  string
	ChatID string
}

func (m *Manager) loadBotSecrets(ctx context.Context) (botSecrets, error) {
	doc, err := m.load(ctx)
	if err != nil {
		return botSecrets{}, err
	}
	stored, ok, err := findChannel[storedBot](doc, botChannelID, ChannelBot)
	if err != nil || !ok || stored.Config.Token == nil {
		if err != nil {
			return botSecrets{}, err
		}
		return botSecrets{}, errors.New("Bot 通知尚未配置")
	}
	token, err := m.crypt.decrypt(stored.Config.Token, "bot.token")
	if err != nil {
		return botSecrets{}, credentialUseError(err)
	}
	if stored.Config.ChatID == "" {
		return botSecrets{}, errors.New("Bot Chat ID 尚未配置")
	}
	return botSecrets{Token: token, ChatID: stored.Config.ChatID}, nil
}

type webhookSecrets struct {
	Format string
	URL    string
	Secret string
	Config storedWebhook
}

func (m *Manager) loadWebhookSecrets(ctx context.Context) (webhookSecrets, error) {
	doc, err := m.load(ctx)
	if err != nil {
		return webhookSecrets{}, err
	}
	stored, ok, err := findChannel[storedWebhook](doc, webhookChannelID, ChannelWebhook)
	if err != nil || !ok || stored.Config.URL == nil {
		if err != nil {
			return webhookSecrets{}, err
		}
		return webhookSecrets{}, errors.New("Webhook 通知尚未配置")
	}
	if _, ok := lookupWebhook(stored.Format); !ok {
		return webhookSecrets{}, errors.New("已保存的 Webhook 格式不受支持")
	}
	urlValue, err := m.crypt.decrypt(stored.Config.URL, "webhook.url")
	if err != nil {
		return webhookSecrets{}, credentialUseError(err)
	}
	if err := validateWebhookURL(urlValue); err != nil {
		return webhookSecrets{}, errors.New("已保存的 Webhook URL 无效")
	}
	secret, err := m.crypt.decrypt(stored.Config.Secret, "webhook.secret")
	if err != nil {
		return webhookSecrets{}, credentialUseError(err)
	}
	return webhookSecrets{Format: stored.Format, URL: urlValue, Secret: secret, Config: stored.Config}, nil
}

func (m *Manager) load(ctx context.Context) (document, error) {
	if m.store == nil {
		return document{}, errors.New("通知配置存储不可用")
	}
	raw, ok, err := m.store.GetSetting(ctx, SettingKey)
	if err != nil {
		return document{}, fmt.Errorf("读取通知配置失败: %w", err)
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return document{Version: documentVersion, Channels: []json.RawMessage{}}, nil
	}
	var doc document
	if err := json.Unmarshal([]byte(raw), &doc); err != nil || doc.Version != documentVersion || doc.Channels == nil {
		return document{}, errors.New("通知配置格式无效")
	}
	return doc, nil
}

func findChannel[T any](doc document, id, typ string) (storedChannel[T], bool, error) {
	for _, raw := range doc.Channels {
		var header channelHeader
		if json.Unmarshal(raw, &header) != nil {
			continue
		}
		if header.ID != id {
			continue
		}
		if header.Type != typ {
			return storedChannel[T]{}, true, errors.New("通知通道配置类型无效")
		}
		var channel storedChannel[T]
		if err := json.Unmarshal(raw, &channel); err != nil {
			return storedChannel[T]{}, true, errors.New("通知通道配置格式无效")
		}
		return channel, true, nil
	}
	return storedChannel[T]{}, false, nil
}

func replaceChannel(doc *document, id string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return errors.New("编码通知配置失败")
	}
	for i, existing := range doc.Channels {
		var header channelHeader
		if json.Unmarshal(existing, &header) == nil && header.ID == id {
			doc.Channels[i] = raw
			return nil
		}
	}
	doc.Channels = append(doc.Channels, raw)
	return nil
}

func (m *Manager) saveDocument(ctx context.Context, doc document) error {
	doc.Version = documentVersion
	encoded, err := json.Marshal(doc)
	if err != nil {
		return errors.New("编码通知配置失败")
	}
	if err := m.store.SetSetting(ctx, SettingKey, string(encoded)); err != nil {
		return fmt.Errorf("保存通知配置失败: %w", err)
	}
	return nil
}

func secretSaveError(err error) error {
	if errors.Is(err, ErrKeyUnavailable) {
		return errors.New("保存通知凭据需要配置有效的 WEB_OAUTH_ENCRYPTION_KEY")
	}
	return errors.New("加密通知凭据失败")
}

func credentialUseError(err error) error {
	switch {
	case errors.Is(err, ErrKeyUnavailable):
		return ErrKeyUnavailable
	case errors.Is(err, ErrKeyMismatch):
		return ErrKeyMismatch
	default:
		return ErrCredentialCorrupt
	}
}

func validateWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("Webhook URL 必须是无用户信息和片段的有效 HTTPS 地址")
	}
	return nil
}

func validChatID(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if r == '-' && i == 0 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != "-"
}

func cleanList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validateWebhookFields(input WebhookInput) error {
	header := strings.TrimSpace(input.GenericSignatureHeader)
	if len(header) > 128 {
		return errors.New("签名请求头名称过长")
	}
	if header != "" && !validHeaderName(header) {
		return errors.New("签名请求头名称无效")
	}
	for _, values := range [][]string{input.FeishuOpenIDs, input.DingTalkMobiles, input.DiscordUserIDs, input.DiscordRoleIDs} {
		if len(values) > 50 {
			return errors.New("提醒对象数量不能超过 50 个")
		}
	}
	return nil
}
