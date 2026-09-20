package delivery

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// LargeFileSender 由 Bot 身份 MTProto 客户端实现（*mtproto.BotClient），
// 负责超过 Bot API 上传上限的大文件直传（上传媒体字节，上限 2000MB），
// 含单文件与相册整组两种形态。接口定义在 delivery——发送词汇归 delivery
// 所有，mtproto 不反向依赖本包；装配在 cmd/bot/main.go。
type LargeFileSender interface {
	// Available 报告大文件通道当前是否可用（Bot 会话就绪）。
	// 不可用时路由按确定性失败处理，任务以明确错误码结束。
	Available() bool
	// SendMedia 上传并发送单个大文件媒体，返回新消息 ID
	//（worker 据此把刚发出的消息复制到用户绑定的频道）。
	SendMedia(ctx context.Context, chatID int64, m message.Media, caption message.Caption, reader io.Reader) (int, error)
	// SendAlbum 上传并发送整组相册（messages.sendMultiMedia，承载超过
	// Bot API 上限的成员），返回逐成员消息 ID（按发送顺序）。medias/
	// readers/captions 按位对应（路由层从 AlbumEntry 拆出，签名不引入
	// delivery 类型以维持依赖方向）；caption 逐成员绑定，与 Bot API
	// 路径同语义。
	SendAlbum(ctx context.Context, chatID int64, medias []message.Media, readers []io.Reader, captions []message.Caption) ([]int, error)
}

// routerSender 按"媒体大小"把发送分派到 Bot API（上传/文本/相册/删除）或
// Bot 身份 MTProto（大文件直传，单文件与相册整组）；对 worker 完全透明，
// Sender 契约不变。
type routerSender struct {
	api       Sender          // Bot API 实现（telegramSender）
	large     LargeFileSender // 大文件直传实现（mtproto.BotClient）
	uploadCap int64           // Bot API 上传路径的大小上限（官方服务器 50MB；本地服务器 = MaxFileSize）
	largeCap  int64           // MTProto 直传通道的大小上限（= MaxFileSize，2000MB 级）
	log       *slog.Logger    // 整组 caption 修复的失败日志（尽力而为，不向上传播）
}

// NewRouter 组装路由 Sender：Size 超过 uploadCap 的媒体走 MTProto 大文件
// 直传，其余（上传、文本、删除）委托 Bot API 实现；相册全员在上限内走
// Bot API sendMediaGroup，含超限成员时走 MTProto 整组直传（largeCap）。
// log 为 nil 时回退 slog.Default()。
func NewRouter(botAPI Sender, large LargeFileSender, uploadCap, largeCap int64, log *slog.Logger) Sender {
	if log == nil {
		log = slog.Default()
	}
	return &routerSender{api: botAPI, large: large, uploadCap: uploadCap, largeCap: largeCap, log: log}
}

func (s *routerSender) SendMessage(ctx context.Context, chatID int64, html string) (int, error) {
	return s.api.SendMessage(ctx, chatID, html)
}

// EditMessageText 编辑既有文本消息（占位提示的实时进度更新）：
// 占位消息只在 Bot API 侧，始终委托 Bot API 实现。
func (s *routerSender) EditMessageText(ctx context.Context, chatID int64, messageID int, html string) error {
	return s.api.EditMessageText(ctx, chatID, messageID, html)
}

func (s *routerSender) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	return s.api.DeleteMessage(ctx, chatID, messageID)
}

// CopyMessages 始终委托 Bot API 实现：服务端复制不传输媒体字节，没有
// "超过 Bot API 上限走大文件直传"的路由语义（2GB 级媒体与 10KB 图片同路径）。
func (s *routerSender) CopyMessages(ctx context.Context, fromChatID, chatID int64, messageIDs []int) ([]int, error) {
	return s.api.CopyMessages(ctx, fromChatID, chatID, messageIDs)
}

// CopyMessage 单条复制带 caption 覆盖（缓存频道干净副本构造），同
// CopyMessages 始终走 Bot API。
func (s *routerSender) CopyMessage(ctx context.Context, fromChatID, chatID int64, messageID int, captionHTML string) (int, error) {
	return s.api.CopyMessage(ctx, fromChatID, chatID, messageID, captionHTML)
}

// EditMessageCaption 编辑既有媒体消息的 caption，始终委托 Bot API 实现。
func (s *routerSender) EditMessageCaption(ctx context.Context, chatID int64, messageID int, captionHTML string) error {
	return s.api.EditMessageCaption(ctx, chatID, messageID, captionHTML)
}

