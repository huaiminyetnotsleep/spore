package queue

// 云盘任务（runCloudJob）测试：fake CloudSink/CloudCfg 驱动，
// 覆盖成功（平铺/相册成夹+caption.txt）、部分失败（已成功记录保留）、
// 取消（占位改取消文案）、纯文本拒绝、依赖缺失防御与临时文件句柄清理。

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// ---- 假实现 ----

type cloudCall struct {
	Dest    cloudarchive.Destination
	Spec    cloudarchive.UploadSpec
	Content []byte // readReaders 时读尽的字节（rcat 路径）
}

// fakeCloudSink 记录每次上传；err 按调用序注入错误；block 阻塞到 ctx
// 取消后返回 ctx.Err()（模拟 rclone 被杀）；verify 注入已上传核验结果
// （nil 时固定 (false, nil)=未上传过，走正常上传）。
type fakeCloudSink struct {
	mu          sync.Mutex
	calls       []cloudCall
	err         func(i int, c cloudCall) error
	block       bool
	readReaders bool // 读尽 Reader（模拟 rclone 消费 stdin）
	pingErr     error
	verify      func(paths []string) (bool, error)
}

func (f *fakeCloudSink) Upload(ctx context.Context, dest cloudarchive.Destination, spec cloudarchive.UploadSpec) error {
	if f.block {
		<-ctx.Done()
		return ctx.Err()
	}
	call := cloudCall{Dest: dest, Spec: spec}
	if f.readReaders && spec.Reader != nil {
		call.Content, _ = io.ReadAll(spec.Reader)
	}
	if f.readReaders && spec.FilePath != "" { // copyto 路径：上传时读取文件内容
		call.Content, _ = os.ReadFile(spec.FilePath)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	idx := len(f.calls) - 1
	inject := f.err
	f.mu.Unlock()
	if inject != nil {
		return inject(idx, call)
	}
	return nil
}

func (f *fakeCloudSink) Ping(context.Context, cloudarchive.Destination) error { return f.pingErr }

func (f *fakeCloudSink) VerifyUploaded(_ context.Context, _ cloudarchive.Destination, paths []string) (bool, error) {
	if f.verify == nil {
		return false, nil
	}
	return f.verify(paths)
}

func (f *fakeCloudSink) snapshot() []cloudCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cloudCall(nil), f.calls...)
}

// fakeCloudCfg 固定返回一个可用目的地。
type fakeCloudCfg struct {
	dest cloudarchive.Destination
	ok   bool
}

func (c fakeCloudCfg) ResolveCloudDestination(name string) (cloudarchive.Destination, bool) {
	if !c.ok || name != c.dest.Name {
		return cloudarchive.Destination{}, false
	}
	return c.dest, true
}

func cloudTestDest() cloudarchive.Destination {
	return cloudarchive.Destination{Name: "mega-1", Type: "mega", Enabled: true, PathPrefix: "spore"}
}

// cloudDeps 构造云盘任务的测试依赖（默认流式下载参数 + errInvoker 假通道）。
func cloudDeps(t *testing.T, s *store.Store, fetcher *fakeFetcher, sink *fakeCloudSink) Deps {
	t.Helper()
	return Deps{
		Fetcher:   fetcher,
		Sender:    &fakeSender{},
		Media:     mediaOptionsForTest(t),
		Store:     s,
		Log:       testLog(),
		CloudSink: sink,
		CloudCfg:  fakeCloudCfg{dest: cloudTestDest(), ok: true},
	}
}

func cloudJob(t *testing.T, s *store.Store) Job {
	t.Helper()
	job, _ := newJobWithRequest(t, s, 55)
	job.CloudDest = "mega-1"
	return job
}

// cloudMsgDate 固定源消息日期，路径断言可预期。
var cloudMsgDate = time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)

// cloudDocMsg 构造带文件名视频文档的云盘测试消息。
func cloudDocMsg(id int, caption string) *tg.Message {
	m := docMsg(id, int64(9000+id))
	m.Message = caption
	m.Date = int(cloudMsgDate.Unix())
	return m
}

