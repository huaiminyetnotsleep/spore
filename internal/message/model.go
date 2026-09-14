// Package message 将 Telegram 源消息标准化为内部模型并渲染 Bot API HTML。
package message

import (
	"github.com/gotd/td/tg"
)

// AlbumMaxItems 是 Telegram 平台的相册单组上限（sendMediaGroup / GroupedID 语义共用）。
// mtproto 的邻域取数窗口、delivery 的整组发送、queue 的降级判断均引用此常量。
const AlbumMaxItems = 10

// ItemKind 媒体类型枚举。文本消息没有 Media（Media == nil），不设 KindText。
type ItemKind int

const (
	KindPhoto ItemKind = iota
	KindVideo
	KindDocument
	KindAudio
	KindVoice
	KindUnsupported
)

// VideoMeta 重发视频所需的元数据。
// SupportsStreaming 由发送侧固定开启（Telegram 会自行校验），不在此建模。
type VideoMeta struct {
	Width, Height int
	Duration      int // 秒
}

// AudioMeta 重发音频/语音所需的元数据；语音与音乐靠 Kind 区分。
type AudioMeta struct {
	Title     string
	Performer string
	Duration  int // 秒
}

// Media 描述媒体下载与重发所需的全部信息；不含网络状态，可安全缓存于内存队列。
type Media struct {
	Kind     ItemKind
	Location tg.InputFileLocationClass // 已构造好的下载位置
	DCID     int                       // 源媒体所在 Telegram 数据中心；0 表示未知
	FileName string                    // DocumentAttributeFilename / photo 等缺省名
	Size     int64                     // 字节，用于发送上限预检查与上传通道路由
	Video    *VideoMeta                // Kind==Video 时有效
	Audio    *AudioMeta                // Kind==Audio/Voice 时有效
}

// Item 内部标准化的消息条目（单条或相册成员）。
// Telegram 中文本正文与媒体 caption 是同一个字段（tg.Message.Message），故只存一份。
type Item struct {
	ID        int
	GroupedID int64 // 0 表示非相册
	Text      string
	Entities  []tg.MessageEntityClass
	Media     *Media
}

// IsAlbumMember 是否属于相册组。
func (it Item) IsAlbumMember() bool {
	return it.GroupedID != 0 && it.Media != nil
}

// RenderHTML 渲染消息正文或 caption 为 Bot API HTML 文本。
// 文本上限 4096 unit、caption 上限 1024 unit，超限截断并附注说明。
func (it Item) RenderHTML() string {
	if it.Media == nil {
		return ToHTMLLimited(it.Text, it.Entities, maxMessageUnits)
	}
	return ToHTMLLimited(it.Text, it.Entities, maxCaptionUnits)
}

// RenderHTMLWithSource 在正文顶部加入原消息链接，末尾附上频道脚注
// （links 非空时），并按消息类型保留平台长度预算。
func (it Item) RenderHTMLWithSource(sourceURL string, links []ChannelLink) string {
	caption := Caption{Text: it.Text, Entities: it.Entities}.
		WithQuotedBody().
		WithSourceLink(sourceURL).
		WithChannels(links)
	if it.Media == nil {
		return caption.renderHTML(maxMessageUnits)
	}
	return caption.renderHTML(maxCaptionUnits)
}

// MediaCaption 返回媒体 caption 的结构化描述（文本 + 原始 MTProto 实体）。
// Bot API 路径经 Caption.RenderHTML 渲染；MTProto 大文件直传路径经 Caption.Limited
// 截断后直接透传实体，避免"实体→HTML→实体"二次转换失真。
func (it Item) MediaCaption() Caption {
	return Caption{Text: it.Text, Entities: it.Entities}
}
