package media

// reorderBuffer（管道化内存流式路径核心组件）的并发语义单测：
//   - 乱序写 → 有序读（就绪前缀推进唤醒阻塞读者）；
//   - 部分就绪即可先读（上传不必等下载完成）；
//   - CloseWithError 双向传播：唤醒阻塞读者 / 终止后续写入；
//   - 成功关闭的短读升级为错误（拒绝上传截断数据）；
//   - 成功关闭后已缓冲数据仍可读尽（EOF）；
//   - 并发读写压测（配合 go test -race）。

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
)

// waitRead 在超时窗内从 r 读取，用于断言"读端应当有返回"；卡死即失败。
func waitRead(t *testing.T, r io.Reader, n int) ([]byte, error) {
	t.Helper()
	type res struct {
		data []byte
		err  error
	}
	done := make(chan res, 1)
	go func() {
		buf := make([]byte, n)
		m, err := r.Read(buf)
		done <- res{buf[:m], err}
	}()
	select {
	case got := <-done:
		return got.data, got.err
	case <-time.After(5 * time.Second):
		t.Fatal("读取卡死：既无数据就绪也无关闭信号")
		return nil, nil
	}
}

// readAllTimeout 全量读取 r，卡死时按超时失败。
func readAllTimeout(t *testing.T, r io.Reader) ([]byte, error) {
	t.Helper()
	type res struct {
		data []byte
		err  error
	}
	done := make(chan res, 1)
	go func() {
		data, err := io.ReadAll(r)
		done <- res{data, err}
	}()
	select {
	case r := <-done:
		return r.data, r.err
	case <-time.After(5 * time.Second):
		t.Fatal("读取卡死：既无数据就绪也无关闭信号")
		return nil, nil
	}
}

// assertBlocked 断言一次读取在超时窗内没有返回（读者应处于阻塞等待）。
func assertBlocked(t *testing.T, r io.Reader) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		_, _ = r.Read(buf)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("未就绪数据的读取不应有返回")
	case <-time.After(100 * time.Millisecond):
	}
}

// 顺序写满后一次读出：内容与写入一致，读尽后 EOF。
func TestReorderBufferSequential(t *testing.T) {
	payload := []byte("0123456789abcdef")
	b := newReorderBuffer(int64(len(payload)))
	if _, err := b.WriteAt(payload, 0); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	b.CloseWithError(nil)

	got, err := readAllTimeout(t, b)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("内容不一致：写入 %q 读出 %q", payload, got)
	}
}

// 乱序写入：读者先启动阻塞等待，跨槽乱序写齐后按顺序读出全部字节。
func TestReorderBufferOutOfOrder(t *testing.T) {
	const size = 3*reorderSlotSize + 1234
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i)
	}
	b := newReorderBuffer(size)

	gotCh := make(chan []byte, 1)
	go func() {
		data, err := io.ReadAll(b)
		if err != nil {
			t.Errorf("读取失败: %v", err)
		}
		gotCh <- data
	}()

	// 读者先等一会儿确保已阻塞，再乱序写入（尾块最先，中块其次，首块最后）
	time.Sleep(50 * time.Millisecond)
	off := int64(2 * reorderSlotSize)
	_, _ = b.WriteAt(payload[off:off+reorderSlotSize], off)
	_, _ = b.WriteAt(payload[reorderSlotSize:2*reorderSlotSize], reorderSlotSize)
	_, _ = b.WriteAt(payload[:reorderSlotSize], 0)
	_, _ = b.WriteAt(payload[2*reorderSlotSize:], 2*reorderSlotSize)
	b.CloseWithError(nil)

	select {
	case got := <-gotCh:
		if !bytes.Equal(got, payload) {
			t.Fatalf("乱序写入后读出内容不一致（长度 %d/%d）", len(got), size)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("乱序写齐后读者未被唤醒")
	}
}

// 部分就绪即可先读：首槽写满后读端立即拿到数据；未就绪区间继续阻塞。
func TestReorderBufferPartialReady(t *testing.T) {
	const size = 2 * reorderSlotSize
	b := newReorderBuffer(size)

	first := bytes.Repeat([]byte{0xAB}, reorderSlotSize)
	if _, err := b.WriteAt(first, 0); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	got, err := waitRead(t, b, reorderSlotSize)
	if err != nil {
		t.Fatalf("首槽就绪后读取失败: %v", err)
	}
	if !bytes.Equal(got, first) {
		t.Fatalf("首槽数据不一致（读出 %d 字节）", len(got))
	}

	assertBlocked(t, b) // 第二槽未写：读端再次阻塞
}

