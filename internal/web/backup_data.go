package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/backup"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
)

const (
	maxSingleJSONUploadSize = 10 << 20 // 10 MiB
	maxAllJSONUploadSize    = 50 << 20 // 50 MiB
)

// JSONFileInfo 是 data/ 目录下单个 JSON 配置文件的元数据。
type JSONFileInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SizeBytes   int64  `json:"size_bytes"`
	ModTime     int64  `json:"mod_time"` // Unix 毫秒
}

// jsonFileDescription 返回已知配置文件的中文说明。
func jsonFileDescription(name string) string {
	switch name {
	case "session.json":
		return "Telegram 用户会话凭据（MTProto）"
	case "bot-session.json":
		return "Telegram 机器人客户端会话"
	case "peers.json":
		return "Telegram 对话与频道实体缓存"
	case "cloud-drive.json":
		return "云盘存储与挂载配置"
	case "bots.json":
		return "机器人 Token 池配置"
	default:
		if strings.HasPrefix(name, "bot-session-") {
			return "副机器人客户端会话"
		}
		return "JSON 配置文件"
	}
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	h.SetMode(0o600)
	w, err := zw.CreateHeader(h)
	if err != nil {
		return fmt.Errorf("创建备份包文件头失败: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("写入备份包文件失败: %w", err)
	}
	return nil
}

// scanDataJSONFiles 扫描 data/ 目录下的顶层 .json 文件。
func (s *Server) scanDataJSONFiles() []JSONFileInfo {
	entries, err := os.ReadDir(s.cfg.DataDir)
	if err != nil {
		s.log.Warn("扫描数据目录 JSON 文件失败", "error", err.Error())
		return []JSONFileInfo{}
	}
	var res []JSONFileInfo
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		res = append(res, JSONFileInfo{
			Name:        name,
			Description: jsonFileDescription(name),
			SizeBytes:   info.Size(),
			ModTime:     info.ModTime().UnixMilli(),
		})
	}
	sort.Slice(res, func(i, j int) bool {
		return res[i].Name < res[j].Name
	})
	if res == nil {
		res = []JSONFileInfo{}
	}
	return res
}

