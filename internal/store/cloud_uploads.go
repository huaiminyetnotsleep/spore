package store

import (
	"context"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// 云盘上传状态（cloud_uploads.status）：worker 逐文件落行，
// uploading → succeeded / failed 单向流转。
const (
	CloudUploadUploading = "uploading" // 上传进行中（行创建时）
	CloudUploadSucceeded = "succeeded"
	CloudUploadFailed    = "failed" // 终态由 FinishCloudUpload 写入
)

// CloudUpload 是 cloud_uploads 的行模型：一次网盘上传的目的地、远端路径、
// 状态与字节计数。数据范围红线：只存路径/状态/计数与错误码，
// 不存媒体内容与网盘凭据；时间 Unix 毫秒（0 表示尚未发生）。
type CloudUpload struct {
	ID          int64
	RequestID   int64
	Destination string // 目的地名称（cloudarchive.Destination.Name）
	RemotePath  string // 完整远端路径（不含 "<name>:" 远程前缀）
	FileName    string // 文件名部分（caption.txt 等规划名）
	Status      string
	ErrorCode   string // apperr 错误码；成功时为空
	ErrorDetail string // 原始根因串（截断后，同 requests.error_detail 规则）；成功或无根因时为空（v25）
	Bytes       int64  // 已上传字节；成功时为文件大小
	CreatedAt   int64
	FinishedAt  int64
}

// InsertCloudUpload 写入一条 uploading 行（worker 上传前），回填 ID 与时间戳。
func (s *Store) InsertCloudUpload(ctx context.Context, in CloudUpload) (CloudUpload, error) {
	now := nowMillis()
	if in.CreatedAt == 0 {
		in.CreatedAt = now
	}
	in.Status = CloudUploadUploading
	res, err := s.ex.ExecContext(ctx, `INSERT INTO cloud_uploads
		(request_id, destination, remote_path, file_name, status, error_code, bytes, created_at, finished_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		in.RequestID, in.Destination, in.RemotePath, in.FileName,
		in.Status, in.ErrorCode, in.Bytes, in.CreatedAt, in.FinishedAt)
	if err != nil {
		return CloudUpload{}, wrapDB("创建云盘上传记录", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return CloudUpload{}, wrapDB("读取云盘上传记录 ID", err)
	}
	in.ID = id
	return in, nil
}

// FinishCloudUpload 落库单次上传的终态：状态（succeeded/failed）、错误码、
// 根因串（截断后的原始错误，v25 起；成功传空）、字节数与完成时间。
// 行不存在返回 ErrNotFound。
func (s *Store) FinishCloudUpload(ctx context.Context, id int64, status, errorCode, errorDetail string, bytes int64, at int64) error {
	if status != CloudUploadSucceeded && status != CloudUploadFailed {
		return apperr.New(apperr.CodeInternal, "非法云盘上传终态: "+status)
	}
	if at == 0 {
		at = nowMillis()
	}
	res, err := s.ex.ExecContext(ctx, `UPDATE cloud_uploads SET
		status = ?, error_code = ?, error_detail = ?, bytes = ?, finished_at = ? WHERE id = ?`,
		status, errorCode, nullStr(errorDetail), bytes, at, id)
	return affected(res, err, "落库云盘上传终态")
}

// CloudUploadsByRequest 按请求读取全部上传记录（创建顺序），供请求详情
// 页的"云盘上传"区块展示；无记录返回空切片。
func (s *Store) CloudUploadsByRequest(ctx context.Context, requestID int64) ([]CloudUpload, error) {
	rows, err := s.ex.QueryContext(ctx, `SELECT id, request_id, destination, remote_path,
		file_name, status, COALESCE(error_code, ''), COALESCE(error_detail, ''),
		bytes, created_at, COALESCE(finished_at, 0)
		FROM cloud_uploads WHERE request_id = ? ORDER BY id`, requestID)
	if err != nil {
		return nil, wrapDB("查询云盘上传记录", err)
	}
	defer rows.Close()
	var out []CloudUpload
	for rows.Next() {
		var u CloudUpload
		if err := rows.Scan(&u.ID, &u.RequestID, &u.Destination, &u.RemotePath,
			&u.FileName, &u.Status, &u.ErrorCode, &u.ErrorDetail,
			&u.Bytes, &u.CreatedAt, &u.FinishedAt); err != nil {
			return nil, wrapDB("扫描云盘上传记录", err)
		}
		out = append(out, u)
	}
	return out, wrapDB("遍历云盘上传记录", rows.Err())
}
