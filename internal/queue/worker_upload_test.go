package queue

// worker 的"下载 → 上传"统一路径测试：
//   - 单媒体：打开句柄后经 Sender 发送（必带 reader）；
//   - 相册：整组打开句柄原子发送，含不可整组成员时降级逐条；
//   - 下载 file reference 过期：经 RefreshMedia 刷新后重试一次；
//   - 上传路径可成功的前提是 media.Open 能返回句柄：用返回错误的假 MTProto
//     invoker 构造 tg.Client，流式下载 goroutine 立即失败并关闭管道写端，
//     而假 Sender 默认不消费 reader，从而让"上传路径"在测试中可观察、可成功；
//     临时文件路径异步化（下载与上传重叠）后，其下载错误与流式/内存管道
//     同构地经 reader 传播——需要观察该语义的测试用 consumeMediaReaders /
//     consumeAlbumReaders 让假 Sender 模拟真实上传侧的消费行为。

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// mediaOptionsForTest 返回宽松的下载参数：大小不触发 FILE_TOO_LARGE，
// 媒体全部走流式路径（配合假 invoker 让上传路径在测试中可观察）。
func mediaOptionsForTest(t *testing.T) media.Options {
	t.Helper()
	return media.Options{TmpDir: t.TempDir(), MaxFileSize: 1 << 30, StreamLimit: 1 << 30}
}

// errInvoker 永远返回错误的假 MTProto invoker：下载立即失败并关闭管道写端，
// 假 Sender 不消费 reader，上传路径的任务仍可成功。
type errInvoker struct{}

func (errInvoker) Invoke(context.Context, bin.Encoder, bin.Decoder) error {
	return errors.New("测试假 invoker：不提供真实下载")
}

// expiredInvoker 永远返回 FILE_REFERENCE_EXPIRED 的假 MTProto invoker：
// 强制同步路径（临时文件）下 media.Open 以过期错误失败，验证刷新重试。
type expiredInvoker struct{}

func (expiredInvoker) Invoke(_ context.Context, _ bin.Encoder, _ bin.Decoder) error {
	return &tgerr.Error{Code: 400, Type: "FILE_REFERENCE_EXPIRED", Message: "FILE_REFERENCE_EXPIRED"}
}

// testInvoker 是假 MTProto invoker 的最小接口（tg.NewClient 的入参形态）。
type testInvoker interface {
	Invoke(context.Context, bin.Encoder, bin.Decoder) error
}

// fetcherWith 构造带假下载通道的取数假实现（上传路径需要非 nil API）。
func fetcherWith(invoker testInvoker, msgs ...*tg.Message) *fakeFetcher {
	return &fakeFetcher{msgs: msgs, api: tg.NewClient(invoker)}
}

// uploadDeps 构造统一上传路径的测试依赖。Sender 经 contractSender 包装：
// 执行 delivery 的"媒体必带数据源"契约，遗漏 reader 的调用形态直接失败。
func uploadDeps(t *testing.T, s *store.Store, fetcher *fakeFetcher, sender *fakeSender) Deps {
	t.Helper()
	return Deps{
		Fetcher: fetcher,
		Sender:  &contractSender{fakeSender: sender},
		// 宽松的下载参数：大小不走 FILE_TOO_LARGE 分支，媒体全部走流式路径
		Media: mediaOptionsForTest(t),
		Store: s,
		Log:   testLog(),
	}
}

// runJobSync 同步执行一个任务并断言成功状态落库。
func runJobSync(t *testing.T, d Deps, job Job) {
	t.Helper()
	Process(d)(context.Background(), job)
	r, err := d.Store.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestSucceeded {
		t.Fatalf("任务应成功，得到 %s（错误码 %s）", r.Status, r.ErrorCode)
	}
}