// handleAPIBackupExportJSON 导出单个指定的 JSON 文件。
func (s *Server) handleAPIBackupExportJSON(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.backup.export.json"
	var in struct {
		Name string `json:"name"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	cleanName := filepath.Base(filepath.Clean(name))
	if cleanName == "" || cleanName != name || strings.Contains(name, "/") || strings.Contains(name, "\\") ||
		strings.HasPrefix(cleanName, ".") || !strings.HasSuffix(strings.ToLower(cleanName), ".json") {
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "非法的文件名称")
		return
	}

	filePath := filepath.Join(s.cfg.DataDir, cleanName)
	fi, err := os.Stat(filePath)
	if err != nil || fi.IsDir() || !fi.Mode().IsRegular() {
		writeAPIError(w, http.StatusNotFound, apiCodeNotFound, "指定的文件不存在或无法访问")
		return
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		s.log.Error("读取备份文件失败", "op", op, "file", cleanName, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "读取文件失败")
		return
	}

	s.audit(r.Context(), "backup.export.json", "store", map[string]any{
		"filename": cleanName,
		"bytes":    len(data),
	})

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, cleanName))
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleAPIBackupExportAllJSON 将 data/ 下所有 JSON 文件打包为 ZIP 流式输出。
func (s *Server) handleAPIBackupExportAllJSON(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.backup.export.all_json"
	jsonFiles := s.scanDataJSONFiles()
	if len(jsonFiles) == 0 {
		writeAPIError(w, http.StatusNotFound, apiCodeNotFound, "数据目录下无任何 JSON 配置文件可供导出")
		return
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	manifestFiles := make([]string, 0, len(jsonFiles))
	for _, jf := range jsonFiles {
		filePath := filepath.Join(s.cfg.DataDir, jf.Name)
		data, err := os.ReadFile(filePath)
		if err != nil {
			s.log.Warn("跳过不可读的 JSON 文件", "file", jf.Name, "error", err.Error())
			continue
		}
		if err := writeZipEntry(zw, jf.Name, data); err != nil {
			s.log.Error("打包 JSON 文件失败", "op", op, "file", jf.Name, "error", err.Error())
			writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "生成 ZIP 压缩包失败")
			return
		}
		manifestFiles = append(manifestFiles, jf.Name)
	}

	manifest := map[string]any{
		"app":        branding.DisplayName,
		"type":       "all_json",
		"created_at": s.now().UTC().Format(time.RFC3339),
		"files":      manifestFiles,
	}
	if manifestData, err := json.MarshalIndent(manifest, "", "  "); err == nil {
		_ = writeZipEntry(zw, "manifest.json", manifestData)
	}

	if err := zw.Close(); err != nil {
		s.log.Error("完成 JSON ZIP 压缩包失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "生成 ZIP 压缩包失败")
		return
	}

	now := s.now()
	filename := fmt.Sprintf("%s-json-backup-%s.zip", branding.StoragePrefix, now.In(s.tz(r.Context())).Format("20060102-150405"))

	s.audit(r.Context(), "backup.export.all_json", "store", map[string]any{
		"count": len(manifestFiles),
		"bytes": buf.Len(),
	})

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// handleAPIBackupExportFull 导出数据库快照与所有 JSON 文件的全量 ZIP 归档。
// 打包口径由 internal/backup.BuildFullZip 统一提供（与定时 R2 上传同一
// 实现；手动导出不排除任何 JSON，保持历史全量口径）。
func (s *Server) handleAPIBackupExportFull(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.backup.export.full"
	ctx := r.Context()
	dir := filepath.Dir(s.dbPath)

	tmpDb, err := os.CreateTemp(dir, branding.StoragePrefix+"-full-db-*.tmp")
	if err != nil {
		s.log.Error("创建全量导出临时文件失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "创建备份快照临时文件失败")
		return
	}
	tmpDbName := tmpDb.Name()
	_ = tmpDb.Close()
	_ = os.Remove(tmpDbName)
	defer os.Remove(tmpDbName)

	if err := s.st.BackupTo(ctx, tmpDbName); err != nil {
		s.log.Error("生成全量数据库快照失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "生成数据库快照失败")
		return
	}

	tmpZip, err := os.CreateTemp(dir, branding.StoragePrefix+"-full-backup-*.zip")
	if err != nil {
		s.log.Error("创建全量 ZIP 临时文件失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "创建压缩包临时文件失败")
		return
	}
	tmpZipName := tmpZip.Name()
	_ = tmpZip.Close()
	defer os.Remove(tmpZipName)

	now := s.now()
	stats, err := backup.BuildFullZip(s.cfg.DataDir, tmpDbName, tmpZipName, nil, now)
	if err != nil {
		s.log.Error("生成全量 ZIP 归档失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "生成压缩包失败")
		return
	}

	if raw, err := json.Marshal(now.UnixMilli()); err == nil {
		if err := s.st.SetSetting(ctx, settingKeyLastBackupAt, string(raw)); err != nil {
			s.log.Warn("记录最近备份时间失败", "error", err.Error())
		}
	}

	filename := fmt.Sprintf("%s-full-backup-%s.zip", branding.StoragePrefix, now.In(s.tz(ctx)).Format("20060102-150405"))

	s.audit(ctx, "backup.export.full", "store", map[string]any{
		"count":       stats.Files,
		"db_bytes":    stats.DBBytes,
		"total_bytes": stats.TotalBytes,
	})

	f, err := os.Open(tmpZipName)
	if err != nil {
		s.log.Error("打开全量备份包失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "读取备份文件状态失败")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	w.Header().Set("Content-Length", strconv.FormatInt(stats.TotalBytes, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

// handleAPIBackupImportJSON 上传单个 JSON 文件并安全覆盖。
func (s *Server) handleAPIBackupImportJSON(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.backup.import.json"
	r.Body = http.MaxBytesReader(w, r.Body, maxSingleJSONUploadSize)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "multipart_parse_failed"})
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "上传解析失败或文件超过大小限制")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, header, err := r.FormFile("file")
	if err != nil {
		file, header, err = r.FormFile("backup")
	}
	if err != nil {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "missing_file"})
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "请选择要上传的 JSON 文件")
		return
	}
	defer file.Close()

	targetName := strings.TrimSpace(r.FormValue("target_name"))
	if targetName == "" {
		targetName = header.Filename
	}
	cleanTarget := filepath.Base(filepath.Clean(targetName))
	if cleanTarget == "" || cleanTarget != targetName || strings.Contains(targetName, "/") || strings.Contains(targetName, "\\") ||
		strings.HasPrefix(cleanTarget, ".") || !strings.HasSuffix(strings.ToLower(cleanTarget), ".json") {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "invalid_target_filename", "name": targetName})
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "非法的文件名称，必须以 .json 结尾")
		return
	}

	data, err := io.ReadAll(io.LimitReader(file, maxSingleJSONUploadSize+1))
	if err != nil || int64(len(data)) > maxSingleJSONUploadSize {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "too_large_or_read_failed"})
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "文件过大或读取失败")
		return
	}

	if !json.Valid(data) {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "invalid_json", "name": cleanTarget})
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "上传的文件不是有效的 JSON 格式")
		return
	}

	tmpFile, err := os.CreateTemp(s.cfg.DataDir, ".import-json-*.tmp")
	if err != nil {
		s.log.Error("创建导入临时文件失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "创建导入临时文件失败")
		return
	}
	tmpName := tmpFile.Name()
	defer os.Remove(tmpName)

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "写入导入临时文件失败")
		return
	}
	_ = tmpFile.Chmod(0o600)
	_ = tmpFile.Close()

	destPath := filepath.Join(s.cfg.DataDir, cleanTarget)
	if err := os.Rename(tmpName, destPath); err != nil {
		s.log.Error("重命名导入文件失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal), "应用导入文件失败")
		return
	}

	s.audit(r.Context(), "backup.import.json", "store", map[string]any{
		"filename": cleanTarget,
		"bytes":    len(data),
	})

	writeAPISingle(w, struct {
		apiWriteOK
		Message  string        `json:"message"`
		Filename string        `json:"filename"`
		Backup   apiBackupView `json:"backup"`
	}{
		apiWriteOK: apiWriteOK{OK: true},
		Message:    fmt.Sprintf("文件 %s 导入成功。涉及 Telegram 会话或机器人长连接需重启服务以完全生效。", cleanTarget),
		Filename:   cleanTarget,
		Backup:     s.buildAPIBackupView(r.Context()),
	})
}

// handleAPIBackupImportAllJSON 上传 ZIP 压缩包并批量校验还原 JSON 文件。
func (s *Server) handleAPIBackupImportAllJSON(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.backup.import.all_json"
	r.Body = http.MaxBytesReader(w, r.Body, maxAllJSONUploadSize)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "multipart_parse_failed"})
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "上传解析失败或压缩包超过大小限制")
		return
	}
	defer func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}()

	file, _, err := r.FormFile("file")
	if err != nil {
		file, _, err = r.FormFile("backup")
	}
	if err != nil {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "missing_file"})
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "请选择要上传的 ZIP 备份包")
		return
	}
	defer file.Close()

	zipBytes, err := io.ReadAll(io.LimitReader(file, maxAllJSONUploadSize+1))
	if err != nil || int64(len(zipBytes)) > maxAllJSONUploadSize {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "zip_too_large"})
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "ZIP 备份包过大或读取失败")
		return
	}

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "invalid_zip"})
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "上传的文件不是有效的 ZIP 压缩包")
		return
	}

	validEntries := make(map[string][]byte)
	for _, f := range zr.File {
		clean := path.Clean(f.Name)
		// Zip Slip 严密检查
		if strings.HasPrefix(clean, "/") || strings.Contains(clean, "\\") || strings.HasPrefix(clean, "../") ||
			clean == "." || clean == ".." || strings.Contains(clean, "/../") {
			s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "zip_slip_detected", "entry": f.Name})
			writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "备份包内包含不安全的文件路径")
			return
		}
		if f.Mode().IsDir() {
			continue
		}
		baseName := path.Base(clean)
		if strings.EqualFold(baseName, "manifest.json") {
			continue
		}
		if strings.HasPrefix(baseName, ".") || !strings.HasSuffix(strings.ToLower(baseName), ".json") {
			continue
		}
		if f.UncompressedSize64 > maxSingleJSONUploadSize {
			writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, fmt.Sprintf("文件 %s 超过大小限制", baseName))
			return
		}

		rc, err := f.Open()
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, fmt.Sprintf("无法读取文件 %s", baseName))
			return
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxSingleJSONUploadSize+1))
		_ = rc.Close()
		if err != nil || int64(len(b)) > maxSingleJSONUploadSize {
			writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, fmt.Sprintf("文件 %s 读取失败或过大", baseName))
			return
		}
		if !json.Valid(b) {
			s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "invalid_json_in_zip", "file": baseName})
			writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, fmt.Sprintf("备份包内文件 %s 不是合法的 JSON", baseName))
			return
		}
		validEntries[baseName] = b
	}

	if len(validEntries) == 0 {
		s.audit(r.Context(), "backup.import.rejected", "store", map[string]any{"reason": "no_json_files"})
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "ZIP 备份包中未找到有效的 JSON 配置文件")
		return
	}

	// 全部校验通过，批量安全写入
	importedFiles := make([]string, 0, len(validEntries))
	for name, content := range validEntries {
		tmp, err := os.CreateTemp(s.cfg.DataDir, ".import-all-*.tmp")
		if err != nil {
			s.log.Error("创建批量导入临时文件失败", "op", op, "file", name, "error", err.Error())
			continue
		}
		tName := tmp.Name()
		if _, err := tmp.Write(content); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tName)
			continue
		}
		_ = tmp.Chmod(0o600)
		_ = tmp.Close()

		dest := filepath.Join(s.cfg.DataDir, name)
		if err := os.Rename(tName, dest); err != nil {
			_ = os.Remove(tName)
			s.log.Error("覆盖导入文件失败", "op", op, "file", name, "error", err.Error())
			continue
		}
		importedFiles = append(importedFiles, name)
	}

	s.audit(r.Context(), "backup.import.all_json", "store", map[string]any{
		"count": len(importedFiles),
		"files": importedFiles,
	})

	writeAPISingle(w, struct {
		apiWriteOK
		Message string        `json:"message"`
		Count   int           `json:"count"`
		Backup  apiBackupView `json:"backup"`
	}{
		apiWriteOK: apiWriteOK{OK: true},
		Message:    fmt.Sprintf("已成功导入 %d 个 JSON 配置文件。涉及 Telegram 会话或机器人长连接需重启服务以完全生效。", len(importedFiles)),
		Count:      len(importedFiles),
		Backup:     s.buildAPIBackupView(r.Context()),
	})
}
