package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
)

const cloudBackupMultipartOverhead = 64 << 10

type apiCloudBackupStatus struct {
	cloudarchive.PendingStatus
	RollbackAvailable bool `json:"rollback_available"`
}

func (s *Server) requireCloudBackup(w http.ResponseWriter, r *http.Request, op string) bool {
	if !s.apiRequireCloudCfg(w, r, op) {
		return false
	}
	if s.cloudPending == nil {
		s.log.Warn("云盘备份服务端加密密钥未接入", "op", op)
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
			"云盘备份服务端加密密钥不可用，请完成配置后重试。")
		return false
	}
	return true
}

// handleAPICloudBackupStatus 返回待确认候选与最近回滚点状态（认证、无 CSRF）。
func (s *Server) handleAPICloudBackupStatus(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.cloud_drive.backup.status"
	if !s.requireCloudBackup(w, r, op) {
		return
	}
	pending, err := s.cloudPending.Status()
	if err != nil {
		s.log.Warn("读取云盘备份候选状态失败", "op", op)
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR",
			"读取云盘备份状态失败，请稍后重试。")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPISingle(w, apiCloudBackupStatus{PendingStatus: pending, RollbackAvailable: s.cloudCfg.HasRollback()})
}

// handleAPICloudBackupExport 创建密码保护的 ZIP v1 并作为附件返回。
func (s *Server) handleAPICloudBackupExport(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.cloud_drive.backup.export"
	if !s.requireCloudBackup(w, r, op) {
		return
	}
	var in struct {
		Password             string `json:"password"`
		PasswordConfirmation string `json:"password_confirmation"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if len([]rune(in.Password)) < cloudarchive.MinBackupPasswordLength {
		s.apiBadRequest(w, r, op, "备份密码至少需要 8 个字符。")
		return
	}
	if in.Password != in.PasswordConfirmation {
		s.apiBadRequest(w, r, op, "两次输入的备份密码不一致。")
		return
	}
	data, meta, err := cloudarchive.ExportBackup(s.cloudCfg.Snapshot(), in.Password, s.version, s.now())
	if err != nil {
		s.auditCloudBackup(r, "cloud_drive.backup.failed", cloudarchive.BackupMetadata{}, "failed")
		s.log.Warn("导出云盘配置备份失败", "op", op)
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "导出云盘配置备份失败，请稍后重试。")
		return
	}
	s.auditCloudBackup(r, "cloud_drive.backup.export", meta, "success")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="spore-cloud-drive-%s.zip"`, s.now().UTC().Format("20060102-150405")))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// handleAPICloudBackupImport 上传并验证加密包，只保存服务端再次加密的候选 Config。
func (s *Server) handleAPICloudBackupImport(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.cloud_drive.backup.import"
	if !s.requireCloudBackup(w, r, op) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, cloudarchive.MaxBackupSize+cloudBackupMultipartOverhead)
	parseErr := r.ParseMultipartForm(cloudarchive.MaxBackupSize + cloudBackupMultipartOverhead)
	defer func() {
		if r.MultipartForm != nil {
			if err := r.MultipartForm.RemoveAll(); err != nil {
				s.log.Warn("清理云盘备份上传临时文件失败")
			}
		}
	}()
	if parseErr != nil {
		s.auditCloudBackup(r, "cloud_drive.backup.failed", cloudarchive.BackupMetadata{}, "failed")
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "云盘备份上传失败或请求过大。")
		return
	}
	password := r.FormValue("password")
	if len([]rune(password)) < cloudarchive.MinBackupPasswordLength {
		s.apiBadRequest(w, r, op, "备份密码至少需要 8 个字符。")
		return
	}
	file, _, err := r.FormFile("backup")
	if err != nil {
		s.apiBadRequest(w, r, op, "请选择云盘配置备份 ZIP 文件。")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, cloudarchive.MaxBackupSize+1))
	if err != nil || len(data) > cloudarchive.MaxBackupSize {
		s.auditCloudBackup(r, "cloud_drive.backup.failed", cloudarchive.BackupMetadata{}, "failed")
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, "云盘备份文件超过 4 MiB 或读取失败。")
		return
	}
	cfg, meta, err := cloudarchive.ImportBackup(data, password)
	if err != nil {
		s.auditCloudBackup(r, "cloud_drive.backup.failed", meta, "failed")
		message := "云盘备份包无效或密码错误。"
		if errors.Is(err, cloudarchive.ErrBackupTooLarge) {
			message = "云盘备份文件超过 4 MiB 大小上限。"
		}
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, message)
		return
	}
	if err := s.cloudPending.Save(cfg, meta, s.now()); err != nil {
		s.auditCloudBackup(r, "cloud_drive.backup.failed", meta, "failed")
		s.log.Warn("保存云盘配置候选失败", "op", op)
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "保存待确认云盘配置失败，请稍后重试。")
		return
	}
	s.auditCloudBackup(r, "cloud_drive.backup.upload", meta, "validated")
	status, _ := s.cloudPending.Status()
	writeAPISingle(w, struct {
		apiWriteOK
		Candidate cloudarchive.PendingStatus `json:"candidate"`
	}{apiWriteOK{OK: true}, status})
}

