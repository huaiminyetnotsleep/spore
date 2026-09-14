package media

import (
	"errors"
	"io"
	"sync"
)

// errShortDownload 标记下载"成功结束"但落位字节少于声明大小：size 元数据
// 不准或传输异常，宁可整体失败也不把截断的数据当完整文件上传。
var errShortDownload = errors.New("download completed short of declared size")

// reorderSlotSize 是重排序缓冲的就绪判定粒度，与下载分片 downloadPartSize
// （upload.getFile 单请求上限 1MB）同宽对齐——每片写入恰好落在一个槽位内，
// 避免跨槽记账开销。取值只影响读者唤醒粒度，不影响正确性——槽位按累计
// 字节数判满，任意写入模式都安全。
const reorderSlotSize = downloadPartSize

// reorderBuffer 是"管道化内存流式"大文件路径的核心组件：同一块预分配内存
// 同时充当下载写入端与上传读取端，使下载与上传完全重叠（边下边发）。
//
//   - 写侧（io.WriterAt）：gotd Parallel 下载的块按完成序乱序落位；
//   - 读侧（io.Reader）：顺序阻塞读——所需前缀未就绪时等待，就绪前缀推进
//     即被唤醒（与 io.Pipe 同构的背压，方向相反：这里生产端更慢）；
//   - CloseWithError：下载 goroutine 收尾（err 为 nil 表示成功；短读升级为
//     错误）或消费方放弃（Cleanup）时调用，唤醒全部阻塞读者；关闭后写入
//     返回错误，让仍在跑的下载循环尽快终止。
//
// 槽位计数不假设写入对齐或互不重叠：同一区域重复写（Telegram 重取分片）
// 只会让计数超过槽长，就绪判定用 ≥ 即可，内容以最后一次写入为准。
type reorderBuffer struct {
	mu       sync.Mutex
	cond     *sync.Cond // 挂在 mu 上：就绪前缀推进或关闭时 Broadcast
	data     []byte     // 预分配，长度 = 声明大小
	written  []int32    // 每槽累计写入字节数（判满用）
	slotSize int64
	size     int64
	ready    int64 // 已连续就绪的前缀长度（watermark）
	readPos  int64
	closed   bool
	err      error // 关闭原因；nil 表示成功关闭
}

func newReorderBuffer(size int64) *reorderBuffer {
	b := &reorderBuffer{
		data:     make([]byte, size),
		written:  make([]int32, (size+reorderSlotSize-1)/reorderSlotSize),
		slotSize: reorderSlotSize,
		size:     size,
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// WriteAt 实现 io.WriterAt（gotd Parallel 的写循环调用）。
func (b *reorderBuffer) WriteAt(p []byte, off int64) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		// 消费方已放弃：返回错误让写循环终止下载，而不是等任务超时
		return 0, errConsumerClosed
	}
	if off < 0 || off > b.size || off+int64(len(p)) > b.size {
		return 0, errors.New("reorderBuffer: write out of range")
	}
	if len(p) == 0 {
		return 0, nil
	}
	end := off + int64(len(p))
	copy(b.data[off:end], p)
	for s := off / b.slotSize; s <= (end-1)/b.slotSize; s++ {
		slotStart := s * b.slotSize
		slotEnd := min(slotStart+b.slotSize, b.size)
		n := min(end, slotEnd) - max(off, slotStart)
		// 防御性夹取：重复写只增计数，就绪判定用 ≥，溢出无意义
		if b.written[s] < int32(slotEnd-slotStart) {
			b.written[s] = min(b.written[s]+int32(n), int32(slotEnd-slotStart))
		}
	}
	// 就绪前缀推进后唤醒等待的读者
	for b.ready < b.size {
		slot := b.ready / b.slotSize
		slotLen := min(b.slotSize, b.size-b.ready)
		if int64(b.written[slot]) < slotLen {
			break
		}
		b.ready += slotLen
	}
	b.cond.Broadcast()
	return len(p), nil
}

// Read 实现 io.Reader：顺序阻塞读，未就绪即等待（上传侧因此可以边下边发）。
// 下载失败/消费方放弃后返回关闭原因；成功关闭且读尽返回 io.EOF。
func (b *reorderBuffer) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		if b.err != nil {
			return 0, b.err
		}
		if b.readPos >= b.size {
			if b.closed {
				return 0, io.EOF
			}
			// 尺寸精确时读尽即等关闭（gotd bigLoop 读到 EOF 才收尾）；
			// 下载 goroutine 在 Parallel 返回后必然关闭，不会悬挂
			b.cond.Wait()
			continue
		}
		n := min(int64(len(p)), b.ready-b.readPos)
		if n == 0 {
			if len(p) == 0 {
				return 0, nil
			}
			b.cond.Wait()
			continue
		}
		copy(p, b.data[b.readPos:b.readPos+n])
		b.readPos += n
		return int(n), nil
	}
}

// CloseWithError 关闭缓冲。err 为 nil 表示下载成功结束——就绪字节不足声明
// 大小时升级为 errShortDownload，拒绝把截断数据当完整文件上传；成功关闭后
// 已缓冲数据仍可继续读出，读尽返回 EOF。
func (b *reorderBuffer) CloseWithError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	if err == nil && b.ready < b.size {
		err = errShortDownload
	}
	if b.err == nil {
		b.err = err
	}
	b.cond.Broadcast()
}
