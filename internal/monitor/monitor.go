// Package monitor 采集进程资源、临时目录占用与传输速率，并提供实时窗口和历史查询。
package monitor

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

const (
	defaultSampleInterval  = 2 * time.Second
	defaultRealtimeWindow  = 15 * time.Minute
	defaultTempCache       = 10 * time.Second
	defaultPersistInterval = 30 * time.Second
	defaultCleanupInterval = time.Hour
	defaultRetention       = 48 * time.Hour
)

// MetricStore 是监控服务需要的最小历史存储接口。
type MetricStore interface {
	UpsertSystemMetricSample(context.Context, store.SystemMetricSample) error
	ListSystemMetricSamples(context.Context, int64, int64) ([]store.SystemMetricSample, error)
	ListSystemMetricBuckets(context.Context, int64, int64, int64) ([]store.SystemMetricSample, error)
	DeleteSystemMetricSamplesBefore(context.Context, int64) (int64, error)
}

// TransferTotals 提供进程生命周期内单调递增的传输总字节数。
type TransferTotals interface {
	TransferTotals() (downloaded, uploaded int64)
}

// Ticker 是可注入时钟创建的周期信号。
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

// Clock 提供监控循环需要的当前时间与 ticker。
type Clock interface {
	Now() time.Time
	NewTicker(time.Duration) Ticker
}

// Point 是一个实时或历史系统指标点；nil 字段表示探针不可用。
type Point struct {
	SampledAt              time.Time
	RSSBytes               *int64
	TempDirBytes           *int64
	DownloadBytesPerSecond *float64
	UploadBytesPerSecond   *float64
	CPUPercent             *float64 // 进程 CPU 占用率（0–100，占全部核心）
}

// Options 配置监控服务。持续时间为零时使用包默认值。
type Options struct {
	Store        MetricStore
	Transfers    TransferTotals
	TempDir      string
	Logger       *slog.Logger
	Clock        Clock
	RSSProbe     func() (int64, error)
	TempDirProbe func(string) (int64, error)
	// CPUProbe 返回进程累计 CPU 时间（user+sys 秒）；nil 回落 getrusage 探针。
	// 测试注入固定序列以做确定性断言。
	CPUProbe func() (float64, error)

	SampleInterval  time.Duration
	RealtimeWindow  time.Duration
	TempCache       time.Duration
	PersistInterval time.Duration
	CleanupInterval time.Duration
	Retention       time.Duration
}

// Service 周期采集指标；Run 由单个 goroutine 调用，查询方法可并发使用。
type Service struct {
	store     MetricStore
	transfers TransferTotals
	tempDir   string
	log       *slog.Logger
	clock     Clock
	rssProbe  func() (int64, error)
	tempProbe func(string) (int64, error)
	cpuProbe  func() (float64, error)

	sampleInterval  time.Duration
	realtimeWindow  time.Duration
	tempCache       time.Duration
	persistInterval time.Duration
	cleanupInterval time.Duration
	retention       time.Duration

	mu         sync.RWMutex
	points     []Point
	current    Point
	hasCurrent bool

	hasTransferBaseline bool
	lastDownloaded      int64
	lastUploaded        int64
	lastTransferAt      time.Time
	lastTempAt          time.Time
	lastTempBytes       *int64
	hasCPUBaseline      bool
	lastCPUSeconds      float64
	lastCPUAt           time.Time
	rollup              accumulator
}

// New 创建监控服务。
func New(options Options) *Service {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	if options.Clock == nil {
		options.Clock = realClock{}
	}
	if options.RSSProbe == nil {
		options.RSSProbe = processRSS
	}
	if options.TempDirProbe == nil {
		options.TempDirProbe = scanTempDir
	}
	if options.CPUProbe == nil {
		options.CPUProbe = processCPUSeconds
	}
	return &Service{
		store: options.Store, transfers: options.Transfers, tempDir: options.TempDir,
		log: options.Logger, clock: options.Clock, rssProbe: options.RSSProbe, tempProbe: options.TempDirProbe,
		cpuProbe:        options.CPUProbe,
		sampleInterval:  durationOr(options.SampleInterval, defaultSampleInterval),
		realtimeWindow:  durationOr(options.RealtimeWindow, defaultRealtimeWindow),
		tempCache:       durationOr(options.TempCache, defaultTempCache),
		persistInterval: durationOr(options.PersistInterval, defaultPersistInterval),
		cleanupInterval: durationOr(options.CleanupInterval, defaultCleanupInterval),
		retention:       durationOr(options.Retention, defaultRetention),
	}
}

// Run 立即采集一次，随后按采样周期运行，直到 ctx 取消。
// 探针、持久化和清理失败只记录警告，不终止监控循环。
func (s *Service) Run(ctx context.Context) {
	now := s.clock.Now()
	lastPersist, lastCleanup := now, now
	s.sample(now)

	ticker := s.clock.NewTicker(s.sampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case tickAt := <-ticker.C():
			now = tickAt
			if now.IsZero() {
				now = s.clock.Now()
			}
			s.sample(now)
			if now.Sub(lastPersist) >= s.persistInterval {
				s.persist(ctx, now)
				lastPersist = now
			}
			if now.Sub(lastCleanup) >= s.cleanupInterval {
				s.cleanup(ctx, now)
				lastCleanup = now
			}
		}
	}
}

// Current 返回最新实时点；尚未采样时 ok 为 false。
func (s *Service) Current() (point Point, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current, s.hasCurrent
}

// Realtime 返回内存窗口中 [since, until) 的点，按时间升序。
// since 或 until 为零时对应边界不限制。
func (s *Service) Realtime(since, until time.Time) []Point {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Point, 0, len(s.points))
	for _, point := range s.points {
		if !since.IsZero() && point.SampledAt.Before(since) {
			continue
		}
		if !until.IsZero() && !point.SampledAt.Before(until) {
			continue
		}
		out = append(out, point)
	}
	return out
}

