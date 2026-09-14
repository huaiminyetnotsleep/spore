package mtproto

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

func testBotLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// ---- uploadedMediaOf / mimeOf（表驱动） ----

func TestUploadedMediaOf(t *testing.T) {
	file := &tg.InputFile{ID: 11, Parts: 1, Name: "video.mp4"}

	t.Run("video 属性与 MIME", func(t *testing.T) {
		media := uploadedMediaOf(file, message.Media{
			Kind:     message.KindVideo,
			FileName: "video.mp4",
			Video:    &message.VideoMeta{Width: 640, Height: 480, Duration: 30},
		})
		doc, ok := media.(*tg.InputMediaUploadedDocument)
		if !ok {
			t.Fatalf("形态应为 InputMediaUploadedDocument: %T", media)
		}
		if doc.File != file || doc.MimeType != "video/mp4" {
			t.Errorf("file/MIME 不符: %+v %q", doc.File, doc.MimeType)
		}
		var name string
		var video *tg.DocumentAttributeVideo
		for _, a := range doc.Attributes {
			switch attr := a.(type) {
			case *tg.DocumentAttributeFilename:
				name = attr.FileName
			case *tg.DocumentAttributeVideo:
				video = attr
			}
		}
		if name != "video.mp4" {
			t.Errorf("文件名属性不符: %q", name)
		}
		if video == nil || video.W != 640 || video.H != 480 || video.Duration != 30 || !video.SupportsStreaming {
			t.Errorf("video 属性不符: %+v", video)
		}
	})

	t.Run("voice 属性", func(t *testing.T) {
		media := uploadedMediaOf(file, message.Media{
			Kind:     message.KindVoice,
			FileName: "voice.ogg",
			Audio:    &message.AudioMeta{Duration: 12},
		})
		doc := media.(*tg.InputMediaUploadedDocument)
		var voice *tg.DocumentAttributeAudio
		for _, a := range doc.Attributes {
			if attr, ok := a.(*tg.DocumentAttributeAudio); ok {
				voice = attr
			}
		}
		if voice == nil || !voice.Voice || voice.Duration != 12 {
			t.Errorf("voice 属性不符: %+v", voice)
		}
		if doc.MimeType != "audio/ogg" {
			t.Errorf("voice MIME 不符: %q", doc.MimeType)
		}
	})

	t.Run("audio 元数据与 document 兜底", func(t *testing.T) {
		audio := uploadedMediaOf(file, message.Media{
			Kind:     message.KindAudio,
			FileName: "a.mp3",
			Audio:    &message.AudioMeta{Title: "T", Performer: "P", Duration: 60},
		}).(*tg.InputMediaUploadedDocument)
		var attr *tg.DocumentAttributeAudio
		for _, a := range audio.Attributes {
			if v, ok := a.(*tg.DocumentAttributeAudio); ok {
				attr = v
			}
		}
		if attr == nil || attr.Title != "T" || attr.Performer != "P" || attr.Voice {
			t.Errorf("audio 属性不符: %+v", attr)
		}

		plain := uploadedMediaOf(file, message.Media{
			Kind:     message.KindDocument,
			FileName: "f.bin",
		}).(*tg.InputMediaUploadedDocument)
		if plain.MimeType != "application/octet-stream" || len(plain.Attributes) != 1 {
			t.Errorf("普通文档应只有文件名属性: %+v", plain)
		}
	})
}

// ---- classifySendError（表驱动） ----

func TestClassifySendError(t *testing.T) {
	flood := &tgerr.Error{Code: 420, Type: "FLOOD_WAIT", Message: "FLOOD_WAIT_X", Argument: 5}
	restricted := &tgerr.Error{Code: 400, Type: "CHAT_FORWARDS_RESTRICTED", Message: "CHAT_FORWARDS_RESTRICTED"}

	if err := classifySendError(nil); err != nil {
		t.Fatalf("nil 应返回 nil: %v", err)
	}
	var ae *apperr.AppError
	if err := classifySendError(flood); !errors.As(err, &ae) || ae.Code != apperr.CodeRateLimited {
		t.Fatalf("FLOOD_WAIT 应归 TELEGRAM_RATE_LIMIT: %v", err)
	}
	if err := classifySendError(restricted); !errors.As(err, &ae) || ae.Code != apperr.CodeSendFailed {
		t.Fatalf("其余应归 BOT_SEND_FAILED: %v", err)
	}
	// cause 保留原始 tgerr：tgerr 判定可穿透 AppError 链
	if !tgerr.Is(classifySendError(restricted), "CHAT_FORWARDS_RESTRICTED") {
		t.Fatal("tgerr 应经 AppError.Unwrap 链可达")
	}
}

