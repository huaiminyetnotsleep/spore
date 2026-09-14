package store

import (
	"context"
	"errors"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func TestUserCreateDefaults(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	u, err := s.CreateUser(ctx, User{ID: 100, Username: "alice", DisplayName: "Alice"})
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if u.Status != UserPending {
		t.Errorf("缺省状态应为 pending，得到 %s", u.Status)
	}
	if u.IsOwner {
		t.Error("新用户不应是 owner")
	}
	if u.SubmitIntervalSec != DefaultSubmitIntervalSec || u.DailyLimit != DefaultDailyLimit || u.ConcurrentLimit != DefaultConcurrentLimit {
		t.Errorf("限额应取默认值，得到 %d/%d/%d", u.SubmitIntervalSec, u.DailyLimit, u.ConcurrentLimit)
	}
	if u.CreatedAt == 0 {
		t.Error("CreatedAt 应回填当前时间")
	}

	got, err := s.GetUser(ctx, 100)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if got.Username != "alice" || got.DisplayName != "Alice" || got.Note != "" {
		t.Errorf("可空文本应往返一致，得到 %+v", got)
	}
	if got.FirstUsedAt != 0 || got.ArchivedAt != 0 || got.LastDeniedAt != 0 {
		t.Errorf("未发生的时间字段应为 0，得到 %+v", got)
	}
}

func TestUserStatusTransitions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u, err := s.CreateUser(ctx, User{ID: 1})
	if err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// pending → enabled → disabled → enabled：全流程返回值与落库一致
	for _, next := range []string{UserEnabled, UserDisabled, UserEnabled} {
		got, err := s.UpdateUserStatus(ctx, u.ID, next)
		if err != nil {
			t.Fatalf("流转到 %s 失败: %v", next, err)
		}
		if got.Status != next {
			t.Fatalf("流转返回状态应为 %s，得到 %s", next, got.Status)
		}
		if got.ArchivedAt != 0 {
			t.Fatalf("非 archived 状态 archived_at 应为空，得到 %d", got.ArchivedAt)
		}
	}

	// enabled → archived：写 archived_at
	archived, err := s.UpdateUserStatus(ctx, u.ID, UserArchived)
	if err != nil {
		t.Fatalf("归档失败: %v", err)
	}
	if archived.ArchivedAt == 0 {
		t.Fatal("归档后 archived_at 应写入时间")
	}

	// archived → enabled（restore）：清空 archived_at
	restored, err := s.UpdateUserStatus(ctx, u.ID, UserEnabled)
	if err != nil {
		t.Fatalf("恢复失败: %v", err)
	}
	if restored.ArchivedAt != 0 {
		t.Fatalf("恢复后 archived_at 应清空，得到 %d", restored.ArchivedAt)
	}

	// 非法状态拒绝
	if _, err := s.UpdateUserStatus(ctx, u.ID, "bogus"); err == nil {
		t.Fatal("非法状态应被拒绝")
	}
}

func TestUserNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.GetUser(context.Background(), 404); !errors.Is(err, ErrNotFound) {
		t.Fatalf("应返回 ErrNotFound，得到 %v", err)
	}
	if err := s.UpdateUserNote(context.Background(), 404, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在的用户应返回 ErrNotFound，得到 %v", err)
	}
}

func TestSetOwnerGloballyUnique(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	a := mustUser(t, s, 1)
	b := mustUser(t, s, 2)

	if err := s.SetOwner(ctx, a.ID, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}
	if err := s.SetOwner(ctx, b.ID, true); err != nil {
		t.Fatalf("转移 owner 失败: %v", err)
	}
	users, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatalf("列出用户失败: %v", err)
	}
	owners := 0
	for _, u := range users {
		if u.IsOwner {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("owner 应全局唯一，现有 %d 个", owners)
	}
	got, _ := s.GetUser(ctx, b.ID)
	if !got.IsOwner {
		t.Fatal("owner 应转移到用户 b")
	}

	if err := s.SetOwner(ctx, b.ID, false); err != nil {
		t.Fatalf("取消 owner 失败: %v", err)
	}
	got, _ = s.GetUser(ctx, b.ID)
	if got.IsOwner {
		t.Fatal("取消后不应仍是 owner")
	}
}

