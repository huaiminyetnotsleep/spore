package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// 用户状态（users.status）。
const (
	UserPending  = "pending"  // 已提交申请，等待管理员审批
	UserEnabled  = "enabled"  // 正常可用
	UserDisabled = "disabled" // 被管理员停用
	UserArchived = "archived" // 归档保留历史记录
)

// userStatuses 是合法状态白名单，防止把未知值写进库。
var userStatuses = map[string]bool{
	UserPending:  true,
	UserEnabled:  true,
	UserDisabled: true,
	UserArchived: true,
}

// 用户限额默认值（与 v1 迁移中的列默认一致）。
const (
	DefaultSubmitIntervalSec = 10 // 两次提交的最小间隔（秒）
	DefaultDailyLimit        = 50 // 每日请求额度
	DefaultConcurrentLimit   = 2  // 未完成请求并发上限
)

// 用户级频道绑定上限默认值：bind_limit 列为 0（未单独配置）时按角色回退。
const (
	DefaultBindLimitUser  = 1 // 普通用户默认可绑定 1 个频道
	DefaultBindLimitOwner = 3 // owner 默认可绑定 3 个频道
)

// 用户级云盘下载权限三态（users.cloud_download）。
const (
	CloudDownloadDefault = 0 // 跟随角色默认：owner 允许、普通用户拒绝
	CloudDownloadAllow   = 1 // 显式允许
	CloudDownloadDeny    = 2 // 显式拒绝
)

// User 是 users 表的行模型，主键即 Telegram User ID。
// 时间字段为 Unix 毫秒时间戳，0 表示尚未发生（库中存 NULL）；
// 可空文本以空字符串等价 NULL。
type User struct {
	ID                int64
	Status            string
	IsOwner           bool // owner 全局唯一：跳过频率/额度/并发检查，请求仍记录
	Username          string
	DisplayName       string
	Note              string
	SubmitIntervalSec int
	DailyLimit        int
	ConcurrentLimit   int
	// BindLimit 是频道绑定数量上限；0 表示未单独配置（见 EffectiveBindLimit）。
	BindLimit int
	// CloudDownload 是用户级云盘下载权限三态；0 表示跟随角色默认
	//（见 EffectiveCloudDownload），与全局开关是 AND 关系。
	CloudDownload int
	// SourceBotID/SourceBotUsername 是来源 bot（首次 /start 的受理 bot）：
	// ID 为 Telegram bot 账号数字 ID，用户名为受理时快照。0/空 = 存量行或
	// Web 管理端手动添加（非 Bot 通道）。
	SourceBotID       int64
	SourceBotUsername string
	CreatedAt         int64
	FirstUsedAt       int64
	LastUsedAt        int64
	ArchivedAt        int64
	LastDeniedAt      int64
	LastDeniedReason  string
}

// selectUser 是 users 查询的统一前缀，可空列已 COALESCE 归一为零值。
const selectUser = `SELECT id, status, is_owner,
	COALESCE(username, ''), COALESCE(display_name, ''), COALESCE(note, ''),
	submit_interval_sec, daily_limit, concurrent_limit, bind_limit,
	cloud_download,
	source_bot_id, source_bot_username,
	created_at, COALESCE(first_used_at, 0), COALESCE(last_used_at, 0),
	COALESCE(archived_at, 0), COALESCE(last_denied_at, 0), COALESCE(last_denied_reason, '')
FROM users`

// scanner 兼容 *sql.Row 与 *sql.Rows 的扫描接口。
type scanner interface {
	Scan(dest ...any) error
}

func scanUser(row scanner) (User, error) {
	var (
		u     User
		owner int64
	)
	err := row.Scan(&u.ID, &u.Status, &owner, &u.Username, &u.DisplayName, &u.Note,
		&u.SubmitIntervalSec, &u.DailyLimit, &u.ConcurrentLimit, &u.BindLimit, &u.CloudDownload,
		&u.SourceBotID, &u.SourceBotUsername,
		&u.CreatedAt, &u.FirstUsedAt, &u.LastUsedAt, &u.ArchivedAt,
		&u.LastDeniedAt, &u.LastDeniedReason)
	u.IsOwner = owner == 1
	return u, err
}

