// 全量备份 ZIP 的共享构建：Web 手动「全量导出」与定时 R2 上传共用同一
// 打包口径（数据库快照 + data/ 顶层 JSON 配置 + manifest）。R2 上传版
// 通过 exclude 排除凭据文件（r2-backup.json / cloud-drive.json），确保
// Secret 不随备份出机器；Web 手动导出保持原有全量口径不变。
package backup

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/branding"
)

// FullZipStats 描述一次全量打包的产物规模（审计与事件用）。
type FullZipStats struct {
	Files      int   // 打包的 JSON 配置文件数
	DBBytes    int64 // 数据库快照字节数
	TotalBytes int64 // ZIP 总字节数
}

// BuildFullZip 把 dbSnapshotPath 的数据库快照 + dataDir 顶层全部 .json
// 配置（排除 exclude 指定的文件名与点开头文件）+ manifest.json 打包写入
// destZipPath（调用方负责创建与清理临时文件）。destZipPath 必须已存在
// 或可创建，函数以截断模式打开它。
func BuildFullZip(dataDir, dbSnapshotPath, destZipPath string, exclude []string, now time.Time) (FullZipStats, error) {
	excluded := make(map[string]struct{}, len(exclude))
	for _, name := range exclude {
		excluded[name] = struct{}{}
	}

	dbData, err := os.ReadFile(dbSnapshotPath)
	if err != nil {
		return FullZipStats{}, fmt.Errorf("读取数据库快照失败: %w", err)
	}

	f, err := os.Create(destZipPath)
	if err != nil {
		return FullZipStats{}, fmt.Errorf("创建压缩包临时文件失败: %w", err)
	}
	tmpName := f.Name()
	defer f.Close()
	zw := zip.NewWriter(f)

	if err := writeFullZipEntry(zw, branding.DatabaseFile, dbData); err != nil {
		return FullZipStats{}, err
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return FullZipStats{}, fmt.Errorf("扫描数据目录失败: %w", err)
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") ||
			!strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		if _, skip := excluded[name]; skip {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	manifestFiles := make([]string, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			// 与 Web 全量导出同口径：单文件不可读跳过而非整体失败
			continue
		}
		if err := writeFullZipEntry(zw, name, data); err != nil {
			return FullZipStats{}, err
		}
		manifestFiles = append(manifestFiles, name)
	}

	manifest := map[string]any{
		"app":        branding.DisplayName,
		"type":       "full",
		"created_at": now.UTC().Format(time.RFC3339),
		"db":         branding.DatabaseFile,
		"files":      manifestFiles,
	}
	if manifestData, err := json.MarshalIndent(manifest, "", "  "); err == nil {
		_ = writeFullZipEntry(zw, "manifest.json", manifestData)
	}

	if err := zw.Close(); err != nil {
		return FullZipStats{}, fmt.Errorf("完成全量 ZIP 归档失败: %w", err)
	}
	if err := f.Close(); err != nil {
		return FullZipStats{}, fmt.Errorf("关闭全量 ZIP 归档失败: %w", err)
	}

	stats := FullZipStats{Files: len(manifestFiles), DBBytes: int64(len(dbData))}
	if fi, err := os.Stat(tmpName); err == nil {
		stats.TotalBytes = fi.Size()
	}
	return stats, nil
}

func writeFullZipEntry(zw *zip.Writer, name string, data []byte) error {
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

// EnsureFreeSpace 校验 dir 所在文件系统剩余空间不低于 minBytes（R2 上传
// 前为临时 ZIP 预留空间的导出版预检，与备份预检同一 ENOSPC 防线）。
// 探测失败不阻塞（后续真实写入失败会自然报错）。
func EnsureFreeSpace(dir string, minBytes int64) error {
	return ensureFreeSpace(dir, minBytes)
}
