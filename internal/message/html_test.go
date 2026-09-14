package message

import (
	"strings"
	"testing"

	"github.com/gotd/td/tg"
)

// ToHTML 测试便利包装：以默认消息上限渲染（生产代码无此函数，全部走 ToHTMLLimited）。
func ToHTML(text string, entities []tg.MessageEntityClass) string {
	return ToHTMLLimited(text, entities, maxMessageUnits)
}

func TestToHTMLEscapePlain(t *testing.T) {
	got := ToHTML(`<script>alert("x")</script>`, nil)
	want := "&lt;script&gt;alert(&#34;x&#34;)&lt;/script&gt;"
	if got != want {
		t.Errorf("纯文本需转义\nwant: %s\ngot:  %s", want, got)
	}
}

func TestToHTMLBoldCJK(t *testing.T) {
	got := ToHTML("你好世界", []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 0, Length: 2}, // 前两个字
	})
	if got != "<b>你好</b>世界" {
		t.Errorf("got: %s", got)
	}
}

func TestToHTMLEmojiAstralOffset(t *testing.T) {
	// 😀 占 2 个 UTF-16 unit：bold 覆盖 [0,2) 应恰好包住 emoji
	got := ToHTML("😀hi", []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 0, Length: 2},
	})
	if got != "<b>😀</b>hi" {
		t.Errorf("星界字符偏移换算错误\ngot: %s", got)
	}
}

func TestToHTMLEntityAfterEmoji(t *testing.T) {
	// 😀(2 units) + 你好：italic 从 unit 2 开始
	got := ToHTML("😀你好", []tg.MessageEntityClass{
		&tg.MessageEntityItalic{Offset: 2, Length: 2},
	})
	if got != "😀<i>你好</i>" {
		t.Errorf("got: %s", got)
	}
}

func TestToHTMLNested(t *testing.T) {
	// bold 覆盖全文，italic 只覆盖后三个字符：合法嵌套应为 <b>abc<i>def</i></b>
	got := ToHTML("abcdef", []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 0, Length: 6},
		&tg.MessageEntityItalic{Offset: 3, Length: 3},
	})
	want := "<b>abc<i>def</i></b>"
	if got != want {
		t.Fatalf("want: %s\ngot:  %s", want, got)
	}
}

func TestToHTMLTextURLEscaped(t *testing.T) {
	got := ToHTML("click", []tg.MessageEntityClass{
		&tg.MessageEntityTextURL{Offset: 0, Length: 5, URL: `https://ex.com/?a="q"`},
	})
	want := `<a href="https://ex.com/?a=&#34;q&#34;">click</a>`
	if got != want {
		t.Errorf("\nwant: %s\ngot:  %s", want, got)
	}
}

func TestToHTMLRawURLUsesSource(t *testing.T) {
	got := ToHTML("https://t.me/a", []tg.MessageEntityClass{
		&tg.MessageEntityURL{Offset: 0, Length: 14},
	})
	want := `<a href="https://t.me/a">https://t.me/a</a>`
	if got != want {
		t.Errorf("got: %s", got)
	}
}

func TestToHTMLPreWithLanguage(t *testing.T) {
	// Bot API 要求 class 挂在内层 <code> 上，不能是 <pre class>
	got := ToHTML("go fmt", []tg.MessageEntityClass{
		&tg.MessageEntityPre{Offset: 0, Length: 6, Language: "go"},
	})
	want := `<pre><code class="language-go">go fmt</code></pre>`
	if got != want {
		t.Errorf("got: %s", got)
	}
}

func TestToHTMLUnknownEntityDropped(t *testing.T) {
	// 不支持的实体类型退化为纯文本，不产生标签也不报错
	got := ToHTML("plain", []tg.MessageEntityClass{
		&tg.MessageEntityMentionName{Offset: 0, Length: 5, UserID: 1},
	})
	if got != "plain" {
		t.Errorf("got: %s", got)
	}
}

func TestToHTMLOutOfRangeDropped(t *testing.T) {
	got := ToHTML("abc", []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 10, Length: 3},
	})
	if got != "abc" {
		t.Errorf("越界实体应丢弃，got: %s", got)
	}
}

func TestToHTMLTruncation(t *testing.T) {
	long := strings.Repeat("字", 5000) // 5000 units > 4096 上限
	truncated := ToHTMLLimited(long, nil, maxMessageUnits)
	if !strings.HasSuffix(truncated, truncateNote) {
		t.Fatal("超限文本应附截断说明")
	}
	// 关键约束：正文+附注整体不得超出上限（否则 Bot API 直接拒绝）
	total := newUnitMapper(truncated).units
	if total > maxMessageUnits {
		t.Errorf("截断结果超限：%d units（上限 %d）", total, maxMessageUnits)
	}
}

func TestItemRenderHTMLWithSource(t *testing.T) {
	item := Item{Text: "正文", Entities: []tg.MessageEntityClass{
		&tg.MessageEntityItalic{Offset: 0, Length: 2},
	}}
	got := item.RenderHTMLWithSource("https://t.me/example_channel/42", nil)
	want := `<blockquote><b>🔗 原消息</b>` + "\n" +
		`<a href="https://t.me/example_channel/42">https://t.me/example_channel/42</a></blockquote>` +
		"\n\n<blockquote><i>正文</i></blockquote>"
	if got != want {
		t.Fatalf("来源链接应位于正文顶部\nwant: %s\ngot:  %s", want, got)
	}
}

func TestItemRenderHTMLWithSourceKeepsLinkWhenTruncated(t *testing.T) {
	item := Item{Text: strings.Repeat("字", maxMessageUnits+100)}
	got := item.RenderHTMLWithSource("https://t.me/example_channel/42", nil)
	wantPrefix := `<blockquote><b>🔗 原消息</b>` + "\n" +
		`<a href="https://t.me/example_channel/42">https://t.me/example_channel/42</a></blockquote>` + "\n\n"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("截断后必须保留置顶来源链接: %q", got)
	}
	if !strings.HasSuffix(got, truncateNote) {
		t.Fatalf("超长正文应带截断提示: %q", got)
	}
}

func TestToHTMLZeroLengthEntitySkipped(t *testing.T) {
	got := ToHTML("abc", []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 1, Length: 0},
	})
	if got != "abc" {
		t.Errorf("零长实体应忽略，got: %s", got)
	}
}
