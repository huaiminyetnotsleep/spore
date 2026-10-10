package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// RecoveryFilter 固定历史范围；时间为毫秒，Until 不含边界。
type RecoveryFilter struct {
	ChannelKey string `json:"channel_key"`
	UserID     int64  `json:"user_id"`
	Since      int64  `json:"since"`
	Until      int64  `json:"until"`
}

// RecoveryCopy 是可确认所属频道的完整缓存坐标。
type RecoveryCopy struct {
	ChatID     int64 `json:"chat_id"`
	MessageIDs []int `json:"message_ids"`
}

// RecoveryItem 保存恢复单元的不可变来源快照与逐项结果。
type RecoveryItem struct {
	ID           int64          `json:"id"`
	JobID        int64          `json:"job_id"`
	ChannelKey   string         `json:"channel_key"`
	MessageID    int            `json:"message_id"`
	MemberIDs    []int          `json:"member_ids"`
	CacheCopies  []RecoveryCopy `json:"cache_copies"`
	SentIDs      []int          `json:"sent_ids"`
	Status       string         `json:"status"`
	Method       string         `json:"method"`
	ErrorCode    string         `json:"error_code"`
	ErrorMessage string         `json:"error_message"`
	CreatedAt    int64          `json:"created_at"`
	UpdatedAt    int64          `json:"updated_at"`
	IdentityKey  string         `json:"-"`
}

// RecoveryJob 是持久化任务和实时聚合进度。
type RecoveryJob struct {
	ID            int64          `json:"id"`
	Status        string         `json:"status"`
	TargetChatID  int64          `json:"target_chat_id"`
	TargetTitle   string         `json:"target_title"`
	BotID         int64          `json:"bot_id"`
	Filter        RecoveryFilter `json:"filter"`
	CreatedAt     int64          `json:"created_at"`
	UpdatedAt     int64          `json:"updated_at"`
	Total         int            `json:"total"`
	Pending       int            `json:"pending"`
	Processing    int            `json:"processing"`
	Succeeded     int            `json:"succeeded"`
	Failed        int            `json:"failed"`
	Unrecoverable int            `json:"unrecoverable"`
	Uncertain     int            `json:"uncertain"`
	Skipped       int            `json:"skipped"`
	LastError     string         `json:"last_error"`
}

func recoveryConflict() error {
	return apperr.New(apperr.CodeStoreConstraint, "恢复任务状态冲突")
}
func recoveryJSON(v any) string { b, _ := json.Marshal(v); return string(b) } // only concrete ID-only structs/slices

// ReconcileRecovery 在进程启动时执行一次，绝不自动重放投递。
func (s *Store) ReconcileRecovery(ctx context.Context) error {
	return s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.ex.ExecContext(ctx, `UPDATE recovery_items SET status='uncertain',error_code='INTERRUPTED',error_message='执行中断，投递结果不确定，请人工核对。',updated_at=? WHERE status='processing'`, nowMillis()); err != nil {
			return wrapDB("恢复处理中条目", err)
		}
		_, err := tx.ex.ExecContext(ctx, `UPDATE recovery_jobs SET status='paused',updated_at=?,last_error='执行中断，请复核后继续。' WHERE status='running'`, nowMillis())
		return wrapDB("暂停遗留恢复任务", err)
	})
}

