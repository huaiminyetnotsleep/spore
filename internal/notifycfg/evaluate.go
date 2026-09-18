package notifycfg

import (
	"slices"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/notify"
)

const (
	DecisionEnabled            = "enabled"
	DecisionChannelUnavailable = "channel_unavailable"
	DecisionMuted              = "muted"
	DecisionRecoveryDisabled   = "recovery_disabled"
	DecisionEventDisabled      = "event_disabled"
	DecisionBelowSeverity      = "below_minimum_severity"
	DecisionCategoryDisabled   = "category_disabled"
	DecisionUnknownEvent       = "unknown_event"
	DecisionInvalidChannel     = "invalid_channel"
)

// Evaluation describes a pure notification-policy decision.
type Evaluation struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
	MuteID  string `json:"mute_id,omitempty"`
}

// Evaluate applies availability, active mutes, event overrides, category
// defaults and external-channel minimum severity. Hub cooldown remains a later
// runtime concern and is intentionally not evaluated here.
func Evaluate(policy Policy, mutes []MuteSchedule, eventType, channel string, recovery, channelAvailable bool, now time.Time) Evaluation {
	definition, ok := notify.GetDefinition(eventType)
	if !ok {
		return Evaluation{Reason: DecisionUnknownEvent}
	}
	if !validPolicyChannel(channel) {
		return Evaluation{Reason: DecisionInvalidChannel}
	}
	if !channelAvailable {
		return Evaluation{Reason: DecisionChannelUnavailable}
	}
	effectiveCategory := definition.Category
	if recovery {
		effectiveCategory = notify.CategorySystemRecovery
	}
	for _, mute := range mutes {
		if muteMatches(mute, definition.Type, effectiveCategory, channel, now) {
			return Evaluation{Reason: DecisionMuted, MuteID: mute.ID}
		}
	}

	override, hasOverride := policy.Events[eventType]
	if recovery && hasOverride {
		switch override.Recovery {
		case OverrideDisabled:
			return Evaluation{Reason: DecisionRecoveryDisabled}
		case OverrideEnabled:
			// Recovery is eligible; channel policy still applies below.
		}
	}
	channelOverride := OverrideInherit
	if hasOverride {
		channelOverride = overrideForChannel(override, channel)
	}
	if channelOverride == OverrideDisabled {
		return Evaluation{Reason: DecisionEventDisabled}
	}
	if channelOverride == OverrideEnabled {
		return Evaluation{Enabled: true, Reason: DecisionEnabled}
	}
	// 最低严重级别是告警降噪手段：活动通知（登录/申请等）是 info 级的逐次
	// 显式业务通知，不受该门槛约束（否则默认 warn 会静默吞掉全部活动通知）；
	// 其开关由类别/事件覆盖与静音控制。
	if definition.Category != notify.CategoryActivity &&
		(channel == ChannelBot || channel == ChannelWebhook) &&
		severityRank(definition.Severity) < severityRank(policy.MinimumSeverity) {
		return Evaluation{Reason: DecisionBelowSeverity}
	}
	category, ok := policy.Categories[effectiveCategory]
	if !ok || !categoryChannelEnabled(category, channel) {
		return Evaluation{Reason: DecisionCategoryDisabled}
	}
	return Evaluation{Enabled: true, Reason: DecisionEnabled}
}

func muteMatches(mute MuteSchedule, eventType, category, channel string, now time.Time) bool {
	if !mute.Enabled || !slices.Contains(mute.Channels, channel) {
		return false
	}
	nowMS := now.UnixMilli()
	if mute.StartsAt > 0 && nowMS < mute.StartsAt {
		return false
	}
	if !mute.Permanent && (mute.EndsAt == 0 || nowMS >= mute.EndsAt) {
		return false
	}
	switch mute.MatchMode {
	case MuteMatchAll:
		return true
	case MuteMatchCategory:
		return mute.Category == category
	case MuteMatchEvents:
		return slices.Contains(mute.EventTypes, eventType)
	default:
		return false
	}
}

func overrideForChannel(override EventPolicy, channel string) string {
	switch channel {
	case ChannelAdminBadge:
		return override.AdminBadge
	case ChannelBot:
		return override.Bot
	case ChannelWebhook:
		return override.Webhook
	default:
		return OverrideInherit
	}
}

func categoryChannelEnabled(category CategoryPolicy, channel string) bool {
	switch channel {
	case ChannelAdminBadge:
		return category.AdminBadge
	case ChannelBot:
		return category.Bot
	case ChannelWebhook:
		return category.Webhook
	default:
		return false
	}
}

func severityRank(severity string) int {
	switch severity {
	case notify.SeverityInfo:
		return 1
	case notify.SeverityWarn:
		return 2
	case notify.SeverityError:
		return 3
	default:
		return 0
	}
}
