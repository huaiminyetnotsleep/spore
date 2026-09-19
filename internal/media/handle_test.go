package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// checkTempDir 的确定性测试：注入 DirUsage，不依赖真实分区大小。
// 每个用例使用独立临时目录——占用统计有 30s TTL 缓存（按目录键），
// 同目录换注入函数会命中旧缓存。
func TestCheckTempDir(t *testing.T) {
	log := testLogger()

	// 已用 + 即将落盘 > 上限 → 拒绝
	opt := Options{TmpDir: t.TempDir(), MaxDirSize: 5 << 30}
	opt.DirUsage = func(string) (int64, error) { return int64(5) << 30, nil }
	if err := checkTempDir(opt, 1<<20, log); apperr.From(err).Code != apperr.CodeTempDirFull {
		t.Fatalf("超限应返回 CodeTempDirFull，得到 %v", err)
	}

	// 未超限 → 放行
	opt = Options{TmpDir: t.TempDir(), MaxDirSize: 5 << 30}
	opt.DirUsage = func(string) (int64, error) { return int64(1) << 30, nil }
	if err := checkTempDir(opt, 1<<30, log); err != nil {
		t.Fatalf("未超限应放行，得到 %v", err)
	}

	// 未配置上限（<=0）→ 跳过
	if err := checkTempDir(Options{TmpDir: t.TempDir()}, 1<<40, log); err != nil {
		t.Fatalf("未配置上限应跳过，得到 %v", err)
	}

	// 目录查询失败 → fail-open 放行
	opt = Options{TmpDir: t.TempDir(), MaxDirSize: 5 << 30}
	opt.DirUsage = func(string) (int64, error) { return 0, errors.New("stat failed") }
	if err := checkTempDir(opt, 1<<20, log); err != nil {
		t.Fatalf("查询失败应跳过，得到 %v", err)
	}
}

// dirSize 汇总目录树字节数（含子目录、跳过竞态删除文件）。
func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), make([]byte, 1024), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b.bin"), make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	total, err := dirSize(dir)
	if err != nil || total != 1536 {
		t.Fatalf("dirSize 应得 1536 字节，得到 %d err=%v", total, err)
	}
}

// Open 在临时文件分支的预检：注入 DirUsage 使目录超限，应拒绝且不落盘、不下载。
func TestOpenRejectsTempDirFull(t *testing.T) {
	log := testLogger()
	tmp := t.TempDir()
	opt := Options{
		TmpDir:      tmp,
		MaxFileSize: 50 << 20,
		StreamLimit: 20 << 20,
		MaxDirSize:  1 << 20,
		DirUsage:    func(string) (int64, error) { return 1 << 20, nil }, // 已用 1MB
	}
	m := message.Media{
		Kind:     message.KindDocument,
		Size:     30 << 20, // 落盘分支（> StreamLimit，未启用内存路径）
		FileName: "big.bin",
		Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2},
	}
	_, err := Open(context.Background(), nil, m, "123", opt, log, nil)
	if apperr.From(err).Code != apperr.CodeTempDirFull {
		t.Fatalf("应返回 CodeTempDirFull，得到 %v", err)
	}
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 0 {
		t.Fatalf("拒绝下载不应产生临时文件，得到 %v", entries)
	}
}

// chunkInvoker 是返回真实字节的假 MTProto invoker：把 UploadGetFile 的
// Offset/Limit 映射到预置 payload 切片，使三分支下载在测试中可真实执行
// （返回空切片即文件读完，下载器据此结束）。
type chunkInvoker struct{ payload []byte }

