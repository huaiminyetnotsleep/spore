package syscfg

import (
	"context"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func TestJoinConfigDefaults(t *testing.T) {
	got := LoadJoinConfig(context.Background(), nil)
	want := JoinConfig{
		Enabled:           false, // 总开关默认关
		AutoLeaveExternal: false, // 自动退出外部拉入默认关
		RequireApproval:   true,
		MaxChannels:       20,
		MuteEnabled:       true,
		ArchiveEnabled:    true,
	}
	if got != want {
		t.Fatalf("缺省配置不符\nwant: %+v\ngot:  %+v", want, got)
	}
}

func TestJoinConfigSaveLoad(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir()+"/test.db", nil)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer s.Close()

	in := JoinConfig{Enabled: true, AutoLeaveExternal: true, RequireApproval: false,
		MaxChannels: 50, MuteEnabled: false, ArchiveEnabled: false}
	if err := SaveJoinConfig(context.Background(), s, in); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got := LoadJoinConfig(context.Background(), s)
	if got != in {
		t.Fatalf("回读不一致\nwant: %+v\ngot:  %+v", in, got)
	}
}

func TestJoinConfigValidation(t *testing.T) {
	if err := ValidateJoinConfig(JoinConfig{MaxChannels: 0}); err != nil {
		t.Fatalf("0（不限）应合法: %v", err)
	}
	if err := ValidateJoinConfig(JoinConfig{MaxChannels: 200}); err != nil {
		t.Fatalf("200 应合法: %v", err)
	}
	if err := ValidateJoinConfig(JoinConfig{MaxChannels: -1}); err == nil {
		t.Fatalf("负数应拒绝")
	}
	if err := ValidateJoinConfig(JoinConfig{MaxChannels: 201}); err == nil {
		t.Fatalf("超上限应拒绝")
	}
}

func TestJoinConfigIllegalFallback(t *testing.T) {
	s, err := store.Open(context.Background(), t.TempDir()+"/test.db", nil)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// 直接写非法值（绕过 SaveJoinConfig）→ 读取时回退缺省
	_ = s.SetSetting(ctx, keyJoinEnabled, "not-a-bool")
	_ = s.SetSetting(ctx, keyJoinMaxChannels, `"abc"`)
	got := LoadJoinConfig(ctx, s)
	if got.Enabled || got.MaxChannels != DefaultJoinMaxChannels {
		t.Fatalf("非法值应回退缺省: %+v", got)
	}
}
