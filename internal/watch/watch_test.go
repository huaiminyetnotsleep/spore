package watch

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/watch.db", testLog())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc, err := New(Options{Store: st, Log: testLog()})
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	return svc, st
}

// 申请开关关闭时直接拒绝（无需 Bot 客户端参与，号主豁免）。
func TestSubmitDisabledWithoutBots(t *testing.T) {
	svc, _ := newService(t)
	ctx := context.Background()
	out, err := svc.Submit(ctx, 7, false, "@chan", 0, "")
	if err != nil || out.Kind != SubmitDisabled {
		t.Fatalf("默认配置（apply 关闭）应拒绝: %+v err=%v", out, err)
	}
	// 号主不受申请开关限制，进入后续校验（无 Bot 客户端时报内部错误）
	if _, err := svc.Submit(ctx, 7, true, "@chan", 0, ""); err == nil {
		t.Fatal("号主路径应继续校验（此处因 Bot 未接入报错而非拒绝）")
	}
}

// 用户准入：未 /start（无用户行或非 enabled）一律拒绝。
func TestSubmitUserNotAllowed(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()
	if err := syscfg.SaveWatchConfig(ctx, st, syscfg.WatchConfig{
		ApplyEnabled: true, RequireApproval: true, MaxSources: 20, PerUserLimit: 3,
	}); err != nil {
		t.Fatalf("开启申请失败: %v", err)
	}
	// 无用户行
	out, err := svc.Submit(ctx, 7, false, "@chan", 0, "")
	if err != nil || out.Kind != SubmitUserNotAllowed {
		t.Fatalf("无用户行应拒绝: %+v err=%v", out, err)
	}
	// 待审批用户
	if _, err := st.CreateUser(ctx, store.User{ID: 7, Status: store.UserPending}); err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	out, err = svc.Submit(ctx, 7, false, "@chan", 0, "")
	if err != nil || out.Kind != SubmitUserNotAllowed {
		t.Fatalf("非 enabled 用户应拒绝: %+v err=%v", out, err)
	}
	// enabled 用户进入源校验（无 Bot 客户端 → CodeInternal 错误）
	if _, err := st.UpdateUserStatus(ctx, 7, store.UserEnabled); err != nil {
		t.Fatalf("启用用户失败: %v", err)
	}
	if _, err := svc.Submit(ctx, 7, false, "@chan", 0, ""); err == nil {
		t.Fatal("enabled 用户应进入源校验（Bot 未接入时报错）")
	}
}