// docMsg 构造带视频文档的消息（ConvertOne 会生成下载 Location 与元数据）。
func docMsg(id int, accessHash int64) *tg.Message {
	return &tg.Message{
		ID: id, Message: "cap",
		Media: &tg.MessageMediaDocument{Document: &tg.Document{
			ID: 9000 + int64(id), AccessHash: accessHash, DCID: 2,
			FileReference: []byte{byte(accessHash)},
			MimeType:      "video/mp4",
			Size:          100,
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeVideo{W: 640, H: 480, Duration: 10},
				&tg.DocumentAttributeFilename{FileName: fmt.Sprintf("f-%d.bin", id)},
			},
		}},
	}
}

// runOneMedia 用单媒体消息跑任务（自动补一个带假下载通道的 fetcher）。
func runOneMedia(t *testing.T, d Deps, job Job, m *tg.Message) {
	t.Helper()
	if f, ok := d.Fetcher.(*fakeFetcher); ok && f != nil {
		f.msgs = []*tg.Message{m}
	} else {
		d.Fetcher = fetcherWith(errInvoker{}, m)
	}
	Process(d)(context.Background(), job)
}

// 单媒体统一走上传路径：SendMedia 必带 reader。
func TestWorkerUploadSingleMedia(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	sender := &fakeSender{}
	d := uploadDeps(t, s, nil, sender)

	runOneMedia(t, d, job, docMsg(7, 1101))

	calls := sender.mediaCallsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("应只有一次上传发送，得到 %+v", calls)
	}
	if !calls[0].HasReader || calls[0].Kind != message.KindVideo {
		t.Errorf("应走上传路径（reader 非空）且保留 Kind: %+v", calls[0])
	}
	wantCaption := "🔗 原消息\nhttps://t.me/example/7\n\ncap"
	if calls[0].Caption.Text != wantCaption {
		t.Errorf("单媒体 caption 应包含置顶来源链接\nwant: %q\ngot:  %q", wantCaption, calls[0].Caption.Text)
	}
	if len(calls[0].Caption.Entities) < 3 {
		t.Fatal("单媒体 caption 应包含来源卡片实体")
	}
	link, ok := calls[0].Caption.Entities[2].(*tg.MessageEntityTextURL)
	if !ok || link.URL != "https://t.me/example/7" || link.Offset <= 0 {
		t.Fatalf("单媒体来源链接实体不符: %#v", calls[0].Caption.Entities[2])
	}
}

func TestWorkerUploadSingleMediaEmptyCaption(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	sender := &fakeSender{}
	d := uploadDeps(t, s, nil, sender)
	msg := docMsg(7, 1105)
	msg.Message = ""

	runOneMedia(t, d, job, msg)

	calls := sender.mediaCallsSnapshot()
	if len(calls) != 1 || calls[0].Caption.Text != "🔗 原消息\nhttps://t.me/example/7" || len(calls[0].Caption.Entities) != 3 {
		t.Fatalf("空 caption 媒体仍应显示来源链接: %+v", calls)
	}
}

// 上传发送失败：任务失败并携带媒体元数据。
func TestWorkerUploadSendFailure(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	sender := &fakeSender{mediaErr: func(mediaCall) error {
		return apperr.New(apperr.CodeSendFailed, "send boom")
	}}
	d := uploadDeps(t, s, fetcherWith(errInvoker{}), sender)

	runOneMedia(t, d, job, docMsg(7, 1102))

	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.ErrorCode != "BOT_SEND_FAILED" {
		t.Fatalf("应落 failed(BOT_SEND_FAILED): %+v", r)
	}
	if r.FileSize != 100 {
		t.Errorf("转换后的失败应保留媒体大小，得到 %d", r.FileSize)
	}
}

