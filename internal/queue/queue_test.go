package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// ---- 测试基础设施 ----

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// openStore 在临时目录建独立测试库（queue 的 requests 埋点测试用）。
func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), testLog())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// fakeFetcher 可编程的取数假实现：
// block 非空时 Fetch 阻塞到 ctx 取消（模拟进程退出时的在途任务）；
// api 为空时 API() 返回 nil（仅取数路径的测试用）。
type fakeFetcher struct {
	msgs         []*tg.Message
	err          error
	block        bool
	refresh      *refreshResult
	refreshCalls int // RefreshMedia 被调用次数（过期刷新重试断言用）
	api          *tg.Client
}

func (f *fakeFetcher) Fetch(ctx context.Context, _ tmeurl.SourceRef) ([]*tg.Message, error) {
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.msgs, f.err
}

// refreshResult 是可编程的 RefreshMedia 返回值（引用过期刷新重试测试用）。
type refreshResult struct {
	media message.Media
	ok    bool
}

func (f *fakeFetcher) RefreshMedia(context.Context, tmeurl.SourceRef, int) (message.Media, bool, error) {
	f.refreshCalls++
	if f.refresh == nil {
		return message.Media{}, false, nil
	}
	return f.refresh.media, f.refresh.ok, nil
}

func (f *fakeFetcher) API() *tg.Client { return f.api }

// mediaCall 记录一次 SendMedia 调用的信息。
type mediaCall struct {
	Kind      message.ItemKind
	Caption   message.Caption
	HasReader bool  // 契约要求恒为 true（上传路径必带数据源）
	ReadErr   error // consumeMediaReaders 时读源错误（nil = 成功读尽）
	ThumbLen  int   // 解析好的缩略图字节数（0 = 不带封面）
}

// albumCall 记录一次 SendAlbum 调用的信息。
type albumCall struct {
	Kinds     []message.ItemKind
	Captions  []message.Caption // 逐成员语义 caption（发送前归一化前 worker 构造的形态）
	ReadLens  []int             // consumeAlbumReaders 时逐成员读得的字节数
	ReadErrs  []error           // 逐成员读源错误（nil = 成功读尽）
	ThumbLens []int             // 逐成员缩略图字节数（0 = 不带封面）
}

// copyCall 记录一次 CopyMessages 调用（复用路径）。
type copyCall struct {
	FromChatID int64
	ChatID     int64
	MessageIDs []int
}

// singleCopyCall 记录一次 CopyMessage 调用（缓存频道干净副本构造）。
type singleCopyCall struct {
	FromChatID int64
	ChatID     int64
	MessageID  int
	Caption    string
}

// captionEditCall 记录一次 EditMessageCaption 调用（副本清洗/补脚注）。
type captionEditCall struct {
	ChatID    int64
	MessageID int
	Caption   string
}

// fakeSender 记录全部发送与删除调用，可按调用注入错误。
type fakeSender struct {
	mu              sync.Mutex
	sent            []string
	deleted         []int
	edits           []string
	editErr         error // 非 nil 时编辑恒败（验证熔断）
	mediaCalls      []mediaCall
	albumCalls      []albumCall
	copyCalls       []copyCall
	singleCopyCalls []singleCopyCall
	captionEdits    []captionEditCall
	groupable       func(message.Media) bool                  // nil = 一律不可整组
	mediaErr        func(call mediaCall) error                // nil = 全部成功
	albumErr        func(entries []delivery.AlbumEntry) error // nil = 全部成功
	copyErr         func(call copyCall) error                 // nil = 全部成功

	consumeMediaReaders bool // 单媒体调用时读尽 Reader 并传播读错误（模拟真实上传侧）
	consumeAlbumReaders bool // 整组调用时逐成员读尽 Reader（模拟上传侧消费）

	captureAlbumContent bool     // 整组调用时逐成员留存读取内容（分段内容校验用）
	albumContent        [][]byte // captureAlbumContent 时按成员顺序留存（含读错误前的部分内容）
}

func (f *fakeSender) SendMessage(_ context.Context, _ int64, html string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, html)
	return 100 + len(f.sent), nil
}

