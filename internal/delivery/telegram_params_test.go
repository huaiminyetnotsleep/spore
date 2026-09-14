package delivery

// Bot API 传输层参数测试：用假 HTTP 服务器捕获真实请求体，断言
// caption 位置（show_caption_above_media）等参数按媒体类型正确发送。
// caption 统一置于媒体下方（原消息卡片、正文、脚注随 caption 在下），
// photo/video 均不得携带置顶参数；document/音频类本就不支持置顶。

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbot "github.com/go-telegram/bot"

	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// capturedRequest 记录一次 Bot API 调用的路径与请求体。
type capturedRequest struct {
	Path string
	Body string
}

// newCapturingSender 构造指向本地假服务器的 telegramSender。
// 假服务器记录请求并返回合法的 Telegram 响应（单消息返回对象，整组返回数组）。
func newCapturingSender(t *testing.T) (*telegramSender, *[]capturedRequest) {
	t.Helper()
	captured := &[]capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*captured = append(*captured, capturedRequest{Path: r.URL.Path, Body: string(body)})
		if strings.HasSuffix(r.URL.Path, "sendMediaGroup") {
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(server.Close)

	b, err := tgbot.New("123:test", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("构造测试 Bot 失败: %v", err)
	}
	return &telegramSender{b: b, photoLimit: 10 << 20}, captured
}

func TestSendMediaPhotoCaptionBelow(t *testing.T) {
	s, captured := newCapturingSender(t)
	m := message.Media{Kind: message.KindPhoto, FileName: "p.jpg", Size: 100}
	if _, err := s.SendMedia(context.Background(), 7, m, message.Caption{Text: "<b>cap</b>"}, strings.NewReader("data")); err != nil {
		t.Fatalf("photo 发送应成功: %v", err)
	}
	reqs := *captured
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/sendPhoto") {
		t.Fatalf("应恰好一次 sendPhoto: %+v", reqs)
	}
	if strings.Contains(reqs[0].Body, "show_caption_above_media") {
		t.Error("photo 请求不应携带 caption 置顶参数（caption 在媒体下方）")
	}
}

func TestSendMediaVideoCaptionBelow(t *testing.T) {
	s, captured := newCapturingSender(t)
	m := message.Media{Kind: message.KindVideo, FileName: "v.mp4", Size: 100,
		Video: &message.VideoMeta{Width: 640, Height: 480, Duration: 10}}
	if _, err := s.SendMedia(context.Background(), 7, m, message.Caption{Text: "cap"}, strings.NewReader("data")); err != nil {
		t.Fatalf("video 发送应成功: %v", err)
	}
	reqs := *captured
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/sendVideo") {
		t.Fatalf("应恰好一次 sendVideo: %+v", reqs)
	}
	if strings.Contains(reqs[0].Body, "show_caption_above_media") {
		t.Error("video 请求不应携带 caption 置顶参数（caption 在媒体下方）")
	}
}

func TestSendMediaDocumentNoCaptionAbove(t *testing.T) {
	s, captured := newCapturingSender(t)
	m := message.Media{Kind: message.KindDocument, FileName: "d.bin", Size: 100}
	if _, err := s.SendMedia(context.Background(), 7, m, message.Caption{Text: "cap"}, strings.NewReader("data")); err != nil {
		t.Fatalf("document 发送应成功: %v", err)
	}
	reqs := *captured
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/sendDocument") {
		t.Fatalf("应恰好一次 sendDocument: %+v", reqs)
	}
	if strings.Contains(reqs[0].Body, "show_caption_above_media") {
		t.Error("document 不支持 caption 置顶，不应携带该参数")
	}
}

func TestSendAlbumAllMembersCaptionBelow(t *testing.T) {
	s, captured := newCapturingSender(t)
	entries := []AlbumEntry{
		{Media: message.Media{Kind: message.KindPhoto, FileName: "a.jpg", Size: 10},
			Reader: strings.NewReader("a"), Caption: message.Caption{Text: "图一"}},
		{Media: message.Media{Kind: message.KindVideo, FileName: "b.mp4", Size: 10,
			Video: &message.VideoMeta{Width: 640, Height: 480, Duration: 5}},
			Reader: strings.NewReader("b"), Caption: message.Caption{Text: "图二"}},
	}
	if _, err := s.SendAlbum(context.Background(), 7, entries); err != nil {
		t.Fatalf("整组发送应成功: %v", err)
	}
	reqs := *captured
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/sendMediaGroup") {
		t.Fatalf("应恰好一次 sendMediaGroup: %+v", reqs)
	}
	if n := strings.Count(reqs[0].Body, "show_caption_above_media"); n != 0 {
		t.Errorf("相册成员均不应携带 caption 置顶字段（caption 在媒体下方），得到 %d 处: %s", n, reqs[0].Body)
	}
}

