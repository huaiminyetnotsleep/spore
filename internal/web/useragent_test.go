package web

// 登录活动通知的 User-Agent 解析测试：常见桌面/移动浏览器与未知回退。

import "testing"

func TestParseUserAgent(t *testing.T) {
	cases := []struct {
		name    string
		ua      string
		wantOS  string
		wantWeb string
	}{
		{
			name:    "macOS Chrome",
			ua:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			wantOS:  "macOS",
			wantWeb: "Chrome",
		},
		{
			name:    "Windows Edge",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0",
			wantOS:  "Windows",
			wantWeb: "Edge",
		},
		{
			name:    "Windows Firefox",
			ua:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:127.0) Gecko/20100101 Firefox/127.0",
			wantOS:  "Windows",
			wantWeb: "Firefox",
		},
		{
			name:    "Android Chrome",
			ua:      "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36",
			wantOS:  "Android",
			wantWeb: "Chrome",
		},
		{
			name:    "iOS Safari",
			ua:      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
			wantOS:  "iOS",
			wantWeb: "Safari",
		},
		{
			name:    "curl 工具",
			ua:      "curl/8.4.0",
			wantOS:  "未知",
			wantWeb: "未知",
		},
		{
			name:    "空 UA",
			ua:      "",
			wantOS:  "未知",
			wantWeb: "未知",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			osName, browser := parseUserAgent(tc.ua)
			if osName != tc.wantOS || browser != tc.wantWeb {
				t.Errorf("parseUserAgent(%q)=%q,%q, want %q,%q", tc.ua, osName, browser, tc.wantOS, tc.wantWeb)
			}
		})
	}
}
