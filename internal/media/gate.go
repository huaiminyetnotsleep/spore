package media

import (
	"errors"
	"io"
	"os"
	"sync"
)

// fileGate 是临时文件路径"边下边发"的核心组件：下载 goroutine 经
// io.WriterAt 把 gotd Parallel 的分片乱序落盘，上传侧经 io.Reader 顺序
// 阻塞读——就绪前缀（连续完整落盘的前缀水位）内的字节立即可读，下载
// 落盘与上传读盘完全重叠，不再"先下完再传"。与 reorderBuffer 同构的
// 关闭语义，区别在于数据在磁盘不在内存。
//
//   - 写侧（io.WriterAt）：gotd Parallel 下载的块按完成序乱序落位；
//   - 读侧（io.Reader）：顺序阻塞读——所需前缀未就绪时等待，就绪前缀
//     推进即被唤醒（与 reorderBuffer 同构的背压，生产端是磁盘写入）；
//   - CloseWithError：下载 goroutine 收尾（err 为 nil 表示成功；短读升级
//     为错误）或消费方放弃（Cleanup）时调用，唤醒全部阻塞读者；关闭后
//     写入返回错误，配合取消的下载 ctx 让下载 goroutine 尽快终止。
//
// 槽位记账只保证"计数过的字节必然已完成文件写入"：文件写入成功后才在
// 锁内记账，读者因此读到的字节必然已落盘。同一区域重复写（Telegram 重取
// 分片）只增计数，就绪判定用 ≥，内容以最后一次写入为准。
type fileGate struct {
	f  *os.File
	on func(int64) // 分片落位后的进度回调（下载观测，可为 nil）

	mu       sync.Mutex
	cond     *sync.Cond // 挂在 mu 上：就绪前缀推进或关闭时 Broadcast
	written  []int32    // 每槽累计落位字节数（判满用）
	slotSize int64
	size     int64
	ready    int64 // 已连续就绪的前缀长度（watermark）
	readPos  int64
	closed   bool
	err      error // 关闭原因；nil 表示成功关闭
}

func newFileGate(f *os.File, size int64, on func(int64)) *fileGate {
	g := &fileGate{
		f:        f,
		on:       on,
		written:  make([]int32, (size+downloadPartSize-1)/downloadPartSize),
		slotSize: downloadPartSize,
		size:     size,
	}
	g.cond = sync.NewCond(&g.mu)
	return g
}

// WriteAt 实现 io.WriterAt（gotd Parallel 的写循环调用）。文件写入在锁外
// 完成（pwrite 天然并发安全，避免磁盘 IO 阻塞其他分片记账）；进度回调在
// WriteAt 返回前同步执行，保证"EOF 即全部回调已发生"的 happens-before
// 链（与内存路径的 countWriterAt 一致）。
func (g *fileGate) WriteAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	g.mu.Lock()
	closed := g.closed
	g.mu.Unlock()
	if closed {
		// 消费方已放弃：返回错误让写循环终止下载，而不是等任务超时
		return 0, errConsumerClosed
	}
	if off < 0 || off > g.size || off+int64(len(p)) > g.size {
		return 0, errors.New("fileGate: write out of range")
	}
	n, err := g.f.WriteAt(p, off)
	if n > 0 {
		g.account(off, n)
	}
	return n, err
}

// account 在锁内累计槽位字节并推进就绪前缀（唤醒等待的读者），锁外执行
// 进度回调。仅在文件写入成功返回后调用。
func (g *fileGate) account(off int64, n int) {
	end := off + int64(n)
	g.mu.Lock()
	for s := off / g.slotSize; s <= (end-1)/g.slotSize; s++ {
		slotStart := s * g.slotSize
		slotEnd := min(slotStart+g.slotSize, g.size)
		m := min(end, slotEnd) - max(off, slotStart)
		// 防御性夹取：重复写只增计数，就绪判定用 ≥，溢出无意义
		if g.written[s] < int32(slotEnd-slotStart) {
			g.written[s] = min(g.written[s]+int32(m), int32(slotEnd-slotStart))
		}
	}
	for g.ready < g.size {
		slot := g.ready / g.slotSize
		slotLen := min(g.slotSize, g.size-g.ready)
		if int64(g.written[slot]) < slotLen {
			break
		}
		g.ready += slotLen
	}
	g.cond.Broadcast()
	g.mu.Unlock()
	if g.on != nil {
		g.on(int64(n))
	}
}

// Read 实现 io.Reader：顺序阻塞读就绪前缀内的字节（上传侧因此可以边下边发）。
// 下载失败/消费方放弃后返回关闭原因；成功关闭且读尽返回 io.EOF。
func (g *fileGate) Read(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		if g.err != nil {
			return 0, g.err
		}
		if g.readPos >= g.size {
			if g.closed {
				return 0, io.EOF
			}
			// 尺寸精确时读尽即等关闭（gotd bigLoop 读到 EOF 才收尾）；
			// 下载 goroutine 在 Parallel 返回后必然关闭，不会悬挂
			g.cond.Wait()
			continue
		}
		n := min(int64(len(p)), g.ready-g.readPos)
		if n == 0 {
			if len(p) == 0 {
				return 0, nil
			}
			g.cond.Wait()
			continue
		}
		off := g.readPos
		g.readPos += n // 只被读方推进，锁外读取安全
		// 文件读取在锁外完成（pread 与并发 pwrite 互不干扰），避免磁盘 IO
		// 阻塞下载侧记账；读取范围限于就绪前缀，字节必然已落盘
		g.mu.Unlock()
		m, err := g.f.ReadAt(p[:n], off)
		g.mu.Lock()
		if err != nil {
			if errors.Is(err, io.EOF) {
				// 就绪前缀内的字节必然已写入，短读说明文件被外部截断（异常路径）
				err = io.ErrUnexpectedEOF
			}
			return m, err
		}
		return m, nil
	}
}

// waitClosed 等待门控关闭并返回关闭原因（下载成功结束为 nil，含短读
// 升级的 errShortDownload）。拆分投递在切分前经 Handle.WaitDownloaded
// 间接调用；ctx 取消经下载侧传播——下载 goroutine 收到取消即以错误关闭，
// 本方法随之返回，无需独立监听 ctx。
func (g *fileGate) waitClosed() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for !g.closed {
		g.cond.Wait()
	}
	return g.err
}

// CloseWithError 关闭门控。err 为 nil 表示下载成功结束——就绪字节不足声明
// 大小时升级为 errShortDownload，拒绝把截断数据当完整文件上传；成功关闭后
// 已落盘的就绪数据仍可继续读出，读尽返回 EOF。幂等：首次关闭为准。
func (g *fileGate) CloseWithError(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true
	if err == nil && g.ready < g.size {
		err = errShortDownload
	}
	if g.err == nil {
		g.err = err
	}
	g.cond.Broadcast()
}
