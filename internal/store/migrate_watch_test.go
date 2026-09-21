package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// TestMigrateV18UpgradesLegacyV17 复现线上事故（2026-09-21）：部署过
// v17 首版形态的库（无 kind / bot 列、无 watch_events），user_version 已
// 是 17，v17 不会重跑——新代码查询 w.kind 报 no such column。v18 以
// 重建表方式把旧 v17 库升级到最终结构并保留既有行。
func TestMigrateV18UpgradesLegacyV17(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	// 真实路径构造旧库：依次执行 v1–v17（v17 当前即首次发布的原始形态）
	// 并写入一行，停在 user_version=17——等价于部署过 v17 首版构建的库。
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("打开原始库失败: %v", err)
	}
	for v, script := range migrations[:17] {
		if _, err := raw.Exec(script); err != nil {
			t.Fatalf("执行旧迁移 v%d 失败: %v", v+1, err)
		}
		// PRAGMA 不接受绑定参数（同 execMigration 的注释）；版本号来自切片下标
		if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			t.Fatalf("写旧版本号失败: %v", err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO watch_sources
		(channel_id, username, title, status, enabled, added_by, created_at, updated_at)
		VALUES (-1001234, 'mychan', '旧源', 'approved', 1, 7, 1789900000000, 1789900000000)`); err != nil {
		t.Fatalf("灌入旧库数据失败: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("旧 v17 库应经 v18 顺利迁移: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// 既有行保留且新列取默认值
	got, err := s.GetWatchSource(context.Background(), -1001234)
	if err != nil {
		t.Fatalf("迁移后读取失败: %v", err)
	}
	if got.Username != "mychan" || got.Status != WatchApproved || got.AddedBy != 7 {
		t.Fatalf("旧库既有行应保留: %+v", got)
	}
	if got.Kind != "" || got.BotID != 0 || got.BotUsername != "" {
		t.Fatalf("新增列应取默认值: %+v", got)
	}
	// v18 建好的事件表可直接读写
	if _, err := s.InsertWatchEvent(context.Background(), WatchEvent{
		ChannelID: -1001234, Title: "旧源", MessageID: 11,
		MemberIDs: []int{11}, DumpIDs: []int{101}, BotID: 42,
		BotUsername: "b", Path: WatchPathCopy,
	}); err != nil {
		t.Fatalf("watch_events 应已建好: %v", err)
	}
	events, total, err := s.ListWatchEvents(context.Background(), WatchEventsQuery{Page: 1, PageSize: 10})
	if err != nil || total != 1 || len(events) != 1 {
		t.Fatalf("事件表应可查询: total=%d err=%v", total, err)
	}
	// 联表列表（事故接口 ListWatchSourcesWithUser）不再报 no such column
	if _, err := s.ListWatchSourcesWithUser(context.Background()); err != nil {
		t.Fatalf("事故接口应恢复: %v", err)
	}
	// 幂等：再次打开版本不变、不重复迁移
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(context.Background(), path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("重复打开应幂等: %v", err)
	}
	defer s2.Close()
	var version int
	if err := s2.ex.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("版本应停在最新且不再推进: %d err=%v", version, err)
	}
}

// TestMigrateFreshConvergesSameShape 断言新库（v17+v18）与升级路径收敛到
// 同一结构：fresh 库建行/查事件同样可用。
func TestMigrateFreshConvergesSameShape(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.UpsertWatchSource(ctx, WatchSource{
		ChannelID: -10042, Kind: "supergroup", Username: "chan",
		Status: WatchApproved, Enabled: true, AddedBy: 9, BotID: 5, BotUsername: "b5",
	}); err != nil {
		t.Fatalf("新库写入失败: %v", err)
	}
	got, err := s.GetWatchSource(ctx, -10042)
	if err != nil || got.Kind != "supergroup" || got.BotID != 5 {
		t.Fatalf("新库最终形态应含全部列: %+v err=%v", got, err)
	}
}
