package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// WebSession 是 web_sessions 表的行模型：管理端登录会话。
// 出于安全考虑，会话 ID 只以 SHA-256 哈希形式落库（id_hash）。
type WebSession struct {
	IDHash    string
	CreatedAt int64
	ExpiresAt int64 // Unix 毫秒；滑动续期由 TouchWebSession 推进
	CSRFToken string
	IP        string // 审计来源 IP（取反代 X-Forwarded-For），可空
	UserAgent string // 可空
}

// CreateWebSession 写入一条会话；id_hash 与 csrf_token 不能为空，
// 重复的 id_hash 返回 STORE_CONSTRAINT。CreatedAt 缺省取当前时间。
func (s *Store) CreateWebSession(ctx context.Context, sess WebSession) error {
	if sess.IDHash == "" || sess.CSRFToken == "" {
		return apperr.New(apperr.CodeInternal, "会话 ID 哈希与 CSRF token 不能为空")
	}
	if sess.CreatedAt == 0 {
		sess.CreatedAt = nowMillis()
	}
	_, err := s.ex.ExecContext(ctx, `INSERT INTO web_sessions (id_hash, created_at, expires_at, csrf_token, ip, user_agent)
		VALUES (?,?,?,?,?,?)`,
		sess.IDHash, sess.CreatedAt, sess.ExpiresAt, sess.CSRFToken, nullStr(sess.IP), nullStr(sess.UserAgent))
	if err != nil {
		if isConstraintErr(err) {
			return apperr.Wrap(apperr.CodeStoreConstraint, fmt.Errorf("创建会话（id_hash 已存在）: %w", err))
		}
		return wrapDB("创建会话", err)
	}
	return nil
}

// GetWebSession 按 ID 哈希读取会话；不存在返回 ErrNotFound。
// 过期与否由调用方比较 ExpiresAt 判断。
func (s *Store) GetWebSession(ctx context.Context, idHash string) (WebSession, error) {
	var (
		sess WebSession
		row  = s.ex.QueryRowContext(ctx, `SELECT id_hash, created_at, expires_at, csrf_token,
		COALESCE(ip, ''), COALESCE(user_agent, '') FROM web_sessions WHERE id_hash = ?`, idHash)
	)
	if err := row.Scan(&sess.IDHash, &sess.CreatedAt, &sess.ExpiresAt, &sess.CSRFToken, &sess.IP, &sess.UserAgent); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return WebSession{}, ErrNotFound
		}
		return WebSession{}, wrapDB("读取会话", err)
	}
	return sess, nil
}

// TouchWebSession 滑动续期：把会话过期时间推到 expiresAt；会话不存在返回 ErrNotFound。
func (s *Store) TouchWebSession(ctx context.Context, idHash string, expiresAt int64) error {
	res, err := s.ex.ExecContext(ctx,
		"UPDATE web_sessions SET expires_at = ? WHERE id_hash = ?", expiresAt, idHash)
	return affected(res, err, "续期会话")
}

// DeleteWebSession 删除当前会话（登出）；会话不存在返回 ErrNotFound。
func (s *Store) DeleteWebSession(ctx context.Context, idHash string) error {
	res, err := s.ex.ExecContext(ctx, "DELETE FROM web_sessions WHERE id_hash = ?", idHash)
	return affected(res, err, "删除会话")
}

// DeleteAllWebSessions 失效全部会话（密钥重置、OAuth 绑定变更时使用）。
func (s *Store) DeleteAllWebSessions(ctx context.Context) error {
	_, err := s.ex.ExecContext(ctx, "DELETE FROM web_sessions")
	return wrapDB("清空会话", err)
}

// CleanExpiredWebSessions 删除已过期的会话；返回删除的条数。
func (s *Store) CleanExpiredWebSessions(ctx context.Context, now int64) (int64, error) {
	res, err := s.ex.ExecContext(ctx, "DELETE FROM web_sessions WHERE expires_at < ?", now)
	if err != nil {
		return 0, wrapDB("清理过期会话", err)
	}
	n, err := res.RowsAffected()
	return n, wrapDB("清理过期会话", err)
}