// Historical 查询持久化的 [since, until) 历史点。bucket 为零时返回原始
// 30 秒 rollup；大于零时由存储层按 epoch 对齐分桶聚合。
func (s *Service) Historical(ctx context.Context, since, until time.Time, bucket time.Duration) ([]Point, error) {
	if s.store == nil {
		return nil, errors.New("monitor: metric store unavailable")
	}
	var (
		samples []store.SystemMetricSample
		err     error
	)
	if bucket > 0 {
		samples, err = s.store.ListSystemMetricBuckets(ctx, since.UnixMilli(), until.UnixMilli(), bucket.Milliseconds())
	} else {
		samples, err = s.store.ListSystemMetricSamples(ctx, since.UnixMilli(), until.UnixMilli())
	}
	if err != nil {
		return nil, err
	}
	out := make([]Point, 0, len(samples))
	for _, sample := range samples {
		out = append(out, pointFromStore(sample))
	}
	return out, nil
}

func (s *Service) sample(now time.Time) {
	point := Point{SampledAt: now}
	if value, err := s.rssProbe(); err != nil {
		s.log.Warn("读取进程 RSS 失败", "error", err)
	} else {
		point.RSSBytes = int64Ptr(value)
	}

	if s.tempDir != "" {
		if s.lastTempAt.IsZero() || now.Sub(s.lastTempAt) >= s.tempCache {
			s.lastTempAt = now
			value, err := s.tempProbe(s.tempDir)
			if err != nil {
				s.lastTempBytes = nil
				s.log.Warn("扫描临时目录失败", "error", err)
			} else {
				s.lastTempBytes = int64Ptr(value)
			}
		}
		point.TempDirBytes = cloneInt64(s.lastTempBytes)
	}

	if s.transfers != nil {
		downloaded, uploaded := s.transfers.TransferTotals()
		if s.hasTransferBaseline {
			seconds := now.Sub(s.lastTransferAt).Seconds()
			if seconds > 0 {
				point.DownloadBytesPerSecond = float64Ptr(float64(nonNegativeDelta(downloaded, s.lastDownloaded)) / seconds)
				point.UploadBytesPerSecond = float64Ptr(float64(nonNegativeDelta(uploaded, s.lastUploaded)) / seconds)
			}
		}
		s.hasTransferBaseline = true
		s.lastDownloaded, s.lastUploaded, s.lastTransferAt = downloaded, uploaded, now
	}

	// CPU 占用率：相邻两点差分累计 CPU 时间，除以壁钟时长与核心数归一到
	// 0–100；首点只建基线不出点（与传输速率同模式）。探针失败不更新基线，
	// 下次成功按两点差分（仍正确，只是跨度变长）。
	if seconds, err := s.cpuProbe(); err != nil {
		s.log.Warn("读取进程 CPU 失败", "error", err)
	} else {
		if s.hasCPUBaseline {
			elapsed := now.Sub(s.lastCPUAt).Seconds()
			if elapsed > 0 {
				used := seconds - s.lastCPUSeconds
				if used < 0 {
					used = 0 // 时钟回拨等异常按 0 计，避免出现负占用
				}
				percent := used / elapsed / float64(runtime.NumCPU()) * 100
				if percent > 100 {
					percent = 100
				}
				point.CPUPercent = float64Ptr(percent)
			}
		}
		s.hasCPUBaseline = true
		s.lastCPUSeconds, s.lastCPUAt = seconds, now
	}

	s.mu.Lock()
	s.current, s.hasCurrent = point, true
	s.points = append(s.points, point)
	cutoff := now.Add(-s.realtimeWindow)
	first := 0
	for first < len(s.points) && s.points[first].SampledAt.Before(cutoff) {
		first++
	}
	if first > 0 {
		s.points = append([]Point(nil), s.points[first:]...)
	}
	s.mu.Unlock()
	s.rollup.add(point)
}

func (s *Service) persist(ctx context.Context, now time.Time) {
	if s.store == nil {
		s.rollup.reset()
		return
	}
	sample, ok := s.rollup.sample(now.UnixMilli())
	s.rollup.reset()
	if !ok {
		return
	}
	if err := s.store.UpsertSystemMetricSample(ctx, sample); err != nil {
		s.log.Warn("持久化系统指标失败", "error", err)
	}
}

func (s *Service) cleanup(ctx context.Context, now time.Time) {
	if s.store == nil {
		return
	}
	if _, err := s.store.DeleteSystemMetricSamplesBefore(ctx, now.Add(-s.retention).UnixMilli()); err != nil {
		s.log.Warn("清理系统指标历史失败", "error", err)
	}
}

func pointFromStore(sample store.SystemMetricSample) Point {
	return Point{
		SampledAt: time.UnixMilli(sample.SampledAt), RSSBytes: sample.RSSBytes,
		TempDirBytes: sample.TempDirBytes, DownloadBytesPerSecond: sample.DownloadBytesPerSecond,
		UploadBytesPerSecond: sample.UploadBytesPerSecond, CPUPercent: sample.CPUPercent,
	}
}

func durationOr(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func nonNegativeDelta(current, previous int64) int64 {
	if current <= previous {
		return 0
	}
	return current - previous
}

func scanTempDir(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func int64Ptr(value int64) *int64       { return &value }
func float64Ptr(value float64) *float64 { return &value }
func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	return int64Ptr(*value)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTicker(interval time.Duration) Ticker {
	return realTicker{time.NewTicker(interval)}
}

type realTicker struct{ *time.Ticker }

func (ticker realTicker) C() <-chan time.Time { return ticker.Ticker.C }
