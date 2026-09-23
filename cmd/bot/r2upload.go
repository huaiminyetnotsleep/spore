// R2 定时上传的执行步骤：本地快照成功后打全量 ZIP 直传 Cloudflare R2
// 并按份数轮转远端对象。配置文件（r2-backup.json）与云盘凭据
// （cloud-drive.json）不进上传包；临时 ZIP 落数据目录并在结束后清理。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/backup"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/r2backup"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// r2UploadTimeout 上传 + 远端轮转的整体时限：几十 MB 的备份 ZIP 在正常
// 网络下单请求即完成，10 分钟足够覆盖慢速链路；超时由 ClassifyError
// 归类为受控「上传超时」场景。
const r2UploadTimeout = 10 * time.Minute

// r2ZipExcludes 是上传包必须排除的凭据文件：R2 Secret 进包 = 拿到一份
// 备份即拿到 bucket 钥匙；云盘凭据同理不出机器（Web 手动全量导出口径
// 不变，仅上传版排除）。
var r2ZipExcludes = []string{r2backup.FileName, cloudarchive.FileName}

// runR2UploadStep 执行一次 R2 上传：空间预检 → 打全量 ZIP（排除凭据
// 文件）→ 上传 + 远端轮转 → 回写状态与审计。返回受控场景文案（空串 =
// 成功或功能未开启）供 backup.failed 事件上报与产生该场景的原始错误
// （成功或未开启为 nil，供错误日志中心落根因；原始错误不进事件文案）。
func runR2UploadStep(ctx context.Context, st *store.Store, dataDir, snapshotPath string, keep int, now time.Time, log *slog.Logger) (string, error) {
	cfg, err := r2backup.Load(dataDir)
	if err != nil {
		log.Error("读取 R2 备份配置失败，本次跳过上传", "error", err.Error())
		return "R2 配置文件不可读，上传已跳过", err
	}
	if !cfg.Enabled || !cfg.Complete() {
		return "", nil // 功能未开启（或草稿未填全）：不算失败，不产生事件
	}

	// 空间预检：本地快照已由 backup.Run 预留 ×1.2，这里只为临时 ZIP
	// 再按数据库大小 ×1.2 预留（ZIP 通常可压缩，取上界）。
	var zipWant int64 = 64 << 20
	if fi, err := os.Stat(filepath.Join(dataDir, branding.DatabaseFile)); err == nil {
		zipWant = fi.Size()
	}
	if err := backup.EnsureFreeSpace(dataDir, int64(float64(zipWant)*1.2)); err != nil {
		log.Warn("磁盘空间不足，R2 上传跳过", "error", err.Error())
		return "磁盘剩余空间不足，R2 上传已跳过", err
	}

	tmp, err := os.CreateTemp(dataDir, ".r2-upload-*.zip")
	if err != nil {
		log.Error("创建 R2 上传临时文件失败", "error", err.Error())
		return "R2 上传包构建失败", err
	}
	tmpName := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpName)

	if _, err := backup.BuildFullZip(dataDir, snapshotPath, tmpName, r2ZipExcludes, now); err != nil {
		log.Error("构建 R2 上传 ZIP 失败", "error", err.Error())
		return "R2 上传包构建失败", err
	}

	objStore, err := r2backup.NewStore(cfg)
	if err != nil {
		log.Error("构造 R2 客户端失败", "error", err.Error())
		return "R2 配置不完整", err
	}
	upCtx, cancel := context.WithTimeout(ctx, r2UploadTimeout)
	defer cancel()
	res, err := r2backup.UploadAndRotate(upCtx, objStore, tmpName, keep, now)
	if err != nil {
		// 上传成功但远端旧份清理失败：主目标已达成，按告警场景回执
		if res.Key != "" {
			scene := "R2 上传成功，但远端旧备份清理失败"
			log.Warn(scene, "key", res.Key, "error", err.Error())
			_ = r2backup.UpdateStatus(dataDir, now.UnixMilli(), scene)
			return scene, err
		}
		scene := r2backup.ClassifyError(err)
		log.Error("R2 上传失败", "scene", scene, "error", err.Error())
		_ = r2backup.UpdateStatus(dataDir, now.UnixMilli(), scene)
		return scene, err
	}
	log.Info("R2 上传完成", "key", res.Key, "bytes", res.SizeBytes, "removed", res.Removed)
	if err := r2backup.UpdateStatus(dataDir, now.UnixMilli(), ""); err != nil {
		log.Warn("回写 R2 上传状态失败", "error", err.Error())
	}
	_ = st.AppendAudit(ctx, store.AuditEntry{
		Actor:  "system",
		Action: "backup.r2_upload",
		Target: "r2",
		AfterJSON: fmt.Sprintf(`{"key":%q,"size_bytes":%d,"removed":%d}`,
			res.Key, res.SizeBytes, res.Removed),
	})
	return "", nil
}
