package syscfg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir()+"/test.db", nil)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestValidateName(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "缺省值合法", raw: "Spore", want: "Spore"},
		{name: "去首尾空白", raw: "  我的提取站  ", want: "我的提取站"},
		{name: "空串拒绝", raw: "   ", wantErr: true},
		{name: "超长拒绝", raw: strings.Repeat("字", nameMaxLen+1), wantErr: true},
		{name: "上限长度合法", raw: strings.Repeat("字", nameMaxLen), want: strings.Repeat("字", nameMaxLen)},
		{name: "控制字符拒绝", raw: "bad\x07name", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateName(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("期望合法，实际报错: %v", err)
			}
			if got != tt.want {
				t.Fatalf("期望 %q，实际 %q", tt.want, got)
			}
		})
	}
}

func TestNameFallbacks(t *testing.T) {
	ctx := context.Background()
	if got := Name(ctx, nil); got != DefaultName {
		t.Fatalf("nil Store 应回退缺省值，实际 %q", got)
	}
	st := newTestStore(t)
	if got := Name(ctx, st); got != DefaultName {
		t.Fatalf("未配置应回退缺省值，实际 %q", got)
	}
	// 非法 JSON / 非法值一律回退
	for _, raw := range []string{`"  "`, strings.Repeat("超长", 40), `123`} {
		if err := st.SetSetting(ctx, settingKeyName, raw); err != nil {
			t.Fatalf("写入非法值失败: %v", err)
		}
		if got := Name(ctx, st); got != DefaultName {
			t.Fatalf("非法值 %q 应回退缺省值，实际 %q", raw, got)
		}
	}
}

func TestSetNameAndName(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := SetName(ctx, st, "  我的提取站 "); err != nil {
		t.Fatalf("SetName 失败: %v", err)
	}
	if got := Name(ctx, st); got != "我的提取站" {
		t.Fatalf("期望读取规范化名称，实际 %q", got)
	}
	if err := SetName(ctx, st, ""); !errors.Is(err, err) || err == nil {
		t.Fatalf("空名称应报错，实际 %v", err)
	}
	// 校验失败不写库：旧值保留
	if got := Name(ctx, st); got != "我的提取站" {
		t.Fatalf("校验失败后旧值应保留，实际 %q", got)
	}
}
