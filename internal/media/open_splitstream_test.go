package media

// Open 对可拆分视频的流式切段句柄测试：流式窗口路径（无文件路径暴露、
// 零临时目录写入、进度回调守恒）、OpenSplitFile 强制落盘、内存预算降级
// 与 TEMP_DIR_FULL 1× 预检。

import (
	"context"
	"io"
	"os"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// streamTestVideo 构造可拆分视频媒体（50B 上限 + 400B 拆分总量内）。
func streamTestVideo(size int64) message.Media {
	return message.Media{
		Kind:     message.KindVideo,
		Size:     size,
		FileName: "big.mp4",
		Video:    &message.VideoMeta{Width: 640, Height: 480, Duration: 100},
		Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2, FileReference: []byte{1}},
	}
}

func streamTestOpt(tmp string) Options {
	return Options{
		TmpDir:            tmp,
		MaxFileSize:       50,
		StreamLimit:       10,
		MemoryLimit:       20, // 可拆分媒体不进内存管道分支（不受此限制约束）
		DownloadThreads:   4,
		MaxSplitTotalSize: 400,
		SplitSegmentSize:  60,
	}
}

// 可拆分视频：走流式窗口句柄——Path 不可用、内容完整、零临时目录写入。
func TestOpenSplitVideoStreamingHandle(t *testing.T) {
	payload := make([]byte, 200)
	for i := range payload {
		payload[i] = byte(i * 5)
	}
	tmp := t.TempDir()
	h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), streamTestVideo(200), "201", streamTestOpt(tmp), testLogger(), nil)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer h.Cleanup()
	if _, ok := h.Path(); ok {
		t.Fatal("流式句柄不应暴露文件路径")
	}
	if err := h.WaitDownloaded(context.Background()); apperr.From(err).Code != apperr.CodeInternal {
		t.Fatalf("流式句柄应拒绝等待落盘: %v", err)
	}
	got, err := readHandleTimeout(t, h)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("内容不一致（读出 %d 字节）", len(got))
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Fatalf("流式路径不应写临时目录，得到 %v", entries)
	}
}

// OpenSplitFile：强制临时文件路径（流式切段的尾 moov 回退入口）。
func TestOpenSplitFileForcesTempPath(t *testing.T) {
	payload := make([]byte, 200)
	for i := range payload {
		payload[i] = byte(i * 3)
	}
	tmp := t.TempDir()
	m := streamTestVideo(200)
	h, err := OpenSplitFile(context.Background(), tg.NewClient(chunkInvoker{payload}), m, "202", streamTestOpt(tmp), testLogger(), nil)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer h.Cleanup()
	if _, ok := h.Path(); !ok {
		t.Fatal("OpenSplitFile 应强制临时文件路径")
	}
	if err := h.WaitDownloaded(context.Background()); err != nil {
		t.Fatalf("应完整落盘: %v", err)
	}
	sr, err := h.OpenSection(0, 200)
	if err != nil {
		t.Fatalf("区间读取失败: %v", err)
	}
	defer sr.Close()
	data, err := io.ReadAll(sr)
	if err != nil || string(data) != string(payload) {
		t.Fatalf("落盘内容不一致: %d 字节 err=%v", len(data), err)
	}
}

// 内存预算不足（窗口分配 1MB > 预算 64KB）：流式路径降级临时文件路径。
func TestOpenSplitVideoBudgetDegradesToFile(t *testing.T) {
	payload := make([]byte, 200)
	tmp := t.TempDir()
	opt := streamTestOpt(tmp)
	opt.Memory = NewBudgetGate(func() int64 { return 64 << 10 })
	h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), streamTestVideo(200), "203", opt, testLogger(), nil)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer h.Cleanup()
	if _, ok := h.Path(); !ok {
		t.Fatal("预算不足应降级临时文件路径")
	}
	if err := h.WaitDownloaded(context.Background()); err != nil {
		t.Fatalf("降级路径应完整落盘: %v", err)
	}
	if g := opt.Memory.(*BudgetGate).Held(); g != 0 {
		t.Fatalf("降级路径不应持有窗口记账，held=%d", g)
	}
}

// 流式路径 1× 预检：分段总量装不下时 TEMP_DIR_FULL，不发起下载、零写入。
func TestOpenSplitVideoTempDirFull(t *testing.T) {
	payload := make([]byte, 200)
	tmp := t.TempDir()
	opt := streamTestOpt(tmp)
	opt.MaxDirSize = 150 // 1×（200）即超
	opt.DirUsage = func(string) (int64, error) { return 0, nil }
	_, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), streamTestVideo(200), "204", opt, testLogger(), nil)
	if apperr.From(err).Code != apperr.CodeTempDirFull {
		t.Fatalf("应返回 CodeTempDirFull，得到 %v", err)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Fatalf("拒绝下载不应产生临时文件，得到 %v", entries)
	}
}

// 流式路径进度回调：增量之和等于声明大小（乱序落位字节守恒）。
func TestOpenSplitVideoProgressCallback(t *testing.T) {
	payload := make([]byte, 200)
	var seen atomic.Int64
	h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), streamTestVideo(200), "205", streamTestOpt(t.TempDir()), testLogger(), func(n int64) { seen.Add(n) })
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	if _, err := readHandleTimeout(t, h); err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	h.Cleanup()
	if seen.Load() != 200 {
		t.Fatalf("进度回调累计 %d，应为 200", seen.Load())
	}
}
