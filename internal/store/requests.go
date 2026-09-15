package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// 请求状态（requests.status）。
const (
	RequestQueued     = "queued"     // 已入队待处理
	RequestProcessing = "processing" // worker 已取出
	RequestSucceeded  = "succeeded"
	RequestFailed     = "failed"
	RequestCancelled  = "cancelled" // 管理员主动取消
)

// 请求来源类型（requests.source_kind）。
const (
	SourcePublic  = "public"  // t.me/<username>/<msg_id>
	SourcePrivate = "private" // t.me/c/<internal_id>/<msg_id>
)

// 投递方式标记（requests.delivery_mode）：本次请求的媒体最终经何种路径送达。
// 供 worker 按实际投递路径写入、Web 记录页与 CSV 导出展示；中文标签映射在
// internal/web（deliveryModeText）与前端 format.ts（DELIVERY_MODE_LABELS）。
const (
	DeliveryModeReference = "reference" // 全部媒体经 Bot API file_id 引用送达
	DeliveryModeUpload    = "upload"    // 全部媒体经下载+上传送达（历史行的默认值）
	DeliveryModeMixed     = "mixed"     // 引用与上传混合（相册逐条降级等场景）
	DeliveryModeText      = "text"      // 纯文本请求，无媒体，标记不适用
	DeliveryModeCloud     = "cloud"     // 云盘下载：媒体上传到管理员配置的网盘
	DeliveryModeReuse     = "reuse"     // 重复链接复用：经 copyMessages 复制历史已投递消息送达
)

// defaultRequestLimit 是未指定 Limit 时的分页大小。
const defaultRequestLimit = 50

// Request 是 requests 表的行模型，记录一次提取请求的全生命周期。
// 不含消息正文、caption 与媒体本体；media_type/file_size/file_name
// 只是任务结束时的诊断元数据。
type Request struct {
	ID               int64
	UserID           int64
	SourceKind       string // public | private
	ChannelKey       string // username 或 -100 前缀数字 ID
	MessageID        int
	Status           string
	Attempt          int
	ErrorCode        string
	MediaType        string
	MediaTypes       []string // 请求包含的去重媒体类型；相册用于表达成员构成
	FileSize         int64
	FileName         string
	DeliveryMode     string // 投递方式标记（DeliveryMode* 常量）
	SourceMediaDCIDs []int  // 源媒体所在 DC 去重数组；空表示未知/不适用
	ParentRequestID  int64  // 补存行指向的原请求 ID；0（NULL）表示普通请求
	CloudDestination string // 云盘请求的目的地名称；重试/补存重新入队时据此恢复 Job.CloudDest
	// 复用坐标（v12，遗留）：用户聊天 copyMessages 复用时代的列，自转存
	// 频道方案起停止写入；保留读取兼容已部署库的历史行。复用现走
	// dump_entries（缓存频道干净副本坐标）。
	SentChatID     int64
	SentMessageIDs []int
	RequestedAt    int64
	QueuedAt       int64
	StartedAt      int64
	FinishedAt     int64
	DurationMs     int64
}

// RequestWithUser 是管理端请求列表行：请求信息 + 所属用户展示资料。
type RequestWithUser struct {
	Request
	UserUsername    string
	UserDisplayName string
}

// RequestResult 是任务结束时 FinishRequest 需要落库的结果。
type RequestResult struct {
	Status           string // succeeded | failed（cancelled 由 CancelRequest 写入）
	ErrorCode        string // apperr 错误码；成功时留空
	MediaType        string
	MediaTypes       []string // 请求包含的去重媒体类型
	FileSize         int64
	FileName         string
	DeliveryMode     string // 投递方式标记；空串落库时回落默认 upload
	SourceMediaDCIDs []int  // 源媒体所在 DC 去重数组
	At               int64  // 结束时间；0 取当前时间
}

// RequestFilter 是请求列表的筛选条件，零值字段不参与过滤。
type RequestFilter struct {
	UserID       int64
	Status       string
	ChannelKey   string
	MediaType    string
	DeliveryMode string // 投递方式（DeliveryMode* 常量值）
	ErrorCode    string
	Since        int64 // requested_at >= Since（Unix 毫秒）
	Until        int64 // requested_at < Until（Unix 毫秒）
	Limit        int   // <=0 时取 defaultRequestLimit
	Offset       int
}

