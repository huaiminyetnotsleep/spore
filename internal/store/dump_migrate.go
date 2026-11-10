package store

// dump_migrate.go — 缓存迁移持久化 DAO：每次管理端迁移一条 run 行 +
// 逐条处理结果的 item 行（来源坐标 → 各目标频道新消息 ID 或错误）。
// 聚合计数（done/skipped/failed）经查询得出，run 行不存冗余计数，避免
// 双写漂移。状态机见 internal/dumpcache/migrate.go：Handler 即写状态、
// 执行 goroutine 批间感知；重启由 ReconcileDumpMigrate 收敛为 interrupted。
// 只存频道/消息 ID、状态与错误文本，不存正文/媒体 URL（红线）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// DumpMigrateRunStatus 是迁移运行的状态机取值。
const (
	DumpMigrateRunning     = "running"
	DumpMigratePaused      = "paused"
	DumpMigrateCompleted   = "completed"
	DumpMigrateCancelled   = "cancelled"
	DumpMigrateInterrupted = "interrupted"
)

// dumpMigrateKeepRuns 是运行记录的保留数量：发起新运行时清理更早记录。
const dumpMigrateKeepRuns = 20

// DumpMigrateItemResult 是单条明细中一个目标频道的落地结果：
// 成功含新消息 ID，失败含错误串（二者互斥，由调用方保证）。
type DumpMigrateItemResult struct {
	ChannelID  int64  `json:"channel_id"`
	MessageIDs []int  `json:"message_ids,omitempty"`
	Error      string `json:"error,omitempty"`
}

// DumpMigrateRun 是 dump_migrate_runs 的行模型（含明细聚合计数）。
type DumpMigrateRun struct {
	ID            int64   `json:"id"`
	Status        string  `json:"status"`
	FromChannelID int64   `json:"from_channel_id"`
	To            []int64 `json:"to"`
	Total         int64   `json:"total"`
	CreatedAt     int64   `json:"created_at"`
	UpdatedAt     int64   `json:"updated_at"`
	FinishedAt    int64   `json:"finished_at"`
	LastError     string  `json:"last_error"`
	// 聚合计数（查询时经 LEFT JOIN SUM 得出，不落库）
	Done    int64 `json:"done"`
	Skipped int64 `json:"skipped"`
	Failed  int64 `json:"failed"`
}

// DumpMigrateItem 是 dump_migrate_items 的行模型。
type DumpMigrateItem struct {
	ID         int64                   `json:"id"`
	RunID      int64                   `json:"run_id"`
	ChannelKey string                  `json:"channel_key"`
	MessageID  int                     `json:"message_id"`
	Status     string                  `json:"status"`
	Results    []DumpMigrateItemResult `json:"results"`
	Error      string                  `json:"error"`
	CreatedAt  int64                   `json:"created_at"`
}

func dumpMigrateConflict(msg string) error {
	return apperr.New(apperr.CodeStoreConstraint, msg)
}
func dumpMigrateJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// dumpMigrateItemStatus 校验明细状态白名单。
func dumpMigrateItemStatus(v string) bool {
	switch v {
	case "migrated", "skipped", "failed":
		return true
	}
	return false
}

