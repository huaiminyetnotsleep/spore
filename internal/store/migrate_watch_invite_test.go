package store

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMigrateWatchInviteRequestsV19 校验 v19 表结构：列齐全、invite_hash 可空
// （终态清理完整 hash）、enabled NOT NULL DEFAULT 1、user_id 无数据库外键
// （管理员路径 user_id=0，与 watch_sources.added_by 同约定）。
func TestMigrateWatchInviteRequestsV19(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watch-invites.db")
	s, err := Open(context.Background(), path, testLogger())
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("读取数据库版本失败: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("数据库版本应为 %d，得到 %d", len(migrations), version)
	}

	columns := map[string]struct {
		typ     string
		notNull int
	}{
		"id": {}, "user_id": {}, "invite_hash": {}, "masked_hash": {}, "status": {},
		"channel_id": {}, "kind": {}, "username": {}, "title": {},
		"participants": {}, "enabled": {}, "reviewed_by": {}, "note": {},
		"bot_id": {}, "bot_username": {}, "requested_at": {}, "updated_at": {},
	}
	rows, err := s.db.Query("PRAGMA table_info('watch_invite_requests')")
	if err != nil {
		t.Fatalf("读取表结构失败: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("扫描表结构失败: %v", err)
		}
		if _, ok := columns[name]; ok {
			columns[name] = struct {
				typ     string
				notNull int
			}{typ: typ, notNull: notNull}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历表结构失败: %v", err)
	}
	for name, col := range columns {
		if col.typ == "" {
			t.Fatalf("缺少列 %s", name)
		}
	}
	if columns["invite_hash"].notNull != 0 {
		t.Fatal("invite_hash 必须可空，供终态后清理完整 hash")
	}

	var notNull int
	var dflt string
	if err := s.db.QueryRow(
		`SELECT "notnull", COALESCE(dflt_value, '') FROM pragma_table_info('watch_invite_requests') WHERE name = 'enabled'`,
	).Scan(&notNull, &dflt); err != nil {
		t.Fatalf("enabled 列应存在: %v", err)
	}
	if notNull != 1 || dflt != "1" {
		t.Fatalf("enabled 应为 NOT NULL DEFAULT 1: notnull=%d default=%q", notNull, dflt)
	}
	if err := s.db.QueryRow(
		`SELECT "notnull", COALESCE(dflt_value, '') FROM pragma_table_info('watch_invite_requests') WHERE name = 'user_id'`,
	).Scan(&notNull, &dflt); err != nil {
		t.Fatalf("user_id 列应存在: %v", err)
	}
	if notNull != 1 || dflt != "0" {
		t.Fatalf("user_id 应为 NOT NULL DEFAULT 0（管理员路径）: notnull=%d default=%q", notNull, dflt)
	}

	var fkCount int
	if err := s.db.QueryRow(
		"SELECT COUNT(*) FROM pragma_foreign_key_list('watch_invite_requests')",
	).Scan(&fkCount); err != nil {
		t.Fatalf("读取外键失败: %v", err)
	}
	if fkCount != 0 {
		t.Fatalf("watch_invite_requests 不应有数据库外键: %d", fkCount)
	}

	for _, name := range []string{"idx_watch_invite_requests_active", "idx_watch_invite_requests_user_active", "idx_watch_invite_requests_hash_active"} {
		var got string
		if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&got); err != nil {
			t.Fatalf("索引 %s 应存在: %v", name, err)
		}
	}
}
