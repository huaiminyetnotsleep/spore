// 监听源（/watch）配置：settings 表 watch_* 键的单一来源。
// 键名、缺省值与校验只在本包定义，watch / botapi / web 共用同一实现。
package syscfg

import (
	"context"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// WatchConfig 是监听源相关的全部配置项。
type WatchConfig struct {
	// ApplyEnabled 允许用户经 /watch 自助申请（false 时 /watch 直接拒绝；
	// 管理员 Web 添加不受影响）。
	ApplyEnabled bool
	// RequireApproval 用户申请需号主审批（false = 免审批直接生效）。
	RequireApproval bool
	// MaxSources 监听源总数上限（0 = 不限；仅约束用户申请，管理员不受限）。
	MaxSources int
	// PerUserLimit 每用户申请上限（0 = 不限；号主经 Bot 提交同样不受限）。
	PerUserLimit int
}

// 缺省值：用户自助申请默认关闭（需管理员显式开启）；审批默认开启；
// 总上限 20、每用户 3。
const (
	DefaultWatchApplyEnabled    = false
	DefaultWatchRequireApproval = true
	DefaultWatchMaxSources      = 20
	DefaultWatchPerUserLimit    = 3
)

// 上限取值范围：MaxSources 0–200、PerUserLimit 0–20（0 表示不限）。
const (
	watchMaxSourcesUpper   = 200
	watchPerUserLimitUpper = 20
)

// settings 键。
const (
	keyWatchApplyEnabled    = "watch_apply_enabled"
	keyWatchRequireApproval = "watch_apply_require_approval"
	keyWatchMaxSources      = "watch_max_sources"
	keyWatchPerUserLimit    = "watch_per_user_limit"
)

// DefaultWatchConfig 返回全部缺省配置。
func DefaultWatchConfig() WatchConfig {
	return WatchConfig{
		ApplyEnabled:    DefaultWatchApplyEnabled,
		RequireApproval: DefaultWatchRequireApproval,
		MaxSources:      DefaultWatchMaxSources,
		PerUserLimit:    DefaultWatchPerUserLimit,
	}
}

// ValidateWatchConfig 校验配置取值；错误为受控中文文案（Web 400 直接可用）。
func ValidateWatchConfig(c WatchConfig) error {
	if c.MaxSources < 0 || c.MaxSources > watchMaxSourcesUpper {
		return fmt.Errorf("监听源总数上限必须为 0–%d（0 表示不限）。", watchMaxSourcesUpper)
	}
	if c.PerUserLimit < 0 || c.PerUserLimit > watchPerUserLimitUpper {
		return fmt.Errorf("每用户申请上限必须为 0–%d（0 表示不限）。", watchPerUserLimitUpper)
	}
	return nil
}

// LoadWatchConfig 读取监听源配置：单键缺失或非法时回退该项缺省值。
func LoadWatchConfig(ctx context.Context, st *store.Store) WatchConfig {
	cfg := DefaultWatchConfig()
	if st == nil {
		return cfg
	}
	loadBool(ctx, st, keyWatchApplyEnabled, &cfg.ApplyEnabled)
	loadBool(ctx, st, keyWatchRequireApproval, &cfg.RequireApproval)
	if v, ok := loadIntSetting(ctx, st, keyWatchMaxSources); ok && v >= 0 && v <= watchMaxSourcesUpper {
		cfg.MaxSources = v
	}
	if v, ok := loadIntSetting(ctx, st, keyWatchPerUserLimit); ok && v >= 0 && v <= watchPerUserLimitUpper {
		cfg.PerUserLimit = v
	}
	return cfg
}

// SaveWatchConfig 校验并整体写入配置；写前先校验保证不落非法值。
func SaveWatchConfig(ctx context.Context, st *store.Store, cfg WatchConfig) error {
	if st == nil {
		return errors.New("syscfg: Store 为必填项")
	}
	if err := ValidateWatchConfig(cfg); err != nil {
		return err
	}
	if err := setSettingJSON(ctx, st, keyWatchApplyEnabled, cfg.ApplyEnabled); err != nil {
		return err
	}
	if err := setSettingJSON(ctx, st, keyWatchRequireApproval, cfg.RequireApproval); err != nil {
		return err
	}
	if err := setSettingJSON(ctx, st, keyWatchMaxSources, cfg.MaxSources); err != nil {
		return err
	}
	return setSettingJSON(ctx, st, keyWatchPerUserLimit, cfg.PerUserLimit)
}
