package message

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

// TestCaptionLimited 表测截断：预算内原样返回；超限截断并追加附注，
// 完全落在保留区间内的实体保留，跨界/越界实体丢弃；实体偏移不平移。
func TestCaptionLimited(t *testing.T) {
	long := strings.Repeat("a", maxCaptionUnits+100)
	bold := &tg.MessageEntityBold{Offset: 0, Length: 5}
	cross := &tg.MessageEntityBold{Offset: maxCaptionUnits - 3, Length: 10} // 跨截断边界

	t.Run("预算内原样返回", func(t *testing.T) {
		c := Caption{Text: "hello", Entities: []tg.MessageEntityClass{bold}}
		got := c.Limited()
		if got.Text != "hello" || len(got.Entities) != 1 {
			t.Fatalf("预算内应原样返回: %+v", got)
		}
	})

	t.Run("超限截断并丢弃跨界实体", func(t *testing.T) {
		kept := &tg.MessageEntityItalic{Offset: 1, Length: 3}
		c := Caption{Text: long, Entities: []tg.MessageEntityClass{kept, cross}}
		got := c.Limited()

		m := newUnitMapper(got.Text)
		if m.units > maxCaptionUnits {
			t.Fatalf("截断后不得超过预算: %d", m.units)
		}
		if !strings.HasSuffix(got.Text, truncateNote) {
			t.Error("截断后应追加附注")
		}
		if len(got.Entities) != 1 {
			t.Fatalf("跨界实体应被丢弃，得到 %d 个", len(got.Entities))
		}
		if _, ok := got.Entities[0].(*tg.MessageEntityItalic); !ok {
			t.Errorf("保留的实体应保持原对象: %T", got.Entities[0])
		}
	})

	t.Run("空文本与空实体", func(t *testing.T) {
		got := Caption{}.Limited()
		if got.Text != "" || len(got.Entities) != 0 {
			t.Fatalf("空 caption 应原样返回: %+v", got)
		}
	})

	t.Run("星界字符截断对齐 rune 边界", func(t *testing.T) {
		// 全 emoji（每字符 2 个 UTF-16 unit）：截断点落在代理项时须回退到 rune 起点
		text := strings.Repeat("😀", (maxCaptionUnits+50)/2)
		got := Caption{Text: text}.Limited()
		if !strings.HasSuffix(got.Text, truncateNote) {
			t.Error("超限文本应追加附注")
		}
		for _, r := range got.Text[:len(got.Text)-len(truncateNote)] {
			if r == 0xFFFD {
				t.Fatal("截断不得产生非法 rune")
			}
		}
		m := newUnitMapper(got.Text)
		if m.units > maxCaptionUnits {
			t.Fatalf("截断后不得超过预算: %d", m.units)
		}
	})
}

// TestCaptionRenderHTML 与 Limited 的截断策略一致性：同一输入下，
// RenderHTML 的正文部分与 Limited 的文本对应（前者以 HTML 标签呈现实体）。
func TestCaptionRenderHTML(t *testing.T) {
	c := Caption{Text: "hello world", Entities: []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 0, Length: 5},
	}}
	if got := c.RenderHTML(); got != "<b>hello</b> world" {
		t.Fatalf("HTML 渲染不符: %q", got)
	}
}

