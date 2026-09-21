package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// 加入申请状态（join_requests.status）。
const (
	JoinPending  = "pending"  // 待审批
	JoinApproved = "approved" // 已同意并加入成功
	JoinRejected = "rejected" // 已拒绝
	JoinFailed   = "failed"   // 同意后加入失败（链接失效等）
)

// joinedVia 是频道加入来源（joined_channels.joined_via）。
const (
	JoinedViaCommand     = "join_command" // owner 经 /join 即时加入
	JoinedViaApproved    = "approved"     // 普通用户申请经审批加入
	JoinedViaExternal    = "external"     // 检测到的外部拉入（非本系统加入）
	JoinedViaWatchSource = "watch_source" // 私有邀请监听源流程加入
)

// joinStatusSet 合法状态白名单。
var joinStatusSet = map[string]bool{
	JoinPending: true, JoinApproved: true, JoinRejected: true, JoinFailed: true,
}

// joinedViaSet 合法来源白名单。
var joinedViaSet = map[string]bool{
	JoinedViaCommand:     true,
	JoinedViaApproved:    true,
	JoinedViaExternal:    true,
	JoinedViaWatchSource: true,
}

// JoinRequest 是 join_requests 表的行模型。
type JoinRequest struct {
	ID           int64
	UserID       int64
	InviteHash   string // 敏感：仅供审批执行，展示层必须脱敏
	ChannelTitle string
	Participants int
	Status       string
	RequestedAt  int64
	ReviewedAt   int64
	ReviewedBy   string
	Note         string
}

// JoinRequestWithUser 是 Web 审批页列表行：申请 + 提交用户资料。
type JoinRequestWithUser struct {
	JoinRequest
	UserUsername    string
	UserDisplayName string
}

// JoinedChannelRecord 是 joined_channels 表的行模型（留痕记录，
// 实时列表以 MTProto 对话遍历为准）。
type JoinedChannelRecord struct {
	ChannelID int64
	Title     string
	Username  string
	Kind      string
	JoinedVia string
	JoinedBy  int64 // 0 表示系统未关联用户（external 来源）
	JoinedAt  int64
	LeftAt    int64 // 0 表示当前仍加入
}

const selectJoinRequest = `SELECT id, user_id, invite_hash,
	COALESCE(channel_title, ''), COALESCE(participants, 0), status,
	requested_at, COALESCE(reviewed_at, 0), COALESCE(reviewed_by, ''), COALESCE(note, '')
FROM join_requests`

func scanJoinRequest(row scanner) (JoinRequest, error) {
	var r JoinRequest
	err := row.Scan(&r.ID, &r.UserID, &r.InviteHash, &r.ChannelTitle, &r.Participants,
		&r.Status, &r.RequestedAt, &r.ReviewedAt, &r.ReviewedBy, &r.Note)
	return r, err
}

const selectJoinRequestWithUser = `SELECT r.id, r.user_id, r.invite_hash,
	COALESCE(r.channel_title, ''), COALESCE(r.participants, 0), r.status,
	r.requested_at, COALESCE(r.reviewed_at, 0), COALESCE(r.reviewed_by, ''), COALESCE(r.note, ''),
	COALESCE(u.username, ''), COALESCE(u.display_name, '')
FROM join_requests r
LEFT JOIN users u ON u.id = r.user_id`

func scanJoinRequestWithUser(row scanner) (JoinRequestWithUser, error) {
	var r JoinRequestWithUser
	err := row.Scan(&r.ID, &r.UserID, &r.InviteHash, &r.ChannelTitle, &r.Participants,
		&r.Status, &r.RequestedAt, &r.ReviewedAt, &r.ReviewedBy, &r.Note,
		&r.UserUsername, &r.UserDisplayName)
	return r, err
}

