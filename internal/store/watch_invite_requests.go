package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// watch_invite_requests.go — 私有邀请链接监听申请（/watch）的异步处理记录。
// 活动状态 pending/waiting_telegram/waiting_bot 由启动恢复与后台协调流程
// 消费；approved/rejected/failed 为终态。
//
// hash 保留策略（DAO 只提供原语，清理时机由服务层在状态流转时控制）：
//   - pending / waiting_telegram：保留完整 hash，Telegram 侧协作仍在进行；
//   - failed：保留完整 hash，便于服务层重试；
//   - waiting_bot / approved / rejected：加入动作已下发或已有结论，服务层
//     应调用 ClearWatchInviteRequestHash 清理完整 hash，仅留 masked_hash
//     供 Bot/Web 安全展示。
//
// user_id=0 为管理员 Web 路径（与 watch_sources.added_by 同约定，无数据库
// 外键）；>0 为申请人用户 ID。

// 私有邀请链接监听申请状态（watch_invite_requests.status）。
const (
	WatchInvitePending         = "pending"
	WatchInviteWaitingTelegram = "waiting_telegram"
	WatchInviteWaitingBot      = "waiting_bot"
	WatchInviteApproved        = "approved"
	WatchInviteRejected        = "rejected"
	WatchInviteFailed          = "failed"
)

var watchInviteStatusSet = map[string]bool{
	WatchInvitePending:         true,
	WatchInviteWaitingTelegram: true,
	WatchInviteWaitingBot:      true,
	WatchInviteApproved:        true,
	WatchInviteRejected:        true,
	WatchInviteFailed:          true,
}

var activeWatchInviteStatuses = []string{
	WatchInvitePending,
	WatchInviteWaitingTelegram,
	WatchInviteWaitingBot,
}