// ---- 会话状态与本地防御（不触网） ----

func newTestBotClient(t *testing.T) *BotClient {
	t.Helper()
	return NewBotClient(config.Config{DataDir: t.TempDir()}, testBotLogger())
}

func TestBotClientNotReadyRejects(t *testing.T) {
	c := newTestBotClient(t) // 未 setReady
	if c.Available() {
		t.Fatal("初始状态应不可用")
	}
	_, err := c.SendMedia(context.Background(), 7,
		message.Media{Kind: message.KindVideo, FileName: "v.mp4", Size: 1},
		message.Caption{}, strings.NewReader("x"))
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != apperr.CodeLargeChannelUnavailable {
		t.Fatalf("未就绪应确定性拒绝: %v", err)
	}
}

func TestBotClientNilReaderRejected(t *testing.T) {
	c := newTestBotClient(t)
	c.setReady(&tg.Client{})
	_, err := c.SendMedia(context.Background(), 7,
		message.Media{Kind: message.KindVideo, FileName: "v.mp4", Size: 1},
		message.Caption{}, nil)
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != apperr.CodeInternal {
		t.Fatalf("nil reader 应本地拒绝: %v", err)
	}
}

func TestBotClientAvailableLifecycle(t *testing.T) {
	c := newTestBotClient(t)
	c.setReady(&tg.Client{})
	if !c.Available() {
		t.Fatal("setReady 后应可用")
	}
	c.setOffline()
	if c.Available() {
		t.Fatal("setOffline 后应不可用")
	}
}

func TestBotClientPeerCache(t *testing.T) {
	c := newTestBotClient(t)
	c.mu.Lock()
	c.peers[7] = 12345
	c.mu.Unlock()

	peer, err := c.resolveUserPeer(context.Background(), nil, 7)
	if err != nil {
		t.Fatalf("缓存命中应直接返回（不出网）: %v", err)
	}
	p, ok := peer.(*tg.InputPeerUser)
	if !ok || p.UserID != 7 || p.AccessHash != 12345 {
		t.Errorf("缓存 peer 不符: %+v", p)
	}

	// 负数 chatID（群/频道）防御拒绝：bot 只处理私聊用户
	if _, err := c.resolveUserPeer(context.Background(), nil, -100123); err == nil {
		t.Fatal("负数 chatID 应防御报错")
	}
}

// ---- 大文件直传全路径（假 invoker 驱动，精确断言请求构造） ----

// largeSendInvoker 按请求类型分发预设响应：users.getUsers 返回目标用户
// （bot 特权反查），upload.saveFilePart 返回 true，messages.sendMedia 记录
// 请求并返回空 Updates。
type largeSendInvoker struct {
	mu         sync.Mutex
	users      tg.UserClassVector // UsersGetUsers 的响应
	sendErr    error              // sendMedia 阶段注入的错误（nil = 成功）
	usersCalls int
	sendCalls  int
	lastSend   *tg.MessagesSendMediaRequest
}

func (f *largeSendInvoker) Invoke(_ context.Context, req bin.Encoder, d bin.Decoder) error {
	var payload bin.Encoder
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r := req.(type) {
	case *tg.UsersGetUsersRequest:
		f.usersCalls++
		payload = &f.users
	case *tg.UploadSaveFilePartRequest, *tg.UploadSaveBigFilePartRequest:
		payload = &tg.BoolTrue{}
	case *tg.MessagesSendMediaRequest:
		f.sendCalls++
		f.lastSend = r
		if f.sendErr != nil {
			return f.sendErr
		}
		payload = &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 4242, RandomID: 1},
		}}
	default:
		return fmt.Errorf("测试 invoker：未预期的请求 %T", req)
	}
	// 响应对象自带 Decode：把预设 payload 编码进临时 buffer 后交给它解码，
	// 与 gotd 引擎"buffer → 响应对象"的解包方式等价。
	buf := &bin.Buffer{}
	if err := payload.Encode(buf); err != nil {
		return fmt.Errorf("测试 invoker：编码响应失败: %w", err)
	}
	return d.Decode(buf)
}