// CreateJoinRequest 写入一条待审批申请；同用户存在未处理的同 hash 申请时
// 返回已有记录与 created=false（幂等去重）。
func (s *Store) CreateJoinRequest(ctx context.Context, in JoinRequest) (JoinRequest, bool, error) {
	if in.UserID <= 0 || in.InviteHash == "" {
		return JoinRequest{}, false, apperr.New(apperr.CodeInternal, "加入申请缺少用户或邀请链接")
	}
	if in.RequestedAt == 0 {
		in.RequestedAt = nowMillis()
	}
	in.Status = JoinPending

	existing, err := scanJoinRequest(s.ex.QueryRowContext(ctx,
		selectJoinRequest+" WHERE user_id = ? AND invite_hash = ? AND status = ?",
		in.UserID, in.InviteHash, JoinPending))
	switch {
	case err == nil:
		return existing, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return JoinRequest{}, false, wrapDB("查重加入申请", err)
	}

	res, err := s.ex.ExecContext(ctx, `INSERT INTO join_requests
		(user_id, invite_hash, channel_title, participants, status, requested_at)
		VALUES (?,?,?,?,?,?)`,
		in.UserID, in.InviteHash, nullStr(in.ChannelTitle), in.Participants,
		in.Status, in.RequestedAt)
	if err != nil {
		return JoinRequest{}, false, wrapDB("写入加入申请", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return JoinRequest{}, false, wrapDB("读取加入申请 ID", err)
	}
	in.ID = id
	return in, true, nil
}

// GetJoinRequest 按主键读取；不存在返回 ErrNotFound。
func (s *Store) GetJoinRequest(ctx context.Context, id int64) (JoinRequest, error) {
	r, err := scanJoinRequest(s.ex.QueryRowContext(ctx,
		selectJoinRequest+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return JoinRequest{}, ErrNotFound
	}
	if err != nil {
		return JoinRequest{}, wrapDB("读取加入申请", err)
	}
	return r, nil
}

// JoinRequestFilter 加入申请列表的筛选与分页条件；零值字段表示该条件不限。
type JoinRequestFilter struct {
	Status       string // 空为全部；非法值报错
	UserID       int64  // >0 时精确匹配申请人
	TitleKeyword string // 非空时对频道标题做模糊匹配（%/_ 通配已转义）
	NotePrefix   string // 非空时按 note 前缀匹配（请求制频道的懒对账查询）
	Since, Until int64  // requested_at 范围（Unix 毫秒；0=不限）
	Limit        int    // >0 时分页
	Offset       int
}

// validate 校验筛选取值，返回受控错误（调用方 400 直用）。
func (f JoinRequestFilter) validate() error {
	if f.Status != "" && !joinStatusSet[f.Status] {
		return apperr.New(apperr.CodeInternal, fmt.Sprintf("非法状态筛选 %q", f.Status))
	}
	return nil
}

// where 组装筛选条件 SQL 与绑定参数（TitleKeyword 的 LIKE 值带转义）。
func (f JoinRequestFilter) where() (string, []any) {
	clauses := []string{}
	args := []any{}
	if f.Status != "" {
		clauses = append(clauses, "r.status = ?")
		args = append(args, f.Status)
	}
	if f.UserID > 0 {
		clauses = append(clauses, "r.user_id = ?")
		args = append(args, f.UserID)
	}
	if f.TitleKeyword != "" {
		clauses = append(clauses, `COALESCE(r.channel_title, '') LIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(f.TitleKeyword)+"%")
	}
	if f.NotePrefix != "" {
		clauses = append(clauses, `COALESCE(r.note, '') LIKE ? ESCAPE '\'`)
		args = append(args, escapeLike(f.NotePrefix)+"%")
	}
	if f.Since > 0 {
		clauses = append(clauses, "r.requested_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		clauses = append(clauses, "r.requested_at < ?")
		args = append(args, f.Until)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// escapeLike 转义 LIKE 通配符，防用户输入中的 %/_ 扩大匹配范围。
func escapeLike(v string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(v)
}

// ListJoinRequests 按筛选条件返回加入申请（含申请人资料），时间倒序；
// Limit <= 0 时取全量。
func (s *Store) ListJoinRequests(ctx context.Context, f JoinRequestFilter) ([]JoinRequestWithUser, error) {
	if err := f.validate(); err != nil {
		return nil, err
	}
	where, args := f.where()
	q := selectJoinRequestWithUser + where + " ORDER BY r.requested_at DESC, r.id DESC"
	if f.Limit > 0 {
		q += " LIMIT ? OFFSET ?"
		args = append(args, f.Limit, f.Offset)
	}
	rows, err := s.ex.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, wrapDB("列出加入申请", err)
	}
	defer rows.Close()
	out := []JoinRequestWithUser{}
	for rows.Next() {
		r, err := scanJoinRequestWithUser(rows)
		if err != nil {
			return nil, wrapDB("扫描加入申请行", err)
		}
		out = append(out, r)
	}
	return out, wrapDB("遍历加入申请行", rows.Err())
}

// CountJoinRequests 返回同筛选条件下的总数（分页信封用，忽略 Limit/Offset）。
func (s *Store) CountJoinRequests(ctx context.Context, f JoinRequestFilter) (int, error) {
	if err := f.validate(); err != nil {
		return 0, err
	}
	where, args := f.where()
	var n int
	err := s.ex.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM join_requests r"+where, args...).Scan(&n)
	if err != nil {
		return 0, wrapDB("统计加入申请", err)
	}
	return n, nil
}

// ReviewJoinRequest 把申请从 pending 迁移到终态；并返回更新后的记录。
// 仅 pending 可迁移（重复审批返回 STORE_CONSTRAINT）；reviewedBy 为审批人
// 展示名（Web 管理端会话名），note 记录失败原因等备注。
func (s *Store) ReviewJoinRequest(ctx context.Context, id int64, status, reviewedBy, note string, reviewedAt int64) (JoinRequest, error) {
	if !joinStatusSet[status] || status == JoinPending {
		return JoinRequest{}, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法终态 %q", status))
	}
	if reviewedAt == 0 {
		reviewedAt = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx, `UPDATE join_requests SET
		status = ?, reviewed_at = ?, reviewed_by = ?, note = ?
		WHERE id = ? AND status = ?`,
		status, reviewedAt, reviewedBy, nullStr(note), id, JoinPending)
	if err != nil {
		return JoinRequest{}, wrapDB("更新加入申请", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return JoinRequest{}, apperr.Wrap(apperr.CodeStoreConstraint,
			errors.New("该申请已被处理或不存在"))
	}
	return s.GetJoinRequest(ctx, id)
}

// UpdateJoinRequestNote 更新已终态申请的备注（请求制频道懒对账回写）。
// pending 申请的备注由审批动作写入，此处不放开。
func (s *Store) UpdateJoinRequestNote(ctx context.Context, id int64, note string, reviewedAt int64) error {
	if reviewedAt == 0 {
		reviewedAt = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx,
		"UPDATE join_requests SET note = ?, reviewed_at = ? WHERE id = ? AND status <> ?",
		nullStr(note), reviewedAt, id, JoinPending)
	return affected(res, err, "更新加入申请备注")
}

// DeleteJoinRequest 硬删除指定加入申请记录；不存在返回 ErrNotFound。
// pending 状态的记录是否可删由业务层（joinmgr）约束，DAO 不做状态判断。
func (s *Store) DeleteJoinRequest(ctx context.Context, id int64) error {
	res, err := s.ex.ExecContext(ctx, "DELETE FROM join_requests WHERE id = ?", id)
	return affected(res, err, "删除加入申请")
}

// UpsertJoinedChannel 写入/刷新已加入频道留痕：重新加入时清空 left_at 并
// 更新展示字段（同频道可能先退出再加入）。
func (s *Store) UpsertJoinedChannel(ctx context.Context, in JoinedChannelRecord) error {
	if in.ChannelID <= 0 {
		return apperr.New(apperr.CodeInternal, "频道 ID 必须为正")
	}
	if !joinedViaSet[in.JoinedVia] {
		return apperr.New(apperr.CodeInternal, fmt.Sprintf("非法加入来源 %q", in.JoinedVia))
	}
	if in.JoinedAt == 0 {
		in.JoinedAt = nowMillis()
	}
	_, err := s.ex.ExecContext(ctx, `INSERT INTO joined_channels
		(channel_id, title, username, kind, joined_via, joined_by, joined_at, left_at)
		VALUES (?,?,?,?,?,?,?,0)
		ON CONFLICT(channel_id) DO UPDATE SET
			title = excluded.title, username = excluded.username, kind = excluded.kind,
			joined_via = excluded.joined_via, joined_by = excluded.joined_by,
			joined_at = excluded.joined_at, left_at = 0`,
		in.ChannelID, nullStr(in.Title), nullStr(in.Username), in.Kind,
		in.JoinedVia, nullInt64(in.JoinedBy), in.JoinedAt)
	if err != nil {
		return wrapDB("写入已加入频道留痕", err)
	}
	return nil
}

// CountActiveJoinedChannels 统计当前加入中的频道数（left_at = 0）。
func (s *Store) CountActiveJoinedChannels(ctx context.Context) (int, error) {
	var n int
	err := s.ex.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM joined_channels WHERE left_at = 0").Scan(&n)
	if err != nil {
		return 0, wrapDB("统计已加入频道", err)
	}
	return n, nil
}

// JoinRequestTally 是加入申请按状态分组的计数（全时段快照口径）。
type JoinRequestTally struct {
	Pending  int
	Approved int
	Rejected int
	Failed   int
}

// TallyJoinRequests 按状态分组统计加入申请数量（全时段，总览快照用）。
// 未知状态行不落入任何字段（历史数据防御）。
func (s *Store) TallyJoinRequests(ctx context.Context) (JoinRequestTally, error) {
	rows, err := s.ex.QueryContext(ctx,
		"SELECT status, COUNT(*) FROM join_requests GROUP BY status")
	if err != nil {
		return JoinRequestTally{}, wrapDB("统计加入申请状态", err)
	}
	defer rows.Close()
	var out JoinRequestTally
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return JoinRequestTally{}, wrapDB("扫描加入申请状态计数", err)
		}
		switch status {
		case JoinPending:
			out.Pending = n
		case JoinApproved:
			out.Approved = n
		case JoinRejected:
			out.Rejected = n
		case JoinFailed:
			out.Failed = n
		}
	}
	return out, wrapDB("遍历加入申请状态计数", rows.Err())
}

// JoinedChannelTally 是已加入频道按来源拆分的"当前加入/已退出"计数
// （留痕表全量，总览快照口径）。
type JoinedChannelTally struct {
	CommandActive     int
	CommandLeft       int
	ApprovedActive    int
	ApprovedLeft      int
	ExternalActive    int
	ExternalLeft      int
	WatchSourceActive int
	WatchSourceLeft   int
}

// Active 返回当前加入中的频道总数（全部来源合计）。
func (t JoinedChannelTally) Active() int {
	return t.CommandActive + t.ApprovedActive + t.ExternalActive + t.WatchSourceActive
}

// Left 返回已退出的频道总数（全部来源合计）。
func (t JoinedChannelTally) Left() int {
	return t.CommandLeft + t.ApprovedLeft + t.ExternalLeft + t.WatchSourceLeft
}

// TallyJoinedChannels 按 joined_via 分组统计已加入频道的当前加入/已退出
// 数量（全时段留痕）。joined_via 经白名单 switch 落字段，未知来源行忽略。
func (s *Store) TallyJoinedChannels(ctx context.Context) (JoinedChannelTally, error) {
	rows, err := s.ex.QueryContext(ctx, `SELECT joined_via,
			COALESCE(SUM(left_at = 0), 0), COALESCE(SUM(left_at > 0), 0)
		FROM joined_channels GROUP BY joined_via`)
	if err != nil {
		return JoinedChannelTally{}, wrapDB("统计已加入频道来源", err)
	}
	defer rows.Close()
	var out JoinedChannelTally
	for rows.Next() {
		var via string
		var active, left int
		if err := rows.Scan(&via, &active, &left); err != nil {
			return JoinedChannelTally{}, wrapDB("扫描已加入频道来源计数", err)
		}
		switch via {
		case JoinedViaCommand:
			out.CommandActive, out.CommandLeft = active, left
		case JoinedViaApproved:
			out.ApprovedActive, out.ApprovedLeft = active, left
		case JoinedViaExternal:
			out.ExternalActive, out.ExternalLeft = active, left
		case JoinedViaWatchSource:
			out.WatchSourceActive, out.WatchSourceLeft = active, left
		}
	}
	return out, wrapDB("遍历已加入频道来源计数", rows.Err())
}

// ListActiveJoinedChannels 返回留痕中"当前仍加入"的记录（含 external 来源），
// 供 Enforce 与实时列表标注来源使用。
func (s *Store) ListActiveJoinedChannels(ctx context.Context) ([]JoinedChannelRecord, error) {
	rows, err := s.ex.QueryContext(ctx, `SELECT channel_id,
		COALESCE(title, ''), COALESCE(username, ''), kind, joined_via,
		COALESCE(joined_by, 0), joined_at
		FROM joined_channels WHERE left_at = 0
		ORDER BY joined_at, channel_id`)
	if err != nil {
		return nil, wrapDB("列出已加入频道", err)
	}
	defer rows.Close()
	out := []JoinedChannelRecord{}
	for rows.Next() {
		var r JoinedChannelRecord
		if err := rows.Scan(&r.ChannelID, &r.Title, &r.Username, &r.Kind, &r.JoinedVia,
			&r.JoinedBy, &r.JoinedAt); err != nil {
			return nil, wrapDB("扫描已加入频道行", err)
		}
		out = append(out, r)
	}
	return out, wrapDB("遍历已加入频道行", rows.Err())
}

// MarkJoinedChannelsLeft 把指定频道留痕置为已退出（left_at），返回实际
// 更新的行数；不存在的 ID 静默跳过（实时列表为准，留痕可能滞后）。
func (s *Store) MarkJoinedChannelsLeft(ctx context.Context, channelIDs []int64, leftAt int64) (int, error) {
	if len(channelIDs) == 0 {
		return 0, nil
	}
	if leftAt == 0 {
		leftAt = nowMillis()
	}
	// IN 参数由本包拼装（无外部输入拼接风险），逐条更新保持批量小事务
	updated := 0
	for _, id := range channelIDs {
		res, err := s.ex.ExecContext(ctx,
			"UPDATE joined_channels SET left_at = ? WHERE channel_id = ? AND left_at = 0",
			leftAt, id)
		if err != nil {
			return updated, wrapDB("标记已退出频道", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			updated++
		}
	}
	return updated, nil
}
