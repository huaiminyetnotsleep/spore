package mtproto

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
)

func TestChannelsOfUpdates(t *testing.T) {
	ch := &tg.Channel{ID: 5, AccessHash: 55, Title: "频道", Broadcast: true}
	group := &tg.Chat{ID: 9}
	cases := []struct {
		name string
		in   tg.UpdatesClass
		want []int64
	}{
		{"Updates 携带频道", &tg.Updates{Chats: []tg.ChatClass{group, ch}}, []int64{5}},
		{"UpdatesCombined 携带频道", &tg.UpdatesCombined{Chats: []tg.ChatClass{ch}}, []int64{5}},
		{"Updates 无频道对象", &tg.Updates{}, nil},
		{"Short 形态忽略", &tg.UpdateShort{Update: &tg.UpdateChannel{ChannelID: 5}}, nil},
		{"TooLong 忽略", &tg.UpdatesTooLong{}, nil},
	}
	for _, c := range cases {
		got := channelsOfUpdates(c.in)
		if len(got) != len(c.want) {
			t.Fatalf("%s: 期望 %d 个频道, 实得 %d", c.name, len(c.want), len(got))
		}
		for i, id := range c.want {
			if got[i].ID != id || got[i].AccessHash == 0 {
				t.Fatalf("%s: 频道对象不符: %+v", c.name, got[i])
			}
		}
	}
}

func TestChannelUpdateBridge(t *testing.T) {
	b := NewChannelUpdateBridge()
	ch := &tg.Channel{ID: 5, AccessHash: 55}
	u := &tg.Updates{Chats: []tg.ChatClass{ch}}

	// 未绑定时静默丢弃、不 panic
	if err := b.Handle(context.Background(), u); err != nil {
		t.Fatalf("未绑定时应返回 nil: %v", err)
	}

	var got []tg.Channel
	b.Bind(func(ctx context.Context, channels []tg.Channel) { got = append(got, channels...) })
	if err := b.Handle(context.Background(), u); err != nil {
		t.Fatalf("Handle 失败: %v", err)
	}
	if len(got) != 1 || got[0].ID != 5 {
		t.Fatalf("绑定后应转发频道对象: %+v", got)
	}

	// 无频道对象的批次不触发转发
	n := 0
	b.Bind(func(ctx context.Context, channels []tg.Channel) { n += len(channels) })
	if err := b.Handle(context.Background(), &tg.Updates{}); err != nil {
		t.Fatalf("Handle 失败: %v", err)
	}
	if n != 0 {
		t.Fatalf("无频道对象不应转发: %d", n)
	}

	// 解绑后丢弃
	b.Unbind()
	if err := b.Handle(context.Background(), u); err != nil {
		t.Fatalf("解绑后应返回 nil: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("解绑后不应再转发: %+v", got)
	}
}
