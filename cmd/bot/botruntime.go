// 池内 bot 的暂停/恢复运行时控制：每个 bot 一条监督 goroutine（MTProto
// ready 生命周期内启动），按 settings 的暂停集合决定是否拉起轮询。暂停 =
// 取消该 bot 的轮询 ctx（立即停止接收新消息；已受理任务由原 bot 正常完成，
// 发送通道保留在池中）；恢复 = 广播信号，监督循环重新拉起轮询。暂停状态
// 持久化在 settings（web.LoadPausedBots/SetBotPaused），重启与重连后保持。
package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/huaiminyetnotsleep/spore/internal/botpool"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/web"
)

// botRuntime 聚合暂停/恢复的运行时状态；实现 web.BotRuntimeControl。
type botRuntime struct {
	pool *botpool.Pool
	st   *store.Store
	log  *slog.Logger

	mu       sync.Mutex
	cancels  map[int64]context.CancelFunc // botID → 当前轮询取消函数
	enableCh chan struct{}                // 恢复信号广播（close 旧通道并换新）
}

func newBotRuntime(pool *botpool.Pool, st *store.Store, log *slog.Logger) *botRuntime {
	return &botRuntime{
		pool:     pool,
		st:       st,
		log:      log,
		cancels:  map[int64]context.CancelFunc{},
		enableCh: make(chan struct{}),
	}
}

// RunPoller 单 bot 轮询监督循环：未暂停时拉起长轮询并阻塞；被暂停（取消）
// 或恢复（信号）时循环处理，直到生命周期 ctx 结束。库的轮询循环只在 ctx
// 取消时返回，故"生命周期仍在而 Start 返回"即为本控制器主动暂停。
func (r *botRuntime) RunPoller(ctx context.Context, m *botpool.Member) {
	for {
		if web.LoadPausedBots(ctx, r.st)[m.ID] {
			r.log.Info("机器人已暂停，暂不轮询（可在管理端恢复）", "bot_id", m.ID)
			select {
			case <-ctx.Done():
				return
			case <-r.enableChan():
			}
			continue
		}
		pollCtx, cancel := context.WithCancel(ctx)
		r.mu.Lock()
		r.cancels[m.ID] = cancel
		r.mu.Unlock()
		// 注册取消函数后复核暂停态：关闭"检查通过 → 注册前被暂停"的竞态窗口
		//（命中时立即取消，Start 同步返回，落入下方暂停等待）
		if web.LoadPausedBots(ctx, r.st)[m.ID] {
			cancel()
		} else {
			m.BotAPI.Start(pollCtx)
		}
		r.mu.Lock()
		delete(r.cancels, m.ID)
		r.mu.Unlock()
		cancel()
		if ctx.Err() != nil {
			return
		}
		r.log.Info("机器人已暂停，轮询已停止（在途任务由本 bot 正常完成）", "bot_id", m.ID)
		select {
		case <-ctx.Done():
			return
		case <-r.enableChan():
		}
	}
}

// PauseBot 暂停指定 bot：取消其轮询 ctx（立即停止接收新消息）。bot 当前未
// 在轮询（尚未接入/已暂停）时为无害 no-op——持久化由 handler 负责。
func (r *botRuntime) PauseBot(_ context.Context, botID int64) error {
	r.mu.Lock()
	cancel := r.cancels[botID]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// ResumeBot 恢复指定 bot：广播信号让监督循环重新拉起轮询。
func (r *botRuntime) ResumeBot(_ context.Context, _ int64) error {
	r.mu.Lock()
	close(r.enableCh)
	r.enableCh = make(chan struct{})
	r.mu.Unlock()
	return nil
}

// enableChan 返回当前恢复信号通道（等待方持快照，避免持锁阻塞）。
func (r *botRuntime) enableChan() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.enableCh
}
