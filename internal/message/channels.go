// 频道脚注：把用户绑定频道的跳转链接以样式化形态附在消息本体末尾。
// 公开频道展示 @username 并链接到 t.me/<username>；私有频道没有公开
// 用户名，展示频道标题并链接到 t.me/c/<内部ID>/1（仅频道成员可跳转）。
// 脚注占用消息/caption 的长度预算：Bot API 路径由 renderHTML 预留，
// MTProto 直传路径由 Limited 预留——两条路径整体不超平台上限。

package message

import (
	"html"
	"strings"

	"github.com/gotd/td/tg"
)

// footerSeparator 是脚注内多个频道链接之间的分隔符：每个频道独占一行
// （含首项，紧跟"频道："标签换行展示）。
const footerSeparator = "\n"

// ChannelLink 是脚注中的一个频道跳转项：Label 为展示文本，URL 为点击目标。
type ChannelLink struct {
	Label string
	URL   string
}

// WithChannels 设置消息末尾的频道脚注内容；空切片表示不带脚注。
func (c Caption) WithChannels(links []ChannelLink) Caption {
	c.Channels = links
	return c
}

// hasChannels 是否携带脚注。
func (c Caption) hasChannels() bool {
	return len(c.Channels) > 0
}

// channelFooterText 渲染脚注纯文本（以 "\n\n" 起头，直接拼在正文之后；
// 每个频道独占一行）。
func channelFooterText(links []ChannelLink) string {
	var sb strings.Builder
	sb.WriteString("\n\n📢 频道：")
	for _, l := range links {
		sb.WriteString(footerSeparator)
		sb.WriteString(l.Label)
	}
	return sb.String()
}

// channelFooterHTML 渲染脚注为 Bot API HTML：整段引用块包裹，标签加粗、
// 每个频道一个可点击链接独占一行（Label 与 URL 均转义，防止频道标题/
// 用户名注入 HTML）。与原消息来源卡片同款引用块视觉。
func channelFooterHTML(links []ChannelLink) string {
	var sb strings.Builder
	sb.WriteString("\n\n<blockquote>📢 <b>频道</b>：")
	for _, l := range links {
		sb.WriteString(footerSeparator)
		sb.WriteString(`<a href="`)
		sb.WriteString(html.EscapeString(l.URL))
		sb.WriteString(`">`)
		sb.WriteString(html.EscapeString(l.Label))
		sb.WriteString("</a>")
	}
	sb.WriteString("</blockquote>")
	return sb.String()
}

// channelFooterEntities 构建脚注对应的 MTProto 实体：整段脚注的引用块 +
// 加粗的"📢 频道"标签 + 每个频道一个 TextURL 实体（Label 为展示文本）。
// baseUnits 是脚注文本在最终消息中的起始 UTF-16 unit 偏移（即正文前缀的
// unit 数）；引用块跳过前导 "\n\n" 分隔空行，从标签行起包裹。
func channelFooterEntities(links []ChannelLink, baseUnits int) []tg.MessageEntityClass {

	// 标签"📢 频道"的 unit 区间：跳过前导 "\n\n"（2 个 unit）；
	// 📢 是星界字符占 2 个 unit，unitMapper 精确计算无需手工数。
	labelStart := newUnitMapper("\n\n").units
	labelEnd := newUnitMapper("\n\n📢 频道").units
	entities := []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: baseUnits + labelStart, Length: labelEnd - labelStart},
	}

	cursor := labelEnd + newUnitMapper("：").units
	for _, l := range links {
		// 每项前的换行分隔符
		cursor += newUnitMapper(footerSeparator).units
		labelUnits := newUnitMapper(l.Label).units
		entities = append(entities, &tg.MessageEntityTextURL{
			Offset: baseUnits + cursor,
			Length: labelUnits,
			URL:    l.URL,
		})
		cursor += labelUnits
	}
	// 引用块覆盖除前导空行外的整段脚注；追加在末尾不影响既有实体定位
	entities = append(entities, &tg.MessageEntityBlockquote{
		Offset: baseUnits + labelStart,
		Length: newUnitMapper(channelFooterText(links)).units - labelStart,
	})
	return entities
}

// footerUnits 返回脚注占用的 UTF-16 unit 数（用于渲染前预留预算）。
func footerUnits(links []ChannelLink) int {
	return newUnitMapper(channelFooterText(links)).units
}
