package notifycfg

import (
	"context"
	"errors"

	"github.com/huaiminyetnotsleep/spore/internal/notify"
)

// NotifyEvent sends a controlled system event through enabled configured
// channels. Managed is true once automatic_events is enabled, including when
// policy suppresses every channel; this prevents the legacy owner route from
// bypassing an administrator's explicit suppression.
func (m *Manager) NotifyEvent(ctx context.Context, message notify.EventNotification) (notify.RuntimeDeliveryResult, error) {
	var result notify.RuntimeDeliveryResult
	if m == nil || m.policy == nil {
		return result, errors.New("通知运行时不可用")
	}
	if m.client == nil {
		return result, errors.New("通知 HTTP 客户端不可用")
	}

	channels, err := m.load(ctx)
	if err != nil {
		return result, err
	}
	policy, err := m.policy.load(ctx)
	if err != nil {
		return result, err
	}
	now := m.now()
	if !channels.AutomaticEvents {
		// Keep the legacy owner route for the default/compatibility mode, but
		// honor an explicit Bot policy or mute so an administrator can suppress
		// that route without enabling the new configured fan-out mode.
		decision := Evaluate(policy.Policy, policy.Mutes, message.EventType, ChannelBot,
			message.Recovery, true, now)
		if decision.Enabled {
			return result, nil
		}
		result.Managed = true
		return result, nil
	}
	result.Managed = true

	bot, found, err := findChannel[storedBot](channels, botChannelID, ChannelBot)
	if err != nil {
		return result, err
	}
	if found && bot.Config.Enabled {
		decision := Evaluate(policy.Policy, policy.Mutes, message.EventType, ChannelBot,
			message.Recovery, true, now)
		if decision.Enabled {
			result.Attempted++
			token, decryptErr := m.crypt.decrypt(bot.Config.Token, "bot.token")
			if decryptErr != nil || token == "" || bot.Config.ChatID == "" {
				result.Failed++
			} else if sendErr := m.sendBotMessage(ctx, token, bot.Config.ChatID, notify.RenderText(message)); sendErr != nil {
				result.Failed++
			} else {
				result.Delivered++
			}
		}
	}

	webhook, found, err := findChannel[storedWebhook](channels, webhookChannelID, ChannelWebhook)
	if err != nil {
		return result, err
	}
	if found && webhook.Config.Enabled {
		decision := Evaluate(policy.Policy, policy.Mutes, message.EventType, ChannelWebhook,
			message.Recovery, true, now)
		if decision.Enabled {
			result.Attempted++
			secrets, loadErr := m.loadWebhookSecrets(ctx)
			if loadErr != nil {
				result.Failed++
			} else if adapter, ok := lookupWebhook(secrets.Format); !ok {
				result.Failed++
			} else if sendErr := adapter.send(ctx, m.client, secrets, notify.RenderText(message), now); sendErr != nil {
				result.Failed++
			} else {
				result.Delivered++
			}
		}
	}

	return result, nil
}
