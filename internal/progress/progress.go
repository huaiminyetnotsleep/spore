// Package progress 维护处理中请求的实时传输进度（内存态，不落库）。
// 进度是瞬时状态：worker 在任务开始时注册、结束时清除，进程重启后自然清零
// （中断请求由启动恢复统一置 failed，无需持久化进度）。worker 写入、
// Web 管理端读取，二者在同一进程内共享同一 Registry 实例。
package progress

import (
	"io"
	"sync"
	"sync/atomic"
)

// Snapshot 是某请求当前传输进度的只读快照（字节数）。
// 下载与上传是重叠进行的两条独立管线，分别计数。
type Snapshot struct {
	TotalBytes      int64 // 本任务全部媒体的大小之和（相册为成员累加）
	DownloadedBytes int64
	UploadedBytes   int64
}

// tracker 是单个请求的进度计数；分片写入来自多个下载/上传 goroutine，
// 用原子计数避免写路径互斥。
type tracker struct {
	total      atomic.Int64
	downloaded atomic.Int64
	uploaded   atomic.Int64
}

// Registry 按请求 ID（requests 行 ID，Job.RequestID）聚合传输进度。
// 全部方法对 nil 接收者安全（进度未接线的部署形态下调用方无需判空）。
type Registry struct {
	mu              sync.RWMutex
	trackers        map[int64]*tracker
	totalDownloaded atomic.Int64
	totalUploaded   atomic.Int64
}

// NewRegistry 创建空进度注册表。
func NewRegistry() *Registry {
	return &Registry{trackers: make(map[int64]*tracker)}
}

// Register 为请求建立新的进度计数（重复 Register 重置为零值，对应任务重试）。
func (r *Registry) Register(id int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.trackers[id] = &tracker{}
	r.mu.Unlock()
}

// Done 移除请求的进度计数（任务收尾调用；未注册的 ID 无副作用）。
func (r *Registry) Done(id int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.trackers, id)
	r.mu.Unlock()
}

// AddTotal 累加任务媒体总量（逐成员调用，多媒体任务为大小之和）。
func (r *Registry) AddTotal(id int64, n int64) {
	if r == nil {
		return
	}
	r.mu.RLock()
	t := r.trackers[id]
	r.mu.RUnlock()
	if t != nil {
		t.total.Add(n)
	}
}

// AddDownloaded 累加已下载字节数（下载分片回调，可并发调用）。
func (r *Registry) AddDownloaded(id int64, n int64) {
	if r == nil {
		return
	}
	r.mu.RLock()
	t := r.trackers[id]
	r.mu.RUnlock()
	if t != nil {
		t.downloaded.Add(n)
		if n > 0 {
			r.totalDownloaded.Add(n)
		}
	}
}

// AddUploaded 累加已上传字节数（上传计数 reader，可并发调用）。
func (r *Registry) AddUploaded(id int64, n int64) {
	if r == nil {
		return
	}
	r.mu.RLock()
	t := r.trackers[id]
	r.mu.RUnlock()
	if t != nil {
		t.uploaded.Add(n)
		if n > 0 {
			r.totalUploaded.Add(n)
		}
	}
}

// TransferTotals 返回本进程生命周期内累计的下载与上传字节数。
// 计数单调递增，不受请求 Done 或重新 Register 影响；nil 接收者返回零值。
func (r *Registry) TransferTotals() (downloaded, uploaded int64) {
	if r == nil {
		return 0, 0
	}
	return r.totalDownloaded.Load(), r.totalUploaded.Load()
}

// Snapshot 读取请求进度快照；未注册返回 nil。
func (r *Registry) Snapshot(id int64) *Snapshot {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	t := r.trackers[id]
	r.mu.RUnlock()
	if t == nil {
		return nil
	}
	return &Snapshot{
		TotalBytes:      t.total.Load(),
		DownloadedBytes: t.downloaded.Load(),
		UploadedBytes:   t.uploaded.Load(),
	}
}

// CountingReader 包装单次消费的媒体数据源，把被上传方读走的字节数经 on
// 回调上报（读字节 ≈ 已上传字节，统一覆盖 Bot API multipart 与 MTProto
// 直传两条上传通道）。r 为 nil 时原样返回 nil。
func CountingReader(r io.Reader, on func(int64)) io.Reader {
	if r == nil {
		return nil
	}
	if on == nil {
		return r
	}
	return &countingReader{r: r, on: on}
}

type countingReader struct {
	r  io.Reader
	on func(int64)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.on(int64(n))
	}
	return n, err
}