// CreateRecoveryJob 在短事务内固定快照、去重已确认输出并写审计。
func (s *Store) CreateRecoveryJob(ctx context.Context, j RecoveryJob, items []RecoveryItem) (RecoveryJob, error) {
	if len(items) == 0 || j.BotID <= 0 || j.TargetChatID == 0 {
		return j, recoveryConflict()
	}
	err := s.Tx(ctx, func(tx *Store) error {
		now := nowMillis()
		j.CreatedAt = now
		j.UpdatedAt = now
		j.Status = "running"
		res, err := tx.ex.ExecContext(ctx, `INSERT INTO recovery_jobs(status,target_chat_id,target_title,bot_id,filter_json,created_at,updated_at) VALUES('running',?,?,?,?,?,?)`, j.TargetChatID, j.TargetTitle, j.BotID, recoveryJSON(j.Filter), now, now)
		if isConstraintErr(err) {
			return recoveryConflict()
		}
		if err != nil {
			return wrapDB("创建恢复任务", err)
		}
		j.ID, err = res.LastInsertId()
		if err != nil {
			return wrapDB("读取恢复任务ID", err)
		}
		for _, it := range items {
			status := "pending"
			// 身份只锚定来源与最小成员：历史后补相册成员会改变成员集合，
			// 让成员数组进入身份会绕过既有结果核对。
			it.IdentityKey = recoveryIdentity(it.ChannelKey, it.MessageID)
			conflict, superseded, e := tx.recoveryOverlap(ctx, it.ChannelKey, it.MemberIDs, j.TargetChatID, j.ID)
			if e != nil {
				return e
			}
			if conflict != nil {
				return apperr.New(apperr.CodeStoreConstraint, "同目标已有不确定或在途恢复结果，请先人工核对，不能新建任务绕过。")
			}
			if superseded != nil {
				status = "skipped"
			}
			_, err = tx.ex.ExecContext(ctx, `INSERT INTO recovery_items(job_id,channel_key,message_id,member_ids_json,cache_copies_json,status,created_at,updated_at,identity_key) VALUES(?,?,?,?,?,?,?,?,?)`, j.ID, it.ChannelKey, it.MessageID, recoveryJSON(it.MemberIDs), recoveryJSON(it.CacheCopies), status, now, now, it.IdentityKey)
			if err != nil {
				return wrapDB("创建恢复条目", err)
			}
		}
		return tx.recoveryAudit(ctx, j, "create")
	})
	if err != nil {
		return RecoveryJob{}, err
	}
	return s.GetRecoveryJob(ctx, j.ID)
}
func (s *Store) recoveryAudit(ctx context.Context, j RecoveryJob, action string) error {
	return s.AppendAudit(ctx, AuditEntry{Actor: "admin", Action: "recovery." + action, Target: "recovery:" + strconv.FormatInt(j.ID, 10), AfterJSON: recoveryJSON(struct {
		JobID  int64 `json:"job_id"`
		Target int64 `json:"target_chat_id"`
		Bot    int64 `json:"bot_id"`
	}{j.ID, j.TargetChatID, j.BotID})})
}

const recoveryJobSelect = `SELECT j.id,j.status,j.target_chat_id,j.target_title,j.bot_id,j.filter_json,j.created_at,j.updated_at,j.last_error,
 COUNT(i.id),COALESCE(SUM(i.status='pending'),0),COALESCE(SUM(i.status='processing'),0),COALESCE(SUM(i.status='succeeded'),0),COALESCE(SUM(i.status='failed'),0),COALESCE(SUM(i.status='unrecoverable'),0),COALESCE(SUM(i.status='uncertain'),0),COALESCE(SUM(i.status='skipped'),0)
 FROM recovery_jobs j LEFT JOIN recovery_items i ON i.job_id=j.id`

func scanRecoveryJob(r scanner) (RecoveryJob, error) {
	var j RecoveryJob
	var f string
	err := r.Scan(&j.ID, &j.Status, &j.TargetChatID, &j.TargetTitle, &j.BotID, &f, &j.CreatedAt, &j.UpdatedAt, &j.LastError, &j.Total, &j.Pending, &j.Processing, &j.Succeeded, &j.Failed, &j.Unrecoverable, &j.Uncertain, &j.Skipped)
	if err == nil {
		err = json.Unmarshal([]byte(f), &j.Filter)
	}
	return j, err
}