func (f *fakeSender) SendMedia(_ context.Context, _ int64, m message.Media, caption message.Caption, reader io.Reader) (int, error) {
	var readErr error
	if f.consumeMediaReaders { // 模拟上传侧读源：下载错误经 reader 在发送阶段传播
		_, readErr = io.Copy(io.Discard, reader)
	}
	call := mediaCall{Kind: m.Kind, Caption: caption, HasReader: reader != nil, ReadErr: readErr,
		ThumbLen: len(m.ThumbJPEG)}
	f.mu.Lock()
	f.mediaCalls = append(f.mediaCalls, call)
	mediaErr := f.mediaErr
	f.mu.Unlock()
	if readErr != nil {
		return 0, readErr
	}
	if mediaErr != nil {
		return 0, mediaErr(call)
	}
	return 200 + len(f.mediaCalls), nil
}

func (f *fakeSender) SendAlbum(_ context.Context, _ int64, entries []delivery.AlbumEntry) ([]int, error) {
	kinds := make([]message.ItemKind, 0, len(entries))
	captions := make([]message.Caption, 0, len(entries))
	thumbLens := make([]int, 0, len(entries))
	for _, e := range entries {
		kinds = append(kinds, e.Media.Kind)
		captions = append(captions, e.Caption)
		thumbLens = append(thumbLens, len(e.Media.ThumbJPEG))
	}
	call := albumCall{Kinds: kinds, Captions: captions, ThumbLens: thumbLens}
	var content [][]byte
	if f.consumeAlbumReaders { // 模拟上传侧读源：驱动流式/内存管道的下载
		for _, e := range entries {
			data, err := io.ReadAll(e.Reader)
			call.ReadLens = append(call.ReadLens, len(data))
			call.ReadErrs = append(call.ReadErrs, err)
			if f.captureAlbumContent {
				content = append(content, data)
			}
		}
	}
	f.mu.Lock()
	f.albumCalls = append(f.albumCalls, call)
	if content != nil {
		f.albumContent = append(f.albumContent, content...)
	}
	albumErr := f.albumErr
	f.mu.Unlock()
	for _, err := range call.ReadErrs { // 读源失败即整组发送失败（真实 multipart/uploader 语义）
		if err != nil {
			return nil, err
		}
	}
	if albumErr != nil {
		return nil, albumErr(entries)
	}
	ids := make([]int, 0, len(entries))
	for i := range entries {
		ids = append(ids, 300+i)
	}
	return ids, nil
}

func (f *fakeSender) CopyMessages(_ context.Context, fromChatID, chatID int64, messageIDs []int) ([]int, error) {
	call := copyCall{FromChatID: fromChatID, ChatID: chatID,
		MessageIDs: append([]int(nil), messageIDs...)}
	f.mu.Lock()
	f.copyCalls = append(f.copyCalls, call)
	copyErr := f.copyErr
	f.mu.Unlock()
	if copyErr != nil {
		return nil, copyErr(call)
	}
	ids := make([]int, 0, len(messageIDs))
	for i := range messageIDs {
		ids = append(ids, 400+i)
	}
	return ids, nil
}

func (f *fakeSender) CopyMessage(_ context.Context, fromChatID, chatID int64, messageID int, captionHTML string) (int, error) {
	f.mu.Lock()
	f.singleCopyCalls = append(f.singleCopyCalls, singleCopyCall{
		FromChatID: fromChatID, ChatID: chatID, MessageID: messageID, Caption: captionHTML})
	copyErr := f.copyErr
	f.mu.Unlock()
	if copyErr != nil {
		return 0, copyErr(copyCall{FromChatID: fromChatID, ChatID: chatID, MessageIDs: []int{messageID}})
	}
	return 500 + len(f.singleCopyCalls), nil
}

func (f *fakeSender) EditMessageCaption(_ context.Context, chatID int64, messageID int, captionHTML string) error {
	f.mu.Lock()
	f.captionEdits = append(f.captionEdits, captionEditCall{ChatID: chatID, MessageID: messageID, Caption: captionHTML})
	f.mu.Unlock()
	return nil
}

func (f *fakeSender) AlbumGroupable(m message.Media) bool {
	if f.groupable == nil {
		return false
	}
	return f.groupable(m)
}
func (f *fakeSender) DeleteMessage(_ context.Context, _ int64, messageID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, messageID)
	return nil
}