// selectRequest 是 requests 查询的统一前缀，可空列已 COALESCE 归一为零值。
// delivery_mode 与 cloud_destination 为 NOT NULL 列（v2/v10 迁移带 DEFAULT），无需 COALESCE。
const selectRequest = `SELECT id, user_id, COALESCE(source_kind, ''), channel_key, message_id,
	status, attempt, COALESCE(error_code, ''), COALESCE(media_type, ''), media_types_json,
		COALESCE(file_size, 0), COALESCE(file_name, ''), delivery_mode, source_media_dc_ids_json,
		COALESCE(parent_request_id, 0), cloud_destination,
		sent_chat_id, sent_message_ids_json,
		requested_at, COALESCE(queued_at, 0), COALESCE(started_at, 0),
		COALESCE(finished_at, 0), COALESCE(duration_ms, 0)
FROM requests`

func scanRequest(row scanner) (Request, error) {
	var r Request
	var mediaTypesJSON, dcJSON, sentIDsJSON sql.NullString
	err := row.Scan(&r.ID, &r.UserID, &r.SourceKind, &r.ChannelKey, &r.MessageID,
		&r.Status, &r.Attempt, &r.ErrorCode, &r.MediaType, &mediaTypesJSON, &r.FileSize, &r.FileName,
		&r.DeliveryMode, &dcJSON, &r.ParentRequestID, &r.CloudDestination,
		&r.SentChatID, &sentIDsJSON,
		&r.RequestedAt, &r.QueuedAt, &r.StartedAt, &r.FinishedAt, &r.DurationMs)
	if err != nil {
		return r, err
	}
	var errDecode error
	r.MediaTypes, errDecode = decodeMediaTypes(mediaTypesJSON)
	if errDecode != nil {
		return Request{}, wrapDB("解析请求媒体类型", errDecode)
	}
	r.SourceMediaDCIDs, errDecode = decodeDCIDs(dcJSON)
	if errDecode != nil {
		return Request{}, wrapDB("解析请求媒体 DC", errDecode)
	}
	r.SentMessageIDs, errDecode = decodeSentMessageIDs(sentIDsJSON)
	if errDecode != nil {
		return Request{}, wrapDB("解析请求已发送消息", errDecode)
	}
	return r, nil
}

const selectRequestWithUser = `SELECT r.id, r.user_id, COALESCE(r.source_kind, ''), r.channel_key, r.message_id,
		r.status, r.attempt, COALESCE(r.error_code, ''), COALESCE(r.media_type, ''), r.media_types_json,
		COALESCE(r.file_size, 0), COALESCE(r.file_name, ''), r.delivery_mode, r.source_media_dc_ids_json,
		COALESCE(r.parent_request_id, 0), r.cloud_destination,
		r.sent_chat_id, r.sent_message_ids_json,
		r.requested_at, COALESCE(r.queued_at, 0), COALESCE(r.started_at, 0),
		COALESCE(r.finished_at, 0), COALESCE(r.duration_ms, 0),
		COALESCE(u.username, ''), COALESCE(u.display_name, '')
	FROM requests r
	LEFT JOIN users u ON u.id = r.user_id`

func scanRequestWithUser(row scanner) (RequestWithUser, error) {
	var out RequestWithUser
	var mediaTypesJSON, dcJSON, sentIDsJSON sql.NullString
	err := row.Scan(&out.ID, &out.UserID, &out.SourceKind, &out.ChannelKey, &out.MessageID,
		&out.Status, &out.Attempt, &out.ErrorCode, &out.MediaType, &mediaTypesJSON, &out.FileSize, &out.FileName,
		&out.DeliveryMode, &dcJSON, &out.ParentRequestID, &out.CloudDestination,
		&out.SentChatID, &sentIDsJSON, &out.RequestedAt, &out.QueuedAt, &out.StartedAt,
		&out.FinishedAt, &out.DurationMs, &out.UserUsername, &out.UserDisplayName)
	if err != nil {
		return out, err
	}
	var errDecode error
	out.MediaTypes, errDecode = decodeMediaTypes(mediaTypesJSON)
	if errDecode != nil {
		return RequestWithUser{}, wrapDB("解析请求媒体类型", errDecode)
	}
	out.SourceMediaDCIDs, errDecode = decodeDCIDs(dcJSON)
	if errDecode != nil {
		return RequestWithUser{}, wrapDB("解析请求媒体 DC", errDecode)
	}
	out.SentMessageIDs, errDecode = decodeSentMessageIDs(sentIDsJSON)
	if errDecode != nil {
		return RequestWithUser{}, wrapDB("解析请求已发送消息", errDecode)
	}
	return out, nil
}

