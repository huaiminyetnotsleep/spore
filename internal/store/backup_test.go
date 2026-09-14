package store

// 备份与状态计数测试：VACUUM INTO 快照一致性、各状态请求数统计。

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCountRequestsByStatus(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	seedRequest(t, s, seedReq{1, "alpha", 1, RequestQueued, 100, "", ""})
	seedRequest(t, s, seedReq{1, "alpha", 2, RequestProcessing, 200, "", ""})
	seedRequest(t, s, seedReq{1, "alpha", 3, RequestProcessing, 300, "", ""})
	seedRequest(t, s, seedReq{1, "alpha", 4, RequestSucceeded, 400, "text", ""})

	queued, processing, err := s.CountRequestsByStatus(ctx)
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if queued != 1 || processing != 2 {
		t.Fatalf("各状态计数不符：queued=%d processing=%d", queued, processing)
	}
}

func TestBackupTo(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)
	seedRequest(t, s, seedReq{1, "alpha", 1, RequestSucceeded, 100, "text", ""})

	dest := filepath.Join(t.TempDir(), "snapshot.db")
	if err := s.BackupTo(ctx, dest); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 快照是合法 SQLite 文件
	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("读取快照失败: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte("SQLite format 3\x00")) {
		t.Fatalf("快照应为 SQLite 文件，前缀 %q", raw[:16])
	}

	// 快照可作为数据库打开，且内容与源一致（源后续变更不影响快照）
	if _, err := s.CreateUser(ctx, User{ID: 99, Status: UserEnabled}); err != nil {
		t.Fatalf("源库追加用户失败: %v", err)
	}
	snap, err := Open(ctx, dest, testLogger())
	if err != nil {
		t.Fatalf("打开快照失败: %v", err)
	}
	defer snap.Close()
	users, err := snap.ListUsers(ctx)
	if err != nil {
		t.Fatalf("读取快照用户失败: %v", err)
	}
	if len(users) != 1 || users[0].ID != 1 {
		t.Fatalf("快照应只含备份时的用户：%+v", users)
	}

	// 目标已存在时 SQLite 拒绝（调用方须保证唯一临时名）
	if err := s.BackupTo(ctx, dest); err == nil {
		t.Fatal("目标已存在时备份应失败")
	}
}
