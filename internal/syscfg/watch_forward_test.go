package syscfg

import (
	"context"
	"testing"
)

func TestWatchForwardChannelsEmptyByDefault(t *testing.T) {
	if got := LoadWatchForwardChannels(context.Background(), watchTestStore(t)); len(got) != 0 {
		t.Fatalf("空库应返回空转发频道列表: %+v", got)
	}
}

func TestWatchForwardChannelsRoundtrip(t *testing.T) {
	ctx := context.Background()
	st := watchTestStore(t)
	want := []WatchForwardChannel{
		{ChannelID: -100111, Title: "转发一"},
		{ChannelID: -100222, Title: "转发二"},
	}
	if err := SaveWatchForwardChannels(ctx, st, want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got := LoadWatchForwardChannels(ctx, st)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("读回应一致: %+v vs %+v", got, want)
	}

	// 空列表合法：清空 = 仅缓存兜底
	if err := SaveWatchForwardChannels(ctx, st, nil); err != nil {
		t.Fatalf("清空应合法: %v", err)
	}
	if got := LoadWatchForwardChannels(ctx, st); len(got) != 0 {
		t.Fatalf("清空后应为空: %+v", got)
	}
}

func TestWatchForwardChannelsValidation(t *testing.T) {
	ctx := context.Background()
	st := watchTestStore(t)
	tooMany := make([]WatchForwardChannel, watchForwardChannelsUpper+1)
	for i := range tooMany {
		tooMany[i] = WatchForwardChannel{ChannelID: int64(-100 - i), Title: "x"}
	}
	if err := SaveWatchForwardChannels(ctx, st, tooMany); err == nil {
		t.Error("超上限应拒绝落库")
	}
	if got := LoadWatchForwardChannels(ctx, st); len(got) != 0 {
		t.Fatalf("拒绝后不应落库: %+v", got)
	}
}

func TestWatchForwardChannelsLoadSanitizes(t *testing.T) {
	ctx := context.Background()
	st := watchTestStore(t)
	// 脏数据防御：零 ID 丢弃、重复 ID 去重、非法 JSON 回退空。
	if err := st.SetSetting(ctx, keyWatchForwardChannels,
		`[{"channel_id":0,"title":"零ID"},{"channel_id":-1001,"title":"a"},{"channel_id":-1001,"title":"b"}]`); err != nil {
		t.Fatalf("预置脏数据失败: %v", err)
	}
	got := LoadWatchForwardChannels(ctx, st)
	if len(got) != 1 || got[0].ChannelID != -1001 || got[0].Title != "a" {
		t.Fatalf("应去重并丢弃零 ID: %+v", got)
	}
	if err := st.SetSetting(ctx, keyWatchForwardChannels, "not-json"); err != nil {
		t.Fatalf("预置非法 JSON 失败: %v", err)
	}
	if got := LoadWatchForwardChannels(ctx, st); len(got) != 0 {
		t.Fatalf("非法 JSON 应回退空: %+v", got)
	}
}

func TestLoadDumpChannelTitle(t *testing.T) {
	ctx := context.Background()
	st := watchTestStore(t)
	if got := LoadDumpChannelTitle(ctx, st); got != "" {
		t.Fatalf("未配置应为空: %q", got)
	}
	if err := st.SetSetting(ctx, KeyDumpChannelTitle, `"Spore Cache"`); err != nil {
		t.Fatalf("写入标题失败: %v", err)
	}
	if got := LoadDumpChannelTitle(ctx, st); got != "Spore Cache" {
		t.Fatalf("读回应一致: %q", got)
	}
	// 非法 JSON 回退空
	if err := st.SetSetting(ctx, KeyDumpChannelTitle, "123"); err != nil {
		t.Fatalf("预置非法值失败: %v", err)
	}
	if got := LoadDumpChannelTitle(ctx, st); got != "" {
		t.Fatalf("非法存量值应回退空: %q", got)
	}
}