func decodeDCIDs(raw sql.NullString) ([]int, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return []int{}, nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(raw.String), &ids); err != nil {
		return nil, fmt.Errorf("无效 DC 数组: %w", err)
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("DC ID 必须为正数")
		}
	}
	return ids, nil
}

func encodeDCIDs(ids []int) (string, error) {
	if len(ids) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(ids)
	return string(b), err
}

// decodeSentMessageIDs 解码 v12 复用坐标（按序已发送消息 ID）；空/NULL 视为无坐标
// （失败、云盘与 v12 前历史行），ID 必须为正。列自转存频道方案起停止写入，
// 读取仅兼容已部署库的历史行。
func decodeSentMessageIDs(raw sql.NullString) ([]int, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return nil, nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(raw.String), &ids); err != nil {
		return nil, fmt.Errorf("无效已发送消息数组: %w", err)
	}
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("消息 ID 必须为正")
		}
	}
	return ids, nil
}

func decodeMediaTypes(raw sql.NullString) ([]string, error) {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return []string{}, nil
	}
	var mediaTypes []string
	if err := json.Unmarshal([]byte(raw.String), &mediaTypes); err != nil {
		return nil, fmt.Errorf("无效媒体类型数组: %w", err)
	}
	seen := make(map[string]struct{}, len(mediaTypes))
	for _, mediaType := range mediaTypes {
		if strings.TrimSpace(mediaType) == "" {
			return nil, fmt.Errorf("媒体类型不能为空")
		}
		if _, ok := seen[mediaType]; ok {
			return nil, fmt.Errorf("媒体类型不能重复: %s", mediaType)
		}
		seen[mediaType] = struct{}{}
	}
	return mediaTypes, nil
}

func encodeMediaTypes(mediaTypes []string) (string, error) {
	if len(mediaTypes) == 0 {
		return "[]", nil
	}
	seen := make(map[string]struct{}, len(mediaTypes))
	for _, mediaType := range mediaTypes {
		if strings.TrimSpace(mediaType) == "" {
			return "", fmt.Errorf("媒体类型不能为空")
		}
		if _, ok := seen[mediaType]; ok {
			return "", fmt.Errorf("媒体类型不能重复: %s", mediaType)
		}
		seen[mediaType] = struct{}{}
	}
	b, err := json.Marshal(mediaTypes)
	return string(b), err
}

