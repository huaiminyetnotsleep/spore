package syscfg

import (
	"context"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func watchTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), t.TempDir()+"/watch.db", nil)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestWatchConfigDefaults(t *testing.T) {
	cfg := LoadWatchConfig(context.Background(), watchTestStore(t))
	if cfg != DefaultWatchConfig() {
		t.Fatalf("空库应返回缺省配置: %+v", cfg)
	}
	if !cfg.RequireApproval || cfg.ApplyEnabled || cfg.MaxSources != 20 || cfg.PerUserLimit != 3 {
		t.Fatalf("缺省值不符: %+v", cfg)
	}
}

func TestWatchConfigSaveLoadRoundtrip(t *testing.T) {
	ctx := context.Background()
	st := watchTestStore(t)
	want := WatchConfig{ApplyEnabled: true, RequireApproval: false, MaxSources: 50, PerUserLimit: 0}
	if err := SaveWatchConfig(ctx, st, want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if got := LoadWatchConfig(ctx, st); got != want {
		t.Fatalf("读回应一致: %+v vs %+v", got, want)
	}
}

func TestWatchConfigValidation(t *testing.T) {
	cases := []WatchConfig{
		{MaxSources: -1},
		{MaxSources: 201},
		{PerUserLimit: -1},
		{PerUserLimit: 21},
	}
	for i, cfg := range cases {
		if err := ValidateWatchConfig(cfg); err == nil {
			t.Errorf("case %d 应拒绝: %+v", i, cfg)
		}
	}
	if err := ValidateWatchConfig(DefaultWatchConfig()); err != nil {
		t.Errorf("缺省配置应合法: %v", err)
	}
	if err := SaveWatchConfig(context.Background(), watchTestStore(t), WatchConfig{MaxSources: 999}); err == nil {
		t.Error("非法值应拒绝落库")
	}
}

func TestWatchConfigInvalidStoredValueFallsBack(t *testing.T) {
	ctx := context.Background()
	st := watchTestStore(t)
	if err := st.SetSetting(ctx, keyWatchMaxSources, "999"); err != nil {
		t.Fatalf("预置非法值失败: %v", err)
	}
	cfg := LoadWatchConfig(ctx, st)
	if cfg.MaxSources != DefaultWatchMaxSources {
		t.Fatalf("非法存量值应回退缺省: %+v", cfg)
	}
}