// EditMessageText 参数与特殊错误语义：HTML parse_mode 正确携带；
// "message is not modified"（相同内容编辑）按成功 no-op 处理。
func TestEditMessageTextParams(t *testing.T) {
	s, captured := newCapturingSender(t)
	if err := s.EditMessageText(context.Background(), 7, 42, "<b>进度</b>"); err != nil {
		t.Fatalf("编辑应成功: %v", err)
	}
	reqs := *captured
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/editMessageText") {
		t.Fatalf("应恰好一次 editMessageText: %+v", reqs)
	}
	if !strings.Contains(reqs[0].Body, "edit_message_text") &&
		!strings.Contains(reqs[0].Body, "%E8%BF%9B%E5%BA%A6") && !strings.Contains(reqs[0].Body, "进度") {
		t.Fatalf("请求体应携带编辑文本: %s", reqs[0].Body)
	}
	if !strings.Contains(reqs[0].Body, "HTML") {
		t.Fatalf("请求体应携带 parse_mode=HTML: %s", reqs[0].Body)
	}
}

func TestEditMessageTextNotModifiedIsNoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`))
	}))
	t.Cleanup(server.Close)
	b, err := tgbot.New("123:test", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("构造测试 Bot 失败: %v", err)
	}
	s := &telegramSender{b: b, photoLimit: 10 << 20}
	if err := s.EditMessageText(context.Background(), 7, 42, "same"); err != nil {
		t.Fatalf("相同内容编辑应视为成功 no-op，得到 %v", err)
	}
}

// TestCopyMessagesParams 断言 copyMessages 请求参数（源聊天、目标聊天、
// 消息 ID 列表）与新消息 ID 提取。
func TestCopyMessagesParams(t *testing.T) {
	var reqs []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		reqs = append(reqs, capturedRequest{Path: r.URL.Path, Body: string(body)})
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"message_id":51},{"message_id":52}]}`))
	}))
	t.Cleanup(server.Close)
	b, err := tgbot.New("123:test", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("构造测试 Bot 失败: %v", err)
	}
	s := &telegramSender{b: b, photoLimit: 10 << 20}

	ids, err := s.CopyMessages(context.Background(), 111, 222, []int{11, 12})
	if err != nil {
		t.Fatalf("复制应成功: %v", err)
	}
	if len(ids) != 2 || ids[0] != 51 || ids[1] != 52 {
		t.Fatalf("应按序返回新消息 ID [51 52]，得到 %v", ids)
	}
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/copyMessages") {
		t.Fatalf("应恰好一次 copyMessages: %+v", reqs)
	}
	// 请求体是 multipart 表单：逐字段断言字段名与值
	for _, want := range []string{`name="from_chat_id"`, `name="chat_id"`,
		`name="message_ids"`, "111", "222", "[11,12]"} {
		if !strings.Contains(reqs[0].Body, want) {
			t.Errorf("请求体应包含 %s，得到 %s", want, reqs[0].Body)
		}
	}
}

// TestCopyMessagesEmptyIDs 空列表属调用方违约，按内部防御错误处理。
func TestCopyMessagesEmptyIDs(t *testing.T) {
	s := &telegramSender{}
	if _, err := s.CopyMessages(context.Background(), 1, 2, nil); err == nil {
		t.Fatal("空 messageIDs 应返回防御错误")
	}
}

// TestCopyMessageCaptionOverride 断言 copyMessage 请求的 caption 覆盖参数
// 与 parse_mode。
func TestCopyMessageCaptionOverride(t *testing.T) {
	var reqs []capturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		reqs = append(reqs, capturedRequest{Path: r.URL.Path, Body: string(body)})
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":88}}`))
	}))
	t.Cleanup(server.Close)
	b, err := tgbot.New("123:test", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("构造测试 Bot 失败: %v", err)
	}
	s := &telegramSender{b: b, photoLimit: 10 << 20}

	id, err := s.CopyMessage(context.Background(), 111, 222, 33, "<b>干净</b>")
	if err != nil {
		t.Fatalf("复制应成功: %v", err)
	}
	if id != 88 {
		t.Fatalf("应返回新消息 ID 88，得到 %d", id)
	}
	if len(reqs) != 1 || !strings.HasSuffix(reqs[0].Path, "/copyMessage") {
		t.Fatalf("应恰好一次 copyMessage: %+v", reqs)
	}
	for _, want := range []string{`name="from_chat_id"`, `name="chat_id"`,
		`name="caption"`, "干净", "HTML"} {
		if !strings.Contains(reqs[0].Body, want) {
			t.Errorf("请求体应包含 %s，得到 %s", want, reqs[0].Body)
		}
	}
}

// TestEditMessageCaptionNotModifiedIsNoop 相同 caption 编辑按成功 no-op 处理。
func TestEditMessageCaptionNotModifiedIsNoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: message is not modified"}`))
	}))
	t.Cleanup(server.Close)
	b, err := tgbot.New("123:test", tgbot.WithServerURL(server.URL), tgbot.WithSkipGetMe())
	if err != nil {
		t.Fatalf("构造测试 Bot 失败: %v", err)
	}
	s := &telegramSender{b: b, photoLimit: 10 << 20}
	if err := s.EditMessageCaption(context.Background(), 7, 42, "same"); err != nil {
		t.Fatalf("相同 caption 编辑应视为成功 no-op，得到 %v", err)
	}
}