// readyBotClient 构造已就绪且走假 invoker 的 BotClient。
func readyBotClient(t *testing.T, inv *largeSendInvoker) *BotClient {
	t.Helper()
	c := newTestBotClient(t)
	c.setReady(tg.NewClient(inv))
	return c
}

func largeVideo() message.Media {
	return message.Media{
		Kind:     message.KindVideo,
		FileName: "big.mp4",
		Size:     8, // 测试载荷很小：走 saveFilePart 小文件分片
		Video:    &message.VideoMeta{Width: 1920, Height: 1080, Duration: 90},
	}
}

func TestBotClientSendLargeMediaBuildsRequest(t *testing.T) {
	inv := &largeSendInvoker{users: tg.UserClassVector{Elems: []tg.UserClass{
		&tg.User{ID: 7, AccessHash: 777},
	}}}
	c := readyBotClient(t, inv)
	cap := message.Caption{Text: "cap", Entities: []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 0, Length: 3},
	}}.WithSourceLink("https://t.me/example/7")

	if _, err := c.SendMedia(context.Background(), 7, largeVideo(), cap, strings.NewReader("payload")); err != nil {
		t.Fatalf("大文件直传应成功: %v", err)
	}

	// bot 特权反查：InputUser{UserID, AccessHash:0}
	if inv.usersCalls != 1 {
		t.Fatalf("应恰好一次 UsersGetUsers 反查: %d", inv.usersCalls)
	}

	req := inv.lastSend
	if req == nil {
		t.Fatal("应发出 messages.sendMedia 请求")
	}
	// 目标 peer 用反查所得 access hash
	if p, ok := req.Peer.(*tg.InputPeerUser); !ok || p.UserID != 7 || p.AccessHash != 777 {
		t.Errorf("目标 peer 不符: %+v", req.Peer)
	}
	// 媒体为"已上传文档"形态（大文件通道不发 photo 坐标引用）
	doc, ok := req.Media.(*tg.InputMediaUploadedDocument)
	if !ok {
		t.Fatalf("媒体应为 InputMediaUploadedDocument: %T", req.Media)
	}
	if f, ok := doc.File.(*tg.InputFile); !ok || f.Name != "big.mp4" || f.Parts < 1 {
		t.Errorf("上传文件句柄不符: %+v", doc.File)
	}
	if req.Message != "🔗 原消息\nhttps://t.me/example/7\n\ncap" || len(req.Entities) != 4 {
		t.Errorf("caption 文本与实体应透传: %q %d", req.Message, len(req.Entities))
	}
	if _, ok := req.Entities[0].(*tg.MessageEntityBlockquote); !ok {
		t.Fatalf("来源卡片实体应透传: %#v", req.Entities[0])
	}
	link, ok := req.Entities[2].(*tg.MessageEntityTextURL)
	if !ok || link.URL != "https://t.me/example/7" || link.Offset <= 0 {
		t.Fatalf("来源链接实体应透传: %#v", req.Entities[2])
	}
	bold, ok := req.Entities[3].(*tg.MessageEntityBold)
	if !ok || bold.Offset <= 0 || bold.Length != 3 {
		t.Fatalf("原 caption 实体应平移后透传: %#v", req.Entities[1])
	}
	if req.RandomID == 0 {
		t.Error("RandomID 应非零（防重放）")
	}
	if req.InvertMedia {
		t.Error("视频 caption 应在媒体下方，不应设置 invert_media")
	}

	// document 类同样不应设置 invert_media
	inv2 := &largeSendInvoker{users: tg.UserClassVector{Elems: []tg.UserClass{
		&tg.User{ID: 7, AccessHash: 777},
	}}}
	c2 := readyBotClient(t, inv2)
	docMedia := message.Media{Kind: message.KindDocument, FileName: "big.bin", Size: 8}
	if _, err := c2.SendMedia(context.Background(), 7, docMedia, message.Caption{}, strings.NewReader("payload")); err != nil {
		t.Fatalf("document 直传应成功: %v", err)
	}
	if inv2.lastSend == nil || inv2.lastSend.InvertMedia {
		t.Errorf("document 不应设置 invert_media: %+v", inv2.lastSend)
	}

	// 二次发送：peer 已缓存，不再反查
	if _, err := c.SendMedia(context.Background(), 7, largeVideo(), message.Caption{}, strings.NewReader("payload")); err != nil {
		t.Fatalf("二次发送应成功: %v", err)
	}
	if inv.usersCalls != 1 {
		t.Fatalf("peer 缓存命中不应再次反查: %d", inv.usersCalls)
	}
}

