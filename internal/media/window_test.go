package media

import (
	"bytes"
	"errors"
	"io"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// newTestWindow 构造指定参数的窗口缓冲（槽位对齐由 streamWindowAlloc 语义
// 之外的用例自行保证：winSize 必须是 reorderSlotSize 整数倍）。
func newTestWindow(size, winSize int64) *windowBuffer {
	return newWindowBuffer(size, winSize)
}

// 乱序写顺序读：多槽乱序落位后读出内容与直写一致（窗口大于文件时退化为
// reorderBuffer 语义）。
func TestWindowBufferOutOfOrderWriteSequentialRead(t *testing.T) {
	const size = 5*reorderSlotSize + 123 // 5 槽 + 零头
	w := newTestWindow(size, 6*reorderSlotSize)
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	// 乱序分片写入（整槽粒度 + 零头尾片：文件尾 123 字节不足一槽）
	offsets := []int64{2 * reorderSlotSize, 0, 5 * reorderSlotSize, reorderSlotSize, 4 * reorderSlotSize, 3 * reorderSlotSize}
	for _, off := range offsets {
		end := min(off+reorderSlotSize, size)
		if _, err := w.WriteAt(payload[off:end], off); err != nil {
			t.Fatalf("写入 [%d,%d) 失败: %v", off, end, err)
		}
	}
	w.CloseWithError(nil)
	got, err := io.ReadAll(w)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("内容不一致：读出 %d 字节（期望 %d）", len(got), len(payload))
	}
}

// 窗口小于文件：写端超前窗口时背压阻塞，读端推进释放后继续——滑窗语义。
func TestWindowBufferBackpressureSlidesWindow(t *testing.T) {
	const size = 8 * reorderSlotSize
	const win = 3 * reorderSlotSize
	w := newTestWindow(size, win)
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i ^ 0x3C)
	}

	blocked := make(chan struct{}, 1)
	go func() {
		// 顺序写满整个文件：第 3 槽写入应因超出窗口而阻塞（读端未启动）
		for off := int64(0); off < size; off += reorderSlotSize {
			end := min(off+reorderSlotSize, size)
			if _, err := w.WriteAt(payload[off:end], off); err != nil {
				t.Errorf("写入 [%d,%d) 失败: %v", off, end, err)
				return
			}
			if off == 2*reorderSlotSize {
				blocked <- struct{}{}
			}
		}
		w.CloseWithError(nil)
	}()
	// 写完前 3 槽（窗口满）后应阻塞在第 4 槽——读端启动前不产生更多写入
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("前 3 槽写入超时")
	}
	time.Sleep(50 * time.Millisecond) // 留出窗口外写入被背压的时间窗

	got, err := io.ReadAll(w)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("滑窗内容不一致：读出 %d 字节（期望 %d）", len(got), len(payload))
	}
}