// CreateRequest 写入一条 queued 请求（入队成功的持久化事实），回填 ID 与时间戳。
// 额度扣减与入队的原子性由调用方（access 服务）在同一事务边界内保证；
// 本方法自身只负责落库。user_id 不存在时返回 STORE_CONSTRAINT。
// 云盘请求由调用方传 DeliveryMode=cloud 占位与 CloudDestination 目的地名称、
// 管理端补存传 ParentRequestID 指向原请求；均为普通列写入，不在此做业务校验。
func (s *Store) CreateRequest(ctx context.Context, in Request) (Request, error) {
	now := nowMillis()
	if in.RequestedAt == 0 {
		in.RequestedAt = now
	}
	if in.QueuedAt == 0 {
		in.QueuedAt = now
	}
	if in.Attempt == 0 {
		in.Attempt = 1
	}
	in.Status = RequestQueued
	// delivery_mode 显式落列：queued 阶段尚未投递，统一以历史默认值占位，
	// 终态由 FinishRequest 按实际投递路径覆盖。
	if in.DeliveryMode == "" {
		in.DeliveryMode = DeliveryModeUpload
	}
	res, err := s.ex.ExecContext(ctx, `INSERT INTO requests
		(user_id, source_kind, channel_key, message_id, status, attempt, delivery_mode,
		 parent_request_id, cloud_destination, requested_at, queued_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		in.UserID, nullStr(in.SourceKind), in.ChannelKey, in.MessageID,
		in.Status, in.Attempt, in.DeliveryMode, nullInt64(in.ParentRequestID),
		in.CloudDestination, in.RequestedAt, in.QueuedAt)
	if err != nil {
		if isConstraintErr(err) {
			return Request{}, apperr.Wrap(apperr.CodeStoreConstraint,
				fmt.Errorf("创建请求（用户 %d 不存在或字段非法）: %w", in.UserID, err))
		}
		return Request{}, wrapDB("创建请求", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Request{}, wrapDB("读取新请求 ID", err)
	}
	in.ID = id
	return in, nil
}

// GetRequest 按 ID 读取请求；不存在返回 ErrNotFound。
func (s *Store) GetRequest(ctx context.Context, id int64) (Request, error) {
	r, err := scanRequest(s.ex.QueryRowContext(ctx, selectRequest+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, wrapDB("读取请求", err)
	}
	return r, nil
}

// MarkRequestStarted 把 queued 请求条件置为 processing 并写入 started_at（worker
// 取出任务的埋点）。已取消或已结束的请求返回 STORE_CONSTRAINT，排队任务可据此
// 在出队时跳过，不会开始执行。
func (s *Store) MarkRequestStarted(ctx context.Context, id int64, at int64) error {
	if at == 0 {
		at = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx, `UPDATE requests SET status = ?, started_at = ?
		WHERE id = ? AND status = ?`, RequestProcessing, at, id, RequestQueued)
	if err != nil {
		return wrapDB("标记请求开始", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return wrapDB("标记请求开始", err)
	} else if n > 0 {
		return nil
	}
	return requestUpdateConflict(ctx, s, id, "标记请求开始")
}

// CancelRequest 以条件更新把 queued/processing 请求置为 cancelled，并返回取消前
// 快照。条件更新保证取消与自然完成、重试之间至多一个操作成功；已结束请求返回
// STORE_CONSTRAINT，不存在请求返回 ErrNotFound。
func (s *Store) CancelRequest(ctx context.Context, id int64, at int64) (Request, error) {
	before, err := s.GetRequest(ctx, id)
	if err != nil {
		return Request{}, err
	}
	if before.Status != RequestQueued && before.Status != RequestProcessing {
		return Request{}, requestStateConflict(id, before.Status, "取消请求")
	}
	if at == 0 {
		at = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx, `UPDATE requests SET
		status = ?, error_code = ?, finished_at = ?,
		duration_ms = ? - COALESCE(started_at, queued_at, requested_at)
		WHERE id = ? AND status IN (?, ?)`,
		RequestCancelled, string(apperr.CodeRequestCancelled), at, at, id, RequestQueued, RequestProcessing)
	if err != nil {
		return Request{}, wrapDB("取消请求", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return Request{}, wrapDB("取消请求", err)
	} else if n == 0 {
		return Request{}, requestUpdateConflict(ctx, s, id, "取消请求")
	}
	return before, nil
}

// FinishRequest 落库任务终态：写 finished_at、duration_ms（自 started_at 起；
// 若任务未及开始即失败，则回退到 queued_at）与结果元数据。状态条件更新防止
// 已取消请求被自然成功/失败覆盖；queued 终态仍允许用于提交后入队失败和历史
// 测试种子，processing 终态是正常 worker 收尾路径。
// DeliveryMode 空串（无投递信息的失败/丢弃任务）回落列默认 upload。
func (s *Store) FinishRequest(ctx context.Context, id int64, r RequestResult) error {
	if r.Status != RequestSucceeded && r.Status != RequestFailed {
		return apperr.New(apperr.CodeInternal, fmt.Sprintf("非法请求终态 %q", r.Status))
	}
	if r.At == 0 {
		r.At = nowMillis()
	}
	mode := r.DeliveryMode
	if mode == "" {
		mode = DeliveryModeUpload
	}
	mediaTypesJSON, err := encodeMediaTypes(r.MediaTypes)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, fmt.Errorf("编码请求媒体类型: %w", err))
	}
	dcJSON, err := encodeDCIDs(r.SourceMediaDCIDs)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, fmt.Errorf("编码请求媒体 DC: %w", err))
	}
	// v12 复用坐标列（sent_chat_id/sent_message_ids_json）自转存频道方案起
	// 停止写入（列保留在 schema，历史行仍可读——迁移只增不改）
	res, err := s.ex.ExecContext(ctx, `UPDATE requests SET
		status = ?, error_code = ?, media_type = ?, media_types_json = ?, file_size = ?, file_name = ?,
		delivery_mode = ?, source_media_dc_ids_json = ?,
		finished_at = ?, duration_ms = ? - COALESCE(started_at, queued_at, requested_at)
		WHERE id = ? AND status IN (?, ?, ?, ?)`,
		r.Status, nullStr(r.ErrorCode), nullStr(r.MediaType), mediaTypesJSON,
		nullInt64(r.FileSize), nullStr(r.FileName), mode, dcJSON, r.At, r.At, id,
		RequestQueued, RequestProcessing, RequestSucceeded, RequestFailed)
	if err != nil {
		return wrapDB("落库请求终态", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return wrapDB("落库请求终态", err)
	} else if n > 0 {
		return nil
	}
	return requestUpdateConflict(ctx, s, id, "落库请求终态")
}

// RetryRequest 把 failed 请求重置回 queued 并累加 attempt（复用同一行）。
// 前置条件（状态 failed、attempt 上限、用户 enabled、队列容量、不扣额度）
// 由 internal/access 的 Retry 在同一事务内校验后再调用本方法；
// 清空 error_code 与阶段时间戳，等待下一轮处理重新落。
func (s *Store) RetryRequest(ctx context.Context, id int64, queuedAt int64) error {
	if queuedAt == 0 {
		queuedAt = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx, `UPDATE requests SET
		status = ?, attempt = attempt + 1, queued_at = ?,
		started_at = NULL, finished_at = NULL, duration_ms = NULL, error_code = NULL
		WHERE id = ? AND status IN (?, ?, ?)`, RequestQueued, queuedAt, id, RequestFailed, RequestSucceeded, RequestQueued)
	if err != nil {
		return wrapDB("重试请求", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return wrapDB("重试请求", err)
	} else if n > 0 {
		return nil
	}
	return requestUpdateConflict(ctx, s, id, "重试请求")
}

func requestStateConflict(id int64, status, op string) error {
	return apperr.Wrap(apperr.CodeStoreConstraint,
		fmt.Errorf("%s：请求 %d 当前状态为 %s", op, id, status))
}

// requestUpdateConflict 把条件 UPDATE 未命中区分为不存在或状态冲突。
func requestUpdateConflict(ctx context.Context, s *Store, id int64, op string) error {
	current, err := s.GetRequest(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return requestStateConflict(id, current.Status, op)
}

// ListRequests 按筛选条件分页查询请求，按请求时间倒序。
func (s *Store) ListRequests(ctx context.Context, f RequestFilter) ([]Request, error) {
	where, args := f.where()
	limit := f.Limit
	if limit <= 0 {
		limit = defaultRequestLimit
	}
	args = append(args, limit, f.Offset)

	rows, err := s.ex.QueryContext(ctx, selectRequest+where+
		" ORDER BY requested_at DESC, id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, wrapDB("查询请求列表", err)
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, wrapDB("扫描请求行", err)
		}
		out = append(out, r)
	}
	return out, wrapDB("遍历请求行", rows.Err())
}

// ListRequestsWithUser 按筛选条件分页查询请求，并一次带出所属用户资料。
func (s *Store) ListRequestsWithUser(ctx context.Context, f RequestFilter) ([]RequestWithUser, error) {
	where, args := f.whereWithPrefix("r")
	limit := f.Limit
	if limit <= 0 {
		limit = defaultRequestLimit
	}
	args = append(args, limit, f.Offset)

	rows, err := s.ex.QueryContext(ctx, selectRequestWithUser+where+
		" ORDER BY r.requested_at DESC, r.id DESC LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, wrapDB("查询请求列表", err)
	}
	defer rows.Close()
	var out []RequestWithUser
	for rows.Next() {
		r, err := scanRequestWithUser(rows)
		if err != nil {
			return nil, wrapDB("扫描请求行", err)
		}
		out = append(out, r)
	}
	return out, wrapDB("遍历请求行", rows.Err())
}

// CountRequests 按同款筛选条件统计请求总数（CSV 导出的上限预判）。
func (s *Store) CountRequests(ctx context.Context, f RequestFilter) (int, error) {
	where, args := f.where()
	var n int
	err := s.ex.QueryRowContext(ctx, "SELECT COUNT(*) FROM requests"+where, args...).Scan(&n)
	return n, wrapDB("统计请求数", err)
}

// where 组装筛选条件的 WHERE 片段（含前导空格；无条件时为空串）。
func (f RequestFilter) where() (string, []any) {
	return f.whereWithPrefix("")
}

func (f RequestFilter) whereWithPrefix(prefix string) (string, []any) {
	col := func(name string) string {
		if prefix == "" {
			return name
		}
		return prefix + "." + name
	}
	var conds []string
	var args []any
	if f.UserID > 0 {
		conds = append(conds, col("user_id")+" = ?")
		args = append(args, f.UserID)
	}
	if f.Status != "" {
		conds = append(conds, col("status")+" = ?")
		args = append(args, f.Status)
	}
	if f.ChannelKey != "" {
		conds = append(conds, col("channel_key")+" = ?")
		args = append(args, f.ChannelKey)
	}
	if f.MediaType != "" {
		conds = append(conds, col("media_type")+" = ?")
		args = append(args, f.MediaType)
	}
	if f.DeliveryMode != "" {
		conds = append(conds, col("delivery_mode")+" = ?")
		args = append(args, f.DeliveryMode)
	}
	if f.ErrorCode != "" {
		conds = append(conds, col("error_code")+" = ?")
		args = append(args, f.ErrorCode)
	}
	if f.Since > 0 {
		conds = append(conds, col("requested_at")+" >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		conds = append(conds, col("requested_at")+" < ?")
		args = append(args, f.Until)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// CountUnfinishedByUser 统计用户的未完成请求数（queued + processing），
// 供并发上限校验使用。
func (s *Store) CountUnfinishedByUser(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.ex.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM requests WHERE user_id = ? AND status IN (?, ?)",
		userID, RequestQueued, RequestProcessing).Scan(&n)
	return n, wrapDB("统计未完成请求", err)
}

// ListUnfinishedByUserAndLink 查询该用户名下与链接（channel_key + message_id）
// 匹配的未完成请求（queued + processing），按请求时间倒序；user_id 限定即归属校验。
// 供 Bot 侧 /cancel <链接> 自助取消定位目标请求。
func (s *Store) ListUnfinishedByUserAndLink(ctx context.Context, userID int64, channelKey string, messageID int) ([]Request, error) {
	rows, err := s.ex.QueryContext(ctx, selectRequest+
		" WHERE user_id = ? AND channel_key = ? AND message_id = ? AND status IN (?, ?)"+
		" ORDER BY requested_at DESC, id DESC",
		userID, channelKey, messageID, RequestQueued, RequestProcessing)
	if err != nil {
		return nil, wrapDB("查询用户链接未完成请求", err)
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, wrapDB("扫描用户链接未完成请求行", err)
		}
		out = append(out, r)
	}
	return out, wrapDB("遍历用户链接未完成请求行", rows.Err())
}

// HasRecentSucceeded 判断 since 之后该用户对同一链接是否已有成功记录，
// 供重复检测窗口使用（命中则不建任务、不扣额度）。
func (s *Store) HasRecentSucceeded(ctx context.Context, userID int64, channelKey string, messageID int, since int64) (bool, error) {
	var ok bool
	err := s.ex.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM requests
		WHERE user_id = ? AND channel_key = ? AND message_id = ?
		  AND status = ? AND requested_at >= ?)`,
		userID, channelKey, messageID, RequestSucceeded, since).Scan(&ok)
	return ok, wrapDB("查询近期成功记录", err)
}