func TestBotClientSendLargeMediaErrors(t *testing.T) {
	t.Run("sendMedia tgerr 错误归类", func(t *testing.T) {
		inv := &largeSendInvoker{
			users:   tg.UserClassVector{Elems: []tg.UserClass{&tg.User{ID: 7, AccessHash: 1}}},
			sendErr: &tgerr.Error{Code: 420, Type: "FLOOD_WAIT", Message: "FLOOD_WAIT"},
		}
		c := readyBotClient(t, inv)
		_, err := c.SendMedia(context.Background(), 7, largeVideo(), message.Caption{}, strings.NewReader("p"))
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeRateLimited {
			t.Fatalf("FLOOD_WAIT 应归 TELEGRAM_RATE_LIMIT: %v", err)
		}
	})

	t.Run("反查未返回目标用户 → 发送失败", func(t *testing.T) {
		inv := &largeSendInvoker{users: tg.UserClassVector{Elems: []tg.UserClass{
			&tg.User{ID: 999, AccessHash: 1}, // 非 7
		}}}
		c := readyBotClient(t, inv)
		_, err := c.SendMedia(context.Background(), 7, largeVideo(), message.Caption{}, strings.NewReader("p"))
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeSendFailed {
			t.Fatalf("反查未命中应报发送失败: %v", err)
		}
		if inv.lastSend != nil {
			t.Error("反查失败不应发出 sendMedia")
		}
	})
}

// ---- 相册整组直传（两阶段 uploadMedia → sendMultiMedia） ----

// albumSendInvoker 按请求类型分发预设响应：users.getUsers 反查目标用户、
// upload.save*Part 恒成功、messages.uploadMedia 按入参媒体类型返回注册坐标
// （ID/AccessHash/FileReference 按调用次序生成，供引用断言）、
// messages.sendMultiMedia 记录请求。callLog 记录 RPC 顺序，供
// "先注册后整组"断言；failUploadOn（1 起，0 = 不注入）在对应次数的
// uploadMedia 上返回 uploadErr。
type albumSendInvoker struct {
	mu           sync.Mutex
	users        tg.UserClassVector
	failUploadOn int
	uploadErr    error

	nextID       int64
	usersCalls   int
	uploadCalls  int
	sendCalls    int
	callLog      []string
	uploadedKind []string
	lastSend     *tg.MessagesSendMultiMediaRequest
}

