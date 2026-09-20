package media

import (
	"errors"
	"io"
	"sync"
)

// streamWindowBytes 是有界窗口重排序缓冲的窗口上限：下载侧允许超前消费侧
// 的最大字节数。128MB 在 DOWNLOAD_THREADS 典型 4–16 线程下留足分片错位
// 空间（并行吞吐不受窗口制约），同时把流式切段路径的常驻 RAM 钉死在该值
// （经 Options.Memory 进程级预算记账，预算不足自动降级临时文件路径）。
const streamWindowBytes = int64(128) << 20

// windowBuffer 是超大视频"单遍流式切段"路径的核心组件：下载多线程并行
// 分片乱序写入，消费侧（ffmpeg stdin）顺序阻塞读——下载保持并行（对照
// gotd Stream 的顺序单连接），源文件全程不落盘，内存封顶在窗口大小。
//
// 与 reorderBuffer（内存全量重排序，Size 即内存占用）的区别正是"有界"：
// 已消费前缀立即释放复用（滑动缓冲），写端超前读端超过窗口时阻塞形成
// 背压。与 reorderBuffer / fileGate 同构的关闭语义：
//
//   - 写侧（io.WriterAt）：gotd Parallel 下载的块按完成序乱序落位；
//   - 读侧（io.Reader）：顺序阻塞读——所需前缀未就绪时等待，就绪前缀
//     推进即被唤醒；读端推进后唤醒被背压的写者；
//   - CloseWithError：下载 goroutine 收尾（err 为 nil 表示成功；短读升级
//     为错误）或消费方放弃（Cleanup）时调用，双侧 Broadcast 唤醒全部
//     阻塞者；关闭后写入返回错误，让仍在跑的下载循环尽快终止。
//
// 缓冲位置以绝对偏移取模放置（真环形，无需搬移数据）：空间条件
// （写区间末尾 ≤ readPos+winSize）保证同一下标上"已消费旧槽"与"在窗新槽"
// 永不同时存活。槽位记账与 reorderBuffer 同一套：按槽累计判满（不假设
// 对齐/不重叠——Telegram 重取分片重复写，就绪判定用 ≥）；已消费区的重叠
// 写入直接丢弃（内容一致，读端早已读走）；读者越过整槽后复位计数，环形
// 下标供后续槽复用。
type windowBuffer struct {
	mu        sync.Mutex
	dataCond  *sync.Cond // 挂在 mu 上：就绪前缀推进或关闭时 Broadcast（唤醒读者）
	spaceCond *sync.Cond // 挂在 mu 上：读端推进或关闭时 Broadcast（唤醒背压写者）

	buf      []byte  // 滑动缓冲，长度 = winSize；窗口内偏移 = 绝对偏移 - readPos
	written  []int32 // 槽位累计写入计数（环形下标 = 绝对槽号 % 槽数）
	winSize  int64   // 窗口大小（槽位对齐，封顶 streamWindowBytes）
	slotSize int64
	size     int64 // 文件声明大小
	ready    int64 // 已连续就绪的前缀长度（watermark）
	readPos  int64 // 读者推进（只被 Read 推进，恒 ≤ ready）
	resetTo  int64 // 计数已复位的绝对槽边界（整槽越过 readPos 即复位）
	closed   bool
	err      error // 关闭原因；nil 表示成功关闭
}

// newWindowBuffer 构造窗口缓冲。winSize 由 streamWindowAlloc 提供（与
// Open 的内存预算记账同一来源，保证申请与归还一致）。
func newWindowBuffer(size, winSize int64) *windowBuffer {
	w := &windowBuffer{
		buf:      make([]byte, winSize),
		written:  make([]int32, winSize/reorderSlotSize),
		winSize:  winSize,
		slotSize: reorderSlotSize,
		size:     size,
	}
	w.dataCond = sync.NewCond(&w.mu)
	w.spaceCond = sync.NewCond(&w.mu)
	return w
}

// streamWindowAlloc 返回给定文件大小的窗口分配量：封顶 streamWindowBytes，
// 小文件按槽位对齐收缩（本地小媒体测试场景），下限一个槽位。
func streamWindowAlloc(size int64) int64 {
	if size >= streamWindowBytes {
		return streamWindowBytes
	}
	alloc := (size + reorderSlotSize - 1) / reorderSlotSize * reorderSlotSize
	if alloc <= 0 {
		alloc = reorderSlotSize
	}
	return alloc
}