// HasUnfinishedCloudBackfill 报告该原请求是否已有在途云盘补存任务：
// parent_request_id 只有补存建行会写入，命中 queued/processing 即在途
// （管理端补存资格校验用，与 cloud_delivery 列无关）。
func (s *Store) HasUnfinishedCloudBackfill(ctx context.Context, parentID int64) (bool, error) {
	var ok bool
	err := s.ex.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM requests WHERE parent_request_id = ? AND status IN (?, ?))`,
		parentID, RequestQueued, RequestProcessing).Scan(&ok)
	return ok, wrapDB("查询在途云盘补存", err)
}

// LatestSucceededCloudRequest 查找该用户同链接同目的地最近一次成功的云盘请求
// （delivery_mode=cloud），供 /download 任务在跳过重复上传时定位历史远端路径。
// 文件当前是否仍在云盘由调用方的远端核验判断，本查询不设时间窗口；
// 无匹配返回 ErrNotFound。
func (s *Store) LatestSucceededCloudRequest(ctx context.Context, userID int64, channelKey string, messageID int, cloudDestination string) (Request, error) {
	r, err := scanRequest(s.ex.QueryRowContext(ctx, selectRequest+
		" WHERE user_id = ? AND channel_key = ? AND message_id = ?"+
		" AND status = ? AND delivery_mode = ? AND cloud_destination = ?"+
		" ORDER BY COALESCE(finished_at, 0) DESC, id DESC LIMIT 1",
		userID, channelKey, messageID, RequestSucceeded, DeliveryModeCloud, cloudDestination))
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, wrapDB("查询最近成功云盘请求", err)
	}
	return r, nil
}

// HasUnfinishedCloudRequest 报告该用户同链接同目的地是否已有在途（queued/
// processing）云盘请求：同路径并发上传会在允许同名对象的网盘（如 MEGA）
// 产生重复文件，提交链据此拒绝重复在途提交。
func (s *Store) HasUnfinishedCloudRequest(ctx context.Context, userID int64, channelKey string, messageID int, cloudDestination string) (bool, error) {
	var ok bool
	err := s.ex.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM requests
		WHERE user_id = ? AND channel_key = ? AND message_id = ?
		  AND status IN (?, ?) AND delivery_mode = ? AND cloud_destination = ?)`,
		userID, channelKey, messageID, RequestQueued, RequestProcessing,
		DeliveryModeCloud, cloudDestination).Scan(&ok)
	return ok, wrapDB("查询在途云盘请求", err)
}

