package store

import (
	"context"
	"fmt"
)

// UpdateUserProfile 更新 Telegram 资料，不触碰状态、owner 或任何限额字段。
// Telegram username 可以为空，因此空值会清除旧用户名；调用方应只对已存在用户调用。
func (s *Store) UpdateUserProfile(ctx context.Context, id int64, username, displayName string) error {
	res, err := s.ex.ExecContext(ctx, `UPDATE users
		SET username = ?, display_name = ?
		WHERE id = ?`, nullStr(username), nullStr(displayName), id)
	if err != nil {
		return wrapDB("更新用户资料", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return wrapDB("更新用户资料", err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// UserProfile 是 Web/Telegram 资料查询边界的最小模型。
type UserProfile struct {
	ID          int64
	Username    string
	DisplayName string
}

// ValidateUserProfile 用于避免外部资料查询把错误用户 ID 写入数据库。
func ValidateUserProfile(p UserProfile) error {
	if p.ID <= 0 {
		return fmt.Errorf("用户 ID 必须为正整数")
	}
	return nil
}