// 下载 file reference 过期（临时文件路径异步化后经 reader 在发送阶段传播）：
// 刷新一次后重试，重试仍过期时以原错误失败。
func TestWorkerUploadRefreshesExpiredReference(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	// consumeMediaReaders 模拟真实上传侧消费：下载错误经 reader 传出，
	// sendMediaItem 据此触发 FILE_REFERENCE_EXPIRED 刷新重试
	sender := &fakeSender{consumeMediaReaders: true}
	d := uploadDeps(t, s, nil, sender)
	d.Media = media.Options{TmpDir: t.TempDir(), MaxFileSize: 1 << 30}
	fetcher := fetcherWith(expiredInvoker{}, docMsg(7, 1103))
	fetcher.refresh = &refreshResult{media: message.Media{
		Kind:     message.KindVideo,
		Location: &tg.InputDocumentFileLocation{ID: 9107, AccessHash: 1103},
		Size:     100,
	}, ok: true}
	d.Fetcher = fetcher

	runOneMedia(t, d, job, docMsg(7, 1103))

	if fetcher.refreshCalls != 1 {
		t.Fatalf("过期错误应触发恰好一次刷新重试，得到 %d", fetcher.refreshCalls)
	}
	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.ErrorCode != "MEDIA_DOWNLOAD_FAILED" {
		t.Fatalf("重试仍过期应落 failed(MEDIA_DOWNLOAD_FAILED): %+v", r)
	}
	// 两次发送尝试（初次 + 刷新重试）均发起但都因读源失败，无一成功送达
	calls := sender.mediaCallsSnapshot()
	if len(calls) != 2 {
		t.Fatalf("应有一次初次发送与一次刷新重试，得到 %d 次", len(calls))
	}
	for i, c := range calls {
		if !c.HasReader || c.ReadErr == nil {
			t.Errorf("第 %d 次发送应带 reader 且以读源错误失败: %+v", i, c)
		}
	}
}

// 相册：全员可整组 → 一次 SendAlbum，成员全部带 reader。
func TestWorkerUploadAlbumGroup(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	msgs := []*tg.Message{docMsg(7, 1201), docMsg(8, 1202)}
	msgs[0].SetGroupedID(42)
	msgs[1].SetGroupedID(42)
	sender := &fakeSender{groupable: func(message.Media) bool { return true }}
	d := uploadDeps(t, s, fetcherWith(errInvoker{}, msgs...), sender)

	runJobSync(t, d, job)

	calls := sender.albumCallsSnapshot()
	if len(calls) != 1 || len(calls[0].Kinds) != 2 {
		t.Fatalf("应整组发送一次且含两个成员: %+v", calls)
	}
	if got := calls[0].Captions[0].Text; got != "🔗 原消息\nhttps://t.me/example/7\n\ncap" {
		t.Errorf("相册首项应包含置顶来源链接: %q", got)
	}
	if len(calls[0].Captions[0].Entities) < 3 {
		t.Fatal("相册首项应包含来源卡片实体")
	}
	if got := calls[0].Captions[1].Text; got != "cap" {
		t.Errorf("相册后续成员不应重复来源链接文本: %+v", calls[0].Captions[1])
	}
	// 后续成员只带正文引用块样式，不得携带来源卡片实体
	for _, e := range calls[0].Captions[1].Entities {
		if _, isLink := e.(*tg.MessageEntityTextURL); isLink {
			t.Errorf("相册后续成员不应包含来源链接实体: %+v", calls[0].Captions[1])
		}
	}
	if singles := sender.mediaCallsSnapshot(); len(singles) != 0 {
		t.Errorf("整组成功不应逐条发送: %+v", singles)
	}
}

