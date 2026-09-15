package dumpcache

// dumpcache 单测：干净副本三形态构造（caption 无频道脚注）、批量清洗、
// 写失败不落条目、Entry/CopyOut/Enabled。

import (
	"errors"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeSender 记录缓存频道相关的全部调用。
type fakeSender struct {
	mu           sync.Mutex
	sent         []string      // SendMessage
	copyMsgs     [][2]int64    // CopyMessages from/to
	singleCopies []singleCopy  // CopyMessage
	capEdits     []captionEdit // EditMessageCaption
	txtEdits     []captionEdit // EditMessageText
	failCopies   bool
}

type singleCopy struct {
	From, To int64
	MsgID    int
	Caption  string
}

type captionEdit struct {
	ChatID  int64
	MsgID   int
	Caption string
}

func (f *fakeSender) SendMessage(_ context.Context, _ int64, html string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, html)
	return 600 + len(f.sent), nil
}
func (f *fakeSender) EditMessageText(_ context.Context, chatID int64, msgID int, html string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.txtEdits = append(f.txtEdits, captionEdit{chatID, msgID, html})
	return nil
}
func (f *fakeSender) SendMedia(context.Context, int64, message.Media, message.Caption, io.Reader) (int, error) {
	return 1, nil
}
func (f *fakeSender) SendAlbum(context.Context, int64, []delivery.AlbumEntry) ([]int, error) {
	return nil, nil
}
func (f *fakeSender) AlbumGroupable(message.Media) bool               { return false }
func (f *fakeSender) DeleteMessage(context.Context, int64, int) error { return nil }
func (f *fakeSender) CopyMessages(_ context.Context, from, to int64, ids []int) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.copyMsgs = append(f.copyMsgs, [2]int64{from, to})
	if f.failCopies {
		return nil, errBoom
	}
	out := make([]int, len(ids))
	for i := range ids {
		out[i] = 700 + i
	}
	return out, nil
}
func (f *fakeSender) CopyMessage(_ context.Context, from, to int64, msgID int, caption string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.singleCopies = append(f.singleCopies, singleCopy{from, to, msgID, caption})
	if f.failCopies {
		return 0, errBoom
	}
	return 800 + len(f.singleCopies), nil
}
func (f *fakeSender) EditMessageCaption(_ context.Context, chatID int64, msgID int, caption string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capEdits = append(f.capEdits, captionEdit{chatID, msgID, caption})
	return nil
}

var errBoom = &fakeErr{}

type fakeErr struct{}

func (*fakeErr) Error() string { return "boom" }

// openStore 建独立临时库。
func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir()+"/test.db", testLog())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mediaItem(id int, text string) message.Item {
	return message.Item{ID: id, GroupedID: 42, Text: text,
		Media: &message.Media{Kind: message.KindPhoto, FileName: "p.jpg", Size: 10}}
}

func TestEnabledRequiresChannel(t *testing.T) {
	var nilSvc *Service
	if nilSvc.Enabled() {
		t.Fatal("nil 服务不应 Enabled")
	}
	s := New(&fakeSender{}, openStore(t), func() int64 { return 0 }, testLog())
	if s.Enabled() {
		t.Fatal("channelID=0 不应 Enabled")
	}
	if e, ok := s.Entry(context.Background(), "k", 1); ok || e.ID != 0 {
		t.Fatal("未配置时 Entry 应返回零值/false")
	}
}

func TestWriteCleanSingleMedia(t *testing.T) {
	st := openStore(t)
	fs := &fakeSender{}
	s := New(fs, st, func() int64 { return -1001234567890 }, testLog())
	ctx := context.Background()

	items := []message.Item{mediaItem(7, "正文")}
	s.WriteClean(ctx, 111, "example", 7, items, []int{55}, "https://t.me/example/7")

	if len(fs.singleCopies) != 1 {
		t.Fatalf("单媒体应走 CopyMessage，得到 %+v", fs.singleCopies)
	}
	c := fs.singleCopies[0]
	if c.From != 111 || c.To != -1001234567890 || c.MsgID != 55 {
		t.Fatalf("复制方向/目标不符: %+v", c)
	}
	if c.Caption == "" {
		t.Fatal("干净 caption 不应为空")
	}
	e, err := st.LatestDumpEntry(ctx, "example", 7)
	if err != nil {
		t.Fatalf("应落条目: %v", err)
	}
	if len(e.DumpIDs) != 1 {
		t.Fatalf("条目应记录副本 ID: %+v", e)
	}
}