func (c chunkInvoker) Invoke(_ context.Context, in bin.Encoder, out bin.Decoder) error {
	req, ok := in.(*tg.UploadGetFileRequest)
	if !ok {
		return fmt.Errorf("chunkInvoker: 意外的请求类型 %T", in)
	}
	if req.Offset < 0 {
		return errors.New("chunkInvoker: 负偏移读取")
	}
	// 并行下载的 worker 会在收到 EOF 信号前乐观领到越界偏移——
	// 与真实服务器一致：越过文件末尾返回空字节（下载器据此判结束）
	if req.Offset >= int64(len(c.payload)) {
		return encodeUploadFile(out, nil)
	}
	end := min(req.Offset+int64(req.Limit), int64(len(c.payload)))
	return encodeUploadFile(out, c.payload[req.Offset:end])
}

// encodeUploadFile 把一个 upload.file 响应编码进 out（假 invoker 的应答通道）。
func encodeUploadFile(out bin.Decoder, data []byte) error {
	file := &tg.UploadFile{
		Type:  &tg.StorageFilePartial{},
		Bytes: data,
	}
	b := new(bin.Buffer)
	if err := file.Encode(b); err != nil {
		return err
	}
	return out.Decode(b)
}

// errInvoker 永远失败，用于观察下载失败在三条路径上的传播。
type errInvoker struct{}

func (errInvoker) Invoke(context.Context, bin.Encoder, bin.Decoder) error {
	return errors.New("测试假 invoker：下载失败")
}

// readHandleTimeout 在超时窗内读尽句柄，卡死即失败。
func readHandleTimeout(t *testing.T, h *Handle) ([]byte, error) {
	t.Helper()
	type res struct {
		data []byte
		err  error
	}
	done := make(chan res, 1)
	go func() {
		data, err := io.ReadAll(h.Reader)
		done <- res{data, err}
	}()
	select {
	case r := <-done:
		return r.data, r.err
	case <-time.After(10 * time.Second):
		t.Fatal("句柄读取卡死")
		return nil, nil
	}
}

// 流式分支（Size <= StreamLimit）：真实字节经 io.Pipe 透传，内容一致。
func TestOpenStreamPathDeliversPayload(t *testing.T) {
	payload := []byte("stream-payload-0123456789")
	opt := Options{TmpDir: t.TempDir(), MaxFileSize: 1 << 20, StreamLimit: 1 << 20}
	m := message.Media{
		Kind:     message.KindDocument,
		Size:     int64(len(payload)),
		FileName: "small.bin",
		Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2},
	}
	h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), m, "1", opt, testLogger(), nil)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer h.Cleanup()
	got, err := readHandleTimeout(t, h)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("内容不一致：读出 %q", got)
	}
}

// 内存管道分支（StreamLimit < Size <= MemoryLimit）：多线程并行下载乱序落位，
// 顺序读出完整内容；期间临时目录零写入。
func TestOpenMemoryPipelinePath(t *testing.T) {
	payload := make([]byte, 2*reorderSlotSize+5)
	for i := range payload {
		payload[i] = byte(i * 3)
	}
	tmp := t.TempDir()
	opt := Options{
		TmpDir:          tmp,
		MaxFileSize:     1 << 30,
		StreamLimit:     64 << 10, // 强制走非流式分支
		MemoryLimit:     1 << 30,
		DownloadThreads: 4,
	}
	m := message.Media{
		Kind:     message.KindDocument,
		Size:     int64(len(payload)),
		FileName: "mid.bin",
		Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2},
	}
	h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), m, "2", opt, testLogger(), nil)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	got, err := readHandleTimeout(t, h)
	h.Cleanup()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if len(got) != len(payload) {
		t.Fatalf("长度不一致：%d/%d", len(got), len(payload))
	}
	for i := range payload {
		if got[i] != payload[i] {
			t.Fatalf("第 %d 字节不一致：%d != %d", i, got[i], payload[i])
		}
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Fatalf("内存管道路径不应写临时目录，得到 %v", entries)
	}
}

