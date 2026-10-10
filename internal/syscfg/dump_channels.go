// 缓存频道（dump channel）列表配置：settings 表 dump_channels 键的单一来源。
// 每项含独立 enabled 开关：开启的频道在成功任务后各写一份干净副本（扇出），
// 复用/补写/预热在全部启用频道范围内查命中；关闭的频道不读不写（历史
// dump_entries 行保留，重新开启即恢复命中）。
//
// 兼容旧单频道部署：dump_channels 键缺失时按旧键折算——dump_channel_id
// 存在（含显式 0=关闭，覆盖环境变量）折算为单条启用/空列表；键也缺失时
// 回退环境变量 DUMP_CHANNEL_ID（web 频道设置页旧版维护的正是这两个键）。
// dump_channels 键一旦写入（含空列表）即完全接管，旧键仅作Bootstrap 回退
// 不再回写；首次经 Web 修改列表即完成迁移，存量部署零手工迁移。
package syscfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// DumpChannel 是一个缓存频道：解析校验后的数字 ID 与标题快照 + 启用开关。
// 投递只按数字 ID；标题仅展示用（快照存于配置与监听事件行）。
type DumpChannel struct {
	ChannelID int64  `json:"channel_id"`
	Title     string `json:"title"`
	Enabled   bool   `json:"enabled"`
}

// dumpChannelsUpper 是缓存频道数量上限。
const dumpChannelsUpper = 10

// settings 键。keyDumpChannelID 为旧单频道键（仅作折算回退读取，不再回写）；
// KeyDumpChannelTitle 与旧版 web 频道设置页共用（折算项的标题来源）。
const (
	KeyDumpChannels  = "dump_channels"
	keyDumpChannelID = "dump_channel_id"
)

// DumpChannelsUpper 暴露数量上限（Web 校验与文案共用）。
func DumpChannelsUpper() int { return dumpChannelsUpper }

// loadDumpChannels 读取 dump_channels 键原始配置：found 区分「键存在（含空
// 列表，接管生效）」与「键缺失（走旧键/env 折算）」。读出后按 ID 去重防御
// 历史脏数据。
func loadDumpChannels(ctx context.Context, st *store.Store) ([]DumpChannel, bool) {
	if st == nil {
		return nil, false
	}
	v, ok, err := st.GetSetting(ctx, KeyDumpChannels)
	if err != nil || !ok {
		return nil, false
	}
	var out []DumpChannel
	if json.Unmarshal([]byte(v), &out) != nil {
		return nil, false
	}
	clean := make([]DumpChannel, 0, len(out))
	seen := make(map[int64]bool, len(out))
	for _, c := range out {
		if c.ChannelID == 0 || seen[c.ChannelID] {
			continue
		}
		seen[c.ChannelID] = true
		clean = append(clean, c)
	}
	return clean, true
}

// LoadDumpChannels 读取缓存频道列表配置（缺失/非法返回 nil）。
func LoadDumpChannels(ctx context.Context, st *store.Store) []DumpChannel {
	list, _ := loadDumpChannels(ctx, st)
	return list
}

// LoadEffectiveDumpChannels 读取缓存频道列表的运行时有效值（含旧键折算）：
// dump_channels 键存在（含空列表=显式关闭）直接生效；缺失时旧键
// dump_channel_id 存在则折算（0=空列表，非 0=单条启用项，标题取
// dump_channel_title 快照）；旧键也缺失/非法时回退环境变量传入值
// （与旧 LoadEffectiveDumpChannelID 的「显式 0 覆盖 env」语义一致）。
func LoadEffectiveDumpChannels(ctx context.Context, st *store.Store, envChannelID int64) []DumpChannel {
	if list, ok := loadDumpChannels(ctx, st); ok {
		return list
	}
	if st != nil {
		if v, ok, err := st.GetSetting(ctx, keyDumpChannelID); err == nil && ok {
			var id int64
			if json.Unmarshal([]byte(v), &id) == nil {
				if id != 0 {
					return []DumpChannel{{ChannelID: id, Title: LoadDumpChannelTitle(ctx, st), Enabled: true}}
				}
				// 显式 0（Web 端「清除配置」的产物）：关闭，覆盖环境变量
				return []DumpChannel{}
			}
			// 非法值：继续环境变量兜底（与旧 loadJSONSetting 行为一致）
		}
	}
	if envChannelID != 0 {
		return []DumpChannel{{ChannelID: envChannelID, Enabled: true}}
	}
	return nil
}

// EnabledDumpChannelIDs 取列表中启用频道的数字 ID（保持配置顺序）。
func EnabledDumpChannelIDs(channels []DumpChannel) []int64 {
	out := make([]int64, 0, len(channels))
	for _, c := range channels {
		if c.Enabled {
			out = append(out, c.ChannelID)
		}
	}
	return out
}

// SaveDumpChannels 校验并整体写入缓存频道列表；写前先校验保证不落非法值
// （数量上限、按 ID 去重）。空列表合法（清空 = 复用关闭）。
func SaveDumpChannels(ctx context.Context, st *store.Store, channels []DumpChannel) error {
	if st == nil {
		return errors.New("syscfg: Store 为必填项")
	}
	if len(channels) > dumpChannelsUpper {
		return fmt.Errorf("缓存频道最多 %d 个。", dumpChannelsUpper)
	}
	clean := make([]DumpChannel, 0, len(channels))
	seen := make(map[int64]bool, len(channels))
	for _, c := range channels {
		if c.ChannelID == 0 || seen[c.ChannelID] {
			continue
		}
		seen[c.ChannelID] = true
		clean = append(clean, c)
	}
	return setSettingJSON(ctx, st, KeyDumpChannels, clean)
}
