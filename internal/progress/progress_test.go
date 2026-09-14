package progress

import (
	"sync"
	"testing"
)

func TestRegistryRegisterAndSnapshot(t *testing.T) {
	r := NewRegistry()
	r.Register(1)
	r.AddTotal(1, 100)
	r.AddTotal(1, 50) // 多媒体任务：总量累加
	r.AddDownloaded(1, 60)
	r.AddUploaded(1, 20)

	snap := r.Snapshot(1)
	if snap == nil {
		t.Fatal("已注册请求应返回快照")
	}
	if snap.TotalBytes != 150 || snap.DownloadedBytes != 60 || snap.UploadedBytes != 20 {
		t.Fatalf("快照数值不符: %+v", snap)
	}
}

func TestRegistryUnregisteredAndDone(t *testing.T) {
	r := NewRegistry()
	if snap := r.Snapshot(42); snap != nil {
		t.Fatalf("未注册请求应返回 nil，得到 %+v", snap)
	}
	// 未注册 ID 的写入是 no-op，不产生记录
	r.AddTotal(42, 10)
	r.AddDownloaded(42, 10)
	r.AddUploaded(42, 10)
	if snap := r.Snapshot(42); snap != nil {
		t.Fatalf("未注册请求写入后仍应返回 nil，得到 %+v", snap)
	}

	r.Register(7)
	r.AddDownloaded(7, 5)
	r.Done(7)
	if snap := r.Snapshot(7); snap != nil {
		t.Fatalf("Done 后应返回 nil，得到 %+v", snap)
	}
	r.Done(7) // 重复 Done 无副作用
}

func TestRegistryRegisterResets(t *testing.T) {
	r := NewRegistry()
	r.Register(3)
	r.AddDownloaded(3, 100)
	r.Register(3) // 任务重试：重新注册应清零
	snap := r.Snapshot(3)
	if snap == nil || snap.DownloadedBytes != 0 {
		t.Fatalf("重新注册应重置计数: %+v", snap)
	}
	if downloaded, uploaded := r.TransferTotals(); downloaded != 100 || uploaded != 0 {
		t.Fatalf("重新注册不应重置进程总量: downloaded=%d uploaded=%d", downloaded, uploaded)
	}
}

func TestRegistryTransferTotalsAreMonotonicAcrossDone(t *testing.T) {
	r := NewRegistry()
	r.Register(1)
	r.AddDownloaded(1, 10)
	r.AddUploaded(1, 20)
	r.Done(1)

	if downloaded, uploaded := r.TransferTotals(); downloaded != 10 || uploaded != 20 {
		t.Fatalf("Done 不应清除进程总量: downloaded=%d uploaded=%d", downloaded, uploaded)
	}

	// 未注册写入仍保持原有 no-op 语义，负增量也不能破坏单调总量。
	r.AddDownloaded(1, 50)
	r.Register(2)
	r.AddDownloaded(2, -1)
	r.AddUploaded(2, -2)
	if downloaded, uploaded := r.TransferTotals(); downloaded != 10 || uploaded != 20 {
		t.Fatalf("进程总量必须单调且只统计有效注册请求: downloaded=%d uploaded=%d", downloaded, uploaded)
	}
}

func TestRegistryNilReceiver(t *testing.T) {
	var r *Registry
	// nil 接收者安全：进度未接线时调用方无需判空
	r.Register(1)
	r.Done(1)
	r.AddTotal(1, 1)
	r.AddDownloaded(1, 1)
	r.AddUploaded(1, 1)
	if snap := r.Snapshot(1); snap != nil {
		t.Fatalf("nil Registry 应返回 nil，得到 %+v", snap)
	}
}

func TestRegistryConcurrentAccess(t *testing.T) {
	r := NewRegistry()
	r.Register(9)
	const goroutines = 8
	const increments = 100
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < increments; i++ {
				r.AddDownloaded(9, 1)
				r.AddUploaded(9, 2)
			}
		}()
	}
	wg.Wait()
	snap := r.Snapshot(9)
	if snap.DownloadedBytes != goroutines*increments {
		t.Fatalf("并发下载计数丢失: %d != %d", snap.DownloadedBytes, goroutines*increments)
	}
	if snap.UploadedBytes != 2*goroutines*increments {
		t.Fatalf("并发上传计数丢失: %d != %d", snap.UploadedBytes, 2*goroutines*increments)
	}
}