func TestCaptionWithSourceLink(t *testing.T) {
	const sourceURL = "https://t.me/example_channel/42"
	cardText := sourceLinkText + "\n" + sourceURL
	original := &tg.MessageEntityBold{Offset: 0, Length: 5}
	got := (Caption{
		Text:     "hello",
		Entities: []tg.MessageEntityClass{original},
	}).WithSourceLink(sourceURL)

	if got.Text != cardText+"\n\nhello" {
		t.Fatalf("置顶文本不符: %q", got.Text)
	}
	if len(got.Entities) != 4 {
		t.Fatalf("应包含卡片、标题、来源链接和原实体，得到 %d 个", len(got.Entities))
	}
	quote, ok := got.Entities[0].(*tg.MessageEntityBlockquote)
	if !ok || quote.Offset != 0 || quote.Length != newUnitMapper(cardText).units {
		t.Fatalf("引用卡片实体不符: %#v", got.Entities[0])
	}
	title, ok := got.Entities[1].(*tg.MessageEntityBold)
	if !ok || title.Offset != 0 || title.Length != newUnitMapper(sourceLinkText).units {
		t.Fatalf("卡片标题实体不符: %#v", got.Entities[1])
	}
	link, ok := got.Entities[2].(*tg.MessageEntityTextURL)
	if !ok || link.Offset != newUnitMapper(sourceLinkText+"\n").units ||
		link.Length != newUnitMapper(sourceURL).units || link.URL != sourceURL {
		t.Fatalf("来源链接实体不符: %#v", got.Entities[2])
	}
	bold, ok := got.Entities[3].(*tg.MessageEntityBold)
	if !ok || bold.Offset != newUnitMapper(cardText+"\n\n").units || bold.Length != 5 {
		t.Fatalf("原实体偏移未正确平移: %#v", got.Entities[3])
	}
	if original.Offset != 0 {
		t.Fatalf("不得修改源实体: %+v", original)
	}
	wantHTML := `<blockquote><b>🔗 原消息</b>` + "\n" +
		`<a href="https://t.me/example_channel/42">https://t.me/example_channel/42</a></blockquote>` +
		"\n\n<b>hello</b>"
	if html := got.RenderHTML(); html != wantHTML {
		t.Fatalf("HTML 不符\nwant: %s\ngot:  %s", wantHTML, html)
	}
}

func TestCaptionWithSourceLinkEmptyAndLimited(t *testing.T) {
	const sourceURL = "https://t.me/c/1234567890/7"
	cardText := sourceLinkText + "\n" + sourceURL
	empty := (Caption{}).WithSourceLink(sourceURL)
	if empty.Text != cardText || len(empty.Entities) != 3 {
		t.Fatalf("空 caption 仍应显示完整来源卡片: %+v", empty)
	}

	long := Caption{Text: strings.Repeat("😀", maxCaptionUnits)}.WithSourceLink(sourceURL).Limited()
	if !strings.HasPrefix(long.Text, cardText+"\n\n") {
		t.Fatalf("截断后必须保留置顶来源卡片: %q", long.Text)
	}
	if !strings.HasSuffix(long.Text, truncateNote) {
		t.Fatalf("超长 caption 应保留截断提示: %q", long.Text)
	}
	if units := newUnitMapper(long.Text).units; units > maxCaptionUnits {
		t.Fatalf("截断后超限: %d", units)
	}
	if _, ok := long.Entities[0].(*tg.MessageEntityBlockquote); !ok {
		t.Fatalf("截断后引用卡片实体必须保留: %#v", long.Entities)
	}
	link, ok := long.Entities[2].(*tg.MessageEntityTextURL)
	if !ok || link.URL != sourceURL {
		t.Fatalf("截断后来源链接实体必须保留: %#v", long.Entities)
	}
}