// 相册成员超过进程内存预算 → 混合路径：拿到预算的成员走内存管道，其余
// 自动降级临时文件路径，整组仍原子发送一次；任务收尾后预算全部归还。
func TestWorkerUploadAlbumMixedPathsWithBudget(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	msgs := []*tg.Message{docMsg(7, 1301), docMsg(8, 1302), docMsg(9, 1303)}
	for _, m := range msgs {
		m.SetGroupedID(42)
		// 1MB：落在下方配置的内存管道区间（64KB–2MB）内
		m.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).Size = 1 << 20
	}
	sender := &fakeSender{groupable: func(message.Media) bool { return true }}
	d := uploadDeps(t, s, fetcherWith(errInvoker{}, msgs...), sender)
	d.Media.StreamLimit = 64 << 10
	d.Media.MemoryLimit = 2 << 20
	d.Media.DownloadThreads = 2
	g := media.NewBudgetGate(func() int64 { return 2 << 20 }) // 预算只够 2 个成员
	d.Media.Memory = g

	runJobSync(t, d, job)

	calls := sender.albumCallsSnapshot()
	if len(calls) != 1 || len(calls[0].Kinds) != 3 {
		t.Fatalf("预算不足应混合路径整组发送一次且含三个成员: %+v", calls)
	}
	if singles := sender.mediaCallsSnapshot(); len(singles) != 0 {
		t.Errorf("混合路径不应触发逐条发送: %+v", singles)
	}
	if g.Held() != 0 {
		t.Fatalf("任务收尾后预算应全部归还，得到 %d", g.Held())
	}
}

// 相册含不可整组成员 → 降级逐条发送。
func TestWorkerUploadAlbumDegradesIndividually(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	msgs := []*tg.Message{docMsg(7, 1203), docMsg(8, 1204)}
	msgs[0].SetGroupedID(42)
	msgs[1].SetGroupedID(42)
	sender := &fakeSender{groupable: func(message.Media) bool { return false }}
	d := uploadDeps(t, s, fetcherWith(errInvoker{}, msgs...), sender)

	runJobSync(t, d, job)

	if calls := sender.albumCallsSnapshot(); len(calls) != 0 {
		t.Fatalf("不可整组不应整组发送: %+v", calls)
	}
	singles := sender.mediaCallsSnapshot()
	if len(singles) != 2 {
		t.Fatalf("应逐条发送两次: %+v", singles)
	}
	for _, c := range singles {
		if !c.HasReader {
			t.Errorf("逐条发送也必须带 reader: %+v", c)
		}
	}
	if singles[0].Caption.Text != "🔗 原消息\nhttps://t.me/example/7\n\ncap" || len(singles[0].Caption.Entities) < 3 {
		t.Errorf("降级逐条时首项应包含来源链接: %+v", singles[0].Caption)
	}
	if singles[1].Caption.Text != "cap" {
		t.Errorf("降级逐条时后续项不应重复来源链接文本: %+v", singles[1].Caption)
	}
	// 后续项只带正文引用块样式，不得携带来源卡片实体
	for _, e := range singles[1].Caption.Entities {
		if _, isLink := e.(*tg.MessageEntityTextURL); isLink {
			t.Errorf("降级逐条时后续项不应包含来源链接实体: %+v", singles[1].Caption)
		}
	}
}

// 相册成员下载失败（临时文件路径异步化后经 reader 在发送消费阶段传播）：
// 整组发送因成员读源失败而失败，不回退逐条发送。
func TestWorkerUploadAlbumDownloadFailure(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	msgs := []*tg.Message{docMsg(7, 1205), docMsg(8, 1206)}
	msgs[0].SetGroupedID(42)
	msgs[1].SetGroupedID(42)
	sender := &fakeSender{
		groupable:           func(message.Media) bool { return true },
		consumeAlbumReaders: true, // 模拟真实上传侧：读源失败即整组失败
	}
	d := uploadDeps(t, s, nil, sender)
	d.Media = media.Options{TmpDir: t.TempDir(), MaxFileSize: 1 << 30}
	d.Fetcher = fetcherWith(errInvoker{}, msgs...)

	Process(d)(context.Background(), job)

	// 打开阶段不再同步失败（下载与上传重叠）：整组发送发起一次但以成员读源
	// 错误失败；失败后不回退逐条
	calls := sender.albumCallsSnapshot()
	if len(calls) != 1 || len(calls[0].ReadErrs) != 2 || calls[0].ReadErrs[0] == nil {
		t.Fatalf("应整组发送一次且首成员读源失败: %+v", calls)
	}
	if singles := sender.mediaCallsSnapshot(); len(singles) != 0 {
		t.Fatalf("整组失败不应回退逐条发送: %+v", singles)
	}
	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestFailed || r.ErrorCode != "MEDIA_DOWNLOAD_FAILED" {
		t.Fatalf("应落 failed(MEDIA_DOWNLOAD_FAILED): %+v", r)
	}
}

