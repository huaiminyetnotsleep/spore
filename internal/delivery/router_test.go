package delivery

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"
	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// testLogger 返回静默日志器：caption 修复路径的 Warn 不污染测试输出。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeLargeSender 记录大文件直传调用（单媒体与整组），可注入可用性与错误。
type fakeLargeSender struct {
	available bool
	mediaErr  error
	albumErr  error

	mediaCalls int
	lastMedia  message.Media
	lastCap    message.Caption
	lastReader io.Reader

	albumCalls   int
	lastMedias   []message.Media
	lastReaders  []io.Reader
	lastCaptions []message.Caption
}

func (f *fakeLargeSender) Available() bool { return f.available }

func (f *fakeLargeSender) SendMedia(_ context.Context, _ int64, m message.Media, caption message.Caption, r io.Reader) (int, error) {
	f.mediaCalls++
	f.lastMedia = m
	f.lastCap = caption
	f.lastReader = r
	if f.mediaErr != nil {
		return 0, f.mediaErr
	}
	return 1, nil
}

func (f *fakeLargeSender) SendAlbum(_ context.Context, _ int64, medias []message.Media, readers []io.Reader, captions []message.Caption) ([]int, error) {
	f.albumCalls++
	f.lastMedias = medias
	f.lastReaders = readers
	f.lastCaptions = captions
	if f.albumErr != nil {
		return nil, f.albumErr
	}
	ids := make([]int, 0, len(medias))
	for i := range medias {
		ids = append(ids, 10+i)
	}
	return ids, nil
}

// fakeAPISender 记录 Bot API 委托调用（上传/文本/删除/复制/整组判定）。
type fakeAPISender struct {
	mediaCalls   int
	albumCalls   int
	messageCalls int
	deleteCalls  int
	copyCalls    int
	editCalls    []string
	groupable    bool

	// lastEntries 记录最近一次 SendAlbum 收到的条目（归一化后的形态）。
	lastEntries []AlbumEntry

	// captionEdits 记录 EditMessageCaption 调用（缓存清洗/复用补脚注等）；
	// captionErr 非 nil 时该调用返回错误（仍记录）。
	captionEdits []captionEditCall
	captionErr   error
}

// captionEditCall 是一次 caption 编辑的实参快照。
type captionEditCall struct {
	chatID    int64
	messageID int
	caption   string
}

func (f *fakeAPISender) SendMessage(ctx context.Context, chatID int64, html string) (int, error) {
	return f.SendMessageWithMarkup(ctx, chatID, html, nil)
}

func (f *fakeAPISender) SendMessageWithMarkup(context.Context, int64, string, models.ReplyMarkup) (int, error) {
	f.messageCalls++
	return 1, nil
}

func (f *fakeAPISender) SendMedia(context.Context, int64, message.Media, message.Caption, io.Reader) (int, error) {
	f.mediaCalls++
	return 1, nil
}

func (f *fakeAPISender) SendAlbum(_ context.Context, _ int64, entries []AlbumEntry) ([]int, error) {
	f.albumCalls++
	f.lastEntries = entries
	ids := make([]int, len(entries))
	for i := range entries {
		ids[i] = i + 1
	}
	return ids, nil
}

func (f *fakeAPISender) AlbumGroupable(message.Media) bool { return f.groupable }
func (f *fakeAPISender) CopyMessage(_ context.Context, _, _ int64, messageID int, _ string) (int, error) {
	f.copyCalls++
	return messageID, nil
}

func (f *fakeAPISender) EditMessageCaption(_ context.Context, chatID int64, messageID int, caption string) error {
	f.captionEdits = append(f.captionEdits, captionEditCall{chatID: chatID, messageID: messageID, caption: caption})
	return f.captionErr
}

func (f *fakeAPISender) CopyMessages(_ context.Context, fromChatID, chatID int64, messageIDs []int) ([]int, error) {
	f.copyCalls++
	ids := make([]int, len(messageIDs))
	for i := range messageIDs {
		ids[i] = i + 1
	}
	_ = fromChatID
	_ = chatID
	return ids, nil
}

func (f *fakeAPISender) DeleteMessage(context.Context, int64, int) error {
	f.deleteCalls++
	return nil
}

func (f *fakeAPISender) EditMessageText(ctx context.Context, chatID int64, messageID int, html string) error {
	return f.EditMessageTextWithMarkup(ctx, chatID, messageID, html, nil)
}

func (f *fakeAPISender) EditMessageTextWithMarkup(_ context.Context, _ int64, _ int, html string, _ models.ReplyMarkup) error {
	f.editCalls = append(f.editCalls, html)
	return nil
}

