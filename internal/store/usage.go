package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// DailyUsage 是 usage_daily 表的行模型：某用户在某运营日（时区相关日期，
// 格式 YYYY-MM-DD，由调用方按设置的时区计算）的用量。
type DailyUsage struct {
	UserID     int64
	Day        string
	Used       int
	ResetCount int // 管理员重置次数
}

// IncrementUsage 把 (userID, day) 的当日用量累加 delta（通常为 1）。
// 单条 UPSERT 原子完成，无需显式事务；delta 不允许为负。
func (s *Store) IncrementUsage(ctx context.Context, userID int64, day string, delta int) error {
	if delta < 0 {
		return apperr.New(apperr.CodeInternal, "用量增量不能为负")
	}
	_, err := s.ex.ExecContext(ctx, `INSERT INTO usage_daily (user_id, day, used)
		VALUES (?, ?, ?)
		ON CONFLICT(user_id, day) DO UPDATE SET used = used + excluded.used`,
		userID, day, delta)
	return wrapDB("累加日用量", err)
}

// GetUsage 读取某用户某日的用量；无记录时返回零值（用户当日尚未提交过）。
func (s *Store) GetUsage(ctx context.Context, userID int64, day string) (DailyUsage, error) {
	var u DailyUsage
	err := s.ex.QueryRowContext(ctx,
		"SELECT user_id, day, used, reset_count FROM usage_daily WHERE user_id = ? AND day = ?",
		userID, day).Scan(&u.UserID, &u.Day, &u.Used, &u.ResetCount)
	if errors.Is(err, sql.ErrNoRows) {
		return DailyUsage{UserID: userID, Day: day}, nil
	}
	return u, wrapDB("读取日用量", err)
}

// ResetUsage 把某用户某日用量清零并累加 reset_count；当日无记录时不做任何事。
// 审计由调用方记录。
func (s *Store) ResetUsage(ctx context.Context, userID int64, day string) error {
	_, err := s.ex.ExecContext(ctx,
		"UPDATE usage_daily SET used = 0, reset_count = reset_count + 1 WHERE user_id = ? AND day = ?",
		userID, day)
	return wrapDB("重置日用量", err)
}
