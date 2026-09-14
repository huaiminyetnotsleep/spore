package web

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

const maxBackupUpload = int64(2<<30) + 1<<20

// runBackupImportUpload 接收并校验 Spore SQLite 导出文件（备份导入唯一
// 入口 /api/v1/backup/import 的核心）：校验通过后只进入待确认状态，不改变
// 当前数据库。返回成功提示或受控错误文案；拒绝路径的脱敏审计已在核心内
// 完成，调用方按各自呈现方式输出。
func (s *Server) runBackupImportUpload(w http.ResponseWriter, r *http.Request) (notice, errMsg string) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupUpload)
	parseErr := r.ParseMultipartForm(32 << 20)
	defer func() {
		if r.MultipartForm != nil {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				s.log.Warn("清理备份上传临时文件失败", "error", err.Error())
			}
		}
	}()
	if parseErr != nil {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "multipart_parse_failed"})
		return "", "备份上传失败，请重试。"
	}
	file, header, err := r.FormFile("backup")
	if err != nil {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "missing_file"})
		return "", "请选择 SQLite .db 备份文件。"
	}
	defer file.Close()
	if filepath.Ext(strings.TrimSpace(header.Filename)) != ".db" {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "invalid_extension"})
		return "", "只接受 Spore 导出的 .db 文件。"
	}
	dir := filepath.Dir(s.dbPath)
	tmp, err := os.CreateTemp(dir, ".pending-import-*.upload")
	if err != nil {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "temp_create_failed"})
		return "", "无法创建上传临时文件。"
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "temp_permission_failed"})
		return "", "无法保护上传临时文件。"
	}
	n, copyErr := io.Copy(tmp, io.LimitReader(file, maxBackupUpload+1))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil || n > maxBackupUpload {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "upload_failed_or_too_large"})
		return "", "备份文件过大或上传失败。"
	}
	if err := store.ValidateBackup(r.Context(), tmpName); err != nil {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "invalid_backup"})
		return "", "备份校验失败：" + safeBackupError(err)
	}
	pending := store.PendingImportPath(s.cfg.DataDir)
	if err := os.Rename(tmpName, pending); err != nil {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "pending_save_failed"})
		return "", "保存待确认备份失败。"
	}
	s.audit(r.Context(), "backup.import.validated", "store", map[string]any{"bytes": n})
	return "备份已校验通过，但尚未导入。请确认后通过受控重启在下一次启动应用。", ""
}

// runBackupImportConfirm 校验二次确认并写入明确的待导入 marker
// （/api/v1/backup/import/confirm 的核心）；不会在线替换当前数据库。
// 返回成功提示或受控错误文案。
func (s *Server) runBackupImportConfirm(ctx context.Context, confirm string) (notice, errMsg string) {
	if confirm != "import" {
		s.audit(ctx, "backup.import.rejected", "store", map[string]any{"reason": "confirmation_missing"})
		return "", "请确认整库替换导入。"
	}
	if _, err := os.Stat(store.PendingImportPath(s.cfg.DataDir)); err != nil {
		s.audit(ctx, "backup.import.rejected", "store", map[string]any{"reason": "pending_missing"})
		return "", "没有可确认的待导入备份。"
	}
	marker, err := store.WritePendingImport(s.cfg.DataDir, "admin", s.now())
	if err != nil {
		s.audit(ctx, "backup.import.rejected", "store", map[string]any{"reason": "confirmation_failed"})
		return "", "写入待导入标记失败：" + safeBackupError(err)
	}
	s.audit(ctx, "backup.import.confirmed", "store", map[string]any{"sha256": marker.SHA256, "effect": "next_start"})
	return "备份已确认，将在下一次服务启动时应用；当前数据库尚未改变。", ""
}

// safeBackupError 把备份内部错误归一为受控文案，不向 API 响应暴露
// SQLite、文件路径或其他实现细节；底层原因只由调用方记录在受控日志中。
func safeBackupError(err error) string {
	if err == nil {
		return "未知错误"
	}
	ae := apperr.From(err)
	switch ae.Code {
	case apperr.CodeStoreUnavailable:
		return apperr.UserText(apperr.CodeStoreUnavailable)
	case apperr.CodeStoreMigration:
		return apperr.UserText(apperr.CodeStoreMigration)
	case apperr.CodeStoreConstraint:
		return apperr.UserText(apperr.CodeStoreConstraint)
	default:
		return "备份操作失败，请稍后重试。"
	}
}

// handleBackupImportUpload / handleBackupImportConfirm 及其页面渲染辅助
// 备份上传与确认的唯一入口是 /api/v1/backup/import*
// （api_backup.go，共用上方核心）。
