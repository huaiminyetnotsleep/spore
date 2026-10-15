package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// buildLegacyDB 依次执行 v1..v[n] 构造停在版本 n 的旧库（真实升级路径）。
func buildLegacyDB(t *testing.T, path string, n int) {
	t.Helper()
	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("打开原始库失败: %v", err)
	}
	for v, script := range migrations[:n] {
		if _, err := raw.Exec(script); err != nil {
			t.Fatalf("执行旧迁移 v%d 失败: %v", v+1, err)
		}
		// PRAGMA 不接受绑定参数（同 execMigration 的注释）；版本号来自切片下标
		if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			t.Fatalf("写旧版本号失败: %v", err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
}

// mustBinding 写入一条归属 userID 的有效频道绑定（外键约束要求
// forward_binding_channel_id 指向真实绑定行）。
func mustBinding(t *testing.T, s *Store, channelID, userID int64) {
	t.Helper()
	if _, err := s.UpsertChannelBinding(context.Background(), ChannelBinding{
		ChannelID: channelID, UserID: userID, BoundVia: BoundViaBot,
	}); err != nil {
		t.Fatalf("写入绑定 %d 失败: %v", channelID, err)
	}
}

// TestMigrateV28ForwardPolicyBackfill 验证 v28 迁移的回填语义：旧管理员源
// （added_by=0）保持全局镜像，旧用户源默认收紧为不镜像；既有行的新列均
// 为空引用；channel_bindings 新增 kind 列且旧行为空串；重复打开幂等。
func TestMigrateV28ForwardPolicyBackfill(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy27.db")
	buildLegacyDB(t, path, 27)

	raw, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	for _, row := range []struct {
		id      int64
		addedBy int64
	}{
		{-1002001, 0},  // 管理员源
		{-1002002, 77}, // 用户源
	} {
		if _, err := raw.Exec(`INSERT INTO watch_sources
			(channel_id, kind, username, title, status, enabled, added_by, bot_id, bot_username, created_at, updated_at)
			VALUES (?, 'channel', '', ?, 'approved', 1, ?, 0, '', 1789900000000, 1789900000000)`,
			row.id, "源", row.addedBy); err != nil {
			t.Fatalf("灌入旧库数据失败: %v", err)
		}
	}
	// 旧绑定行：v27 schema 无 kind 列，迁移后应为空串快照（待重绑/刷新回填）
	if _, err := raw.Exec(`INSERT INTO users (id, status, created_at)
		VALUES (77, 'enabled', 1789900000000)`); err != nil {
		t.Fatalf("灌入旧用户失败: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO channel_bindings
		(channel_id, user_id, title, bound_via, created_at, updated_at)
		VALUES (-1002100, 77, '旧绑定', 'bot', 1789900000000, 1789900000000)`); err != nil {
		t.Fatalf("灌入旧绑定失败: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(context.Background(), path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("旧 v27 库应经 v28 顺利迁移: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	adminSrc, err := s.GetWatchSource(ctx, -1002001)
	if err != nil {
		t.Fatalf("读取管理员源失败: %v", err)
	}
	if !adminSrc.GlobalMirrorEnabled || adminSrc.ForwardBindingChannelID != 0 {
		t.Fatalf("管理员源应保持全局镜像且无专属目标: %+v", adminSrc)
	}
	userSrc, err := s.GetWatchSource(ctx, -1002002)
	if err != nil {
		t.Fatalf("读取用户源失败: %v", err)
	}
	if userSrc.GlobalMirrorEnabled || userSrc.ForwardBindingChannelID != 0 {
		t.Fatalf("用户源默认关闭全局镜像且无专属目标: %+v", userSrc)
	}
	legacyBinding, err := s.GetChannelBinding(ctx, -1002100)
	if err != nil {
		t.Fatalf("读取旧绑定失败: %v", err)
	}
	if legacyBinding.Kind != "" {
		t.Fatalf("旧绑定迁移后 kind 应为空串快照: %+v", legacyBinding)
	}

	// 幂等：再次打开版本停在最新
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(context.Background(), path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("重复打开应幂等: %v", err)
	}
	defer s2.Close()
	var version int
	if err := s2.ex.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("版本应停在最新且不再推进: %d err=%v", version, err)
	}
}

// TestUpsertWatchSourcePolicyDefaultsAndPreservation 验证插入默认值推导与
// 冲突更新不重置策略字段（重复 /watch 不得覆盖用户目标或管理员开关）。
func TestUpsertWatchSourcePolicyDefaultsAndPreservation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	admin, err := s.UpsertWatchSource(ctx, WatchSource{
		ChannelID: -1003001, Kind: "channel", Status: WatchApproved, Enabled: true,
	})
	if err != nil {
		t.Fatalf("写入管理员源失败: %v", err)
	}
	if !admin.GlobalMirrorEnabled {
		t.Fatalf("新管理员源应默认开启全局镜像: %+v", admin)
	}
	user, err := s.UpsertWatchSource(ctx, WatchSource{
		ChannelID: -1003002, Kind: "channel", Status: WatchPending, AddedBy: 88, BotID: 42,
	})
	if err != nil {
		t.Fatalf("写入用户源失败: %v", err)
	}
	if user.GlobalMirrorEnabled {
		t.Fatalf("新用户源应默认关闭全局镜像: %+v", user)
	}

	// 设置策略字段后重复提交同一源（幂等刷新路径）
	mustUser(t, s, 88)
	mustBinding(t, s, -1003999, 88)
	if _, err := s.SetWatchSourceForwardBinding(ctx, -1003002, -1003999); err != nil {
		t.Fatalf("设置专属目标失败: %v", err)
	}
	if _, err := s.SetWatchSourceGlobalMirror(ctx, -1003002, true); err != nil {
		t.Fatalf("开启全局镜像失败: %v", err)
	}
	again, err := s.UpsertWatchSource(ctx, WatchSource{
		ChannelID: -1003002, Kind: "channel", Status: WatchApproved, Enabled: true,
		AddedBy: 88, BotID: 42, Title: "刷新标题",
	})
	if err != nil {
		t.Fatalf("重复提交应幂等: %v", err)
	}
	if again.ForwardBindingChannelID != -1003999 || !again.GlobalMirrorEnabled {
		t.Fatalf("重复 /watch 不得覆盖策略字段: %+v", again)
	}
}

func TestSetWatchSourceForwardBinding(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.UpsertWatchSource(ctx, WatchSource{
		ChannelID: -1003003, Kind: "channel", Status: WatchApproved, Enabled: true, AddedBy: 88,
	}); err != nil {
		t.Fatalf("写入监听源失败: %v", err)
	}
	mustUser(t, s, 88)
	mustBinding(t, s, -1003998, 88)
	set, err := s.SetWatchSourceForwardBinding(ctx, -1003003, -1003998)
	if err != nil || set.ForwardBindingChannelID != -1003998 {
		t.Fatalf("设置专属目标失败: %+v err=%v", set, err)
	}
	cleared, err := s.SetWatchSourceForwardBinding(ctx, -1003003, 0)
	if err != nil || cleared.ForwardBindingChannelID != 0 {
		t.Fatalf("清空专属目标失败: %+v err=%v", cleared, err)
	}
	if _, err := s.SetWatchSourceForwardBinding(ctx, -1004242, -1003998); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的源应返回 ErrNotFound: %v", err)
	}
}

func TestForwardTargetInUse(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 101)

	inv, err := s.CreateWatchInviteRequest(ctx, WatchInviteRequest{
		UserID: 101, InviteHash: "AbCdEfGh12345678", Enabled: true, BotID: 42,
	})
	if err != nil {
		t.Fatalf("创建邀请申请失败: %v", err)
	}
	if inUse, err := s.ForwardTargetInUse(ctx, -1003990); err != nil || inUse {
		t.Fatalf("未引用的目标不应占用: %v err=%v", inUse, err)
	}
	// 活动申请暂存的目标占用
	if _, err := s.SetWatchInviteRequestForwardBinding(ctx, inv.ID, -1003990); err != nil {
		t.Fatalf("暂存目标失败: %v", err)
	}
	if inUse, err := s.ForwardTargetInUse(ctx, -1003990); err != nil || !inUse {
		t.Fatalf("活动申请暂存的目标应占用: %v err=%v", inUse, err)
	}
	// 终态申请不再占用
	if _, err := s.UpdateWatchInviteRequestStatus(ctx, inv.ID, WatchInviteRejected, "", 2000); err != nil {
		t.Fatalf("流转终态失败: %v", err)
	}
	if inUse, err := s.ForwardTargetInUse(ctx, -1003990); err != nil || inUse {
		t.Fatalf("终态申请的目标不应占用: %v err=%v", inUse, err)
	}
	// 监听源引用占用（外键要求绑定行真实存在）
	mustBinding(t, s, -1003991, 101)
	if _, err := s.UpsertWatchSource(ctx, WatchSource{
		ChannelID: -1003004, Kind: "channel", Status: WatchApproved, Enabled: true, AddedBy: 101,
	}); err != nil {
		t.Fatalf("写入监听源失败: %v", err)
	}
	if _, err := s.SetWatchSourceForwardBinding(ctx, -1003004, -1003991); err != nil {
		t.Fatalf("设置专属目标失败: %v", err)
	}
	if inUse, err := s.ForwardTargetInUse(ctx, -1003991); err != nil || !inUse {
		t.Fatalf("源引用的目标应占用: %v err=%v", inUse, err)
	}
}

// TestDeleteChannelBindingClearsForwardReferences 物理删除绑定必须同事务清空
// 监听源与活动邀请申请的引用；软解绑保留引用（投递时复查状态）。
func TestDeleteChannelBindingClearsForwardReferences(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 101)
	const target = int64(-1003992)

	if _, err := s.UpsertChannelBinding(ctx, ChannelBinding{
		ChannelID: target, UserID: 101, BoundVia: BoundViaBot,
	}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	if _, err := s.UpsertWatchSource(ctx, WatchSource{
		ChannelID: -1003005, Kind: "channel", Status: WatchApproved, Enabled: true, AddedBy: 101,
	}); err != nil {
		t.Fatalf("写入监听源失败: %v", err)
	}
	if _, err := s.SetWatchSourceForwardBinding(ctx, -1003005, target); err != nil {
		t.Fatalf("设置专属目标失败: %v", err)
	}
	inv, err := s.CreateWatchInviteRequest(ctx, WatchInviteRequest{
		UserID: 101, InviteHash: "ZzYyXxWw12345678", Enabled: true, BotID: 42,
	})
	if err != nil {
		t.Fatalf("创建邀请申请失败: %v", err)
	}
	if _, err := s.SetWatchInviteRequestForwardBinding(ctx, inv.ID, target); err != nil {
		t.Fatalf("暂存目标失败: %v", err)
	}

	// 软解绑：引用保留（展示仍可指向该绑定，投递时按状态失效）
	if _, _, err := s.MarkChannelBindingUnbound(ctx, target, 0, UnbindReasonManual); err != nil {
		t.Fatalf("软解绑失败: %v", err)
	}
	src, err := s.GetWatchSource(ctx, -1003005)
	if err != nil || src.ForwardBindingChannelID != target {
		t.Fatalf("软解绑应保留专属目标引用: %+v err=%v", src, err)
	}

	// 重新绑定后物理删除：引用清空
	if _, err := s.UpsertChannelBinding(ctx, ChannelBinding{
		ChannelID: target, UserID: 101, BoundVia: BoundViaBot,
	}); err != nil {
		t.Fatalf("重新绑定失败: %v", err)
	}
	if _, err := s.DeleteChannelBinding(ctx, target, 0); err != nil {
		t.Fatalf("物理删除绑定失败: %v", err)
	}
	src, err = s.GetWatchSource(ctx, -1003005)
	if err != nil || src.ForwardBindingChannelID != 0 {
		t.Fatalf("物理删除应清空监听源专属目标引用: %+v err=%v", src, err)
	}
	gotInv, err := s.GetWatchInviteRequest(ctx, inv.ID)
	if err != nil || gotInv.ForwardBindingChannelID != 0 {
		t.Fatalf("物理删除应清空邀请申请暂存目标: %+v err=%v", gotInv, err)
	}
	if inUse, err := s.ForwardTargetInUse(ctx, target); err != nil || inUse {
		t.Fatalf("清空后目标不应再占用: %v err=%v", inUse, err)
	}
}
