package store

import (
	"context"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// defaultAuditLimit 是未指定 Limit 时的审计列表分页大小。
const defaultAuditLimit = 50

// AuditEntry 是 audit_log 表的行模型：管理员/系统的一次变更记录。
// BeforeJSON/AfterJSON 存变更前后状态的 JSON 快照，由调用方序列化。
type AuditEntry struct {
	ID         int64
	At         int64
	Actor      string // 来源标识，如 "admin"、"system"
	Action     string // 动作名，如 "user.enable"、"backup.export"
	Target     string // 操作对象，如 "user:123"
	BeforeJSON string
	AfterJSON  string
}

// AuditFilter 是审计查询范围；Since 包含、Until 不包含，Has 字段区分 epoch 边界与未设置。
type AuditFilter struct {
	Since    int64
	Until    int64
	HasSince bool
	HasUntil bool
}

// AppendAudit 追加一条审计记录；action 不能为空，at 缺省取当前时间。
func (s *Store) AppendAudit(ctx context.Context, e AuditEntry) error {
	if e.Action == "" {
		return apperr.New(apperr.CodeInternal, "审计 action 不能为空")
	}
	if e.At == 0 {
		e.At = nowMillis()
	}
	_, err := s.ex.ExecContext(ctx, `INSERT INTO audit_log (at, actor, action, target, before_json, after_json)
		VALUES (?,?,?,?,?,?)`,
		e.At, nullStr(e.Actor), e.Action, nullStr(e.Target), nullStr(e.BeforeJSON), nullStr(e.AfterJSON))
	return wrapDB("写审计记录", err)
}

// CountAudit 统计全部审计记录总数。
func (s *Store) CountAudit(ctx context.Context) (int, error) {
	return s.CountAuditFiltered(ctx, AuditFilter{})
}

// CountAuditFiltered 按与列表相同的时间范围统计审计记录。
func (s *Store) CountAuditFiltered(ctx context.Context, f AuditFilter) (int, error) {
	where, args := auditWhere(f)
	var n int
	err := s.ex.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_log"+where, args...).Scan(&n)
	return n, wrapDB("统计审计记录", err)
}

// ListAudit 按时间倒序分页读取全部审计记录。
func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]AuditEntry, error) {
	return s.ListAuditFiltered(ctx, AuditFilter{}, limit, offset)
}

// ListAuditFiltered 按时间范围、时间倒序分页读取审计记录。
func (s *Store) ListAuditFiltered(ctx context.Context, f AuditFilter, limit, offset int) ([]AuditEntry, error) {
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	where, args := auditWhere(f)
	args = append(args, limit, offset)
	rows, err := s.ex.QueryContext(ctx, `SELECT id, at, COALESCE(actor, ''), action,
		COALESCE(target, ''), COALESCE(before_json, ''), COALESCE(after_json, '')
		FROM audit_log`+where+` ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, wrapDB("查询审计列表", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.Target, &e.BeforeJSON, &e.AfterJSON); err != nil {
			return nil, wrapDB("扫描审计行", err)
		}
		out = append(out, e)
	}
	return out, wrapDB("遍历审计行", rows.Err())
}

// DeleteAuditByIDs 删除明确给出的审计 ID，返回实际命中行数。
func (s *Store) DeleteAuditByIDs(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	res, err := s.ex.ExecContext(ctx, "DELETE FROM audit_log WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		return 0, wrapDB("批量删除审计记录", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB("读取审计删除行数", err)
	}
	return int(n), nil
}

// DeleteAllAudit 删除全部审计记录，返回实际删除行数。
func (s *Store) DeleteAllAudit(ctx context.Context) (int, error) {
	res, err := s.ex.ExecContext(ctx, "DELETE FROM audit_log")
	if err != nil {
		return 0, wrapDB("清除审计记录", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB("读取审计清除行数", err)
	}
	return int(n), nil
}

func auditWhere(f AuditFilter) (string, []any) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if f.HasSince || f.Since != 0 {
		clauses = append(clauses, "at >= ?")
		args = append(args, f.Since)
	}
	if f.HasUntil || f.Until != 0 {
		clauses = append(clauses, "at < ?")
		args = append(args, f.Until)
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}
