package syscfg

import (
	"context"
	"testing"
)

func TestBackupLocalEnabledDefaultAndSet(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if !LoadBackupLocalEnabled(ctx, nil) || !LoadBackupLocalEnabled(ctx, st) {
		t.Fatal("nil Store 和未配置时应默认保留本地备份")
	}
	if err := SetBackupLocalEnabled(ctx, st, false); err != nil {
		t.Fatalf("关闭本地保留失败: %v", err)
	}
	if LoadBackupLocalEnabled(ctx, st) {
		t.Fatal("设置关闭后应读取为 false")
	}
	if err := SetBackupLocalEnabled(ctx, st, true); err != nil {
		t.Fatalf("开启本地保留失败: %v", err)
	}
	if !LoadBackupLocalEnabled(ctx, st) {
		t.Fatal("设置开启后应读取为 true")
	}
}

func TestBackupLocalEnabledInvalidSettingFallsBack(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := st.SetSetting(ctx, keyBackupLocalEnabled, `"false"`); err != nil {
		t.Fatal(err)
	}
	if !LoadBackupLocalEnabled(ctx, st) {
		t.Fatal("非法类型应回退为默认开启")
	}
}