// GetRecoveryJob 返回单任务及全部条目计数。
func (s *Store) GetRecoveryJob(ctx context.Context, id int64) (RecoveryJob, error) {
	j, err := scanRecoveryJob(s.ex.QueryRowContext(ctx, recoveryJobSelect+` WHERE j.id=? GROUP BY j.id`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return j, ErrNotFound
	}
	return j, wrapDB("读取恢复任务", err)
}
func recoveryPage(p, n int) (int, int) {
	if p < 1 {
		p = 1
	}
	if n < 1 {
		n = 20
	}
	if n > 100 {
		n = 100
	}
	return p, n
}

// ListRecoveryJobs 分页返回任务，不将查询失败伪装为空列表。
func (s *Store) ListRecoveryJobs(ctx context.Context, p, n int) ([]RecoveryJob, int, error) {
	p, n = recoveryPage(p, n)
	var total int
	if err := s.ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_jobs`).Scan(&total); err != nil {
		return nil, 0, wrapDB("统计恢复任务", err)
	}
	rows, err := s.ex.QueryContext(ctx, recoveryJobSelect+` GROUP BY j.id ORDER BY j.id DESC LIMIT ? OFFSET ?`, n, (p-1)*n)
	if err != nil {
		return nil, 0, wrapDB("列出恢复任务", err)
	}
	defer rows.Close()
	out := []RecoveryJob{}
	for rows.Next() {
		j, e := scanRecoveryJob(rows)
		if e != nil {
			return nil, 0, wrapDB("扫描恢复任务", e)
		}
		out = append(out, j)
	}
	return out, total, wrapDB("遍历恢复任务", rows.Err())
}

const recoveryItemSelect = `SELECT id,job_id,channel_key,message_id,member_ids_json,cache_copies_json,sent_ids_json,status,method,error_code,error_message,created_at,updated_at,identity_key FROM recovery_items`

func scanRecoveryItem(r scanner) (RecoveryItem, error) {
	var i RecoveryItem
	var a, b, c string
	err := r.Scan(&i.ID, &i.JobID, &i.ChannelKey, &i.MessageID, &a, &b, &c, &i.Status, &i.Method, &i.ErrorCode, &i.ErrorMessage, &i.CreatedAt, &i.UpdatedAt, &i.IdentityKey)
	if err != nil {
		return i, err
	}
	for _, v := range []struct {
		s string
		v any
	}{{a, &i.MemberIDs}, {b, &i.CacheCopies}, {c, &i.SentIDs}} {
		if err = json.Unmarshal([]byte(v.s), v.v); err != nil {
			return i, err
		}
	}
	return i, nil
}

// ListRecoveryItems 状态可选，稳定按来源快照顺序分页。
func (s *Store) ListRecoveryItems(ctx context.Context, id int64, status string, p, n int) ([]RecoveryItem, int, error) {
	p, n = recoveryPage(p, n)
	where := ` WHERE job_id=?`
	args := []any{id}
	if status != "" {
		if !recoveryItemStatus(status) {
			return nil, 0, apperr.New(apperr.CodeInvalidURL, "恢复条目状态无效")
		}
		where += ` AND status=?`
		args = append(args, status)
	}
	var total int
	if err := s.ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_items`+where, args...).Scan(&total); err != nil {
		return nil, 0, wrapDB("统计恢复条目", err)
	}
	rows, err := s.ex.QueryContext(ctx, recoveryItemSelect+where+` ORDER BY id LIMIT ? OFFSET ?`, append(args, n, (p-1)*n)...)
	if err != nil {
		return nil, 0, wrapDB("列出恢复条目", err)
	}
	defer rows.Close()
	out := []RecoveryItem{}
	for rows.Next() {
		i, e := scanRecoveryItem(rows)
		if e != nil {
			return nil, 0, wrapDB("扫描恢复条目", e)
		}
		out = append(out, i)
	}
	return out, total, wrapDB("遍历恢复条目", rows.Err())
}
func recoveryItemStatus(v string) bool {
	switch v {
	case "pending", "processing", "succeeded", "failed", "unrecoverable", "uncertain", "skipped":
		return true
	}
	return false
}

