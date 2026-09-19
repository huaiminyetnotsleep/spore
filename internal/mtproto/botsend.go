// Bot 号会话的大文件直传发送：把下载好的媒体字节经 gotd uploader 上传为
// Bot 自己的新文件（messages.sendMedia + InputMediaUploadedDocument），绕过
// 官方 Bot API 服务器的 50MB 上传硬上限（MTProto 通道上限 2000MB）；
// 相册整组形态见 SendAlbum（messages.sendMultiMedia）。
// 本文件的方法集合即 delivery.LargeFileSender 接口（接口定义在 delivery，
// 本包不 import delivery；装配在 cmd/bot/main.go）。

package mtproto

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// ErrBotSessionNotReady 本地哨兵：Bot 会话未就绪，大文件直传不可用。
// 包装为 CodeLargeChannelUnavailable（确定性失败，调用方不重试）。
var ErrBotSessionNotReady = errors.New("bot mtproto session not ready")

// SendMedia 上传并发送单个大文件媒体（实现 delivery.LargeFileSender）。
// r 为单次消费的媒体数据源（内存重排序缓冲或临时文件句柄，顺序读取即可）；
// 上传按 UPLOAD_THREADS 并发发送分片（>10MB 走 saveBigFilePart，读仍单流顺序，
// 不要求 r 可重放）；caption 经 Limited 截断后透传原始实体，避免
// "实体→HTML→实体"二次转换失真。成功时返回新消息 ID（worker 据此把
// 刚发出的消息复制到用户绑定的频道）。
func (c *BotClient) SendMedia(ctx context.Context, chatID int64, m message.Media, caption message.Caption, r io.Reader) (int, error) {
	api, ok := c.current()
	if !ok {
		return 0, apperr.Wrap(apperr.CodeLargeChannelUnavailable, ErrBotSessionNotReady)
	}
	if r == nil {
		return 0, apperr.New(apperr.CodeInternal, "大文件直传要求提供媒体数据源 reader")
	}
	peer, err := c.resolvePeer(ctx, api, chatID)
	if err != nil {
		return 0, err
	}

	// 并发分片上传：bigLoop 单读多发，读端背压由数据源（缓冲/文件）承担
	up := uploader.NewUploader(api).WithThreads(c.uploadThreads())
	// 缩略图先传（≤200KB 级，失败早暴露）：MTProto 服务器不为上传的
	// document 自动生成缩略图，不带 Thumb 的视频在客户端无封面预览
	thumb, err := c.uploadThumb(ctx, up, m)
	if err != nil {
		return 0, err
	}
	file, err := up.Upload(ctx, uploader.NewUpload(m.FileName, r, m.Size))
	if err != nil {
		return 0, classifySendError(err)
	}

	limited := caption.Limited()
	req := &tg.MessagesSendMediaRequest{
		Peer:     peer,
		Media:    uploadedMediaOf(file, m, thumb),
		Message:  limited.Text,
		RandomID: rand.Int64(),
	}
	if len(limited.Entities) > 0 {
		req.SetEntities(limited.Entities)
	}
	upd, err := api.MessagesSendMedia(ctx, req)
	if err != nil {
		return 0, classifySendError(err)
	}
	ids := sentMessageIDs(upd)
	if len(ids) != 1 {
		return 0, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("MessagesSendMedia 响应应含 1 条消息，得到 %d 条", len(ids)))
	}
	return ids[0], nil
}

