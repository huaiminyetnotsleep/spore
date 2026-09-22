// 自动备份配置：settings 表 backup_interval_hours / backup_keep_count 键的
// 单一来源。键名、缺省值与校验只在本包定义，定时循环 / CLI / Web 共用
// 同一实现。
package syscfg

import (
	"context"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// DefaultBackupIntervalHours 是自动备份间隔的缺省值（小时）；0 = 关闭。
// 缺省 6 小时 × 默认保留 8 份 = 48 小时滚动窗口。
const DefaultBackupIntervalHours = 6

// backupIntervalHoursUpper 是间隔的取值上界（小时，一周），防误配。
const backupIntervalHoursUpper = 168

// DefaultBackupKeepCount 是自动备份保留份数的缺省值；默认间隔 6 小时下
// 恰好覆盖 48 小时。
const DefaultBackupKeepCount = 8

// backupKeepCountUpper 是保留份数的取值上界。
const backupKeepCountUpper = 50

// settings 键（value_json 为 JSON 数字）。
const (
	keyBackupIntervalHours = "backup_interval_hours"
	keyBackupKeepCount     = "backup_keep_count"
)

// LoadBackupIntervalHours 读取自动备份间隔（小时）：0 = 关闭；键缺失或
// 非法时回退缺省值。定时循环每 tick 重读，管理端修改即时生效。
func LoadBackupIntervalHours(ctx context.Context, st *store.Store) int {
	if st == nil {
		return DefaultBackupIntervalHours
	}
	if v, ok := loadIntSetting(ctx, st, keyBackupIntervalHours); ok && v >= 0 && v <= backupIntervalHoursUpper {
		return v
	}
	return DefaultBackupIntervalHours
}

// ValidateBackupIntervalHours 校验取值范围；错误为受控中文文案。
func ValidateBackupIntervalHours(n int) error {
	if n < 0 || n > backupIntervalHoursUpper {
		return fmt.Errorf("自动备份间隔必须为 0–%d 的整数小时（0 = 关闭）。", backupIntervalHoursUpper)
	}
	return nil
}

// SetBackupIntervalHours 校验并写入自动备份间隔。
func SetBackupIntervalHours(ctx context.Context, st *store.Store, n int) error {
	if st == nil {
		return errors.New("syscfg: Store 为必填项")
	}
	if err := ValidateBackupIntervalHours(n); err != nil {
		return err
	}
	return setSettingJSON(ctx, st, keyBackupIntervalHours, n)
}

// LoadBackupKeepCount 读取自动备份保留份数；键缺失或非法时回退缺省值。
func LoadBackupKeepCount(ctx context.Context, st *store.Store) int {
	if st == nil {
		return DefaultBackupKeepCount
	}
	if v, ok := loadIntSetting(ctx, st, keyBackupKeepCount); ok && v >= 1 && v <= backupKeepCountUpper {
		return v
	}
	return DefaultBackupKeepCount
}

// ValidateBackupKeepCount 校验取值范围；错误为受控中文文案。
func ValidateBackupKeepCount(n int) error {
	if n < 1 || n > backupKeepCountUpper {
		return fmt.Errorf("备份保留份数必须为 1–%d 的整数。", backupKeepCountUpper)
	}
	return nil
}

// SetBackupKeepCount 校验并写入保留份数。
func SetBackupKeepCount(ctx context.Context, st *store.Store, n int) error {
	if st == nil {
		return errors.New("syscfg: Store 为必填项")
	}
	if err := ValidateBackupKeepCount(n); err != nil {
		return err
	}
	return setSettingJSON(ctx, st, keyBackupKeepCount, n)
}

// KeyLastBackupAt 是"最近一次备份时间"（JSON 数字，Unix 毫秒）的 settings
// 键：Web 手动导出、CLI 与定时备份共用同一口径（备份页/总览据此展示）。
const KeyLastBackupAt = "last_backup_at"

// SetLastBackupAt 更新最近一次备份时间（尽力而为语义由调用方决定）。
func SetLastBackupAt(ctx context.Context, st *store.Store, ms int64) error {
	if st == nil {
		return errors.New("syscfg: Store 为必填项")
	}
	return setSettingJSON(ctx, st, KeyLastBackupAt, ms)
}

// LoadLastBackupAt 读取最近一次备份时间（Unix 毫秒；从未备份为 0）。
func LoadLastBackupAt(ctx context.Context, st *store.Store) int64 {
	if st == nil {
		return 0
	}
	if v, ok := loadInt64Setting(ctx, st, KeyLastBackupAt); ok {
		return v
	}
	return 0
}
