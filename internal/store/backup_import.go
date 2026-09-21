package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/branding"
)

// PendingImport 是备份导入的显式确认标记；只保存候选文件摘要和操作者，
// 不保存访问密钥、OAuth Secret 或 Web 会话。
type PendingImport struct {
	Version     int    `json:"version"`
	File        string `json:"file"`
	SHA256      string `json:"sha256"`
	ConfirmedAt int64  `json:"confirmed_at"`
	Actor       string `json:"actor"`
	Status      string `json:"status"`
}

const (
	PendingImportDBName     = "pending-import.db"
	PendingImportMarkerName = "pending-import.json"
	pendingImportVersion    = 1
)

var requiredBackupColumns = map[string][]string{
	"users":        {"id", "status", "username", "display_name"},
	"requests":     {"id", "user_id", "source_kind", "channel_key", "message_id", "status"},
	"usage_daily":  {"user_id", "day", "used"},
	"audit_log":    {"id", "at", "action"},
	"events":       {"id", "key", "severity", "message", "status"},
	"settings":     {"key", "value_json"},
	"web_sessions": {"id_hash", "expires_at", "csrf_token"},
}

// ValidateBackup 校验候选文件是当前 Spore 数据库快照，且不会修改文件。
func ValidateBackup(ctx context.Context, path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("读取备份文件失败: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return errors.New("备份必须是普通文件")
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return fmt.Errorf("打开备份失败: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("备份不是有效 SQLite 文件: %w", err)
	}
	var integrity string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || !strings.EqualFold(integrity, "ok") {
		if err == nil {
			err = errors.New(integrity)
		}
		return fmt.Errorf("备份完整性校验失败: %w", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("读取备份 schema 版本失败: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("备份 schema 版本 %d 高于当前程序支持的 %d", version, len(migrations))
	}
	for table, columns := range requiredBackupColumns {
		got, err := tableColumns(ctx, db, table)
		if err != nil {
			return err
		}
		for _, column := range columns {
			if !got[column] {
				return fmt.Errorf("备份缺少必要字段 %s.%s", table, column)
			}
		}
	}
	requestColumns, err := tableColumns(ctx, db, "requests")
	if err != nil {
		return err
	}
	for _, requirement := range []struct {
		version int
		table   string
		column  string
	}{
		{version: 8, table: "requests", column: "source_media_dc_ids_json"},
		{version: 9, table: "requests", column: "media_types_json"},
		{version: 11, table: "users", column: "cloud_download"},
		{version: 19, table: "watch_invite_requests", column: "id"},
	} {
		columns := requestColumns
		if requirement.table != "requests" {
			columns, err = tableColumns(ctx, db, requirement.table)
			if err != nil {
				return err
			}
		}
		if version >= requirement.version && !columns[requirement.column] {
			return fmt.Errorf("备份缺少 schema v%d 必要字段 %s.%s", requirement.version, requirement.table, requirement.column)
		}
	}
	return nil
}

func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("读取备份表结构 %s 失败: %w", table, err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("扫描备份表结构失败: %w", err)
		}
		out[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历备份表结构失败: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("备份缺少必要表 %s", table)
	}
	return out, nil
}

// PendingImportPath 返回数据目录内的待导入文件路径。
func PendingImportPath(dataDir string) string { return filepath.Join(dataDir, PendingImportDBName) }

// PendingImportMarkerPath 返回数据目录内的待导入标记路径。
func PendingImportMarkerPath(dataDir string) string {
	return filepath.Join(dataDir, PendingImportMarkerName)
}

// ReadPendingImport 读取待导入标记；不存在时返回 ErrNotFound。
func ReadPendingImport(dataDir string) (PendingImport, error) {
	data, err := os.ReadFile(PendingImportMarkerPath(dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return PendingImport{}, ErrNotFound
	}
	if err != nil {
		return PendingImport{}, err
	}
	var marker PendingImport
	if err := json.Unmarshal(data, &marker); err != nil {
		return PendingImport{}, fmt.Errorf("待导入标记格式无效: %w", err)
	}
	return marker, nil
}

// WritePendingImport 写入确认后的待导入标记，并以临时文件原子替换。
func WritePendingImport(dataDir, actor string, now time.Time) (PendingImport, error) {
	path := PendingImportPath(dataDir)
	if err := ValidateBackup(context.Background(), path); err != nil {
		return PendingImport{}, err
	}
	sum, err := fileSHA256(path)
	if err != nil {
		return PendingImport{}, err
	}
	marker := PendingImport{Version: pendingImportVersion, File: PendingImportDBName,
		SHA256: sum, ConfirmedAt: now.UnixMilli(), Actor: actor, Status: "confirmed"}
	data, err := json.Marshal(marker)
	if err != nil {
		return PendingImport{}, err
	}
	tmp, err := os.CreateTemp(dataDir, ".pending-import-*.tmp")
	if err != nil {
		return PendingImport{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return PendingImport{}, err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return PendingImport{}, err
	}
	if err := tmp.Close(); err != nil {
		return PendingImport{}, err
	}
	if err := os.Rename(tmpName, PendingImportMarkerPath(dataDir)); err != nil {
		return PendingImport{}, err
	}
	return marker, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ApplyPendingImport 在启动期应用已确认导入。普通启动（无 confirmed marker）不做任何操作。
// 候选库恢复当前 settings（访问密钥、OAuth、运行设置）并清空 Web 会话，
// 业务表由候选快照整体替换；最近一次旧库保留为 rollback 文件。
func ApplyPendingImport(ctx context.Context, dbPath, dataDir string, log *slog.Logger) (bool, error) {
	if log == nil {
		log = slog.Default()
	}
	marker, err := ReadPendingImport(dataDir)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if marker.Status != "confirmed" {
		return false, nil
	}
	candidate := filepath.Join(dataDir, filepath.Base(marker.File))
	if filepath.Base(marker.File) != PendingImportDBName {
		return false, markPendingFailed(dataDir, marker, "待导入文件名无效")
	}
	if sum, err := fileSHA256(candidate); err != nil || sum != marker.SHA256 {
		if err == nil {
			err = errors.New("文件摘要不匹配")
		}
		return false, markPendingFailed(dataDir, marker, err.Error())
	}
	if err := ValidateBackup(ctx, candidate); err != nil {
		return false, markPendingFailed(dataDir, marker, err.Error())
	}

	currentSettings, err := readSettings(ctx, dbPath)
	if err != nil {
		return false, markPendingFailed(dataDir, marker, err.Error())
	}
	if err := applySettings(ctx, candidate, currentSettings); err != nil {
		return false, markPendingFailed(dataDir, marker, err.Error())
	}

	rollback := ""
	if _, err := os.Stat(dbPath); err == nil {
		rollback = dbPath + ".rollback-" + time.Now().UTC().Format("20060102-150405.000000000")
		// 当前库使用 WAL 时，主文件可能还依赖 -wal；先 checkpoint 后再
		// 复制回滚点，否则替换后会把旧 WAL/SHM 与新数据库混用。
		if err := checkpointDatabase(ctx, dbPath); err != nil {
			return false, markPendingFailed(dataDir, marker, err.Error())
		}
		if err := copyFile(dbPath, rollback); err != nil {
			return false, markPendingFailed(dataDir, marker, err.Error())
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, markPendingFailed(dataDir, marker, err.Error())
	}
	if err := removeSQLiteSidecars(dbPath); err != nil {
		return false, markPendingFailed(dataDir, marker, err.Error())
	}
	if err := removeSQLiteSidecars(candidate); err != nil {
		return false, markPendingFailed(dataDir, marker, err.Error())
	}
	if err := os.Rename(candidate, dbPath); err != nil {
		return false, markPendingFailed(dataDir, marker, err.Error())
	}
	if err := os.Remove(PendingImportMarkerPath(dataDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		// marker 未清理意味着下次启动会再次尝试；恢复替换前状态，避免
		// 把“已替换但仍 confirmed”的半成功状态留给下一次启动。
		restoreErr := restoreDatabaseAfterImport(dbPath, candidate, rollback)
		if restoreErr == nil {
			appendImportRollbackAudit(dataDir)
		}
		failureErr := markPendingFailed(dataDir, marker, "清理待导入标记失败")
		if restoreErr != nil {
			return false, fmt.Errorf("清理待导入标记失败，且回滚失败: %w", restoreErr)
		}
		return false, failureErr
	}
	log.Info("已应用待导入数据库备份", "rollback", rollback)
	return true, nil
}

func readSettings(ctx context.Context, path string) (map[string]string, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT key, value_json FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func applySettings(ctx context.Context, candidate string, settings map[string]string) error {
	db, err := sql.Open("sqlite", dsn(candidate))
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM settings"); err != nil {
		return err
	}
	for key, value := range settings {
		if _, err := tx.ExecContext(ctx, "INSERT INTO settings(key, value_json) VALUES (?, ?)", key, value); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM web_sessions"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log(at, actor, action, target, after_json)
		VALUES (?, ?, ?, ?, ?)`, nowMillis(), "system", "backup.import.applied", "store", `{"status":"applied"}`); err != nil {
		return err
	}
	return tx.Commit()
}

func markPendingFailed(dataDir string, marker PendingImport, reason string) error {
	// 启动期尚未打开 Store，失败结果仍尽力写入当前业务库的脱敏审计；
	// 当前库损坏或不可访问时保留 marker 错误作为唯一恢复线索。
	appendImportFailureAudit(dataDir)
	marker.Status = "failed"
	marker.Actor = "system"
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("导入失败（%s），且无法写入失败标记: %w", reason, err)
	}
	if err := os.WriteFile(PendingImportMarkerPath(dataDir), data, 0o600); err != nil {
		return fmt.Errorf("导入失败（%s），且无法写入失败标记: %w", reason, err)
	}
	return fmt.Errorf("应用备份失败: %s", reason)
}

func restoreDatabaseAfterImport(dbPath, candidate, rollback string) error {
	if err := removeSQLiteSidecars(dbPath); err != nil {
		return err
	}
	if rollback != "" {
		if err := os.Remove(dbPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Rename(rollback, dbPath)
	}
	return os.Rename(dbPath, candidate)
}

func appendImportFailureAudit(dataDir string) {
	appendImportAudit(dataDir, "backup.import.failed", "failed")
}

func appendImportRollbackAudit(dataDir string) {
	appendImportAudit(dataDir, "backup.import.rollback", "rolled_back")
}

func appendImportAudit(dataDir, action, status string) {
	path := filepath.Join(dataDir, branding.DatabaseFile)
	if _, err := os.Stat(path); err != nil {
		return
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return
	}
	after, _ := json.Marshal(map[string]string{"status": status})
	_, _ = db.ExecContext(ctx, `INSERT INTO audit_log(at, actor, action, target, after_json)
		VALUES (?, ?, ?, ?, ?)`, nowMillis(), "system", action, "store", string(after))
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	complete := false
	defer func() {
		if !complete {
			_ = out.Close()
			_ = os.Remove(dst)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	complete = true
	return nil
}

// checkpointDatabase 把当前 SQLite WAL 合并回主文件，确保复制回滚点时是完整快照。
func checkpointDatabase(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return fmt.Errorf("打开当前数据库以 checkpoint 失败: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("连接当前数据库以 checkpoint 失败: %w", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return fmt.Errorf("checkpoint 当前数据库失败: %w", err)
	}
	return nil
}

// removeSQLiteSidecars 删除指定数据库的 WAL/SHM 文件；替换主文件前不能留下旧侧文件。
func removeSQLiteSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除 SQLite 侧文件失败: %w", err)
		}
	}
	return nil
}