func (f *fakeSender) EditMessageText(_ context.Context, _ int64, _ int, html string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.editErr != nil {
		return f.editErr
	}
	f.edits = append(f.edits, html)
	return nil
}

func (f *fakeSender) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}

func (f *fakeSender) deletedIDs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int(nil), f.deleted...)
}

func (f *fakeSender) mediaCallsSnapshot() []mediaCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]mediaCall(nil), f.mediaCalls...)
}

func (f *fakeSender) albumCallsSnapshot() []albumCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]albumCall(nil), f.albumCalls...)
}

func (f *fakeSender) albumContentSnapshot() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.albumContent...)
}

// contractSender 在 fakeSender 之上执行 delivery 的"媒体数据源"契约：
// 单媒体必须带 reader、相册成员必须全员带 reader，违例按真实实现返回
// INTERNAL_ERROR 防御错误，防止 worker 退化出发送即失败的调用形态。
type contractSender struct {
	*fakeSender
}

func (c *contractSender) SendMedia(ctx context.Context, chatID int64, m message.Media, caption message.Caption, reader io.Reader) (int, error) {
	if reader == nil {
		return 0, apperr.New(apperr.CodeInternal,
			"媒体发送要求数据源 reader 非空")
	}
	return c.fakeSender.SendMedia(ctx, chatID, m, caption, reader)
}

func (c *contractSender) SendAlbum(ctx context.Context, chatID int64, entries []delivery.AlbumEntry) ([]int, error) {
	for i, e := range entries {
		if e.Reader == nil {
			return nil, apperr.New(apperr.CodeInternal,
				fmt.Sprintf("相册第 %d 项缺少上传数据源", i))
		}
	}
	return c.fakeSender.SendAlbum(ctx, chatID, entries)
}

// newJobWithRequest 在测试库中建好用户与 queued 请求行，返回关联好的任务。
func newJobWithRequest(t *testing.T, s *store.Store, statusMsgID int) (Job, store.Request) {
	t.Helper()
	if _, err := s.CreateUser(context.Background(), store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	r, err := s.CreateRequest(context.Background(), store.Request{
		UserID: 1, SourceKind: store.SourcePublic, ChannelKey: "example", MessageID: 7,
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	return NewJob(1, 1, tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: 7},
		statusMsgID, r.ID), r
}

// waitDone 等待异步处理结束。
func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("等待任务处理超时")
	}
}

// ---- 阶段埋点 ----

func TestProcessSuccessLifecycle(t *testing.T) {
	s := openStore(t)
	job, r := newJobWithRequest(t, s, 55)
	sender := &fakeSender{}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Store:   s,
		Log:     testLog(),
	}

	done := make(chan struct{})
	go func() { Process(deps)(context.Background(), job); close(done) }()
	waitDone(t, done)

	got, err := s.GetRequest(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if got.Status != store.RequestSucceeded {
		t.Fatalf("终态应为 succeeded，得到 %s", got.Status)
	}
	if got.StartedAt == 0 || got.FinishedAt == 0 {
		t.Errorf("started_at/finished_at 应已写入: %+v", got)
	}
	if got.DurationMs < 0 || got.FinishedAt-got.StartedAt != got.DurationMs {
		t.Errorf("duration_ms 应等于 finished_at-started_at: %+v", got)
	}
	if got.MediaType != "text" || got.ErrorCode != "" {
		t.Errorf("成功应落媒体类型 text 且无错误码: %+v", got)
	}
	texts := sender.texts()
	wantText := `<blockquote><b>🔗 原消息</b>` + "\n" +
		`<a href="https://t.me/example/7">https://t.me/example/7</a></blockquote>` + "\n\n" +
		`<blockquote>hello</blockquote>`
	if len(texts) != 1 || texts[0] != wantText {
		t.Errorf("成功回传应在顶部包含原消息链接\nwant: %q\ngot:  %v", wantText, texts)
	}
	if ids := sender.deletedIDs(); len(ids) != 1 || ids[0] != 55 {
		t.Errorf("成功后应删除状态提示，得到 %v", ids)
	}
}