// 内存管道分支下载失败：读端以 MEDIA_DOWNLOAD_FAILED 错误返回。
func TestOpenMemoryPipelineDownloadFailure(t *testing.T) {
	opt := Options{
		TmpDir:          t.TempDir(),
		MaxFileSize:     1 << 30,
		StreamLimit:     64 << 10,
		MemoryLimit:     1 << 30,
		DownloadThreads: 2,
	}
	m := message.Media{
		Kind:     message.KindDocument,
		Size:     reorderSlotSize + 1,
		FileName: "bad.bin",
		Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2},
	}
	h, err := Open(context.Background(), tg.NewClient(errInvoker{}), m, "3", opt, testLogger(), nil)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer h.Cleanup()
	if _, err := readHandleTimeout(t, h); apperr.From(err).Code != apperr.CodeMediaDownloadFailed {
		t.Fatalf("读端应以 CodeMediaDownloadFailed 失败，得到 %v", err)
	}
}

// 未启用内存上限（MemoryLimit=0 或 Size 超限）：回落临时文件路径，
// 多线程落盘后读出内容一致，Cleanup 删除文件。
func TestOpenTempFileFallbackWithThreads(t *testing.T) {
	payload := make([]byte, reorderSlotSize+7)
	for i := range payload {
		payload[i] = byte(i ^ 0x5A)
	}
	tmp := t.TempDir()
	opt := Options{
		TmpDir:          tmp,
		MaxFileSize:     1 << 30,
		StreamLimit:     64 << 10,
		DownloadThreads: 4, // MemoryLimit 未配置 → 直接落盘
	}
	m := message.Media{
		Kind:     message.KindDocument,
		Size:     int64(len(payload)),
		FileName: "huge.bin",
		Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2},
	}
	h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), m, "4", opt, testLogger(), nil)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 1 {
		t.Fatalf("落盘分支应产生 1 个临时文件，得到 %d", len(entries))
	}
	got, err := readHandleTimeout(t, h)
	h.Cleanup()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("内容不一致（读出 %d 字节）", len(got))
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Fatalf("Cleanup 后临时文件应删除，残留 %d 个", len(entries))
	}
}

// onDownload 进度回调：三条下载路径的增量回调之和都应等于声明大小
// （内存管道路径为乱序落位字节，总和仍守恒）。
func TestOpenDownloadProgressCallback(t *testing.T) {
	cases := []struct {
		name string
		opt  func(tmp string) Options
	}{
		{"stream", func(tmp string) Options {
			return Options{TmpDir: tmp, MaxFileSize: 1 << 30, StreamLimit: 2 << 20}
		}},
		{"memory", func(tmp string) Options {
			return Options{TmpDir: tmp, MaxFileSize: 1 << 30, StreamLimit: 64 << 10, MemoryLimit: 1 << 30, DownloadThreads: 4}
		}},
		{"tempfile", func(tmp string) Options {
			return Options{TmpDir: tmp, MaxFileSize: 1 << 30, StreamLimit: 64 << 10, DownloadThreads: 4}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := make([]byte, 2*reorderSlotSize+5)
			var seen atomic.Int64
			h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}),
				message.Media{
					Kind:     message.KindDocument,
					Size:     int64(len(payload)),
					FileName: "progress.bin",
					Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2},
				}, "p", tc.opt(t.TempDir()), testLogger(), func(n int64) { seen.Add(n) })
			if err != nil {
				t.Fatalf("打开失败: %v", err)
			}
			if _, err := readHandleTimeout(t, h); err != nil {
				t.Fatalf("读取失败: %v", err)
			}
			h.Cleanup()
			if seen.Load() != int64(len(payload)) {
				t.Fatalf("进度回调累计 %d，应为 %d", seen.Load(), len(payload))
			}
		})
	}
}

