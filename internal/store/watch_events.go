package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// watch_events.go — 预热事件记录：监听转储的逐次留痕（哪个 bot、哪个源、
// 转发了哪些消息、缓存频道落点、路径）。只存 ID 与元数据（数据范围红线）；
// 源标题/用户名与 bot 用户名存快照，源或 bot 删除后记录仍可读。

// 预热路径。
const (
	WatchPathCopy     = "copy"     // 快路径：服务端 copyMessages 整批复制
	WatchPathFallback = "fallback" // 受保护内容：特权入队重传管线（带关联 requests 行）
)

// watchPathSet 是合法路径白名单。
var watchPathSet = map[string]bool{
	WatchPathCopy:     true,
	WatchPathFallback: true,
}

// WatchEvent 是 watch_events 表的行模型。
type WatchEvent struct {
	ID          int64  `json:"id"`
	ChannelID   int64  `json:"channel_id"`   // 源频道/群组 ID（-100 形态）
	Username    string `json:"username"`     // 源公开用户名快照（无 @；私有源为空）
	Title       string `json:"title"`        // 源标题快照
	MessageID   int    `json:"message_id"`   // 定位消息 ID（相册取首条成员）
	MemberIDs   []int  `json:"member_ids"`   // 转发的源消息 ID（相册为全部成员）
	DumpIDs     []int  `json:"dump_ids"`     // 缓存频道落点消息 ID（回退入队时为空）
	RequestID   int64  `json:"request_id"`   // 关联 requests 行（仅 fallback；0 = 无）
	BotID       int64  `json:"bot_id"`       // 执行转储的 bot
	BotUsername string `json:"bot_username"` // bot 用户名快照
	Path        string `json:"path"`         // copy / fallback
	CreatedAt   int64  `json:"created_at"`
}

// WatchEventsQuery 是事件列表查询：ChannelID=0 表示全部源；Path 为空
// 表示全部路径（copy/fallback）。
type WatchEventsQuery struct {
	ChannelID int64
	Path      string
	Page      int
	PageSize  int
}

