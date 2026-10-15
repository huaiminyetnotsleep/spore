package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// watch_sources.go — 监听源预热缓存（/watch）：配置的源频道/超级群组由
// bot 接收新帖并自动转储缓存频道（dumpcache）预热 dump_entries。行状态：
// 管理员 Web 直接添加天然 approved；用户 /watch 申请按配置走审批
//（pending → approved/rejected）。added_by=0 表示管理员添加，>0 为申请人。

// 监听源行状态。
const (
	WatchPending  = "pending"  // 用户申请待审批
	WatchApproved = "approved" // 生效（配合 enabled 暂停开关）
	WatchRejected = "rejected" // 申请被拒（终态，可删）
)

// watchStatusSet 是合法状态白名单。
var watchStatusSet = map[string]bool{
	WatchPending:  true,
	WatchApproved: true,
	WatchRejected: true,
}

// WatchSource 是 watch_sources 表的行模型。
type WatchSource struct {
	ChannelID   int64  `json:"channel_id"`   // Bot API -100 形态频道/群组 ID
	Kind        string `json:"kind"`         // channel / supergroup（展示用；空 = 旧数据）
	Username    string `json:"username"`     // 公开源用户名（无 @）；私有源为空
	Title       string `json:"title"`        // 展示标题（配置时快照）
	Status      string `json:"status"`       // pending / approved / rejected
	Enabled     bool   `json:"enabled"`      // approved 行的独立暂停开关
	AddedBy     int64  `json:"added_by"`     // 0 = 管理员 Web 添加；>0 = 申请人用户 ID
	BotID       int64  `json:"bot_id"`       // 用户 /watch 的受理 bot ID；0 = Web 管理端添加
	BotUsername string `json:"bot_username"` // 受理 bot 用户名快照（展示自持，bot 移出池后历史仍可读）
	ReviewedBy  string `json:"reviewed_by"`  // 审批人标识（idHash 或 admin）；未审批为空
	// ForwardBindingChannelID 是源申请人选择的专属转发目标（channel_bindings
	// 引用，仅用户源可设置）；0 = 未设置。引用有效性（active/归属/路由）由
	// watch 服务在设置时校验、在投递时复查。
	ForwardBindingChannelID int64 `json:"forward_binding_channel_id"`
	// GlobalMirrorEnabled 是管理员逐源控制的「额外镜像到全局监听转发频道」
	// 开关：管理员源（added_by=0）默认开以保持历史行为，用户源默认关。
	GlobalMirrorEnabled bool  `json:"global_mirror_enabled"`
	CreatedAt           int64 `json:"created_at"`
	UpdatedAt           int64 `json:"updated_at"`
}

// WatchSourceWithUser 是 Web 管理端列表行：监听源 + 申请人展示资料
// （申请人可能已被硬删除，LEFT JOIN 后资料为零值，展示退化为 user_id）。
type WatchSourceWithUser struct {
	WatchSource
	UserUsername    string `json:"user_username"`
	UserDisplayName string `json:"user_display_name"`
}

const selectWatchSource = `SELECT channel_id,
	COALESCE(kind, ''), COALESCE(username, ''), COALESCE(title, ''), status, enabled,
	added_by, bot_id, bot_username, COALESCE(reviewed_by, ''),
	COALESCE(forward_binding_channel_id, 0), global_mirror_enabled,
	created_at, updated_at
FROM watch_sources`

func scanWatchSource(row scanner) (WatchSource, error) {
	var s WatchSource
	err := row.Scan(&s.ChannelID, &s.Kind, &s.Username, &s.Title, &s.Status, &s.Enabled,
		&s.AddedBy, &s.BotID, &s.BotUsername, &s.ReviewedBy,
		&s.ForwardBindingChannelID, &s.GlobalMirrorEnabled,
		&s.CreatedAt, &s.UpdatedAt)
	return s, err
}

const selectWatchSourceWithUser = `SELECT w.channel_id,
	COALESCE(w.kind, ''), COALESCE(w.username, ''), COALESCE(w.title, ''), w.status, w.enabled,
	w.added_by, w.bot_id, w.bot_username, COALESCE(w.reviewed_by, ''),
	COALESCE(w.forward_binding_channel_id, 0), w.global_mirror_enabled,
	w.created_at, w.updated_at,
	COALESCE(u.username, ''), COALESCE(u.display_name, '')
FROM watch_sources w
LEFT JOIN users u ON u.id = w.added_by`

