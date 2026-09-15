// Package tmeurl 解析 t.me 消息链接（逐条移植自旧 src/telegram/links.ts）。
//
// 对外暴露 Parse / ParseAll 与 SourceRef——与旧实现一致：纯函数、不触网、不验证频道存在性。
package tmeurl

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// PeerKind 标记链接指向的聊天形态。
type PeerKind int

const (
	PeerUsername  PeerKind = iota // 公开频道/群组（@username）
	PeerChannelID                 // 私有频道（裸内部 ID）
)

// SourceRef 统一消息定位符。
type SourceRef struct {
	Kind      PeerKind
	Username  string // Kind==PeerUsername 时有效，不带 @ 前缀
	ChannelID int64  // Kind==PeerChannelID 时有效，存裸正 ID（非 -100 形式）
	MessageID int
}

// String 人可读形式，用于调试输出与 M1 回显。
func (r SourceRef) String() string {
	if r.Kind == PeerChannelID {
		return fmt.Sprintf("频道 %d 消息 %d", r.ChannelID, r.MessageID)
	}
	return fmt.Sprintf("@%s 消息 %d", r.Username, r.MessageID)
}

// URL 返回规范化的 Telegram 原消息链接；结构化字段无效时返回空值。
func (r SourceRef) URL() (string, bool) {
	if r.MessageID <= 0 {
		return "", false
	}
	switch r.Kind {
	case PeerUsername:
		if !usernamePattern.MatchString(r.Username) {
			return "", false
		}
		return "https://t.me/" + r.Username + "/" + strconv.Itoa(r.MessageID), true
	case PeerChannelID:
		if r.ChannelID <= 0 {
			return "", false
		}
		return "https://t.me/c/" + strconv.FormatInt(r.ChannelID, 10) + "/" + strconv.Itoa(r.MessageID), true
	default:
		return "", false
	}
}

var (
	// candidatePattern 移植 TS CANDIDATE_PATTERN 并移除 lookbehind（RE2 不支持），
	// 边界检查改由 Parse 在匹配起点前回查字符，见 boundaryForbidden。
	candidatePattern = regexp.MustCompile(`(?i)(?:https?://)?(?:www\.)?(?:t\.me|telegram\.me)/[^\s<>()]+`)
	usernamePattern  = regexp.MustCompile(`^[A-Za-z0-9_]{4,64}$`)
	idPattern        = regexp.MustCompile(`^\d{1,16}$`)
	schemePattern    = regexp.MustCompile(`(?i)^https?://`) // 对齐 TS 的 /^https?:\/\//i：大写 scheme 同样接受
)

// Parse 提取文本中第一条能成功解析的 Telegram 消息链接。
func Parse(text string) (SourceRef, bool) {
	refs := ParseAll(text)
	if len(refs) == 0 {
		return SourceRef{}, false
	}
	return refs[0], true
}

// ParseAll 返回文本中全部能成功解析的 Telegram 消息链接，按出现顺序排列；
// 供调用方批量处理一条消息中的多个有效链接。
func ParseAll(text string) []SourceRef {
	var refs []SourceRef
	for _, loc := range candidatePattern.FindAllStringIndex(text, -1) {
		start := loc[0]
		if start > 0 {
			prev, _ := utf8.DecodeLastRuneInString(text[:start])
			if boundaryForbidden(prev) {
				continue
			}
		}
		if ref, ok := parseCandidate(text[start:loc[1]]); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

// boundaryForbidden 对应 TS 正则的 lookbehind (?<![A-Za-z0-9_./-])：
// 候选紧邻的前一字符属于该集合时（如 "evil.t.me"、"axhttps://"），视为其他词的一部分而跳过。
func boundaryForbidden(prev rune) bool {
	switch {
	case prev >= 'a' && prev <= 'z',
		prev >= 'A' && prev <= 'Z',
		prev >= '0' && prev <= '9',
		prev == '_', prev == '.', prev == '/', prev == '-':
		return true
	}
	return false
}

func positiveID(s string) (int, bool) {
	if !idPattern.MatchString(s) {
		return 0, false
	}
	// 16 位十进制上限天然落在 int64 与 JS 安全整数范围内，语义与 TS 一致。
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return int(n), true
}

func parseCandidate(candidate string) (SourceRef, bool) {
	// 链接常嵌在中文引号、书名号或句末省略号中；统一移除末尾 Unicode 标点，
	// 避免这些字符被误并入消息 ID。候选本身不含空白，但一并处理更稳妥。
	cleaned := strings.TrimRightFunc(candidate, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})
	withScheme := cleaned
	if !schemePattern.MatchString(cleaned) {
		withScheme = "https://" + cleaned
	}

	u, err := url.Parse(withScheme)
	if err != nil || u.Host == "" {
		return SourceRef{}, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return SourceRef{}, false
	}
	switch strings.ToLower(u.Hostname()) {
	case "t.me", "www.t.me", "telegram.me", "www.telegram.me":
	default:
		return SourceRef{}, false
	}

	segments := make([]string, 0, 4)
	for _, p := range strings.Split(u.Path, "/") {
		if p != "" {
			segments = append(segments, p)
		}
	}
	// 兼容 /s/ 前缀；query/fragment 天然被 u.Path 忽略（?single 等）
	if len(segments) > 0 && strings.EqualFold(segments[0], "s") {
		segments = segments[1:]
	}
	if len(segments) == 0 {
		return SourceRef{}, false
	}

	// 私有频道：/c/<裸ID>/<消息ID>，两段都必须是正整数（至多 16 位数字）
	if strings.EqualFold(segments[0], "c") {
		if len(segments) != 3 {
			return SourceRef{}, false
		}
		channelID, ok := positiveID(segments[1])
		if !ok {
			return SourceRef{}, false
		}
		msgID, ok := positiveID(segments[2])
		if !ok {
			return SourceRef{}, false
		}
		return SourceRef{Kind: PeerChannelID, ChannelID: int64(channelID), MessageID: msgID}, true
	}

	// 公开聊天：/<username>/<消息ID>
	if len(segments) != 2 || !usernamePattern.MatchString(segments[0]) {
		return SourceRef{}, false
	}
	msgID, ok := positiveID(segments[1])
	if !ok {
		return SourceRef{}, false
	}
	return SourceRef{Kind: PeerUsername, Username: segments[0], MessageID: msgID}, true
}