// CloseWithError 唤醒阻塞读者：下载失败以错误传播到读端。
func TestWindowBufferCloseWakesBlockedReader(t *testing.T) {
	w := newTestWindow(reorderSlotSize, reorderSlotSize)
	boom := errors.New("下载失败")
	go func() {
		time.Sleep(30 * time.Millisecond)
		w.CloseWithError(boom)
	}()
	start := time.Now()
	if _, err := w.Read(make([]byte, 16)); !errors.Is(err, boom) {
		t.Fatalf("阻塞读者应被关闭唤醒并返回关闭原因，得到 %v", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatal("读者应阻塞等待而非立即返回")
	}
}

// Close 唤醒背压阻塞的写者：errConsumerClosed 让下载循环尽快终止。
func TestWindowBufferCloseWakesBlockedWriter(t *testing.T) {
	const size = 8 * reorderSlotSize
	w := newTestWindow(size, reorderSlotSize) // 窗口仅 1 槽：第 2 槽必背压
	payload := make([]byte, reorderSlotSize)
	done := make(chan error, 1)
	go func() {
		if _, err := w.WriteAt(payload, 0); err != nil {
			done <- err
			return
		}
		_, err := w.WriteAt(payload, reorderSlotSize) // 超前 readPos+win → 阻塞
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	w.CloseWithError(nil)
	select {
	case err := <-done:
		if !errors.Is(err, errConsumerClosed) {
			t.Fatalf("背压写者应被关闭唤醒并返回 errConsumerClosed，得到 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("背压写者未被关闭唤醒")
	}
}

// 重复写幂等：同区域重写不破坏就绪判定；已消费区的重叠写入被丢弃
// （Telegram 重取分片语义），后续读取不受影响。
func TestWindowBufferDuplicateAndOverlappedWrites(t *testing.T) {
	const size = 4 * reorderSlotSize
	// 窗口取全文件大小：本用例聚焦重复/重叠写语义，滑窗背压由专测覆盖
	w := newTestWindow(size, 4*reorderSlotSize)
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i + 1)
	}
	// 写槽 0 → 读走（滑窗）→ 重写槽 0（已消费区，应丢弃）→ 写槽 1..
	if _, err := w.WriteAt(payload[:reorderSlotSize], 0); err != nil {
		t.Fatalf("写入槽 0 失败: %v", err)
	}
	head := make([]byte, reorderSlotSize)
	if n, err := io.ReadFull(w, head); err != nil || int64(n) != reorderSlotSize {
		t.Fatalf("读取槽 0 失败: n=%d err=%v", n, err)
	}
	dup := make([]byte, reorderSlotSize) // 全零重写：若未丢弃会污染已读数据之后
	// 的就绪判定（计数不影响，内容一致性由丢弃保证）
	if n, err := w.WriteAt(dup, 0); err != nil || n != len(dup) {
		t.Fatalf("已消费区重叠写应丢弃并成功返回: n=%d err=%v", n, err)
	}
	for off := int64(reorderSlotSize); off < size; off += reorderSlotSize {
		if _, err := w.WriteAt(payload[off:off+reorderSlotSize], off); err != nil {
			t.Fatalf("写入槽 %d 失败: %v", off/reorderSlotSize, err)
		}
	}
	w.CloseWithError(nil)
	rest, err := io.ReadAll(w)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(rest, payload[reorderSlotSize:]) {
		t.Fatal("已消费区重叠写不应影响后续内容")
	}
}

// 短读升级：下载"成功"关闭但落位不足声明大小 → errShortDownload。
func TestWindowBufferShortDownloadUpgrade(t *testing.T) {
	w := newTestWindow(2*reorderSlotSize, reorderSlotSize)
	if _, err := w.WriteAt(make([]byte, reorderSlotSize), 0); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	w.CloseWithError(nil) // 只落位一半
	if _, err := w.Read(make([]byte, 16)); !errors.Is(err, errShortDownload) {
		t.Fatalf("短读应升级为 errShortDownload，得到 %v", err)
	}
}

// 并发压测（-race）：多写者乱序 + 滑窗背压 + 读者顺序消费，内容守恒。
func TestWindowBufferConcurrentStress(t *testing.T) {
	const slots = 24
	const size = slots * reorderSlotSize
	const win = 5 * reorderSlotSize
	w := newTestWindow(size, win)
	payload := make([]byte, size)
	rng := rand.New(rand.NewSource(42))
	for i := range payload {
		payload[i] = byte(rng.Intn(256))
	}

	const writers = 4
	var wg sync.WaitGroup
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			// 每个写者负责交错的槽子集，模拟 gotd Parallel 的区域划分
			for s := g; s < slots; s += writers {
				off := int64(s) * reorderSlotSize
				if _, err := w.WriteAt(payload[off:off+reorderSlotSize], off); err != nil {
					t.Errorf("写者 %d 槽 %d 失败: %v", g, s, err)
					return
				}
			}
		}(g)
	}
	go func() {
		wg.Wait()
		w.CloseWithError(nil)
	}()

	got, err := io.ReadAll(w)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("并发滑窗内容不一致：读出 %d 字节（期望 %d）", len(got), len(payload))
	}
}

// streamWindowAlloc 边界：大文件封顶、小文件槽位对齐收缩、零防御下限。
func TestStreamWindowAlloc(t *testing.T) {
	if got := streamWindowAlloc(streamWindowBytes + 1); got != streamWindowBytes {
		t.Fatalf("大文件应封顶 %d，得到 %d", streamWindowBytes, got)
	}
	if got := streamWindowAlloc(reorderSlotSize / 2); got != reorderSlotSize {
		t.Fatalf("小文件应对齐到一个槽位，得到 %d", got)
	}
	if got := streamWindowAlloc(3*reorderSlotSize + 1); got != 4*reorderSlotSize {
		t.Fatalf("非整槽小文件应向上对齐，得到 %d", got)
	}
	if got := streamWindowAlloc(0); got != reorderSlotSize {
		t.Fatalf("零大小应取槽位下限，得到 %d", got)
	}
}
