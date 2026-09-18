package notify

import (
	"embed"
	"strings"
	"sync"
	"text/template"
	"time"
)

// 事件通知模板：templates/ 目录下每种事件一个 .tmpl 文件（文件名 = 事件 key），
// 统一管理消息正文的关键信息行。模板输出纯文本——配置化通道（Bot/Webhook）
// 直接使用；兼容 owner 私聊由 RenderHTML 整体转义后发送，因此模板内不得
// 使用 HTML 标签。渲染失败或模板缺失时回退目录受控描述，绝不阻断通知。

// gmt8Zone 是未注入运营时区时的兜底时区（GMT+8，与默认运营时区一致）。
var gmt8Zone = time.FixedZone("GMT+8", 8*3600)

//go:embed templates/*.tmpl
var templateFS embed.FS

var (
	templatesOnce   sync.Once
	eventTemplates  *template.Template
	templatesFmtErr error
)

// lookupTemplate 返回事件 key 对应的已解析模板；解析失败或模板缺失返回 false。
func lookupTemplate(key string) (*template.Template, bool) {
	templatesOnce.Do(func() {
		eventTemplates, templatesFmtErr = template.New("notify").Funcs(template.FuncMap{
			"bytes":    humanBytes,
			"display":  displayOrUnknown,
			"fallback": firstNonEmpty,
		}).ParseFS(templateFS, "templates/*.tmpl")
	})
	if templatesFmtErr != nil {
		return nil, false
	}
	t := eventTemplates.Lookup(key + ".tmpl")
	return t, t != nil
}

// renderEventBody 渲染事件通知正文；模板缺失或渲染失败回退目录受控描述。
// 尾部多余换行统一裁掉，保证与信封/脚注的空行衔接稳定。
func renderEventBody(key string, data templateData) string {
	t, ok := lookupTemplate(key)
	if !ok {
		return controlledEventMessage(key)
	}
	var sb strings.Builder
	if err := t.Execute(&sb, data); err != nil {
		return controlledEventMessage(key)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// displayOrUnknown 空值展示兜底（模板内 optional 字段使用）。
func displayOrUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "未知"
	}
	return strings.TrimSpace(value)
}

// firstNonEmpty 返回第一个非空参数（模板内展示名回退用户名等场景）。
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
