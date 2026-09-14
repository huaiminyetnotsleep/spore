package store

import (
	"context"
	"testing"
)

func TestSettingsRoundtrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 不存在：ok=false 且无错误
	if v, ok, err := s.GetSetting(ctx, "timezone"); err != nil || ok || v != "" {
		t.Fatalf("未写入的键应 ok=false 无错误，得到 %q %v %v", v, ok, err)
	}

	// 写入与读取
	if err := s.SetSetting(ctx, "timezone", `"Asia/Shanghai"`); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	v, ok, err := s.GetSetting(ctx, "timezone")
	if err != nil || !ok {
		t.Fatalf("读取失败: ok=%v err=%v", ok, err)
	}
	if v != `"Asia/Shanghai"` {
		t.Errorf("值应往返一致，得到 %q", v)
	}

	// 覆盖
	if err := s.SetSetting(ctx, "timezone", `"UTC"`); err != nil {
		t.Fatalf("覆盖失败: %v", err)
	}
	if v, _, _ = s.GetSetting(ctx, "timezone"); v != `"UTC"` {
		t.Errorf("覆盖后应为新值，得到 %q", v)
	}

	// 键之间互不影响
	if err := s.SetSetting(ctx, "dedup_window_min", "10"); err != nil {
		t.Fatalf("写第二键失败: %v", err)
	}
	if _, ok, _ := s.GetSetting(ctx, "dedup_window_min"); !ok {
		t.Error("第二键应独立存在")
	}

	// 删除与幂等删除
	if err := s.DeleteSetting(ctx, "timezone"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, ok, _ := s.GetSetting(ctx, "timezone"); ok {
		t.Error("删除后应读不到")
	}
	if err := s.DeleteSetting(ctx, "timezone"); err != nil {
		t.Fatalf("重复删除应幂等: %v", err)
	}
}
