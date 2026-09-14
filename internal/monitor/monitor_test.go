package monitor

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type testTransfers struct{ downloaded, uploaded int64 }

func (t *testTransfers) TransferTotals() (int64, int64) { return t.downloaded, t.uploaded }

func TestServiceSampleComputesRatesAndKeepsWindow(t *testing.T) {
	transfers := &testTransfers{}
	s := New(Options{
		Transfers:      transfers,
		RSSProbe:       func() (int64, error) { return 100, nil },
		TempDirProbe:   func(string) (int64, error) { return 200, nil },
		RealtimeWindow: time.Minute,
	})
	t0 := time.UnixMilli(1000)
	s.sample(t0)
	points := s.Realtime(time.Time{}, time.Time{})
	if len(points) != 1 || points[0].DownloadBytesPerSecond != nil {
		t.Fatalf("首次采样不应有速率: %+v", points)
	}
	transfers.downloaded, transfers.uploaded = 1000, 500
	s.sample(t0.Add(2 * time.Second))
	points = s.Realtime(time.Time{}, time.Time{})
	if len(points) != 2 || points[1].DownloadBytesPerSecond == nil || *points[1].DownloadBytesPerSecond != 500 {
		t.Fatalf("下载速率不对: %+v", points)
	}
	if points[1].UploadBytesPerSecond == nil || *points[1].UploadBytesPerSecond != 250 {
		t.Fatalf("上传速率不对: %+v", points)
	}
}

func TestScanTempDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("123"), 0600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "b"), []byte("12345"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := scanTempDir(dir)
	if err != nil || got != 8 {
		t.Fatalf("目录大小 = %d, err=%v", got, err)
	}
}

// CPU 占用率采样：首点只建基线不出点；相邻两点按 ΔCPU/(Δwall×核心数) 归一；
// 探针失败不出点也不更新基线，恢复后按跨失败窗口的两点差分继续。
func TestServiceSampleCPUPercent(t *testing.T) {
	var (
		cpu      float64
		probeErr error
	)
	s := New(Options{
		CPUProbe:       func() (float64, error) { return cpu, probeErr },
		RSSProbe:       func() (int64, error) { return 100, nil },
		RealtimeWindow: time.Minute,
	})
	cores := float64(runtime.NumCPU())

	t0 := time.UnixMilli(1000)
	s.sample(t0) // 首点：只建基线
	points := s.Realtime(time.Time{}, time.Time{})
	if len(points) != 1 || points[0].CPUPercent != nil {
		t.Fatalf("首点不应有 CPU 占用率: %+v", points)
	}

	// 2 秒壁钟内累计 1 核秒：占用率 = 1/2/核心数 × 100
	cpu = 1.0
	s.sample(t0.Add(2 * time.Second))
	points = s.Realtime(time.Time{}, time.Time{})
	want := 1.0 / 2.0 / cores * 100
	if points[1].CPUPercent == nil || *points[1].CPUPercent != want {
		t.Fatalf("CPU 占用率应为 %v，得到 %v", want, points[1].CPUPercent)
	}

	// 探针失败：不出点、基线不动
	probeErr = errors.New("probe failed")
	cpu = 5.0
	s.sample(t0.Add(4 * time.Second))
	points = s.Realtime(time.Time{}, time.Time{})
	if points[2].CPUPercent != nil {
		t.Fatalf("探针失败不应出 CPU 点: %+v", points[2])
	}

	// 恢复：按跨失败窗口差分（基线仍停在 t0+2s、cpu=1.0；当前 cpu=5.0，
	// ΔCPU=4 核秒，Δwall=4s）
	probeErr = nil
	s.sample(t0.Add(6 * time.Second))
	points = s.Realtime(time.Time{}, time.Time{})
	want = (5.0 - 1.0) / 4.0 / cores * 100
	if points[3].CPUPercent == nil || *points[3].CPUPercent != want {
		t.Fatalf("恢复后 CPU 占用率应为 %v，得到 %v", want, points[3].CPUPercent)
	}
}

// rollup 聚合：CPU 占用率按窗口内均值进历史采样。
func TestAccumulatorCPUAveraging(t *testing.T) {
	var a accumulator
	a.add(Point{CPUPercent: float64Ptr(50)})
	a.add(Point{}) // duration=2，CPU 合计 50
	sample, ok := a.sample(1)
	if !ok {
		t.Fatal("应有聚合采样")
	}
	if sample.CPUPercent == nil || *sample.CPUPercent != 25 {
		t.Fatalf("CPU 均值应为 25，得到 %v", sample.CPUPercent)
	}
}