func TestWriteCleanSingleText(t *testing.T) {
	st := openStore(t)
	fs := &fakeSender{}
	s := New(fs, st, func() int64 { return -1001234567890 }, testLog())

	s.WriteClean(context.Background(), 111, "example", 7,
		[]message.Item{{ID: 7, Text: "纯文本"}}, []int{66}, "https://t.me/example/7")

	if len(fs.sent) != 1 || len(fs.singleCopies) != 0 {
		t.Fatalf("纯文本应走 SendMessage: sent=%v copy=%v", fs.sent, fs.singleCopies)
	}
	if _, err := st.LatestDumpEntry(context.Background(), "example", 7); err != nil {
		t.Fatalf("应落条目: %v", err)
	}
}

func TestWriteCleanAlbumBatchAndEdits(t *testing.T) {
	st := openStore(t)
	fs := &fakeSender{}
	s := New(fs, st, func() int64 { return -1001234567890 }, testLog())
	ctx := context.Background()

	items := []message.Item{mediaItem(7, "图一"), mediaItem(8, "图二"), {ID: 9, Text: "附言"}}
	s.WriteClean(ctx, 111, "example", 7, items, []int{55, 56, 57}, "https://t.me/example/7")

	if len(fs.copyMsgs) != 1 || fs.copyMsgs[0] != [2]int64{111, -1001234567890} {
		t.Fatalf("多条应整批 CopyMessages: %+v", fs.copyMsgs)
	}
	if len(fs.capEdits) != 2 { // 两个媒体成员清洗 caption
		t.Fatalf("媒体成员应逐条清洗: %+v", fs.capEdits)
	}
	if len(fs.txtEdits) != 1 { // 文本成员清洗文本
		t.Fatalf("文本成员应清洗: %+v", fs.txtEdits)
	}
	for _, e := range append(append([]captionEdit{}, fs.capEdits...), fs.txtEdits...) {
		if e.ChatID != -1001234567890 {
			t.Fatalf("清洗应指向缓存频道: %+v", e)
		}
	}
	e, err := st.LatestDumpEntry(ctx, "example", 7)
	if err != nil || len(e.DumpIDs) != 3 {
		t.Fatalf("应落整组条目: %+v err=%v", e, err)
	}
}

func TestWriteCleanFailureNoEntry(t *testing.T) {
	st := openStore(t)
	fs := &fakeSender{failCopies: true}
	s := New(fs, st, func() int64 { return -1001234567890 }, testLog())
	ctx := context.Background()

	s.WriteClean(ctx, 111, "example", 7,
		[]message.Item{mediaItem(7, "x")}, []int{55}, "https://t.me/example/7")
	if _, err := st.LatestDumpEntry(ctx, "example", 7); err == nil {
		t.Fatal("复制失败不应落条目")
	}

	// 条目数与已发送数不一致同样跳过
	s2 := New(&fakeSender{}, st, func() int64 { return -1001234567890 }, testLog())
	s2.WriteClean(ctx, 111, "example", 8, []message.Item{mediaItem(8, "x"), mediaItem(9, "y")}, []int{55}, "")
	if _, err := st.LatestDumpEntry(ctx, "example", 8); err == nil {
		t.Fatal("数量不一致不应落条目")
	}
}

func TestCopyOut(t *testing.T) {
	st := openStore(t)
	fs := &fakeSender{}
	s := New(fs, st, func() int64 { return -1001234567890 }, testLog())
	if _, err := st.InsertDumpEntry(context.Background(), store.DumpEntry{
		ChannelKey: "example", MessageID: 7, DumpIDs: []int{701, 702}}); err != nil {
		t.Fatalf("落条目失败: %v", err)
	}
	e, ok := s.Entry(context.Background(), "example", 7)
	if !ok || len(e.DumpIDs) != 2 {
		t.Fatalf("应命中条目: %+v ok=%v", e, ok)
	}
	ids, err := s.CopyOut(context.Background(), 222, e.DumpIDs)
	if err != nil || len(ids) != 2 {
		t.Fatalf("复制应成功: %v %v", ids, err)
	}
	if len(fs.copyMsgs) != 1 || fs.copyMsgs[0] != [2]int64{-1001234567890, 222} {
		t.Fatalf("应从缓存频道复制到目标聊天: %+v", fs.copyMsgs)
	}
	if _, ok := s.Entry(context.Background(), "other", 7); ok {
		t.Fatal("其他链接不应命中")
	}
}

