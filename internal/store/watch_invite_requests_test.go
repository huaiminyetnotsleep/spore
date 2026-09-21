package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestWatchInviteRequestLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, User{
		ID: 101, Status: UserEnabled, Username: "alice", DisplayName: "Alice",
	}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	created, err := s.CreateWatchInviteRequest(ctx, WatchInviteRequest{
		UserID: 101, InviteHash: "AbCdEfGh12345678", Participants: 128,
		Enabled: true, BotID: 42, BotUsername: "watch_bot", RequestedAt: 1000,
	})
	if err != nil {
		t.Fatalf("创建监听邀请申请失败: %v", err)
	}
	if created.Status != WatchInvitePending || created.MaskedHash != "AbCd…5678" || !created.Enabled {
		t.Fatalf("初始状态/脱敏 hash/开关不符: %+v", created)
	}
	if created.Participants != 128 {
		t.Fatalf("成员数应回写: %+v", created)
	}
	if created.UserUsername != "alice" || created.UserDisplayName != "Alice" {
		t.Fatalf("API 安全视图应包含申请人资料: %+v", created)
	}
	if created.UpdatedAt != created.RequestedAt {
		t.Fatalf("默认更新时间应等于申请时间: %+v", created)
	}

	payload, err := json.Marshal(created)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	text := string(payload)
	if strings.Contains(text, created.InviteHash) || strings.Contains(text, "invite_hash") {
		t.Fatalf("完整 invite hash 不得进入 JSON: %s", text)
	}
	for _, key := range []string{
		`"masked_hash":"AbCd…5678"`, `"channel_title":`, `"created_at":1000`,
		`"participants":128`, `"enabled":true`, `"reviewed_by":`, `"note":`,
	} {
		if !strings.Contains(text, key) {
			t.Fatalf("JSON 应包含前端契约字段 %s: %s", key, text)
		}
	}

	found, err := s.FindActiveWatchInviteRequestByHash(ctx, created.InviteHash)
	if err != nil || found.ID != created.ID {
		t.Fatalf("活动 hash 查重失败: %+v err=%v", found, err)
	}

	waiting, err := s.UpdateWatchInviteRequestStatus(ctx, created.ID, WatchInviteWaitingTelegram, "", 1100)
	if err != nil || waiting.Status != WatchInviteWaitingTelegram || waiting.UpdatedAt != 1100 {
		t.Fatalf("更新 Telegram 等待状态失败: %+v err=%v", waiting, err)
	}
	waiting, err = s.UpdateWatchInviteRequestStatus(ctx, created.ID, WatchInviteWaitingBot, "等待受理 bot 执行", 1200)
	if err != nil || waiting.Status != WatchInviteWaitingBot || waiting.Note != "等待受理 bot 执行" {
		t.Fatalf("更新 Bot 等待状态与备注失败: %+v err=%v", waiting, err)
	}

	reviewed, err := s.SetWatchInviteRequestReview(ctx, created.ID, "admin-hash", 1250)
	if err != nil || reviewed.ReviewedBy != "admin-hash" || reviewed.Status != WatchInviteWaitingBot || reviewed.UpdatedAt != 1250 {
		t.Fatalf("回写审批人应保持状态不变: %+v err=%v", reviewed, err)
	}

	withChannel, err := s.UpdateWatchInviteRequestChannel(ctx, created.ID, -1001234,
		"supergroup", "private_source", "私有监听源", 1300)
	if err != nil {
		t.Fatalf("更新频道信息失败: %v", err)
	}
	if withChannel.ChannelID != -1001234 || withChannel.Kind != "supergroup" ||
		withChannel.Username != "private_source" || withChannel.Title != "私有监听源" {
		t.Fatalf("频道信息未完整回写: %+v", withChannel)
	}

	approved, err := s.UpdateWatchInviteRequestStatus(ctx, created.ID, WatchInviteApproved, "", 1400)
	if err != nil || approved.Status != WatchInviteApproved || approved.Note != "" {
		t.Fatalf("更新终态应清除备注: %+v err=%v", approved, err)
	}
	if _, err := s.FindActiveWatchInviteRequestByHash(ctx, created.InviteHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("终态申请不应参与活动查重: %v", err)
	}

	cleared, err := s.ClearWatchInviteRequestHash(ctx, created.ID, 1500)
	if err != nil {
		t.Fatalf("清理完整 hash 失败: %v", err)
	}
	if cleared.InviteHash != "" || cleared.MaskedHash != "AbCd…5678" || cleared.UpdatedAt != 1500 {
		t.Fatalf("清理后应只保留脱敏 hash: %+v", cleared)
	}

	if err := s.DeleteWatchInviteRequest(ctx, created.ID); err != nil {
		t.Fatalf("删除申请失败: %v", err)
	}
	if _, err := s.GetWatchInviteRequest(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应返回 ErrNotFound: %v", err)
	}
	if err := s.DeleteWatchInviteRequest(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复删除应返回 ErrNotFound: %v", err)
	}
}

