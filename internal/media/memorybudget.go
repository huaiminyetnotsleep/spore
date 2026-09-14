package media

import "sync"

// MemoryBudget 是内存管道的进程级预算闸门：媒体进入内存重排序缓冲前按
// 声明大小记账，预算不足时由 Open 降级到临时文件路径（非阻塞，不排队），
// 使进程常驻 RAM 被固定额度封顶，不随并发任务数线性放大。
// nil 接口等价于"不启用限制"（历史行为），由包内 helper 统一判空。
type MemoryBudget interface {
	// TryAcquire 按文件大小申请预算：预算足够则记账并返回 true；不足立即
	// 返回 false（调用方降级，不等待）。size <= 0 一律拒绝（防御）。
	TryAcquire(size int64) bool
	// Release 归还一次成功 TryAcquire 的记账；与获取必须配对（media.Open
	// 以 sync.Once 保证恰好一次）。防御性钳制：重复或超额归还不产生负余额。
	Release(size int64)
}

// BudgetGate 是 MemoryBudget 的进程级实现：额度上限由 limit 闭包动态提供，
// 每次获取前在锁外读取一次——闭包可能读库（settings 热生效），不能持锁执行。
// 热调小额度不影响在途占用（软上限）：已记账的句柄照常存活至消费完成，
// 释放后新获取按新额度竞争。
type BudgetGate struct {
	limit func() int64

	mu   sync.Mutex
	held int64 // 当前在途记账字节数
}

// NewBudgetGate 构造预算闸门。limit 在每次 TryAcquire 时调用，应无阻塞副作用；
// 返回 <= 0 视为额度为零（内存路径全部降级）——装配侧已保证合法值有下界，
// 该分支仅为防御。
func NewBudgetGate(limit func() int64) *BudgetGate {
	return &BudgetGate{limit: limit}
}

// TryAcquire 实现 MemoryBudget。
func (g *BudgetGate) TryAcquire(size int64) bool {
	if size <= 0 {
		return false
	}
	budget := g.limit() // 可能触发 I/O（读 settings），保持在锁外
	g.mu.Lock()
	defer g.mu.Unlock()
	if budget <= 0 || g.held+size > budget {
		return false
	}
	g.held += size
	return true
}

// Release 实现 MemoryBudget。
func (g *BudgetGate) Release(size int64) {
	if size <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.held -= size
	if g.held < 0 {
		g.held = 0
	}
}

// Held 返回当前在途记账字节数（观测与测试用）。
func (g *BudgetGate) Held() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.held
}

// tryAcquireMemory 是 Open 的判空入口：未装配闸门（nil）等价于无限制放行。
func tryAcquireMemory(b MemoryBudget, size int64) bool {
	if b == nil {
		return true
	}
	return b.TryAcquire(size)
}

// releaseMemory 与 tryAcquireMemory 配对的判空归还。
func releaseMemory(b MemoryBudget, size int64) {
	if b == nil {
		return
	}
	b.Release(size)
}

// heldMemory 返回当前在途记账（nil 闸门为 0），仅供降级日志观测。
func heldMemory(b MemoryBudget) int64 {
	if g, ok := b.(*BudgetGate); ok && g != nil {
		return g.Held()
	}
	return 0
}
