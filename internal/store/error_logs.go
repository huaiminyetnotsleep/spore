package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// error_logs.go — 错误日志中心：请求管线与 bot 相关环节错误的逐条留痕
// （哪个来源、哪个环节、错误码、受控描述、原始根因串与参数快照）。与
// events 事件中心互补：events 按 key 合并聚合计数（管通知），本表逐条
// 记录（管诊断）。数据范围红线：只存错误文本与纯 ID 类参数，不存凭据、
// 消息正文与媒体 URL；detail 由写入门面（internal/errlog）统一截断。

// 错误来源（source 列与管理端筛选的白名单）。
const (
	ErrorSourceRequest = "request" // 请求管线（下载/发送/云盘任务/退出丢弃等）
	ErrorSourceBotAPI  = "botapi"  // Bot 消息收发（占位/提示/确认等尽力而为操作）
	ErrorSourceCloud   = "cloud"   // 网盘连通性测试等管理端触发动作
	ErrorSourceBackup  = "backup"  // 本地快照与 R2 上云
	ErrorSourceWatch   = "watch"   // 监听源转储与维护
	ErrorSourceMTProto = "mtproto" // 用户号会话（离线/封禁根因）
)

// errorSourceSet 是合法来源白名单（筛选参数校验用）。
var errorSourceSet = map[string]bool{
	ErrorSourceRequest: true,
	ErrorSourceBotAPI:  true,
	ErrorSourceCloud:   true,
	ErrorSourceBackup:  true,
	ErrorSourceWatch:   true,
	ErrorSourceMTProto: true,
}

// 错误严重级别（severity 列）。
const (
	ErrorSeverityError = "error" // 任务失败或进程级异常
	ErrorSeverityWarn  = "warn"  // 尽力而为操作的失败（不影响任务结果）
)

// errorSeveritySet 是合法级别白名单。
var errorSeveritySet = map[string]bool{
	ErrorSeverityError: true,
	ErrorSeverityWarn:  true,
}

// ErrorLog 是 error_logs 表的行模型。Context 为参数快照（纯 ID/名称类
// 值），序列化为 JSON 对象存储；时间 Unix 毫秒。
type ErrorLog struct {
	ID        int64          `json:"id"`
	Source    string         `json:"source"`
	Code      string         `json:"code"`       // apperr 错误码；空串=未分类
	Stage     string         `json:"stage"`      // 环节名（fetch/send/upload/pin…）；空串=未标注
	Severity  string         `json:"severity"`   // error / warn
	Message   string         `json:"message"`    // 受控中文描述（发生了什么）
	Detail    string         `json:"detail"`     // 原始错误串（截断后）；空串=无根因文本
	Context   map[string]any `json:"context"`    // 参数快照；nil 序列化为 {}
	RequestID int64          `json:"request_id"` // 关联 requests 行；0=无归属请求
	CreatedAt int64          `json:"created_at"`
}

// ValidErrorSource 报告来源是否在白名单内（Web 筛选参数校验用）。
func ValidErrorSource(v string) bool { return errorSourceSet[v] }

// ValidErrorSeverity 报告级别是否在白名单内（Web 筛选参数校验用）。
func ValidErrorSeverity(v string) bool { return errorSeveritySet[v] }

