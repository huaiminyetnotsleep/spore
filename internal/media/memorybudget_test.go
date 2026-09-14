package media

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/message"
)

// usageCache：TTL 内同目录复用缓存（usage 只查一次），过期后重查；
// 查询失败原样返回且不污染缓存。
func TestUsageCache(t *testing.T) {
	var (
		calls atomic.Int64
		now   atomic.Int64 // Unix 秒
	)
	now.Store(1000)
	cache := newUsageCache(func() time.Time { return time.Unix(now.Load(), 0) })
	usage := func(string) (int64, error) {
		calls.Add(1)
		return 42, nil
	}

	for i := 0; i < 3; i++ { // TTL 内多次获取：只查一次
		used, err := cache.get("/tmp", usage)
		if err != nil || used != 42 {
			t.Fatalf("缓存命中应返回 42，得到 %d err=%v", used, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("TTL 内应只查询一次，得到 %d", calls.Load())
	}

	now.Store(1000 + 31) // 越过 30s TTL：重查
	if _, err := cache.get("/tmp", usage); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("过期后应重查，得到 %d 次", calls.Load())
	}

	// 不同目录：互不命中
	if _, err := cache.get("/other", usage); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("不同目录应重查，得到 %d 次", calls.Load())
	}

	// 查询失败：返回错误且不缓存（下次仍重查）
	now.Store(1000 + 62) // 先让各目录缓存全部过期
	failing := func(string) (int64, error) {
		calls.Add(1)
		return 0, errors.New("stat failed")
	}
	if _, err := cache.get("/tmp", failing); err == nil {
		t.Fatal("查询失败应返回错误")
	}
	if _, err := cache.get("/tmp", usage); err != nil {
		t.Fatalf("失败结果不应污染缓存: %v", err)
	}
	if calls.Load() != 5 {
		t.Fatalf("失败不缓存应触发重查，得到 %d 次", calls.Load())
	}
}

// 基础记账：获取累计 held、额度耗尽拒绝、释放后恢复。
func TestBudgetGateAccounting(t *testing.T) {
	const budget = int64(1000)
	g := NewBudgetGate(func() int64 { return budget })

	if !g.TryAcquire(600) {
		t.Fatal("预算内应获取成功")
	}
	if !g.TryAcquire(400) {
		t.Fatal("恰好占满应获取成功")
	}
	if g.TryAcquire(1) {
		t.Fatal("额度耗尽后应拒绝")
	}
	if g.Held() != budget {
		t.Fatalf("held 应为 %d，得到 %d", budget, g.Held())
	}
	g.Release(400)
	if g.Held() != 600 {
		t.Fatalf("释放后 held 应为 600，得到 %d", g.Held())
	}
	if !g.TryAcquire(400) {
		t.Fatal("释放后应能再次获取")
	}
}

// 超额/重复归还：防御性钳制，held 不得为负。
func TestBudgetGateOverRelease(t *testing.T) {
	g := NewBudgetGate(func() int64 { return 100 })
	g.TryAcquire(50)
	g.Release(50)
	g.Release(50) // 重复归还
	g.Release(1)  // 超额归还
	if g.Held() != 0 {
		t.Fatalf("held 应钳制为 0，得到 %d", g.Held())
	}
	if !g.TryAcquire(100) {
		t.Fatal("归还后应能全额获取")
	}
}

// 非法入参防御：size <= 0 的获取一律拒绝、归无为无操作。
func TestBudgetGateInvalidSize(t *testing.T) {
	g := NewBudgetGate(func() int64 { return 100 })
	if g.TryAcquire(0) || g.TryAcquire(-1) {
		t.Fatal("size <= 0 应拒绝")
	}
	g.Release(0)
	g.Release(-5)
	if g.Held() != 0 {
		t.Fatalf("非法归还不应改变 held，得到 %d", g.Held())
	}
}

// 动态额度：热调小不影响在途占用（软上限），新获取按新额度竞争；
// limit <= 0 视为零额度全部拒绝。
func TestBudgetGateDynamicLimit(t *testing.T) {
	var limit atomic.Int64
	limit.Store(1000)
	g := NewBudgetGate(limit.Load)

	if !g.TryAcquire(800) {
		t.Fatal("初始额度内应获取成功")
	}
	limit.Store(500) // 热调小：在途 800 超过新额度，但不得被回收
	if g.TryAcquire(100) {
		t.Fatal("调小后新获取应被拒绝（在途已超新额度）")
	}
	if g.Held() != 800 {
		t.Fatalf("在途占用不应受热调小影响，得到 %d", g.Held())
	}
	g.Release(800)
	if !g.TryAcquire(500) {
		t.Fatal("释放后应能按新额度获取")
	}
	limit.Store(0)
	g.Release(500)
	if g.TryAcquire(1) {
		t.Fatal("零额度应拒绝一切获取")
	}
}