// CreateDumpMigrateRun 创建 running 运行行（短事务：插行 + 审计 + 清理
// 超出保留数量的旧运行及其明细）。from/to 为源与目标频道快照，total 为
// 发起时源频道条目总数。
func (s *Store) CreateDumpMigrateRun(ctx context.Context, from int64, to []int64, total int64) (DumpMigrateRun, error) {
	if from == 0 || len(to) == 0 {
		return DumpMigrateRun{}, dumpMigrateConflict("迁移运行要求源频道与目标频道均非空")
	}
	now := nowMillis()
	run := DumpMigrateRun{
		Status: DumpMigrateRunning, FromChannelID: from, To: to, Total: total,
		CreatedAt: now, UpdatedAt: now,
	}
	err := s.Tx(ctx, func(tx *Store) error {
		res, e := tx.ex.ExecContext(ctx, `INSERT INTO dump_migrate_runs
			(status, from_channel_id, to_json, total, created_at, updated_at)
			VALUES (?,?,?,?,?,?)`,
			run.Status, run.FromChannelID, dumpMigrateJSON(run.To), run.Total, now, now)
		if isConstraintErr(e) {
			// 唯一活跃索引命中：已有 running/paused 运行（并发发起的硬保证）
			return dumpMigrateConflict("已有缓存迁移进行中，请等待完成或先取消")
		}
		if e != nil {
			return wrapDB("创建迁移运行", e)
		}
		run.ID, e = res.LastInsertId()
		if e != nil {
			return wrapDB("读取迁移运行 ID", e)
		}
		// 保留策略：按 id 倒序保留前 dumpMigrateKeepRuns 条，更早的连明细删除。
		// 删除与清理审计同事务（数据库规范：审计清理不得拆开）。
		_, e = tx.ex.ExecContext(ctx, `DELETE FROM dump_migrate_items WHERE run_id IN (
			SELECT id FROM dump_migrate_runs ORDER BY id DESC LIMIT -1 OFFSET ?)`, dumpMigrateKeepRuns)
		if e != nil {
			return wrapDB("清理迁移明细", e)
		}
		_, e = tx.ex.ExecContext(ctx, `DELETE FROM dump_migrate_runs WHERE id IN (
			SELECT id FROM dump_migrate_runs ORDER BY id DESC LIMIT -1 OFFSET ?)`, dumpMigrateKeepRuns)
		if e != nil {
			return wrapDB("清理迁移运行", e)
		}
		return tx.AppendAudit(ctx, AuditEntry{Actor: "admin", Action: "dumpcache.migrate_run.create",
			Target: "dump_migrate_run:" + fmt.Sprint(run.ID),
			AfterJSON: dumpMigrateJSON(struct {
				Run  int64   `json:"run_id"`
				From int64   `json:"from_channel_id"`
				To   []int64 `json:"to"`
			}{run.ID, run.FromChannelID, run.To})})
	})
	if err != nil {
		return DumpMigrateRun{}, err
	}
	return run, nil
}

// dumpMigrateRunSelect 是 run 查询公共段：聚合计数经 LEFT JOIN SUM 得出
// （done+skipped+failed 即已处理条目数，不再单独 COUNT）。
const dumpMigrateRunSelect = `SELECT r.id,r.status,r.from_channel_id,r.to_json,r.total,r.created_at,r.updated_at,r.finished_at,r.last_error,
 COALESCE(SUM(i.status='migrated'),0),COALESCE(SUM(i.status='skipped'),0),COALESCE(SUM(i.status='failed'),0)
 FROM dump_migrate_runs r LEFT JOIN dump_migrate_items i ON i.run_id=r.id`

func scanDumpMigrateRun(row scanner) (DumpMigrateRun, error) {
	var r DumpMigrateRun
	var toJSON string
	err := row.Scan(&r.ID, &r.Status, &r.FromChannelID, &toJSON, &r.Total, &r.CreatedAt,
		&r.UpdatedAt, &r.FinishedAt, &r.LastError, &r.Done, &r.Skipped, &r.Failed)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(toJSON), &r.To); err != nil {
		return r, wrapDB("解析迁移运行目标频道", err)
	}
	return r, nil
}