// SendAlbum 上传并发送整组相册（实现 delivery.LargeFileSender 的整组形态）：
// 每成员 uploader.Upload → messages.uploadMedia 注册到目标 peer → 汇总为
// InputMediaPhoto/InputMediaDocument 引用 → messages.sendMultiMedia 一次整组
// 发送（单成员上限 2000MB，承载含超过 Bot API 上限成员的相册与分卷拆分段）。
// 两阶段是协议硬约束：sendMultiMedia 只接受已注册的媒体引用，直接携带
// inputMediaUploaded* 会被 400 MEDIA_INVALID 拒绝（真机结论 2026-09-03）。
// 成员类型支持 photo/video/document（document 即分卷拆分段——Telegram 不允许
// document 与 photo/video 混组，全 document 组合法，混组由调用方保证不出现）。
// medias/readers/captions 按位对应、单次消费；成员串行处理：uploader 内部
// 已按 UPLOAD_THREADS 并发分片，再按成员并发放大会显著提高 FLOOD_WAIT
// 概率（与 worker 相册打开并发上限=2 同理）。caption 逐成员绑定（Limited
// 截断、实体透传），保持源相册中文字与媒体的对应关系。
func (c *BotClient) SendAlbum(ctx context.Context, chatID int64, medias []message.Media, readers []io.Reader, captions []message.Caption) ([]int, error) {
	api, ok := c.current()
	if !ok {
		return nil, apperr.Wrap(apperr.CodeLargeChannelUnavailable, ErrBotSessionNotReady)
	}
	if len(medias) < 2 || len(medias) > message.AlbumMaxItems {
		return nil, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("相册成员数不合法：%d", len(medias)))
	}
	if len(readers) != len(medias) || len(captions) != len(medias) {
		return nil, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("相册成员与数据源/caption 数量不一致：%d vs %d/%d",
				len(medias), len(readers), len(captions)))
	}
	peer, err := c.resolvePeer(ctx, api, chatID)
	if err != nil {
		return nil, err
	}

	threads := c.uploadThreads()
	up := uploader.NewUploader(api).WithThreads(threads)

	multi := make([]tg.InputSingleMedia, 0, len(medias))
	for i, m := range medias {
		if m.Kind != message.KindPhoto && m.Kind != message.KindVideo && m.Kind != message.KindDocument {
			return nil, apperr.New(apperr.CodeInternal,
				fmt.Sprintf("整组直传仅支持 photo/video/document，第 %d 项为 %v", i, m.Kind))
		}
		if readers[i] == nil {
			return nil, apperr.New(apperr.CodeInternal,
				fmt.Sprintf("相册第 %d 项缺少上传数据源（reader 为空）", i))
		}
		thumb, err := c.uploadThumb(ctx, up, m)
		if err != nil {
			return nil, err
		}
		file, err := up.Upload(ctx, uploader.NewUpload(m.FileName, readers[i], m.Size))
		if err != nil {
			return nil, classifySendError(err)
		}
		// 注册到目标会话，换取 sendMultiMedia 可用的媒体引用
		//（含新鲜 file_reference 的 Photo/Document 坐标）
		registered, err := api.MessagesUploadMedia(ctx, &tg.MessagesUploadMediaRequest{
			Peer:  peer,
			Media: uploadedInputMedia(file, m, thumb),
		})
		if err != nil {
			return nil, classifySendError(err)
		}
		ref, err := mediaReference(registered)
		if err != nil {
			return nil, err
		}
		single := tg.InputSingleMedia{Media: ref, RandomID: rand.Int64()}
		if limited := captions[i].Limited(); limited.Text != "" {
			single.Message = limited.Text
			if len(limited.Entities) > 0 {
				single.SetEntities(limited.Entities)
			}
		}
		multi = append(multi, single)
	}

	sendReq := &tg.MessagesSendMultiMediaRequest{
		Peer:       peer,
		MultiMedia: multi,
	}
	upd, err := api.MessagesSendMultiMedia(ctx, sendReq)
	if err != nil {
		return nil, classifySendError(err)
	}
	ids := sentMessageIDs(upd)
	if len(ids) != len(medias) {
		return nil, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("MessagesSendMultiMedia 响应应含 %d 条消息，得到 %d 条", len(medias), len(ids)))
	}
	return ids, nil
}

func (c *BotClient) uploadThreads() int {
	threads := c.cfg.UploadThreads
	if c.transfer != nil {
		threads = c.transfer.Snapshot().UploadThreads
	}
	if threads < 1 {
		return 1
	}
	return threads
}

