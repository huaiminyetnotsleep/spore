package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// 绑定来源（channel_bindings.bound_via）。
const (
	BoundViaBot = "bot" // 用户经 Bot /bind 指令绑定
	BoundViaWeb = "web" // 管理员经 Web 管理端绑定
)

// boundViaSet 是合法绑定来源白名单，防止把未知值写进库。
var boundViaSet = map[string]bool{
	BoundViaBot: true,
	BoundViaWeb: true,
}

// ChannelBinding 是 channel_bindings 表的行模型，主键为 Bot API 的频道
// 数字 ID（-100 前缀）。同一频道只归属一个用户；时间字段为 Unix 毫秒。
type ChannelBinding struct {
	ChannelID int64
	UserID    int64
	Username  string // 频道公开用户名（无 @），私有频道为空
	Title     string
	BoundVia  string
	CreatedAt int64
	UpdatedAt int64
}

// ChannelBindingWithUser 是 Web 管理端列表行：绑定信息 + 所属用户的展示资料
// （用户可能已被硬删除，LEFT JOIN 后资料为零值，展示退化为 user_id）。
type ChannelBindingWithUser struct {
	ChannelBinding
	UserUsername    string
	UserDisplayName string
}

const selectChannelBinding = `SELECT channel_id, user_id,
	COALESCE(username, ''), COALESCE(title, ''), bound_via,
	created_at, updated_at
FROM channel_bindings`

func scanChannelBinding(row scanner) (ChannelBinding, error) {
	var b ChannelBinding
	err := row.Scan(&b.ChannelID, &b.UserID, &b.Username, &b.Title, &b.BoundVia,
		&b.CreatedAt, &b.UpdatedAt)
	return b, err
}

const selectChannelBindingWithUser = `SELECT b.channel_id, b.user_id,
	COALESCE(b.username, ''), COALESCE(b.title, ''), b.bound_via,
	b.created_at, b.updated_at,
	COALESCE(u.username, ''), COALESCE(u.display_name, '')
FROM channel_bindings b
LEFT JOIN users u ON u.id = b.user_id`

func scanChannelBindingWithUser(row scanner) (ChannelBindingWithUser, error) {
	var b ChannelBindingWithUser
	err := row.Scan(&b.ChannelID, &b.UserID, &b.Username, &b.Title, &b.BoundVia,
		&b.CreatedAt, &b.UpdatedAt, &b.UserUsername, &b.UserDisplayName)
	return b, err
}

// UpsertChannelBinding 写入或刷新绑定。channel_id 是主键：
//   - 同一用户重复绑定 → 更新 username/title/来源与 updated_at（幂等）；
//   - 同一频道已被其他用户绑定 → 返回 STORE_CONSTRAINT（调用方转译为
//     "已被其他用户绑定"的业务拒绝），避免静默夺取归属。
//
// 查重与写入同事务执行（SQLite 的 ON CONFLICT DO UPDATE WHERE 语义在
// 条件不满足时是静默跳过而非报错，无法承载跨用户冲突语义）。
func (s *Store) UpsertChannelBinding(ctx context.Context, in ChannelBinding) (ChannelBinding, error) {
	if in.UserID <= 0 {
		return ChannelBinding{}, apperr.New(apperr.CodeInternal, "频道绑定必须归属一个用户")
	}
	if !boundViaSet[in.BoundVia] {
		return ChannelBinding{}, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法绑定来源 %q", in.BoundVia))
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = nowMillis()
	}
	in.UpdatedAt = nowMillis()

	var out ChannelBinding
	err := s.Tx(ctx, func(tx *Store) error {
		existing, err := tx.GetChannelBinding(ctx, in.ChannelID)
		switch {
		case errors.Is(err, ErrNotFound):
			_, err := tx.ex.ExecContext(ctx, `INSERT INTO channel_bindings
				(channel_id, user_id, username, title, bound_via, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?)`,
				in.ChannelID, in.UserID, nullStr(in.Username), nullStr(in.Title),
				in.BoundVia, in.CreatedAt, in.UpdatedAt)
			if err != nil {
				return wrapDB("写入频道绑定", err)
			}
			out = in
			return nil
		case err != nil:
			return err
		case existing.UserID != in.UserID:
			return apperr.Wrap(apperr.CodeStoreConstraint,
				fmt.Errorf("频道 %d 已被其他用户绑定", in.ChannelID))
		}
		_, err = tx.ex.ExecContext(ctx, `UPDATE channel_bindings SET
			username = ?, title = ?, bound_via = ?, updated_at = ?
			WHERE channel_id = ?`,
			nullStr(in.Username), nullStr(in.Title), in.BoundVia, in.UpdatedAt, in.ChannelID)
		if err != nil {
			return wrapDB("更新频道绑定", err)
		}
		in.CreatedAt = existing.CreatedAt // 更新路径保留原绑定时间
		out = in
		return nil
	})
	if err != nil {
		return ChannelBinding{}, err
	}
	return out, nil
}