// handleAPICloudBackupConfirm 二次确认后整体恢复文件与 Manager 在线快照。
func (s *Server) handleAPICloudBackupConfirm(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.cloud_drive.backup.confirm"
	if !s.requireCloudBackup(w, r, op) {
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.Confirm != "import_cloud_drive" {
		s.apiBadRequest(w, r, op, "请确认整体替换云盘配置。")
		return
	}
	var applied cloudarchive.PendingStatus
	err := s.cloudPending.Apply(func(cfg cloudarchive.Config, status cloudarchive.PendingStatus) error {
		applied = status
		s.auditCloudBackupStatus(r, "cloud_drive.backup.confirm", status, "confirmed")
		return s.cloudCfg.Restore(cfg)
	})
	if errors.Is(err, cloudarchive.ErrPendingNotFound) {
		writeAPIError(w, http.StatusConflict, apiCodeConflict, "没有待确认的云盘配置备份。")
		return
	}
	if err != nil {
		s.auditCloudBackupStatus(r, "cloud_drive.backup.failed", applied, "failed")
		s.log.Warn("应用云盘配置备份失败", "op", op)
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "应用云盘配置备份失败，当前配置保持不变。")
		return
	}
	s.auditCloudBackupStatus(r, "cloud_drive.backup.applied", applied, "success")
	writeAPISingle(w, struct {
		apiWriteOK
		RollbackAvailable bool `json:"rollback_available"`
	}{apiWriteOK{OK: true}, s.cloudCfg.HasRollback()})
}

// handleAPICloudBackupPendingDelete 取消待确认候选（认证 + CSRF）。
func (s *Server) handleAPICloudBackupPendingDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.cloud_drive.backup.cancel"
	if !s.requireCloudBackup(w, r, op) {
		return
	}
	status, _ := s.cloudPending.Status()
	if err := s.cloudPending.Delete(); errors.Is(err, cloudarchive.ErrPendingNotFound) {
		writeAPIError(w, http.StatusConflict, apiCodeConflict, "没有待取消的云盘配置备份。")
		return
	} else if err != nil {
		s.log.Warn("取消云盘配置候选失败", "op", op)
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "取消待确认云盘配置失败，请稍后重试。")
		return
	}
	s.auditCloudBackupStatus(r, "cloud_drive.backup.cancel", status, "cancelled")
	writeAPISingle(w, apiWriteOK{OK: true})
}

// handleAPICloudBackupRollback 恢复最近一次 Restore 前的配置并更新在线快照。
func (s *Server) handleAPICloudBackupRollback(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.cloud_drive.backup.rollback"
	if !s.requireCloudBackup(w, r, op) {
		return
	}
	var in struct {
		Confirm string `json:"confirm"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.Confirm != "rollback_cloud_drive" {
		s.apiBadRequest(w, r, op, "请确认回滚云盘配置。")
		return
	}
	if !s.cloudCfg.HasRollback() {
		writeAPIError(w, http.StatusConflict, apiCodeConflict, "没有可用的云盘配置回滚点。")
		return
	}
	if err := s.cloudCfg.Rollback(); err != nil {
		s.auditCloudBackup(r, "cloud_drive.backup.failed", cloudarchive.BackupMetadata{}, "failed")
		s.log.Warn("回滚云盘配置失败", "op", op)
		writeAPIError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "回滚云盘配置失败，当前配置保持不变。")
		return
	}
	meta := cloudarchive.BackupMetadata{DestinationNames: cloudDestinationNames(s.cloudCfg.Snapshot())}
	s.auditCloudBackup(r, "cloud_drive.backup.rollback", meta, "success")
	writeAPISingle(w, apiWriteOK{OK: true})
}

func (s *Server) auditCloudBackup(r *http.Request, action string, meta cloudarchive.BackupMetadata, result string) {
	detail := map[string]any{"result": result}
	if meta.PayloadSHA256 != "" {
		detail["hash"] = meta.PayloadSHA256
	}
	if meta.FormatVersion != 0 {
		detail["format"] = meta.FormatVersion
	}
	if meta.PackageSize != 0 {
		detail["size"] = meta.PackageSize
	}
	if meta.DestinationNames != nil {
		detail["destination_names"] = meta.DestinationNames
	}
	s.audit(r.Context(), action, "cloud_drive", detail)
}

func (s *Server) auditCloudBackupStatus(r *http.Request, action string, status cloudarchive.PendingStatus, result string) {
	s.auditCloudBackup(r, action, cloudarchive.BackupMetadata{
		FormatVersion: status.FormatVersion, PayloadSHA256: status.SHA256,
		PackageSize: status.Size, DestinationNames: status.DestinationNames,
	}, result)
}

func cloudDestinationNames(cfg cloudarchive.Config) []string {
	names := make([]string, 0, len(cfg.Destinations))
	for _, destination := range cfg.Destinations {
		names = append(names, destination.Name)
	}
	return names
}
