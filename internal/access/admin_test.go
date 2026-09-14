package access

// 管理操作测试：状态变更（owner 保护）、owner 转移、手动添加与设置读取。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func TestSetUserStatus(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	if _, err := st.CreateUser(ctx, store.User{ID: 1, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// 禁用 → 归档 → 恢复，各留审计
	for _, tc := range []struct {
		status string
	}{
		{store.UserDisabled}, {store.UserArchived}, {store.UserEnabled},
	} {
		if err := svc.SetUserStatus(ctx, "admin", 1, tc.status); err != nil {
			t.Fatalf("变更到 %s 失败: %v", tc.status, err)
		}
	}
	u, _ := st.GetUser(ctx, 1)
	if u.Status != store.UserEnabled || u.ArchivedAt != 0 {
		t.Fatalf("恢复后应 enabled 且清归档时间：%+v", u)
	}
	audits, _ := st.ListAudit(ctx, 10, 0)
	if len(audits) != 3 || audits[0].Action != "user.set_status" || audits[0].Actor != "admin" {
		t.Fatalf("应留三条状态审计：%+v", audits)
	}

	// 幂等：同状态不写审计
	before := len(audits)
	if err := svc.SetUserStatus(ctx, "admin", 1, store.UserEnabled); err != nil {
		t.Fatalf("幂等变更失败: %v", err)
	}
	audits, _ = st.ListAudit(ctx, 10, 0)
	if len(audits) != before {
		t.Fatalf("同状态不应写审计，得到 %d 条", len(audits))
	}

	// owner 保护：不能停用或归档
	if err := st.SetOwner(ctx, 1, true); err != nil {
		t.Fatalf("设 owner 失败: %v", err)
	}
	for _, bad := range []string{store.UserDisabled, store.UserArchived} {
		err := svc.SetUserStatus(ctx, "admin", 1, bad)
		if apperr.From(err).Code != apperr.CodeStoreConstraint {
			t.Fatalf("停用/归档 owner 应被拒绝（%s），得到 %v", bad, err)
		}
	}
	u, _ = st.GetUser(ctx, 1)
	if u.Status != store.UserEnabled {
		t.Fatalf("owner 状态不应变化：%s", u.Status)
	}

	// 不存在与非法状态
	if err := svc.SetUserStatus(ctx, "admin", 404, store.UserEnabled); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在用户应 ErrNotFound，得到 %v", err)
	}
	if err := svc.SetUserStatus(ctx, "admin", 1, "bogus"); apperr.From(err).Code != apperr.CodeInternal {
		t.Fatalf("非法状态应 INTERNAL，得到 %v", err)
	}
}

func TestSetOwnerTransfer(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	mustEnabledUser(t, st, 1)
	mustEnabledUser(t, st, 2)

	if err := svc.SetOwner(ctx, "admin", 1, true); err != nil {
		t.Fatalf("设 owner 失败: %v", err)
	}
	// 转移到 2：1 失去标记（全局唯一）
	if err := svc.SetOwner(ctx, "admin", 2, true); err != nil {
		t.Fatalf("转移 owner 失败: %v", err)
	}
	u1, _ := st.GetUser(ctx, 1)
	u2, _ := st.GetUser(ctx, 2)
	if u1.IsOwner || !u2.IsOwner {
		t.Fatalf("owner 应转移到 2：%v %v", u1.IsOwner, u2.IsOwner)
	}
	// 取消
	if err := svc.SetOwner(ctx, "admin", 2, false); err != nil {
		t.Fatalf("取消 owner 失败: %v", err)
	}
	u2, _ = st.GetUser(ctx, 2)
	if u2.IsOwner {
		t.Fatal("应已取消 owner")
	}
	if !containsAuditAction(st, "user.set_owner") {
		t.Fatal("owner 变更应写审计")
	}
	// 幂等：重复取消不写审计
	n := countAudit(st)
	if err := svc.SetOwner(ctx, "admin", 2, false); err != nil {
		t.Fatalf("重复取消失败: %v", err)
	}
	if got := countAudit(st); got != n {
		t.Fatalf("幂等不应写审计：%d → %d", n, got)
	}
}

