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
// 整组发送成功后做 caption 修复（repairAlbumCaptions，参照缓存频道副本
// WriteClean 已真机验证的 Bot API 编辑链路）：把各成员非空 caption 经
// editMessageCaption 重写一遍。两条分支都需要——MTProto 路径上
// sendMultiMedia 逐成员携带的 caption 中图片成员的 caption 在客户端不展示
// （相册聊天界面只渲染首条成员 caption，首条为图片时相册下方无任何文字；
// 真机 2026-09-20）；Bot API 路径（本地 Bot API 服务器模式下的拆分相册：
// 1800MB 分段 ≤ uploadCap=MaxFileSize，全员落 Bot API 承载）同理重写兜底。
// 普通相册（无 Split 标记且全员 Bot API 承载）caption 展示正常，不重写。
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

// repairAlbumCaptions 整组发送成功后，把各成员非空 caption 经 Bot API
// editMessageCaption 重写一遍（与缓存频道副本 WriteClean 同款编辑链路，
// 展示已真机验证）。尽力而为：整组媒体此刻已送达，caption 修复失败只记
// 日志、不改变发送结果——把已完成的投递标记为失败只会诱导用户重发，重复
// 2GB 级上传。ID 数与成员数不符（发送器契约违约，SendAlbum 已防御）时无法
// 按位定位成员，整体跳过。成功重写记一条 Info（真机排查相册 caption 展示
// 问题的观测点）。
func (s *routerSender) repairAlbumCaptions(ctx context.Context, chatID int64, entries []AlbumEntry, ids []int) {
	if len(ids) != len(entries) {
		return
	}
	edited := 0
	for i, e := range entries {
		captionHTML := e.Caption.RenderHTML()
		if captionHTML == "" {
			continue
		}
		if err := s.api.EditMessageCaption(ctx, chatID, ids[i], captionHTML); err != nil {
			s.log.Warn("整组 caption 重写失败（相册下方文字可能缺失）",
				"chat_id", chatID, "message_id", ids[i], "error", err.Error())
		} else {
			edited++
		}
	}
	if edited > 0 {
		s.log.Info("整组 caption 已重写（保证相册下方文字展示）",
			"chat_id", chatID, "messages", len(ids), "edited", edited)
	}
}
