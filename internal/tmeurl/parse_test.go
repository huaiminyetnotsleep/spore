package tmeurl

import "testing"

// 用例全量移植自 tests/links.test.ts，另补 Go/RE2 特有回归。

func assertRef(t *testing.T, got SourceRef, ok bool, want SourceRef) {
	t.Helper()
	if !ok {
		t.Fatalf("期望解析成功，实际失败")
	}
	if got.Kind != want.Kind || got.Username != want.Username ||
		got.ChannelID != want.ChannelID || got.MessageID != want.MessageID {
		t.Fatalf("解析结果不符\nwant: %+v\ngot:  %+v", want, got)
	}
}

func TestParsePublicLink(t *testing.T) {
	for _, input := range []string{
		"https://t.me/example_channel/123",
		"t.me/example_channel/123",
		"https://www.t.me/example_channel/123?single=1",
		// 大写 scheme 回归：TS 版接受（/^https?:\/\//i），Go 移植须保持
		"HTTPS://T.ME/example_channel/123",
		"Http://t.me/example_channel/123",
	} {
		got, ok := Parse(input)
		assertRef(t, got, ok, SourceRef{Kind: PeerUsername, Username: "example_channel", MessageID: 123})
	}
}

func TestParsePrivateLink(t *testing.T) {
	got, ok := Parse("https://t.me/c/1234567890/123")
	assertRef(t, got, ok, SourceRef{Kind: PeerChannelID, ChannelID: 1234567890, MessageID: 123})
}

// 短 ID 回归：确保按字符串前缀语义存储裸 ID（而非固定偏移）
func TestParsePrivateLinkShortID(t *testing.T) {
	got, ok := Parse("t.me/c/456/7")
	assertRef(t, got, ok, SourceRef{Kind: PeerChannelID, ChannelID: 456, MessageID: 7})
}

// telegram.me 官方别名域名：与 t.me 等价解析（B 缺口）。
func TestParseTelegramMe(t *testing.T) {
	for _, input := range []string{
		"https://telegram.me/example_channel/123",
		"telegram.me/example_channel/123",
		"www.telegram.me/example_channel/123",
		"https://www.telegram.me/example_channel/123?single=1",
		"HTTPS://TELEGRAM.ME/example_channel/123",
	} {
		got, ok := Parse(input)
		assertRef(t, got, ok, SourceRef{Kind: PeerUsername, Username: "example_channel", MessageID: 123})
	}

	got, ok := Parse("https://telegram.me/c/1234567890/123")
	assertRef(t, got, ok, SourceRef{Kind: PeerChannelID, ChannelID: 1234567890, MessageID: 123})
}

func TestParseSurroundingText(t *testing.T) {
	got, ok := Parse("复制这个：https://t.me/example_channel/42 谢谢")
	assertRef(t, got, ok, SourceRef{Kind: PeerUsername, Username: "example_channel", MessageID: 42})
}

func TestParseTrailingPunctuation(t *testing.T) {
	for _, input := range []string{
		"https://t.me/example_channel/42。",
		"《https://t.me/example_channel/42》",
		"https://t.me/example_channel/42”",
		"https://t.me/example_channel/42…",
	} {
		got, ok := Parse(input)
		assertRef(t, got, ok, SourceRef{Kind: PeerUsername, Username: "example_channel", MessageID: 42})
	}
}

func TestParseRejects(t *testing.T) {
	for _, input := range []string{
		"hello",
		"https://t.me/example_channel",
		"https://t.me/c/123",
		"https://t.me/c/abc/1",
		"https://t.me/joinchat/abcdef",
		"https://t.me/example_channel/0",
		"https://t.me/example_channel/7/123",
		"https://evil.example/t.me/example_channel/12",
		// Go 移植回归：lookbehind 边界
		"evil.t.me/example_channel/12",
		"x.t.me/example_channel/12",
		"axhttps://t.me/example_channel/42",
		// telegram.me 边界：紧邻字母前缀的域名应被拒（与 t.me 同规则）
		"eviltelegram.me/example_channel/12",
		"https://other.com/example_channel/12",
	} {
		if got, ok := Parse(input); ok {
			t.Errorf("%q 应被拒绝，实际解析为 %+v", input, got)
		}
	}
}

func TestSourceRefURL(t *testing.T) {
	tests := []struct {
		name string
		ref  SourceRef
		want string
		ok   bool
	}{
		{name: "公开消息", ref: SourceRef{Kind: PeerUsername, Username: "example_channel", MessageID: 42}, want: "https://t.me/example_channel/42", ok: true},
		{name: "私有消息", ref: SourceRef{Kind: PeerChannelID, ChannelID: 1234567890, MessageID: 7}, want: "https://t.me/c/1234567890/7", ok: true},
		{name: "空用户名", ref: SourceRef{Kind: PeerUsername, MessageID: 1}},
		{name: "非法用户名", ref: SourceRef{Kind: PeerUsername, Username: "bad-name", MessageID: 1}},
		{name: "空频道", ref: SourceRef{Kind: PeerChannelID, MessageID: 1}},
		{name: "空消息", ref: SourceRef{Kind: PeerUsername, Username: "example_channel"}},
		{name: "未知类型", ref: SourceRef{Kind: PeerKind(99), Username: "example_channel", MessageID: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.ref.URL()
			if got != tt.want || ok != tt.ok {
				t.Fatalf("URL() = %q, %v; want %q, %v", got, ok, tt.want, tt.ok)
			}
			if !ok {
				return
			}
			parsed, parsedOK := Parse(got)
			assertRef(t, parsed, parsedOK, tt.ref)
		})
	}
}

func TestParseAll(t *testing.T) {
	// 多链接按出现顺序全部返回（botapi 只取第一条并提示）
	refs := ParseAll("先看 https://t.me/example_channel/1 再看 https://t.me/c/1234567890/9")
	if len(refs) != 2 {
		t.Fatalf("应解析出 2 条链接，得到 %d", len(refs))
	}
	if refs[0].Username != "example_channel" || refs[0].MessageID != 1 {
		t.Errorf("第一条应为公开频道消息，得到 %+v", refs[0])
	}
	if refs[1].Kind != PeerChannelID || refs[1].ChannelID != 1234567890 || refs[1].MessageID != 9 {
		t.Errorf("第二条应为私有频道消息，得到 %+v", refs[1])
	}

	// 混入非法链接：只返回合法的
	refs = ParseAll("https://t.me/joinchat/abcdef 和 https://t.me/example_channel/2")
	if len(refs) != 1 || refs[0].MessageID != 2 {
		t.Fatalf("非法链接应被跳过，得到 %+v", refs)
	}

	// 无链接与重复链接
	if got := ParseAll("hello"); len(got) != 0 {
		t.Errorf("无链接应返回空，得到 %+v", got)
	}
	if got := ParseAll("https://t.me/example_channel/3 https://t.me/example_channel/3"); len(got) != 2 {
		t.Errorf("重复链接各自计入，应 2 条，得到 %d", len(got))
	}

	// 回归：单条链接场景 Parse 与 ParseAll 首条一致
	first, ok := Parse("https://t.me/example_channel/42")
	if !ok || first != ParseAll("https://t.me/example_channel/42")[0] {
		t.Errorf("Parse 应等价于 ParseAll 首条，得到 %+v", first)
	}
}
