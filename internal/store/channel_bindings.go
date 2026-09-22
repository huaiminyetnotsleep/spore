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

// 绑定状态（channel_bindings.status，v24 软解绑状态机）。
const (
	BindingStatusActive  = "active"  // 有效绑定，参与副本投递
	BindingStatusUnbound = "unbound" // 已解绑：记录保留供审计展示，不再投递
)

// 解绑原因（channel_bindings.unbind_reason）。
const (
	UnbindReasonManual      = "manual"       // 用户 /unbind 或管理端手动解绑
	UnbindReasonChannelGone = "channel_gone" // 副本投递发现频道已不存在，自动解绑
)

// boundViaSet 是合法绑定来源白名单，防止把未知值写进库。
var boundViaSet = map[string]bool{
	BoundViaBot: true,
	BoundViaWeb: true,
}

// unbindReasonSet 是合法解绑原因白名单。
var unbindReasonSet = map[string]bool{
	UnbindReasonManual:      true,
	UnbindReasonChannelGone: true,
}

// ChannelBinding 是 channel_bindings 表的行模型，主键为 Bot API 的频道
// 数字 ID（-100 前缀）。同一频道只归属一个用户；时间字段为 Unix 毫秒。
// BotID 是绑定路由：>0 表示绑定经该 bot 建立并通过硬校验，副本/置顶只接受
// 它受理的任务；0 为通配（Web 绑定与 v21 之前的历史行），任意受理 bot 均
// 尝试投递（失败优雅降级，不影响任务结果）。
// Status 是 v24 软解绑状态：解绑不删行，置 unbound 并记原因/时间；
// 重新绑定同频道即复活（改归属用户也允许——接管被解绑的行），
// 物理删除仅经管理端删除入口。
type ChannelBinding struct {
	ChannelID    int64
	UserID       int64
	Username     string // 频道公开用户名（无 @），私有频道为空
	Title        string
	BoundVia     string
	BotID        int64
	Status       string // active / unbound
	UnbindReason string // 解绑原因（status=active 时为空）
	UnboundAt    int64  // 解绑时间（Unix 毫秒，0=未解绑）
	CreatedAt    int64
	UpdatedAt    int64
}

// ChannelBindingWithUser 是 Web 管理端列表行：绑定信息 + 所属用户的展示资料
// （用户可能已被硬删除，LEFT JOIN 后资料为零值，展示退化为 user_id）。
type ChannelBindingWithUser struct {
	ChannelBinding
	UserUsername    string
	UserDisplayName string
}

const selectChannelBinding = `SELECT channel_id, user_id,
	COALESCE(username, ''), COALESCE(title, ''), bound_via, bot_id,
	status, COALESCE(unbind_reason, ''), unbound_at,
	created_at, updated_at
FROM channel_bindings`

func scanChannelBinding(row scanner) (ChannelBinding, error) {
	var b ChannelBinding
	err := row.Scan(&b.ChannelID, &b.UserID, &b.Username, &b.Title, &b.BoundVia,
		&b.BotID, &b.Status, &b.UnbindReason, &b.UnboundAt,
		&b.CreatedAt, &b.UpdatedAt)
	return b, err
}

const selectChannelBindingWithUser = `SELECT b.channel_id, b.user_id,
	COALESCE(b.username, ''), COALESCE(b.title, ''), b.bound_via, b.bot_id,
	b.status, COALESCE(b.unbind_reason, ''), b.unbound_at,
	b.created_at, b.updated_at,
	COALESCE(u.username, ''), COALESCE(u.display_name, '')
FROM channel_bindings b
LEFT JOIN users u ON u.id = b.user_id`

func scanChannelBindingWithUser(row scanner) (ChannelBindingWithUser, error) {
	var b ChannelBindingWithUser
	err := row.Scan(&b.ChannelID, &b.UserID, &b.Username, &b.Title, &b.BoundVia,
		&b.BotID, &b.Status, &b.UnbindReason, &b.UnboundAt,
		&b.CreatedAt, &b.UpdatedAt, &b.UserUsername, &b.UserDisplayName)
	return b, err
}