func TestProcessPrivateSourceLink(t *testing.T) {
	sender := &fakeSender{}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Log:     testLog(),
	}
	job := NewJob(1, 1, tmeurl.SourceRef{
		Kind: tmeurl.PeerChannelID, ChannelID: 1234567890, MessageID: 7,
	}, 0, 0)

	Process(deps)(context.Background(), job)

	texts := sender.texts()
	want := `<blockquote><b>🔗 原消息</b>` + "\n" +
		`<a href="https://t.me/c/1234567890/7">https://t.me/c/1234567890/7</a></blockquote>` + "\n\n" +
		`<blockquote>hello</blockquote>`
	if len(texts) != 1 || texts[0] != want {
		t.Fatalf("私有来源链接不符\nwant: %q\ngot:  %v", want, texts)
	}
}

func TestProcessInvalidSourceFallsBackToOriginalContent(t *testing.T) {
	sender := &fakeSender{}
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}},
		Sender:  sender,
		Log:     testLog(),
	}

	Process(deps)(context.Background(), NewJob(1, 1, tmeurl.SourceRef{}, 0, 0))

	if texts := sender.texts(); len(texts) != 1 || texts[0] != "<blockquote>hello</blockquote>" {
		t.Fatalf("无效来源应退化为原内容: %v", texts)
	}
}

func TestProcessEarlyFailLifecycle(t *testing.T) {
	s := openStore(t)
	job, r := newJobWithRequest(t, s, 55)
	sender := &fakeSender{}
	deps := Deps{
		Fetcher: &fakeFetcher{err: apperr.New(apperr.CodeChannelInaccessible, "no access")},
		Sender:  sender,
		Store:   s,
		Log:     testLog(),
	}

	done := make(chan struct{})
	go func() { Process(deps)(context.Background(), job); close(done) }()
	waitDone(t, done)

	got, _ := s.GetRequest(context.Background(), r.ID)
	if got.Status != store.RequestFailed || got.ErrorCode != "CHANNEL_NOT_ACCESSIBLE" {
		t.Fatalf("取数失败应落 failed + 错误码: %+v", got)
	}
	if got.MediaType != "" || got.FileName != "" || got.FileSize != 0 {
		t.Errorf("转换前的失败不应带媒体元数据: %+v", got)
	}
	if got.DurationMs < 0 || got.FinishedAt == 0 {
		t.Errorf("早失败也应写 finished_at/duration（回退 queued_at 起算）: %+v", got)
	}
	// 用户收到分类后的中文提示（附来源链接），状态提示被删除
	texts := sender.texts()
	if len(texts) != 1 || texts[0] != failureNoticeHTML(apperr.CodeChannelInaccessible, job.Ref) {
		t.Errorf("失败应回复用户文案（含来源链接）: %v", texts)
	}
	if ids := sender.deletedIDs(); len(ids) != 1 || ids[0] != 55 {
		t.Errorf("失败后应删除状态提示，得到 %v", ids)
	}
}

func TestFailureNoticeHTML(t *testing.T) {
	text := apperr.UserText(apperr.CodeFileTooLarge)
	ref := tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: 7}
	want := text + "\n" + `<a href="https://t.me/example/7">https://t.me/example/7</a>`
	if got := failureNoticeHTML(apperr.CodeFileTooLarge, ref); got != want {
		t.Fatalf("失败提示应为文案+可点击链接\nwant: %q\ngot:  %q", want, got)
	}
	// 链接不可重建（如私有频道键数据异常）：退化为纯错误文案
	if got := failureNoticeHTML(apperr.CodeFileTooLarge, tmeurl.SourceRef{}); got != text {
		t.Fatalf("链接不可重建应退化为纯文案: %q", got)
	}
}

func TestProcessFailAfterConvertKeepsMediaMeta(t *testing.T) {
	s := openStore(t)
	job, r := newJobWithRequest(t, s, 0)
	sender := &fakeSender{}
	deps := Deps{
		// 不支持的媒体类型：转换成功、发送前即失败，媒体元数据仍可落库
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{
			ID: 7, Message: "cap", Media: &tg.MessageMediaContact{},
		}}},
		Sender: sender,
		Store:  s,
		Log:    testLog(),
	}

	done := make(chan struct{})
	go func() { Process(deps)(context.Background(), job); close(done) }()
	waitDone(t, done)

	got, _ := s.GetRequest(context.Background(), r.ID)
	if got.Status != store.RequestFailed || got.ErrorCode != "MEDIA_UNSUPPORTED" {
		t.Fatalf("应落 failed(MEDIA_UNSUPPORTED): %+v", got)
	}
	if got.MediaType != "unsupported" {
		t.Errorf("转换后的失败应带媒体类型，得到 %q", got.MediaType)
	}
}

