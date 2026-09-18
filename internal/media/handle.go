// Package media 处理媒体下载策略：小文件直通流式上传，中文件走内存
// 重排序缓冲（下载与上传重叠的"管道化内存流式"），大文件落临时文件并经
// 就绪水位门控与上传重叠，超限直接拒绝（docs/reference/architecture.md §3.4）。
package media

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// downloadErrorCode 把下载路径的底层错误细分为可定位的错误码：
// file reference 失效 → FILE_REFERENCE_INVALID（cause 保留，worker 经
// IsFileReferenceExpired 结构化判断后仍会刷新重试）；网络传输故障 →
// NETWORK_ERROR；其余保持 MEDIA_DOWNLOAD_FAILED 兜底。
func downloadErrorCode(err error) apperr.Code {
	switch {
	case tgerr.Is(err, "FILE_REFERENCE_EXPIRED", "PERSISTENT_FILE_REFERENCE_INVALID"):
		return apperr.CodeFileReferenceInvalid
	case apperr.IsTransportFailure(err):
		return apperr.CodeNetworkError
	default:
		return apperr.CodeMediaDownloadFailed
	}
}

// Handle 表示一份准备就绪的媒体数据。
// Cleanup 在消费完成或放弃时必须调用：
//   - 临时文件路径：删除文件并关闭句柄；
//   - 流式路径：关闭管道让仍在写入的下载 goroutine 立即结束
//     （否则它会阻塞在 Pipe 写端直到任务超时）。
//
// 文件名不在此重复存放——发送侧取自 message.Media.FileName。
type Handle struct {
	Reader  io.Reader
	Cleanup func()
}

// downloadPartSize 是下载分片大小：upload.getFile 单请求上限 1MB（4KB 对齐），
// 三种下载路径共用（gotd downloader 默认 512KB，调大后请求次数减半、RTT
// 开销减半）；reorderBuffer 槽位与 fileGate 水位槽同宽对齐。
const downloadPartSize = 1 << 20

// Options 下载策略参数（源自 config.Config）。
type Options struct {
	TmpDir          string
	MaxFileSize     int64
	StreamLimit     int64
	MemoryLimit     int64                           // 内存重排序缓冲上限（字节）；<=0 表示不启用该路径
	DownloadThreads int                             // 并行下载线程数；<1 按 1 处理（gotd WithThreads 同语义）
	MaxDirSize      int64                           // 临时目录总量上限（字节）；<=0 表示不限、跳过检查
	DirUsage        func(dir string) (int64, error) // 目录占用查询（测试注入用）；nil 回落真实 dirSize
	// Memory 是内存管道的进程级预算闸门（可选）：进入内存路径前按文件大小
	// 记账，预算不足自动降级临时文件路径，使常驻 RAM 被额度封顶而不随并发
	// 任务数放大。nil 表示不启用限制（历史行为）。
	Memory MemoryBudget
	// FFmpegPath 是视频封面兜底抽帧用的 ffmpeg 可执行文件路径（FFMPEG_PATH，
	// 默认按 PATH 查找 "ffmpeg"）；空串表示关闭抽帧兜底（测试用）。
	FFmpegPath string
}