// UpsertChannelBinding 写入或刷新绑定。channel_id 是主键：
//   - 同一用户重复绑定 → 更新 username/title/来源/路由 bot 与 updated_at
//     （幂等；换 bot 重绑即改路由归属）；
//   - 同一频道已被其他用户**有效**绑定（status=active）→ 返回
//     STORE_CONSTRAINT（调用方转译为"已被其他用户绑定"的业务拒绝），
//     避免静默夺取归属；
//   - 现存行为 unbound → 视同空位：同用户重绑复活、换用户重绑接管
//     （原绑定已解除，频道归属让渡），状态复位 active、清理解绑痕迹。
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
				(channel_id, user_id, username, title, bound_via, bot_id, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?)`,
				in.ChannelID, in.UserID, nullStr(in.Username), nullStr(in.Title),
				in.BoundVia, in.BotID, in.CreatedAt, in.UpdatedAt)
			if err != nil {
				return wrapDB("写入频道绑定", err)
			}
			in.Status = BindingStatusActive
			out = in
			return nil
		case err != nil:
			return err
		case existing.Status == BindingStatusActive && existing.UserID != in.UserID:
			return apperr.Wrap(apperr.CodeStoreConstraint,
				fmt.Errorf("频道 %d 已被其他用户绑定", in.ChannelID))
		}
		// 同用户刷新，或复活/接管 unbound 行：状态复位并清理解绑痕迹。
		created := existing.CreatedAt
		if existing.UserID != in.UserID {
			created = in.CreatedAt // 接管视为新绑定
		}
		_, err = tx.ex.ExecContext(ctx, `UPDATE channel_bindings SET
			user_id = ?, username = ?, title = ?, bound_via = ?, bot_id = ?,
			status = ?, unbind_reason = '', unbound_at = 0,
			created_at = ?, updated_at = ?
			WHERE channel_id = ?`,
			in.UserID, nullStr(in.Username), nullStr(in.Title), in.BoundVia, in.BotID,
			BindingStatusActive, created, in.UpdatedAt, in.ChannelID)
		if err != nil {
			return wrapDB("更新频道绑定", err)
		}
		in.CreatedAt = created
		in.Status = BindingStatusActive
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

// ListChannelBindingsByUser 返回该用户名下全部**有效**绑定（status=active），
// 按绑定时间排序。Bot 侧投递与 /unbind、绑定状态等交互的语义都是"当前
// 有效绑定"；已解绑行仅供管理端审计展示（见 ListChannelBindingsWithUser）。
func (s *Store) ListChannelBindingsByUser(ctx context.Context, userID int64) ([]ChannelBinding, error) {
	rows, err := s.ex.QueryContext(ctx,
		selectChannelBinding+" WHERE user_id = ? AND status = ? ORDER BY created_at, channel_id",
		userID, BindingStatusActive)
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
	return s.listChannelBindingsWithUser(ctx, "", nil)
}

// ListChannelBindingsWithUserByUser 返回指定用户的全部频道绑定（含用户资料）。
func (s *Store) ListChannelBindingsWithUserByUser(ctx context.Context, userID int64) ([]ChannelBindingWithUser, error) {
	return s.listChannelBindingsWithUser(ctx, " WHERE b.user_id = ?", []any{userID})
}

func (s *Store) listChannelBindingsWithUser(ctx context.Context, where string, args []any) ([]ChannelBindingWithUser, error) {
	rows, err := s.ex.QueryContext(ctx, selectChannelBindingWithUser+where+" ORDER BY b.created_at DESC, b.channel_id", args...)
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

// MarkChannelBindingUnbound 软解绑：行保留，status 置 unbound 并记录原因
// 与时间，返回解绑后的记录。changed=false 表示本就处于 unbound（幂等，
// 调用方据此只做一次通知/审计）。ownerID > 0 时限定只解绑该用户的绑定
// （Bot 侧路径），0 表示无归属限制（Web 管理端）。不存在或不属于该用户
// 时返回 ErrNotFound。
func (s *Store) MarkChannelBindingUnbound(ctx context.Context, channelID, ownerID int64, reason string) (ChannelBinding, bool, error) {
	if !unbindReasonSet[reason] {
		return ChannelBinding{}, false, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法解绑原因 %q", reason))
	}
	b, err := s.GetChannelBinding(ctx, channelID)
	if err != nil {
		return ChannelBinding{}, false, err
	}
	if ownerID > 0 && b.UserID != ownerID {
		return ChannelBinding{}, false, ErrNotFound
	}
	if b.Status == BindingStatusUnbound {
		return b, false, nil
	}
	now := nowMillis()
	res, err := s.ex.ExecContext(ctx, `UPDATE channel_bindings SET
		status = ?, unbind_reason = ?, unbound_at = ?, updated_at = ?
		WHERE channel_id = ? AND status = ?`,
		BindingStatusUnbound, reason, now, now, channelID, BindingStatusActive)
	if err := affected(res, err, "解绑频道绑定"); err != nil {
		return ChannelBinding{}, false, err
	}
	b.Status = BindingStatusUnbound
	b.UnbindReason = reason
	b.UnboundAt = now
	b.UpdatedAt = now
	return b, true, nil
}

// DeleteChannelBinding 物理删除绑定并返回删除前的记录；ownerID > 0 时限定
// 只删除该用户的绑定（Bot 侧"解绑自己的频道"），0 表示无归属限制
// （Web 管理端解绑任意绑定）。不存在或不属于该用户时返回 ErrNotFound。
// 常规解绑请用 MarkChannelBindingUnbound（软解绑留痕）；本方法仅供管理端
// 删除入口与既有语义兜底。
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