// sentMessageIDs 从 messages.sendMedia / sendMultiMedia 的 Updates 响应中
// 按发送顺序提取新消息 ID（Bot API 消息 ID 与 MTProto 消息 ID 同一编号空间，
// worker 用它们把刚发出的消息经 Bot API copyMessages 复制到绑定的频道）。
// 优先取携带完整消息体的 UpdateNewMessage/UpdateNewChannelMessage；缺失时
// 回落到仅含编号映射的 UpdateMessageID。非预期形态返回空切片，由调用方按
// 内部错误处理。
func sentMessageIDs(upd tg.UpdatesClass) []int {
	var (
		ids     []int
		idsOnly []int
	)
	addMessage := func(m tg.MessageClass) {
		if msg, ok := m.(*tg.Message); ok && msg.ID != 0 {
			ids = append(ids, msg.ID)
		}
	}
	switch v := upd.(type) {
	case *tg.Updates:
		for _, u := range v.Updates {
			switch u := u.(type) {
			case *tg.UpdateNewMessage:
				addMessage(u.Message)
			case *tg.UpdateNewChannelMessage:
				addMessage(u.Message)
			case *tg.UpdateMessageID:
				if u.ID != 0 {
					idsOnly = append(idsOnly, u.ID)
				}
			}
		}
	case *tg.UpdateShortSentMessage:
		if v.ID != 0 {
			ids = append(ids, v.ID)
		}
	}
	if len(ids) > 0 {
		return ids
	}
	return idsOnly
}

// uploadedInputMedia 构造 messages.uploadMedia 的入参媒体：photo 保持
// photo 形态（document 与 photo 混组会被拒，进入本路径的图片必经
// photoLimit 预检），video 复用单发大文件的 document+video 属性构造。
func uploadedInputMedia(file tg.InputFileClass, m message.Media, thumb tg.InputFileClass) tg.InputMediaClass {
	if m.Kind == message.KindPhoto {
		return &tg.InputMediaUploadedPhoto{File: file}
	}
	return uploadedMediaOf(file, m, thumb)
}

// mediaReference 从 messages.uploadMedia 的返回中提取 sendMultiMedia 可用的
// 媒体引用：MessageMediaPhoto → InputMediaPhoto、MessageMediaDocument →
// InputMediaDocument（经 AsInput 携带 ID/AccessHash/FileReference）。
// 空坐标（PhotoEmpty/DocumentEmpty）与非预期类型按发送失败/内部错误处理，
// 不向上传播 panic。
func mediaReference(mm tg.MessageMediaClass) (tg.InputMediaClass, error) {
	switch m := mm.(type) {
	case *tg.MessageMediaPhoto:
		p, ok := m.Photo.(*tg.Photo)
		if !ok || p == nil {
			return nil, apperr.New(apperr.CodeSendFailed,
				"uploadMedia 返回的 photo 坐标为空（PhotoEmpty）")
		}
		return &tg.InputMediaPhoto{ID: p.AsInput()}, nil
	case *tg.MessageMediaDocument:
		d, ok := m.Document.(*tg.Document)
		if !ok || d == nil {
			return nil, apperr.New(apperr.CodeSendFailed,
				"uploadMedia 返回的 document 坐标为空（DocumentEmpty）")
		}
		return &tg.InputMediaDocument{ID: d.AsInput()}, nil
	case nil:
		return nil, apperr.New(apperr.CodeInternal, "uploadMedia 未返回媒体对象（nil）")
	default:
		return nil, apperr.New(apperr.CodeSendFailed,
			fmt.Sprintf("uploadMedia 返回非预期媒体类型：%T", mm))
	}
}

