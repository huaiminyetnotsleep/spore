package web

// 高风险页面迁移：备份导出 / 上传校验 /
// 导入确认的 SPA API（/api/v1，当前为唯一入口）。业务规则、大小与扩展名
// 校验、审计由 runBackupExport / runBackupImportUpload / runBackupImportConfirm
// 提供。
//
// 契约要点：
//   - 导出为流式 .db 文件响应（CSRF 经 X-CSRF-Token 头）；
//   - 上传是 multipart 请求，不经 apiReadJSON（JSON 64KB 上限不适用），
//     大小上限为 maxBackupUpload；
//   - 导入只进入待确认状态，确认后下次启动应用；marker 语义由 internal/store
//     保证（安全设置保留、Web 会话清除、Session/peers 文件不受影响）。

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// apiBackupView 是 GET /api/v1/backup 的响应 DTO。
type apiBackupView struct {
	DBPath       string `json:"db_path"`
	DBSizeBytes  int64  `json:"db_size_bytes"`  // 文件不可统计为 0
	LastBackupAt int64  `json:"last_backup_at"` // Unix 毫秒；0 表示从未备份
	// 待导入状态：Pending 为 false 时其余字段为零值。
	Pending       bool           `json:"pending"`
	PendingState  string         `json:"pending_state,omitempty"` // 待确认 | confirmed | failed
	PendingSHA256 string         `json:"pending_sha256,omitempty"`
	JSONFiles     []JSONFileInfo `json:"json_files"`
}

// buildAPIBackupView 组装备份页读取响应（备份状态唯一读取口径）。
func (s *Server) buildAPIBackupView(ctx context.Context) apiBackupView {
	view := apiBackupView{
		DBPath:       s.dbPath,
		LastBackupAt: s.lastBackupAt(ctx),
		JSONFiles:    s.scanDataJSONFiles(),
	}
	if fi, err := os.Stat(s.dbPath); err == nil {
		view.DBSizeBytes = fi.Size()
	}
	// 候选文件存在但 marker 不可读时按"待确认"呈现，
	// marker 可读时以 marker 状态为准。
	if _, err := os.Stat(store.PendingImportPath(s.cfg.DataDir)); err == nil {
		view.Pending = true
		view.PendingState = "待确认"
	}
	if marker, err := store.ReadPendingImport(s.cfg.DataDir); err == nil {
		view.Pending = true
		view.PendingState = marker.Status
		view.PendingSHA256 = marker.SHA256
	}
	return view
}

// handleAPIBackupGet 返回备份页状态（文件大小、最近备份与待导入 marker）。
func (s *Server) handleAPIBackupGet(w http.ResponseWriter, r *http.Request, _ session) {
	writeAPISingle(w, s.buildAPIBackupView(r.Context()))
}

// handleAPIBackupExport 流式导出数据库快照（核心 runBackupExport：
// 响应头、审计与最近备份时间语义一致）。
func (s *Server) handleAPIBackupExport(w http.ResponseWriter, r *http.Request, _ session) {
	if err := s.runBackupExport(w, r); err != nil {
		// 响应尚未写出：按统一 JSON 错误输出；失败阶段仅进入服务日志
		s.log.Error("API 数据库备份导出失败", "op", backupExportOp(err), "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal),
			apperr.UserText(apperr.CodeInternal))
		return
	}
}

// handleAPIBackupImportUpload 处理 multipart 上传（共用核心；校验边界与
// 审计同源）。结果是 JSON 摘要而非整页重渲染。
func (s *Server) handleAPIBackupImportUpload(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.backup.import.upload"
	notice, errMsg := s.runBackupImportUpload(w, r)
	if errMsg != "" {
		s.log.Warn("备份上传被拒绝", "op", op)
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, errMsg)
		return
	}
	writeAPISingle(w, struct {
		apiWriteOK
		Message string        `json:"message"`
		Backup  apiBackupView `json:"backup"`
	}{apiWriteOK{OK: true}, notice, s.buildAPIBackupView(r.Context())})
}

// handleAPIBackupImportConfirm 处理导入确认（JSON 载荷 confirm=import；
// 共用核心写 marker 与审计）。
func (s *Server) handleAPIBackupImportConfirm(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.backup.import.confirm"
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	notice, errMsg := s.runBackupImportConfirm(r.Context(), in.Confirm)
	if errMsg != "" {
		s.log.Warn("备份导入确认被拒绝", "op", op)
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, errMsg)
		return
	}
	writeAPISingle(w, struct {
		apiWriteOK
		Message string        `json:"message"`
		Backup  apiBackupView `json:"backup"`
	}{apiWriteOK{OK: true}, notice, s.buildAPIBackupView(r.Context())})
}

// lastBackupAt 读取最近备份时间（Unix 毫秒；从未备份为 0）。
func (s *Server) lastBackupAt(ctx context.Context) int64 {
	raw, ok, err := s.st.GetSetting(ctx, settingKeyLastBackupAt)
	if err != nil || !ok {
		return 0
	}
	var ms int64
	if json.Unmarshal([]byte(raw), &ms) != nil {
		return 0
	}
	return ms
}