// LatestSucceededTGRequest 查找同链接最近一次成功的 TG 投递请求（非云盘），
// 供缓存频道复用命中时还原媒体诊断元数据（metaFromRequest）；不要求复用
// 坐标、不带 user_id（跨用户）、无匹配返回 ErrNotFound。
func (s *Store) LatestSucceededTGRequest(ctx context.Context, channelKey string, messageID int) (Request, error) {
	r, err := scanRequest(s.ex.QueryRowContext(ctx, selectRequest+
		" WHERE channel_key = ? AND message_id = ? AND status = ? AND cloud_destination = ''"+
		" ORDER BY COALESCE(finished_at, 0) DESC, id DESC LIMIT 1",
		channelKey, messageID, RequestSucceeded))
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	if err != nil {
		return Request{}, wrapDB("查询最近成功 TG 请求", err)
	}
	return r, nil
}

// DeleteRequest 硬删除单条请求记录；行不存在返回 ErrNotFound。
// 仅终态可删的前置校验由 internal/access 在同一事务内完成后再调用本方法。
func (s *Store) DeleteRequest(ctx context.Context, id int64) error {
	res, err := s.ex.ExecContext(ctx, "DELETE FROM requests WHERE id = ?", id)
	return affected(res, err, "删除请求")
}

// CountUnfinishedByChannel 统计频道未完成请求数（queued + processing），
// 供频道记录删除前的安全预检使用。
func (s *Store) CountUnfinishedByChannel(ctx context.Context, channelKey string) (int, error) {
	var n int
	err := s.ex.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM requests WHERE channel_key = ? AND status IN (?, ?)",
		channelKey, RequestQueued, RequestProcessing).Scan(&n)
	return n, wrapDB("统计频道未完成请求", err)
}