func TestOwnerID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 无 owner 时返回 ErrNotFound（事件中心据此判定"通知无法投递"）
	if _, err := s.OwnerID(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("无 owner 应返回 ErrNotFound，得到 %v", err)
	}

	a := mustUser(t, s, 1)
	b := mustUser(t, s, 2)
	if err := s.SetOwner(ctx, a.ID, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}
	id, err := s.OwnerID(ctx)
	if err != nil || id != a.ID {
		t.Fatalf("OwnerID 应为 %d，得到 %d, %v", a.ID, id, err)
	}

	// 转移 owner 后无需重启即生效
	if err := s.SetOwner(ctx, b.ID, true); err != nil {
		t.Fatalf("转移 owner 失败: %v", err)
	}
	if id, _ = s.OwnerID(ctx); id != b.ID {
		t.Fatalf("转移后 OwnerID 应为 %d，得到 %d", b.ID, id)
	}
}

func TestOwnerIDRejectsMultipleOwners(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, User{ID: 1}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if _, err := s.CreateUser(ctx, User{ID: 2}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	// 正常路径由 SetOwner 保证唯一；这里模拟外部改库/历史数据异常，
	// OwnerID 不得使用 LIMIT 1 静默选出任意管理员。
	if _, err := s.ex.ExecContext(ctx, "UPDATE users SET is_owner = 1 WHERE id IN (1, 2)"); err != nil {
		t.Fatalf("构造多个 owner 失败: %v", err)
	}
	if _, err := s.OwnerID(ctx); err == nil {
		t.Fatal("多个 owner 应拒绝返回目标")
	} else {
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeStoreConstraint {
			t.Fatalf("多个 owner 应返回 STORE_CONSTRAINT，得到 %v", err)
		}
	}
}

func TestUpdateUserLimitsAndNote(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	// 零值字段保持原值
	if err := s.UpdateUserLimits(ctx, u.ID, 0, 100, 0); err != nil {
		t.Fatalf("更新限额失败: %v", err)
	}
	got, _ := s.GetUser(ctx, u.ID)
	if got.DailyLimit != 100 {
		t.Errorf("daily_limit 应更新为 100，得到 %d", got.DailyLimit)
	}
	if got.SubmitIntervalSec != DefaultSubmitIntervalSec || got.ConcurrentLimit != DefaultConcurrentLimit {
		t.Errorf("传 0 的限额应保持默认，得到 %d/%d", got.SubmitIntervalSec, got.ConcurrentLimit)
	}

	if err := s.UpdateUserNote(ctx, u.ID, "内部测试账号"); err != nil {
		t.Fatalf("更新备注失败: %v", err)
	}
	got, _ = s.GetUser(ctx, u.ID)
	if got.Note != "内部测试账号" {
		t.Errorf("备注应往返一致，得到 %q", got.Note)
	}
	if err := s.UpdateUserNote(ctx, u.ID, ""); err != nil {
		t.Fatalf("清空备注失败: %v", err)
	}
	got, _ = s.GetUser(ctx, u.ID)
	if got.Note != "" {
		t.Errorf("空串应清空备注，得到 %q", got.Note)
	}
}

func TestTouchUserUsageAndDenied(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	t1, t2 := int64(1000), int64(2000)
	if err := s.TouchUserUsage(ctx, u.ID, t1); err != nil {
		t.Fatalf("首次记录使用时间失败: %v", err)
	}
	if err := s.TouchUserUsage(ctx, u.ID, t2); err != nil {
		t.Fatalf("再次记录使用时间失败: %v", err)
	}
	got, _ := s.GetUser(ctx, u.ID)
	if got.FirstUsedAt != t1 {
		t.Errorf("first_used_at 只写首次，应 %d，得到 %d", t1, got.FirstUsedAt)
	}
	if got.LastUsedAt != t2 {
		t.Errorf("last_used_at 应刷新为 %d，得到 %d", t2, got.LastUsedAt)
	}

	if err := s.MarkUserDenied(ctx, u.ID, "RATE_LIMITED", 3000); err != nil {
		t.Fatalf("记录拒绝失败: %v", err)
	}
	got, _ = s.GetUser(ctx, u.ID)
	if got.LastDeniedAt != 3000 || got.LastDeniedReason != "RATE_LIMITED" {
		t.Errorf("拒绝记录应往返一致，得到 %d/%q", got.LastDeniedAt, got.LastDeniedReason)
	}
}

func TestDeleteUserConstraint(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u := mustUser(t, s, 1)

	// 无关联请求：允许删除
	if err := s.DeleteUser(ctx, u.ID); err != nil {
		t.Fatalf("无请求时应可删除: %v", err)
	}
	if _, err := s.GetUser(ctx, u.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除后应查不到，得到 %v", err)
	}

	// 有关联请求：外键约束阻止硬删除
	u2 := mustUser(t, s, 2)
	if _, err := s.CreateRequest(ctx, Request{UserID: u2.ID, ChannelKey: "example", MessageID: 1}); err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	err := s.DeleteUser(ctx, u2.ID)
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != apperr.CodeStoreConstraint {
		t.Fatalf("有请求记录时应返回 STORE_CONSTRAINT，得到 %v", err)
	}
}

func TestCreateUserDuplicateID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, User{ID: 7}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	_, err := s.CreateUser(ctx, User{ID: 7})
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != apperr.CodeStoreConstraint {
		t.Fatalf("重复 ID 应返回 STORE_CONSTRAINT，得到 %v", err)
	}
}