// cloudDocMsgSized 同上，但文档大小可指定（配合 chunkInvoker 提供真实字节）。
func cloudDocMsgSized(id int, caption string, size int64) *tg.Message {
	m := cloudDocMsg(id, caption)
	if d, ok := m.Media.(*tg.MessageMediaDocument); ok {
		d.Document.(*tg.Document).Size = size
	}
	return m
}

// ---- 场景 ----

// 单媒体无配文：平铺路径、内存句柄（Reader+Size）、确认文本与终态落库。
func TestCloudJobSingleMedia(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	sink := &fakeCloudSink{readReaders: true}
	payload := []byte("0123456789")
	d := cloudDeps(t, s, fetcherWith(chunkInvoker{payload: payload},
		cloudDocMsgSized(7, "", int64(len(payload)))), sink)

	Process(d)(context.Background(), job)

	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestSucceeded || r.DeliveryMode != store.DeliveryModeCloud {
		t.Fatalf("应成功且 delivery_mode=cloud: %+v", r)
	}
	calls := sink.snapshot()
	if len(calls) != 1 {
		t.Fatalf("应恰好一次上传，得到 %d", len(calls))
	}
	spec := calls[0].Spec
	if spec.RemotePath != "spore/example/2026-09-09/f-7.bin" || spec.FilePath != "" {
		t.Fatalf("平铺路径与内存句柄不符: %+v", spec)
	}
	if spec.Size != int64(len(payload)) || string(calls[0].Content) != string(payload) {
		t.Fatalf("句柄内容/大小不符: size=%d content=%q", spec.Size, calls[0].Content)
	}
	ups, _ := s.CloudUploadsByRequest(context.Background(), job.RequestID)
	if len(ups) != 1 || ups[0].Status != store.CloudUploadSucceeded || ups[0].Bytes != int64(len(payload)) ||
		ups[0].Destination != "mega-1" || ups[0].RemotePath != spec.RemotePath {
		t.Fatalf("上传记录不符: %+v", ups)
	}
	sender := d.Sender.(*fakeSender)
	texts := sender.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "已上传到网盘 <b>mega-1</b>") ||
		!strings.Contains(texts[0], "spore/example/2026-09-09/f-7.bin") {
		t.Fatalf("确认文本不符: %v", texts)
	}
	if ids := sender.deletedIDs(); len(ids) != 1 || ids[0] != 55 {
		t.Errorf("成功后应删除占位: %v", ids)
	}
}

// 相册带配文：成夹布局、序号前缀、caption.txt（成员 caption 依序拼接），
// 确认文本合并为一行目录。
func TestCloudJobAlbumWithCaption(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	msgs := []*tg.Message{cloudDocMsg(7, "part-1"), cloudDocMsg(8, "part-2")}
	msgs[0].SetGroupedID(42)
	msgs[1].SetGroupedID(42)
	sink := &fakeCloudSink{readReaders: true}
	d := cloudDeps(t, s, fetcherWith(errInvoker{}, msgs...), sink)

	Process(d)(context.Background(), job)

	r, _ := s.GetRequest(context.Background(), job.RequestID)
	if r.Status != store.RequestSucceeded || r.MediaType != "album" {
		t.Fatalf("相册任务应成功: %+v", r)
	}
	calls := sink.snapshot()
	if len(calls) != 3 {
		t.Fatalf("应上传两个媒体 + caption.txt，得到 %d", len(calls))
	}
	dir := "spore/example/2026-09-09/7"
	if calls[0].Spec.RemotePath != dir+"/01_f-7.bin" || calls[1].Spec.RemotePath != dir+"/02_f-8.bin" {
		t.Fatalf("媒体序号前缀不符: %s / %s", calls[0].Spec.RemotePath, calls[1].Spec.RemotePath)
	}
	if calls[2].Spec.RemotePath != dir+"/caption.txt" || calls[2].Spec.FilePath != "" {
		t.Fatalf("caption 路径不符: %+v", calls[2].Spec)
	}
	if got := string(calls[2].Content); got != "part-1\n\npart-2" {
		t.Fatalf("caption 内容应为成员拼接: %q", got)
	}
	// 确认文本：目录一行，不逐文件罗列
	sender := d.Sender.(*fakeSender)
	texts := sender.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "已上传到网盘 <b>mega-1</b>") || !strings.Contains(texts[0], dir) {
		t.Fatalf("确认文本应含目录一行: %v", texts)
	}
	if strings.Contains(texts[0], "01_f-7.bin") {
		t.Fatalf("成夹布局不应逐文件罗列: %q", texts[0])
	}
	ups, _ := s.CloudUploadsByRequest(context.Background(), job.RequestID)
	if len(ups) != 3 {
		t.Fatalf("应有三条上传记录: %+v", ups)
	}
	for _, u := range ups {
		if u.Status != store.CloudUploadSucceeded {
			t.Fatalf("全部应成功: %+v", u)
		}
	}
}