func TestWatchInviteRequestAdminPathDisabled(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 管理员路径：user_id=0 合法（无外键），可显式传 enabled=false 预录入停用态。
	created, err := s.CreateWatchInviteRequest(ctx, WatchInviteRequest{
		InviteHash: "AbCdEfGh12345678", Title: "管理员直达", Enabled: false, RequestedAt: 100,
	})
	if err != nil {
		t.Fatalf("管理员路径应可创建: %v", err)
	}
	if created.UserID != 0 || created.Enabled {
		t.Fatalf("管理员行应保留 user_id=0 与显式停用开关: %+v", created)
	}
	if created.UserUsername != "" || created.UserDisplayName != "" {
		t.Fatalf("管理员行不应联出用户资料: %+v", created)
	}
	got, err := s.GetWatchInviteRequest(ctx, created.ID)
	if err != nil || got.Enabled || got.UserID != 0 {
		t.Fatalf("回读应保持停用态与管理员归属: %+v err=%v", got, err)
	}
	// 用户路径显式传 true 时开关保持放开（无外键，用户行可尚不存在）
	enabledRow, err := s.CreateWatchInviteRequest(ctx, WatchInviteRequest{
		UserID: 101, InviteHash: "hashbbb111222333", Enabled: true, RequestedAt: 200,
	})
	if err != nil {
		t.Fatalf("用户路径创建失败: %v", err)
	}
	if !enabledRow.Enabled {
		t.Fatalf("用户路径显式 true 应落库: %+v", enabledRow)
	}
}

func TestWatchInviteRequestActiveListAndCounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 101)
	mustUser(t, s, 202)

	seed := []WatchInviteRequest{
		{UserID: 101, InviteHash: "hashaaa111222333", Status: WatchInvitePending, RequestedAt: 100},
		{UserID: 101, InviteHash: "hashbbb111222333", Status: WatchInviteWaitingTelegram, RequestedAt: 200},
		{UserID: 202, InviteHash: "hashccc111222333", Status: WatchInviteWaitingBot, RequestedAt: 300},
		{UserID: 202, InviteHash: "hashddd111222333", Status: WatchInviteApproved, RequestedAt: 400},
		{UserID: 202, InviteHash: "hasheee111222333", Status: WatchInviteRejected, RequestedAt: 500},
		{UserID: 202, InviteHash: "hashfff111222333", Status: WatchInviteFailed, RequestedAt: 600},
	}
	for _, in := range seed {
		if _, err := s.CreateWatchInviteRequest(ctx, in); err != nil {
			t.Fatalf("预置申请失败: %v", err)
		}
	}

	rows, err := s.ListActiveWatchInviteRequests(ctx)
	if err != nil {
		t.Fatalf("列出活动申请失败: %v", err)
	}
	if len(rows) != 3 || rows[0].Status != WatchInvitePending ||
		rows[1].Status != WatchInviteWaitingTelegram || rows[2].Status != WatchInviteWaitingBot {
		t.Fatalf("活动列表状态或排序不符: %+v", rows)
	}
	if n, err := s.CountActiveWatchInviteRequests(ctx); err != nil || n != 3 {
		t.Fatalf("全局活动数应为 3: n=%d err=%v", n, err)
	}
	if n, err := s.CountActiveWatchInviteRequestsByUser(ctx, 101); err != nil || n != 2 {
		t.Fatalf("用户 101 活动数应为 2: n=%d err=%v", n, err)
	}
	if n, err := s.CountActiveWatchInviteRequestsByUser(ctx, 202); err != nil || n != 1 {
		t.Fatalf("用户 202 活动数应为 1: n=%d err=%v", n, err)
	}
	if _, err := s.CountActiveWatchInviteRequestsByUser(ctx, 0); err == nil {
		t.Fatal("非正用户 ID 应拒绝")
	}

	// 相同 hash 的终态历史行不影响新的活动行查重。
	active, err := s.CreateWatchInviteRequest(ctx, WatchInviteRequest{
		UserID: 101, InviteHash: "hashddd111222333", Status: WatchInvitePending, RequestedAt: 700,
	})
	if err != nil {
		t.Fatalf("创建同 hash 活动申请失败: %v", err)
	}
	found, err := s.FindActiveWatchInviteRequestByHash(ctx, "hashddd111222333")
	if err != nil || found.ID != active.ID {
		t.Fatalf("应命中新活动申请: %+v err=%v", found, err)
	}
}

