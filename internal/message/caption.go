package message

import (
	"reflect"

	"github.com/gotd/td/tg"
)

// Caption 是媒体 caption 的结构化描述：文本 + 原始 MTProto 实体（偏移按 UTF-16 计）。
// Bot API 路径经 RenderHTML 渲染为 HTML；MTProto 引用直发路径经 Limited 截断后
// 直接透传实体——文本与实体同源于源消息，无二次转换失真。
// Channels 是消息末尾的频道脚注（可点击跳转链接，见 channels.go）；
// 两条路径都会为其预留长度预算。
type Caption struct {
	Text     string
	Entities []tg.MessageEntityClass
	Channels []ChannelLink
}

const sourceLinkText = "🔗 原消息"

// WithQuotedBody 把正文包进普通引用块（blockquote），与链接卡片、频道脚注形成
// 视觉区分；正文自带的实体（加粗/链接等）保留嵌套。必须在 WithSourceLink /
// WithChannels 之前调用——卡片前插与脚注后追都会平移既有实体，引用块因此
// 恰好只覆盖正文区间。正文为空时不加。
func (c Caption) WithQuotedBody() Caption {
	if c.Text == "" {
		return c
	}
	bodyUnits := newUnitMapper(c.Text).units
	entities := make([]tg.MessageEntityClass, 0, len(c.Entities)+1)
	entities = append(entities, &tg.MessageEntityBlockquote{Offset: 0, Length: bodyUnits})
	entities = append(entities, c.Entities...)
	c.Entities = entities
	return c
}

// WithSourceLink 在 caption 顶部加入引用卡片样式的原消息入口，并平移原实体偏移。
func (c Caption) WithSourceLink(sourceURL string) Caption {
	if sourceURL == "" {
		return c
	}
	cardText := sourceLinkText + "\n" + sourceURL
	prefix := cardText
	if c.Text != "" {
		prefix += "\n\n"
	}
	titleUnits := newUnitMapper(sourceLinkText).units
	cardUnits := newUnitMapper(cardText).units
	urlOffset := titleUnits + newUnitMapper("\n").units
	delta := newUnitMapper(prefix).units
	entities := make([]tg.MessageEntityClass, 0, len(c.Entities)+3)
	entities = append(entities,
		&tg.MessageEntityBlockquote{Offset: 0, Length: cardUnits},
		&tg.MessageEntityBold{Offset: 0, Length: titleUnits},
		&tg.MessageEntityTextURL{Offset: urlOffset, Length: newUnitMapper(sourceURL).units, URL: sourceURL},
	)
	for _, entity := range c.Entities {
		if shifted, ok := cloneEntityWithOffset(entity, delta); ok {
			entities = append(entities, shifted)
		}
	}
	return Caption{Text: prefix + c.Text, Entities: entities}
}

// cloneEntityWithOffset 浅拷贝 gotd 生成的实体结构并平移 Offset，避免污染源消息实体。
func cloneEntityWithOffset(entity tg.MessageEntityClass, delta int) (tg.MessageEntityClass, bool) {
	if entity == nil {
		return nil, false
	}
	value := reflect.ValueOf(entity)
	if value.Kind() != reflect.Pointer || value.IsNil() || value.Elem().Kind() != reflect.Struct {
		return nil, false
	}
	clone := reflect.New(value.Elem().Type())
	clone.Elem().Set(value.Elem())
	field := clone.Elem().FieldByName("Offset")
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.Int {
		return nil, false
	}
	field.SetInt(field.Int() + int64(delta))
	shifted, ok := clone.Interface().(tg.MessageEntityClass)
	return shifted, ok
}

// RenderHTML 渲染为 Bot API HTML（parse_mode=HTML），预算 maxCaptionUnits，
// 截断策略与 Item.RenderHTML 的媒体分支一致。
func (c Caption) RenderHTML() string {
	return c.renderHTML(maxCaptionUnits)
}

func (c Caption) renderHTML(limitUnits int) string {
	if c.hasChannels() {
		// 为脚注预留预算：正文（含截断附注）+ 脚注整体不超 limit
		limitUnits -= footerUnits(c.Channels)
		if limitUnits < 0 {
			limitUnits = 0
		}
		return ToHTMLLimited(c.Text, c.Entities, limitUnits) + channelFooterHTML(c.Channels)
	}
	return ToHTMLLimited(c.Text, c.Entities, limitUnits)
}

// Limited 返回不超过 maxCaptionUnits（UTF-16 unit）的 Caption：
// 为截断附注与频道脚注预留预算，仅保留完全落在保留区间内的实体（与
// ToHTMLLimited 同策略），截断时在文本尾部追加纯文本附注——实体偏移基于
// 截断前的前缀，无需平移；脚注文本与实体追加在最末，偏移 = 前缀 unit 数。
func (c Caption) Limited() Caption {
	footUnits := 0
	if c.hasChannels() {
		footUnits = footerUnits(c.Channels)
	}
	effective := maxCaptionUnits - truncateNoteUnits - footUnits
	if effective < 0 {
		effective = 0
	}
	m := newUnitMapper(c.Text)
	if m.units > effective {
		cut := m.alignEnd(effective)

		kept := make([]tg.MessageEntityClass, 0, len(c.Entities))
		for _, e := range c.Entities {
			base, ok := e.(interface {
				GetOffset() int
				GetLength() int
			})
			if !ok {
				continue
			}
			start, end := base.GetOffset(), base.GetOffset()+base.GetLength()
			if start < 0 || start >= end || end > effective {
				// 起点为 0 的引用块越出截断预算：收缩到预算边界而非整块丢弃，
				// 保证长文截断后引用样式仍覆盖可见文本；其余照旧丢弃
				if _, isBQ := e.(*tg.MessageEntityBlockquote); isBQ && start == 0 && end > effective && effective > 0 {
					kept = append(kept, &tg.MessageEntityBlockquote{Offset: 0, Length: effective})
				}
				continue // 非法 / 跨界 / 越出截断预算：丢弃该实体
			}
			kept = append(kept, e)
		}
		c.Text = c.Text[:cut] + truncateNote
		c.Entities = kept
	}
	if c.hasChannels() {
		// 前缀（正文 + 截断附注）的 unit 数即脚注在最终消息中的起始偏移
		base := newUnitMapper(c.Text).units
		c.Text += channelFooterText(c.Channels)
		c.Entities = append(c.Entities, channelFooterEntities(c.Channels, base)...)
	}
	return c
}