// InsertWatchEvent 落一条预热事件（尽力而为语义由调用方决定：写失败记
// 日志不影响转储结果）。MemberIDs/DumpIDs 序列化为 JSON 数组存储。
func (s *Store) InsertWatchEvent(ctx context.Context, in WatchEvent) (WatchEvent, error) {
	if !watchPathSet[in.Path] {
		return WatchEvent{}, wrapDB("写入预热事件", fmt.Errorf("非法路径 %s", in.Path))
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = nowMillis()
	}
	members, err := json.Marshal(membersOr(in.MemberIDs, in.MessageID))
	if err != nil {
		return WatchEvent{}, wrapDB("写入预热事件", err)
	}
	dumps := []int{}
	if len(in.DumpIDs) > 0 {
		dumps = in.DumpIDs
	}
	dumpJSON, err := json.Marshal(dumps)
	if err != nil {
		return WatchEvent{}, wrapDB("写入预热事件", err)
	}
	res, err := s.ex.ExecContext(ctx, `INSERT INTO watch_events
		(channel_id, username, title, message_id, member_ids_json, dump_ids_json,
		 request_id, bot_id, bot_username, path, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		// 快照列是 NOT NULL DEFAULT ''（与 watch_sources 的可空列不同）：
		// 直接存字符串，不经 nullStr（空串转 NULL 会违反约束）
		in.ChannelID, in.Username, in.Title, in.MessageID,
		string(members), string(dumpJSON), in.RequestID, in.BotID,
		in.BotUsername, in.Path, in.CreatedAt)
	if err != nil {
		return WatchEvent{}, wrapDB("写入预热事件", err)
	}
	in.ID, _ = res.LastInsertId()
	return in, nil
}

// membersOr：未显式给成员时按定位消息退化为单成员。
func membersOr(ids []int, messageID int) []int {
	if len(ids) > 0 {
		return ids
	}
	if messageID > 0 {
		return []int{messageID}
	}
	return []int{}
}

const selectWatchEvent = `SELECT id, channel_id,
	COALESCE(username, ''), COALESCE(title, ''), message_id,
	COALESCE(member_ids_json, '[]'), COALESCE(dump_ids_json, '[]'),
	request_id, bot_id, COALESCE(bot_username, ''), path, created_at
FROM watch_events`

func scanWatchEvent(row scanner) (WatchEvent, error) {
	var e WatchEvent
	var members, dumps string
	err := row.Scan(&e.ID, &e.ChannelID, &e.Username, &e.Title, &e.MessageID,
		&members, &dumps, &e.RequestID, &e.BotID, &e.BotUsername, &e.Path, &e.CreatedAt)
	if err != nil {
		return e, err
	}
	_ = json.Unmarshal([]byte(members), &e.MemberIDs)
	_ = json.Unmarshal([]byte(dumps), &e.DumpIDs)
	return e, nil
}

// ListWatchEvents 按查询返回事件页（id 倒序，最新在前）与总数。
func (s *Store) ListWatchEvents(ctx context.Context, q WatchEventsQuery) ([]WatchEvent, int, error) {
	clauses := []string{}
	args := []any{}
	if q.ChannelID != 0 {
		clauses = append(clauses, "channel_id = ?")
		args = append(args, q.ChannelID)
	}
	if q.Path != "" {
		if !watchPathSet[q.Path] {
			return nil, 0, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法路径筛选 %q", q.Path))
		}
		clauses = append(clauses, "path = ?")
		args = append(args, q.Path)
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	var total int
	if err := s.ex.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM watch_events"+where, args...).Scan(&total); err != nil {
		return nil, 0, wrapDB("统计预热事件", err)
	}
	if q.PageSize <= 0 {
		q.PageSize = 20
	}
	if q.Page <= 0 {
		q.Page = 1
	}
	args = append(args, q.PageSize, (q.Page-1)*q.PageSize)
	rows, err := s.ex.QueryContext(ctx,
		selectWatchEvent+where+" ORDER BY id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, wrapDB("列出预热事件", err)
	}
	defer rows.Close()
	out := []WatchEvent{}
	for rows.Next() {
		e, err := scanWatchEvent(rows)
		if err != nil {
			return nil, 0, wrapDB("扫描预热事件行", err)
		}
		out = append(out, e)
	}
	return out, total, wrapDB("遍历预热事件行", rows.Err())
}

// DeleteWatchEvents 按 ID 批量删除预热事件（管理端单条/批量删除共用，
// 单条传单元素切片），返回实际删除行数（不存在的不计入）。
func (s *Store) DeleteWatchEvents(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	res, err := s.ex.ExecContext(ctx,
		"DELETE FROM watch_events WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		return 0, wrapDB("删除预热事件", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB("统计预热事件删除数", err)
	}
	return n, nil
}

// ---- 监听源统计（业务统计页监听模块；全部基于 watch_events 聚合） ----

// WatchSourceStat 是按源聚合的转储统计。
type WatchSourceStat struct {
	ChannelID int64  `json:"channel_id"`
	Title     string `json:"title"`
	Username  string `json:"username"`
	Events    int    `json:"events"`   // 转储批次数
	Messages  int    `json:"messages"` // 转储消息条数（相册成员合计）
	LastAt    int64  `json:"last_at"`  // 最近一次转储时间
}

// watchStatsWhere 组装时间/bot 界（与 StatsFilter 同语义：Since/Until 为
// Unix 毫秒，0 = 不设界；BotID=0 = 全部）。
func watchStatsWhere(since, until, botID int64) (string, []any) {
	q := " WHERE (? = 0 OR w.created_at >= ?) AND (? = 0 OR w.created_at < ?)"
	args := []any{since, since, until, until}
	if botID != 0 {
		q += " AND w.bot_id = ?"
		args = append(args, botID)
	}
	return q, args
}

// WatchStatsBySource 按源聚合转储统计（事件数倒序；时间/bot 界可选）。
func (s *Store) WatchStatsBySource(ctx context.Context, since, until, botID int64) ([]WatchSourceStat, error) {
	where, args := watchStatsWhere(since, until, botID)
	rows, err := s.ex.QueryContext(ctx, `SELECT w.channel_id,
		COALESCE(MAX(w.title), ''), COALESCE(MAX(w.username), ''),
		COUNT(*), COALESCE(SUM(json_array_length(w.member_ids_json)), 0), MAX(w.created_at)
		FROM watch_events w`+where+` GROUP BY w.channel_id ORDER BY COUNT(*) DESC, w.channel_id`, args...)
	if err != nil {
		return nil, wrapDB("聚合监听源按源统计", err)
	}
	defer rows.Close()
	out := []WatchSourceStat{}
	for rows.Next() {
		var st WatchSourceStat
		if err := rows.Scan(&st.ChannelID, &st.Title, &st.Username,
			&st.Events, &st.Messages, &st.LastAt); err != nil {
			return nil, wrapDB("扫描监听源统计行", err)
		}
		out = append(out, st)
	}
	return out, wrapDB("遍历监听源统计行", rows.Err())
}

// WatchBotStat 是按受理 bot 聚合的转储统计。
type WatchBotStat struct {
	BotID       int64  `json:"bot_id"`
	BotUsername string `json:"bot_username"`
	Events      int    `json:"events"`
}

// WatchStatsByBot 按 bot 聚合转储统计（事件数倒序；时间界可选，bot 界
// 对本聚合无意义不启用）。
func (s *Store) WatchStatsByBot(ctx context.Context, since, until int64) ([]WatchBotStat, error) {
	where, args := watchStatsWhere(since, until, 0)
	rows, err := s.ex.QueryContext(ctx, `SELECT w.bot_id, COALESCE(MAX(w.bot_username), ''), COUNT(*)
		FROM watch_events w`+where+` GROUP BY w.bot_id ORDER BY COUNT(*) DESC, w.bot_id`, args...)
	if err != nil {
		return nil, wrapDB("聚合监听源按 bot 统计", err)
	}
	defer rows.Close()
	out := []WatchBotStat{}
	for rows.Next() {
		var st WatchBotStat
		if err := rows.Scan(&st.BotID, &st.BotUsername, &st.Events); err != nil {
			return nil, wrapDB("扫描监听 bot 统计行", err)
		}
		out = append(out, st)
	}
	return out, wrapDB("遍历监听 bot 统计行", rows.Err())
}

// WatchUserStat 是按源归属用户聚合的转储统计（事件 JOIN 当前 watch_sources
// 归属；源已删除的事件不计入任何用户，added_by=0 归"管理员"）。
type WatchUserStat struct {
	AddedBy         int64  `json:"added_by"`
	UserUsername    string `json:"user_username"`
	UserDisplayName string `json:"user_display_name"`
	Events          int    `json:"events"`
}

// WatchStatsByUser 按源归属用户聚合转储统计（事件数倒序；时间/bot 界可选）。
func (s *Store) WatchStatsByUser(ctx context.Context, since, until, botID int64) ([]WatchUserStat, error) {
	where, args := watchStatsWhere(since, until, botID)
	rows, err := s.ex.QueryContext(ctx, `SELECT s.added_by,
		COALESCE(MAX(u.username), ''), COALESCE(MAX(u.display_name), ''), COUNT(*)
		FROM watch_events w
		JOIN watch_sources s ON s.channel_id = w.channel_id
		LEFT JOIN users u ON u.id = s.added_by`+where+`
		GROUP BY s.added_by ORDER BY COUNT(*) DESC, s.added_by`, args...)
	if err != nil {
		return nil, wrapDB("聚合监听源按用户统计", err)
	}
	defer rows.Close()
	out := []WatchUserStat{}
	for rows.Next() {
		var st WatchUserStat
		if err := rows.Scan(&st.AddedBy, &st.UserUsername, &st.UserDisplayName, &st.Events); err != nil {
			return nil, wrapDB("扫描监听用户统计行", err)
		}
		out = append(out, st)
	}
	return out, wrapDB("遍历监听用户统计行", rows.Err())
}

// WatchStatusCounts 是监听源状态计数。
type WatchStatusCounts struct {
	Approved int `json:"approved"`
	Pending  int `json:"pending"`
	Rejected int `json:"rejected"`
}

// CountWatchSourcesByStatus 按状态计数监听源。
func (s *Store) CountWatchSourcesByStatus(ctx context.Context) (WatchStatusCounts, error) {
	var c WatchStatusCounts
	err := s.ex.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(status='approved'), 0), COALESCE(SUM(status='pending'), 0),
			COALESCE(SUM(status='rejected'), 0) FROM watch_sources`).
		Scan(&c.Approved, &c.Pending, &c.Rejected)
	if err != nil {
		return c, wrapDB("计数监听源状态", err)
	}
	return c, nil
}