func TestAddUser(t *testing.T) {
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()

	if err := svc.AddUser(ctx, "admin", 77, "备注"); err != nil {
		t.Fatalf("添加失败: %v", err)
	}
	u, err := st.GetUser(ctx, 77)
	if err != nil || u.Status != store.UserEnabled || u.Note != "备注" {
		t.Fatalf("添加结果不符：%+v err=%v", u, err)
	}
	if !containsAuditAction(st, "user.add") {
		t.Fatal("添加应写审计")
	}
	// 重复 → STORE_CONSTRAINT；非法 ID → INTERNAL
	if err := svc.AddUser(ctx, "admin", 77, ""); apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatalf("重复添加应 STORE_CONSTRAINT，得到 %v", err)
	}
	if err := svc.AddUser(ctx, "admin", -1, ""); apperr.From(err).Code != apperr.CodeInternal {
		t.Fatalf("非法 ID 应 INTERNAL，得到 %v", err)
	}
}

func TestApproveConcurrentWithSetSender(t *testing.T) {
	// 当前 Web 审批（读 sender）与 MTProto 每轮重连的 SetSender（写）并发，
	// 须无数据竞争（-race 下验证读写锁保护）。
	clock := newClock(baseTime)
	svc, st, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()
	for i := int64(1); i <= 30; i++ {
		if _, err := st.CreateUser(ctx, store.User{ID: i, Status: store.UserPending}); err != nil {
			t.Fatalf("创建用户失败: %v", err)
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			svc.SetSender(&fakeSender{})
		}
	}()
	go func() {
		defer wg.Done()
		for i := int64(1); i <= 30; i++ {
			if _, err := svc.Approve(ctx, "admin", i); err != nil {
				t.Errorf("并发审批失败: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

func TestSettingsGetters(t *testing.T) {
	clock := newClock(baseTime)
	svc, _, _ := newTestService(t, 4, clock.Now)
	ctx := context.Background()

	// 缺省回退
	if got := svc.TimezoneName(ctx); got != defaultTimezoneName {
		t.Fatalf("缺省时区名不符：%s", got)
	}
	if got := svc.DedupWindowMinutes(ctx); got != int(defaultDedupWindow/time.Minute) {
		t.Fatalf("缺省去重窗口不符：%d", got)
	}
	if loc := svc.Location(ctx); loc.String() != defaultTimezoneName {
		t.Fatalf("缺省时区不符：%s", loc)
	}

	// 变更后读取生效
	if err := svc.SetTimezone(ctx, "UTC"); err != nil {
		t.Fatalf("设置时区失败: %v", err)
	}
	if err := svc.SetDedupWindow(ctx, 25); err != nil {
		t.Fatalf("设置去重窗口失败: %v", err)
	}
	if got := svc.TimezoneName(ctx); got != "UTC" {
		t.Fatalf("时区应生效：%s", got)
	}
	if got := svc.DedupWindowMinutes(ctx); got != 25 {
		t.Fatalf("去重窗口应生效：%d", got)
	}
	// 非法值拒绝入库
	if err := svc.SetDedupWindow(ctx, 0); err == nil {
		t.Fatal("非法去重窗口应报错")
	}
}

// containsAuditAction 判断是否已有指定动作的审计。
func containsAuditAction(st *store.Store, action string) bool {
	entries, err := st.ListAudit(context.Background(), 100, 0)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Action == action {
			return true
		}
	}
	return false
}

// countAudit 统计当前审计条数。
func countAudit(st *store.Store) int {
	entries, err := st.ListAudit(context.Background(), 1000, 0)
	if err != nil {
		return -1
	}
	return len(entries)
}