// seedPriorCloudUpload 种子一条同用户同链接同目的地的历史成功云盘请求及其
// 成功上传行（成夹布局传多个路径）。
func seedPriorCloudUpload(t *testing.T, s *store.Store, j Job, remotePaths ...string) {
	t.Helper()
	ctx := context.Background()
	prior, err := s.CreateRequest(ctx, store.Request{
		UserID: j.UserID, SourceKind: store.SourcePublic, ChannelKey: "example",
		MessageID: j.Ref.MessageID, DeliveryMode: store.DeliveryModeCloud,
		CloudDestination: j.CloudDest,
	})
	if err != nil {
		t.Fatalf("种子历史云盘请求失败: %v", err)
	}
	_ = s.MarkRequestStarted(ctx, prior.ID, 1)
	if err := s.FinishRequest(ctx, prior.ID, store.RequestResult{
		Status: store.RequestSucceeded, DeliveryMode: store.DeliveryModeCloud,
		MediaType: "video", MediaTypes: []string{"video"}, FileName: "f-7.bin", FileSize: 9007,
	}); err != nil {
		t.Fatalf("种子历史终态失败: %v", err)
	}
	for _, p := range remotePaths {
		row, err := s.InsertCloudUpload(ctx, store.CloudUpload{
			RequestID: prior.ID, Destination: j.CloudDest, RemotePath: p,
			FileName: p[strings.LastIndexByte(p, '/')+1:],
		})
		if err != nil {
			t.Fatalf("种子历史上传行失败: %v", err)
		}
		if err := s.FinishCloudUpload(ctx, row.ID, store.CloudUploadSucceeded, "", "", 100, 0); err != nil {
			t.Fatalf("种子历史上传终态失败: %v", err)
		}
	}
}

// 同链接同目的地已上传且远端核验通过：跳过 fetch/下载/上传（零次 Upload、
// 不新增上传记录），复用历史元数据，请求直接成功；确认文本含历史路径
// （成夹折叠为目录一行）、未重复下载说明、原链接与网盘官网。
func TestCloudJobSkipAlreadyUploaded(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	dir := "spore/example/2026-09-09/7"
	seedPriorCloudUpload(t, s, job,
		dir+"/01_f-7.bin", dir+"/02_f-8.bin", dir+"/caption.txt")

	var verifiedPaths []string
	sink := &fakeCloudSink{verify: func(paths []string) (bool, error) {
		verifiedPaths = paths
		return true, nil
	}}
	// errInvoker 的媒体通道打开必败：若跳过失效走了正常流程，任务会失败
	d := cloudDeps(t, s, fetcherWith(errInvoker{}, cloudDocMsg(7, "")), sink)

	Process(d)(context.Background(), job)

	if len(verifiedPaths) != 3 {
		t.Fatalf("应核验三条历史路径，得到 %v", verifiedPaths)
	}
	if calls := sink.snapshot(); len(calls) != 0 {
		t.Fatalf("核验通过不应再上传，得到 %d 次", len(calls))
	}
	r, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if r.Status != store.RequestSucceeded || r.DeliveryMode != store.DeliveryModeCloud {
		t.Fatalf("跳过路径应直接成功且 delivery_mode=cloud: %+v", r)
	}
	if r.FileSize != 9007 || r.MediaType != "video" || r.FileName != "f-7.bin" {
		t.Fatalf("应复用历史请求元数据: %+v", r)
	}
	if ups, _ := s.CloudUploadsByRequest(context.Background(), job.RequestID); len(ups) != 0 {
		t.Fatalf("跳过路径不应新增上传记录: %+v", ups)
	}
	sender := d.Sender.(*fakeSender)
	texts := sender.texts()
	if len(texts) != 1 {
		t.Fatalf("应恰好一条确认文本: %v", texts)
	}
	text := texts[0]
	for _, want := range []string{
		"已上传到网盘 <b>mega-1</b>",
		dir,
		"该链接此前已上传，本次未重复下载",
		"<b>🔗 原消息</b>\n<a href=\"https://t.me/example/7\">https://t.me/example/7</a>",
		"🌐 网盘官网：<a href=\"https://mega.nz/\">https://mega.nz/</a>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("确认文本应包含 %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "01_f-7.bin") {
		t.Errorf("成夹复用应折叠为目录一行:\n%s", text)
	}
	if ids := sender.deletedIDs(); len(ids) != 1 || ids[0] != 55 {
		t.Errorf("成功后应删除占位: %v", ids)
	}
}

