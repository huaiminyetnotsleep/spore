package cloudarchive

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// PlanInput 是路径构建的输入。message.Item 不携带源消息
// Date，且 photo 命名/相册聚合发生在 worker 层，故本包定义自己的输入
// 结构，由调用方（queue 云盘任务）从 tmeurl.SourceRef + message.Item +
// 源消息 Date 组装，message 包保持不动。
type PlanInput struct {
	ChannelName string    // 频道标识（公开频道 username；私有频道 -100 前缀数字 ID 文本）
	Date        time.Time // 源消息时间（取其日期部分，YYYY-MM-DD）
	MessageID   int       // 源消息 ID（相册文件夹名）
	Media       []PlanMedia
	Caption     string // 消息文字（相册为成员 caption 依序拼接）；非空时写 caption.txt
}

// PlanMedia 是路径构建视角的单个媒体描述。
type PlanMedia struct {
	FileName string // 已知文件名；photo 一类无名媒体留空
	IsPhoto  bool   // 无文件名的 photo 按 photo-{msgid}-{i}.jpg 命名
}

// PlanFile 是规划出的一个远端文件。
type PlanFile struct {
	RemotePath string // 完整远端路径（含 path_prefix，不含 "<name>:" 远程前缀）
	FileName   string // 文件名部分（诊断与 cloud_uploads.file_name 落库用）
	// MediaIndex 指向 PlanInput.Media 的下标；caption.txt 为 -1。
	MediaIndex int
	IsCaption  bool
}

// Plan 是一次消息的远端布局结果。Grouped 表示"成夹"布局（相册或带配文）；
// 展示侧据此把多条文件合并为一行目录（相册合并确认文案）。
type Plan struct {
	Dir     string // 消息所在目录（含 path_prefix，无尾斜杠）；平铺时即日期目录
	Grouped bool   // 多媒体或带配文 → {日期}/{msgid}/ 文件夹布局
	Files   []PlanFile
}

// CaptionFileName 是配文文件名（UTF-8 纯文本）。
const CaptionFileName = "caption.txt"

// nameMaxRunes 是单个文件名/频道名的长度上限（按字符截断，保持 UTF-8 安全）。
const nameMaxRunes = 120

// BuildPlan 规划一条消息（或相册）的远端布局：
//   - 目录：{path_prefix}/{频道名}/{YYYY-MM-DD}/；
//   - 多个媒体或配文非空 → {日期}/{msgid}/ 文件夹：媒体按组内顺序加
//     01_、02_… 序号前缀，配文非空时另写 caption.txt；
//   - 单媒体且无配文 → 直接平铺 {日期}/{文件名}；
//   - photo 无文件名 → photo-{msgid}-{i}.jpg（i 为媒体序号，1 起）；
//   - 同一目录内同名冲突 → 追加 -2、-3 后缀（扩展名前插入；
//     MEGA 允许同名共存，必须主动保证唯一；
//   - 频道名/文件名 sanitize：剔除路径分隔符与控制字符，超长按字符截断。
func BuildPlan(pathPrefix string, in PlanInput) Plan {
	date := in.Date.Format("2006-01-02")
	parts := make([]string, 0, 4)
	for _, p := range []string{pathPrefix, sanitizeSegment(in.ChannelName, "channel"), date} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	dayDir := strings.Join(parts, "/")
	plan := Plan{Dir: dayDir}

	grouped := len(in.Media) > 1 || strings.TrimSpace(in.Caption) != ""
	used := make(map[string]int, len(in.Media)+1) // 目录内已用名 → 已出现次数
	if grouped {
		msgDir := fmt.Sprintf("%s/%d", dayDir, in.MessageID)
		plan.Dir = msgDir
		plan.Grouped = true
		for i, m := range in.Media {
			name := sanitizeSegment(mediaFileName(in, i, m), "file")
			name = fmt.Sprintf("%02d_%s", i+1, name)
			name = dedupeName(name, used)
			plan.Files = append(plan.Files, PlanFile{
				RemotePath: msgDir + "/" + name,
				FileName:   name,
				MediaIndex: i,
			})
		}
		if strings.TrimSpace(in.Caption) != "" {
			capName := dedupeName(CaptionFileName, used)
			plan.Files = append(plan.Files, PlanFile{
				RemotePath: msgDir + "/" + capName,
				FileName:   capName,
				MediaIndex: -1,
				IsCaption:  true,
			})
		}
		return plan
	}

	// 单媒体无配文：平铺。
	if len(in.Media) == 1 {
		name := dedupeName(sanitizeSegment(mediaFileName(in, 0, in.Media[0]), "file"), used)
		plan.Files = append(plan.Files, PlanFile{
			RemotePath: dayDir + "/" + name,
			FileName:   name,
			MediaIndex: 0,
		})
	}
	return plan
}