// WatchTrendPoint 是监听转储按日趋势点（运营时区日界）。
type WatchTrendPoint struct {
	Day    string `json:"day"`
	Events int    `json:"events"`
}

// ListWatchTrend 按日聚合转储事件数（since/until 毫秒界，0 = 开放；
// utcOffsetSec 与 ListRequestTrend 同源：运营时区偏移秒，日界对齐业务
// 统计口径；升序返回）。
func (s *Store) ListWatchTrend(ctx context.Context, since, until, utcOffsetSec int64) ([]WatchTrendPoint, error) {
	where, args := watchStatsWhere(since, until, 0)
	rows, err := s.ex.QueryContext(ctx, `SELECT strftime('%Y-%m-%d', (w.created_at + ?) / 1000, 'unixepoch'), COUNT(*)
		FROM watch_events w`+where+` GROUP BY 1 ORDER BY 1`, append([]any{int64(utcOffsetSec) * 1000}, args...)...)
	if err != nil {
		return nil, wrapDB("聚合监听转储趋势", err)
	}
	defer rows.Close()
	out := []WatchTrendPoint{}
	for rows.Next() {
		var p WatchTrendPoint
		if err := rows.Scan(&p.Day, &p.Events); err != nil {
			return nil, wrapDB("扫描监听趋势行", err)
		}
		out = append(out, p)
	}
	return out, wrapDB("遍历监听趋势行", rows.Err())
}
