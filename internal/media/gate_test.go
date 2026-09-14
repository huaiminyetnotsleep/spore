package media

// fileGate（临时文件路径"边下边发"核心组件）的并发语义单测：
//   - 乱序落盘 → 有序读（就绪前缀推进唤醒阻塞读者）；
//   - 部分就绪即可先读（上传不必等整个文件落盘）；
//   - CloseWithError 双向传播：唤醒阻塞读者 / 终止后续写入；
//   - 成功关闭的短读升级为 errShortDownload；
//   - 成功关闭后已落盘数据仍可读尽（EOF）；
//   - 进度回调与读写并发正确性（配合 go test -race）。

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTestGate 在临时目录建文件并返回 fileGate。
func newTestGate(t *testing.T, size int64, on func(int64)) *fileGate {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "gate.bin"))
	if err != nil {
		t.Fatalf("建文件失败: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return newFileGate(f, size, on)
}

// 顺序写满后一次读出：内容与写入一致，读尽后 EOF。
func TestFileGateSequential(t *testing.T) {
	payload := []byte("0123456789abcdef")
	g := newTestGate(t, int64(len(payload)), nil)
	if _, err := g.WriteAt(payload, 0); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	g.CloseWithError(nil)

	got, err := readAllTimeout(t, g)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("内容不一致：写入 %q 读出 %q", payload, got)
	}
}

// 乱序写入：读者先阻塞，跨槽乱序落盘后按顺序读出全部字节。
func TestFileGateOutOfOrder(t *testing.T) {
	const size = 3*reorderSlotSize + 1234
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i)
	}
	g := newTestGate(t, size, nil)

	gotCh := make(chan []byte, 1)
	go func() {
		data, err := io.ReadAll(g)
		if err != nil {
			t.Errorf("读取失败: %v", err)
		}
		gotCh <- data
	}()

	time.Sleep(50 * time.Millisecond) // 确保读者已阻塞
	off := int64(2 * reorderSlotSize)
	_, _ = g.WriteAt(payload[off:off+reorderSlotSize], off)
	_, _ = g.WriteAt(payload[reorderSlotSize:2*reorderSlotSize], reorderSlotSize)
	_, _ = g.WriteAt(payload[:reorderSlotSize], 0)
	_, _ = g.WriteAt(payload[2*reorderSlotSize:], 2*reorderSlotSize)
	g.CloseWithError(nil)

	select {
	case got := <-gotCh:
		if !bytes.Equal(got, payload) {
			t.Fatalf("乱序落盘后读出内容不一致（长度 %d/%d）", len(got), size)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("乱序写齐后读者未被唤醒")
	}
}

// 部分就绪即可先读：首槽落盘后读端立即拿到数据；未就绪区间继续阻塞。
func TestFileGatePartialReady(t *testing.T) {
	const size = 2 * reorderSlotSize
	g := newTestGate(t, size, nil)

	first := bytes.Repeat([]byte{0xAB}, reorderSlotSize)
	if _, err := g.WriteAt(first, 0); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got, err := waitRead(t, g, reorderSlotSize)
	if err != nil {
		t.Fatalf("首槽就绪后读取失败: %v", err)
	}
	if !bytes.Equal(got, first) {
		t.Fatalf("首槽数据不一致（读出 %d 字节）", len(got))
	}

	assertBlocked(t, g) // 第二槽未写：读端再次阻塞
}

// CloseWithError 唤醒阻塞读者：读端以传入错误返回，不再吐数据。
func TestFileGateCloseWakesReader(t *testing.T) {
	boom := errors.New("下载中断")
	g := newTestGate(t, 1024, nil)

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := g.Read(buf)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // 确保读者已进入阻塞等待
	g.CloseWithError(boom)

	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("阻塞读者应以关闭错误醒来，得到 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("关闭未唤醒阻塞读者")
	}
}

// 关闭后写入返回 errConsumerClosed：让仍在跑的下载写循环尽快终止。
func TestFileGateWriteAfterClose(t *testing.T) {
	g := newTestGate(t, 64, nil)
	g.CloseWithError(nil)
	if _, err := g.WriteAt([]byte("x"), 0); !errors.Is(err, errConsumerClosed) {
		t.Fatalf("关闭后写入应返回 errConsumerClosed，得到 %v", err)
	}
}

// 成功关闭但短读：读端以 errShortDownload 失败——截断数据不得当完整文件上传。
func TestFileGateShortDownload(t *testing.T) {
	g := newTestGate(t, 2*reorderSlotSize, nil)
	first := bytes.Repeat([]byte{0x11}, reorderSlotSize)
	if _, err := g.WriteAt(first, 0); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	g.CloseWithError(nil) // 第二槽从未写入

	got, err := readAllTimeout(t, g)
	if !errors.Is(err, errShortDownload) {
		t.Fatalf("短读应以 errShortDownload 失败，得到 %v（数据 %d 字节）", err, len(got))
	}
}