func TestCaptionWithQuotedBody(t *testing.T) {
	t.Run("正文包进引用块且原实体保留嵌套", func(t *testing.T) {
		bold := &tg.MessageEntityBold{Offset: 2, Length: 2}
		got := Caption{Text: "ab中文cd", Entities: []tg.MessageEntityClass{bold}}.WithQuotedBody()
		if len(got.Entities) != 2 {
			t.Fatalf("应包含引用块与原实体，得到 %d 个", len(got.Entities))
		}
		quote, ok := got.Entities[0].(*tg.MessageEntityBlockquote)
		if !ok || quote.Offset != 0 || quote.Length != 6 {
			t.Fatalf("引用块应覆盖整个正文: %#v", got.Entities[0])
		}
		if bold.Offset != 2 || bold.Length != 2 {
			t.Fatalf("原实体不得被修改: %#v", bold)
		}
		if got.RenderHTML() != "<blockquote>ab<b>中文</b>cd</blockquote>" {
			t.Fatalf("HTML 渲染不符: %q", got.RenderHTML())
		}
	})

	t.Run("与来源卡片和脚注叠加时引用块只覆盖正文", func(t *testing.T) {
		const sourceURL = "https://t.me/example_channel/42"
		cardText := sourceLinkText + "\n" + sourceURL
		links := []ChannelLink{{Label: "@chan", URL: "https://t.me/chan"}}
		got := Caption{Text: "hello", Entities: []tg.MessageEntityClass{
			&tg.MessageEntityBold{Offset: 0, Length: 5},
		}}.WithQuotedBody().WithSourceLink(sourceURL).WithChannels(links)

		wantText := cardText + "\n\nhello"
		if got.Text != wantText {
			t.Fatalf("文本不符: %q", got.Text)
		}
		if len(got.Entities) != 5 {
			t.Fatalf("应为卡片 3 实体 + 正文引用块 + 原实体，得到 %d 个", len(got.Entities))
		}
		bodyOffset := newUnitMapper(cardText + "\n\n").units
		quote, ok := got.Entities[3].(*tg.MessageEntityBlockquote)
		if !ok || quote.Offset != bodyOffset || quote.Length != 5 {
			t.Fatalf("正文引用块应随卡片前插平移且不覆盖脚注: %#v", got.Entities[3])
		}
		wantHTML := `<blockquote><b>🔗 原消息</b>` + "\n" +
			`<a href="https://t.me/example_channel/42">https://t.me/example_channel/42</a></blockquote>` +
			"\n\n<blockquote><b>hello</b></blockquote>" +
			"\n\n<blockquote>📢 <b>频道</b>：\n<a href=\"https://t.me/chan\">@chan</a></blockquote>"
		if html := got.RenderHTML(); html != wantHTML {
			t.Fatalf("HTML 不符\nwant: %s\ngot:  %s", wantHTML, html)
		}
	})

	t.Run("空正文不加引用块", func(t *testing.T) {
		got := (Caption{}).WithQuotedBody()
		if got.Text != "" || len(got.Entities) != 0 {
			t.Fatalf("空正文应原样返回: %+v", got)
		}
	})

	t.Run("MTProto 截断时引用块收缩到预算边界", func(t *testing.T) {
		long := strings.Repeat("a", maxCaptionUnits+100)
		got := Caption{Text: long}.WithQuotedBody().Limited()
		effective := maxCaptionUnits - truncateNoteUnits
		if len(got.Entities) != 1 {
			t.Fatalf("截断后应只剩收缩的引用块，得到 %d 个", len(got.Entities))
		}
		quote, ok := got.Entities[0].(*tg.MessageEntityBlockquote)
		if !ok || quote.Offset != 0 || quote.Length != effective {
			t.Fatalf("引用块应收缩到预算 %d: %#v", effective, got.Entities[0])
		}
	})

	t.Run("HTML 路径截断后引用块仍完整闭合", func(t *testing.T) {
		got := Caption{Text: strings.Repeat("a", maxCaptionUnits+100)}.WithQuotedBody().RenderHTML()
		if strings.Count(got, "<blockquote>") != 1 || strings.Count(got, "</blockquote>") != 1 {
			t.Fatalf("截断后引用块标签应成对出现一次: %q", got)
		}
		if !strings.HasSuffix(got, truncateNote) {
			t.Fatalf("应保留截断提示: %q", got)
		}
	})
}

func TestCaptionWithNote(t *testing.T) {
	t.Run("空附注原样返回", func(t *testing.T) {
		c := Caption{Text: "正文"}
		if got := c.WithNote(""); got.Text != "正文" || len(got.Entities) != 0 {
			t.Fatalf("空附注不应改动 caption: %+v", got)
		}
	})

	t.Run("尾部追加不平移实体", func(t *testing.T) {
		c := Caption{Text: "正文", Entities: []tg.MessageEntityClass{
			&tg.MessageEntityBold{Offset: 0, Length: 4},
		}}
		got := c.WithNote("附注")
		if got.Text != "正文\n\n附注" {
			t.Fatalf("附注应追加在文本尾部: %q", got.Text)
		}
		bold, ok := got.Entities[0].(*tg.MessageEntityBold)
		if !ok || bold.Offset != 0 || bold.Length != 4 {
			t.Fatalf("既有实体偏移不应变化: %#v", got.Entities[0])
		}
	})

	t.Run("与来源卡片/脚注构建器串联", func(t *testing.T) {
		got := Caption{Text: "正文"}.
			WithSourceLink("https://t.me/example/7").
			WithNote("已分为 3 段")
		if !strings.HasPrefix(got.Text, "🔗 原消息") {
			t.Fatalf("附注应在来源卡片之后: %q", got.Text)
		}
		if !strings.HasSuffix(got.Text, "已分为 3 段") {
			t.Fatalf("附注应在最末: %q", got.Text)
		}
		if len(got.Entities) != 3 {
			t.Fatalf("来源卡片实体应保留: %d", len(got.Entities))
		}
	})
}