// WatchInviteRequest 是私有邀请链接监听申请的持久化模型，JSON 形态即
// Bot/Web API 的安全视图：InviteHash 是执行加入流程所需的敏感值，禁止
// 序列化（json:"-"）；Title/RequestedAt 对外分别以 channel_title/created_at
// 下发；MaskedHash 在完整 hash 清理后仍保留，供安全展示。
type WatchInviteRequest struct {
	ID              int64  `json:"id"`
	UserID          int64  `json:"user_id"`
	UserUsername    string `json:"user_username"`
	UserDisplayName string `json:"user_display_name"`
	InviteHash      string `json:"-"`
	MaskedHash      string `json:"masked_hash"`
	Status          string `json:"status"`
	ChannelID       int64  `json:"channel_id"`
	Kind            string `json:"kind"`
	Username        string `json:"username"`
	Title           string `json:"channel_title"`
	Participants    int    `json:"participants"`
	Enabled         bool   `json:"enabled"`
	ReviewedBy      string `json:"reviewed_by"`
	Note            string `json:"note"`
	BotID           int64  `json:"bot_id"`
	BotUsername     string `json:"bot_username"`
	RequestedAt     int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`
}

const selectWatchInviteRequest = `SELECT r.id, r.user_id,
	COALESCE(u.username, ''), COALESCE(u.display_name, ''),
	COALESCE(r.invite_hash, ''), r.masked_hash, r.status,
	r.channel_id, r.kind, r.username, r.title,
	r.participants, r.enabled, r.reviewed_by, r.note,
	r.bot_id, r.bot_username, r.requested_at, r.updated_at
FROM watch_invite_requests r
LEFT JOIN users u ON u.id = r.user_id`

func scanWatchInviteRequest(row scanner) (WatchInviteRequest, error) {
	var (
		r       WatchInviteRequest
		enabled int64
	)
	err := row.Scan(&r.ID, &r.UserID, &r.UserUsername, &r.UserDisplayName,
		&r.InviteHash, &r.MaskedHash, &r.Status, &r.ChannelID, &r.Kind,
		&r.Username, &r.Title, &r.Participants, &enabled, &r.ReviewedBy, &r.Note,
		&r.BotID, &r.BotUsername, &r.RequestedAt, &r.UpdatedAt)
	r.Enabled = enabled == 1
	return r, err
}

// CreateWatchInviteRequest 写入一条私有邀请链接监听申请。状态缺省为 pending；
// Enabled 由调用方显式传值（服务层用户路径显式传 true，管理员路径可传
// false 预录入停用态），DAO 不做零值默认；MaskedHash 由完整 hash 经
// tmeurl.MaskInviteHash 生成，避免调用方误把敏感值作为 API 展示字段。
// UserID=0 为管理员路径，负数报受控内部错误。
func (s *Store) CreateWatchInviteRequest(ctx context.Context, in WatchInviteRequest) (WatchInviteRequest, error) {
	if in.UserID < 0 {
		return WatchInviteRequest{}, apperr.New(apperr.CodeInternal, "监听邀请申请用户 ID 不能为负")
	}
	if in.InviteHash == "" {
		return WatchInviteRequest{}, apperr.New(apperr.CodeInternal, "监听邀请申请缺少邀请 hash")
	}
	if in.Status == "" {
		in.Status = WatchInvitePending
	}
	if !watchInviteStatusSet[in.Status] {
		return WatchInviteRequest{}, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法监听邀请申请状态 %q", in.Status))
	}
	if in.RequestedAt == 0 {
		in.RequestedAt = nowMillis()
	}
	if in.UpdatedAt == 0 {
		in.UpdatedAt = in.RequestedAt
	}
	in.MaskedHash = tmeurl.MaskInviteHash(in.InviteHash)

	res, err := s.ex.ExecContext(ctx, `INSERT INTO watch_invite_requests
		(user_id, invite_hash, masked_hash, status, channel_id, kind, username, title,
		 participants, enabled, reviewed_by, note, bot_id, bot_username, requested_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		in.UserID, in.InviteHash, in.MaskedHash, in.Status, in.ChannelID,
		in.Kind, in.Username, in.Title, in.Participants, in.Enabled,
		in.ReviewedBy, in.Note, in.BotID, in.BotUsername, in.RequestedAt, in.UpdatedAt)
	if err != nil {
		if isConstraintErr(err) {
			return WatchInviteRequest{}, apperr.Wrap(apperr.CodeStoreConstraint,
				fmt.Errorf("创建监听邀请申请: %w", err))
		}
		return WatchInviteRequest{}, wrapDB("创建监听邀请申请", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return WatchInviteRequest{}, wrapDB("读取监听邀请申请 ID", err)
	}
	return s.GetWatchInviteRequest(ctx, id)
}

// GetWatchInviteRequest 按主键读取申请；不存在返回 ErrNotFound。
func (s *Store) GetWatchInviteRequest(ctx context.Context, id int64) (WatchInviteRequest, error) {
	r, err := scanWatchInviteRequest(s.ex.QueryRowContext(ctx,
		selectWatchInviteRequest+" WHERE r.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return WatchInviteRequest{}, ErrNotFound
	}
	if err != nil {
		return WatchInviteRequest{}, wrapDB("读取监听邀请申请", err)
	}
	return r, nil
}

// FindActiveWatchInviteRequestByHash 查找相同 hash 的活动申请（不限用户）；
// 不存在返回 ErrNotFound。活动状态为 pending/waiting_telegram/waiting_bot，
// 终态历史行不参与查重（同一邀请终态后可重新申请）。
func (s *Store) FindActiveWatchInviteRequestByHash(ctx context.Context, inviteHash string) (WatchInviteRequest, error) {
	r, err := scanWatchInviteRequest(s.ex.QueryRowContext(ctx,
		selectWatchInviteRequest+" WHERE r.invite_hash = ? AND "+activeWatchInviteWhere("r.status")+
			" ORDER BY r.requested_at, r.id LIMIT 1",
		append([]any{inviteHash}, activeWatchInviteArgs()...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return WatchInviteRequest{}, ErrNotFound
	}
	if err != nil {
		return WatchInviteRequest{}, wrapDB("查重活动监听邀请申请", err)
	}
	return r, nil
}

// ListActiveWatchInviteRequests 返回全部活动申请，按申请时间从早到晚排列，
// 供启动恢复与后台协调流程消费。
func (s *Store) ListActiveWatchInviteRequests(ctx context.Context) ([]WatchInviteRequest, error) {
	rows, err := s.ex.QueryContext(ctx,
		selectWatchInviteRequest+" WHERE "+activeWatchInviteWhere("r.status")+
			" ORDER BY r.requested_at, r.id", activeWatchInviteArgs()...)
	if err != nil {
		return nil, wrapDB("列出活动监听邀请申请", err)
	}
	defer rows.Close()

	out := []WatchInviteRequest{}
	for rows.Next() {
		r, err := scanWatchInviteRequest(rows)
		if err != nil {
			return nil, wrapDB("扫描监听邀请申请行", err)
		}
		out = append(out, r)
	}
	return out, wrapDB("遍历监听邀请申请行", rows.Err())
}

// ListWatchInviteRequests 返回全部状态的申请（Web 管理列表）：待审批在前，
// 其余按申请时间倒序（组内同序，保证展示稳定）。
func (s *Store) ListWatchInviteRequests(ctx context.Context) ([]WatchInviteRequest, error) {
	rows, err := s.ex.QueryContext(ctx, selectWatchInviteRequest+
		" ORDER BY CASE WHEN r.status = ? THEN 0 ELSE 1 END, r.requested_at DESC, r.id DESC",
		WatchInvitePending)
	if err != nil {
		return nil, wrapDB("列出监听邀请申请", err)
	}
	defer rows.Close()

	out := []WatchInviteRequest{}
	for rows.Next() {
		r, err := scanWatchInviteRequest(rows)
		if err != nil {
			return nil, wrapDB("扫描监听邀请申请行", err)
		}
		out = append(out, r)
	}
	return out, wrapDB("遍历监听邀请申请行", rows.Err())
}

// ListWatchInviteRequestsByUser 返回该用户全部状态的申请（Bot /watch 列表），
// 按申请时间倒序；userID<=0 报受控内部错误（0 是管理员路径专用值）。
func (s *Store) ListWatchInviteRequestsByUser(ctx context.Context, userID int64) ([]WatchInviteRequest, error) {
	if userID <= 0 {
		return nil, apperr.New(apperr.CodeInternal, "监听邀请申请用户 ID 必须为正")
	}
	rows, err := s.ex.QueryContext(ctx, selectWatchInviteRequest+
		" WHERE r.user_id = ? ORDER BY r.requested_at DESC, r.id DESC", userID)
	if err != nil {
		return nil, wrapDB("列出用户监听邀请申请", err)
	}
	defer rows.Close()

	out := []WatchInviteRequest{}
	for rows.Next() {
		r, err := scanWatchInviteRequest(rows)
		if err != nil {
			return nil, wrapDB("扫描监听邀请申请行", err)
		}
		out = append(out, r)
	}
	return out, wrapDB("遍历监听邀请申请行", rows.Err())
}

// CountActiveWatchInviteRequests 返回全局活动申请数。
func (s *Store) CountActiveWatchInviteRequests(ctx context.Context) (int, error) {
	return s.countActiveWatchInviteRequests(ctx, 0)
}

// CountActiveWatchInviteRequestsByUser 返回指定用户的活动申请数。
func (s *Store) CountActiveWatchInviteRequestsByUser(ctx context.Context, userID int64) (int, error) {
	if userID <= 0 {
		return 0, apperr.New(apperr.CodeInternal, "监听邀请申请用户 ID 必须为正")
	}
	return s.countActiveWatchInviteRequests(ctx, userID)
}

func (s *Store) countActiveWatchInviteRequests(ctx context.Context, userID int64) (int, error) {
	q := "SELECT COUNT(*) FROM watch_invite_requests WHERE " + activeWatchInviteWhere("status")
	args := activeWatchInviteArgs()
	if userID > 0 {
		q += " AND user_id = ?"
		args = append(args, userID)
	}
	var n int
	if err := s.ex.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, wrapDB("统计活动监听邀请申请", err)
	}
	return n, nil
}

// UpdateWatchInviteRequestStatus 更新申请状态，note 始终覆盖写入（空串即
// 清除），并刷新 updated_at；不存在返回 ErrNotFound。状态迁移合法性与
// hash 清理时机由服务层约束（保留策略见文件头注释）。
func (s *Store) UpdateWatchInviteRequestStatus(ctx context.Context, id int64, status, note string, updatedAt int64) (WatchInviteRequest, error) {
	if !watchInviteStatusSet[status] {
		return WatchInviteRequest{}, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法监听邀请申请状态 %q", status))
	}
	if updatedAt == 0 {
		updatedAt = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx,
		"UPDATE watch_invite_requests SET status = ?, note = ?, updated_at = ? WHERE id = ?",
		status, note, updatedAt, id)
	if err := affected(res, err, "更新监听邀请申请状态"); err != nil {
		return WatchInviteRequest{}, err
	}
	return s.GetWatchInviteRequest(ctx, id)
}

// SetWatchInviteRequestReview 只回写审批人标识（actor 为 Web 会话哈希或
// admin），状态不变、由状态更新单独承载；不存在返回 ErrNotFound。
func (s *Store) SetWatchInviteRequestReview(ctx context.Context, id int64, actor string, updatedAt int64) (WatchInviteRequest, error) {
	if updatedAt == 0 {
		updatedAt = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx,
		"UPDATE watch_invite_requests SET reviewed_by = ?, updated_at = ? WHERE id = ?",
		actor, updatedAt, id)
	if err := affected(res, err, "写入监听邀请申请审批人"); err != nil {
		return WatchInviteRequest{}, err
	}
	return s.GetWatchInviteRequest(ctx, id)
}

// UpdateWatchInviteRequestChannel 更新邀请解析得到的频道快照；不存在返回 ErrNotFound。
func (s *Store) UpdateWatchInviteRequestChannel(ctx context.Context, id, channelID int64, kind, username, title string, updatedAt int64) (WatchInviteRequest, error) {
	if channelID == 0 {
		return WatchInviteRequest{}, apperr.New(apperr.CodeInternal, "监听邀请申请频道 ID 不能为空")
	}
	if updatedAt == 0 {
		updatedAt = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx, `UPDATE watch_invite_requests SET
		channel_id = ?, kind = ?, username = ?, title = ?, updated_at = ? WHERE id = ?`,
		channelID, kind, username, title, updatedAt, id)
	if err := affected(res, err, "更新监听邀请申请频道信息"); err != nil {
		return WatchInviteRequest{}, err
	}
	return s.GetWatchInviteRequest(ctx, id)
}

// ClearWatchInviteRequestHash 清理完整邀请 hash，同时保留 MaskedHash 供安全
// 展示；不存在返回 ErrNotFound。调用时机见文件头 hash 保留策略（由服务层
// 在 waiting_bot/approved/rejected 流转时执行）。
func (s *Store) ClearWatchInviteRequestHash(ctx context.Context, id, updatedAt int64) (WatchInviteRequest, error) {
	if updatedAt == 0 {
		updatedAt = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx,
		"UPDATE watch_invite_requests SET invite_hash = NULL, updated_at = ? WHERE id = ?",
		updatedAt, id)
	if err := affected(res, err, "清理监听邀请申请 hash"); err != nil {
		return WatchInviteRequest{}, err
	}
	return s.GetWatchInviteRequest(ctx, id)
}

// DeleteWatchInviteRequest 硬删除指定申请；不存在返回 ErrNotFound。
func (s *Store) DeleteWatchInviteRequest(ctx context.Context, id int64) error {
	res, err := s.ex.ExecContext(ctx, "DELETE FROM watch_invite_requests WHERE id = ?", id)
	return affected(res, err, "删除监听邀请申请")
}

func activeWatchInviteWhere(column string) string {
	return column + " IN (" + strings.TrimRight(strings.Repeat("?,", len(activeWatchInviteStatuses)), ",") + ")"
}

func activeWatchInviteArgs() []any {
	args := make([]any, len(activeWatchInviteStatuses))
	for i, status := range activeWatchInviteStatuses {
		args[i] = status
	}
	return args
}
