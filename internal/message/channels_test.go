package message

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

func TestChannelFooterHTML(t *testing.T) {
	links := []ChannelLink{
		{Label: "@pub", URL: "https://t.me/pub"},
		{Label: "我的私有频道", URL: "https://t.me/c/1234567890/1"},
	}
	got := channelFooterHTML(links)
	want := "\n\n📢 <b>频道</b>：\n<a href=\"https://t.me/pub\">@pub</a>\n" +
		`<a href="https://t.me/c/1234567890/1">我的私有频道</a>`
	if got != want {
		t.Fatalf("脚注 HTML 不符:\nwant: %q\ngot:  %q", want, got)
	}

	// Label/URL 中的 HTML 敏感字符必须转义
	evil := channelFooterHTML([]ChannelLink{{Label: `<b>x</b>`, URL: `https://t.me/a?u=1&v=2`}})
	if strings.Contains(evil, "<b>x</b></a>") {
		t.Fatalf("Label 未转义: %q", evil)
	}
	if !strings.Contains(evil, `&amp;v=2`) {
		t.Fatalf("URL 未转义: %q", evil)
	}
}

func TestCaptionRenderHTMLWithFooterBudget(t *testing.T) {
	links := []ChannelLink{{Label: "@pub", URL: "https://t.me/pub"}}

	// 短文本：正文 + 脚注都完整保留
	c := Caption{Text: "hello"}.WithChannels(links)
	got := c.RenderHTML()
	if !strings.HasPrefix(got, "hello") || !strings.Contains(got, `href="https://t.me/pub"`) {
		t.Fatalf("短文本应保留正文与脚注: %q", got)
	}

	// 预算让位：正文恰好占满（1024 - 截断附注预留）时，无脚注不截断；
	// 有脚注则正文让位被截断
	full := Caption{Text: strings.Repeat("汉", maxCaptionUnits-truncateNoteUnits)}
	if got := full.RenderHTML(); strings.Contains(got, truncateNote) {
		t.Fatal("无脚注时恰满预算不应截断")
	}
	if got := full.WithChannels(links).RenderHTML(); !strings.Contains(got, truncateNote) {
		t.Fatal("有脚注时应为脚注让出预算、正文被截断")
	}

	// 截断后脚注仍完整存在（含截断附注与脚注的总量在预算内）
	if got := full.WithChannels(links).RenderHTML(); !strings.Contains(got, `href="https://t.me/pub"`) {
		t.Fatalf("截断后脚注不应丢失: %q", got)
	}
}

func TestCaptionLimitedWithFooter(t *testing.T) {
	links := []ChannelLink{
		{Label: "@pub", URL: "https://t.me/pub"},
		{Label: "🎬电影", URL: "https://t.me/c/1234567890/1"},
	}

	t.Run("短文本：脚注文本与实体按前缀偏移追加", func(t *testing.T) {
		c := Caption{Text: "hello"}.WithChannels(links).Limited()
		wantText := "hello" + channelFooterText(links)
		if c.Text != wantText {
			t.Fatalf("Limited 文本不符:\nwant: %q\ngot:  %q", wantText, c.Text)
		}
		base := newUnitMapper("hello").units

		// 第一个实体是脚注的加粗标签
		bold, ok := c.Entities[0].(*tg.MessageEntityBold)
		if !ok {
			t.Fatalf("第一个实体应为 Bold: %T", c.Entities[0])
		}
		labelStart := newUnitMapper("\n\n").units
		labelEnd := newUnitMapper("\n\n📢 频道").units
		if bold.Offset != base+labelStart || bold.Length != labelEnd-labelStart {
			t.Fatalf("Bold 标签偏移不符: offset=%d length=%d", bold.Offset, bold.Length)
		}

		// 两个 TextURL：@pub 与 🎬电影（星界字符占双 unit，偏移须精确）
		turl1, ok := c.Entities[1].(*tg.MessageEntityTextURL)
		if !ok || turl1.URL != "https://t.me/pub" {
			t.Fatalf("第二个实体应为 @pub 的 TextURL: %#v", c.Entities[1])
		}
		wantOffset1 := base + newUnitMapper("\n\n📢 频道：").units + newUnitMapper(footerSeparator).units
		if turl1.Offset != wantOffset1 || turl1.Length != newUnitMapper("@pub").units {
			t.Fatalf("@pub 偏移不符: offset=%d length=%d", turl1.Offset, turl1.Length)
		}
		turl2, ok := c.Entities[2].(*tg.MessageEntityTextURL)
		if !ok || turl2.URL != "https://t.me/c/1234567890/1" {
			t.Fatalf("第三个实体应为私有频道的 TextURL: %#v", c.Entities[2])
		}
		wantOffset2 := wantOffset1 + newUnitMapper("@pub").units + newUnitMapper(footerSeparator).units
		if turl2.Offset != wantOffset2 || turl2.Length != newUnitMapper("🎬电影").units {
			t.Fatalf("私有频道偏移不符: offset=%d length=%d", turl2.Offset, turl2.Length)
		}
	})

	t.Run("长文本截断：原实体让位、脚注完整", func(t *testing.T) {
		text := strings.Repeat("汉", 1100)
		c := Caption{
			Text:     text,
			Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 5}},
		}.WithChannels(links).Limited()

		if !strings.Contains(c.Text, truncateNote) || !strings.Contains(c.Text, "📢 频道") {
			t.Fatalf("应有截断附注且保留脚注: %q", c.Text[:60]+"...")
		}
		if u := newUnitMapper(c.Text).units; u > maxCaptionUnits {
			t.Fatalf("Limited 总 unit 数超限: %d > %d", u, maxCaptionUnits)
		}
		// 原实体（0..5）仍在保留区间内应保留 + 脚注的 3 个实体
		if len(c.Entities) != 4 {
			t.Fatalf("应有原实体 1 个 + 脚注 3 个，得到 %d", len(c.Entities))
		}
		if bold, ok := c.Entities[0].(*tg.MessageEntityBold); !ok || bold.Offset != 0 || bold.Length != 5 {
			t.Fatalf("原实体应保留: %#v", c.Entities[0])
		}
	})

	t.Run("无脚注：行为与既有语义一致", func(t *testing.T) {
		c := Caption{Text: "hello"}.Limited()
		if c.Text != "hello" || len(c.Entities) != 0 {
			t.Fatalf("无脚注不应追加内容: %q", c.Text)
		}
	})
}