// GetChannelBinding 按频道 ID 读取绑定；不存在返回 ErrNotFound。
func (s *Store) GetChannelBinding(ctx context.Context, channelID int64) (ChannelBinding, error) {
	b, err := scanChannelBinding(s.ex.QueryRowContext(ctx,
		selectChannelBinding+" WHERE channel_id = ?", channelID))
	if errors.Is(err, sql.ErrNoRows) {
		return ChannelBinding{}, ErrNotFound
	}
	if err != nil {
		return ChannelBinding{}, wrapDB("读取频道绑定", err)
	}
	return b, nil
}

// ListChannelBindingsByUser 返回该用户名下全部绑定，按绑定时间排序。
func (s *Store) ListChannelBindingsByUser(ctx context.Context, userID int64) ([]ChannelBinding, error) {
	rows, err := s.ex.QueryContext(ctx,
		selectChannelBinding+" WHERE user_id = ? ORDER BY created_at, channel_id", userID)
	if err != nil {
		return nil, wrapDB("列出用户频道绑定", err)
	}
	defer rows.Close()
	out := []ChannelBinding{}
	for rows.Next() {
		b, err := scanChannelBinding(rows)
		if err != nil {
			return nil, wrapDB("扫描频道绑定行", err)
		}
		out = append(out, b)
	}
	return out, wrapDB("遍历频道绑定行", rows.Err())
}

// ListChannelBindingsWithUser 返回全部绑定（含所属用户资料）供 Web 管理端
// 展示，按绑定时间倒序。
func (s *Store) ListChannelBindingsWithUser(ctx context.Context) ([]ChannelBindingWithUser, error) {
	rows, err := s.ex.QueryContext(ctx, selectChannelBindingWithUser+" ORDER BY b.created_at DESC, b.channel_id")
	if err != nil {
		return nil, wrapDB("列出频道绑定", err)
	}
	defer rows.Close()
	out := []ChannelBindingWithUser{}
	for rows.Next() {
		b, err := scanChannelBindingWithUser(rows)
		if err != nil {
			return nil, wrapDB("扫描频道绑定行", err)
		}
		out = append(out, b)
	}
	return out, wrapDB("遍历频道绑定行", rows.Err())
}

// DeleteChannelBinding 解除绑定并返回删除前的记录；ownerID > 0 时限定
// 只删除该用户的绑定（Bot 侧"解绑自己的频道"），0 表示无归属限制
// （Web 管理端解绑任意绑定）。不存在或不属于该用户时返回 ErrNotFound。
func (s *Store) DeleteChannelBinding(ctx context.Context, channelID, ownerID int64) (ChannelBinding, error) {
	b, err := s.GetChannelBinding(ctx, channelID)
	if err != nil {
		return ChannelBinding{}, err
	}
	if ownerID > 0 && b.UserID != ownerID {
		return ChannelBinding{}, ErrNotFound
	}
	res, err := s.ex.ExecContext(ctx, "DELETE FROM channel_bindings WHERE channel_id = ?", channelID)
	if err := affected(res, err, "删除频道绑定"); err != nil {
		return ChannelBinding{}, err
	}
	return b, nil
}