// 历史记录存在但远端文件已删除（核验返回未上传）：走正常下载上传重传。
func TestCloudJobReuploadWhenMissing(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	seedPriorCloudUpload(t, s, job, "spore/example/2026-09-09/f-7.bin")
	sink := &fakeCloudSink{readReaders: true, verify: func([]string) (bool, error) {
		return false, nil
	}}
	payload := []byte("0123456789")
	d := cloudDeps(t, s, fetcherWith(chunkInvoker{payload: payload},
		cloudDocMsgSized(7, "", int64(len(payload)))), sink)

	Process(d)(context.Background(), job)

	if calls := sink.snapshot(); len(calls) != 1 {
		t.Fatalf("文件缺失应重新上传一次，得到 %d 次", len(calls))
	}
	r, _ := s.GetRequest(context.Background(), job.RequestID)
	if r.Status != store.RequestSucceeded {
		t.Fatalf("重传后应成功: %+v", r)
	}
	sender := d.Sender.(*fakeSender)
	texts := sender.texts()
	if len(texts) != 1 || !strings.Contains(texts[0], "spore/example/2026-09-09/f-7.bin") {
		t.Fatalf("确认文本应含新上传路径: %v", texts)
	}
	if strings.Contains(texts[0], "该链接此前已上传") {
		t.Errorf("重传路径不应带未重复下载说明: %q", texts[0])
	}
}

// 核验本身失败（网络/限流）：任务失败 CLOUD_VERIFY_FAILED 并回复对应文案，
// 既不误报成功也不盲目重传。
func TestCloudJobVerifyUnavailable(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	seedPriorCloudUpload(t, s, job, "spore/example/2026-09-09/f-7.bin")
	sink := &fakeCloudSink{verify: func([]string) (bool, error) {
		return false, apperr.New(apperr.CodeCloudNetwork, "dial tcp timeout")
	}}
	d := cloudDeps(t, s, fetcherWith(errInvoker{}, cloudDocMsg(7, "")), sink)

	Process(d)(context.Background(), job)

	if calls := sink.snapshot(); len(calls) != 0 {
		t.Fatalf("核验失败不得重传，得到 %d 次上传", len(calls))
	}
	r, _ := s.GetRequest(context.Background(), job.RequestID)
	if r.Status != store.RequestFailed || r.ErrorCode != string(apperr.CodeCloudVerifyFailed) ||
		r.DeliveryMode != store.DeliveryModeCloud {
		t.Fatalf("应失败 CLOUD_VERIFY_FAILED 且保持 cloud 标记: %+v", r)
	}
	sender := d.Sender.(*fakeSender)
	texts := sender.texts()
	if len(texts) != 1 || texts[0] != failureNoticeHTML(apperr.CodeCloudVerifyFailed, job.Ref) {
		t.Fatalf("应回复核验不可用文案（含来源链接）: %v", texts)
	}
}