// 并发压力：多 goroutine 随机大小获取/归还，最终余额归零且任一时刻
// 成功持有的总量不超过额度（-race 下运行）。
func TestBudgetGateConcurrentStress(t *testing.T) {
	const budget = int64(1 << 20)
	var limit atomic.Int64
	limit.Store(budget)
	g := NewBudgetGate(limit.Load)

	const workers, iters = 16, 200
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			size := int64(1 << (10 + w%9)) // 1KB–256KB 不等
			for i := 0; i < iters; i++ {
				if g.TryAcquire(size) {
					if held := g.Held(); held > budget {
						t.Errorf("held %d 超过预算 %d", held, budget)
						return
					}
					g.Release(size)
				}
			}
		}(w)
	}
	wg.Wait()
	if g.Held() != 0 {
		t.Fatalf("压力结束后 held 应为 0，得到 %d", g.Held())
	}
}

// 预算不足：内存区间内的媒体自动降级临时文件路径，预算不被占用；
// Cleanup 删除降级产生的临时文件。
func TestOpenMemoryBudgetDowngrade(t *testing.T) {
	payload := make([]byte, reorderSlotSize+3)
	tmp := t.TempDir()
	g := NewBudgetGate(func() int64 { return 1 << 10 }) // 1KB，必然不足
	opt := Options{
		TmpDir:          tmp,
		MaxFileSize:     1 << 30,
		StreamLimit:     64 << 10,
		MemoryLimit:     1 << 30,
		DownloadThreads: 4,
		Memory:          g,
	}
	m := message.Media{
		Kind:     message.KindDocument,
		Size:     int64(len(payload)),
		FileName: "downgrade.bin",
		Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2},
	}
	h, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), m, "d1", opt, testLogger(), nil)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	if g.Held() != 0 {
		t.Fatalf("降级路径不应占用预算，得到 %d", g.Held())
	}
	entries, _ := os.ReadDir(tmp)
	if len(entries) != 1 {
		t.Fatalf("降级应产生 1 个临时文件，得到 %d", len(entries))
	}
	got, err := readHandleTimeout(t, h)
	h.Cleanup()
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("内容不一致（读出 %d 字节）", len(got))
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Fatalf("Cleanup 后临时文件应删除，残留 %d 个", len(entries))
	}
}

// 预算耗尽→释放→恢复：第一个文件占满预算走内存路径；Cleanup（含重复调用）
// 恰好归还一次，后续文件回到内存路径。
func TestOpenMemoryBudgetReleaseOnCleanup(t *testing.T) {
	payload := make([]byte, reorderSlotSize+3)
	newOpt := func(tmp string, g *BudgetGate) Options {
		return Options{
			TmpDir:          tmp,
			MaxFileSize:     1 << 30,
			StreamLimit:     64 << 10,
			MemoryLimit:     1 << 30,
			DownloadThreads: 4,
			Memory:          g,
		}
	}
	newMedia := func(name string) message.Media {
		return message.Media{
			Kind:     message.KindDocument,
			Size:     int64(len(payload)),
			FileName: name,
			Location: &tg.InputDocumentFileLocation{ID: 1, AccessHash: 2},
		}
	}

	tmp := t.TempDir()
	g := NewBudgetGate(func() int64 { return int64(len(payload)) }) // 恰好容纳一个文件

	h1, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), newMedia("a.bin"), "r1", newOpt(tmp, g), testLogger(), nil)
	if err != nil {
		t.Fatalf("第一次打开失败: %v", err)
	}
	if g.Held() != int64(len(payload)) {
		t.Fatalf("内存路径应占用预算 %d，得到 %d", len(payload), g.Held())
	}

	// 预算被占满：第二个文件降级
	h2, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), newMedia("b.bin"), "r2", newOpt(tmp, g), testLogger(), nil)
	if err != nil {
		t.Fatalf("第二次打开失败: %v", err)
	}
	if g.Held() != int64(len(payload)) {
		t.Fatalf("降级不应改变预算占用，得到 %d", g.Held())
	}

	// Cleanup 幂等：重复调用只归还一次
	h1.Cleanup()
	h1.Cleanup()
	if g.Held() != 0 {
		t.Fatalf("Cleanup 后预算应归还，得到 %d", g.Held())
	}

	// 释放后恢复内存路径：读出内容一致且临时目录零写入
	h3, err := Open(context.Background(), tg.NewClient(chunkInvoker{payload}), newMedia("c.bin"), "r3", newOpt(tmp, g), testLogger(), nil)
	if err != nil {
		t.Fatalf("第三次打开失败: %v", err)
	}
	defer h3.Cleanup()
	if g.Held() != int64(len(payload)) {
		t.Fatalf("恢复后应重新占用预算，得到 %d", g.Held())
	}
	got, err := readHandleTimeout(t, h3)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if string(got) != string(payload) {
		t.Fatalf("内容不一致（读出 %d 字节）", len(got))
	}

	// 清理降级句柄，验证其临时文件被删除
	h2.Cleanup()
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Fatalf("降级句柄 Cleanup 后临时文件应删除，残留 %d 个", len(entries))
	}
}
