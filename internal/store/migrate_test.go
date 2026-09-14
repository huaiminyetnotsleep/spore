package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func TestMigrateIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	ctx := context.Background()

	s1, err := Open(ctx, path, testLogger())
	if err != nil {
		t.Fatalf("首次打开失败: %v", err)
	}
	var version int
	if err := s1.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("读取 user_version 失败: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("迁移后 user_version 应为 %d，得到 %d", len(migrations), version)
	}
	var auditIndex string
	if err := s1.db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_audit_at_id'").Scan(&auditIndex); err != nil {
		t.Fatalf("审计时间索引应存在: %v", err)
	}
	// WAL 模式应已生效并持久化在数据库文件里
	var mode string
	if err := s1.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("读取 journal_mode 失败: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode 应为 wal，得到 %s", mode)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	// 二次启动：不重复执行迁移，版本保持不变
	s2, err := Open(ctx, path, testLogger())
	if err != nil {
		t.Fatalf("二次打开失败: %v", err)
	}
	defer s2.Close()
	var again int
	if err := s2.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&again); err != nil {
		t.Fatalf("二次读取 user_version 失败: %v", err)
	}
	if again != version {
		t.Fatalf("重复启动不应改变 user_version：首次 %d，二次 %d", version, again)
	}
}

func TestMigrateRejectsNewerDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	ctx := context.Background()

	s, err := Open(ctx, path, testLogger())
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	// 模拟"未来版本的程序创建过数据库"：把版本号抬高后重开
	if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 99"); err != nil {
		t.Fatalf("抬高版本失败: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("关闭失败: %v", err)
	}

	_, err = Open(ctx, path, testLogger())
	if err == nil {
		t.Fatal("数据库版本高于程序支持时应拒绝打开")
	}
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != apperr.CodeStoreMigration {
		t.Fatalf("应归类为 STORE_MIGRATION_FAILED，得到 %v", err)
	}
}

// v10：cloud_uploads 表、请求索引与 requests.parent_request_id/cloud_destination 列。
func TestMigrateCloudUploads(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	var idx string
	if err := s.db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_cloud_uploads_request'").Scan(&idx); err != nil {
		t.Fatalf("cloud_uploads 请求索引应存在: %v", err)
	}
	// parent_request_id 列可写可读（NULL 归一为 0）
	res, err := s.db.ExecContext(ctx,
		"UPDATE requests SET parent_request_id = 42 WHERE id = 999")
	if err != nil {
		t.Fatalf("parent_request_id 列应可用: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 0 {
		t.Fatal("不存在的行不应被更新")
	}
	// cloud_destination 列存在且 NOT NULL 带默认空串
	var def, notNull string
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(dflt_value, ''), \"notnull\" FROM pragma_table_info('requests') WHERE name = 'cloud_destination'").
		Scan(&def, &notNull); err != nil {
		t.Fatalf("cloud_destination 列应存在: %v", err)
	}
	if def != "''" || notNull != "1" {
		t.Fatalf("cloud_destination 应为 NOT NULL DEFAULT ''，得到 default=%s notnull=%s", def, notNull)
	}
	// cloud_uploads 时间列为整数（Unix 毫秒约定）
	var typ string
	if err := s.db.QueryRowContext(ctx,
		"SELECT type FROM pragma_table_info('cloud_uploads') WHERE name = 'created_at'").Scan(&typ); err != nil {
		t.Fatalf("读取 cloud_uploads 列类型失败: %v", err)
	}
	if typ != "INTEGER" {
		t.Fatalf("created_at 应为 INTEGER（Unix 毫秒约定），得到 %s", typ)
	}
}