func TestEffectiveCloudDownloadMatrix(t *testing.T) {
	cases := []struct {
		name  string
		owner bool
		mode  int
		want  bool
	}{
		{"owner默认允许", true, CloudDownloadDefault, true},
		{"owner显式允许", true, CloudDownloadAllow, true},
		{"owner显式拒绝", true, CloudDownloadDeny, false},
		{"普通用户默认拒绝", false, CloudDownloadDefault, false},
		{"普通用户显式允许", false, CloudDownloadAllow, true},
		{"普通用户显式拒绝", false, CloudDownloadDeny, false},
	}
	for _, tc := range cases {
		u := User{IsOwner: tc.owner, CloudDownload: tc.mode}
		if got := u.EffectiveCloudDownload(); got != tc.want {
			t.Errorf("%s: EffectiveCloudDownload() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestUpdateUserCloudDownloadRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := mustUser(t, s, 1)
	if err := s.SetOwner(ctx, owner.ID, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}
	user := mustUser(t, s, 2)

	// 新列默认 0：owner 生效允许、普通用户生效拒绝
	for id, want := range map[int64]bool{owner.ID: true, user.ID: false} {
		got, err := s.GetUser(ctx, id)
		if err != nil {
			t.Fatalf("读取用户 %d 失败: %v", id, err)
		}
		if got.CloudDownload != CloudDownloadDefault {
			t.Fatalf("用户 %d 默认应为跟随默认(0)，得到 %d", id, got.CloudDownload)
		}
		if got.EffectiveCloudDownload() != want {
			t.Fatalf("用户 %d 默认生效值应为 %v", id, want)
		}
	}

	// 显式允许普通用户 → 生效；显式拒绝 owner → 失效；恢复默认 → 回到角色默认
	if err := s.UpdateUserCloudDownload(ctx, user.ID, CloudDownloadAllow); err != nil {
		t.Fatalf("显式允许失败: %v", err)
	}
	if u, _ := s.GetUser(ctx, user.ID); !u.EffectiveCloudDownload() {
		t.Fatal("显式允许后普通用户应生效允许")
	}
	if err := s.UpdateUserCloudDownload(ctx, owner.ID, CloudDownloadDeny); err != nil {
		t.Fatalf("显式拒绝 owner 失败: %v", err)
	}
	if u, _ := s.GetUser(ctx, owner.ID); u.EffectiveCloudDownload() {
		t.Fatal("显式拒绝后 owner 应失效")
	}
	if err := s.UpdateUserCloudDownload(ctx, user.ID, CloudDownloadDefault); err != nil {
		t.Fatalf("恢复默认失败: %v", err)
	}
	if u, _ := s.GetUser(ctx, user.ID); u.EffectiveCloudDownload() {
		t.Fatal("恢复默认后普通用户应回到拒绝")
	}
}