// 分卷拆分路径：超过单文件上限但落在拆分总上限内的媒体不再拒绝，强制走
// 临时文件路径；完整落盘后 Path/WaitDownloaded/OpenSection 支撑按区间
// 切分。不可拆分（未配置拆分上限或超出总上限）保持 FILE_TOO_LARGE。
func TestOpenSplitPath(t *testing.T) {
	log := testLogger()
	payload := make([]byte, 200)
	for i := range payload {
		payload[i] = byte(i)
	}
	newOpt := func() Options {
		return Options{
			TmpDir:            t.TempDir(),
			MaxFileSize:       100,
			StreamLimit:       10,
			MemoryLimit:       50,
			DownloadThreads:   3,
			MaxSplitTotalSize: 400,
			SplitSegmentSize:  60,
		}
	}
	newMedia := func(size int64) message.Media {
		return message.Media{
			Kind:     message.KindDocument,
			Size:     size,
			FileName: "big.bin",
			Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{1}},
		}
	}

	t.Run("超限可拆分：强制临时文件路径并完整落盘", func(t *testing.T) {
		opt := newOpt()
		h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload: payload}),
			newMedia(200), "123", opt, log, nil)
		if err != nil {
			t.Fatalf("拆分媒体应放行: %v", err)
		}
		defer h.Cleanup()
		path, ok := h.Path()
		if !ok {
			t.Fatal("拆分媒体应走临时文件路径（Path 可用）")
		}
		if err := h.WaitDownloaded(context.Background()); err != nil {
			t.Fatalf("下载应完整落盘: %v", err)
		}
		// 区间切分：60/60/60/20 四段，拼接等于原文件
		var got []byte
		for start := int64(0); start < 200; start += 60 {
			length := min(int64(60), 200-start)
			sr, err := h.OpenSection(start, length)
			if err != nil {
				t.Fatalf("区间打开失败: %v", err)
			}
			data, err := io.ReadAll(sr)
			sr.Close()
			if err != nil {
				t.Fatalf("区间读取失败: %v", err)
			}
			got = append(got, data...)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("分段拼接应等于原文件: got %d 字节", len(got))
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("临时文件应存在于 Cleanup 前: %v", err)
		}
	})

	t.Run("区间越界拒绝", func(t *testing.T) {
		opt := newOpt()
		h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload: payload}),
			newMedia(200), "124", opt, log, nil)
		if err != nil {
			t.Fatalf("拆分媒体应放行: %v", err)
		}
		defer h.Cleanup()
		if err := h.WaitDownloaded(context.Background()); err != nil {
			t.Fatalf("下载应完整落盘: %v", err)
		}
		if _, err := h.OpenSection(150, 60); apperr.From(err).Code != apperr.CodeInternal {
			t.Fatalf("越界区间应防御报错: %v", err)
		}
	})

	t.Run("超出拆分总上限拒绝", func(t *testing.T) {
		opt := newOpt()
		_, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload: payload}),
			newMedia(401), "125", opt, log, nil)
		if apperr.From(err).Code != apperr.CodeFileTooLarge {
			t.Fatalf("超拆分总上限应报 FILE_TOO_LARGE: %v", err)
		}
	})

	t.Run("未启用拆分保持历史拒绝", func(t *testing.T) {
		opt := newOpt()
		opt.MaxSplitTotalSize = 0
		_, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload: payload}),
			newMedia(200), "126", opt, log, nil)
		if apperr.From(err).Code != apperr.CodeFileTooLarge {
			t.Fatalf("未启用拆分应报 FILE_TOO_LARGE: %v", err)
		}
	})

	t.Run("非临时文件路径不支持拆分扩展", func(t *testing.T) {
		opt := newOpt()
		small := newMedia(10)
		opt.MaxFileSize = 100
		h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload: payload[:10]}),
			small, "127", opt, log, nil)
		if err != nil {
			t.Fatalf("小文件应正常打开: %v", err)
		}
		defer h.Cleanup()
		if _, ok := h.Path(); ok {
			t.Fatal("流式路径不应暴露文件路径")
		}
		if err := h.WaitDownloaded(context.Background()); apperr.From(err).Code != apperr.CodeInternal {
			t.Fatalf("流式路径应拒绝等待落盘: %v", err)
		}
		if _, err := h.OpenSection(0, 1); apperr.From(err).Code != apperr.CodeInternal {
			t.Fatalf("流式路径应拒绝区间读取: %v", err)
		}
	})
}
