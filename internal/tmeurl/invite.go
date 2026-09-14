// 邀请链接解析：t.me/+<hash>（含 telegram.me 别名与旧式 joinchat/<hash>）。
// 供 /join 命令使用，与 Parse 一样是纯函数、不触网。
package tmeurl

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// inviteHashPattern 邀请 hash：base64url 风格（大小写字母、数字、下划线、连字符）。
// 长度由 Telegram 服务端最终校验；本地只限制为非空且不超过合理上限，
// 避免因客户端/版本差异误拒绝有效邀请链接。
var inviteHashPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ParseInvite 从文本中提取邀请链接的 hash。
// 接受：https://t.me/+<hash>、t.me/+<hash>、https://telegram.me/+<hash>（含 www.）、
// https://t.me/joinchat/<hash>，以及不带链接形态的裸 hash。
// 无法识别时 ok 为 false。
func ParseInvite(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}

	// 链接形态：取路径解析（scheme 可省略，同 Parse 的处理方式）
	for _, loc := range candidatePattern.FindAllStringIndex(text, -1) {
		start := loc[0]
		if start > 0 {
			prev := text[start-1]
			if boundaryForbidden(rune(prev)) {
				continue
			}
		}
		if hash, ok := parseInviteCandidate(text[start:loc[1]]); ok {
			return hash, true
		}
	}

	// 裸 hash 形态（用户直接粘贴 hash 本体）
	cleaned := trimInviteSuffix(text)
	if inviteHashPattern.MatchString(cleaned) {
		return cleaned, true
	}
	return "", false
}

// trimInviteSuffix 移除链接末尾的自然语言标点，但保留邀请 hash 允许的
// ASCII 连字符和下划线，避免合法 token 被误截断。
func trimInviteSuffix(s string) string {
	return strings.TrimRightFunc(s, func(r rune) bool {
		if r == '-' || r == '_' {
			return false
		}
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})
}

// parseInviteCandidate 解析单个候选链接：host 限定 t.me/telegram.me，
// 路径为 /+<hash> 或 /joinchat/<hash>。
func parseInviteCandidate(candidate string) (string, bool) {
	cleaned := trimInviteSuffix(candidate)
	withScheme := cleaned
	if !schemePattern.MatchString(cleaned) {
		withScheme = "https://" + cleaned
	}
	u, err := url.Parse(withScheme)
	if err != nil || u.Host == "" {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	switch strings.ToLower(u.Hostname()) {
	case "t.me", "www.t.me", "telegram.me", "www.telegram.me":
	default:
		return "", false
	}

	segments := make([]string, 0, 3)
	for _, p := range strings.Split(u.Path, "/") {
		if p != "" {
			segments = append(segments, p)
		}
	}
	if len(segments) == 0 {
		return "", false
	}

	// t.me/+<hash>：url.Parse 会把 "+" 保留在首段路径中
	if strings.HasPrefix(segments[0], "+") {
		hash := segments[0][1:]
		if len(segments) == 1 && inviteHashPattern.MatchString(hash) {
			return hash, true
		}
		return "", false
	}
	// 旧式 t.me/joinchat/<hash>
	if strings.EqualFold(segments[0], "joinchat") {
		if len(segments) == 2 && inviteHashPattern.MatchString(segments[1]) {
			return segments[1], true
		}
	}
	return "", false
}

// MaskInviteHash 生成邀请 hash 的脱敏展示（保留首尾各 4 字符），用于页面与日志。
func MaskInviteHash(hash string) string {
	if len(hash) <= 8 {
		return strings.Repeat("*", len(hash))
	}
	return hash[:4] + "…" + hash[len(hash)-4:]
}