// 编译期契约：fakeAPISender 满足 Sender（作为路由的 Bot API 委托方）。
var _ Sender = (*fakeAPISender)(nil)

const (
	testUploadCap = int64(100)
	testLargeCap  = int64(1000)
)

func TestRouterSendMediaDispatch(t *testing.T) {
	ctx := context.Background()
	cap := message.Caption{Text: "cap"}

	t.Run("超上限且通道可用 → MTProto 大文件直传", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		m := message.Media{Kind: message.KindVideo, FileName: "big.mp4", Size: testUploadCap + 1}
		if _, err := s.SendMedia(ctx, 7, m, cap, strings.NewReader("data")); err != nil {
			t.Fatalf("大文件直传应成功: %v", err)
		}
		if large.mediaCalls != 1 || api.mediaCalls != 0 {
			t.Fatalf("应只调大文件通道: large=%d api=%d", large.mediaCalls, api.mediaCalls)
		}
		if large.lastMedia.Size != m.Size || large.lastCap.Text != "cap" || large.lastReader == nil {
			t.Errorf("直传调用应透传媒体、caption 与 reader: %+v", large.lastMedia)
		}
	})

	t.Run("超上限且通道不可用 → 确定性失败（零网络）", func(t *testing.T) {
		large := &fakeLargeSender{available: false}
		api := &fakeAPISender{}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		m := message.Media{Kind: message.KindVideo, Size: testUploadCap + 1}
		var ae *apperr.AppError
		_, err := s.SendMedia(ctx, 7, m, cap, strings.NewReader("data"))
		if !errors.As(err, &ae) || ae.Code != apperr.CodeLargeChannelUnavailable {
			t.Fatalf("通道不可用应确定性失败，得到 %v", err)
		}
		if large.mediaCalls != 0 || api.mediaCalls != 0 {
			t.Errorf("不应有任何发送调用: large=%d api=%d", large.mediaCalls, api.mediaCalls)
		}
	})

	t.Run("未超上限 → Bot API（通道不可用也不受影响）", func(t *testing.T) {
		large := &fakeLargeSender{available: false}
		api := &fakeAPISender{}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		m := message.Media{Kind: message.KindVideo, Size: testUploadCap}
		if _, err := s.SendMedia(ctx, 7, m, cap, strings.NewReader("data")); err != nil {
			t.Fatalf("Bot API 上传应成功: %v", err)
		}
		if api.mediaCalls != 1 || large.mediaCalls != 0 {
			t.Fatalf("应只调 Bot API 通道: large=%d api=%d", large.mediaCalls, api.mediaCalls)
		}
	})

	t.Run("缺 reader → 契约防御", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		s := NewRouter(&fakeAPISender{}, large, testUploadCap, testLargeCap, testLogger())
		var ae *apperr.AppError
		_, err := s.SendMedia(ctx, 7, message.Media{Kind: message.KindPhoto, Size: 1}, cap, nil)
		if !errors.As(err, &ae) || ae.Code != apperr.CodeInternal {
			t.Fatalf("无 reader 应防御报错，得到 %v", err)
		}
	})
}

