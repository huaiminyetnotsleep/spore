// Package queue 提供内存 Job 队列与 worker。
package queue

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// Job 一次提取任务。
type Job struct {
	ID          string           // 唯一标识（纯数字纳秒时间戳——media 孤儿清理依赖该格式）
	ChatID      int64            // 目标私聊
	UserID      int64            // 发起人
	Ref         tmeurl.SourceRef // 链接解析结果
	StatusMsgID int              // "正在获取消息..." 提示消息 ID，完成后删除
	RequestID   int64            // 关联的 requests 行 ID；0 表示无持久化记录（仅测试构造）
	// CloudDest 非空表示云盘下载任务（/download 指令）：媒体上传到该名称
	// 的网盘目的地而不重发回 Telegram。空值 = 现有 TG 投递路径（零值兼容，
	// 存量路径行为不变）。
	CloudDest string
	// DumpOnly 表示仅缓存补写任务（管理端"转存缓存频道"）：fetch 源消息后
	// 直接向缓存频道发送干净副本并落 dump_entries，全程不向用户发送任何
	// 消息。false = 现有路径（零值兼容）。
	DumpOnly bool
	// BotID 是受理 bot（多机器人池归属）：worker 据此解析发送通道（状态
	// 提示编辑、媒体投递、频道副本都发给受理 bot 对应的私聊）。0 = 存量
	// 任务/Web 补存，回退主 bot。
	BotID int64
	// NeedsStatusPrompt 表示任务入队时没有占位提示（受控重试重建的任务：
	// 原占位已随上次失败收尾删除，进程中断场景则遗留为冻结的旧进度）。
	// worker 认领成功后补发一条占位提示，重试执行才在 Bot 里有实时进度
	// 展示，终态删除/取消文案也随之落在新占位上。仅缓存补写任务不设置
	// （全程不打扰用户，与首次执行一致）。
	NeedsStatusPrompt bool
}

// shutdownWindow 是进程退出后收尾动作（用户通知、状态删除、终态落库）
// 共享的尽力而为时间窗：独立于取消信号，但限时防止挂死退出流程。
const shutdownWindow = 10 * time.Second

// Processor 处理单个任务的具体逻辑。
type Processor func(ctx context.Context, job Job)

// Queue 固定容量内存队列；满载时拒绝入队（由 handler 直接向用户提示繁忙）。
//
// 双通道优先级：云盘任务（CloudDest 非空）走低优先级通道，其余任务（TG
// 投递、/pin、缓存补写）走高优先级通道。高优先级通道非空时低优先级任务
// 不出队——云盘大任务与 TG 投递并发会竞争临时目录预算（TEMP_DIR_MAX_SIZE
// 预检直接拒绝），且云盘交付天然不急。不做运行中任务抢占：云盘任务开始
// 后跑完为止，WORKER_COUNT=1（默认）下任务永不并发。
type Queue struct {
	hi chan Job
	lo chan Job

	// takeMu 保证"hi 出队"与"lo 出队"互斥：acquire 在锁内先排空 hi 再取
	// lo，Enqueue 的 hi 入队与 lo 出队因此不会交错出"hi 非空却取到 lo"
	// 的时序（纯 select 多通道就绪时由 Go 随机选择分支，做不到严格优先）。
	takeMu sync.Mutex
	// notify 是入队信号（缓冲 1）：acquire 在双通道皆空后阻塞等待该信号
	// 再重查。缓冲 1 合并连续入队信号；多唤醒无副作用（醒来重查为空再睡）。
	notify chan struct{}

	activeMu sync.Mutex
	active   map[int64]context.CancelCauseFunc
	pending  map[int64]Job // 已入队未出队的任务索引（按 RequestID），供取消时即时清理

	pendingCancel PendingCancelHandler // 可选：取消命中排队任务时的即时清理回调
}

// PendingCancelHandler 处理"取消命中时尚未出队"的任务：worker 的 claim 拦截
// 要等出队才触发，队列繁忙时占位提示可能滞留分钟级，装配层借此立即清理
// （通常删除占位提示）。与出队路径的 deleteStatusBestEffort 幂等叠加，
// 故每条任务至多触发一次（命中即从索引移除）。
type PendingCancelHandler func(job Job)

var ErrBusy = errors.New("queue is full")

