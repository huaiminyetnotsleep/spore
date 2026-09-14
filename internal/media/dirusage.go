package media

import (
	"sync"
	"time"
)

// dirUsageTTL 是临时目录占用统计的缓存时长：checkTempDir 在每次下载前调用
// （相册逐成员触发），全树遍历的成本在相册场景成倍放大。预检本就是
// best-effort（fail-open），≤TTL 的占用漂移不改变其"把磁盘写满提前为明确
// 拒绝"的定位——真正的兜底仍是写盘失败。
const dirUsageTTL = 30 * time.Second

// usageCache 按目录缓存最近一次占用统计，TTL 内复用。
// 查询失败不缓存（保持调用方 fail-open 语义）；now 可注入以便确定性测试。
type usageCache struct {
	mu   sync.Mutex
	dir  string
	used int64
	at   time.Time
	now  func() time.Time
}

func newUsageCache(now func() time.Time) *usageCache {
	return &usageCache{now: now}
}

// get 返回 dir 的占用字节数：同目录且未过期时直接复用缓存，否则经 usage
// 现查并回填。查询失败原样返回错误且不污染缓存。
func (c *usageCache) get(dir string, usage func(string) (int64, error)) (int64, error) {
	now := c.now()
	c.mu.Lock()
	if c.dir == dir && now.Sub(c.at) < dirUsageTTL {
		used := c.used
		c.mu.Unlock()
		return used, nil
	}
	c.mu.Unlock()

	used, err := usage(dir)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.dir, c.used, c.at = dir, used, now
	c.mu.Unlock()
	return used, nil
}

// globalUsageCache 是下载预检共用的进程级缓存（键为临时目录路径）。
var globalUsageCache = newUsageCache(time.Now)
