package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateUserProfileKeepsOtherFields(t *testing.T) {
	s := openTestStore(t)
	u := mustUser(t, s, 7)
	if err := s.SetOwner(context.Background(), u.ID, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}
	if err := s.UpdateUserProfile(context.Background(), u.ID, "alice", "Alice Doe"); err != nil {
		t.Fatalf("更新资料失败: %v", err)
	}
	got, err := s.GetUser(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if got.Username != "alice" || got.DisplayName != "Alice Doe" || !got.IsOwner || got.Status != UserPending {
		t.Fatalf("资料更新不应影响其他字段: %+v", got)
	}
	if err := s.UpdateUserProfile(context.Background(), u.ID, "", ""); err != nil {
		t.Fatalf("清空资料失败: %v", err)
	}
	got, _ = s.GetUser(context.Background(), u.ID)
	if got.Username != "" || got.DisplayName != "" {
		t.Fatalf("空资料应清除旧值: %+v", got)
	}
}

func TestValidateBackupRejectsMissingVersionedRequestColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-v9.db")
	s, err := Open(context.Background(), path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(context.Background(), "ALTER TABLE requests DROP COLUMN media_types_json"); err != nil {
		t.Fatalf("构造缺列备份失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	err = ValidateBackup(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "requests.media_types_json") {
		t.Fatalf("v9 备份缺少 media_types_json 应被拒绝，得到 %v", err)
	}
}

func TestApplyPendingImportPreservesSettingsAndClearsSessions(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	currentPath := filepath.Join(dataDir, "spore.db")
	current, err := Open(context.Background(), currentPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := current.CreateUser(context.Background(), User{ID: 1, Status: UserEnabled}); err != nil {
		t.Fatal(err)
	}
	if err := current.SetSetting(context.Background(), "access_key_hash", `"current"`); err != nil {
		t.Fatal(err)
	}
	if err := current.SetSetting(context.Background(), "max_file_size", "123"); err != nil {
		t.Fatal(err)
	}
	if err := current.CreateWebSession(context.Background(), WebSession{IDHash: "old", CSRFToken: "csrf", CreatedAt: 1, ExpiresAt: 999999}); err != nil {
		t.Fatal(err)
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}
	// 模拟异常退出遗留的 SQLite 侧文件；替换主库时必须清理，不能与新库混用。
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.WriteFile(currentPath+suffix, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	sourcePath := filepath.Join(root, "source.db")
	source, err := Open(context.Background(), sourcePath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.CreateUser(context.Background(), User{ID: 2, Status: UserPending}); err != nil {
		t.Fatal(err)
	}
	if err := source.SetSetting(context.Background(), "access_key_hash", `"imported"`); err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	pending := PendingImportPath(dataDir)
	if err := copyFile(sourcePath, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := WritePendingImport(dataDir, "admin", time.Now()); err != nil {
		t.Fatal(err)
	}

	applied, err := ApplyPendingImport(context.Background(), currentPath, dataDir, testLogger())
	if err != nil || !applied {
		t.Fatalf("待导入应成功应用: applied=%v err=%v", applied, err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(currentPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("替换后不应残留 SQLite 侧文件 %s: %v", suffix, err)
		}
	}
	got, err := Open(context.Background(), currentPath, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer got.Close()
	users, err := got.ListUsers(context.Background())
	if err != nil || len(users) != 1 || users[0].ID != 2 {
		t.Fatalf("业务数据应来自候选库: users=%+v err=%v", users, err)
	}
	value, ok, err := got.GetSetting(context.Background(), "access_key_hash")
	if err != nil || !ok || value != `"current"` {
		t.Fatalf("安全设置应保留当前库值: %q %v %v", value, ok, err)
	}
	value, ok, err = got.GetSetting(context.Background(), "max_file_size")
	if err != nil || !ok || value != "123" {
		t.Fatalf("运行设置应保留当前库值: %q %v %v", value, ok, err)
	}
	if _, err := got.GetWebSession(context.Background(), "old"); err != ErrNotFound {
		t.Fatalf("Web 会话应清空: %v", err)
	}
	if _, err := os.Stat(PendingImportMarkerPath(dataDir)); !os.IsNotExist(err) {
		t.Fatalf("成功后 marker 应清理: %v", err)
	}
}

func TestValidateBackupRejectsMissingCloudDownloadColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-v11.db")
	s, err := Open(context.Background(), path, testLogger())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(context.Background(), "ALTER TABLE users DROP COLUMN cloud_download"); err != nil {
		t.Fatalf("构造缺列备份失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	err = ValidateBackup(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "users.cloud_download") {
		t.Fatalf("v11 备份缺少 cloud_download 应被拒绝，得到 %v", err)
	}
}
