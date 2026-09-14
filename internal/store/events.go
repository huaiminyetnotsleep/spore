package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// 事件状态（events.status）。
const (
	EventOpen     = "open"
	EventResolved = "resolved"
)

// Event 是 events 表的行模型：系统级异常事件的去重合并记录。
type Event struct {
	ID             int64
	Key            string // 去重键，如 "mtproto.session_invalid"
	Severity       string
	Message        string
	Count          int // 同 key 合并次数
	FirstAt        int64
	LastAt         int64
	LastNotifiedAt int64 // 0 表示从未通知
	Status         string
}

// selectEvent 是 events 查询的统一前缀，可空列已 COALESCE 归一为零值。
const selectEvent = `SELECT id, key, severity, message, count, first_at, last_at,
	COALESCE(last_notified_at, 0), status
FROM events`

// UpsertEvent 按 key 写入或合并事件：已存在时累加 count、刷新 severity/message/last_at，
// 并把 resolved 的事件重新置为 open（"恢复后再次发生可重新触发通知"）。
// first_at 保持不变；从 resolved 重开时清空 last_notified_at（旧通知的冷却期
// 不应抑制"恢复后再次发生"的新通知）。count 缺省为 1。
func (s *Store) UpsertEvent(ctx context.Context, e Event) error {
	if e.Key == "" {
		return apperr.New(apperr.CodeInternal, "事件 key 不能为空")
	}
	now := nowMillis()
	if e.FirstAt == 0 {
		e.FirstAt = now
	}
	if e.LastAt == 0 {
		e.LastAt = now
	}
	if e.Count == 0 {
		e.Count = 1
	}
	// ON CONFLICT 的 SET 表达式看到的都是更新前的旧行值：
	// events.status 是旧状态，events.last_notified_at 是旧通知时间
	_, err := s.ex.ExecContext(ctx, `INSERT INTO events (key, severity, message, count, first_at, last_at, status)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET
			severity = excluded.severity,
			message = excluded.message,
			count = count + excluded.count,
			last_at = excluded.last_at,
			last_notified_at = CASE WHEN events.status = ? THEN NULL ELSE events.last_notified_at END,
			status = ?`,
		e.Key, e.Severity, e.Message, e.Count, e.FirstAt, e.LastAt, EventOpen, EventResolved, EventOpen)
	return wrapDB("写事件", err)
}

// ResolveEvent 把事件标记为 resolved；不存在返回 ErrNotFound。
func (s *Store) ResolveEvent(ctx context.Context, id int64) error {
	res, err := s.ex.ExecContext(ctx, "UPDATE events SET status = ? WHERE id = ?", EventResolved, id)
	return affected(res, err, "解决事件")
}

// ResolveEventByKey 按 key 把处于 open 状态的事件标记为 resolved（系统自动恢复用，
// 如 MTProto 重连成功）；事件不存在或已经解决返回 ErrNotFound。使用状态条件
// 让并发恢复只有一个调用观察到状态迁移并写恢复审计。
func (s *Store) ResolveEventByKey(ctx context.Context, key string) error {
	res, err := s.ex.ExecContext(ctx,
		"UPDATE events SET status = ? WHERE key = ? AND status = ?",
		EventResolved, key, EventOpen)
	return affected(res, err, "按 key 解决事件")
}

// GetEvent 按 key 读取事件；不存在返回 ErrNotFound。
func (s *Store) GetEvent(ctx context.Context, key string) (Event, error) {
	var e Event
	err := s.ex.QueryRowContext(ctx, selectEvent+" WHERE key = ?", key).
		Scan(&e.ID, &e.Key, &e.Severity, &e.Message, &e.Count,
			&e.FirstAt, &e.LastAt, &e.LastNotifiedAt, &e.Status)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, wrapDB("读取事件", err)
	}
	return e, nil
}

// MarkEventNotified 记录事件最近一次成功通知的时间。
// 调用方只应在发送成功后调用；发送失败不得推进冷却期。不存在返回
// ErrNotFound（含已解决事件），数据库写入错误按存储错误返回。
func (s *Store) MarkEventNotified(ctx context.Context, key string, at int64) error {
	if at == 0 {
		at = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx,
		"UPDATE events SET last_notified_at = ? WHERE key = ? AND status = ?",
		at, key, EventOpen)
	return affected(res, err, "记录事件通知时间")
}

// ListEvents 按最近发生时间倒序返回全部事件。
func (s *Store) ListEvents(ctx context.Context) ([]Event, error) {
	rows, err := s.ex.QueryContext(ctx, selectEvent+" ORDER BY last_at DESC, id DESC")
	if err != nil {
		return nil, wrapDB("查询事件列表", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.Key, &e.Severity, &e.Message, &e.Count,
			&e.FirstAt, &e.LastAt, &e.LastNotifiedAt, &e.Status); err != nil {
			return nil, wrapDB("扫描事件行", err)
		}
		out = append(out, e)
	}
	return out, wrapDB("遍历事件行", rows.Err())
}
