// 错误日志保留配置：settings 表 error_log_retention_days 键的单一来源。
// 键名、缺省值与校验只在本包定义，errlog / web 共用同一实现。
package syscfg

import (
	"context"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// DefaultErrorLogRetentionDays 是错误日志保留天数的缺省值。
const DefaultErrorLogRetentionDays = 30

// errorLogRetentionDaysUpper 是保留天数上界（防误配把日志全清光）。
const errorLogRetentionDaysUpper = 365

// keyErrorLogRetentionDays 是 settings 表中的键（value_json 为 JSON 数字）。
const keyErrorLogRetentionDays = "error_log_retention_days"

// LoadErrorLogRetentionDays 读取错误日志保留天数：键缺失或非法时回退
// 缺省值。errlog 清理循环每轮调用（低频），管理端修改后即时生效。
func LoadErrorLogRetentionDays(ctx context.Context, st *store.Store) int {
	if st == nil {
		return DefaultErrorLogRetentionDays
	}
	if v, ok := loadIntSetting(ctx, st, keyErrorLogRetentionDays); ok && v >= 1 && v <= errorLogRetentionDaysUpper {
		return v
	}
	return DefaultErrorLogRetentionDays
}

// ValidateErrorLogRetentionDays 校验取值范围；错误为受控中文文案
// （Web 400 直接可用）。
func ValidateErrorLogRetentionDays(n int) error {
	if n < 1 || n > errorLogRetentionDaysUpper {
		return fmt.Errorf("错误日志保留天数必须为 1–%d 的整数。", errorLogRetentionDaysUpper)
	}
	return nil
}

// SetErrorLogRetentionDays 校验并写入保留天数；写前先校验保证不落非法值。
func SetErrorLogRetentionDays(ctx context.Context, st *store.Store, n int) error {
	if st == nil {
		return errors.New("syscfg: Store 为必填项")
	}
	if err := ValidateErrorLogRetentionDays(n); err != nil {
		return err
	}
	return setSettingJSON(ctx, st, keyErrorLogRetentionDays, n)
}