func scanWatchSourceWithUser(row scanner) (WatchSourceWithUser, error) {
	var s WatchSourceWithUser
	err := row.Scan(&s.ChannelID, &s.Kind, &s.Username, &s.Title, &s.Status, &s.Enabled,
		&s.AddedBy, &s.BotID, &s.BotUsername, &s.ReviewedBy,
		&s.ForwardBindingChannelID, &s.GlobalMirrorEnabled,
		&s.CreatedAt, &s.UpdatedAt,
		&s.UserUsername, &s.UserDisplayName)
	return s, err
}

// UpsertWatchSource 写入或刷新监听源（幂等）：同一频道重复添加只刷新
// username/title/status/enabled/added_by 与 updated_at，保留 created_at。
// 首次插入按 added_by 推导策略默认值（管理员源全局镜像开、用户源关，
// 专属目标恒空）；冲突更新路径不触碰 forward_binding_channel_id 与
// global_mirror_enabled——重复 /watch 不得覆盖用户目标选择或管理员开关。
func (s *Store) UpsertWatchSource(ctx context.Context, in WatchSource) (WatchSource, error) {
	if !watchStatusSet[in.Status] {
		return WatchSource{}, wrapDB("写入监听源", errors.New("非法状态 "+in.Status))
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = nowMillis()
	}
	in.UpdatedAt = nowMillis()
	_, err := s.ex.ExecContext(ctx, `INSERT INTO watch_sources
		(channel_id, kind, username, title, status, enabled, added_by, bot_id, bot_username,
		 reviewed_by, created_at, updated_at, forward_binding_channel_id, global_mirror_enabled)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,NULL,?)
		ON CONFLICT(channel_id) DO UPDATE SET
			kind = excluded.kind,
			username = excluded.username,
			title = excluded.title,
			status = excluded.status,
			enabled = excluded.enabled,
			added_by = excluded.added_by,
			bot_id = excluded.bot_id,
			bot_username = excluded.bot_username,
			reviewed_by = excluded.reviewed_by,
			updated_at = excluded.updated_at`,
		in.ChannelID, in.Kind, nullStr(in.Username), nullStr(in.Title), in.Status, in.Enabled,
		in.AddedBy, in.BotID, in.BotUsername, nullStr(in.ReviewedBy), in.CreatedAt, in.UpdatedAt,
		in.AddedBy == 0)
	if err != nil {
		return WatchSource{}, wrapDB("写入监听源", err)
	}
	// 回读落库行：冲突更新路径的 created_at 保留首次写入值，内存构造值
	// 会带本次填充的时间戳，直接返回会误导调用方。
	return s.GetWatchSource(ctx, in.ChannelID)
}

// GetWatchSource 按频道 ID 读取监听源；不存在返回 ErrNotFound。
func (s *Store) GetWatchSource(ctx context.Context, channelID int64) (WatchSource, error) {
	src, err := scanWatchSource(s.ex.QueryRowContext(ctx,
		selectWatchSource+" WHERE channel_id = ?", channelID))
	if errors.Is(err, sql.ErrNoRows) {
		return WatchSource{}, ErrNotFound
	}
	if err != nil {
		return WatchSource{}, wrapDB("读取监听源", err)
	}
	return src, nil
}

// ListWatchSources 返回全部监听源（含各状态），按添加时间排序。
func (s *Store) ListWatchSources(ctx context.Context) ([]WatchSource, error) {
	rows, err := s.ex.QueryContext(ctx, selectWatchSource+" ORDER BY created_at, channel_id")
	if err != nil {
		return nil, wrapDB("列出监听源", err)
	}
	defer rows.Close()
	out := []WatchSource{}
	for rows.Next() {
		src, err := scanWatchSource(rows)
		if err != nil {
			return nil, wrapDB("扫描监听源行", err)
		}
		out = append(out, src)
	}
	return out, wrapDB("遍历监听源行", rows.Err())
}