// 部分失败：整体 failed，已成功文件记录保留，失败行带错误码，无确认文本。
func TestCloudJobPartialFailure(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	msgs := []*tg.Message{cloudDocMsg(7, ""), cloudDocMsg(8, "")}
	msgs[0].SetGroupedID(42)
	msgs[1].SetGroupedID(42)
	sink := &fakeCloudSink{err: func(i int, _ cloudCall) error {
		if i == 1 {
			return apperr.New(apperr.CodeCloudQuota, "quota boom")
		}
		return nil
	}}
	d := cloudDeps(t, s, fetcherWith(errInvoker{}, msgs...), sink)

	Process(d)(context.Background(), job)

	r, _ := s.GetRequest(context.Background(), job.RequestID)
	if r.Status != store.RequestFailed || r.ErrorCode != "CLOUD_QUOTA" || r.DeliveryMode != store.DeliveryModeCloud {
		t.Fatalf("应失败 CLOUD_QUOTA 且保持 cloud 标记: %+v", r)
	}
	ups, _ := s.CloudUploadsByRequest(context.Background(), job.RequestID)
	if len(ups) != 2 {
		t.Fatalf("失败前应有两条记录: %+v", ups)
	}
	if ups[0].Status != store.CloudUploadSucceeded || ups[0].Bytes != 100 {
		t.Fatalf("首个文件应保留成功记录: %+v", ups[0])
	}
	if ups[1].Status != store.CloudUploadFailed || ups[1].ErrorCode != "CLOUD_QUOTA" {
		t.Fatalf("第二个文件应记失败与错误码: %+v", ups[1])
	}
	sender := d.Sender.(*fakeSender)
	texts := sender.texts()
	if len(texts) != 1 || texts[0] != failureNoticeHTML(apperr.CodeCloudQuota, job.Ref) {
		t.Fatalf("失败应回复用户文案（含来源链接）: %v", texts)
	}
}

// 云盘上传超时：请求级错误明确归类为 CLOUD_UPLOAD_TIMEOUT；上传中的文件
// 保留 uploading 现场，避免把上下文中断误记为普通文件失败。
func TestCloudJobUploadTimeout(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	sink := &fakeCloudSink{err: func(int, cloudCall) error {
		return context.DeadlineExceeded
	}}
	d := cloudDeps(t, s, fetcherWith(errInvoker{}, cloudDocMsg(7, "")), sink)

	Process(d)(context.Background(), job)

	r, _ := s.GetRequest(context.Background(), job.RequestID)
	if r.Status != store.RequestFailed || r.ErrorCode != string(apperr.CodeCloudUploadTimeout) ||
		r.DeliveryMode != store.DeliveryModeCloud {
		t.Fatalf("应失败 CLOUD_UPLOAD_TIMEOUT 且保持 cloud 标记: %+v", r)
	}
	ups, _ := s.CloudUploadsByRequest(context.Background(), job.RequestID)
	if len(ups) != 1 || ups[0].Status != store.CloudUploadUploading {
		t.Fatalf("超时后应保留 uploading 现场: %+v", ups)
	}
	sender := d.Sender.(*fakeSender)
	texts := sender.texts()
	if len(texts) != 1 || texts[0] != failureNoticeHTML(apperr.CodeCloudUploadTimeout, job.Ref) {
		t.Fatalf("应回复云盘上传超时文案（含来源链接）: %v", texts)
	}
	if strings.Contains(strings.Join(texts, "\n"), "已上传到网盘") {
		t.Fatalf("失败不应发送上传成功确认: %v", texts)
	}
}