func (f *albumSendInvoker) Invoke(_ context.Context, req bin.Encoder, d bin.Decoder) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	var payload bin.Encoder
	switch r := req.(type) {
	case *tg.UsersGetUsersRequest:
		f.usersCalls++
		f.callLog = append(f.callLog, "users")
		payload = &f.users
	case *tg.UploadSaveFilePartRequest, *tg.UploadSaveBigFilePartRequest:
		f.callLog = append(f.callLog, "part")
		payload = &tg.BoolTrue{}
	case *tg.MessagesUploadMediaRequest:
		f.uploadCalls++
		f.callLog = append(f.callLog, "uploadMedia")
		if f.failUploadOn > 0 && f.uploadCalls >= f.failUploadOn {
			return f.uploadErr
		}
		f.nextID++
		switch media := r.Media.(type) {
		case *tg.InputMediaUploadedPhoto:
			f.uploadedKind = append(f.uploadedKind, "photo")
			payload = &tg.MessageMediaPhoto{Photo: &tg.Photo{
				ID: f.nextID, AccessHash: f.nextID * 10, FileReference: []byte("fresh-fr"),
			}}
		case *tg.InputMediaUploadedDocument:
			f.uploadedKind = append(f.uploadedKind, "document")
			payload = &tg.MessageMediaDocument{Document: &tg.Document{
				ID: f.nextID, AccessHash: f.nextID * 10, FileReference: []byte("fresh-fr"),
			}}
		default:
			return fmt.Errorf("测试 invoker：未预期的 uploadMedia 媒体 %T", media)
		}
	case *tg.MessagesSendMultiMediaRequest:
		f.sendCalls++
		f.callLog = append(f.callLog, "sendMultiMedia")
		f.lastSend = r
		payload = &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 4242, RandomID: 1},
			&tg.UpdateMessageID{ID: 4243, RandomID: 2},
		}}
	default:
		return fmt.Errorf("测试 invoker：未预期的请求 %T", req)
	}
	buf := &bin.Buffer{}
	if err := payload.Encode(buf); err != nil {
		return fmt.Errorf("测试 invoker：编码响应失败: %w", err)
	}
	return d.Decode(buf)
}

func albumMembers() ([]message.Media, []io.Reader, []message.Caption) {
	return []message.Media{
			{Kind: message.KindPhoto, FileName: "photo.jpg", Size: 5},
			{Kind: message.KindVideo, FileName: "big.mp4", Size: 8,
				Video: &message.VideoMeta{Width: 640, Height: 480, Duration: 30}},
		},
		[]io.Reader{strings.NewReader("photo-data"), strings.NewReader("video-data")},
		[]message.Caption{
			{Text: "图说明", Entities: []tg.MessageEntityClass{
				&tg.MessageEntityBold{Offset: 0, Length: 3},
			}},
			{Text: "视频说明"},
		}
}