func TestRouterSendAlbumDispatch(t *testing.T) {
	ctx := context.Background()

	t.Run("全员在上限内 → Bot API 整组", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindPhoto, Size: 10}, Reader: strings.NewReader("a")},
			{Media: message.Media{Kind: message.KindPhoto, Size: 20}, Reader: strings.NewReader("b")},
		}
		if _, err := s.SendAlbum(ctx, 7, entries); err != nil {
			t.Fatalf("整组上传应成功: %v", err)
		}
		if api.albumCalls != 1 || large.mediaCalls != 0 || large.albumCalls != 0 {
			t.Fatalf("应只调 Bot API 通道: api=%d large(media=%d album=%d)",
				api.albumCalls, large.mediaCalls, large.albumCalls)
		}
	})

	t.Run("混入超上限成员 → MTProto 整组直传", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindPhoto, Size: 10}, Reader: strings.NewReader("a"),
				Caption: message.Caption{Text: "图说明"}},
			{Media: message.Media{Kind: message.KindVideo, Size: testUploadCap + 1}, Reader: strings.NewReader("b"),
				Caption: message.Caption{Text: "视频说明"}},
		}
		if _, err := s.SendAlbum(ctx, 7, entries); err != nil {
			t.Fatalf("MTProto 整组直传应成功: %v", err)
		}
		if large.albumCalls != 1 || api.albumCalls != 0 || large.mediaCalls != 0 {
			t.Fatalf("应只调大文件通道整组: large(album=%d media=%d) api=%d",
				large.albumCalls, large.mediaCalls, api.albumCalls)
		}
		if len(large.lastMedias) != 2 || len(large.lastReaders) != 2 || len(large.lastCaptions) != 2 {
			t.Fatalf("整组调用应透传全部成员: medias=%d readers=%d captions=%d",
				len(large.lastMedias), len(large.lastReaders), len(large.lastCaptions))
		}
		if large.lastMedias[1].Size != entries[1].Media.Size {
			t.Errorf("成员顺序应保持（第 2 项应为超限视频）: %+v", large.lastMedias)
		}
		if large.lastReaders[0] == nil || large.lastReaders[1] == nil {
			t.Error("整组调用应携带全部数据源")
		}
		// 归一化：两条语义 caption 合并进组首，其余成员清零
		if large.lastCaptions[0].Text != "图说明\n\n视频说明" {
			t.Errorf("caption 应合并进组首: %q", large.lastCaptions[0].Text)
		}
		if large.lastCaptions[1].Text != "" || len(large.lastCaptions[1].Entities) != 0 {
			t.Errorf("非组首 caption 应清零: %+v", large.lastCaptions[1])
		}
	})

	t.Run("混入超上限成员且通道不可用 → 确定性失败（零网络）", func(t *testing.T) {
		large := &fakeLargeSender{available: false}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindVideo, Size: testUploadCap + 1}, Reader: strings.NewReader("b")},
			{Media: message.Media{Kind: message.KindPhoto, Size: 10}, Reader: strings.NewReader("a")},
		}
		var ae *apperr.AppError
		_, err := s.SendAlbum(ctx, 7, entries)
		if !errors.As(err, &ae) || ae.Code != apperr.CodeLargeChannelUnavailable {
			t.Fatalf("通道不可用应确定性失败，得到 %v", err)
		}
		if api.albumCalls != 0 || large.albumCalls != 0 {
			t.Errorf("不应发出任何整组请求: api=%d large=%d", api.albumCalls, large.albumCalls)
		}
	})

	t.Run("成员超出双通道上限 → 防御错误（调用方应逐条发送）", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindVideo, Size: testLargeCap + 1}, Reader: strings.NewReader("b")},
		}
		var ae *apperr.AppError
		_, err := s.SendAlbum(ctx, 7, entries)
		if !errors.As(err, &ae) || ae.Code != apperr.CodeInternal {
			t.Fatalf("超双通道上限成员应防御报错，得到 %v", err)
		}
		if api.albumCalls != 0 || large.albumCalls != 0 {
			t.Error("不应发出整组请求")
		}
	})

	t.Run("全 document 组（分卷拆分段）→ MTProto 整组直传", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindDocument, FileName: "big.mkv.part1of2", Size: testUploadCap + 1},
				Reader: strings.NewReader("p1"), Caption: message.Caption{Text: "首段"}},
			{Media: message.Media{Kind: message.KindDocument, FileName: "big.mkv.part2of2", Size: testUploadCap + 1},
				Reader: strings.NewReader("p2")},
		}
		ids, err := s.SendAlbum(ctx, 7, entries)
		if err != nil {
			t.Fatalf("document 整组应走大文件通道: %v", err)
		}
		if large.albumCalls != 1 || api.albumCalls != 0 {
			t.Fatalf("应只调大文件通道整组: large=%d api=%d", large.albumCalls, api.albumCalls)
		}
		if len(ids) != 2 {
			t.Fatalf("应透传消息 ID: %v", ids)
		}
	})

	t.Run("缺 Reader → 契约防御", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindPhoto, Size: 10}, Reader: strings.NewReader("a")},
			{Media: message.Media{Kind: message.KindPhoto, Size: 20}},
		}
		var ae *apperr.AppError
		_, err := s.SendAlbum(ctx, 7, entries)
		if !errors.As(err, &ae) || ae.Code != apperr.CodeInternal {
			t.Fatalf("无 Reader 应防御报错，得到 %v", err)
		}
	})
}

