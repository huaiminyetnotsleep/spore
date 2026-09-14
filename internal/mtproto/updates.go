// 账号 update 的轻量消费：只提取 update 批次里携带的频道对象（含
// access_hash），供"被拉入频道后实时归档"等按需场景。不建 updates 状态
// 管理、不做 getDifference——漏收的 update 由 joinmgr 的惰性对账兜底。
package mtproto

import (
	"context"
	"sync"

	"github.com/gotd/td/tg"
)

// ChannelUpdateBridge 是生命周期感知的 update 分发桥（仿 MembershipBridge）：
// gotd 客户端构造期需要一个不变的 telegram.UpdateHandler，而处理能力依赖
// ready 作用域内才创建的 Fetcher/joinmgr，故经 Bind/Unbind 转发。每轮重连
// 重新绑定，未绑定时静默丢弃。
type ChannelUpdateBridge struct {
	mu      sync.RWMutex
	process func(ctx context.Context, channels []tg.Channel)
}

// NewChannelUpdateBridge 创建未绑定的分发桥。
func NewChannelUpdateBridge() *ChannelUpdateBridge { return &ChannelUpdateBridge{} }

// Bind 绑定处理目标（ready 作用域内调用）。fn 在 gotd 读循环内同步执行，
// 只允许做内存级工作（缓存过滤、起 goroutine），禁止网络调用。
func (b *ChannelUpdateBridge) Bind(fn func(ctx context.Context, channels []tg.Channel)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.process = fn
}

// Unbind 解绑处理目标（ready 退出时调用），此后 update 静默丢弃。
func (b *ChannelUpdateBridge) Unbind() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.process = nil
}

// Handle 实现 telegram.UpdateHandler：提取批次中的频道对象并转发；未绑定
// 或批次不含频道对象时丢弃。恒返回 nil——update 消费失败不阻断连接。
func (b *ChannelUpdateBridge) Handle(ctx context.Context, u tg.UpdatesClass) error {
	b.mu.RLock()
	fn := b.process
	b.mu.RUnlock()
	if fn == nil || u == nil {
		return nil
	}
	channels := channelsOfUpdates(u)
	if len(channels) == 0 {
		return nil
	}
	fn(ctx, channels)
	return nil
}

// channelsOfUpdates 从 update 批次携带的 Chats 里提取频道对象（值拷贝）。
// 被拉入/新消息等频道相关 update 都会附带完整 Channel 对象（含 access_hash）；
// Short 形态与 UpdatesTooLong 不携带，忽略（由惰性对账兜底）。
func channelsOfUpdates(u tg.UpdatesClass) []tg.Channel {
	var chats []tg.ChatClass
	switch v := u.(type) {
	case *tg.Updates:
		chats = v.Chats
	case *tg.UpdatesCombined:
		chats = v.Chats
	}
	var out []tg.Channel
	for _, c := range chats {
		if ch, ok := c.(*tg.Channel); ok && ch != nil {
			out = append(out, *ch)
		}
	}
	return out
}