// uploadThumb 上传文档缩略图（worker 解析好的 JPEG 字节，≤200KB 级）。
// 尽力而为：失败只记日志并降级为无缩略图——缩略图是外观增强，不值得让
// 可能已完成大半的大文件上传前功尽弃；仅 ctx 已取消时向外传播错误
// （主文件上传必然同样失败，早失败省去无谓等待）。
func (c *BotClient) uploadThumb(ctx context.Context, up *uploader.Uploader, m message.Media) (tg.InputFileClass, error) {
	if len(m.ThumbJPEG) == 0 {
		return nil, nil
	}
	f, err := up.Upload(ctx, uploader.NewUpload(message.ThumbFileName,
		bytes.NewReader(m.ThumbJPEG), int64(len(m.ThumbJPEG))))
	if err == nil {
		return f, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	c.log.Warn("缩略图上传失败，本次发送不带封面",
		"file", m.FileName, "error", err.Error())
	return nil, nil
}

// uploadedMediaOf 把上传所得 InputFile 构造为发送用的输入媒体：
// 大文件通道一律按 document 发送（photo 本就不会超过 Bot API 上限，
// 超限的"图片"实为超大文档），按 Kind 补齐 video/audio 属性与 MIME；
// thumb 非 nil 时挂为文档封面。
func uploadedMediaOf(file tg.InputFileClass, m message.Media, thumb tg.InputFileClass) tg.InputMediaClass {
	doc := &tg.InputMediaUploadedDocument{
		File:     file,
		MimeType: mimeOf(m.Kind),
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: m.FileName},
		},
	}
	if thumb != nil {
		doc.SetThumb(thumb)
	}
	switch m.Kind {
	case message.KindVideo:
		attr := &tg.DocumentAttributeVideo{SupportsStreaming: true}
		if m.Video != nil {
			attr.W = m.Video.Width
			attr.H = m.Video.Height
			attr.Duration = float64(m.Video.Duration)
		}
		doc.Attributes = append(doc.Attributes, attr)
	case message.KindAudio:
		attr := &tg.DocumentAttributeAudio{}
		if m.Audio != nil {
			attr.Title = m.Audio.Title
			attr.Performer = m.Audio.Performer
			attr.Duration = m.Audio.Duration
		}
		doc.Attributes = append(doc.Attributes, attr)
	case message.KindVoice:
		attr := &tg.DocumentAttributeAudio{Voice: true}
		if m.Audio != nil {
			attr.Duration = m.Audio.Duration
		}
		doc.Attributes = append(doc.Attributes, attr)
	}
	return doc
}

// mimeOf 按 Kind 映射上传 MIME 类型（Telegram 只作展示与客户端预处理提示）。
func mimeOf(k message.ItemKind) string {
	switch k {
	case message.KindPhoto:
		return "image/jpeg"
	case message.KindVideo:
		return "video/mp4"
	case message.KindVoice:
		return "audio/ogg"
	case message.KindAudio:
		return "audio/mpeg"
	default:
		return "application/octet-stream"
	}
}

// resolvePeer 解析目标聊天为 InputPeer：chatID > 0 为私聊用户，< 0 为
// 频道/超级群组（缓存频道直传与副本存在性校验的目标）。
func (c *BotClient) resolvePeer(ctx context.Context, api *tg.Client, chatID int64) (tg.InputPeerClass, error) {
	if chatID > 0 {
		return c.resolveUserPeer(ctx, api, chatID)
	}
	return c.resolveChannelPeer(ctx, api, chatID)
}

// resolveUserPeer 解析目标用户为 InputPeerUser。Bot 持有特权：
// UsersGetUsers 接受 access_hash=0 的 InputUser 反查目标（WTelegramBot 同款
// 做法）；结果缓存于内存，bot↔user 的 access hash 长期稳定、跨重连保留。
// chatID 为 Bot API 的 chat id，私聊场景即正数 userID（bot 只处理私聊入站）。
func (c *BotClient) resolveUserPeer(ctx context.Context, api *tg.Client, chatID int64) (tg.InputPeerClass, error) {
	if chatID <= 0 {
		return nil, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("大文件直传仅支持私聊用户目标，得到 chat_id=%d", chatID))
	}
	if h, ok := c.cachedPeer(chatID); ok {
		return &tg.InputPeerUser{UserID: chatID, AccessHash: h}, nil
	}
	users, err := api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: chatID}})
	if err != nil {
		return nil, classifySendError(err)
	}
	for _, uc := range users {
		if u, ok := uc.(*tg.User); ok && u.ID == chatID {
			c.mu.Lock()
			c.peers[chatID] = u.AccessHash
			c.mu.Unlock()
			return &tg.InputPeerUser{UserID: chatID, AccessHash: u.AccessHash}, nil
		}
	}
	return nil, apperr.New(apperr.CodeSendFailed,
		fmt.Sprintf("UsersGetUsers 未返回目标用户 %d", chatID))
}