// ControlRecoveryJob 受控状态转换和重试：不确定条目永远不自动重试。
func (s *Store) ControlRecoveryJob(ctx context.Context, id int64, action string) (RecoveryJob, error) {
	err := s.Tx(ctx, func(tx *Store) error {
		j, e := tx.GetRecoveryJob(ctx, id)
		if e != nil {
			return e
		}
		status := ""
		switch action {
		case "pause":
			if j.Status != "running" {
				return recoveryConflict()
			}
			status = "paused"
		case "cancel":
			if j.Status == "cancelled" {
				return recoveryConflict()
			}
			status = "cancelled"
		case "resume":
			if j.Status != "paused" {
				return recoveryConflict()
			}
			status = "running"
		case "retry":
			if j.Status != "paused" && j.Status != "completed" {
				return recoveryConflict()
			}
			if j.Failed+j.Unrecoverable == 0 {
				return recoveryConflict()
			}
			_, e = tx.ex.ExecContext(ctx, `UPDATE recovery_items SET status='pending',method='',error_code='',error_message='',updated_at=? WHERE job_id=? AND status IN ('failed','unrecoverable')`, nowMillis(), id)
			if e != nil {
				return wrapDB("重试恢复条目", e)
			}
			status = "running"
		default:
			return apperr.New(apperr.CodeInvalidURL, "恢复动作无效")
		}
		if j.Processing != 0 {
			return recoveryConflict()
		}
		_, e = tx.ex.ExecContext(ctx, `UPDATE recovery_jobs SET status=?,last_error='',updated_at=? WHERE id=?`, status, nowMillis(), id)
		if isConstraintErr(e) {
			return recoveryConflict()
		}
		if e != nil {
			return wrapDB("控制恢复任务", e)
		}
		return tx.recoveryAudit(ctx, j, action)
	})
	if err != nil {
		return RecoveryJob{}, err
	}
	return s.GetRecoveryJob(ctx, id)
}

// ClaimRecoveryItem 必须先持久化 processing，再允许外部副作用。
func (s *Store) ClaimRecoveryItem(ctx context.Context, jobID int64) (RecoveryItem, error) {
	var it RecoveryItem
	err := s.Tx(ctx, func(tx *Store) error {
		var e error
		it, e = scanRecoveryItem(tx.ex.QueryRowContext(ctx, recoveryItemSelect+` WHERE job_id=? AND status='pending' AND EXISTS(SELECT 1 FROM recovery_jobs WHERE id=? AND status='running') ORDER BY id LIMIT 1`, jobID, jobID))
		if errors.Is(e, sql.ErrNoRows) {
			return ErrNotFound
		}
		if e != nil {
			return wrapDB("选择恢复条目", e)
		}
		var target int64
		if e := tx.ex.QueryRowContext(ctx, `SELECT target_chat_id FROM recovery_jobs WHERE id=?`, jobID).Scan(&target); e != nil {
			return wrapDB("读取恢复任务目标", e)
		}
		conflict, superseded, e := tx.recoveryOverlap(ctx, it.ChannelKey, it.MemberIDs, target, jobID)
		if e != nil {
			return e
		}
		it.Status = "processing"
		if conflict != nil {
			it.Status = "uncertain"
			it.ErrorCode = "INTERRUPTED"
			it.ErrorMessage = "同目标已有不确定投递，请人工核对。"
			it.SentIDs = conflict.sentIDs
		} else if superseded != nil {
			it.Status = "skipped"
			it.SentIDs = superseded.sentIDs
		}
		if it.SentIDs == nil {
			it.SentIDs = []int{}
		}
		res, e := tx.ex.ExecContext(ctx, `UPDATE recovery_items SET status=?,sent_ids_json=?,error_code=?,error_message=?,updated_at=? WHERE id=? AND status='pending'`, it.Status, recoveryJSON(it.SentIDs), it.ErrorCode, it.ErrorMessage, nowMillis(), it.ID)
		return affected(res, e, "认领恢复条目")
	})
	return it, err
}

// FinishRecoveryItem 只更新已认领条目；DB故障由runner停止，不继续发送。
func (s *Store) FinishRecoveryItem(ctx context.Context, i RecoveryItem) error {
	if !recoveryItemStatus(i.Status) || i.Status == "processing" {
		return recoveryConflict()
	}
	res, e := s.ex.ExecContext(ctx, `UPDATE recovery_items SET status=?,method=?,sent_ids_json=?,error_code=?,error_message=?,updated_at=? WHERE id=? AND status='processing'`, i.Status, i.Method, recoveryJSON(i.SentIDs), i.ErrorCode, i.ErrorMessage, nowMillis(), i.ID)
	return affected(res, e, "写恢复条目结果")
}

