package delivery

import (
	"context"
	"time"
)

// withRateLimitRetry 执行 op；遇 Bot API 429 时按 RetryAfter 等待后最多重试一次。
// 仅用于无请求体（或可重放请求体）的轻量调用；媒体上传的数据源是单次消费的
// 流式管道，已开始传输后无法安全重试，故 SendMedia/SendAlbum 不适用。
// docs/reference/architecture.md §5 与 /§22 的轻量实现。
func (s *telegramSender) withRateLimitRetry(ctx context.Context, op func() error) error {
	err := op()
	tooMany, limited := asRateLimit(err)
	if !limited || tooMany.RetryAfter <= 0 {
		return err
	}

	timer := time.NewTimer(time.Duration(tooMany.RetryAfter+1) * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
		return err // 等待期间被取消：以原始限流错误返回
	}
	return op()
}
