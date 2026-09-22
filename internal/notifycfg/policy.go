package notifycfg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/huaiminyetnotsleep/spore/internal/notify"
)

const (
	SettingKeyPolicy = "notification_policy"
	PolicyVersion    = 1

	OverrideInherit  = "inherit"
	OverrideEnabled  = "enabled"
	OverrideDisabled = "disabled"

	ChannelAdminBadge = "admin_badge"

	MuteMatchAll      = "all"
	MuteMatchCategory = "category"
	MuteMatchEvents   = "events"

	MaxMuteSchedules        = 100
	MaxMuteEventTypes       = 50
	MaxMuteNameRunes        = 100
	maxMuteIDLength         = 64
	maxPolicyEventOverrides = 200
)

var ErrMuteNotFound = errors.New("静音计划不存在")

// ValidationError marks a request value that failed policy validation. Its
// message is controlled and safe for an API response; storage and decode
// failures intentionally use ordinary errors and are mapped to 5xx.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "通知策略参数无效"
	}
	return e.Message
}

func policyValidation(message string) error {
	return &ValidationError{Message: message}
}

func policyValidationf(format string, args ...any) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

// CategoryPolicy controls the default notification channels for a category.
type CategoryPolicy struct {
	AdminBadge bool `json:"admin_badge"`
	Bot        bool `json:"bot"`
	Webhook    bool `json:"webhook"`
}

// EventPolicy overrides a category default for one event. Recovery controls
// whether a future recovery notification is eligible.
type EventPolicy struct {
	AdminBadge string `json:"admin_badge"`
	Bot        string `json:"bot"`
	Webhook    string `json:"webhook"`
	Recovery   string `json:"recovery"`
}

// Policy is the public policy view/input. Mutes share the same settings document
// but are exposed through independent CRUD APIs.
type Policy struct {
	Version         int                       `json:"version"`
	MinimumSeverity string                    `json:"minimum_severity"`
	Categories      map[string]CategoryPolicy `json:"categories"`
	Events          map[string]EventPolicy    `json:"events"`
}

// MuteSchedule suppresses selected channels in a bounded time range.
type MuteSchedule struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	MatchMode  string   `json:"match_mode"`
	Category   string   `json:"category"`
	EventTypes []string `json:"event_types"`
	Channels   []string `json:"channels"`
	StartsAt   int64    `json:"starts_at"`
	EndsAt     int64    `json:"ends_at"`
	Permanent  bool     `json:"permanent"`
	Enabled    bool     `json:"enabled"`
}

type policyDocument struct {
	Policy
	Mutes []MuteSchedule `json:"mutes"`
}

// PolicyManager owns notification_policy persistence and serializes all policy
// and mute writes so read-modify-write CRUD cannot lose concurrent changes.
type PolicyManager struct {
	store  SettingsStore
	now    func() time.Time
	saveMu sync.Mutex
}

func NewPolicyManager(store SettingsStore, now func() time.Time) *PolicyManager {
	if now == nil {
		now = time.Now
	}
	return &PolicyManager{store: store, now: now}
}

// DefaultPolicy returns a fresh default with every known category enabled.
// External delivery still requires notification_channels' automatic_events
// switch and an enabled, valid destination channel.
func DefaultPolicy() Policy {
	categories := make(map[string]CategoryPolicy)
	for category := range knownPolicyCategories() {
		categories[category] = CategoryPolicy{AdminBadge: true, Bot: true, Webhook: true}
	}
	return Policy{
		Version: PolicyVersion, MinimumSeverity: notify.SeverityWarn,
		Categories: categories, Events: make(map[string]EventPolicy),
	}
}

func (m *PolicyManager) Snapshot(ctx context.Context) (Policy, error) {
	doc, err := m.load(ctx)
	if err != nil {
		return Policy{}, err
	}
	return clonePolicy(doc.Policy), nil
}

func (m *PolicyManager) Save(ctx context.Context, policy Policy) error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	doc, err := m.load(ctx)
	if err != nil {
		return err
	}
	if err := validatePolicy(policy); err != nil {
		return err
	}
	doc.Policy = clonePolicy(policy)
	return m.save(ctx, doc)
}

func (m *PolicyManager) ListMutes(ctx context.Context) ([]MuteSchedule, error) {
	doc, err := m.load(ctx)
	if err != nil {
		return nil, err
	}
	return cloneMutes(doc.Mutes), nil
}

func (m *PolicyManager) CreateMute(ctx context.Context, mute MuteSchedule) (MuteSchedule, error) {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	doc, err := m.load(ctx)
	if err != nil {
		return MuteSchedule{}, err
	}
	if len(doc.Mutes) >= MaxMuteSchedules {
		return MuteSchedule{}, policyValidationf("静音计划数量不能超过 %d 个", MaxMuteSchedules)
	}
	mute.ID = ""
	mute, err = normalizeMute(mute, m.now(), mute.Enabled)

	if err != nil {
		return MuteSchedule{}, err
	}
	mute.ID, err = newMuteID()
	if err != nil {
		return MuteSchedule{}, errors.New("生成静音计划 ID 失败")
	}
	doc.Mutes = append(doc.Mutes, mute)
	if err := m.save(ctx, doc); err != nil {
		return MuteSchedule{}, err
	}
	return cloneMute(mute), nil
}

