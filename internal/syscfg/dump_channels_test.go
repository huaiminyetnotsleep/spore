package syscfg

// dump_channels_test.go — 缓存频道列表配置：保存校验（上限/去重/零 ID）、
// 启用集合提取与旧单频道键折算回退链（列表键 > 旧键 > 环境变量）。

import (
	"context"
	"testing"
)

func TestSaveDumpChannelsValidation(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// 空列表合法（清空 = 复用关闭）
	if err := SaveDumpChannels(ctx, st, nil); err != nil {
		t.Fatalf("空列表应合法: %v", err)
	}
	if got := LoadDumpChannels(ctx, st); len(got) != 0 {
		t.Fatalf("空列表应读回空: %+v", got)
	}

	// 超上限拒绝
	tooMany := make([]DumpChannel, 0, dumpChannelsUpper+1)
	for i := 0; i <= dumpChannelsUpper; i++ {
		tooMany = append(tooMany, DumpChannel{ChannelID: int64(-100 - i), Enabled: true})
	}
	if err := SaveDumpChannels(ctx, st, tooMany); err == nil {
		t.Fatal("超上限应拒绝")
	}

	// 保存：去重 + 零 ID 过滤 + 启用状态保留
	channels := []DumpChannel{
		{ChannelID: -100111, Title: "A", Enabled: true},
		{ChannelID: -100111, Title: "重复", Enabled: false}, // 去重
		{ChannelID: 0, Title: "零 ID", Enabled: true},      // 过滤
		{ChannelID: -100222, Title: "B", Enabled: false},
	}
	if err := SaveDumpChannels(ctx, st, channels); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got := LoadDumpChannels(ctx, st)
	if len(got) != 2 || got[0].ChannelID != -100111 || !got[0].Enabled ||
		got[1].ChannelID != -100222 || got[1].Enabled {
		t.Fatalf("读回应去重且保留启用状态: %+v", got)
	}

	ids := EnabledDumpChannelIDs(got)
	if len(ids) != 1 || ids[0] != -100111 {
		t.Fatalf("启用集合应只含启用频道: %v", ids)
	}
}

func TestLoadEffectiveDumpChannelsFallbackChain(t *testing.T) {
	ctx := context.Background()
	const envID int64 = -100999

	// 全部缺失：环境变量折算为单条启用项；env=0 → 未配置
	st := newTestStore(t)
	got := LoadEffectiveDumpChannels(ctx, st, envID)
	if len(got) != 1 || got[0].ChannelID != envID || !got[0].Enabled {
		t.Fatalf("全缺失应回落 env: %+v", got)
	}
	if got := LoadEffectiveDumpChannels(ctx, st, 0); len(got) != 0 {
		t.Fatalf("env=0 应未配置: %+v", got)
	}

	// 旧键显式 0（旧版「清除配置」）：覆盖 env
	if err := st.SetSetting(ctx, keyDumpChannelID, "0"); err != nil {
		t.Fatalf("写旧键失败: %v", err)
	}
	if got := LoadEffectiveDumpChannels(ctx, st, envID); len(got) != 0 {
		t.Fatalf("旧键显式 0 应覆盖 env: %+v", got)
	}

	// 旧键配置频道：折算为单条启用项（标题取旧标题键快照）
	if err := st.SetSetting(ctx, keyDumpChannelID, "-100123"); err != nil {
		t.Fatalf("写旧键失败: %v", err)
	}
	if err := st.SetSetting(ctx, KeyDumpChannelTitle, `"旧频道"`); err != nil {
		t.Fatalf("写旧标题键失败: %v", err)
	}
	got = LoadEffectiveDumpChannels(ctx, st, envID)
	if len(got) != 1 || got[0].ChannelID != -100123 || got[0].Title != "旧频道" || !got[0].Enabled {
		t.Fatalf("旧键应折算为单条启用项: %+v", got)
	}

	// 列表键写入（含空列表）即完全接管
	if err := SaveDumpChannels(ctx, st, []DumpChannel{{ChannelID: -100555, Title: "新", Enabled: true}}); err != nil {
		t.Fatalf("写列表键失败: %v", err)
	}
	got = LoadEffectiveDumpChannels(ctx, st, envID)
	if len(got) != 1 || got[0].ChannelID != -100555 {
		t.Fatalf("列表键应接管: %+v", got)
	}
	if err := SaveDumpChannels(ctx, st, nil); err != nil {
		t.Fatalf("清空列表键失败: %v", err)
	}
	if got := LoadEffectiveDumpChannels(ctx, st, envID); len(got) != 0 {
		t.Fatalf("空列表键应显式关闭（不回落旧键/env）: %+v", got)
	}
}