// resolveChannelPeer 解析目标频道为 InputPeerChannel。Bot 为该频道管理员
// （缓存频道场景：Bot API copyMessage 投递副本的前提）；access_hash 经
// ChannelsGetChannels 特权反查（channelAccessHash），与用户 peer 同缓存
// （bot↔频道 access hash 长期稳定、跨重连保留）。缓存频道 ID 是 Bot API
// 的 -100 前缀数字 ID，还原为原生 channel ID 后查询。
func (c *BotClient) resolveChannelPeer(ctx context.Context, api *tg.Client, chatID int64) (tg.InputPeerClass, error) {
	channelID, ok := nativeChannelID(chatID)
	if !ok {
		return nil, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("大文件直传不支持的目标 chat_id=%d（仅支持用户私聊与 -100 前缀频道）", chatID))
	}
	accessHash, err := c.channelAccessHash(ctx, api, chatID, channelID)
	if err != nil {
		return nil, err
	}
	return &tg.InputPeerChannel{ChannelID: channelID, AccessHash: accessHash}, nil
}

// nativeChannelID 把 Bot API 的 -100 前缀频道 ID 还原为 Telegram 原生
// channel ID；非 -100 前缀的负数 ID（普通群组等）不支持，返回 false。
func nativeChannelID(chatID int64) (int64, bool) {
	s := strconv.FormatInt(chatID, 10)
	if !strings.HasPrefix(s, "-100") {
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(s, "-100"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// channelAccessHash 返回频道的 access_hash：优先取缓存（peers 表，key 为
// Bot API 的 -100 形式 chatID），未缓存时经 ChannelsGetChannels 特权反查
// 并写缓存。
func (c *BotClient) channelAccessHash(ctx context.Context, api *tg.Client, chatID, channelID int64) (int64, error) {
	if h, ok := c.cachedPeer(chatID); ok {
		return h, nil
	}
	chats, err := api.ChannelsGetChannels(ctx, []tg.InputChannelClass{
		&tg.InputChannel{ChannelID: channelID},
	})
	if err != nil {
		return 0, classifySendError(err)
	}
	for _, ch := range chats.GetChats() {
		if channel, ok := ch.(*tg.Channel); ok && channel.ID == channelID {
			c.mu.Lock()
			c.peers[chatID] = channel.AccessHash
			c.mu.Unlock()
			return channel.AccessHash, nil
		}
	}
	return 0, apperr.New(apperr.CodeSendFailed,
		fmt.Sprintf("ChannelsGetChannels 未返回目标频道 %d", chatID))
}

func (c *BotClient) cachedPeer(userID int64) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	h, ok := c.peers[userID]
	return h, ok
}

// classifySendError 把 MTProto 上传/发送错误包装为 AppError：限流 →
// TELEGRAM_RATE_LIMIT，服务端 5xx → TELEGRAM_SERVER_ERROR，网络传输故障 →
// NETWORK_ERROR，其余 → BOT_SEND_FAILED 兜底。cause 保留原始 tgerr，
// 调用方可经错误链（AppError.Unwrap）做 tgerr 结构化判定。
func classifySendError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := tgerr.AsFloodWait(err); ok {
		return apperr.Wrap(apperr.CodeRateLimited, err)
	}
	if tgerr.IsCode(err, 500) {
		return apperr.Wrap(apperr.CodeTelegramServer, err)
	}
	if apperr.IsTransportFailure(err) {
		return apperr.Wrap(apperr.CodeNetworkError, err)
	}
	return apperr.Wrap(apperr.CodeSendFailed, err)
}
