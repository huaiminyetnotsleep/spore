package mtproto

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 并发上限：limit=2 时任意时刻在途 acquire 不超过 2（sync.Cond 等待语义）。
func TestTransferGateLimitsConcurrency(t *testing.T) {
	var limit atomic.Int64
	limit.Store(2)
	g := newTransferGate(nil, func() int { return int(limit.Load()) })

	var busy, peak atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if err := g.acquire(context.Background()); err != nil {
					t.Errorf("acquire 失败: %v", err)
					return
				}
				cur := busy.Add(1)
				for {
					p := peak.Load()
					if cur <= p || peak.CompareAndSwap(p, cur) {
						break
					}
				}
				time.Sleep(time.Millisecond)
				busy.Add(-1)
				g.release()
			}
		}()
	}
	wg.Wait()
	if peak.Load() > 2 {
		t.Fatalf("在途峰值 %d 超过限额 2", peak.Load())
	}
}

// 动态限额：调大后新获取立即成功，无需等待释放。
func TestTransferGateDynamicLimit(t *testing.T) {
	var limit atomic.Int64
	limit.Store(1)
	g := newTransferGate(nil, func() int { return int(limit.Load()) })

	if err := g.acquire(context.Background()); err != nil {
		t.Fatalf("首次获取失败: %v", err)
	}
	limit.Store(2) // 热调大：新获取按新限额立即放行
	done := make(chan error, 1)
	go func() { done <- g.acquire(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("调大限额后获取应成功: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("调大限额后获取仍被阻塞")
	}
}

// ctx 取消：等待中的 acquire 在下次释放（Broadcast）后被唤醒并返回 ctx.Err()。
// 与旧 50ms 轮询不同，唤醒依赖释放事件——饱和等待意味着释放在持续发生，
// 取消感知延迟以单次传输 RPC 为上界。
func TestTransferGateCancelWhileWaiting(t *testing.T) {
	g := newTransferGate(nil, func() int { return 1 })
	ctx, cancel := context.WithCancel(context.Background())

	// 占满唯一槽位；后续获取进入等待
	if err := g.acquire(context.Background()); err != nil {
		t.Fatalf("占位获取失败: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- g.acquire(ctx) }()

	// 等待者就位后取消，再释放槽位触发 Broadcast
	time.Sleep(50 * time.Millisecond)
	cancel()
	g.release()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("应返回 context.Canceled，得到 %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消后未在释放时唤醒")
	}
}
