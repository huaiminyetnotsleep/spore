package notify

import (
	"fmt"
	"html"
	"strings"
)

// RenderText renders the controlled event message for text-based webhook
// adapters. It intentionally contains only the notification envelope,
// catalog text and template-rendered key info; callers cannot inject a raw
// cause or message body.
func RenderText(message EventNotification) string {
	source := displayValue(message.SourceName, "Spore")
	typeLabel := displayValue(message.TypeLabel, "系统告警")
	severity := severityLabel(message.Severity)
	title := displayValue(message.Title, "系统异常")
	body := displayValue(message.Body, "请查看管理端事件中心。")
	if message.Recovery {
		return fmt.Sprintf("[%s · %s · %s]\n✅ %s\n\n%s", source, typeLabel, severity, title, body)
	}
	if message.Activity {
		return fmt.Sprintf("[%s · %s · %s]\n%s\n\n%s", source, typeLabel, severity, title, body)
	}
	count := message.Count
	if count < 1 {
		count = 1
	}
	return fmt.Sprintf("[%s · %s · %s]\n%s\n\n%s\n\n已发生 %d 次，请前往管理端事件中心查看。",
		source, typeLabel, severity, title, body, count)
}

// RenderHTML renders the controlled event message for Telegram delivery.S
// Every interpolated value is escaped at this boundary because the legacy
// delivery.Sender sends with Telegram HTML parse mode.
func RenderHTML(message EventNotification) string {
	source := html.EscapeString(displayValue(message.SourceName, "Spore"))
	typeLabel := html.EscapeString(displayValue(message.TypeLabel, "系统告警"))
	severity := html.EscapeString(severityLabel(message.Severity))
	title := html.EscapeString(displayValue(message.Title, "系统异常"))
	body := html.EscapeString(displayValue(message.Body, "请查看管理端事件中心。"))
	if message.Recovery {
		return fmt.Sprintf("<b>%s · %s · %s</b>\n✅ <b>%s</b>\n\n%s",
			source, typeLabel, severity, title, body)
	}
	if message.Activity {
		return fmt.Sprintf("<b>%s · %s · %s</b>\n<b>%s</b>\n%s",
			source, typeLabel, severity, title, body)
	}
	count := message.Count
	if count < 1 {
		count = 1
	}
	return fmt.Sprintf("<b>%s · %s · %s</b>\n<b>%s</b>\n%s\n\n已发生 %d 次，请前往管理端事件中心查看。",
		source, typeLabel, severity, title, body, count)
}

func severityLabel(value string) string {
	switch value {
	case SeverityInfo:
		return "提示"
	case SeverityWarn:
		return "警告"
	case SeverityError:
		return "错误"
	default:
		return "事件"
	}
}

func displayValue(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