// ErrRequestCancelled 是管理端请求取消所使用的 context cause。
// 进程根 context 的取消不会使用该原因，worker 可据此保留 INTERRUPTED 语义。
var ErrRequestCancelled = errors.New("request cancelled")

// RequestCanceller 是按请求 ID取消活动 worker 的最小接口。
type RequestCanceller interface {
	CancelRequest(requestID int64) bool
}

// New 创建容量 capacity 的队列（高、低优先级通道各 capacity）。
func New(capacity int) *Queue {
	if capacity < 0 {
		capacity = 0
	}
	return &Queue{
		hi:      make(chan Job, capacity),
		lo:      make(chan Job, capacity),
		notify:  make(chan struct{}, 1),
		active:  make(map[int64]context.CancelCauseFunc),
		pending: make(map[int64]Job),
	}
}

// route 按任务形态选择通道：仅云盘任务低优先，其余（TG 投递、/pin、
// 缓存补写）高优先。缓存补写不降级的理由：副本越早写入缓存频道，后续
// 同链接 TG 请求越早命中零下载复用（reuse.go），降级反而推迟缓存变热。
func (q *Queue) route(j Job) chan Job {
	if j.CloudDest != "" {
		return q.lo
	}
	return q.hi
}

// SetPendingCancelHandler 注册排队任务取消回调；重复注册以最后一次为准
// （MTProto 每轮重连会以新 Sender 重新装配）。传 nil 解除注册。
func (q *Queue) SetPendingCancelHandler(fn PendingCancelHandler) {
	q.activeMu.Lock()
	q.pendingCancel = fn
	q.activeMu.Unlock()
}

// Enqueue 入队；对应优先级通道满返回 ErrBusy。先登记 pending 索引再入
// 通道，保证取消不会漏掉刚入队的任务；入队失败时回滚登记。成功后发入队
// 信号唤醒阻塞中的 acquire（缓冲 1，合并连续信号）。
func (q *Queue) Enqueue(j Job) error {
	ch := q.route(j)
	if j.RequestID != 0 {
		q.activeMu.Lock()
		q.pending[j.RequestID] = j
		q.activeMu.Unlock()
	}
	select {
	case ch <- j:
		q.signal()
		return nil
	default:
		if j.RequestID != 0 {
			q.activeMu.Lock()
			delete(q.pending, j.RequestID)
			q.activeMu.Unlock()
		}
		return ErrBusy
	}
}

// signal 发放入队信号：缓冲已满时丢弃——待消费的信号本身代表"有新任务"，
// 等待者醒来后会重查双通道，不会丢任务。
func (q *Queue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// FullFor 报告对应优先级通道是否已满，供 handler 在发送占位提示前预检，
// 避免饱和期间对每条被拒消息多付两次 Bot API 调用。cloud=true 检查低
// 优先级通道（云盘任务），false 检查高优先级通道。
func (q *Queue) FullFor(cloud bool) bool {
	if cloud {
		return len(q.lo) >= cap(q.lo)
	}
	return len(q.hi) >= cap(q.hi)
}

// Len 返回当前排队任务数（Web 总览页队列指标；双通道合计）。
func (q *Queue) Len() int { return len(q.hi) + len(q.lo) }

// Cap 返回单通道容量（进程生命周期内固定；与容量配置语义一致）。
func (q *Queue) Cap() int { return cap(q.hi) }

// NewJob 以纳秒时间戳生成任务 ID 的便捷构造。
// requestID 是 access.Submit 落库的 requests 行 ID，worker 据此回写阶段与终态。
func NewJob(userID, chatID int64, ref tmeurl.SourceRef, statusMsgID int, requestID int64) Job {
	return Job{
		ID:          strconv.FormatInt(time.Now().UnixNano(), 10),
		ChatID:      chatID,
		UserID:      userID,
		Ref:         ref,
		StatusMsgID: statusMsgID,
		RequestID:   requestID,
	}
}

// RegisterRequest 注册活动请求的取消函数。requestID 为 0 时忽略，便于测试任务
// 与无持久化任务复用同一队列。
func (q *Queue) RegisterRequest(requestID int64, cancel context.CancelCauseFunc) {
	if requestID == 0 || cancel == nil {
		return
	}
	q.activeMu.Lock()
	q.active[requestID] = cancel
	q.activeMu.Unlock()
}

// UnregisterRequest 移除活动请求的取消函数；requestID 不存在时无操作。
func (q *Queue) UnregisterRequest(requestID int64) {
	if requestID == 0 {
		return
	}
	q.activeMu.Lock()
	delete(q.active, requestID)
	q.activeMu.Unlock()
}

// Register 是 RegisterRequest 的兼容别名。
func (q *Queue) Register(requestID int64, cancel context.CancelCauseFunc) {
	q.RegisterRequest(requestID, cancel)
}

// Unregister 是 UnregisterRequest 的兼容别名。
func (q *Queue) Unregister(requestID int64) { q.UnregisterRequest(requestID) }

// CancelRequest 向活动请求传递管理员取消原因。返回 true 表示命中活动 worker。
// 排队中的任务返回 false：数据库状态与出队 claim 仍会阻止其执行；若已注册
// PendingCancelHandler 则同时触发即时清理，占位提示不必等出队后才删除。
func (q *Queue) CancelRequest(requestID int64) bool {
	if requestID == 0 {
		return false
	}
	q.activeMu.Lock()
	cancel, active := q.active[requestID]
	var (
		job     Job
		handler PendingCancelHandler
	)
	if !active {
		if j, pending := q.pending[requestID]; pending {
			// 命中即移除：钩子至多触发一次，重复取消直接落空
			delete(q.pending, requestID)
			job = j
			handler = q.pendingCancel
		}
	}
	q.activeMu.Unlock()
	if active {
		cancel(ErrRequestCancelled)
		return true
	}
	if handler != nil {
		handler(job)
	}
	return false
}

// Cancel 是 CancelRequest 的简短别名，供调用方按请求 ID 发出取消信号。
func (q *Queue) Cancel(requestID int64) bool { return q.CancelRequest(requestID) }

// IsRequestCancelled 判断 context 是否携带管理端请求取消原因。
func IsRequestCancelled(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), ErrRequestCancelled)
}

