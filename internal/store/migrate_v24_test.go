package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// TestMigrateV24UpgradesLegacyV23 构造停在 user_version=23 的旧库并写入
// dump_entries 与 channel_bindings 行，升级后新列应取默认值（存量
// dump_channel_id=0 永不命中、绑定 status=active），既有行保留。
func TestMigrateV24UpgradesLegacyV23(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("打开原始库失败: %v", err)
	}
	for v, script := range migrations[:23] {
		if _, err := raw.Exec(script); err != nil {
			t.Fatalf("执行旧迁移 v%d 失败: %v", v+1, err)
		}
		if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			t.Fatalf("写旧版本号失败: %v", err)
		}
	}
	if _, err := raw.Exec(`INSERT INTO dump_entries
		(channel_key, message_id, dump_ids_json, format_version, created_at)
		VALUES ('legacychan', 5, '[11,12]', 1, 1789900000000)`); err != nil {
		t.Fatalf("灌入旧缓存条目失败: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO users (id, status, is_owner, created_at)
		VALUES (42, 'enabled', 0, 1789900000000)`); err != nil {
		t.Fatalf("灌入旧用户失败: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO channel_bindings
		(channel_id, user_id, title, bound_via, bot_id, created_at, updated_at)
		VALUES (-100123, 42, '旧频道', 'bot', 7, 1789900000000, 1789900000000)`); err != nil {
		t.Fatalf("灌入旧绑定失败: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), path, testLogger())
	if err != nil {
		t.Fatalf("旧 v23 库应经 v24 顺利迁移: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	// 存量缓存条目：dump_channel_id=0，任何现行频道都不命中
	if _, err := s.LatestDumpEntry(ctx, "legacychan", 5, -100999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("存量条目（频道 0）不应命中现行频道: %v", err)
	}
	if n, err := s.CountDumpEntriesByChannel(ctx, 0); err != nil || n != 1 {
		t.Fatalf("按频道 0 统计存量应为 1: n=%d err=%v", n, err)
	}

	// 新写入带频道归属；同链接不同频道互不命中
	if _, err := s.InsertDumpEntry(ctx, DumpEntry{ChannelKey: "legacychan",
		MessageID: 5, DumpIDs: []int{21}, DumpChannelID: -100999}); err != nil {
		t.Fatalf("写入新频道条目失败: %v", err)
	}
	got, err := s.LatestDumpEntry(ctx, "legacychan", 5, -100999)
	if err != nil || len(got.DumpIDs) != 1 || got.DumpIDs[0] != 21 {
		t.Fatalf("新频道条目应命中: %+v err=%v", got, err)
	}
	if _, err := s.LatestDumpEntry(ctx, "legacychan", 5, -100888); !errors.Is(err, ErrNotFound) {
		t.Fatalf("他频道查询不应命中: %v", err)
	}

	// 既有绑定迁移后默认 active
	b, err := s.GetChannelBinding(ctx, -100123)
	if err != nil || b.Status != BindingStatusActive || b.UnboundAt != 0 {
		t.Fatalf("旧绑定应保留且为 active: %+v err=%v", b, err)
	}
}

// TestChannelBindingSoftUnbind 覆盖软解绑状态机：解绑留痕（幂等，二次
// changed=false）、ByUser 只回 active、同用户重绑复活、他用户接管放行、
// active 冲突仍拒绝、物理删除兜底。
func TestChannelBindingSoftUnbind(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 42)
	mustUser(t, s, 43)

	mk := func(uid int64) ChannelBinding {
		return ChannelBinding{ChannelID: -100200, UserID: uid, Title: "频道",
			BoundVia: BoundViaBot, BotID: 7}
	}
	if _, err := s.UpsertChannelBinding(ctx, mk(42)); err != nil {
		t.Fatalf("建立绑定失败: %v", err)
	}

	// active 冲突拒绝；软解绑后接管放行
	if _, err := s.UpsertChannelBinding(ctx, mk(43)); err == nil {
		t.Fatalf("active 绑定被他用户绑定应拒绝")
	}
	got, changed, err := s.MarkChannelBindingUnbound(ctx, -100200, 0, UnbindReasonChannelGone)
	if err != nil || !changed {
		t.Fatalf("软解绑应成功且 changed=true: %+v changed=%v err=%v", got, changed, err)
	}
	if got.Status != BindingStatusUnbound || got.UnbindReason != UnbindReasonChannelGone ||
		got.UnboundAt == 0 {
		t.Fatalf("解绑痕迹应落库: %+v", got)
	}
	if _, changed, err := s.MarkChannelBindingUnbound(ctx, -100200, 0, UnbindReasonManual); err != nil || changed {
		t.Fatalf("重复解绑应幂等 changed=false: changed=%v err=%v", changed, err)
	}

	// ByUser 只回 active
	rows, err := s.ListChannelBindingsByUser(ctx, 42)
	if err != nil || len(rows) != 0 {
		t.Fatalf("解绑后 ByUser 应为空: n=%d err=%v", len(rows), err)
	}
	// 管理端全量列表仍可见
	all, err := s.ListChannelBindingsWithUser(ctx)
	if err != nil || len(all) != 1 || all[0].Status != BindingStatusUnbound {
		t.Fatalf("管理端列表应含解绑行: n=%d err=%v", len(all), err)
	}

	// 他用户接管：复活且归属让渡
	taken, err := s.UpsertChannelBinding(ctx, mk(43))
	if err != nil || taken.Status != BindingStatusActive || taken.UserID != 43 {
		t.Fatalf("unbound 行应允许接管并复活: %+v err=%v", taken, err)
	}
	b, err := s.GetChannelBinding(ctx, -100200)
	if err != nil || b.UnbindReason != "" || b.UnboundAt != 0 {
		t.Fatalf("复活应清理解绑痕迹: %+v err=%v", b, err)
	}

	// 同用户重绑复活（先解绑再绑回）
	if _, _, err := s.MarkChannelBindingUnbound(ctx, -100200, 0, UnbindReasonManual); err != nil {
		t.Fatalf("再次解绑失败: %v", err)
	}
	revived, err := s.UpsertChannelBinding(ctx, mk(43))
	if err != nil || revived.Status != BindingStatusActive {
		t.Fatalf("同用户重绑应复活: %+v err=%v", revived, err)
	}

	// Bot 侧归属限定：解绑他人的绑定返回 ErrNotFound
	if _, _, err := s.MarkChannelBindingUnbound(ctx, -100200, 42, UnbindReasonManual); !errors.Is(err, ErrNotFound) {
		t.Fatalf("限定归属解绑他人绑定应 ErrNotFound: %v", err)
	}

	// 物理删除兜底
	if _, err := s.DeleteChannelBinding(ctx, -100200, 0); err != nil {
		t.Fatalf("物理删除失败: %v", err)
	}
	if _, err := s.GetChannelBinding(ctx, -100200); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应 ErrNotFound: %v", err)
	}
}

// TestDumpEntryChannelFilter 覆盖迁移工具的列表/回写/删除回环。
func TestDumpEntryChannelFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	e1, err := s.InsertDumpEntry(ctx, DumpEntry{ChannelKey: "chan", MessageID: 1,
		DumpIDs: []int{10}, DumpChannelID: -100111})
	if err != nil {
		t.Fatalf("写入条目 1 失败: %v", err)
	}
	if _, err := s.InsertDumpEntry(ctx, DumpEntry{ChannelKey: "chan", MessageID: 2,
		DumpIDs: []int{20, 21}, DumpChannelID: -100111}); err != nil {
		t.Fatalf("写入条目 2 失败: %v", err)
	}

	// 游标分页：从 0 起按 id 升序
	page, err := s.ListDumpEntriesByChannel(ctx, -100111, 0, 1)
	if err != nil || len(page) != 1 || page[0].ID != e1.ID {
		t.Fatalf("分页应按 id 升序: n=%d err=%v", len(page), err)
	}
	next, err := s.ListDumpEntriesByChannel(ctx, -100111, page[0].ID, 10)
	if err != nil || len(next) != 1 {
		t.Fatalf("游标翻页应取剩余行: n=%d err=%v", len(next), err)
	}

	// 回写新频道与新消息 ID 后按新频道命中
	if err := s.UpdateDumpEntryCopy(ctx, e1.ID, -100222, []int{77}); err != nil {
		t.Fatalf("迁移回写失败: %v", err)
	}
	got, err := s.LatestDumpEntry(ctx, "chan", 1, -100222)
	if err != nil || len(got.DumpIDs) != 1 || got.DumpIDs[0] != 77 {
		t.Fatalf("回写后应按新频道命中: %+v err=%v", got, err)
	}
	if _, err := s.LatestDumpEntry(ctx, "chan", 1, -100111); !errors.Is(err, ErrNotFound) {
		t.Fatalf("旧频道不应再命中: %v", err)
	}

	// 物理删除
	if err := s.DeleteDumpEntry(ctx, e1.ID); err != nil {
		t.Fatalf("删除条目失败: %v", err)
	}
	if n, err := s.CountDumpEntriesByChannel(ctx, -100222); err != nil || n != 0 {
		t.Fatalf("删除后统计应为 0: n=%d err=%v", n, err)
	}
}