func TestBotClientSendAlbumTwoPhase(t *testing.T) {
	inv := &albumSendInvoker{users: tg.UserClassVector{Elems: []tg.UserClass{
		&tg.User{ID: 7, AccessHash: 777},
	}}}
	c := newTestBotClient(t)
	c.setReady(tg.NewClient(inv))
	medias, readers, captions := albumMembers()
	captions[0] = captions[0].WithSourceLink("https://t.me/example/7")

	if _, err := c.SendAlbum(context.Background(), 7, medias, readers, captions); err != nil {
		t.Fatalf("两阶段整组发送应成功: %v", err)
	}

	if inv.sendCalls != 1 {
		t.Fatalf("应恰好一次 sendMultiMedia: %d", inv.sendCalls)
	}
	// 顺序约束：所有 uploadMedia 都发生在 sendMultiMedia 之前
	lastUpload, sendAt := -1, -1
	for i, name := range inv.callLog {
		switch name {
		case "uploadMedia":
			lastUpload = i
		case "sendMultiMedia":
			sendAt = i
		}
	}
	if lastUpload == -1 || sendAt == -1 || lastUpload > sendAt {
		t.Fatalf("uploadMedia 应先于 sendMultiMedia: log=%v", inv.callLog)
	}
	if inv.uploadCalls != 2 || len(inv.uploadedKind) != 2 ||
		inv.uploadedKind[0] != "photo" || inv.uploadedKind[1] != "document" {
		t.Fatalf("每成员应各注册一次: calls=%d kinds=%v", inv.uploadCalls, inv.uploadedKind)
	}

	req := inv.lastSend
	if len(req.MultiMedia) != 2 {
		t.Fatalf("整组应含两个成员: %d", len(req.MultiMedia))
	}
	// 最终媒体只含引用形态，坐标来自 uploadMedia 返回值
	photoRef, ok := req.MultiMedia[0].Media.(*tg.InputMediaPhoto)
	if !ok {
		t.Fatalf("photo 成员应为 InputMediaPhoto 引用: %T", req.MultiMedia[0].Media)
	}
	if id, ok := photoRef.ID.(*tg.InputPhoto); !ok ||
		id.ID != 1 || id.AccessHash != 10 || string(id.FileReference) != "fresh-fr" {
		t.Errorf("photo 引用坐标应来自 uploadMedia: %+v", photoRef.ID)
	}
	docRef, ok := req.MultiMedia[1].Media.(*tg.InputMediaDocument)
	if !ok {
		t.Fatalf("video 成员应为 InputMediaDocument 引用: %T", req.MultiMedia[1].Media)
	}
	if id, ok := docRef.ID.(*tg.InputDocument); !ok || id.ID != 2 || id.AccessHash != 20 {
		t.Errorf("document 引用坐标应来自 uploadMedia: %+v", docRef.ID)
	}
	// caption 逐成员绑定（含来源链接与原实体透传）
	if req.MultiMedia[0].Message != "🔗 原消息\nhttps://t.me/example/7\n\n图说明" || len(req.MultiMedia[0].Entities) != 4 {
		t.Errorf("成员 0 caption 不符: %q entities=%d",
			req.MultiMedia[0].Message, len(req.MultiMedia[0].Entities))
	}
	if _, ok := req.MultiMedia[0].Entities[0].(*tg.MessageEntityBlockquote); !ok {
		t.Fatalf("成员 0 来源卡片实体不符: %#v", req.MultiMedia[0].Entities[0])
	}
	link, ok := req.MultiMedia[0].Entities[2].(*tg.MessageEntityTextURL)
	if !ok || link.URL != "https://t.me/example/7" || link.Offset <= 0 {
		t.Fatalf("成员 0 来源链接实体不符: %#v", req.MultiMedia[0].Entities[2])
	}
	if req.MultiMedia[1].Message != "视频说明" || len(req.MultiMedia[1].Entities) != 0 {
		t.Errorf("成员 1 caption 不符: %q entities=%d",
			req.MultiMedia[1].Message, len(req.MultiMedia[1].Entities))
	}
	for _, m := range req.MultiMedia {
		if m.RandomID == 0 {
			t.Error("RandomID 应非零（防重放）")
		}
	}
	if p, ok := req.Peer.(*tg.InputPeerUser); !ok || p.UserID != 7 || p.AccessHash != 777 {
		t.Errorf("目标 peer 不符: %+v", req.Peer)
	}
	if req.InvertMedia {
		t.Error("相册整组 caption 应在媒体下方，不应设置 invert_media")
	}
}

func TestBotClientSendAlbumUploadMediaFailure(t *testing.T) {
	inv := &albumSendInvoker{
		users:        tg.UserClassVector{Elems: []tg.UserClass{&tg.User{ID: 7, AccessHash: 777}}},
		failUploadOn: 2, // 第 2 个成员注册失败
		uploadErr:    &tgerr.Error{Code: 400, Type: "MEDIA_INVALID", Message: "MEDIA_INVALID"},
	}
	c := newTestBotClient(t)
	c.setReady(tg.NewClient(inv))
	medias, readers, captions := albumMembers()

	_, err := c.SendAlbum(context.Background(), 7, medias, readers, captions)
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != apperr.CodeSendFailed {
		t.Fatalf("uploadMedia 失败应归 BOT_SEND_FAILED: %v", err)
	}
	if inv.sendCalls != 0 {
		t.Fatalf("注册失败不应发出 sendMultiMedia: %d", inv.sendCalls)
	}
}

