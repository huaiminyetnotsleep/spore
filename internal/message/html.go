package message

import (
	"html"
	"sort"
	"strings"

	"github.com/gotd/td/tg"
)

const (
	maxMessageUnits = 4096 // Bot API 消息长度上限（UTF-16 unit）
	maxCaptionUnits = 1024 // Bot API caption 上限
	truncateNote    = "\n\n（原文过长已截断）"
)

// truncateNoteUnits 附注本身的 UTF-16 长度；包级静态计算一次。
var truncateNoteUnits = newUnitMapper(truncateNote).units

// unitMapper 维护 UTF-16 code unit 序号 → UTF-8 字节偏移的单射映射。
// MTProto 实体偏移按 UTF-16 计，输出 HTML 时需要换算回字节区间。
type unitMapper struct {
	text    string
	offsets []int // offsets[i] = 第 i 个 UTF-16 unit 在 text 中的起始字节偏移
	units   int   // 文本总 unit 数
}

func newUnitMapper(text string) *unitMapper {
	m := &unitMapper{text: text, offsets: make([]int, 0, len(text))}
	for idx, r := range text {
		m.offsets = append(m.offsets, idx)
		if r > 0xFFFF {
			m.offsets = append(m.offsets, idx) // 星界字符（含 emoji）的第二个代理项同一起始字节
		}
	}
	m.units = len(m.offsets)
	return m
}

// mapOffset 把 UTF-16 unit 偏移换算为字节偏移；越界收敛到文本两端。
func (m *unitMapper) mapOffset(unit int) int {
	switch {
	case unit <= 0:
		return 0
	case unit >= m.units:
		return len(m.text)
	default:
		return m.offsets[unit]
	}
}

type span struct {
	start, end int // 字节区间（均落在 rune 边界上）
	open       string
	close      string
}

type eventType bool

const (
	eventClose eventType = true
	eventOpen  eventType = false
)

type event struct {
	pos int
	typ eventType
	idx int
}

// ToHTMLLimited 将文本+实体渲染为 Bot API HTML（parse_mode=HTML）。
// limitUnits 指定渲染预算（UTF-16 unit），超限时在预算处截断并附注；
// 仅保留完全落在保留区间内的实体，跨界与越界实体一律丢弃。
func ToHTMLLimited(text string, entities []tg.MessageEntityClass, limitUnits int) string {
	// 为截断附注预留预算，保证"正文+附注"整体仍在 Bot API 上限内
	effective := limitUnits - truncateNoteUnits
	if effective < 0 {
		effective = 0
	}
	m := newUnitMapper(text)
	spans, cutPos := buildSpans(m, entities, effective)

	var sb strings.Builder
	cur := 0
	for _, ev := range events(spans) {
		pos := ev.pos
		if pos > cutPos {
			break // 事件越出截断边界：其后内容不再渲染（其对应实体已在 buildSpans 中丢弃）
		}
		if pos > cur {
			sb.WriteString(html.EscapeString(m.text[cur:pos]))
			cur = pos
		}
		if ev.typ == eventOpen {
			sb.WriteString(spans[ev.idx].open)
		} else {
			sb.WriteString(spans[ev.idx].close)
		}
		cur = pos
	}
	if cur < cutPos {
		sb.WriteString(html.EscapeString(m.text[cur:cutPos]))
	}
	if cutPos < len(m.text) {
		sb.WriteString(truncateNote)
	}
	return sb.String()
}

// buildSpans 把实体转换为字节区间的标签片段；返回值同时给出截断边界（未截断时等于 len(text)）。
func buildSpans(m *unitMapper, entities []tg.MessageEntityClass, limitUnits int) ([]span, int) {
	limit := m.mapOffset(limitUnits)

	spans := make([]span, 0, len(entities))
	for _, e := range entities {
		base, ok := e.(interface {
			GetOffset() int
			GetLength() int
		})
		if !ok {
			continue
		}
		start := m.alignStart(base.GetOffset())
		end := m.alignEnd(base.GetOffset() + base.GetLength())
		if start < 0 || start >= end {
			continue // 非法实体：丢弃
		}
		if end > limit {
			// 起点在预算内、起点为 0 的引用块越出截断边界：收缩到预算边界
			// 而非整块丢弃，保证长文截断后引用样式仍覆盖可见文本；其余照旧丢弃
			if _, isBQ := e.(*tg.MessageEntityBlockquote); isBQ && base.GetOffset() == 0 && start < limit {
				if clamped := m.alignStart(limitUnits); clamped > start {
					end = clamped
				} else {
					continue
				}
			} else {
				continue // 越出截断预算：丢弃该实体
			}
		}
		open, close, ok := classify(e, m.text[start:end])
		if !ok {
			continue // 不支持的类型退化为纯文本
		}
		spans = append(spans, span{
			start: start, end: end,
			open:  open,
			close: close,
		})
	}
	sort.SliceStable(spans, func(i, j int) bool {
		a, b := spans[i], spans[j]
		if a.start != b.start {
			return a.start < b.start
		}
		return a.end > b.end // 外层实体先开
	})
	return spans, limit
}