// CloseWithError 唤醒阻塞读者：读端以传入错误返回，不再吐数据。
func TestReorderBufferCloseWakesReader(t *testing.T) {
	boom := errors.New("下载中断")
	b := newReorderBuffer(1024)

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := b.Read(buf)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // 确保读者已进入阻塞等待
	b.CloseWithError(boom)

	select {
	case err := <-done:
		if !errors.Is(err, boom) {
			t.Fatalf("阻塞读者应以关闭错误醒来，得到 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("关闭未唤醒阻塞读者")
	}
}

// 关闭后写入返回错误：让仍在跑的下载写循环尽快终止。
func TestReorderBufferWriteAfterClose(t *testing.T) {
	b := newReorderBuffer(64)
	b.CloseWithError(nil)
	if _, err := b.WriteAt([]byte("x"), 0); !errors.Is(err, errConsumerClosed) {
		t.Fatalf("关闭后写入应返回 errConsumerClosed，得到 %v", err)
	}
}

// 成功关闭但短读：读端以 errShortDownload 失败——截断数据不得当完整文件
// 上传（消费者无论已读多少，下一步读取立即感知失败并中止）。
func TestReorderBufferShortDownload(t *testing.T) {
	b := newReorderBuffer(2 * reorderSlotSize)
	first := bytes.Repeat([]byte{0x11}, reorderSlotSize)
	if _, err := b.WriteAt(first, 0); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	b.CloseWithError(nil) // 第二槽从未写入

	got, err := readAllTimeout(t, b)
	if !errors.Is(err, errShortDownload) {
		t.Fatalf("短读应以 errShortDownload 失败，得到 %v（数据 %d 字节）", err, len(got))
	}
}

// 越界写入按错误拒绝（防御：size 元数据与实际行为不符时尽早暴露）。
func TestReorderBufferWriteOutOfRange(t *testing.T) {
	b := newReorderBuffer(16)
	if _, err := b.WriteAt(make([]byte, 8), 12); err == nil {
		t.Fatal("越界写入应返回错误")
	}
	if _, err := b.WriteAt([]byte("ok"), 14); err != nil {
		t.Fatalf("边界内写入不应失败: %v", err)
	}
}

// 并发压测：多个写 goroutine 按 512KB 对齐分片（含少量重复/乱序）写，
// 一个读者全量读出，校验内容完整——go test -race 下验证数据竞争。
func TestReorderBufferConcurrentStress(t *testing.T) {
	const size = 8*reorderSlotSize + 777
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	b := newReorderBuffer(size)

	var writers sync.WaitGroup
	for w := int64(0); w < 4; w++ {
		writers.Add(1)
		go func(w int64) {
			defer writers.Done()
			// 每个 writer 负责自己的分片子集，乱序由调度自然产生；
			// 追加少量跨子集重复写（Telegram 重取语义），内容幂等
			for s := w; s*reorderSlotSize < size; s += 4 {
				off := s * reorderSlotSize
				if _, err := b.WriteAt(payload[off:min(off+reorderSlotSize, size)], off); err != nil {
					t.Errorf("并发写入失败: %v", err)
					return
				}
			}
			if w == 0 { // 重复写首片，验证幂等
				_, _ = b.WriteAt(payload[:reorderSlotSize], 0)
			}
		}(w)
	}

	gotCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	go func() {
		data, err := io.ReadAll(b)
		gotCh <- data
		errCh <- err
	}()

	writers.Wait()
	b.CloseWithError(nil)

	select {
	case got := <-gotCh:
		if err := <-errCh; err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("并发写入后读出内容不一致（长度 %d/%d）", len(got), size)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("并发写齐后读者未被唤醒")
	}
}

// 重复调用 CloseWithError：首次错误为准，后续为无害 no-op。
func TestReorderBufferDoubleClose(t *testing.T) {
	b := newReorderBuffer(32)
	boom := errors.New("first")
	b.CloseWithError(boom)
	b.CloseWithError(errors.New("second"))
	if _, err := waitRead(t, b, 4); !errors.Is(err, boom) {
		t.Fatalf("应以首次关闭错误为准，得到 %v", err)
	}
}

// 随机读写模糊：乱序 + 变长分片写满，读出必须精确还原（rand 只影响顺序）。
func TestReorderBufferFuzzyOrder(t *testing.T) {
	const size = 5*reorderSlotSize + 321
	payload := make([]byte, size)
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range payload {
		payload[i] = byte(rng.IntN(256))
	}
	b := newReorderBuffer(size)

	var slots []int64
	for off := int64(0); off < size; off += reorderSlotSize {
		slots = append(slots, off)
	}
	rng.Shuffle(len(slots), func(i, j int) { slots[i], slots[j] = slots[j], slots[i] })

	gotCh := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(b)
		gotCh <- data
	}()
	for _, off := range slots {
		_, _ = b.WriteAt(payload[off:min(off+reorderSlotSize, size)], off)
	}
	b.CloseWithError(nil)

	select {
	case got := <-gotCh:
		if !bytes.Equal(got, payload) {
			t.Fatalf("随机乱序写后读出内容不一致（长度 %d/%d）", len(got), size)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("随机乱序写齐后读者未被唤醒")
	}
}