// 路由透传类方法：文本/删除委托 Bot API；整组判定 = Bot API 承载 ∪ MTProto
// 整组直传承载（video 超 uploadCap 但 ≤ largeCap）。
func TestRouterDelegation(t *testing.T) {
	large := &fakeLargeSender{available: false}
	api := &fakeAPISender{groupable: true}
	s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())
	ctx := context.Background()

	if _, err := s.SendMessage(ctx, 7, "hi"); err != nil || api.messageCalls != 1 {
		t.Fatalf("文本应委托 Bot API: %v", err)
	}
	if err := s.DeleteMessage(ctx, 7, 5); err != nil || api.deleteCalls != 1 {
		t.Fatalf("删除应委托 Bot API: %v", err)
	}
	if !s.AlbumGroupable(message.Media{Kind: message.KindVideo, Size: 10}) {
		t.Fatal("上限内整组判定应委托 Bot API")
	}
	if !s.AlbumGroupable(message.Media{Kind: message.KindVideo, Size: testUploadCap + 1}) {
		t.Fatal("超 Bot API 上限的 video 应可经 MTProto 整组直传承载")
	}
	if s.AlbumGroupable(message.Media{Kind: message.KindVideo, Size: testLargeCap + 1}) {
		t.Fatal("超出 MTProto 上限的媒体不应进入整组")
	}
}

// TestRouterCopyMessagesDelegation 复制是服务端操作，与媒体大小无关，
// 应始终委托 Bot API 通道。
func TestRouterCopyMessagesDelegation(t *testing.T) {
	api := &fakeAPISender{}
	s := NewRouter(api, &fakeLargeSender{available: true}, testUploadCap, testLargeCap, testLogger())
	ids, err := s.CopyMessages(context.Background(), 111, 222, []int{7, 8})
	if err != nil {
		t.Fatalf("复制应成功: %v", err)
	}
	if api.copyCalls != 1 || len(ids) != 2 {
		t.Fatalf("应恰好委托一次并透传 ID 列表: calls=%d ids=%v", api.copyCalls, ids)
	}
}