// Run 启动 workers 个消费循环，阻塞直到 ctx 取消。
// 退出前对仍排队、未来得及处理的任务逐个调用 discard（如通知用户并清理状态提示），
// 避免"正在获取消息..."提示永久滞留。
func (q *Queue) Run(ctx context.Context, workers int, process, discard Processor) {
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for {
				job, ok := q.acquire(ctx)
				if !ok {
					return
				}
				// 出队即移出 pending 索引：此后取消走活动 worker 信号
				if job.RequestID != 0 {
					q.activeMu.Lock()
					delete(q.pending, job.RequestID)
					q.activeMu.Unlock()
				}
				if ctx.Err() != nil {
					// 取消与出队的竞态胜出：该任务按"遗留任务"处理，
					// 用独立窗口通知用户并清理状态提示（否则两者皆无，占位永久滞留）
					dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWindow)
					discard(dctx, job)
					cancel()
					continue
				}
				jobCtx, cancel := context.WithCancelCause(ctx)
				if job.RequestID != 0 {
					q.RegisterRequest(job.RequestID, cancel)
				}
				process(jobCtx, job)
				if job.RequestID != 0 {
					q.UnregisterRequest(job.RequestID)
				}
				cancel(nil)
			}
		}()
	}
	wg.Wait()

	// ctx 已结束，Bot API 调用需要独立于取消信号的短窗口（尽力而为）
	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWindow)
	defer cancel()
	for {
		job, ok := q.tryTake()
		if !ok {
			return
		}
		discard(drainCtx, job)
	}
}

// acquire 返回下一个要执行的任务；ctx 结束返回 false。
// 先非阻塞排空双通道（锁内先 hi 后 lo，保证 hi 非空时不取 lo），皆空后
// 阻塞等待入队信号再重查。
func (q *Queue) acquire(ctx context.Context) (Job, bool) {
	for {
		if job, ok := q.tryTake(); ok {
			return job, true
		}
		select {
		case <-ctx.Done():
			return Job{}, false
		case <-q.notify:
		}
	}
}

// tryTake 在互斥内尝试出队一个任务：先 hi 后 lo；双通道皆空返回 false。
func (q *Queue) tryTake() (Job, bool) {
	q.takeMu.Lock()
	defer q.takeMu.Unlock()
	select {
	case job := <-q.hi:
		return job, true
	default:
	}
	select {
	case job := <-q.lo:
		return job, true
	default:
		return Job{}, false
	}
}