// 纯文本消息：CLOUD_TEXT_ONLY 失败，无上传记录。
func TestCloudJobTextOnly(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	sink := &fakeCloudSink{}
	d := cloudDeps(t, s, &fakeFetcher{msgs: []*tg.Message{{
		ID: 7, Message: "hello", Date: int(cloudMsgDate.Unix()),
	}}}, sink)

	Process(d)(context.Background(), job)

	r, _ := s.GetRequest(context.Background(), job.RequestID)
	if r.Status != store.RequestFailed || r.ErrorCode != "CLOUD_TEXT_ONLY" {
		t.Fatalf("纯文本应失败 CLOUD_TEXT_ONLY: %+v", r)
	}
	if ups, _ := s.CloudUploadsByRequest(context.Background(), job.RequestID); len(ups) != 0 {
		t.Fatalf("纯文本不应有上传记录: %+v", ups)
	}
	if calls := sink.snapshot(); len(calls) != 0 {
		t.Fatalf("不应调用上传: %+v", calls)
	}
	sender := d.Sender.(*fakeSender)
	if texts := sender.texts(); len(texts) != 1 || texts[0] != failureNoticeHTML(apperr.CodeCloudTextOnly, job.Ref) {
		t.Fatalf("应回复纯文本不支持文案（含来源链接）: %v", texts)
	}
}

// 取消：上传中取消 → 子进程终止语义（sink 返回 ctx.Err()），
// 占位改为取消文案，请求不被自然失败覆盖，uploading 行保留中断现场。
func TestCloudJobCancelled(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	sink := &fakeCloudSink{block: true}
	d := cloudDeps(t, s, fetcherWith(errInvoker{}, cloudDocMsg(7, "")), sink)

	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	go func() { Process(d)(ctx, job); close(done) }()
	time.Sleep(50 * time.Millisecond) // 让任务进入上传阻塞
	cancel(ErrRequestCancelled)
	waitDone(t, done)

	sender := d.Sender.(*fakeSender)
	if len(sender.edits) == 0 || !strings.Contains(sender.edits[len(sender.edits)-1], StatusCancelledHTML) {
		t.Fatalf("取消后占位应改为取消文案: %v", sender.edits)
	}
	r, _ := s.GetRequest(context.Background(), job.RequestID)
	if r.Status == store.RequestFailed {
		t.Fatalf("取消不应被覆盖为自然失败: %+v", r)
	}
	ups, _ := s.CloudUploadsByRequest(context.Background(), job.RequestID)
	if len(ups) != 1 || ups[0].Status != store.CloudUploadUploading {
		t.Fatalf("取消后应保留 uploading 现场: %+v", ups)
	}
}

// 依赖防御：CloudSink 缺失或目的地解析失败 → 明确失败，不 panic。
func TestCloudJobMissingDeps(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	msg := cloudDocMsg(7, "")

	d := cloudDeps(t, s, fetcherWith(errInvoker{}, msg), &fakeCloudSink{})
	d.CloudSink = nil // 模拟装配遗漏：接口本身为 nil
	Process(d)(context.Background(), job)
	r, _ := s.GetRequest(context.Background(), job.RequestID)
	if r.Status != store.RequestFailed || r.ErrorCode != "CLOUD_UPLOAD_FAILED" {
		t.Fatalf("缺 CloudSink 应失败 CLOUD_UPLOAD_FAILED: %+v", r)
	}

	// 目的地不可用（排队后被删除/停用）
	s2 := openStore(t)
	job2 := cloudJob(t, s2)
	d2 := cloudDeps(t, s2, fetcherWith(errInvoker{}, msg), &fakeCloudSink{})
	d2.CloudCfg = fakeCloudCfg{dest: cloudTestDest(), ok: false}
	Process(d2)(context.Background(), job2)
	r2, _ := s2.GetRequest(context.Background(), job2.RequestID)
	if r2.Status != store.RequestFailed || r2.ErrorCode != "CLOUD_UPLOAD_FAILED" {
		t.Fatalf("目的地不可用应失败 CLOUD_UPLOAD_FAILED: %+v", r2)
	}
}