// TestRouterAlbumCaptionNormalization 发送前把整组 caption 归一化为"恰好
// 组首一条"：客户端对多成员带 caption 的相册首渲染抑制组级展示位（真机
// 2026-09-20 五组实验），全部成员语义 caption 按源顺序合并进组首（正文、
// 切段说明、实体保留），其余成员清零——两条整组通道从首次请求起就收到
// 规范形态，无任何发送后 caption 编辑。归一化不修改调用方条目。
func TestRouterAlbumCaptionNormalization(t *testing.T) {
	ctx := context.Background()

	t.Run("MTProto 整组 → 合并进组首、其余清零", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindPhoto, Size: 10}, Reader: strings.NewReader("a"),
				Caption: message.Caption{Text: "图片说明"}},
			{Media: message.Media{Kind: message.KindVideo, Size: testUploadCap + 1}, Reader: strings.NewReader("b"),
				Caption: message.Caption{Text: "视频说明"}},
			{Media: message.Media{Kind: message.KindVideo, Size: testUploadCap + 1}, Reader: strings.NewReader("c")},
		}
		if _, err := s.SendAlbum(ctx, 7, entries); err != nil {
			t.Fatalf("MTProto 整组直传应成功: %v", err)
		}
		if large.albumCalls != 1 {
			t.Fatalf("应一次整组直传: %d", large.albumCalls)
		}
		if large.lastCaptions[0].Text != "图片说明\n\n视频说明" {
			t.Errorf("caption 应按源顺序合并进组首: %q", large.lastCaptions[0].Text)
		}
		for i := 1; i < len(large.lastCaptions); i++ {
			if large.lastCaptions[i].Text != "" {
				t.Errorf("成员 %d caption 应清零: %q", i, large.lastCaptions[i].Text)
			}
		}
		if len(api.captionEdits) != 0 {
			t.Errorf("不应有任何发送后 caption 编辑: %+v", api.captionEdits)
		}
		// 调用方条目不被修改
		if entries[0].Caption.Text != "图片说明" || entries[1].Caption.Text != "视频说明" {
			t.Errorf("归一化不应修改调用方条目: %q %q", entries[0].Caption.Text, entries[1].Caption.Text)
		}
	})

	t.Run("Bot API 整组（普通相册）→ 同样归一化", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindPhoto, Size: 10}, Reader: strings.NewReader("a"),
				Caption: message.Caption{Text: "图一"}},
			{Media: message.Media{Kind: message.KindPhoto, Size: 20}, Reader: strings.NewReader("b"),
				Caption: message.Caption{Text: "图二"}},
		}
		if _, err := s.SendAlbum(ctx, 7, entries); err != nil {
			t.Fatalf("Bot API 整组应成功: %v", err)
		}
		if api.albumCalls != 1 {
			t.Fatalf("应一次整组上传: %d", api.albumCalls)
		}
		if api.lastEntries[0].Caption.Text != "图一\n\n图二" {
			t.Errorf("普通相册也应归一化进组首: %q", api.lastEntries[0].Caption.Text)
		}
		if api.lastEntries[1].Caption.Text != "" {
			t.Errorf("第二成员 caption 应清零: %q", api.lastEntries[1].Caption.Text)
		}
		if len(api.captionEdits) != 0 {
			t.Errorf("不应有任何发送后 caption 编辑: %+v", api.captionEdits)
		}
	})

	t.Run("Bot API 整组含拆分段（本地服务器模式形态）→ 同样归一化", func(t *testing.T) {
		// 本地 Bot API 服务器：uploadCap = MaxFileSize，分段全员落在 Bot API
		// 承载内、整组走 sendMediaGroup 分支——与 MTProto 分支同一归一化规则，
		// 不再有 Split 特判
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindPhoto, Size: 10}, Reader: strings.NewReader("a"),
				Caption: message.Caption{Text: "图片说明"}},
			{Media: message.Media{Kind: message.KindDocument, FileName: "big.part1of2", Size: testUploadCap},
				Reader: strings.NewReader("p1"), Caption: message.Caption{Text: "大文件\n\n已切分为 2 段视频"}},
			{Media: message.Media{Kind: message.KindDocument, FileName: "big.part2of2", Size: testUploadCap},
				Reader: strings.NewReader("p2")},
		}
		if _, err := s.SendAlbum(ctx, 7, entries); err != nil {
			t.Fatalf("Bot API 整组应成功: %v", err)
		}
		if api.albumCalls != 1 || large.albumCalls != 0 {
			t.Fatalf("应只调 Bot API 通道: api=%d large=%d", api.albumCalls, large.albumCalls)
		}
		got := api.lastEntries[0].Caption.Text
		if !strings.Contains(got, "图片说明") || !strings.Contains(got, "大文件") || !strings.Contains(got, "已切分为 2 段视频") {
			t.Errorf("组首应合并图片正文、视频正文与切段说明: %q", got)
		}
		if api.lastEntries[1].Caption.Text != "" || api.lastEntries[2].Caption.Text != "" {
			t.Errorf("分段 caption 应清零: %+v %+v", api.lastEntries[1].Caption, api.lastEntries[2].Caption)
		}
		if len(api.captionEdits) != 0 {
			t.Errorf("不应有任何发送后 caption 编辑: %+v", api.captionEdits)
		}
	})

	t.Run("合并保留实体并平移 UTF-16 偏移", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindPhoto, Size: 10}, Reader: strings.NewReader("a"),
				Caption: message.Caption{Text: "ab", Entities: []tg.MessageEntityClass{&tg.MessageEntityBold{Offset: 0, Length: 2}}}},
			{Media: message.Media{Kind: message.KindVideo, Size: testUploadCap + 1}, Reader: strings.NewReader("b"),
				Caption: message.Caption{Text: "cd", Entities: []tg.MessageEntityClass{&tg.MessageEntityItalic{Offset: 0, Length: 2}}}},
		}
		if _, err := s.SendAlbum(ctx, 7, entries); err != nil {
			t.Fatalf("整组应成功: %v", err)
		}
		merged := large.lastCaptions[0]
		if merged.Text != "ab\n\ncd" {
			t.Fatalf("合并文本不符: %q", merged.Text)
		}
		if len(merged.Entities) != 2 {
			t.Fatalf("应保留两条实体: %+v", merged.Entities)
		}
		first, ok := merged.Entities[0].(*tg.MessageEntityBold)
		if !ok || first.Offset != 0 || first.Length != 2 {
			t.Errorf("组首实体应保持原偏移: %+v", merged.Entities[0])
		}
		second, ok := merged.Entities[1].(*tg.MessageEntityItalic)
		if !ok || second.Offset != 4 || second.Length != 2 {
			t.Errorf("后续实体应平移到前缀之后（UTF-16）: %+v", merged.Entities[1])
		}
	})

	t.Run("恰好组首一条 → 原样透传", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap, testLogger())

		entries := []AlbumEntry{
			{Media: message.Media{Kind: message.KindPhoto, Size: 10}, Reader: strings.NewReader("a"),
				Caption: message.Caption{Text: "唯一说明", Channels: []message.ChannelLink{{Label: "频道", URL: "https://t.me/c"}}}},
			{Media: message.Media{Kind: message.KindVideo, Size: testUploadCap + 1}, Reader: strings.NewReader("b")},
		}
		if _, err := s.SendAlbum(ctx, 7, entries); err != nil {
			t.Fatalf("整组应成功: %v", err)
		}
		if large.lastCaptions[0].Text != "唯一说明" || len(large.lastCaptions[0].Channels) != 1 {
			t.Errorf("单条 caption 应原样透传: %+v", large.lastCaptions[0])
		}
	})
}
