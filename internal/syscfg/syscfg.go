// Package syscfg 是系统设置（settings 表 system_* 键）的单一来源：
// 系统名称等身份配置的键名、缺省值、校验与读写只在本包定义，
// botapi / notify / access / web 共用同一实现，避免各包各持键名。
package syscfg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// DefaultName 是系统名称缺省值（settings 无记录或记录非法时回退）。
const DefaultName = branding.DisplayName

// nameMaxLen 限制名称最大字符数（按 rune 计），避免超长名称撑爆
// Bot 帮助文案、事件通知标题与管理端界面。
const nameMaxLen = 32

// settingKeyName 是 settings 表中的键（value_json 为 JSON 字符串）。
const settingKeyName = "system_name"

// ValidateName 校验并规范化系统名称：去首尾空白，要求 1–32 个字符
// （按 rune 计），拒绝控制字符。返回规范化后的值；错误为受控中文文案，
// 调用方（Web API）可直接作为 400 响应消息。
func ValidateName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", errors.New("系统名称不能为空。")
	}
	if utf8.RuneCountInString(name) > nameMaxLen {
		return "", fmt.Errorf("系统名称不能超过 %d 个字符。", nameMaxLen)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", errors.New("系统名称不能包含控制字符。")
		}
	}
	return name, nil
}

// Name 读取系统名称：键缺失、读取错误或值非法时回退 DefaultName。
// 调用频率低（命令文案、通知标题、SPA 壳），不做进程内缓存，
// 保证管理端修改后即时生效。
func Name(ctx context.Context, st *store.Store) string {
	if st == nil {
		return DefaultName
	}
	v, ok, err := st.GetSetting(ctx, settingKeyName)
	if err != nil || !ok {
		return DefaultName
	}
	var s string
	if json.Unmarshal([]byte(v), &s) != nil {
		return DefaultName
	}
	name := strings.TrimSpace(s)
	if name == "" || utf8.RuneCountInString(name) > nameMaxLen {
		return DefaultName
	}
	return name
}

// SetName 校验并写入系统名称；校验失败返回 ValidateName 的受控文案。
func SetName(ctx context.Context, st *store.Store, raw string) error {
	if st == nil {
		return errors.New("syscfg: Store 为必填项")
	}
	name, err := ValidateName(raw)
	if err != nil {
		return err
	}
	v, err := json.Marshal(name)
	if err != nil {
		return fmt.Errorf("syscfg: 编码系统名称失败: %w", err)
	}
	return st.SetSetting(ctx, settingKeyName, string(v))
}