// AlbumGroupable 判断媒体能否进入整组发送（与 SendAlbum 内部分流同源），
// 供 worker 在打开下载句柄前做元数据预检：
//   - 全员满足 Bot API 判定（photo/video，photo ≤ photoLimit）且 ≤ uploadCap
//     → Bot API sendMediaGroup 承载；
//   - video 超过 uploadCap 但 ≤ largeCap（2000MB）→ Bot 号 MTProto 整组
//     直传承载（引用直发移除后 sendMediaGroup 无法承载大成员，靠本通道
//     保住相册整组语义）；
//   - photo 超 photoLimit、document/audio/voice 及超 largeCap → 不可整组，
//     由调用方降级逐条发送（Telegram 不允许 document 与 photo/video 混组）。
func (s *routerSender) AlbumGroupable(m message.Media) bool {
	if m.Size <= s.uploadCap && s.api.AlbumGroupable(m) {
		return true
	}
	return m.Kind == message.KindVideo && m.Size <= s.largeCap
}

// SendMedia 路由单媒体发送：超过 Bot API 上限走大文件直传，其余委托 Bot API；
// reader 必须非 nil（契约防御，两条路径都消费媒体字节）。
// 成功时透传新消息 ID（频道副本复制用）。
func (s *routerSender) SendMedia(ctx context.Context, chatID int64, m message.Media, caption message.Caption, reader io.Reader) (int, error) {
	if reader == nil {
		return 0, apperr.New(apperr.CodeInternal, "媒体发送要求提供数据源 reader")
	}
	if m.Size <= s.uploadCap {
		return s.api.SendMedia(ctx, chatID, m, caption, reader)
	}
	if !s.large.Available() {
		// 本地确定性失败（零网络）：明确告知大文件通道不可用，
		// 小文件路径不受影响，不因 Bot 会话故障整体停摆
		return 0, apperr.New(apperr.CodeLargeChannelUnavailable,
			fmt.Sprintf("媒体超过 Bot API 上限（size=%d > cap=%d）且大文件直传通道未就绪", m.Size, s.uploadCap))
	}
	return s.large.SendMedia(ctx, chatID, m, caption, reader)
}

// SendAlbum 路由整组发送：全员经 Bot API 判定且在上限内 → Bot API
// sendMediaGroup；含超限成员（经 AlbumGroupable 预检必为 video 且 ≤ largeCap）
// → Bot 号 MTProto 整组直传，保住"图+大视频混合相册"的整组语义。
// document 成员同样走 MTProto 整组通道——只由分卷拆分路径产生（全 document
// 组，Telegram 允许；与 photo/video 混组会被服务器拒绝，调用方保证不出现），
// worker 相册预检（AlbumGroupable）不感知，普通相册的 document 成员仍逐条
// 降级，历史行为不变。大文件通道未就绪按确定性失败处理（与单媒体路径同
// 姿态）；混入双通道都承载不了的成员属调用方违约（worker 预检已排除），
// 按防御错误处理。
//
// 整组发送成功后执行"恰好组首一条 caption"不变量（repairAlbumCaptions，
// 参照缓存频道副本 WriteClean 的已验证机制——副本只保留每组首条 caption，
// 展示一直正常）：客户端对相册的首渲染在**多个成员携带 caption** 时抑制
// 组级展示位（相册下方空白；真机 2026-09-20 五组实验：恰好组首一条 → 正常
// 展示，2+ 条 → 抑制——包括把署名写到组末分段后缓存频道副本也失去文字的
// 反向验证）。发送路径已按不变量构造（拆分段仅组首携带 caption），此处兜底：
// 非组首成员带 caption 则清空，组首 caption 缺失则补写。MTProto 分支与
// Bot API 分支的拆分相册（Split 标记）执行；普通相册（全员 Bot API 承载且
// 无拆分）不执行，历史行为不变。
func (s *routerSender) SendAlbum(ctx context.Context, chatID int64, entries []AlbumEntry) ([]int, error) {
	allAPI := true
	splitAlbum := false
	for i, e := range entries {
		if e.Reader == nil {
			return nil, apperr.New(apperr.CodeInternal,
				fmt.Sprintf("相册第 %d 项缺少上传数据源（Reader 为空）", i))
		}
		if e.Split {
			splitAlbum = true
		}
		switch {
		case e.Media.Size <= s.uploadCap && s.api.AlbumGroupable(e.Media): // Bot API 承载
		case e.Media.Size <= s.largeCap &&
			(e.Media.Kind == message.KindVideo || e.Media.Kind == message.KindDocument): // MTProto 整组承载
			allAPI = false
		default:
			return nil, apperr.New(apperr.CodeInternal,
				fmt.Sprintf("相册第 %d 项不可整组（类型/大小超出双通道上限），应逐条发送", i))
		}
	}
	if allAPI {
		ids, err := s.api.SendAlbum(ctx, chatID, entries)
		if err != nil {
			return nil, err
		}
		if splitAlbum { // 拆分相册走 Bot API 分支（本地服务器模式）：同样重写 caption
			s.repairAlbumCaptions(ctx, chatID, entries, ids)
		}
		return ids, nil
	}
	if !s.large.Available() {
		return nil, apperr.New(apperr.CodeLargeChannelUnavailable,
			fmt.Sprintf("相册含超过 Bot API 上限的成员（cap=%d）且大文件直传通道未就绪", s.uploadCap))
	}
	medias := make([]message.Media, len(entries))
	readers := make([]io.Reader, len(entries))
	captions := make([]message.Caption, len(entries))
	for i, e := range entries {
		medias[i] = e.Media
		readers[i] = e.Reader
		captions[i] = e.Caption
	}
	ids, err := s.large.SendAlbum(ctx, chatID, medias, readers, captions)
	if err != nil {
		return nil, err
	}
	s.repairAlbumCaptions(ctx, chatID, entries, ids)
	return ids, nil
}