func TestWatchInviteRequestWebAndUserLists(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 101)
	mustUser(t, s, 202)

	seed := []WatchInviteRequest{
		{UserID: 101, InviteHash: "hashaaa111222333", Status: WatchInvitePending, RequestedAt: 100},
		{UserID: 101, InviteHash: "hashbbb111222333", Status: WatchInviteApproved, RequestedAt: 200},
		{UserID: 202, InviteHash: "hashccc111222333", Status: WatchInviteWaitingBot, RequestedAt: 300},
		{UserID: 202, InviteHash: "hashddd111222333", Status: WatchInvitePending, RequestedAt: 400},
		{UserID: 202, InviteHash: "hasheee111222333", Status: WatchInviteRejected, RequestedAt: 500},
	}
	ids := make([]int64, 0, len(seed))
	for _, in := range seed {
		created, err := s.CreateWatchInviteRequest(ctx, in)
		if err != nil {
			t.Fatalf("预置申请失败: %v", err)
		}
		ids = append(ids, created.ID)
	}

	all, err := s.ListWatchInviteRequests(ctx)
	if err != nil {
		t.Fatalf("管理列表失败: %v", err)
	}
	if len(all) != len(seed) {
		t.Fatalf("管理列表应含全部状态 %d 行: %d", len(seed), len(all))
	}
	// 待审批在前（组内时间倒序），其余按申请时间倒序
	wantOrder := []int64{ids[3], ids[0], ids[4], ids[2], ids[1]}
	for i, want := range wantOrder {
		if all[i].ID != want {
			t.Fatalf("管理列表排序不符: 位置 %d 应为 %d，得到 %d", i, want, all[i].ID)
		}
	}

	byUser, err := s.ListWatchInviteRequestsByUser(ctx, 202)
	if err != nil || len(byUser) != 3 {
		t.Fatalf("用户列表应含 3 行: %+v err=%v", byUser, err)
	}
	if byUser[0].ID != ids[4] || byUser[1].ID != ids[3] || byUser[2].ID != ids[2] {
		t.Fatalf("用户列表应按申请时间倒序: %+v", byUser)
	}
	for _, bad := range []int64{0, -1} {
		if _, err := s.ListWatchInviteRequestsByUser(ctx, bad); err == nil {
			t.Fatalf("用户 ID %d 应拒绝", bad)
		}
	}
}

func TestWatchInviteRequestValidationAndNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 101)

	if _, err := s.CreateWatchInviteRequest(ctx, WatchInviteRequest{UserID: 101}); err == nil {
		t.Fatal("空 hash 应拒绝")
	}
	if _, err := s.CreateWatchInviteRequest(ctx, WatchInviteRequest{
		UserID: -1, InviteHash: "hashaaa111222333",
	}); err == nil {
		t.Fatal("负数用户 ID 应拒绝")
	}
	if _, err := s.CreateWatchInviteRequest(ctx, WatchInviteRequest{
		UserID: 101, InviteHash: "hashaaa111222333", Status: "bogus",
	}); err == nil {
		t.Fatal("非法状态应拒绝")
	}
	if _, err := s.UpdateWatchInviteRequestStatus(ctx, 999, "bogus", "", 0); err == nil {
		t.Fatal("非法更新状态应拒绝")
	}
	if _, err := s.UpdateWatchInviteRequestStatus(ctx, 999, WatchInviteFailed, "", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在申请应 ErrNotFound: %v", err)
	}
	if _, err := s.SetWatchInviteRequestReview(ctx, 999, "admin", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("回写审批人不存在应 ErrNotFound: %v", err)
	}
	if _, err := s.UpdateWatchInviteRequestChannel(ctx, 999, 0, "", "", "", 0); err == nil {
		t.Fatal("空频道 ID 应拒绝")
	}
	if _, err := s.ClearWatchInviteRequestHash(ctx, 999, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("清理不存在申请应 ErrNotFound: %v", err)
	}
	if _, err := s.FindActiveWatchInviteRequestByHash(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("查重未命中应 ErrNotFound: %v", err)
	}
}
