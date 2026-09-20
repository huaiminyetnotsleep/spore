package syscfg

import (
	"context"
	"testing"
)

func TestLoadMaxRequestAttemptsFallbacks(t *testing.T) {
	ctx := context.Background()
	if got := LoadMaxRequestAttempts(ctx, nil); got != DefaultMaxRequestAttempts {
		t.Fatalf("nil Store 应回退缺省值，实际 %d", got)
	}
	st := newTestStore(t)
	if got := LoadMaxRequestAttempts(ctx, st); got != DefaultMaxRequestAttempts {
		t.Fatalf("未配置应回退缺省值，实际 %d", got)
	}
	// 非法 JSON / 越界值一律回退
	for _, raw := range []string{`"3"`, `0`, `-1`, `11`, `null`} {
		if err := st.SetSetting(ctx, keyMaxRequestAttempts, raw); err != nil {
			t.Fatalf("写入非法值失败: %v", err)
		}
		if got := LoadMaxRequestAttempts(ctx, st); got != DefaultMaxRequestAttempts {
			t.Fatalf("非法值 %q 应回退缺省值，实际 %d", raw, got)
		}
	}
}

func TestSetMaxRequestAttempts(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// 边界值合法
	for _, n := range []int{1, maxRequestAttemptsUpper} {
		if err := SetMaxRequestAttempts(ctx, st, n); err != nil {
			t.Fatalf("写入边界值 %d 失败: %v", n, err)
		}
		if got := LoadMaxRequestAttempts(ctx, st); got != n {
			t.Fatalf("期望读回 %d，实际 %d", n, got)
		}
	}

	// 越界拒绝且不写库（旧值保留）
	if err := SetMaxRequestAttempts(ctx, st, 0); err == nil {
		t.Fatal("0 应被拒绝")
	}
	if err := SetMaxRequestAttempts(ctx, st, maxRequestAttemptsUpper+1); err == nil {
		t.Fatal("超上界应被拒绝")
	}
	if got := LoadMaxRequestAttempts(ctx, st); got != maxRequestAttemptsUpper {
		t.Fatalf("校验失败后旧值应保留，实际 %d", got)
	}
}

func TestValidateMaxRequestAttempts(t *testing.T) {
	for _, n := range []int{-1, 0, maxRequestAttemptsUpper + 1} {
		if err := ValidateMaxRequestAttempts(n); err == nil {
			t.Fatalf("%d 应被拒绝", n)
		}
	}
	for _, n := range []int{1, 3, maxRequestAttemptsUpper} {
		if err := ValidateMaxRequestAttempts(n); err != nil {
			t.Fatalf("%d 应合法: %v", n, err)
		}
	}
}