func (m *PolicyManager) UpdateMute(ctx context.Context, id string, mute MuteSchedule) (MuteSchedule, error) {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	doc, err := m.load(ctx)
	if err != nil {
		return MuteSchedule{}, err
	}
	index := muteIndex(doc.Mutes, id)
	if index < 0 {
		return MuteSchedule{}, ErrMuteNotFound
	}
	mute.ID = id
	mute, err = normalizeMute(mute, m.now(), mute.Enabled)
	if err != nil {

		return MuteSchedule{}, err
	}
	doc.Mutes[index] = mute
	if err := m.save(ctx, doc); err != nil {
		return MuteSchedule{}, err
	}
	return cloneMute(mute), nil
}

func (m *PolicyManager) DeleteMute(ctx context.Context, id string) error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	doc, err := m.load(ctx)
	if err != nil {
		return err
	}
	index := muteIndex(doc.Mutes, id)
	if index < 0 {
		return ErrMuteNotFound
	}
	doc.Mutes = append(doc.Mutes[:index], doc.Mutes[index+1:]...)
	return m.save(ctx, doc)
}

func (m *PolicyManager) load(ctx context.Context) (policyDocument, error) {
	if m.store == nil {
		return policyDocument{}, errors.New("通知策略存储不可用")
	}
	raw, ok, err := m.store.GetSetting(ctx, SettingKeyPolicy)
	if err != nil {
		return policyDocument{}, fmt.Errorf("读取通知策略失败: %w", err)
	}
	if !ok || strings.TrimSpace(raw) == "" {
		return policyDocument{Policy: DefaultPolicy(), Mutes: []MuteSchedule{}}, nil
	}
	var doc policyDocument
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return policyDocument{}, errors.New("通知策略格式无效")
	}
	// 类别扩充的就地升级：先补齐缺失类别再校验，存量文档不受影响。
	backfillCategories(&doc.Policy)
	if err := validatePolicy(doc.Policy); err != nil {
		return policyDocument{}, errors.New("通知策略格式无效")
	}
	if len(doc.Mutes) > MaxMuteSchedules {
		return policyDocument{}, errors.New("通知策略格式无效")
	}
	seen := make(map[string]bool, len(doc.Mutes))
	for i := range doc.Mutes {
		normalized, err := normalizeMute(doc.Mutes[i], time.Time{}, false)
		if err != nil || normalized.ID == "" || seen[normalized.ID] {
			return policyDocument{}, errors.New("通知策略格式无效")
		}
		seen[normalized.ID] = true
		doc.Mutes[i] = normalized
	}
	return doc, nil
}

func (m *PolicyManager) save(ctx context.Context, doc policyDocument) error {
	doc.Version = PolicyVersion
	if doc.Mutes == nil {
		doc.Mutes = []MuteSchedule{}
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		return errors.New("编码通知策略失败")
	}
	if err := m.store.SetSetting(ctx, SettingKeyPolicy, string(encoded)); err != nil {
		return fmt.Errorf("保存通知策略失败: %w", err)
	}
	return nil
}

func validatePolicy(policy Policy) error {
	if policy.Version != PolicyVersion {
		return policyValidation("通知策略版本无效")
	}
	if !validSeverity(policy.MinimumSeverity) {
		return policyValidation("最低通知级别无效")
	}
	knownCategories := knownPolicyCategories()
	if len(policy.Categories) != len(knownCategories) {
		return policyValidation("通知类别配置必须完整")
	}
	for category := range policy.Categories {
		if !knownCategories[category] {
			return policyValidation("通知类别无效")
		}
	}
	if len(policy.Events) > maxPolicyEventOverrides {
		return policyValidationf("事件覆盖数量不能超过 %d 个", maxPolicyEventOverrides)
	}
	for eventType, override := range policy.Events {
		definition, ok := notify.GetDefinition(eventType)
		if !ok {
			return policyValidation("事件类型无效")
		}
		for _, value := range []string{override.AdminBadge, override.Bot, override.Webhook, override.Recovery} {
			if !validOverride(value) {
				return policyValidation("事件覆盖值无效")
			}
		}
		if !definition.SupportsRecovery && override.Recovery != OverrideInherit {
			return policyValidation("该事件不支持恢复通知覆盖")
		}
	}
	return nil
}