func TestProcessCancelledMarksInterrupted(t *testing.T) {
	s := openStore(t)
	job, r := newJobWithRequest(t, s, 55)
	sender := &fakeSender{}
	deps := Deps{
		Fetcher: &fakeFetcher{block: true}, // 取数阻塞直到 ctx 取消
		Sender:  sender,
		Store:   s,
		Log:     testLog(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Process(deps)(ctx, job); close(done) }()
	time.Sleep(50 * time.Millisecond) // 让任务进入处理中
	cancel()
	waitDone(t, done)

	got, _ := s.GetRequest(context.Background(), r.ID)
	if got.Status != store.RequestFailed || got.ErrorCode != "INTERRUPTED" {
		t.Fatalf("进程退出中断的任务应落 failed(INTERRUPTED): %+v", got)
	}
	// 关停路径不再用死 ctx 发用户提示（由 drain 窗口负责收尾）
	if texts := sender.texts(); len(texts) != 0 {
		t.Errorf("取消后不应再发错误提示: %v", texts)
	}
}

func TestMarkStartedWithoutStore(t *testing.T) {
	// Store 为 nil（或 Job 无 RequestID）时埋点跳过，不影响任务执行
	deps := Deps{
		Fetcher: &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hi"}}},
		Sender:  &fakeSender{},
		Log:     testLog(),
	}
	done := make(chan struct{})
	go func() {
		Process(deps)(context.Background(), NewJob(1, 1,
			tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: 7}, 0, 0))
		close(done)
	}()
	waitDone(t, done) // 不 panic、不报错即通过
}

// ---- 媒体元数据提取 ----

func TestMediaMetaOf(t *testing.T) {
	text := message.Item{ID: 1, Text: "hi"}
	photo := message.Item{ID: 2, GroupedID: 99, Text: "caption", Media: &message.Media{Kind: message.KindPhoto, DCID: 4, FileName: "photo.jpg", Size: 100}}
	photo2 := message.Item{ID: 3, GroupedID: 99, Media: &message.Media{Kind: message.KindPhoto, DCID: 4, FileName: "photo-2.jpg", Size: 200}}
	video := message.Item{ID: 4, GroupedID: 99, Media: &message.Media{Kind: message.KindVideo, DCID: 2, FileName: "clip.mp4", Size: 500}}

	got := mediaMetaOf([]message.Item{text})
	if got.MediaType != "text" || !slices.Equal(got.MediaTypes, []string{"text"}) || got.FileSize != 0 || got.FileName != "" {
		t.Errorf("文本消息元数据不对: %+v", got)
	}

	got = mediaMetaOf([]message.Item{photo, photo2})
	if got.MediaType != "album" || !slices.Equal(got.MediaTypes, []string{"photo"}) || got.FileSize != 300 || got.FileName != "photo.jpg" {
		t.Errorf("纯图片相册元数据不对: %+v", got)
	}

	got = mediaMetaOf([]message.Item{photo, video})
	if got.MediaType != "album" || !slices.Equal(got.MediaTypes, []string{"photo", "video"}) || got.FileSize != 600 || got.FileName != "photo.jpg" {
		t.Errorf("混合相册元数据不对: %+v", got)
	}
	if !slices.Equal(got.MediaDCIDs, []int{2, 4}) {
		t.Errorf("相册 DC 应去重并排序: %+v", got.MediaDCIDs)
	}

	standalonePhoto := photo
	standalonePhoto.GroupedID = 0
	got = mediaMetaOf([]message.Item{standalonePhoto})
	if got.MediaType != "photo" || !slices.Equal(got.MediaTypes, []string{"photo"}) {
		t.Errorf("带 caption 的单图片仍应记 photo: %+v", got)
	}
}

// ---- 既有行为回归：队列满 / 退出 drain ----