// 临时文件句柄：通过 fileGate Reader 走 rcat，上传消费已落盘前缀并与下载
// 重叠，任务收尾后临时目录被清空（Handle.Cleanup）。
func TestCloudJobTempFileCleanup(t *testing.T) {
	s := openStore(t)
	job := cloudJob(t, s)
	sink := &fakeCloudSink{readReaders: true}
	d := cloudDeps(t, s, nil, sink)
	tmpDir := t.TempDir()
	// MemoryLimit=0：超过 StreamLimit 的媒体直接落临时文件
	d.Media = media.Options{TmpDir: tmpDir, MaxFileSize: 1 << 30, StreamLimit: 16}
	payload := make([]byte, 100)
	for i := range payload {
		payload[i] = byte(i)
	}
	d.Fetcher = fetcherWith(chunkInvoker{payload: payload}, cloudDocMsgSized(7, "", int64(len(payload))))

	Process(d)(context.Background(), job)

	r, _ := s.GetRequest(context.Background(), job.RequestID)
	if r.Status != store.RequestSucceeded {
		t.Fatalf("临时文件路径任务应成功: %+v", r)
	}
	calls := sink.snapshot()
	if len(calls) != 1 {
		t.Fatalf("应一次上传: %+v", calls)
	}
	spec := calls[0].Spec
	if spec.FilePath != "" || spec.Reader == nil {
		t.Fatalf("临时文件句柄应通过 Reader 流式上传: %+v", spec)
	}
	if !bytes.Equal(calls[0].Content, payload) {
		t.Fatalf("上传消费到的内容应完整: got %d 字节", len(calls[0].Content))
	}
	// 收尾后临时目录被清空（Cleanup 删除句柄文件）
	entries, err := os.ReadDir(tmpDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("临时目录应被清空: %v %v", entries, err)
	}
	ups, _ := s.CloudUploadsByRequest(context.Background(), job.RequestID)
	if len(ups) != 1 || ups[0].Bytes != int64(len(payload)) {
		t.Fatalf("上传记录字节不符: %+v", ups)
	}
}

// 私有频道：路径使用 -100 前缀数字 ID。
func TestCloudJobPrivateChannelName(t *testing.T) {
	s := openStore(t)
	if _, err := s.CreateUser(context.Background(), store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	r, err := s.CreateRequest(context.Background(), store.Request{
		UserID: 1, SourceKind: store.SourcePrivate, ChannelKey: "-1001234567890", MessageID: 7,
		DeliveryMode: store.DeliveryModeCloud,
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	job := NewJob(1, 1, tmeurl.SourceRef{
		Kind: tmeurl.PeerChannelID, ChannelID: 1234567890, MessageID: 7,
	}, 0, r.ID)
	job.CloudDest = "mega-1"
	sink := &fakeCloudSink{}
	d := cloudDeps(t, s, fetcherWith(errInvoker{}, cloudDocMsg(7, "")), sink)

	Process(d)(context.Background(), job)

	got, err := s.GetRequest(context.Background(), job.RequestID)
	if err != nil || got.Status != store.RequestSucceeded {
		t.Fatalf("私有频道任务应成功: %+v %v", got, err)
	}
	calls := sink.snapshot()
	if len(calls) != 1 || calls[0].Spec.RemotePath != "spore/-1001234567890/2026-09-09/f-7.bin" {
		t.Fatalf("私有频道路径不符: %+v", calls)
	}
}

// 零值 CloudDest 回归：不设置云盘字段的任务走原 TG 投递路径，
// 不触碰 CloudSink（C3）。
func TestCloudDestZeroValueUsesTGPath(t *testing.T) {
	s := openStore(t)
	job, _ := newJobWithRequest(t, s, 55)
	sink := &fakeCloudSink{}
	d := cloudDeps(t, s, &fakeFetcher{msgs: []*tg.Message{{ID: 7, Message: "hello"}}}, sink)

	Process(d)(context.Background(), job)

	if calls := sink.snapshot(); len(calls) != 0 {
		t.Fatalf("非云盘任务不应调用 CloudSink: %+v", calls)
	}
	r, _ := s.GetRequest(context.Background(), job.RequestID)
	if r.Status != store.RequestSucceeded || r.DeliveryMode != store.DeliveryModeText {
		t.Fatalf("文本任务应成功且标记 text: %+v", r)
	}
	sender := d.Sender.(*fakeSender)
	if texts := sender.texts(); len(texts) != 1 || !strings.Contains(texts[0], "hello") {
		t.Fatalf("应走原文本发送路径: %v", texts)
	}
}