func normalizeMute(mute MuteSchedule, now time.Time, requireFutureEnd bool) (MuteSchedule, error) {
	mute.ID = strings.TrimSpace(mute.ID)
	mute.Name = strings.TrimSpace(mute.Name)
	mute.MatchMode = strings.TrimSpace(mute.MatchMode)
	mute.Category = strings.TrimSpace(mute.Category)
	if len(mute.ID) > maxMuteIDLength {
		return MuteSchedule{}, policyValidation("静音计划 ID 无效")
	}
	if mute.Name == "" || utf8.RuneCountInString(mute.Name) > MaxMuteNameRunes {
		return MuteSchedule{}, policyValidationf("静音计划名称须为 1-%d 个字符", MaxMuteNameRunes)
	}
	knownCategories := knownPolicyCategories()
	switch mute.MatchMode {
	case MuteMatchAll:
		if mute.Category != "" || len(mute.EventTypes) != 0 {
			return MuteSchedule{}, policyValidation("全部事件静音不能指定类别或事件")

		}
	case MuteMatchCategory:
		if !knownCategories[mute.Category] || len(mute.EventTypes) != 0 {
			return MuteSchedule{}, policyValidation("类别静音配置无效")

		}
	case MuteMatchEvents:
		if mute.Category != "" || len(mute.EventTypes) == 0 || len(mute.EventTypes) > MaxMuteEventTypes {
			return MuteSchedule{}, policyValidationf("指定事件数量须为 1-%d 个", MaxMuteEventTypes)

		}
	default:
		return MuteSchedule{}, policyValidation("静音匹配方式无效")
	}

	mute.EventTypes = cleanUnique(mute.EventTypes)
	if mute.MatchMode == MuteMatchEvents && len(mute.EventTypes) == 0 {
		return MuteSchedule{}, policyValidation("指定事件不能为空")
	}
	for _, eventType := range mute.EventTypes {
		if _, ok := notify.GetDefinition(eventType); !ok {
			return MuteSchedule{}, policyValidation("静音事件类型无效")
		}
	}
	mute.Channels = cleanUnique(mute.Channels)
	if len(mute.Channels) == 0 || len(mute.Channels) > 3 {
		return MuteSchedule{}, policyValidation("静音渠道须至少选择一个")
	}
	for _, channel := range mute.Channels {
		if !validPolicyChannel(channel) {
			return MuteSchedule{}, policyValidation("静音渠道无效")
		}
	}
	if mute.StartsAt < 0 || mute.EndsAt < 0 {
		return MuteSchedule{}, policyValidation("静音时间无效")
	}
	if mute.Permanent {
		if mute.EndsAt != 0 {
			return MuteSchedule{}, policyValidation("永久静音不能设置结束时间")
		}
	} else {
		if mute.EndsAt <= mute.StartsAt {
			return MuteSchedule{}, policyValidation("静音结束时间必须晚于开始时间")
		}
		if requireFutureEnd && mute.EndsAt <= now.UnixMilli() {
			return MuteSchedule{}, policyValidation("启用中的静音结束时间必须晚于当前时间")
		}
	}
	return mute, nil
}

func clonePolicy(policy Policy) Policy {
	out := policy
	out.Categories = make(map[string]CategoryPolicy, len(policy.Categories))
	for key, value := range policy.Categories {
		out.Categories[key] = value
	}
	out.Events = make(map[string]EventPolicy, len(policy.Events))
	for key, value := range policy.Events {
		out.Events[key] = value
	}
	return out
}

func cloneMute(mute MuteSchedule) MuteSchedule {
	mute.EventTypes = slices.Clone(mute.EventTypes)
	mute.Channels = slices.Clone(mute.Channels)
	return mute
}

func cloneMutes(mutes []MuteSchedule) []MuteSchedule {
	out := make([]MuteSchedule, len(mutes))
	for i := range mutes {
		out[i] = cloneMute(mutes[i])
	}
	return out
}

func cleanUnique(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func muteIndex(mutes []MuteSchedule, id string) int {
	id = strings.TrimSpace(id)
	for i := range mutes {
		if mutes[i].ID == id {
			return i
		}
	}
	return -1
}

func newMuteID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func knownPolicyCategories() map[string]bool {
	return map[string]bool{
		notify.CategorySystemAlert:    true,
		notify.CategorySystemRecovery: true,
		notify.CategoryActivity:       true,
	}
}

// backfillCategories 为存量策略文档补齐后加的类别（各渠道默认开启），
// 保证旧版本 JSON 在类别扩充后仍能通过完整性校验（就地升级，无需版本迁移）。
func backfillCategories(policy *Policy) {
	if policy.Categories == nil {
		policy.Categories = make(map[string]CategoryPolicy, len(knownPolicyCategories()))
	}
	for category := range knownPolicyCategories() {
		if _, ok := policy.Categories[category]; !ok {
			policy.Categories[category] = CategoryPolicy{AdminBadge: true, Bot: true, Webhook: true}
		}
	}
}

func validSeverity(value string) bool {
	return value == notify.SeverityInfo || value == notify.SeverityWarn ||
		value == notify.SeverityError || value == notify.SeverityCritical
}

func validOverride(value string) bool {
	return value == OverrideInherit || value == OverrideEnabled || value == OverrideDisabled
}

func validPolicyChannel(value string) bool {
	return value == ChannelAdminBadge || value == ChannelBot || value == ChannelWebhook
}
