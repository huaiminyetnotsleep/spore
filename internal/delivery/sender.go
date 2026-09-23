package delivery

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// ErrAlbumNotSupported 表示整组发送不可行（混入不支持类型或超限图片），调用方应降级逐条发。
var ErrAlbumNotSupported = errors.New("album contains unsupported media type")

// ErrPoolUnavailable 表示多机器人池当前没有任何可用通道（全部 bot 未就绪），
// 调用方按"Bot 未就绪"既有语义处理（事件补发/通知失败不留痕）。
var ErrPoolUnavailable = errors.New("bot pool unavailable")

// Config 是 Sender 实现的运行参数。
type Config struct {
	// PhotoLimit 图片经 sendPhoto 发送的大小上限（官方服务器 10MB；
	// 本地 Bot API 服务器可放宽到 MAX_FILE_SIZE）。超限图片降级为 document。
	PhotoLimit int64
}

// Sender 抽象消息投递；业务层不直接触碰 Bot API / MTProto 客户端，
// 未来扩展"发送到频道/群/多用户"时只需换实现。
type Sender interface {
	// SendMessage 以 HTML parse_mode 发送文本消息，返回新消息 ID
	//（botapi 需要用它关联"正在获取消息..."占位提示）。
	SendMessage(ctx context.Context, chatID int64, html string) (int, error)

	// EditMessageText 以 HTML parse_mode 编辑既有文本消息（worker 把
	// "正在获取消息..."占位提示定期编辑为实时进度）。相同内容的编辑
	// 返回的 "message is not modified" 由实现识别为成功 no-op。
	EditMessageText(ctx context.Context, chatID int64, messageID int, html string) error

	// SendMedia 按媒体类型分发 photo/video/document/audio/voice 发送，
	// 返回新消息 ID（worker 把刚发出的消息经 Bot API copyMessages 复制到
	// 用户绑定的频道）；reader 为单次消费的媒体数据源（流或文件句柄），
	// 必须非 nil；路由层按媒体大小选择 Bot API 上传或 Bot 号 MTProto
	// 直传（见 router.go）。
	SendMedia(ctx context.Context, chatID int64, m message.Media, caption message.Caption, reader io.Reader) (int, error)

	// SendAlbum 整组发送相册，返回逐成员的新消息 ID（按发送顺序，供频道
	// 副本整组复制）；每个成员的 caption 绑定在 AlbumEntry 上
	//（Bot API 与 MTProto 路径同语义：逐成员携带，保持源相册中文字与
	// 媒体的对应关系）。成员全部带 Reader（上传组）；不可整组发送时返回
	// ErrAlbumNotSupported，由调用方降级为逐条发送。全员在 Bot API 上限内
	// 走 sendMediaGroup；含超限成员（video 且 ≤2000MB）时分流到 Bot 号
	// MTProto 整组直传（路由层内部决策，本契约对调用方透明）。
	SendAlbum(ctx context.Context, chatID int64, entries []AlbumEntry) ([]int, error)

	// AlbumGroupable 判断媒体能否进入整组发送（与 SendAlbum 内部判定同源），
	// 供 worker 在打开下载句柄前做元数据预检。
	AlbumGroupable(m message.Media) bool

	// CopyMessages 从 fromChatID 整组复制 bot 已发送的消息到 chatID（服务端
	// 复制，媒体与 caption 原样保留、无转发头，相册保持整组，不受上传上限
	// 约束）。返回新消息 ID（按发送顺序）。重复链接复用与频道副本共用语义；
	// 源消息已删除等失败由调用方决定回落策略。
	CopyMessages(ctx context.Context, fromChatID, chatID int64, messageIDs []int) ([]int, error)

	// CopyMessage 单条复制并覆盖 caption（干净副本构造用：投递消息的 caption
	// 织有该用户绑定频道的脚注，复制到缓存频道时换成无脚注版本）。返回新
	// 消息 ID；仅适用于单条投递（相册走 CopyMessages + EditMessageCaption）。
	CopyMessage(ctx context.Context, fromChatID, chatID int64, messageID int, captionHTML string) (int, error)

	// EditMessageCaption 以 HTML parse_mode 编辑既有媒体消息的 caption
	// （缓存频道批量复制后的逐条清洗，与复用时为绑频道用户补脚注）。
	// 相同内容的编辑返回的 "message is not modified" 由实现识别为成功 no-op
	// （语义同 EditMessageText）。
	EditMessageCaption(ctx context.Context, chatID int64, messageID int, captionHTML string) error

	// DeleteMessage 删除一条 Bot 自己发出的消息；错误已按错误码分类，
	// "哪些删除失败可忽略"（如消息已被用户手动删除）由调用方决定。
	DeleteMessage(ctx context.Context, chatID int64, messageID int) error
}

// MarkupSender 是支持 Telegram ReplyMarkup 的可选扩展接口。普通 Sender
// 不需要实现它，调用方应在类型断言失败时降级为纯文本发送。
type MarkupSender interface {
	SendMessageWithMarkup(ctx context.Context, chatID int64, html string, markup models.ReplyMarkup) (int, error)
	EditMessageTextWithMarkup(ctx context.Context, chatID int64, messageID int, html string, markup models.ReplyMarkup) error
}

// AlbumEntry 相册单成员：媒体描述、数据源与该成员自己的语义 caption。
// caption 逐成员构造（worker 从 Item.MediaCaption() 填充，拆分成员携带
// 自己的正文与切段说明）；SendAlbum 发送前统一归一化为"恰好组首一条"
// （全部成员正文合并进组首，见 routerSender.SendAlbum）——客户端对相册
// 的首渲染在多个成员携带 caption 时抑制组级展示位（相册下方空白，真机
// 2026-09-20），两条整组通道从首次请求起就只消费归一化后的条目。
type AlbumEntry struct {
	Media   message.Media
	Reader  io.Reader
	Caption message.Caption
}

// TryDeleteStatus 尽力删除状态提示消息：ID 为 0 跳过，失败仅记 debug 日志。
// botapi（入队失败清理）与 worker（任务收尾清理）共用同一语义。
func TryDeleteStatus(ctx context.Context, snd Sender, log *slog.Logger, chatID int64, msgID int) {
	if msgID == 0 {
		return
	}
	if err := snd.DeleteMessage(ctx, chatID, msgID); err != nil {
		log.Debug("状态消息清理失败", "chat_id", chatID, "msg_id", msgID, "error", err.Error())
	}
}
