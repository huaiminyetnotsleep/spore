// Package backup 提供数据库一致性备份的共用核心：CLI（spore admin backup）
// 与容器内定时备份共用同一实现。备份经 SQLite 在线备份（VACUUM INTO）生成
// 一致快照，只覆盖业务数据库——session.json / peers.json / bots.json 等
// 运行文件不在其内（全量恢复需另经 Web 备份页"全量导出"，见 operations
// 手册）。成功后更新 last_backup_at（与 Web 手动导出同口径）并按 mtime
// 轮转保留最近 N 份。
package backup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// 文件名前缀（轮转只管理此前缀的 .db 文件）。
const (
	filePrefix = "spore-backup-"
	fileSuffix = ".db"
)

// ErrInsufficientSpace 是备份前的磁盘剩余空间检查未通过（调用方据此给出
// 不同于一般故障的提示与事件场景）。
var ErrInsufficientSpace = errors.New("backup: 磁盘剩余空间不足，跳过备份")

// Result 是一次成功备份的产物描述。
type Result struct {
	Path      string // 备份文件绝对路径
	SizeBytes int64
}

// Run 执行一次备份：空间预检 → VACUUM INTO 快照 → 更新 last_backup_at →
// 轮转保留最近 keep 份。output 为空时落 data/backups 并参与轮转；指定
// output 时备份到该精确路径（不参与轮转，供 CLI --output 场景）。
// actor 进审计（cli / system / admin）。磁盘空间按 DB 大小 ×1.2 预留，
// 这是本项目 ENOSPC 前科（分段切段根因）之后的硬防线。
func Run(ctx context.Context, st *store.Store, dataDir, output string, keep int, actor string, now time.Time, log *slog.Logger) (Result, error) {
	if st == nil {
		return Result{}, apperr.New(apperr.CodeInternal, "backup: Store 为必填项")
	}
	if keep < 1 {
		keep = syscfg.DefaultBackupKeepCount
	}
	dbPath := filepath.Join(dataDir, branding.DatabaseFile)
	backupDir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return Result{}, apperr.Wrap(apperr.CodeInternal, fmt.Errorf("创建备份目录失败: %w", err))
	}

	dest := output
	rotatable := false
	if dest == "" {
		// 秒级时间戳撞名（同一秒重试）时递增序号保证唯一——VACUUM INTO
		// 要求目标文件不存在。
		base := filepath.Join(backupDir, filePrefix+now.Format("20060102-150405"))
		dest = base + fileSuffix
		for seq := 1; ; seq++ {
			if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
				break
			} else if err != nil {
				return Result{}, apperr.Wrap(apperr.CodeInternal, fmt.Errorf("检查备份目标失败: %w", err))
			}
			dest = fmt.Sprintf("%s-%d%s", base, seq, fileSuffix)
		}
		rotatable = true
	} else if _, err := os.Stat(dest); err == nil {
		return Result{}, apperr.New(apperr.CodeInvalidURL, "备份目标文件已存在")
	}

	if err := checkDiskSpace(backupDir, dbPath); err != nil {
		return Result{}, err
	}

	if err := st.BackupTo(ctx, dest); err != nil {
		return Result{}, err
	}
	size := int64(0)
	if fi, err := os.Stat(dest); err == nil {
		size = fi.Size()
	}

	// 与 Web 手动导出同一口径：成功即刷新最近备份时间
	if err := syscfg.SetLastBackupAt(ctx, st, now.UnixMilli()); err != nil {
		log.Warn("更新最近备份时间失败", "error", err.Error())
	}
	_ = st.AppendAudit(ctx, store.AuditEntry{
		Actor:  actor,
		Action: "backup.export",
		Target: "database",
		AfterJSON: fmt.Sprintf(`{"path":%q,"size_bytes":%d,"rotated":%t}`,
			filepath.Base(dest), size, rotatable),
	})
	log.Info("数据库备份完成", "path", dest, "size_bytes", size)

	if rotatable {
		if removed, err := rotate(backupDir, keep); err != nil {
			log.Warn("备份轮转失败（不影响本次备份）", "error", err.Error())
		} else if removed > 0 {
			log.Info("备份轮转完成", "removed", removed, "keep", keep)
		}
	}
	return Result{Path: dest, SizeBytes: size}, nil
}

// checkDiskSpace 校验备份目录所在文件系统的剩余空间：按数据库文件大小
// ×1.2 预留（快照不含 WAL 空洞时通常更小，留足余量）。数据库尚未创建
// （首次启动前）时按 64MB 兜底。剩余不足返回 ErrInsufficientSpace。
func checkDiskSpace(backupDir, dbPath string) error {
	var want int64 = 64 << 20
	if fi, err := os.Stat(dbPath); err == nil {
		want = int64(float64(fi.Size()) * 1.2)
	}
	return ensureFreeSpace(backupDir, want)
}

// ensureFreeSpace 是空间预检的共用实现（备份快照与 R2 临时 ZIP 共用）。
func ensureFreeSpace(dir string, want int64) error {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		// 空间探测失败不阻塞备份（备份本身失败会自然报错）
		return nil
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	if free < want {
		return apperr.Wrap(apperr.CodeTempDirFull, fmt.Errorf("%w：需要约 %d 字节，剩余 %d 字节",
			ErrInsufficientSpace, want, free))
	}
	return nil
}

// rotate 删除 backups 目录中超出 keep 份的旧备份（按修改时间新→旧排序，
// 保留最新 keep 份），返回删除数。只管理 spore-backup-*.db 命名。
func rotate(backupDir string, keep int) (int, error) {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return 0, err
	}
	type item struct {
		name string
		mti  time.Time
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), filePrefix) || !strings.HasSuffix(e.Name(), fileSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{name: e.Name(), mti: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mti.After(items[j].mti) })
	if len(items) <= keep {
		return 0, nil // 现存份数未超上限
	}
	removed := 0
	for _, it := range items[keep:] {
		if err := os.Remove(filepath.Join(backupDir, it.name)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