// mediaFileName 生成单个媒体的文件名：photo 无名 → photo-{msgid}-{i}.jpg。
func mediaFileName(in PlanInput, i int, m PlanMedia) string {
	if m.FileName == "" && m.IsPhoto {
		return fmt.Sprintf("photo-%d-%d.jpg", in.MessageID, i+1)
	}
	return m.FileName
}

// sanitizeSegment 清理路径段：剔除路径分隔符与控制字符、各类空白折叠为
// 普通空格、去首尾空白与尾部点号；清理后为空回落 fallback；超长按字符
// 截断并尽量保留扩展名（可读性），截断结果仍非法时回落 fallback。
func sanitizeSegment(s, fallback string) string {
	cleaned := sanitizeChars(s)
	if cleaned == "" {
		return fallback
	}
	if utf8.RuneCountInString(cleaned) <= nameMaxRunes {
		return cleaned
	}
	base, ext := splitExt(cleaned)
	maxBase := nameMaxRunes - utf8.RuneCountInString(ext)
	if maxBase < 1 { // 扩展名本身超长：放弃保留，整体截断
		base, ext, maxBase = cleaned, "", nameMaxRunes
	}
	out := truncateRunes(base, maxBase)
	if out == "" {
		return fallback
	}
	return out + ext
}

// sanitizeChars 做字符级清理（剔除分隔符/控制字符、空白折叠、去首尾
// 空白与尾部点号）；结果可能为空串。
func sanitizeChars(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '/' || r == '\\':
			// 路径分隔符剔除（文件名不得携带目录结构）
		case r < 0x20 || r == 0x7f:
			// 控制字符剔除
		case unicode.IsSpace(r):
			b.WriteRune(' ') // 各类空白折叠为普通空格，避免目录名含换行等
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(strings.TrimSpace(b.String()), ".")
}

// splitExt 在最后一个点处切分扩展名；无扩展名（点在首位/末位/不存在）时
// 返回原串与空扩展。
func splitExt(s string) (base, ext string) {
	dot := strings.LastIndexByte(s, '.')
	if dot <= 0 || dot == len(s)-1 {
		return s, ""
	}
	return s[:dot], s[dot:]
}

// truncateRunes 按字符数截断（UTF-8 安全），截断后清尾空白与点号。
func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return strings.TrimRight(strings.TrimSpace(string([]rune(s)[:max])), ".")
}

// dedupeName 在同一目录内保证文件名唯一：首次出现原样返回，此后在
// 扩展名前插入 -2、-3… 序号（无扩展名则直接追加）；生成的名字本身
// 也计入已用集合（a.txt、a-2.txt、a.txt 共存时第三个得 a-3.txt）。
func dedupeName(name string, used map[string]int) string {
	if _, seen := used[name]; !seen {
		used[name]++
		return name
	}
	ext := ""
	base := name
	if dot := strings.LastIndexByte(name, '.'); dot > 0 {
		base, ext = name[:dot], name[dot:]
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s-%d%s", base, n, ext)
		if _, taken := used[cand]; !taken {
			used[cand]++
			return cand
		}
	}
}