// ---- 回归：errgroup ctx 不能承载相册后台下载（真机 2026-09-03） ----
//
// errgroup.Wait() 在全员成功返回时也会取消其 ctx；流式/内存管道成员的下载
// 在句柄打开后仍在后台进行，若沿用 gctx，整组打开完成的一刻后台下载即被
// 取消，上传读源时以 MEDIA_DOWNLOAD_FAILED(context canceled) 失败。
// 用真实字节的假 invoker + 读尽 Reader 的假 Sender 锁定该行为。

// chunkInvoker 把 UploadGetFile 的 Offset/Limit 映射到预置 payload，
// 使流式/并行下载在测试中真实执行（越过文件末尾返回空字节即结束信号）。
type chunkInvoker struct{ payload []byte }

func (c chunkInvoker) Invoke(_ context.Context, in bin.Encoder, out bin.Decoder) error {
	req, ok := in.(*tg.UploadGetFileRequest)
	if !ok {
		return fmt.Errorf("chunkInvoker: 意外的请求类型 %T", in)
	}
	if req.Offset < 0 {
		return errors.New("chunkInvoker: 负偏移读取")
	}
	if req.Offset >= int64(len(c.payload)) {
		return encodeUploadFile(out, nil)
	}
	end := min(req.Offset+int64(req.Limit), int64(len(c.payload)))
	return encodeUploadFile(out, c.payload[req.Offset:end])
}

// encodeUploadFile 把一个 upload.file 响应编码进 out（假 invoker 的应答通道）。
func encodeUploadFile(out bin.Decoder, data []byte) error {
	file := &tg.UploadFile{Type: &tg.StorageFilePartial{}, Bytes: data}
	b := new(bin.Buffer)
	if err := file.Encode(b); err != nil {
		return err
	}
	return out.Decode(b)
}

// docMsgSized 同 docMsg，但允许指定文档大小（配合 chunkInvoker 提供真实字节）。
func docMsgSized(id int, accessHash int64, size int64) *tg.Message {
	m := docMsg(id, accessHash)
	if d, ok := m.Media.(*tg.MessageMediaDocument); ok {
		d.Document.(*tg.Document).Size = size
	}
	return m
}

func TestWorkerUploadAlbumStreamsSurviveGroupOpen(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 0)
	payload := make([]byte, 3<<20) // 3MB：多分片，跨多次下载 RPC
	for i := range payload {
		payload[i] = byte(i)
	}
	msgs := []*tg.Message{
		docMsgSized(7, 1301, int64(len(payload))),
		docMsgSized(8, 1302, int64(len(payload))),
	}
	msgs[0].SetGroupedID(44)
	msgs[1].SetGroupedID(44)
	sender := &fakeSender{
		groupable:           func(message.Media) bool { return true },
		consumeAlbumReaders: true, // 模拟上传侧：逐成员读尽 Reader
	}
	d := uploadDeps(t, s, fetcherWith(chunkInvoker{payload: payload}, msgs...), sender)

	runJobSync(t, d, job)

	calls := sender.albumCallsSnapshot()
	if len(calls) != 1 || len(calls[0].Kinds) != 2 {
		t.Fatalf("应整组发送一次且含两个成员: %+v", calls)
	}
	for m, err := range calls[0].ReadErrs {
		if err != nil {
			t.Fatalf("成员 %d 读源不应失败（下载必须活过整组打开阶段）: %v", m, err)
		}
	}
	for m, n := range calls[0].ReadLens {
		if n != len(payload) {
			t.Errorf("成员 %d 应读得完整字节: got %d want %d", m, n, len(payload))
		}
	}
}