func TestEnqueueFull(t *testing.T) {
	q := New(1)
	if err := q.Enqueue(NewJob(1, 1, tmeurl.SourceRef{}, 0, 0)); err != nil {
		t.Fatalf("首个任务应入队成功: %v", err)
	}
	if !q.FullFor(false) {
		t.Error("容量 1 的队列应报告已满")
	}
	if err := q.Enqueue(NewJob(2, 2, tmeurl.SourceRef{}, 0, 0)); !errors.Is(err, ErrBusy) {
		t.Fatalf("满载时应返回 ErrBusy，得到 %v", err)
	}
}

func TestRunDrainDiscardsAndMarksInterrupted(t *testing.T) {
	s := openStore(t)
	if _, err := s.CreateUser(context.Background(), store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	q := New(8)
	sender := &fakeSender{}
	deps := Deps{Sender: sender, Store: s, Log: testLog()}

	var ids []int64
	for i := 0; i < 2; i++ {
		r, err := s.CreateRequest(context.Background(), store.Request{
			UserID: 1, ChannelKey: "example", MessageID: i + 1,
		})
		if err != nil {
			t.Fatalf("创建请求失败: %v", err)
		}
		ids = append(ids, r.ID)
		if err := q.Enqueue(NewJob(1, 1,
			tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: i + 1},
			10+i, r.ID)); err != nil {
			t.Fatalf("入队失败: %v", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		// processor 不应被调用（立即取消）；discard 负责收尾
		q.Run(ctx, 1, func(context.Context, Job) { t.Error("取消后不应再处理任务") }, Discard(deps))
		close(runDone)
	}()
	cancel()
	waitDone(t, runDone)

	// 两条任务都被丢弃：用户收到退出通知、状态提示被删、请求行置 INTERRUPTED
	texts := sender.texts()
	if len(texts) != 2 {
		t.Fatalf("两条任务均应收到退出通知，得到 %d", len(texts))
	}
	if ids2 := sender.deletedIDs(); len(ids2) != 2 {
		t.Errorf("两条状态提示均应删除，得到 %v", ids2)
	}
	for _, id := range ids {
		got, err := s.GetRequest(context.Background(), id)
		if err != nil {
			t.Fatalf("读取请求失败: %v", err)
		}
		if got.Status != store.RequestFailed || got.ErrorCode != "INTERRUPTED" {
			t.Errorf("丢弃任务应落 failed(INTERRUPTED): %+v", got)
		}
	}

	// 再跑一次启动恢复：行已终态，幂等无动作
	if n, err := s.FailInterruptedRequests(context.Background(), 0); err != nil || n != 0 {
		t.Errorf("drain 后启动恢复应为 0，得到 n=%d err=%v", n, err)
	}
}

func TestNewJobRequestAssociation(t *testing.T) {
	j := NewJob(7, 8, tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "c", MessageID: 3}, 9, 42)
	if j.RequestID != 42 || j.UserID != 7 || j.ChatID != 8 || j.StatusMsgID != 9 {
		t.Errorf("任务字段关联不对: %+v", j)
	}
	if _, err := strconv.ParseInt(j.ID, 10, 64); err != nil {
		t.Errorf("任务 ID 应保持纯数字纳秒时间戳格式（media 孤儿清理依赖）: %q", j.ID)
	}
}

// ---- 优先级队列：云盘任务低优先，hi 非空不取 lo ----

// newCloudJob 构造一个云盘任务（唯一入低优先级通道的形态）。
func newCloudJob(userID, chatID int64, requestID int64) Job {
	j := NewJob(userID, chatID, tmeurl.SourceRef{Kind: tmeurl.PeerUsername, Username: "example", MessageID: 1}, 0, requestID)
	j.CloudDest = "mega"
	return j
}

func TestEnqueueRoutesByPriority(t *testing.T) {
	q := New(1)
	// 两形态各占一通道：普通任务占 hi、云盘任务占 lo，互不挤占
	if err := q.Enqueue(NewJob(1, 1, tmeurl.SourceRef{}, 0, 0)); err != nil {
		t.Fatalf("普通任务应入高优先级通道: %v", err)
	}
	if err := q.Enqueue(newCloudJob(1, 1, 0)); err != nil {
		t.Fatalf("云盘任务应入低优先级通道: %v", err)
	}
	if q.FullFor(true) != true {
		t.Error("低优先级通道应报告已满")
	}
	if q.FullFor(false) != true {
		t.Error("高优先级通道也应报告已满（各占一通道）")
	}
	if q.Len() != 2 {
		t.Errorf("Len 应汇总双通道，得到 %d", q.Len())
	}
	// 各通道再入第二个任务：分别撞各自通道的容量上限
	if err := q.Enqueue(newCloudJob(1, 1, 0)); !errors.Is(err, ErrBusy) {
		t.Errorf("低优先级通道满应返回 ErrBusy，得到 %v", err)
	}
	if err := q.Enqueue(NewJob(2, 2, tmeurl.SourceRef{}, 0, 0)); !errors.Is(err, ErrBusy) {
		t.Errorf("高优先级通道满应返回 ErrBusy，得到 %v", err)
	}
}

func TestPriorityHiBeforeLo(t *testing.T) {
	q := New(8)
	// 交错入队：云盘、普通、云盘、普通
	want := []string{"lo", "hi", "lo", "hi"}
	var gotOrder []string
	for _, kind := range want {
		if kind == "lo" {
			if err := q.Enqueue(newCloudJob(1, 1, 0)); err != nil {
				t.Fatalf("入队失败: %v", err)
			}
			continue
		}
		if err := q.Enqueue(NewJob(1, 1, tmeurl.SourceRef{}, 0, 0)); err != nil {
			t.Fatalf("入队失败: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	process := func(_ context.Context, j Job) {
		mu.Lock()
		defer mu.Unlock()
		if j.CloudDest != "" {
			gotOrder = append(gotOrder, "lo")
		} else {
			gotOrder = append(gotOrder, "hi")
		}
	}
	done := make(chan struct{})
	go func() {
		q.Run(ctx, 1, process, func(context.Context, Job) {})
		close(done)
	}()
	for {
		mu.Lock()
		done_ := len(gotOrder) == 4
		mu.Unlock()
		if done_ {
			break
		}
		select {
		case <-done:
			t.Fatal("任务未处理完 Run 就退出了")
		case <-time.After(time.Second):
		}
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(gotOrder, []string{"hi", "hi", "lo", "lo"}) {
		t.Errorf("应先排空高优先级通道再取低优先级，得到 %v", gotOrder)
	}
}

func TestAcquireWakesOnBothChannels(t *testing.T) {
	q := New(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	acquired := make(chan Job, 1)
	go func() {
		job, ok := q.acquire(ctx)
		if ok {
			acquired <- job
		}
	}()
	// 双通道皆空时阻塞；先到 lo（此刻 hi 空，取 lo 合法），随后到 hi
	time.Sleep(50 * time.Millisecond)
	if err := q.Enqueue(newCloudJob(1, 1, 0)); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	select {
	case job := <-acquired:
		if job.CloudDest == "" {
			t.Error("先入队的 lo 任务应先被取到")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquire 应被 lo 入队唤醒")
	}
	// 第二次 acquire 取 hi 任务
	go func() {
		job, ok := q.acquire(ctx)
		if ok {
			acquired <- job
		}
	}()
	time.Sleep(50 * time.Millisecond)
	if err := q.Enqueue(NewJob(1, 1, tmeurl.SourceRef{}, 0, 0)); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	select {
	case job := <-acquired:
		if job.CloudDest != "" {
			t.Error("后入队的 hi 任务应被取到")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquire 应被 hi 入队唤醒")
	}
}

func TestCancelPendingLowPriorityJob(t *testing.T) {
	q := New(4)
	var pendingJobs []Job
	q.SetPendingCancelHandler(func(j Job) { pendingJobs = append(pendingJobs, j) })
	if err := q.Enqueue(newCloudJob(1, 1, 42)); err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if q.CancelRequest(42) {
		t.Error("排队任务未出队，应走 pending 回调而非活动取消")
	}
	if len(pendingJobs) != 1 || pendingJobs[0].RequestID != 42 {
		t.Fatalf("取消应命中排队中的低优先级任务: %+v", pendingJobs)
	}
	// 命中即从索引移除：重复取消落空
	if q.CancelRequest(42) {
		t.Error("已移除的 pending 任务不应再次命中")
	}
	if len(pendingJobs) != 1 {
		t.Error("重复取消不应再次触发回调")
	}
}