// WriteAt 实现 io.WriterAt（gotd Parallel 的写循环调用）。写区间末尾超出
// 窗口（readPos + winSize）时阻塞等待读者推进（背压）；已消费区的重叠
// 写入丢弃（Telegram 重取分片：内容一致，读端早已读走）。关闭后返回
// errConsumerClosed 终止下载循环。
func (w *windowBuffer) WriteAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if int64(len(p)) > w.winSize {
		return 0, errors.New("windowBuffer: write larger than window")
	}
	var start, end int64
	for {
		if w.closed {
			return 0, errConsumerClosed
		}
		if off < 0 || off > w.size || off+int64(len(p)) > w.size {
			return 0, errors.New("windowBuffer: write out of range")
		}
		start = max(off, w.readPos) // 已消费前缀的重叠部分丢弃
		end = off + int64(len(p))
		if start >= end {
			return len(p), nil
		}
		if end <= w.readPos+w.winSize {
			break // 窗口内有空间
		}
		w.spaceCond.Wait() // 醒来重验 closed 与窗口边界
	}
	// 拷贝与记账在同一临界区：环形放置（绝对偏移取模），空间条件
	//（end ≤ readPos+winSize）保证与未消费槽位永不同槽混叠
	src := p[start-off:]
	rel := start % w.winSize
	for remaining := end - start; remaining > 0; {
		c := min(remaining, w.winSize-rel)
		copy(w.buf[rel:rel+c], src[:c])
		src = src[c:]
		remaining -= c
		rel = (rel + c) % w.winSize
	}
	for s := start / w.slotSize; s <= (end-1)/w.slotSize; s++ {
		slotStart := s * w.slotSize
		slotEnd := min(slotStart+w.slotSize, w.size)
		n := min(end, slotEnd) - max(start, slotStart)
		idx := s % int64(len(w.written))
		// 防御性夹取：重复写只增计数，就绪判定用 ≥，溢出无意义
		if int64(w.written[idx]) < slotEnd-slotStart {
			w.written[idx] = int32(min(int64(w.written[idx])+n, slotEnd-slotStart))
		}
	}
	// 水位推进限定在窗口内（ready < readPos+winSize）：越过窗口的环形下标
	// 与未消费槽位混叠，会把未写入区域误判为就绪——必须等读者推进腾出空间
	for w.ready < w.size && w.ready < w.readPos+w.winSize {
		slot := w.ready / w.slotSize
		slotLen := min(w.slotSize, w.size-w.ready)
		if int64(w.written[slot%int64(len(w.written))]) < slotLen {
			break
		}
		w.ready += slotLen
	}
	w.dataCond.Broadcast()
	return len(p), nil
}

// Read 实现 io.Reader：顺序阻塞读就绪前缀内的字节（消费侧因此可以边下
// 边切）；读端推进后唤醒被背压的写者（释放窗口）。下载失败/消费方放弃
// 后返回关闭原因；成功关闭且读尽返回 io.EOF。
func (w *windowBuffer) Read(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		if w.err != nil {
			return 0, w.err
		}
		if w.readPos >= w.size {
			if w.closed {
				return 0, io.EOF
			}
			// 读尽即等关闭（下载 goroutine 在 Parallel 返回后必然关闭）
			w.dataCond.Wait()
			continue
		}
		n := min(int64(len(p)), w.ready-w.readPos)
		if n == 0 {
			if len(p) == 0 {
				return 0, nil
			}
			w.dataCond.Wait()
			continue
		}
		// 环形读出（可能与写入同样跨越缓冲末尾，分两段拷贝）
		rel := w.readPos % w.winSize
		dst := p
		for remaining := n; remaining > 0; {
			c := min(remaining, w.winSize-rel)
			copy(dst[:c], w.buf[rel:rel+c])
			dst = dst[c:]
			remaining -= c
			rel = (rel + c) % w.winSize
		}
		w.readPos += n
		// 整槽越过 readPos 即复位计数：该环形下标即将被后续窗口槽复用，
		// 残留计数会把未写满的新槽误判为就绪
		for s := w.resetTo / w.slotSize; (s+1)*w.slotSize <= w.readPos; s++ {
			w.written[s%int64(len(w.written))] = 0
		}
		w.resetTo = w.readPos
		w.spaceCond.Broadcast()
		return int(n), nil
	}
}

// CloseWithError 关闭缓冲。err 为 nil 表示下载成功结束——就绪字节不足声明
// 大小时升级为 errShortDownload，拒绝把截断数据当完整文件消费；成功关闭
// 后已缓冲数据仍可继续读出，读尽返回 EOF。幂等：首次关闭为准。双侧
// Broadcast：既唤醒等数据的读者，也唤醒等窗口空间的写者（防悬挂）。
func (w *windowBuffer) CloseWithError(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	if err == nil && w.ready < w.size {
		err = errShortDownload
	}
	if w.err == nil {
		w.err = err
	}
	w.dataCond.Broadcast()
	w.spaceCond.Broadcast()
}