// Open 按大小选择下载路径并返回句柄。
//
//   - Size > MaxFileSize：拒绝（不发起下载）
//   - Size <= StreamLimit：goroutine 中流式写入 io.Pipe，与 Bot API 上传背压串联
//   - StreamLimit < Size <= MemoryLimit：多线程并行下载到内存重排序缓冲
//     （reorderBuffer），上传侧顺序阻塞读——下载与上传完全重叠、零磁盘写入
//   - 其余：落到 TempName(jobID, 文件名) 多线程并行落盘，经 fileGate 就绪
//     水位门控顺序读——下载落盘与上传读盘重叠，由 Cleanup 清理
//
// 内存路径在 MemoryLimit 之外还受进程级预算闸门（Options.Memory）约束：
// 预算不足时同区间媒体自动降级临时文件路径（非阻塞），使常驻 RAM 被额度
// 封顶而不随并发任务数线性放大；预算在 Handle.Cleanup 时归还。
//
// onDownload 在每段字节落位时收到增量字节数（下载进度观测，可为 nil）：
// 内存管道路径下分片乱序落位，计数代表"已落位字节"而非顺序前缀。
func Open(ctx context.Context, api *tg.Client, m message.Media, jobID string, opt Options, log *slog.Logger, onDownload func(int64)) (*Handle, error) {
	if m.Size > opt.MaxFileSize {
		return nil, apperr.New(apperr.CodeFileTooLarge,
			fmt.Sprintf("size=%d > max_file_size=%d", m.Size, opt.MaxFileSize))
	}
	if m.Location == nil {
		// 防御：无下载位置的媒体（如转换失败的兜底类型）不应进入下载，
		// 否则 nil 接口编码进请求会 panic
		return nil, apperr.New(apperr.CodeMediaUnsupported, "media has no location")
	}

	dl := downloader.NewDownloader().WithPartSize(downloadPartSize)

	if m.Size <= opt.StreamLimit {
		log.Debug("媒体走流式路径", "file", m.FileName, "size", m.Size)
		pr, pw := io.Pipe()
		go func() {
			_, err := dl.Download(api, m.Location).Stream(ctx, &countWriter{w: pw, on: onDownload})
			if err != nil {
				log.Debug("流式下载结束", "file", m.FileName, "error", err.Error())
				err = apperr.Wrap(downloadErrorCode(err), err)
			}
			_ = pw.CloseWithError(err) // 成功时为 nil，正常关闭
		}()
		// 消费方放弃（发送失败/相册整组失败）时关闭读端，
		// 释放阻塞在写端的下载 goroutine，而不是等 15 分钟任务超时
		return &Handle{
			Reader:  pr,
			Cleanup: func() { _ = pr.CloseWithError(errConsumerClosed) },
		}, nil
	}

	if opt.MemoryLimit > 0 && m.Size <= opt.MemoryLimit {
		if !tryAcquireMemory(opt.Memory, m.Size) {
			// 进程内存预算已满：降级到下方临时文件路径（边下边传语义不变），
			// 把常驻 RAM 钉死在预算内。Debug 级别——饱和期逐文件触发，Info
			// 会刷屏；落盘路径自带 Info 日志，这里补充"为什么"。
			log.Debug("内存预算不足，媒体降级临时文件路径",
				"file", m.FileName, "size", m.Size, "held", heldMemory(opt.Memory))
		} else {
			// 中大文件：内存重排序缓冲（管道化流式）——多线程并行下载乱序落位，
			// 上传顺序阻塞读，两阶段完全重叠；内存占用 = 文件大小（MemoryLimit
			// 门控 + 进程级预算闸门封顶）
			buf := newReorderBuffer(m.Size)
			var releaseOnce sync.Once
			go func() {
				_, err := dl.Download(api, m.Location).WithThreads(opt.DownloadThreads).Parallel(ctx, &countWriterAt{w: buf, on: onDownload})
				if err != nil {
					log.Debug("内存管道下载结束", "file", m.FileName, "error", err.Error())
					err = apperr.Wrap(downloadErrorCode(err), err)
				}
				buf.CloseWithError(err) // 成功时为 nil；短读在缓冲内升级为错误
			}()
			log.Info("大文件走内存管道路径", "file", m.FileName, "size", m.Size, "threads", opt.DownloadThreads)
			// 消费方放弃时关闭缓冲：阻塞的读者立即醒来，后续写入返回错误
			// 让下载 goroutine 尽快终止（与流式路径的 Pipe 语义同构）；
			// Cleanup 可重复调用，预算经 Once 保证与获取恰好配对一次
			return &Handle{
				Reader: buf,
				Cleanup: func() {
					releaseOnce.Do(func() { releaseMemory(opt.Memory, m.Size) })
					buf.CloseWithError(errConsumerClosed)
				},
			}, nil
		}
	}

	// 超出内存上限的大文件：临时文件 + 就绪水位门控（多线程并行落盘，
	// 上传侧顺序读就绪前缀——下载落盘与上传读盘重叠，不再"先下完再传"）
	if err := checkTempDir(opt, m.Size, log); err != nil {
		return nil, err
	}
	path := filepath.Join(opt.TmpDir, TempName(jobID, m.FileName))
	f, err := os.Create(path)
	if err != nil {
		_ = os.Remove(path)
		return nil, apperr.Wrap(apperr.CodeMediaDownloadFailed, err)
	}
	// 下载 ctx 独立可取消：消费方 Cleanup 时立即终止在途下载（与流式/内存
	// 管道的放弃语义同构），成功路径下 cancel 为无害 no-op；os.File 的
	// ReadAt（上传读）与 Parallel 的 WriteAt（下载写）经 pread/pwrite 并发安全
	dctx, cancel := context.WithCancel(ctx)
	gate := newFileGate(f, m.Size, onDownload)
	go func() {
		_, err := dl.Download(api, m.Location).WithThreads(opt.DownloadThreads).Parallel(dctx, gate)
		if err != nil {
			log.Debug("临时文件下载结束", "file", m.FileName, "error", err.Error())
			err = apperr.Wrap(downloadErrorCode(err), err)
		}
		gate.CloseWithError(err) // 成功时为 nil；短读在门控内升级为错误
	}()
	log.Info("大文件走临时文件路径（下载与上传重叠）", "file", m.FileName, "size", m.Size, "threads", opt.DownloadThreads)
	cleanup := func() {
		gate.CloseWithError(errConsumerClosed) // 唤醒阻塞读者、终止后续写入
		cancel()
		f.Close() // 与在途 pwrite 并发安全（os.File 内部有关闭守卫）
		if rmErr := os.Remove(path); rmErr != nil && !os.IsNotExist(rmErr) {
			log.Warn("临时文件清理失败", "path", path, "error", rmErr.Error())
		}
	}
	return &Handle{Reader: gate, Cleanup: cleanup}, nil
}

