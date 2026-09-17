package notify

import (
	"strings"
	"testing"
)

func TestRenderIncludesSourceTypeAndSeverity(t *testing.T) {
	message := EventNotification{
		SourceName: "我的 <Spore>", TypeLabel: "系统告警", Severity: SeverityError,
		Title: "Bot 轮询冲突", Body: "请检查配置。", Count: 2,
	}
	text := RenderText(message)
	for _, want := range []string{"我的 <Spore>", "系统告警", "错误", "Bot 轮询冲突", "已发生 2 次"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing %q: %s", want, text)
		}
	}
	html := RenderHTML(message)
	for _, want := range []string{"我的 &lt;Spore&gt;", "系统告警", "错误", "Bot 轮询冲突", "已发生 2 次"} {
		if !strings.Contains(html, want) {
			t.Fatalf("html missing %q: %s", want, html)
		}
	}
}

func TestRenderRecoveryUsesRecoveryEnvelope(t *testing.T) {
	message := EventNotification{
		SourceName: "Spore", TypeLabel: "系统恢复", Severity: SeverityInfo,
		Title: "云盘功能已恢复", Body: "现在可以继续使用。", Count: 9, Recovery: true,
	}
	text := RenderText(message)
	if !strings.Contains(text, "系统恢复") || !strings.Contains(text, "✅") || strings.Contains(text, "已发生 9 次") {
		t.Fatalf("unexpected recovery text: %s", text)
	}
}