// repairAlbumCaptions 整组发送成功后，把组首成员的署名 caption 强制写入
// **组首与组末**两个成员（经 Bot API 编辑链路，参照缓存频道副本 WriteClean
// 的已验证机制）。
//
// 客户端对相册组级展示位（相册下方唯一的 caption 槽）的成员取舍规则不稳定，
// 真机实验矩阵（2026-09-20，[图片(署名), 段1(署名), 段2(空 caption)]）：
//   - 完整三成员 → 展示位空白（caption 数据在线上，点开单条可见）；
//   - 删除组首图片 → 展示位出现分段署名；
//   - 删除组末空 caption 分段 → 展示位出现文字与原链接。
//
// 删除任一成员（组级重渲染）都让展示位恢复，说明数据在、渲染规则不稳——
// 与"组末成员 caption 为空"强相关。两端写入使展示位无论按首/末/其他规则
// 解析都能渲染完整署名；绑定频道副本（裸 copyMessages）随源消息自然继承。
//
// 两步强制写入（占位符 → 目标）：两次都是相对当前状态的真修改，不依赖空
// caption 的序列化行为，也免疫"线上已等于目标内容"的 no-op 吸收。首末是
// 同一条消息（单成员防御）时只写一次。尽力而为：媒体此刻已送达，编辑失败
// 只记 Warn（带 Telegram 原始错误文本，真机观测点）、不改变发送结果；任一
// 目标成功即记 Info。
// repairAlbumCaptions 整组发送成功后执行"恰好组首一条 caption"不变量的
// 兜底（Bot API editMessageCaption，与缓存频道副本 WriteClean 同款编辑
// 链路）。客户端对相册的首渲染在多个成员携带 caption 时抑制组级展示位
// （真机 2026-09-20 五组实验矩阵，含把署名写到组末分段后缓存频道副本一并
// 失去文字的反向验证；恰好组首一条——如正常相册、单视频切段、修复前的
// 缓存频道副本——展示正常）。发送路径已按不变量构造，此处兜底：
//   - 非组首成员带 caption → 清空（多 caption 抑制展示；缓存频道副本经
//     copyMessages 忠实继承，清空同时修正下游副本）；
//   - 组首 caption 缺失（发送链路意外丢失）→ 补写（线上已正确时为
//     "message is not modified" no-op）。
//
// 尽力而为：媒体此刻已送达，编辑失败只记 Warn（带 Telegram 原始错误文本，
// 真机观测点）、不改变发送结果。
func (s *routerSender) repairAlbumCaptions(ctx context.Context, chatID int64, entries []AlbumEntry, ids []int) {
	if len(ids) != len(entries) || len(ids) == 0 {
		return
	}
	for i := 1; i < len(entries); i++ {
		if entries[i].Caption.RenderHTML() == "" {
			continue
		}
		if err := s.api.EditMessageCaption(ctx, chatID, ids[i], ""); err != nil {
			s.log.Warn("非组首 caption 清空失败（相册下方署名可能缺失）",
				"chat_id", chatID, "message_id", ids[i], "error", err.Error())
		} else {
			s.log.Info("非组首成员 caption 已清空（保证组级展示位渲染组首署名）",
				"chat_id", chatID, "message_id", ids[i])
		}
	}
	if captionHTML := entries[0].Caption.RenderHTML(); captionHTML != "" {
		if err := s.api.EditMessageCaption(ctx, chatID, ids[0], captionHTML); err != nil {
			s.log.Warn("组首 caption 补写失败（相册下方署名可能缺失）",
				"chat_id", chatID, "message_id", ids[0], "error", err.Error())
		}
	}
}
