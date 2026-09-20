// 请求重试配置：settings 表 max_request_attempts 键的单一来源。
// 键名、缺省值与校验只在本包定义，access / web 共用同一实现。
package syscfg

import (
	"context"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// DefaultMaxRequestAttempts 是单个请求累计尝试上限的缺省值（含首次）。
const DefaultMaxRequestAttempts = 3

// maxRequestAttemptsUpper 是最大尝试次数的取值上界（防误配引发重试风暴）。
const maxRequestAttemptsUpper = 10

// keyMaxRequestAttempts 是 settings 表中的键（value_json 为 JSON 数字）。
const keyMaxRequestAttempts = "max_request_attempts"

// LoadMaxRequestAttempts 读取最大尝试次数：键缺失或非法时回退缺省值。
// 调用频率低（重试校验、详情展示），不做进程内缓存，保证管理端修改后
// 即时生效。
func LoadMaxRequestAttempts(ctx context.Context, st *store.Store) int {
	if st == nil {
		return DefaultMaxRequestAttempts
	}
	if v, ok := loadIntSetting(ctx, st, keyMaxRequestAttempts); ok && v >= 1 && v <= maxRequestAttemptsUpper {
		return v
	}
	return DefaultMaxRequestAttempts
}

// ValidateMaxRequestAttempts 校验取值范围；错误为受控中文文案（Web 400 直接可用）。
func ValidateMaxRequestAttempts(n int) error {
	if n < 1 || n > maxRequestAttemptsUpper {
		return fmt.Errorf("最大尝试次数必须为 1–%d 的整数（累计含首次）。", maxRequestAttemptsUpper)
	}
	return nil
}

// SetMaxRequestAttempts 校验并写入最大尝试次数；写前先校验保证不落非法值。
func SetMaxRequestAttempts(ctx context.Context, st *store.Store, n int) error {
	if st == nil {
		return errors.New("syscfg: Store 为必填项")
	}
	if err := ValidateMaxRequestAttempts(n); err != nil {
		return err
	}
	return setSettingJSON(ctx, st, keyMaxRequestAttempts, n)
}