// GetDumpMigrateRun 返回单条运行（含聚合计数）；无行返回 ErrNotFound。
func (s *Store) GetDumpMigrateRun(ctx context.Context, id int64) (DumpMigrateRun, error) {
	r, err := scanDumpMigrateRun(s.ex.QueryRowContext(ctx, dumpMigrateRunSelect+` WHERE r.id=? GROUP BY r.id`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return DumpMigrateRun{}, ErrNotFound
	}
	return r, wrapDB("读取迁移运行", err)
}

// LatestDumpMigrateRun 返回最近一次运行（含聚合计数）；从未发起返回 ErrNotFound。
func (s *Store) LatestDumpMigrateRun(ctx context.Context) (DumpMigrateRun, error) {
	r, err := scanDumpMigrateRun(s.ex.QueryRowContext(ctx, dumpMigrateRunSelect+` GROUP BY r.id ORDER BY r.id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return DumpMigrateRun{}, ErrNotFound
	}
	return r, wrapDB("读取最近迁移运行", err)
}

// ListDumpMigrateRuns 按 id 倒序分页返回运行列表。
func (s *Store) ListDumpMigrateRuns(ctx context.Context, p, n int) ([]DumpMigrateRun, int, error) {
	p, n = recoveryPage(p, n)
	var total int
	if err := s.ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM dump_migrate_runs`).Scan(&total); err != nil {
		return nil, 0, wrapDB("统计迁移运行", err)
	}
	rows, err := s.ex.QueryContext(ctx, dumpMigrateRunSelect+
		` GROUP BY r.id ORDER BY r.id DESC LIMIT ? OFFSET ?`, n, (p-1)*n)
	if err != nil {
		return nil, 0, wrapDB("列出迁移运行", err)
	}
	defer rows.Close()
	out := []DumpMigrateRun{}
	for rows.Next() {
		r, e := scanDumpMigrateRun(rows)
		if e != nil {
			return nil, 0, wrapDB("扫描迁移运行", e)
		}
		out = append(out, r)
	}
	return out, total, wrapDB("遍历迁移运行", rows.Err())
}

// dumpMigrateAllowedFrom 是受控状态转换表：目标态 → 允许的来源态集合。
// Handler 即写（pause/resume/cancel）；goroutine 只经 SetDumpMigrateFinished
// 写 completed（已被取消则 no-op）；interrupted 仅由 Reconcile 批量写入。
var dumpMigrateAllowedFrom = map[string][]string{
	DumpMigratePaused:    {DumpMigrateRunning},
	DumpMigrateRunning:   {DumpMigratePaused},
	DumpMigrateCancelled: {DumpMigrateRunning, DumpMigratePaused},
}

// SetDumpMigrateRunStatus 执行受控状态转换；来源态不符（含终态再改）
// 返回 STORE_CONSTRAINT。lastErr 随状态写入（取消原因说明，可空）。
func (s *Store) SetDumpMigrateRunStatus(ctx context.Context, id int64, status, lastErr string) error {
	from, ok := dumpMigrateAllowedFrom[status]
	if !ok {
		return apperr.New(apperr.CodeInternal, "迁移运行状态无效: "+status)
	}
	now := nowMillis()
	finishedAt := int64(0)
	if dumpMigrateTerminal(status) {
		finishedAt = now
	}
	res, err := s.ex.ExecContext(ctx,
		`UPDATE dump_migrate_runs SET status=?, last_error=?, updated_at=?, finished_at=? WHERE id=? AND status IN ('`+
			strings.Join(from, "','")+`')`,
		status, lastErr, now, finishedAt, id)
	if err != nil {
		return wrapDB("更新迁移运行状态", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return dumpMigrateConflict("迁移运行状态冲突")
	}
	return nil
}

// dumpMigrateTerminal 判断是否终态。
func dumpMigrateTerminal(status string) bool {
	switch status {
	case DumpMigrateCompleted, DumpMigrateCancelled, DumpMigrateInterrupted:
		return true
	}
	return false
}

// SetDumpMigrateFinished 是执行 goroutine 的自然结束写回：仅当前态仍为
// running/paused 时写 completed（已被 Handler 取消则 no-op，返回 nil）。
func (s *Store) SetDumpMigrateFinished(ctx context.Context, id int64, lastErr string) error {
	res, err := s.ex.ExecContext(ctx, `UPDATE dump_migrate_runs SET status=?, last_error=?, updated_at=?, finished_at=? WHERE id=? AND status IN ('running','paused')`,
		DumpMigrateCompleted, lastErr, nowMillis(), nowMillis(), id)
	if err != nil {
		return wrapDB("完成迁移运行", err)
	}
	_, _ = res.RowsAffected() // 已取消：no-op
	return nil
}

// InsertDumpMigrateItem 落一条处理结果明细。
func (s *Store) InsertDumpMigrateItem(ctx context.Context, in DumpMigrateItem) error {
	if !dumpMigrateItemStatus(in.Status) {
		return apperr.New(apperr.CodeInternal, "迁移明细状态无效")
	}
	if in.Results == nil {
		in.Results = []DumpMigrateItemResult{}
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = nowMillis()
	}
	_, err := s.ex.ExecContext(ctx, `INSERT INTO dump_migrate_items
		(run_id, channel_key, message_id, status, results_json, error, created_at) VALUES (?,?,?,?,?,?,?)`,
		in.RunID, in.ChannelKey, in.MessageID, in.Status, dumpMigrateJSON(in.Results), in.Error, in.CreatedAt)
	return wrapDB("写入迁移明细", err)
}

// DumpMigrateRunStatusOf 轻量读取单条运行状态（执行循环的暂停/取消闸门
// 逐条轮询用，不做聚合计数）。
func (s *Store) DumpMigrateRunStatusOf(ctx context.Context, id int64) (string, error) {
	var status string
	err := s.ex.QueryRowContext(ctx, `SELECT status FROM dump_migrate_runs WHERE id=?`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return status, wrapDB("读取迁移运行状态", err)
}

// TouchDumpMigrateRun 批间刷新 updated_at（活跃度心跳，不改状态）。
func (s *Store) TouchDumpMigrateRun(ctx context.Context, id int64) error {
	_, err := s.ex.ExecContext(ctx, `UPDATE dump_migrate_runs SET updated_at=? WHERE id=?`, nowMillis(), id)
	return wrapDB("刷新迁移运行", err)
}

// ListDumpMigrateItems 按 run 分页返回明细，status 可选（白名单校验）。
func (s *Store) ListDumpMigrateItems(ctx context.Context, runID int64, status string, p, n int) ([]DumpMigrateItem, int, error) {
	p, n = recoveryPage(p, n)
	where := ` WHERE run_id=?`
	args := []any{runID}
	if status != "" {
		if !dumpMigrateItemStatus(status) {
			return nil, 0, apperr.New(apperr.CodeInvalidURL, "迁移明细状态无效")
		}
		where += ` AND status=?`
		args = append(args, status)
	}
	var total int
	if err := s.ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM dump_migrate_items`+where, args...).Scan(&total); err != nil {
		return nil, 0, wrapDB("统计迁移明细", err)
	}
	rows, err := s.ex.QueryContext(ctx, `SELECT id,run_id,channel_key,message_id,status,results_json,error,created_at FROM dump_migrate_items`+
		where+` ORDER BY id LIMIT ? OFFSET ?`, append(args, n, (p-1)*n)...)
	if err != nil {
		return nil, 0, wrapDB("列出迁移明细", err)
	}
	defer rows.Close()
	out := []DumpMigrateItem{}
	for rows.Next() {
		var it DumpMigrateItem
		var resultsJSON string
		if err := rows.Scan(&it.ID, &it.RunID, &it.ChannelKey, &it.MessageID, &it.Status, &resultsJSON, &it.Error, &it.CreatedAt); err != nil {
			return nil, 0, wrapDB("扫描迁移明细", err)
		}
		if err := json.Unmarshal([]byte(resultsJSON), &it.Results); err != nil {
			return nil, 0, wrapDB("解析迁移明细结果", err)
		}
		out = append(out, it)
	}
	return out, total, wrapDB("遍历迁移明细", rows.Err())
}

// ReconcileDumpMigrate 进程启动时执行一次：遗留 running/paused 标记
// interrupted（重启即停语义；可重新发起，幂等续跑）。
func (s *Store) ReconcileDumpMigrate(ctx context.Context) error {
	_, err := s.ex.ExecContext(ctx, `UPDATE dump_migrate_runs SET status=?, last_error=?, updated_at=?, finished_at=? WHERE status IN ('running','paused')`,
		DumpMigrateInterrupted, "执行中断（服务重启），可重新发起继续", nowMillis(), nowMillis())
	return wrapDB("收敛中断迁移运行", err)
}
