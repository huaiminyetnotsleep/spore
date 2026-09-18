package notify

import "context"

// recordStreak 记录一种连续失败来源，并统一处理成功恢复和达到阈值后的告警。
// 调用方负责传入对应计数器与阈值；计数器只能在 Hub 的 countMu 保护下访问。
// detail 为最近一次失败的上下文（随告警 payload 展示，成功时忽略）。
func (h *Hub) recordStreak(ctx context.Context, succeeded bool, count *int, threshold int, key, detail string) {
	h.countMu.Lock()
	if succeeded {
		*count = 0
		h.countMu.Unlock()
		h.Recover(ctx, key)
		return
	}
	*count++
	raise := *count >= threshold
	current := *count
	h.countMu.Unlock()
	if raise {
		h.Raise(ctx, key, SeverityError, StreakData{Count: current, Threshold: threshold, Detail: detail})
	}
}