// InsertErrorLog 落一条错误日志（尽力而为语义由 internal/errlog 门面保证：
// 写失败只记日志不影响业务）。来源与级别走白名单，非法值直接拒绝。
func (s *Store) InsertErrorLog(ctx context.Context, in ErrorLog) (ErrorLog, error) {
	if !errorSourceSet[in.Source] {
		return ErrorLog{}, wrapDB("写入错误日志", fmt.Errorf("非法来源 %s", in.Source))
	}
	if in.Severity == "" {
		in.Severity = ErrorSeverityError
	}
	if !errorSeveritySet[in.Severity] {
		return ErrorLog{}, wrapDB("写入错误日志", fmt.Errorf("非法级别 %s", in.Severity))
	}
	ctxJSON := "{}"
	if len(in.Context) > 0 {
		b, err := json.Marshal(in.Context)
		if err != nil {
			return ErrorLog{}, wrapDB("写入错误日志", err)
		}
		ctxJSON = string(b)
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx, `INSERT INTO error_logs
		(source, code, stage, severity, message, detail, context_json, request_id, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		in.Source, in.Code, in.Stage, in.Severity, in.Message, in.Detail, ctxJSON, in.RequestID, in.CreatedAt)
	if err != nil {
		return ErrorLog{}, wrapDB("写入错误日志", err)
	}
	in.ID, _ = res.LastInsertId()
	return in, nil
}

const selectErrorLog = `SELECT id, source, code, stage, severity, message, detail,
	COALESCE(context_json, '{}'), request_id, created_at
FROM error_logs`

func scanErrorLog(row scanner) (ErrorLog, error) {
	var e ErrorLog
	var ctxJSON string
	if err := row.Scan(&e.ID, &e.Source, &e.Code, &e.Stage, &e.Severity,
		&e.Message, &e.Detail, &ctxJSON, &e.RequestID, &e.CreatedAt); err != nil {
		return e, err
	}
	_ = json.Unmarshal([]byte(ctxJSON), &e.Context)
	return e, nil
}

// ErrorLogsQuery 是错误日志列表查询：字符串筛选空值表示不限；RequestID=0
// 表示全部；CreatedAfter/CreatedBefore 为 Unix 毫秒界（0 = 开放，Before
// 为开区间上界）。
type ErrorLogsQuery struct {
	Source        string
	Code          string
	Severity      string
	RequestID     int64
	CreatedAfter  int64
	CreatedBefore int64
	Page          int
	PageSize      int
}

// errorLogsWhere 组装查询条件（白名单校验 + 参数绑定），返回 WHERE 子句
// （不含 WHERE 关键字，空串=无条件）与参数。
func (q ErrorLogsQuery) where() (string, []any, error) {
	clauses := []string{}
	args := []any{}
	if q.Source != "" {
		if !errorSourceSet[q.Source] {
			return "", nil, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法来源筛选 %q", q.Source))
		}
		clauses = append(clauses, "source = ?")
		args = append(args, q.Source)
	}
	if q.Severity != "" {
		if !errorSeveritySet[q.Severity] {
			return "", nil, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法级别筛选 %q", q.Severity))
		}
		clauses = append(clauses, "severity = ?")
		args = append(args, q.Severity)
	}
	if q.Code != "" {
		clauses = append(clauses, "code = ?")
		args = append(args, q.Code)
	}
	if q.RequestID != 0 {
		clauses = append(clauses, "request_id = ?")
		args = append(args, q.RequestID)
	}
	if q.CreatedAfter != 0 {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, q.CreatedAfter)
	}
	if q.CreatedBefore != 0 {
		clauses = append(clauses, "created_at < ?")
		args = append(args, q.CreatedBefore)
	}
	if len(clauses) == 0 {
		return "", args, nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args, nil
}

// ListErrorLogs 按查询返回错误日志页（id 倒序，最新在前）与总数。
func (s *Store) ListErrorLogs(ctx context.Context, q ErrorLogsQuery) ([]ErrorLog, int, error) {
	where, args, err := q.where()
	if err != nil {
		return nil, 0, err
	}
	var total int
	if err := s.ex.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM error_logs"+where, args...).Scan(&total); err != nil {
		return nil, 0, wrapDB("统计错误日志", err)
	}
	if q.PageSize <= 0 {
		q.PageSize = 20
	}
	if q.Page <= 0 {
		q.Page = 1
	}
	args = append(args, q.PageSize, (q.Page-1)*q.PageSize)
	rows, err := s.ex.QueryContext(ctx,
		selectErrorLog+where+" ORDER BY id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, 0, wrapDB("列出错误日志", err)
	}
	defer rows.Close()
	out := []ErrorLog{}
	for rows.Next() {
		e, err := scanErrorLog(rows)
		if err != nil {
			return nil, 0, wrapDB("扫描错误日志行", err)
		}
		out = append(out, e)
	}
	return out, total, wrapDB("遍历错误日志行", rows.Err())
}

// DeleteErrorLogs 按 ID 批量删除错误日志（管理端单条/批量删除共用），
// 返回实际删除行数（不存在的不计入）。
func (s *Store) DeleteErrorLogs(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	res, err := s.ex.ExecContext(ctx,
		"DELETE FROM error_logs WHERE id IN ("+placeholders+")", args...)
	if err != nil {
		return 0, wrapDB("删除错误日志", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB("统计错误日志删除数", err)
	}
	return n, nil
}

// ErrorLogsRangeQuery 是按时间段删除的条件：After/Before 为 Unix 毫秒界
// （0 = 开放，Before 为开区间上界），可叠加 Source/Code 条件（删"某时间
// 之前的某类错误"场景）。至少要有一个时间界，防止无条件全表清空。
type ErrorLogsRangeQuery struct {
	After  int64
	Before int64
	Source string
	Code   string
}

// DeleteErrorLogsRange 按时间段（可叠加来源/错误码）删除错误日志，返回
// 实际删除行数。管理端"按时间段清理"入口。
func (s *Store) DeleteErrorLogsRange(ctx context.Context, q ErrorLogsRangeQuery) (int64, error) {
	if q.After == 0 && q.Before == 0 {
		return 0, apperr.New(apperr.CodeInternal, "按时间段删除必须至少提供一时间界")
	}
	if q.Source != "" && !errorSourceSet[q.Source] {
		return 0, apperr.New(apperr.CodeInternal, fmt.Sprintf("非法来源筛选 %q", q.Source))
	}
	clauses := []string{}
	args := []any{}
	if q.After != 0 {
		clauses = append(clauses, "created_at >= ?")
		args = append(args, q.After)
	}
	if q.Before != 0 {
		clauses = append(clauses, "created_at < ?")
		args = append(args, q.Before)
	}
	if q.Source != "" {
		clauses = append(clauses, "source = ?")
		args = append(args, q.Source)
	}
	if q.Code != "" {
		clauses = append(clauses, "code = ?")
		args = append(args, q.Code)
	}
	res, err := s.ex.ExecContext(ctx,
		"DELETE FROM error_logs WHERE "+strings.Join(clauses, " AND "), args...)
	if err != nil {
		return 0, wrapDB("按时间段删除错误日志", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB("统计错误日志时间段删除数", err)
	}
	return n, nil
}

// DeleteErrorLogsBefore 删除 cutoff（Unix 毫秒）之前的全部错误日志，
// 返回实际删除行数。保留策略自动清理专用（internal/errlog.Run）。
func (s *Store) DeleteErrorLogsBefore(ctx context.Context, cutoff int64) (int64, error) {
	res, err := s.ex.ExecContext(ctx,
		"DELETE FROM error_logs WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, wrapDB("清理过期错误日志", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB("统计过期错误日志清理数", err)
	}
	return n, nil
}