// alignStart 把起始 unit 偏移换算成字节偏移，并回退到所在 rune 的真实起点，
// 防止星界字符的第二个代理项被当作独立起点，把开标签插进 UTF-8 序列中间。
func (m *unitMapper) alignStart(unit int) int {
	off := m.mapOffset(unit)
	for off > 0 && !runeBoundary(m.text, off) {
		off--
	}
	return off
}

// alignEnd 把结束 unit 偏移换算成字节偏移并对齐到 rune 边界，
// 保证文本片段始终包含完整 UTF-8 rune。
func (m *unitMapper) alignEnd(unit int) int {
	off := m.mapOffset(unit)
	for off < len(m.text) && !runeBoundary(m.text, off) {
		off--
	}
	return off
}

// runeBoundary 判断 off 是否落在某个 UTF-8 rune 的起始字节上。
func runeBoundary(s string, off int) bool {
	if off >= len(s) || off <= 0 {
		return true
	}
	return s[off]&0xC0 != 0x80 // 非 10xxxxxx 续字节即为边界
}

// classify 把 MTProto 实体映射为完整的开/闭 HTML 标签串，以及是否支持。
// 注意：带语言的 Pre 必须渲染为 <pre><code class="language-x">…</code></pre>——
// Bot API 只认可挂在 <code> 上的 class，直接 <pre class> 会被拒收（can't parse entities）。
func classify(e tg.MessageEntityClass, raw string) (open, close string, ok bool) {
	switch ent := e.(type) {
	case *tg.MessageEntityBold:
		return "<b>", "</b>", true
	case *tg.MessageEntityItalic:
		return "<i>", "</i>", true
	case *tg.MessageEntityUnderline:
		return "<u>", "</u>", true
	case *tg.MessageEntityStrike:
		return "<s>", "</s>", true
	case *tg.MessageEntityCode:
		return "<code>", "</code>", true
	case *tg.MessageEntityPre:
		if ent.Language == "" {
			return "<pre>", "</pre>", true
		}
		cls := html.EscapeString(ent.Language)
		return `<pre><code class="language-` + cls + `">`, "</code></pre>", true
	case *tg.MessageEntityBlockquote:
		return "<blockquote>", "</blockquote>", true
	case *tg.MessageEntitySpoiler:
		return "<tg-spoiler>", "</tg-spoiler>", true
	case *tg.MessageEntityURL:
		return `<a href="` + html.EscapeString(raw) + `">`, "</a>", true
	case *tg.MessageEntityTextURL:
		return `<a href="` + html.EscapeString(ent.URL) + `">`, "</a>", true
	default:
		// Mention/@user 由客户端自动识别；MentionName、Email、CustomEmoji 等退化为纯文本。
		return "", "", false
	}
}

// events 生成排序后的标签事件序列：位置升序；同位置先闭后开，保证嵌套合法。
func events(spans []span) []event {
	out := make([]event, 0, len(spans)*2)
	for i := range spans {
		out = append(out, event{pos: spans[i].start, typ: eventOpen, idx: i})
		out = append(out, event{pos: spans[i].end, typ: eventClose, idx: i})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.pos != b.pos {
			return a.pos < b.pos
		}
		if a.typ != b.typ {
			return a.typ == eventClose // 同位置先闭后开
		}
		if a.typ == eventClose {
			sa, sb := spans[a.idx], spans[b.idx]
			if sa.start != sb.start {
				return sa.start > sb.start
			}
			// 同起点同终点（范围完全重合）的实体：后开者先关，保证嵌套合法
			//（如正文引用块与覆盖全文的斜体/加粗同范围叠加）
			return a.idx > b.idx
		}
		return false
	})
	return out
}
