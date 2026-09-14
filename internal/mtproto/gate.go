package mtproto

import (
	"context"
	"sync"

	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

const maxTransferConnections = 16

// transferGate limits only file-transfer RPCs. The underlying gotd pools are
// fixed at maxTransferConnections and lazy; changing the runtime limit therefore
// affects new RPCs without interrupting in-flight requests.
type transferGate struct {
	invoker tg.Invoker
	limit   func() int
	mu      sync.Mutex
	cond    *sync.Cond // 挂在 mu 上：release 归还槽位时 Broadcast 唤醒等待者
	busy    int
}

func newTransferGate(invoker tg.Invoker, limit func() int) *transferGate {
	g := &transferGate{invoker: invoker, limit: limit}
	g.cond = sync.NewCond(&g.mu)
	return g
}

func (g *transferGate) Invoke(ctx context.Context, input bin.Encoder, output bin.Decoder) error {
	if _, ok := fileRouteKey(input); !ok {
		return g.invoker.Invoke(ctx, input, output)
	}
	if err := g.acquire(ctx); err != nil {
		return err
	}
	defer g.release()
	return g.invoker.Invoke(ctx, input, output)
}

// acquire 等待空闲传输槽位。等待采用 sync.Cond 睡眠通知而非轮询自旋：
// 饱和等待必然伴随其他传输在持续完成（每次完成都会 Broadcast），因此
// ctx 取消的感知延迟以"一次在途传输 RPC 完成"为上界——远小于退出 drain
// 窗口，也避免了高并发等待时每 50ms 的空转烧 CPU。
func (g *transferGate) acquire(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for {
		limit := g.limit()
		if limit < 1 {
			limit = 1
		}
		if limit > maxTransferConnections {
			limit = maxTransferConnections
		}
		if g.busy < limit {
			g.busy++
			return nil
		}
		g.cond.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// release 归还槽位并唤醒全部等待者重新竞争（广播在锁外，避免唤醒的
// 等待者立刻阻塞在 mu 上）。
func (g *transferGate) release() {
	g.mu.Lock()
	if g.busy > 0 {
		g.busy--
	}
	g.mu.Unlock()
	g.cond.Broadcast()
}
