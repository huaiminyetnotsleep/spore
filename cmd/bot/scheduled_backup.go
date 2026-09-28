package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/backup"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/r2backup"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

type r2UploadStep func(context.Context, *store.Store, string, string, int, time.Time, *slog.Logger) (string, error)

func runScheduledBackup(ctx context.Context, st *store.Store, dataDir string, localEnabled bool, keep int, now time.Time, log *slog.Logger, upload r2UploadStep) (scene string, returnErr error) {
	output := ""
	if !localEnabled {
		cfg, err := r2backup.Load(dataDir)
		if err != nil {
			log.Error("读取 R2 备份配置失败", "error", err.Error())
			return "R2 配置文件不可读，定时备份已跳过", err
		}
		if !cfg.Enabled || !cfg.Complete() {
			err := errors.New("R2 未启用或配置不完整")
			log.Error("仅 R2 定时备份缺少有效的 R2 配置")
			return "仅 R2 定时备份缺少有效的 R2 配置", err
		}
		tmpDir, err := os.MkdirTemp(dataDir, ".scheduled-r2-snapshot-*")
		if err != nil {
			log.Error("创建 R2 临时快照目录失败", "error", err.Error())
			return "R2 临时快照创建失败", err
		}
		defer func() {
			if err := os.RemoveAll(tmpDir); err != nil {
				log.Error("清理 R2 临时快照失败", "error", err.Error())
				if returnErr == nil {
					scene = "R2 临时快照清理失败"
					returnErr = err
				}
			}
		}()
		output = filepath.Join(tmpDir, branding.DatabaseFile)
	}

	res, err := backup.Run(ctx, st, dataDir, output, keep, "system", now, log)
	if err != nil {
		scene := "定时备份执行失败"
		if errors.Is(err, backup.ErrInsufficientSpace) {
			scene = "磁盘剩余空间不足，定时备份已跳过"
		}
		log.Error("定时备份失败", "error", err.Error())
		return scene, err
	}
	return upload(ctx, st, dataDir, res.Path, keep, now, log)
}
