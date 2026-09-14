package delivery

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

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
}

func (f *fakeAPISender) SendMessage(context.Context, int64, string) (int, error) {
	f.messageCalls++
	return 1, nil
}

func (f *fakeAPISender) SendMedia(context.Context, int64, message.Media, message.Caption, io.Reader) (int, error) {
	f.mediaCalls++
	return 1, nil
}

func (f *fakeAPISender) SendAlbum(context.Context, int64, []AlbumEntry) ([]int, error) {
	f.albumCalls++
	return []int{1, 2}, nil
}

func (f *fakeAPISender) AlbumGroupable(message.Media) bool { return f.groupable }
func (f *fakeAPISender) CopyMessage(_ context.Context, _, _ int64, messageID int, _ string) (int, error) {
	f.copyCalls++
	return messageID, nil
}

func (f *fakeAPISender) EditMessageCaption(context.Context, int64, int, string) error { return nil }

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

func (f *fakeAPISender) EditMessageText(_ context.Context, _ int64, _ int, html string) error {
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
		s := NewRouter(api, large, testUploadCap, testLargeCap)

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
		s := NewRouter(api, large, testUploadCap, testLargeCap)

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
		s := NewRouter(api, large, testUploadCap, testLargeCap)

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
		s := NewRouter(&fakeAPISender{}, large, testUploadCap, testLargeCap)
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
		s := NewRouter(api, large, testUploadCap, testLargeCap)

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
		s := NewRouter(api, large, testUploadCap, testLargeCap)

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
		if large.lastCaptions[0].Text != "图说明" || large.lastCaptions[1].Text != "视频说明" {
			t.Errorf("整组调用应逐成员透传 caption: %+v", large.lastCaptions)
		}
	})

	t.Run("混入超上限成员且通道不可用 → 确定性失败（零网络）", func(t *testing.T) {
		large := &fakeLargeSender{available: false}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap)

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
		s := NewRouter(api, large, testUploadCap, testLargeCap)

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

	t.Run("缺 Reader → 契约防御", func(t *testing.T) {
		large := &fakeLargeSender{available: true}
		api := &fakeAPISender{groupable: true}
		s := NewRouter(api, large, testUploadCap, testLargeCap)

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
	s := NewRouter(api, large, testUploadCap, testLargeCap)
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
	s := NewRouter(api, &fakeLargeSender{available: true}, testUploadCap, testLargeCap)
	ids, err := s.CopyMessages(context.Background(), 111, 222, []int{7, 8})
	if err != nil {
		t.Fatalf("复制应成功: %v", err)
	}
	if api.copyCalls != 1 || len(ids) != 2 {
		t.Fatalf("应恰好委托一次并透传 ID 列表: calls=%d ids=%v", api.copyCalls, ids)
	}
}