// SetRecoveryJobState 是runner暂停/完成入口，不写用户操作审计。
func (s *Store) SetRecoveryJobState(ctx context.Context, id int64, status, msg string) error {
	if status != "paused" && status != "completed" {
		return recoveryConflict()
	}
	res, e := s.ex.ExecContext(ctx, `UPDATE recovery_jobs SET status=?,last_error=?,updated_at=? WHERE id=? AND status='running'`, status, msg, nowMillis(), id)
	return affected(res, e, "更新恢复运行状态")
}

// RunningRecoveryJob 查找唯一运行任务。
func (s *Store) RunningRecoveryJob(ctx context.Context) (RecoveryJob, error) {
	var id int64
	err := s.ex.QueryRowContext(ctx, `SELECT id FROM recovery_jobs WHERE status='running'`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return RecoveryJob{}, ErrNotFound
	}
	if err != nil {
		return RecoveryJob{}, wrapDB("查运行恢复任务", err)
	}
	return s.GetRecoveryJob(ctx, id)
}

// recoveryIdentity 只锚定来源与最小成员；成员集合可能随后续历史增长，
// 不能进入身份。key 已规范化且不含冒号，输出形如 `key:leader`。
func recoveryIdentity(key string, leader int) string {
	return fmt.Sprintf("%s:%d", key, leader)
}

// recoveryIdentityPrefix 构造来源前缀匹配；LIKE 通配符需转义，
// 用户名可能包含 `_`。
func recoveryIdentityPrefix(key string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(key) + ":%"
}

// recoveryPriorResult 是同目标下与本候选重叠的一条旧恢复结果。
type recoveryPriorResult struct {
	status  string
	sentIDs []int
}

// recoveryOverlap 在同目标的历史结果中按成员交集核对，不区分执行 Bot：
// 已知发送坐标属于目标聊天本身，换机器人继续恢复时旧结果同样有效。
// processing/uncertain 与候选相交即冲突（不能盲目重发）；succeeded 覆盖
// 候选全部成员才算已完成。精确身份相等会漏掉成员集合增长的情况。
func (s *Store) recoveryOverlap(ctx context.Context, key string, members []int, target, excludeJob int64) (conflict, superseded *recoveryPriorResult, err error) {
	rows, err := s.ex.QueryContext(ctx, `SELECT i.status,i.member_ids_json,i.sent_ids_json FROM recovery_items i JOIN recovery_jobs j ON j.id=i.job_id WHERE i.identity_key LIKE ? ESCAPE '\' AND i.job_id!=? AND j.target_chat_id=? AND i.status IN ('succeeded','processing','uncertain')`, recoveryIdentityPrefix(key), excludeJob, target)
	if err != nil {
		return nil, nil, wrapDB("核对同目标恢复结果", err)
	}
	defer rows.Close()
	newSet := map[int]bool{}
	for _, id := range members {
		newSet[id] = true
	}
	for rows.Next() {
		var status, ms, sent string
		if err = rows.Scan(&status, &ms, &sent); err != nil {
			return nil, nil, wrapDB("扫描同目标恢复结果", err)
		}
		var old []int
		if err = json.Unmarshal([]byte(ms), &old); err != nil {
			return nil, nil, wrapDB("解析旧恢复成员", err)
		}
		oldSet := map[int]bool{}
		overlap := false
		for _, id := range old {
			oldSet[id] = true
			if newSet[id] {
				overlap = true
			}
		}
		contained := true
		for _, id := range members {
			if !oldSet[id] {
				contained = false
				break
			}
		}
		var ids []int
		if err = json.Unmarshal([]byte(sent), &ids); err != nil {
			return nil, nil, wrapDB("解析旧恢复输出", err)
		}
		r := &recoveryPriorResult{status: status, sentIDs: ids}
		if status != "succeeded" {
			if overlap {
				if conflict == nil {
					conflict = r
				}
				break
			}
			continue
		}
		if contained && superseded == nil {
			superseded = r
		}
	}
	return conflict, superseded, wrapDB("遍历同目标恢复结果", rows.Err())
}