// countWriter 在每次成功写入后按增量字节数回调（流式路径下载观测）。
type countWriter struct {
	w  io.Writer
	on func(int64)
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 && c.on != nil {
		c.on(int64(n))
	}
	return n, err
}

// countWriterAt 在每次成功落位后按增量字节数回调（Parallel 分片写入观测，
// 覆盖内存重排序缓冲与临时文件两条多线程路径）。
type countWriterAt struct {
	w  io.WriterAt
	on func(int64)
}

func (c *countWriterAt) WriteAt(p []byte, off int64) (int, error) {
	n, err := c.w.WriteAt(p, off)
	if n > 0 && c.on != nil {
		c.on(int64(n))
	}
	return n, err
}

// checkTempDir 下载前检查临时目录占用：已用 + 即将落盘大小超过 MaxDirSize 时拒绝。
// MaxDirSize <= 0 表示未配置上限，跳过；占用统计经 globalUsageCache 做 30s TTL
// 缓存（相册逐成员触发预检，避免每次全树遍历）；目录查询失败按 fail-open
// 处理（跳过预检），交由后续 ToPath 自然失败——预检只是把"磁盘写满"提前为
// 明确拒绝。
func checkTempDir(opt Options, size int64, log *slog.Logger) error {
	if opt.MaxDirSize <= 0 {
		return nil
	}
	usage := opt.DirUsage
	if usage == nil {
		usage = dirSize
	}
	used, err := globalUsageCache.get(opt.TmpDir, usage)
	if err != nil {
		log.Warn("临时目录占用查询失败，跳过下载前检查", "dir", opt.TmpDir, "error", err.Error())
		return nil
	}
	if used+size > opt.MaxDirSize {
		return apperr.New(apperr.CodeTempDirFull,
			fmt.Sprintf("used=%d + size=%d > max_dir_size=%d", used, size, opt.MaxDirSize))
	}
	return nil
}

// dirSize 汇总目录树字节数（DirUsage 的默认实现）；目录不可读时返回错误，
// 由调用方决定跳过本轮检查。符号链接不跟随（临时目录内不存在）。
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // 竞态删除的临时文件：跳过不计
		}
		total += info.Size()
		return nil
	})
	return total, err
}