func TestMergeCaptions(t *testing.T) {
	t.Run("按源顺序空行连接并保留全部实体", func(t *testing.T) {
		got := MergeCaptions([]Caption{
			{Text: "图一", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 4}}},
			{Text: "图二", Entities: []tg.MessageEntityClass{&tg.MessageEntityItalic{Offset: 0, Length: 4}}},
		})
		if got.Text != "图一\n\n图二" {
			t.Fatalf("应按源顺序空行连接: %q", got.Text)
		}
		if len(got.Entities) != 2 {
			t.Fatalf("应保留全部实体: %+v", got.Entities)
		}
		bold, ok := got.Entities[0].(*tg.MessageEntityBold)
		if !ok || bold.Offset != 0 || bold.Length != 4 {
			t.Errorf("组首实体偏移应不变: %#v", got.Entities[0])
		}
		italic, ok := got.Entities[1].(*tg.MessageEntityItalic)
		if !ok || italic.Offset != 4 || italic.Length != 4 { // "图一"(2) + "\n\n"(2)
			t.Errorf("后续实体应平移到前缀之后（含分隔符）: %#v", got.Entities[1])
		}
	})

	t.Run("星界字符前缀按双 unit 平移", func(t *testing.T) {
		got := MergeCaptions([]Caption{
			{Text: "😀ab"}, // 6 UTF-16 units（emoji 2 + a/b 各 2？否——ASCII 各 1，共 4）
			{Text: "cd", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 2}}},
		})
		if got.Text != "😀ab\n\ncd" {
			t.Fatalf("合并文本不符: %q", got.Text)
		}
		bold, ok := got.Entities[0].(*tg.MessageEntityBold)
		if !ok || bold.Offset != 6 { // 4（😀ab）+ 2（\n\n）
			t.Errorf("emoji 前缀后实体应按 UTF-16 平移 6: %#v", got.Entities[0])
		}
	})

	t.Run("空 caption 跳过", func(t *testing.T) {
		got := MergeCaptions([]Caption{
			{},
			{Text: "正文"},
			{},
		})
		if got.Text != "正文" || len(got.Entities) != 0 {
			t.Fatalf("空 caption 应跳过: %+v", got)
		}
		if MergeCaptions(nil).Text != "" {
			t.Fatal("空输入应返回零值 caption")
		}
	})

	t.Run("频道脚注只保留首个携带者", func(t *testing.T) {
		links := []ChannelLink{{Label: "频道", URL: "https://t.me/c"}}
		got := MergeCaptions([]Caption{
			{Text: "一", Channels: links},
			{Text: "二", Channels: []ChannelLink{{Label: "其他", URL: "https://t.me/x"}}},
		})
		if len(got.Channels) != 1 || got.Channels[0].Label != "频道" {
			t.Fatalf("应只保留组首脚注: %+v", got.Channels)
		}
		html := got.RenderHTML()
		if strings.Count(html, "t.me/c") != 1 || strings.Contains(html, "t.me/x") {
			t.Fatalf("脚注应恰出现一次且为组首的: %q", html)
		}
	})

	t.Run("合并不截断，预算仍由渲染入口执行", func(t *testing.T) {
		long := strings.Repeat("a", maxCaptionUnits)
		got := MergeCaptions([]Caption{{Text: long}, {Text: long}})
		if newUnitMapper(got.Text).units != 2*maxCaptionUnits+2 {
			t.Fatalf("合并不应截断: %d", newUnitMapper(got.Text).units)
		}
		if lim := got.Limited(); newUnitMapper(lim.Text).units > maxCaptionUnits {
			t.Fatalf("Limited 应执行预算: %d", newUnitMapper(lim.Text).units)
		}
	})

	t.Run("输入 caption 不被修改", func(t *testing.T) {
		first := Caption{Text: "一", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 1}}}
		second := Caption{Text: "二", Entities: []tg.MessageEntityClass{&tg.MessageEntityItalic{Offset: 0, Length: 1}}}
		MergeCaptions([]Caption{first, second})
		if first.Entities[0].(*tg.MessageEntityBold).Offset != 0 ||
			second.Entities[0].(*tg.MessageEntityItalic).Offset != 0 {
			t.Fatal("合并不得修改输入实体偏移")
		}
	})
}