// ListWatchSourcesWithUser 返回全部监听源（含申请人资料），按添加时间倒序
// （Web 管理端列表，待审批在前展示由调用方排序）。
func (s *Store) ListWatchSourcesWithUser(ctx context.Context) ([]WatchSourceWithUser, error) {
	rows, err := s.ex.QueryContext(ctx,
		selectWatchSourceWithUser+" ORDER BY w.created_at DESC, w.channel_id")
	if err != nil {
		return nil, wrapDB("列出监听源", err)
	}
	defer rows.Close()
	out := []WatchSourceWithUser{}
	for rows.Next() {
		src, err := scanWatchSourceWithUser(rows)
		if err != nil {
			return nil, wrapDB("扫描监听源行", err)
		}
		out = append(out, src)
	}
	return out, wrapDB("遍历监听源行", rows.Err())
}

// ListWatchSourcesByUser 返回该用户名下（申请/生效）的监听源，按添加时间排序
// （Bot /watch 无参数列表路径）。
func (s *Store) ListWatchSourcesByUser(ctx context.Context, addedBy int64) ([]WatchSource, error) {
	rows, err := s.ex.QueryContext(ctx,
		selectWatchSource+" WHERE added_by = ? ORDER BY created_at, channel_id", addedBy)
	if err != nil {
		return nil, wrapDB("列出用户监听源", err)
	}
	defer rows.Close()
	out := []WatchSource{}
	for rows.Next() {
		src, err := scanWatchSource(rows)
		if err != nil {
			return nil, wrapDB("扫描监听源行", err)
		}
		out = append(out, src)
	}
	return out, wrapDB("遍历监听源行", rows.Err())
}

// CountWatchSources 统计处于指定状态集合的监听源数（上限校验；
// statuses 为空按全部计）。
func (s *Store) CountWatchSources(ctx context.Context, statuses []string) (int, error) {
	q := "SELECT COUNT(*) FROM watch_sources"
	var args []any
	if len(statuses) > 0 {
		q += " WHERE status IN ("
		for i, st := range statuses {
			if i > 0 {
				q += ","
			}
			q += "?"
			args = append(args, st)
		}
		q += ")"
	}
	var n int
	if err := s.ex.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, wrapDB("统计监听源", err)
	}
	return n, nil
}

// CountWatchSourcesByUser 统计该用户名下处于指定状态集合的监听源数
// （每用户上限校验）。
func (s *Store) CountWatchSourcesByUser(ctx context.Context, addedBy int64, statuses []string) (int, error) {
	q := "SELECT COUNT(*) FROM watch_sources WHERE added_by = ?"
	args := []any{addedBy}
	if len(statuses) > 0 {
		q += " AND status IN ("
		for i, st := range statuses {
			if i > 0 {
				q += ","
			}
			q += "?"
			args = append(args, st)
		}
		q += ")"
	}
	var n int
	if err := s.ex.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return 0, wrapDB("统计用户监听源", err)
	}
	return n, nil
}