// DeleteRequestsByChannel 硬删除该频道的全部请求记录，返回删除行数；
// 无记录时返回 0（由调用方决定是否视为不存在）。
func (s *Store) DeleteRequestsByChannel(ctx context.Context, channelKey string) (int, error) {
	res, err := s.ex.ExecContext(ctx, "DELETE FROM requests WHERE channel_key = ?", channelKey)
	if err != nil {
		return 0, wrapDB("删除频道请求记录", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB("统计频道删除行数", err)
	}
	return int(n), nil
}

// FailInterruptedRequests 启动恢复：把上次进程退出时遗留的 queued/processing
// 请求批量置为 failed(INTERRUPTED)，返回受影响行数。
// 正常关机的 drain 路径已即时落库，本方法是硬退出（kill/崩溃）的兜底；
// 无遗留行时返回 0，幂等可重复调用。
func (s *Store) FailInterruptedRequests(ctx context.Context, at int64) (int, error) {
	if at == 0 {
		at = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx, `UPDATE requests SET
		status = ?, error_code = ?, finished_at = ?,
		duration_ms = ? - COALESCE(started_at, queued_at, requested_at)
		WHERE status IN (?, ?)`,
		RequestFailed, string(apperr.CodeInterrupted), at, at, RequestQueued, RequestProcessing)
	if err != nil {
		return 0, wrapDB("恢复中断请求", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, wrapDB("统计恢复行数", err)
	}
	return int(n), nil
}