// TestCleanCaptionNoChannels 干净 caption 不织频道脚注；links 非空时织入。
func TestCleanCaptionNoChannels(t *testing.T) {
	it := mediaItem(7, "正文")
	got := CleanCaption(it, true, "https://t.me/example/7", nil)
	if got == "" || !contains(got, "正文") || !contains(got, "t.me/example/7") {
		t.Fatalf("干净 caption 应含正文与原链接: %q", got)
	}
	withLinks := CleanCaption(it, true, "https://t.me/example/7",
		[]message.ChannelLink{{Label: "频道", URL: "https://t.me/chan"}})
	if contains(got, "t.me/chan") {
		t.Fatal("无 links 时不应出现频道链接")
	}
	if !contains(withLinks, "t.me/chan") {
		t.Fatal("有 links 时应织入频道链接")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// fakeProber 是 MessageProber 假实现：返回预设结果或错误（EntryLive 判定）。
type fakeProber struct {
	present bool
	err     error
	calls   int
}

func (f *fakeProber) ChannelMessagesPresent(_ context.Context, _ int64, _ []int) (bool, error) {
	f.calls++
	return f.present, f.err
}

// ---- EntryLive：副本失效判定（条目在、消息可能已被删） ----

func TestEntryLive(t *testing.T) {
	ctx := context.Background()

	t.Run("无条目可补写", func(t *testing.T) {
		st := openStore(t)
		s := New(&fakeSender{}, st, func() int64 { return -100123 }, testLog())
		live, err := s.EntryLive(ctx, "example", 7)
		if err != nil || live {
			t.Fatalf("无条目应 (false, nil): %v err=%v", live, err)
		}
	})

	t.Run("条目存在但未注入校验通道时保守视为有效", func(t *testing.T) {
		st := openStore(t)
		s := New(&fakeSender{}, st, func() int64 { return -100123 }, testLog())
		seedEntry(t, st)
		live, err := s.EntryLive(ctx, "example", 7)
		if err != nil || !live {
			t.Fatalf("无 prober 应保守有效: %v err=%v", live, err)
		}
	})

	t.Run("消息已删除放行补写", func(t *testing.T) {
		st := openStore(t)
		s := New(&fakeSender{}, st, func() int64 { return -100123 }, testLog())
		seedEntry(t, st)
		prober := &fakeProber{present: false}
		s.SetProber(prober)
		live, err := s.EntryLive(ctx, "example", 7)
		if err != nil || live {
			t.Fatalf("失效副本应放行 (false, nil): %v err=%v", live, err)
		}
		if prober.calls != 1 {
			t.Fatalf("应恰好一次校验: %d", prober.calls)
		}
	})

	t.Run("消息仍在判定有效", func(t *testing.T) {
		st := openStore(t)
		s := New(&fakeSender{}, st, func() int64 { return -100123 }, testLog())
		seedEntry(t, st)
		s.SetProber(&fakeProber{present: true})
		live, err := s.EntryLive(ctx, "example", 7)
		if err != nil || !live {
			t.Fatalf("有效副本应 (true, nil): %v err=%v", live, err)
		}
	})

	t.Run("校验故障保守视为有效", func(t *testing.T) {
		st := openStore(t)
		s := New(&fakeSender{}, st, func() int64 { return -100123 }, testLog())
		seedEntry(t, st)
		s.SetProber(&fakeProber{err: errors.New("mtproto offline")})
		live, err := s.EntryLive(ctx, "example", 7)
		if err == nil || !live {
			t.Fatalf("校验故障应保守 (true, err): %v err=%v", live, err)
		}
	})
}

func seedEntry(t *testing.T, st *store.Store) {
	t.Helper()
	if _, err := st.InsertDumpEntry(context.Background(), store.DumpEntry{
		ChannelKey: "example", MessageID: 7, DumpIDs: []int{501, 502},
	}); err != nil {
		t.Fatalf("落条目失败: %v", err)
	}
}