// ReviewWatchSource 把 pending 行流转为 approved/rejected 并记录审批人；
// 非 pending（已审/管理员直接添加）返回 STORE_CONSTRAINT，防止重复审批
// 覆盖既有结论。
func (s *Store) ReviewWatchSource(ctx context.Context, channelID int64, status, reviewedBy string) (WatchSource, error) {
	if status != WatchApproved && status != WatchRejected {
		return WatchSource{}, wrapDB("审批监听源", errors.New("非法目标状态 "+status))
	}
	res, err := s.ex.ExecContext(ctx, `UPDATE watch_sources
		SET status = ?, reviewed_by = ?, updated_at = ?
		WHERE channel_id = ? AND status = ?`,
		status, nullStr(reviewedBy), nowMillis(), channelID, WatchPending)
	if err != nil {
		return WatchSource{}, wrapDB("审批监听源", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return WatchSource{}, apperr.Wrap(apperr.CodeStoreConstraint,
			fmt.Errorf("监听源 %d 不存在或不是待审批状态", channelID))
	}
	return s.GetWatchSource(ctx, channelID)
}

// SetWatchSourceEnabled 切换 approved 行的暂停开关（不影响状态）。
func (s *Store) SetWatchSourceEnabled(ctx context.Context, channelID int64, enabled bool) (WatchSource, error) {
	res, err := s.ex.ExecContext(ctx,
		"UPDATE watch_sources SET enabled = ?, updated_at = ? WHERE channel_id = ?",
		enabled, nowMillis(), channelID)
	if err != nil {
		return WatchSource{}, wrapDB("更新监听源开关", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return WatchSource{}, ErrNotFound
	}
	return s.GetWatchSource(ctx, channelID)
}

// SetWatchSourceForwardBinding 设置（bindingChannelID>0）或清空（=0）源的
// 专属转发目标引用。DAO 只落引用不做有效性校验——active/归属/路由/循环
// 校验由 watch 服务在调用前完成。行不存在返回 ErrNotFound。
func (s *Store) SetWatchSourceForwardBinding(ctx context.Context, channelID, bindingChannelID int64) (WatchSource, error) {
	res, err := s.ex.ExecContext(ctx,
		"UPDATE watch_sources SET forward_binding_channel_id = ?, updated_at = ? WHERE channel_id = ?",
		nullInt64(bindingChannelID), nowMillis(), channelID)
	if err := affected(res, err, "更新监听源专属目标"); err != nil {
		return WatchSource{}, err
	}
	return s.GetWatchSource(ctx, channelID)
}

// SetWatchSourceGlobalMirror 管理员逐源切换「额外镜像到全局监听转发频道」
// 开关；行不存在返回 ErrNotFound。
func (s *Store) SetWatchSourceGlobalMirror(ctx context.Context, channelID int64, enabled bool) (WatchSource, error) {
	res, err := s.ex.ExecContext(ctx,
		"UPDATE watch_sources SET global_mirror_enabled = ?, updated_at = ? WHERE channel_id = ?",
		enabled, nowMillis(), channelID)
	if err := affected(res, err, "更新监听源全局镜像开关"); err != nil {
		return WatchSource{}, err
	}
	return s.GetWatchSource(ctx, channelID)
}

// ForwardTargetInUse 报告 channelID 是否被任一监听源选为专属转发目标，或
// 被任一活动私有邀请申请暂存为目标（循环防护：这样的频道不能再成为监听
// 源，否则「源 → 目标（另一源）」形成转发环）。
func (s *Store) ForwardTargetInUse(ctx context.Context, channelID int64) (bool, error) {
	var inUse bool
	err := s.ex.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM watch_sources WHERE forward_binding_channel_id = ?)
		OR EXISTS(SELECT 1 FROM watch_invite_requests WHERE forward_binding_channel_id = ? AND `+
		activeWatchInviteWhere("status")+")",
		append([]any{channelID, channelID}, activeWatchInviteArgs()...)...).Scan(&inUse)
	if err != nil {
		return false, wrapDB("查询专属目标占用", err)
	}
	return inUse, nil
}

// DeleteWatchSource 删除监听源并返回删除前的记录；不存在返回 ErrNotFound。
func (s *Store) DeleteWatchSource(ctx context.Context, channelID int64) (WatchSource, error) {
	src, err := s.GetWatchSource(ctx, channelID)
	if err != nil {
		return WatchSource{}, err
	}
	res, err := s.ex.ExecContext(ctx, "DELETE FROM watch_sources WHERE channel_id = ?", channelID)
	if err := affected(res, err, "删除监听源"); err != nil {
		return WatchSource{}, err
	}
	return src, nil
}

// DumpEntryStats 是一组频道键在 dump_entries 中的聚合：条数与最近写入
// 时间（监听源健康度展示——最近预热时间停更意味着监听可能已静默失效，
// 如 bot 被移出源管理员）。
type DumpEntryStats struct {
	Count  int
	LastAt int64
}

// DumpEntryStatsByKeys 按频道键集合聚合 dump_entries（一次分组查询；
// 双键源由调用方把两个键都传入并自行合并）。
func (s *Store) DumpEntryStatsByKeys(ctx context.Context, keys []string) (map[string]DumpEntryStats, error) {
	out := make(map[string]DumpEntryStats, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	q := "SELECT channel_key, COUNT(*), COALESCE(MAX(created_at), 0) FROM dump_entries WHERE channel_key IN ("
	args := make([]any, 0, len(keys))
	for i, k := range keys {
		if i > 0 {
			q += ","
		}
		q += "?"
		args = append(args, k)
	}
	q += ") GROUP BY channel_key"
	rows, err := s.ex.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapDB("聚合监听源预热统计", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var st DumpEntryStats
		if err := rows.Scan(&k, &st.Count, &st.LastAt); err != nil {
			return nil, wrapDB("扫描监听源预热统计", err)
		}
		out[k] = st
	}
	return out, wrapDB("遍历监听源预热统计", rows.Err())
}
