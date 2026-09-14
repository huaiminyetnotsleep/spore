package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// GetSetting 读取一个设置键的 JSON 值；键不存在时 ok 为 false。
// 已知键包括 access_key_hash、github_binding、timezone、
// dedup_window_min、queue_capacity 等，键名由业务层约定。
func (s *Store) GetSetting(ctx context.Context, key string) (value string, ok bool, err error) {
	err = s.ex.QueryRowContext(ctx, "SELECT value_json FROM settings WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, wrapDB("读取设置", err)
	}
	return value, true, nil
}

// SetSetting 写入（或覆盖）一个设置键的 JSON 值。
// 敏感值（如访问密钥）只能存哈希等不可逆形式，明文不落库。
func (s *Store) SetSetting(ctx context.Context, key, valueJSON string) error {
	_, err := s.ex.ExecContext(ctx, `INSERT INTO settings (key, value_json)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json`,
		key, valueJSON)
	return wrapDB("写设置", err)
}

// DeleteSetting 删除一个设置键；键不存在时静默成功（幂等）。
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.ex.ExecContext(ctx, "DELETE FROM settings WHERE key = ?", key)
	return wrapDB("删除设置", err)
}

// loadBoolSetting 读取布尔设置：键缺失、读取错误或 JSON 解析失败一律返回默认值。
// 历史遗留：引用直发时代的 prefer_media_reference / allow_reference_fallback
// 两键随功能移除不再读取，旧值留在库中无害。
func loadBoolSetting(ctx context.Context, st *Store, key string, defaultValue bool) bool {
	if st == nil {
		return defaultValue
	}
	v, ok, err := st.GetSetting(ctx, key)
	if err != nil || !ok {
		return defaultValue
	}
	var b bool
	if json.Unmarshal([]byte(v), &b) != nil {
		return defaultValue
	}
	return b
}
