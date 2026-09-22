// 频道加入（/join）配置：settings 表 join_* 键的单一来源。
// 键名、缺省值与校验只在本包定义，joinmgr / botapi / web 共用同一实现。
package syscfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// JoinConfig 是频道加入相关的全部配置项。
type JoinConfig struct {
	Enabled           bool // 允许加入频道（总开关；false 时 /join 直接拒绝）
	AutoLeaveExternal bool // 自动退出外部拉入的频道（惰性检测，独立于总开关；默认关）
	RequireApproval   bool // 普通用户提交需号主审核
	MaxChannels       int  // 加入频道数量上限（0 = 不限）
	MuteEnabled       bool // 加入后自动静音
	ArchiveEnabled    bool // 加入后自动归档
}

// 缺省值：总开关默认关闭（需管理员显式开启），其余防护默认开启；
// 自动退出外部拉入默认关（人工在管理页处理）。
const (
	DefaultJoinEnabled           = false
	DefaultJoinAutoLeaveExternal = false
	DefaultJoinRequireApproval   = true
	DefaultJoinMaxChannels       = 20
	DefaultJoinMuteEnabled       = true
	DefaultJoinArchiveEnabled    = true
)

// joinMaxChannelsRange 上限取值范围（0–200）。
const joinMaxChannelsUpper = 200

// settings 键。
const (
	keyJoinEnabled           = "join_enabled"
	keyJoinAutoLeaveExternal = "join_auto_leave_external"
	keyJoinRequireApproval   = "join_require_approval"
	keyJoinMaxChannels       = "join_max_channels"
	keyJoinMuteEnabled       = "join_mute_enabled"
	keyJoinArchiveEnabled    = "join_archive_enabled"
)

// DefaultJoinConfig 返回全部缺省配置。
func DefaultJoinConfig() JoinConfig {
	return JoinConfig{
		Enabled:           DefaultJoinEnabled,
		AutoLeaveExternal: DefaultJoinAutoLeaveExternal,
		RequireApproval:   DefaultJoinRequireApproval,
		MaxChannels:       DefaultJoinMaxChannels,
		MuteEnabled:       DefaultJoinMuteEnabled,
		ArchiveEnabled:    DefaultJoinArchiveEnabled,
	}
}

// ValidateJoinConfig 校验配置取值；错误为受控中文文案（Web 400 直接可用）。
func ValidateJoinConfig(c JoinConfig) error {
	if c.MaxChannels < 0 || c.MaxChannels > joinMaxChannelsUpper {
		return fmt.Errorf("加入数量上限必须为 0–%d（0 表示不限）。", joinMaxChannelsUpper)
	}
	return nil
}

// LoadJoinConfig 读取频道加入配置：单键缺失或非法时回退该项缺省值。
func LoadJoinConfig(ctx context.Context, st *store.Store) JoinConfig {
	cfg := DefaultJoinConfig()
	if st == nil {
		return cfg
	}
	loadBool(ctx, st, keyJoinEnabled, &cfg.Enabled)
	loadBool(ctx, st, keyJoinAutoLeaveExternal, &cfg.AutoLeaveExternal)
	loadBool(ctx, st, keyJoinRequireApproval, &cfg.RequireApproval)
	loadBool(ctx, st, keyJoinMuteEnabled, &cfg.MuteEnabled)
	loadBool(ctx, st, keyJoinArchiveEnabled, &cfg.ArchiveEnabled)
	if v, ok := loadIntSetting(ctx, st, keyJoinMaxChannels); ok && v >= 0 && v <= joinMaxChannelsUpper {
		cfg.MaxChannels = v
	}
	return cfg
}

// SaveJoinConfig 校验并整体写入配置；写前先校验保证不落非法值。
func SaveJoinConfig(ctx context.Context, st *store.Store, cfg JoinConfig) error {
	if st == nil {
		return errors.New("syscfg: Store 为必填项")
	}
	if err := ValidateJoinConfig(cfg); err != nil {
		return err
	}
	if err := setSettingJSON(ctx, st, keyJoinEnabled, cfg.Enabled); err != nil {
		return err
	}
	if err := setSettingJSON(ctx, st, keyJoinAutoLeaveExternal, cfg.AutoLeaveExternal); err != nil {
		return err
	}
	if err := setSettingJSON(ctx, st, keyJoinRequireApproval, cfg.RequireApproval); err != nil {
		return err
	}
	if err := setSettingJSON(ctx, st, keyJoinMaxChannels, cfg.MaxChannels); err != nil {
		return err
	}
	if err := setSettingJSON(ctx, st, keyJoinMuteEnabled, cfg.MuteEnabled); err != nil {
		return err
	}
	return setSettingJSON(ctx, st, keyJoinArchiveEnabled, cfg.ArchiveEnabled)
}

// loadBool 读布尔键：缺失/非法保持缺省。
func loadBool(ctx context.Context, st *store.Store, key string, dst *bool) {
	if v, ok := loadBoolSetting(ctx, st, key); ok {
		*dst = v
	}
}

func loadBoolSetting(ctx context.Context, st *store.Store, key string) (bool, bool) {
	v, ok, err := st.GetSetting(ctx, key)
	if err != nil || !ok {
		return false, false
	}
	var b bool
	if json.Unmarshal([]byte(v), &b) != nil {
		return false, false
	}
	return b, true
}

func loadIntSetting(ctx context.Context, st *store.Store, key string) (int, bool) {
	v, ok, err := st.GetSetting(ctx, key)
	if err != nil || !ok {
		return 0, false
	}
	var n int
	if json.Unmarshal([]byte(v), &n) != nil {
		return 0, false
	}
	return n, true
}

func loadInt64Setting(ctx context.Context, st *store.Store, key string) (int64, bool) {
	v, ok, err := st.GetSetting(ctx, key)
	if err != nil || !ok {
		return 0, false
	}
	var n int64
	if json.Unmarshal([]byte(v), &n) != nil {
		return 0, false
	}
	return n, true
}

func setSettingJSON(ctx context.Context, st *store.Store, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("syscfg: 编码配置 %s 失败: %w", key, err)
	}
	return st.SetSetting(ctx, key, string(raw))
}