// 重复调用 CloseWithError：首次错误为准，后续为无害 no-op（Cleanup 在成功
// 收尾后调用时必须幂等）。
func TestFileGateDoubleClose(t *testing.T) {
	g := newTestGate(t, 32, nil)
	boom := errors.New("first")
	g.CloseWithError(boom)
	g.CloseWithError(errors.New("second"))
	if _, err := waitRead(t, g, 4); !errors.Is(err, boom) {
		t.Fatalf("应以首次关闭错误为准，得到 %v", err)
	}
}

// 越界写入按错误拒绝（防御：size 元数据与实际行为不符时尽早暴露）。
func TestFileGateWriteOutOfRange(t *testing.T) {
	g := newTestGate(t, 16, nil)
	if _, err := g.WriteAt(make([]byte, 8), 12); err == nil {
		t.Fatal("越界写入应返回错误")
	}
	if _, err := g.WriteAt([]byte("ok"), 14); err != nil {
		t.Fatalf("边界内写入不应失败: %v", err)
	}
}

// 重复写同一分片（Telegram 重取语义）：就绪判定幂等，读出内容为最后一次写入。
func TestFileGateRewriteIdempotent(t *testing.T) {
	const size = 2 * reorderSlotSize
	g := newTestGate(t, size, nil)

	first := bytes.Repeat([]byte{0xAA}, reorderSlotSize)
	second := bytes.Repeat([]byte{0xBB}, reorderSlotSize)
	if _, err := g.WriteAt(first, 0); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}
	if _, err := g.WriteAt(second, 0); err != nil { // 重复写首槽
		t.Fatalf("重复写入失败: %v", err)
	}
	if _, err := g.WriteAt(first, reorderSlotSize); err != nil {
		t.Fatalf("尾槽写入失败: %v", err)
	}
	g.CloseWithError(nil)

	got, err := readAllTimeout(t, g)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(got[:reorderSlotSize], second) {
		t.Fatal("重复写后首槽内容应为最后一次写入")
	}
}

// 进度回调：每片落位后的增量回调之和等于落位字节总数（WriteAt 返回前
// 同步执行，EOF 即全部回调已发生）。
func TestFileGateProgressCallback(t *testing.T) {
	const size = 2*reorderSlotSize + 999
	var seen atomic.Int64
	g := newTestGate(t, size, func(n int64) { seen.Add(n) })

	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i * 5)
	}
	gotCh := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(g)
		gotCh <- data
	}()
	for off := int64(0); off < size; off += reorderSlotSize {
		if _, err := g.WriteAt(payload[off:min(off+reorderSlotSize, size)], off); err != nil {
			t.Fatalf("写入失败: %v", err)
		}
	}
	g.CloseWithError(nil)

	select {
	case got := <-gotCh:
		if !bytes.Equal(got, payload) {
			t.Fatalf("读出内容不一致（长度 %d/%d）", len(got), size)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("写齐后读者未被唤醒")
	}
	if seen.Load() != size {
		t.Fatalf("进度回调累计 %d，应为 %d", seen.Load(), size)
	}
}

// 并发压测：多个写 goroutine 按分片对齐乱序落盘，一个读者全量读出，
// 校验内容完整——go test -race 下验证数据竞争。
func TestFileGateConcurrentStress(t *testing.T) {
	const size = 8*reorderSlotSize + 777
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	var seen atomic.Int64
	g := newTestGate(t, size, func(n int64) { seen.Add(n) })

	var writers sync.WaitGroup
	for w := int64(0); w < 4; w++ {
		writers.Add(1)
		go func(w int64) {
			defer writers.Done()
			// 每个 writer 负责自己的分片子集，乱序由调度自然产生；
			// 追加少量跨子集重复写（Telegram 重取语义），内容幂等
			for s := w; s*reorderSlotSize < size; s += 4 {
				off := s * reorderSlotSize
				if _, err := g.WriteAt(payload[off:min(off+reorderSlotSize, size)], off); err != nil {
					t.Errorf("并发写入失败: %v", err)
					return
				}
			}
			if w == 0 { // 重复写首片，验证幂等
				_, _ = g.WriteAt(payload[:reorderSlotSize], 0)
			}
		}(w)
	}

	gotCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		data, err := io.ReadAll(g)
		gotCh <- data
		errCh <- err
	}()

	writers.Wait()
	g.CloseWithError(nil)

	select {
	case got := <-gotCh:
		if err := <-errCh; err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("并发写入后读出内容不一致（长度 %d/%d）", len(got), size)
		}
		if seen.Load() < size {
			t.Fatalf("进度回调累计 %d 不应小于落位总量 %d", seen.Load(), size)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("并发写齐后读者未被唤醒")
	}
}
