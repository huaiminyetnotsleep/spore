package backup

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

// TestRunCreatesRotatingBackup 基本回环：备份落 data/backups、last_backup_at
// 刷新、审计写入；连续备份按 keep 轮转删最老。
func TestRunCreatesRotatingBackup(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "spore.db"), testLog())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	base := time.Date(2026, 9, 22, 10, 0, 0, 0, time.Local)
	// keep=3 连备 5 次（时间递增保证文件名与 mtime 单调）
	for i := 0; i < 5; i++ {
		res, err := Run(ctx, st, dir, "", 3, "cli", base.Add(time.Duration(i)*time.Second), testLog())
		if err != nil {
			t.Fatalf("第 %d 次备份失败: %v", i+1, err)
		}
		if res.SizeBytes <= 0 {
			t.Fatalf("备份应有内容: %+v", res)
		}
	}
	if last := syscfg.LoadLastBackupAt(ctx, st); last != base.Add(4*time.Second).UnixMilli() {
		t.Fatalf("last_backup_at 应为最后一次备份时间: %d", last)
	}
	// 轮转：只剩 3 份，且是最近的
	entries, err := filepath.Glob(filepath.Join(dir, "backups", "spore-backup-*.db"))
	if err != nil || len(entries) != 3 {
		t.Fatalf("轮转后应剩 3 份: n=%d err=%v", len(entries), err)
	}
	wantNewest := filepath.Join(dir, "backups", "spore-backup-20260922-100004.db")
	if entries[0] != wantNewest && entries[2] != wantNewest {
		t.Fatalf("最新一份应保留: %v", entries)
	}
	// 备份可打开（一致性快照）
	snap, err := store.Open(ctx, entries[0], testLog())
	if err != nil {
		t.Fatalf("备份文件应可打开: %v", err)
	}
	_ = snap.Close()
}

// TestRunOutputExactPath 指定 --output 时备份到精确路径、不参与轮转、
// 目标已存在拒绝。
func TestRunOutputExactPath(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "spore.db"), testLog())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	out := filepath.Join(dir, "custom.db")
	if _, err := Run(ctx, st, dir, out, 2, "cli", time.Now(), testLog()); err != nil {
		t.Fatalf("指定路径备份失败: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("备份应落在指定路径: %v", err)
	}
	// 已存在拒绝（VACUUM INTO 硬性约束）
	if _, err := Run(ctx, st, dir, out, 2, "cli", time.Now(), testLog()); err == nil {
		t.Fatal("目标已存在应拒绝")
	}
	// 不参与轮转：默认目录为空
	entries, _ := filepath.Glob(filepath.Join(dir, "backups", "spore-backup-*.db"))
	if len(entries) != 0 {
		t.Fatalf("指定路径不应在轮转目录产生文件: %v", entries)
	}
}

// TestRotateOnlyManagesOwnPrefix 轮转只删除 spore-backup-*.db，其他文件
// （手工放置、导出 JSON 等）不受影响。
func TestRotateOnlyManagesOwnPrefix(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, offset time.Duration) {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		past := time.Now().Add(-24*time.Hour + offset)
		if err := os.Chtimes(path, past, past); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		mk(sporeBackupName(i), time.Duration(i)*time.Minute)
	}
	mk("manual-export.db", 0)
	mk("notes.txt", 0)

	removed, err := rotate(dir, 2)
	if err != nil || removed != 2 {
		t.Fatalf("应删除 2 份旧备份: removed=%d err=%v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "manual-export.db")); err != nil {
		t.Fatalf("无关文件不应被删除: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatalf("无关文件不应被删除: %v", err)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, "spore-backup-*.db"))
	if len(entries) != 2 {
		t.Fatalf("应保留最新 2 份: %v", entries)
	}
}

func sporeBackupName(i int) string {
	return filePrefix + "2026010" + string(rune('1'+i)) + "-000000" + fileSuffix
}

// TestCheckDiskSpaceStatFailureIsNotFatal 空间探测失败（路径不可达）不阻塞
// 备份流程（备份自身失败会自然报错）。
func TestCheckDiskSpaceStatFailureIsNotFatal(t *testing.T) {
	if err := checkDiskSpace("/nonexistent-path-xyz", "/nonexistent-db"); err != nil {
		t.Fatalf("探测失败应放行: %v", err)
	}
}
