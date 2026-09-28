package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/r2backup"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func scheduledBackupTestStore(t *testing.T, dataDir string) (*store.Store, *slog.Logger) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(context.Background(), filepath.Join(dataDir, branding.DatabaseFile), log)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, log
}

func completeScheduledR2Config() r2backup.Config {
	return r2backup.Config{
		Enabled:         true,
		AccountID:       "0123456789abcdef0123456789abcdef",
		AccessKeyID:     "access-key-123",
		SecretAccessKey: "secret-key-123",
		Bucket:          "spore-backup",
	}
}

func TestRunScheduledBackupR2OnlyCleansTemporarySnapshot(t *testing.T) {
	for _, uploadErr := range []error{nil, errors.New("upload failed")} {
		name := "success"
		if uploadErr != nil {
			name = "upload failure"
		}
		t.Run(name, func(t *testing.T) {
			dataDir := t.TempDir()
			if err := r2backup.Save(dataDir, completeScheduledR2Config()); err != nil {
				t.Fatal(err)
			}
			st, log := scheduledBackupTestStore(t, dataDir)
			var snapshotPath string
			scene, err := runScheduledBackup(context.Background(), st, dataDir, false, 3, time.Now(), log,
				func(_ context.Context, _ *store.Store, _ string, path string, _ int, _ time.Time, _ *slog.Logger) (string, error) {
					snapshotPath = path
					if _, statErr := os.Stat(path); statErr != nil {
						t.Fatalf("上传阶段应能读取临时快照: %v", statErr)
					}
					if filepath.Base(filepath.Dir(path)) == "backups" {
						t.Fatal("R2-only 快照不应写入本地备份目录")
					}
					if !strings.HasPrefix(filepath.Base(filepath.Dir(path)), ".scheduled-r2-snapshot-") {
						t.Fatalf("临时快照目录名不可识别: %s", filepath.Dir(path))
					}
					if uploadErr != nil {
						return "R2 upload failed", uploadErr
					}
					return "", nil
				})
			if (uploadErr == nil && err != nil) || (uploadErr != nil && !errors.Is(err, uploadErr)) {
				t.Fatalf("返回错误不符合上传结果: scene=%q err=%v", scene, err)
			}
			if uploadErr != nil && scene != "R2 upload failed" {
				t.Fatalf("上传场景未透传: %q", scene)
			}
			if snapshotPath == "" {
				t.Fatal("上传步骤未收到快照路径")
			}
			if _, statErr := os.Stat(filepath.Dir(snapshotPath)); !os.IsNotExist(statErr) {
				t.Fatalf("临时快照目录应在流程结束后删除，stat err=%v", statErr)
			}
			if _, statErr := os.Stat(filepath.Join(dataDir, "backups")); !os.IsNotExist(statErr) {
				t.Fatalf("R2-only 不应创建本地备份目录，stat err=%v", statErr)
			}
		})
	}
}

func TestRunScheduledBackupKeepsLocalSnapshot(t *testing.T) {
	dataDir := t.TempDir()
	st, log := scheduledBackupTestStore(t, dataDir)
	var snapshotPath string
	scene, err := runScheduledBackup(context.Background(), st, dataDir, true, 3, time.Now(), log,
		func(_ context.Context, _ *store.Store, _ string, path string, _ int, _ time.Time, _ *slog.Logger) (string, error) {
			snapshotPath = path
			return "", nil
		})
	if err != nil || scene != "" {
		t.Fatalf("本地备份流程应成功: scene=%q err=%v", scene, err)
	}
	if filepath.Base(filepath.Dir(snapshotPath)) != "backups" {
		t.Fatalf("本地快照应保留到 backups 目录: %s", snapshotPath)
	}
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("本地快照应保留: %v", err)
	}
}
