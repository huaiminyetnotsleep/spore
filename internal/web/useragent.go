package web

// 管理后台登录活动通知：登录成功后异步推送一条含关键信息
//（时间/IP/操作系统/浏览器/登录方式）的活动通知（web.admin_login）。
// 通知失败只记日志，绝不影响登录主链路。

import (
	"net/http"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/notify"
)

// parseUserAgent 从 User-Agent 识别常见桌面/移动平台与浏览器
// （展示用，不带版本号）。判定顺序重要：Chrome/Edge 的 UA 均携带 Safari 标记，
// 必须先判 Edge、Chrome 再判 Safari。
func parseUserAgent(ua string) (osName, browser string) {
	osName, browser = "未知", "未知"
	switch {
	case strings.Contains(ua, "Windows"):
		osName = "Windows"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		osName = "iOS"
	case strings.Contains(ua, "Android"):
		osName = "Android"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		osName = "macOS"
	case strings.Contains(ua, "Linux"), strings.Contains(ua, "X11"):
		osName = "Linux"
	}
	switch {
	case strings.Contains(ua, "Edg/"), strings.Contains(ua, "Edge"):
		browser = "Edge"
	case strings.Contains(ua, "OPR/"), strings.Contains(ua, "Opera"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/"), strings.Contains(ua, "FxiOS"):
		browser = "Firefox"
	case strings.Contains(ua, "CriOS"), strings.Contains(ua, "Chrome/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	return osName, browser
}

// notifyAdminLogin 异步推送管理后台登录成功活动通知。
// method 为登录方式的可读标签（访问密钥 / GitHub）；ip 已由调用方解析。
func (s *Server) notifyAdminLogin(r *http.Request, method, ip string) {
	if s.hub == nil {
		return
	}
	osName, browser := parseUserAgent(r.UserAgent())
	go s.hub.AdminLogin(r.Context(), notify.AdminLoginData{
		Method: method, IP: ip, OS: osName, Browser: browser,
	})
}