func TestBotClientSendAlbumLocalDefenses(t *testing.T) {
	inv := &albumSendInvoker{users: tg.UserClassVector{Elems: []tg.UserClass{
		&tg.User{ID: 7, AccessHash: 777},
	}}}
	c := newTestBotClient(t)
	c.setReady(tg.NewClient(inv))
	medias, readers, captions := albumMembers()

	t.Run("成员数不足", func(t *testing.T) {
		_, err := c.SendAlbum(context.Background(), 7, medias[:1], readers[:1], captions[:1])
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeInternal {
			t.Fatalf("单成员应防御报错: %v", err)
		}
	})

	t.Run("数量不一致", func(t *testing.T) {
		_, err := c.SendAlbum(context.Background(), 7, medias, readers[:1], captions)
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeInternal {
			t.Fatalf("readers 缺位应防御报错: %v", err)
		}
	})

	t.Run("非 photo/video 成员", func(t *testing.T) {
		invalid := append([]message.Media(nil), medias...)
		invalid[1] = message.Media{Kind: message.KindDocument, FileName: "f.bin", Size: 3}
		_, err := c.SendAlbum(context.Background(), 7, invalid, readers, captions)
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeInternal {
			t.Fatalf("document 成员应防御报错: %v", err)
		}
		if inv.sendCalls != 0 {
			t.Errorf("不应发出 sendMultiMedia: %d", inv.sendCalls)
		}
	})

	t.Run("成员缺 reader", func(t *testing.T) {
		noReader := append([]io.Reader(nil), readers...)
		noReader[0] = nil
		_, err := c.SendAlbum(context.Background(), 7, medias, noReader, captions)
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeInternal {
			t.Fatalf("nil reader 应防御报错: %v", err)
		}
	})
}

func TestMediaReference(t *testing.T) {
	t.Run("photo 坐标", func(t *testing.T) {
		ref, err := mediaReference(&tg.MessageMediaPhoto{Photo: &tg.Photo{
			ID: 5, AccessHash: 50, FileReference: []byte("fr"),
		}})
		if err != nil {
			t.Fatalf("photo 坐标应转换成功: %v", err)
		}
		p, ok := ref.(*tg.InputMediaPhoto)
		if !ok {
			t.Fatalf("应为 InputMediaPhoto: %T", ref)
		}
		if id, ok := p.ID.(*tg.InputPhoto); !ok || id.ID != 5 || id.AccessHash != 50 {
			t.Errorf("引用坐标不符: %+v", p.ID)
		}
	})

	t.Run("document 坐标", func(t *testing.T) {
		ref, err := mediaReference(&tg.MessageMediaDocument{Document: &tg.Document{
			ID: 6, AccessHash: 60, FileReference: []byte("fr"),
		}})
		if err != nil {
			t.Fatalf("document 坐标应转换成功: %v", err)
		}
		d, ok := ref.(*tg.InputMediaDocument)
		if !ok {
			t.Fatalf("应为 InputMediaDocument: %T", ref)
		}
		if id, ok := d.ID.(*tg.InputDocument); !ok || id.ID != 6 || id.AccessHash != 60 {
			t.Errorf("引用坐标不符: %+v", d.ID)
		}
	})

	t.Run("PhotoEmpty → 发送失败", func(t *testing.T) {
		_, err := mediaReference(&tg.MessageMediaPhoto{Photo: &tg.PhotoEmpty{}})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeSendFailed {
			t.Fatalf("空坐标应按发送失败处理: %v", err)
		}
	})

	t.Run("DocumentEmpty → 发送失败", func(t *testing.T) {
		_, err := mediaReference(&tg.MessageMediaDocument{Document: &tg.DocumentEmpty{}})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeSendFailed {
			t.Fatalf("空坐标应按发送失败处理: %v", err)
		}
	})

	t.Run("nil → 内部错误", func(t *testing.T) {
		_, err := mediaReference(nil)
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeInternal {
			t.Fatalf("nil 应按内部错误处理: %v", err)
		}
	})

	t.Run("未知类型 → 发送失败", func(t *testing.T) {
		_, err := mediaReference(&tg.MessageMediaContact{})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeSendFailed {
			t.Fatalf("未知类型应按发送失败处理: %v", err)
		}
	})
}

func TestUploadedInputMedia(t *testing.T) {
	file := &tg.InputFile{ID: 11, Parts: 1, Name: "x"}
	if _, ok := uploadedInputMedia(file, message.Media{
		Kind: message.KindPhoto, FileName: "photo.jpg",
	}).(*tg.InputMediaUploadedPhoto); !ok {
		t.Fatal("photo 应保持 InputMediaUploadedPhoto 形态")
	}
	if _, ok := uploadedInputMedia(file, message.Media{
		Kind: message.KindVideo, FileName: "v.mp4",
	}).(*tg.InputMediaUploadedDocument); !ok {
		t.Fatal("video 应走 InputMediaUploadedDocument 形态")
	}
}
