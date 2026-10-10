// 监听转发频道配置：settings 表 watch_forward_channels 键的单一来源。
// 与缓存频道（dump_channel_*，web 频道设置页维护）相互独立：监听消息
// 始终预热缓存频道，转发频道是额外的镜像目标；未配置转发频道时监听
// 行为与仅有缓存频道时一致（缓存兜底）。
package syscfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// WatchForwardChannel 是一个监听转发频道：解析校验后的数字 ID 与标题
// 快照。投递只按数字 ID；标题仅展示用（快照存于配置与监听事件行）。
type WatchForwardChannel struct {
	ChannelID int64  `json:"channel_id"`
	Title     string `json:"title"`
}

// watchForwardChannelsUpper 是转发频道数量上限。
const watchForwardChannelsUpper = 10

// settings 键。KeyDumpChannelTitle 与 web 频道设置页的缓存频道标题键
// 同源（web 侧加载与保存委托本包）；监听事件落目标快照时一并读取。
const (
	keyWatchForwardChannels = "watch_forward_channels"
	KeyDumpChannelTitle     = "dump_channel_title"
)

// LoadWatchForwardChannels 读取监听转发频道列表（缺失/非法回退空）；
// 读出后按 ID 去重防御历史脏数据。
func LoadWatchForwardChannels(ctx context.Context, st *store.Store) []WatchForwardChannel {
	if st == nil {
		return nil
	}
	v, ok, err := st.GetSetting(ctx, keyWatchForwardChannels)
	if err != nil || !ok {
		return nil
	}
	var out []WatchForwardChannel
	if json.Unmarshal([]byte(v), &out) != nil {
		return nil
	}
	clean := make([]WatchForwardChannel, 0, len(out))
	seen := make(map[int64]bool, len(out))
	for _, c := range out {
		if c.ChannelID == 0 || seen[c.ChannelID] {
			continue
		}
		seen[c.ChannelID] = true
		clean = append(clean, c)
	}
	return clean
}

// SaveWatchForwardChannels 校验并整体写入转发频道列表；写前先校验保证
// 不落非法值（数量上限、按 ID 去重）。空列表合法（清空 = 仅缓存兜底）。
func SaveWatchForwardChannels(ctx context.Context, st *store.Store, channels []WatchForwardChannel) error {
	if st == nil {
		return errors.New("syscfg: Store 为必填项")
	}
	if len(channels) > watchForwardChannelsUpper {
		return fmt.Errorf("监听转发频道最多 %d 个。", watchForwardChannelsUpper)
	}
	clean := make([]WatchForwardChannel, 0, len(channels))
	seen := make(map[int64]bool, len(channels))
	for _, c := range channels {
		if c.ChannelID == 0 || seen[c.ChannelID] {
			continue
		}
		seen[c.ChannelID] = true
		clean = append(clean, c)
	}
	return setSettingJSON(ctx, st, keyWatchForwardChannels, clean)
}

// WatchForwardChannelsUpper 暴露数量上限（Web 校验与文案共用）。
func WatchForwardChannelsUpper() int { return watchForwardChannelsUpper }

// LoadDumpChannelTitle 读取缓存频道标题（展示用快照；未配置或读取失败
// 为空）。web 频道设置页的同名加载函数委托本实现。
func LoadDumpChannelTitle(ctx context.Context, st *store.Store) string {
	if st == nil {
		return ""
	}
	v, ok, err := st.GetSetting(ctx, KeyDumpChannelTitle)
	if err != nil || !ok {
		return ""
	}
	var title string
	if json.Unmarshal([]byte(v), &title) != nil {
		return ""
	}
	return title
}