// CreateUser 写入新用户（status 缺省 pending、限额缺省取默认值），回填 ID 与 CreatedAt。
// 创建的用户始终不是 owner：owner 只能经 SetOwner 变更（全局唯一，审计由调用方负责）。
// ID 重复时返回 STORE_CONSTRAINT。
func (s *Store) CreateUser(ctx context.Context, in User) (User, error) {
	if in.Status == "" {
		in.Status = UserPending
	}
	if !userStatuses[in.Status] {
		return User{}, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法用户状态 %q", in.Status))
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = nowMillis()
	}
	if in.SubmitIntervalSec == 0 {
		in.SubmitIntervalSec = DefaultSubmitIntervalSec
	}
	if in.DailyLimit == 0 {
		in.DailyLimit = DefaultDailyLimit
	}
	if in.ConcurrentLimit == 0 {
		in.ConcurrentLimit = DefaultConcurrentLimit
	}
	res, err := s.ex.ExecContext(ctx, `INSERT INTO users
		(id, status, is_owner, username, display_name, note,
		 submit_interval_sec, daily_limit, concurrent_limit, created_at,
		 first_used_at, last_used_at, archived_at, last_denied_at, last_denied_reason,
		 source_bot_id, source_bot_username)
		VALUES (?,?,0,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		nullInt64(in.ID), in.Status, nullStr(in.Username), nullStr(in.DisplayName), nullStr(in.Note),
		in.SubmitIntervalSec, in.DailyLimit, in.ConcurrentLimit, in.CreatedAt,
		nullInt64(in.FirstUsedAt), nullInt64(in.LastUsedAt), nullInt64(in.ArchivedAt),
		nullInt64(in.LastDeniedAt), nullStr(in.LastDeniedReason),
		in.SourceBotID, in.SourceBotUsername)
	if err != nil {
		if isConstraintErr(err) {
			return User{}, apperr.Wrap(apperr.CodeStoreConstraint, fmt.Errorf("创建用户 %d: %w", in.ID, err))
		}
		return User{}, wrapDB("创建用户", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, wrapDB("读取新用户 ID", err)
	}
	in.ID = id
	return in, nil
}

// GetUser 按 ID 读取用户；不存在返回 ErrNotFound。
func (s *Store) GetUser(ctx context.Context, id int64) (User, error) {
	u, err := scanUser(s.ex.QueryRowContext(ctx, selectUser+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, wrapDB("读取用户", err)
	}
	return u, nil
}

// ListUsers 返回全部用户（容量目标 ≤100，无需分页），按创建时间排序。
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.ex.QueryContext(ctx, selectUser+" ORDER BY created_at, id")
	if err != nil {
		return nil, wrapDB("列出用户", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, wrapDB("扫描用户行", err)
		}
		out = append(out, u)
	}
	return out, wrapDB("遍历用户行", rows.Err())
}

// UpdateUserStatus 执行状态流转并返回更新后的用户。
// 转入 archived 时写入 archived_at，离开 archived 时清空；
// 用户不存在返回 ErrNotFound。审计由调用方记录。
func (s *Store) UpdateUserStatus(ctx context.Context, id int64, status string) (User, error) {
	if !userStatuses[status] {
		return User{}, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法用户状态 %q", status))
	}
	var (
		res sql.Result
		err error
	)
	if status == UserArchived {
		res, err = s.ex.ExecContext(ctx,
			"UPDATE users SET status = ?, archived_at = ? WHERE id = ?", status, nowMillis(), id)
	} else {
		res, err = s.ex.ExecContext(ctx,
			"UPDATE users SET status = ?, archived_at = NULL WHERE id = ?", status, id)
	}
	if err := affected(res, err, "更新用户状态"); err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, id)
}

// UpdateUserLimits 调整单用户的提交间隔/每日额度/并发上限；
// 传 0 的字段保持原值不变（即时生效）。审计由调用方记录。
func (s *Store) UpdateUserLimits(ctx context.Context, id int64, intervalSec, daily, concurrent int) error {
	res, err := s.ex.ExecContext(ctx, `UPDATE users SET
		submit_interval_sec = CASE WHEN ? > 0 THEN ? ELSE submit_interval_sec END,
		daily_limit = CASE WHEN ? > 0 THEN ? ELSE daily_limit END,
		concurrent_limit = CASE WHEN ? > 0 THEN ? ELSE concurrent_limit END
		WHERE id = ?`, intervalSec, intervalSec, daily, daily, concurrent, concurrent, id)
	return affected(res, err, "更新用户限额")
}

// EffectiveBindLimit 返回生效的频道绑定数量上限：
// 用户单独配置（bind_limit > 0）优先，否则按角色回退默认值。
func (u User) EffectiveBindLimit() int {
	if u.BindLimit > 0 {
		return u.BindLimit
	}
	if u.IsOwner {
		return DefaultBindLimitOwner
	}
	return DefaultBindLimitUser
}

// UpdateUserBindLimit 调整单用户的频道绑定数量上限（0 = 跟随角色默认，
// 1–20 为显式值；合法性由调用方校验）。审计由调用方记录。
func (s *Store) UpdateUserBindLimit(ctx context.Context, id int64, n int) error {
	res, err := s.ex.ExecContext(ctx, "UPDATE users SET bind_limit = ? WHERE id = ?", n, id)
	return affected(res, err, "更新频道绑定上限")
}

// EffectiveCloudDownload 返回生效的用户级云盘下载权限：显式允许/拒绝
// 优先，否则按角色回退默认（owner 允许、普通用户拒绝）。这只决定用户级
// 维度；实际可用还需云盘全局开关开启（AND 关系）。
func (u User) EffectiveCloudDownload() bool {
	switch u.CloudDownload {
	case CloudDownloadAllow:
		return true
	case CloudDownloadDeny:
		return false
	default:
		return u.IsOwner
	}
}

// UpdateUserCloudDownload 调整单用户的云盘下载权限三态（0–2；
// 合法性由调用方校验）。审计由调用方记录。
func (s *Store) UpdateUserCloudDownload(ctx context.Context, id int64, mode int) error {
	res, err := s.ex.ExecContext(ctx, "UPDATE users SET cloud_download = ? WHERE id = ?", mode, id)
	return affected(res, err, "更新云盘下载权限")
}

// UpdateUserNote 更新管理员备注；传空字符串即清空。
func (s *Store) UpdateUserNote(ctx context.Context, id int64, note string) error {
	res, err := s.ex.ExecContext(ctx, "UPDATE users SET note = ? WHERE id = ?", nullStr(note), id)
	return affected(res, err, "更新用户备注")
}

// SetOwner 设置或取消 owner 标记。设为 owner 时单条语句内先清空其他用户标记，
// 保证全局唯一、无并发窗口。审计由调用方记录。
func (s *Store) SetOwner(ctx context.Context, id int64, owner bool) error {
	var (
		res sql.Result
		err error
	)
	if owner {
		res, err = s.ex.ExecContext(ctx, "UPDATE users SET is_owner = (id = ?)", id)
	} else {
		res, err = s.ex.ExecContext(ctx, "UPDATE users SET is_owner = 0 WHERE id = ?", id)
	}
	return affected(res, err, "更新 owner 标记")
}

// OwnerID 返回唯一 owner 用户（is_owner=1）的 Telegram ID；
// 未设置 owner 返回 ErrNotFound；发现多个 owner 时返回存储约束错误，拒绝
// 静默选择任意管理员。事件通知用它定位管理员私聊（Bot 私聊的 chat_id
// 与用户 ID 相同），变更 owner 后无需重启即生效。
func (s *Store) OwnerID(ctx context.Context) (int64, error) {
	rows, err := s.ex.QueryContext(ctx, "SELECT id FROM users WHERE is_owner = 1 ORDER BY id")
	if err != nil {
		return 0, wrapDB("查询 owner 用户", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, wrapDB("遍历 owner 用户", err)
		}
		return 0, ErrNotFound
	}
	var id int64
	if err := rows.Scan(&id); err != nil {
		return 0, wrapDB("读取 owner 用户", err)
	}
	if rows.Next() {
		// 正常写入路径通过 SetOwner 保证唯一；这里防御迁移/人工改库等
		// 异常，通知不得随机发往多个潜在管理员中的某一个。
		return 0, apperr.New(apperr.CodeStoreConstraint, "数据库中存在多个 owner 用户")
	}
	if err := rows.Err(); err != nil {
		return 0, wrapDB("遍历 owner 用户", err)
	}
	return id, nil
}

// TouchUserUsage 记录用户使用时间：first_used_at 仅首次写入，last_used_at 每次刷新。
func (s *Store) TouchUserUsage(ctx context.Context, id int64, at int64) error {
	if at == 0 {
		at = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx,
		"UPDATE users SET first_used_at = COALESCE(first_used_at, ?), last_used_at = ? WHERE id = ?",
		at, at, id)
	return affected(res, err, "记录用户使用时间")
}

// MarkUserDenied 记录最近一次拒绝的时间与原因（拒绝不建 requests 行、不扣额度）。
func (s *Store) MarkUserDenied(ctx context.Context, id int64, reason string, at int64) error {
	if at == 0 {
		at = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx,
		"UPDATE users SET last_denied_at = ?, last_denied_reason = ? WHERE id = ?",
		at, nullStr(reason), id)
	return affected(res, err, "记录用户拒绝")
}

// TouchPendingApplication 刷新待审批用户的申请时间（created_at）：
// 重复 /start 视为再次申请，管理员在 Web 上看到的申请时间随之更新。
// 用户不存在返回 ErrNotFound；非 pending 用户不受影响（由调用方保证语义）。
func (s *Store) TouchPendingApplication(ctx context.Context, id int64, at int64) error {
	if at == 0 {
		at = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx,
		"UPDATE users SET created_at = ? WHERE id = ? AND status = ?", at, id, UserPending)
	return affected(res, err, "刷新申请时间")
}

// DeleteUser 硬删除用户；该用户仍有 requests 记录时返回 STORE_CONSTRAINT，
// 此时应改用 archived 状态保留历史。
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.ex.ExecContext(ctx, "DELETE FROM users WHERE id = ?", id)
	if err != nil {
		if isConstraintErr(err) {
			return apperr.Wrap(apperr.CodeStoreConstraint, fmt.Errorf("删除用户 %d（存在关联请求记录）: %w", id, err))
		}
		return wrapDB("删除用户", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return wrapDB("删除用户", err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}
