package store

// 多机器人池的 bot 维度：requests 受理 bot、users 来源 bot 的落库回读，
// 以及列表筛选与统计聚合的 bot 维度行为（v15 迁移）。

import (
	"context"
	"testing"
)

func TestRequestBotFieldsRoundtrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 7)

	created, err := s.CreateRequest(ctx, Request{
		UserID: 7, ChannelKey: "alpha", MessageID: 1,
		BotID: 111, BotUsername: "spore_bot",
	})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	got, err := s.GetRequest(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取请求失败: %v", err)
	}
	if got.BotID != 111 || got.BotUsername != "spore_bot" {
		t.Fatalf("受理 bot 字段回读不符: %+v", got)
	}

	// 未指定时落默认 0（存量/Web 补存路径），回读为 0
	legacy, err := s.CreateRequest(ctx, Request{UserID: 7, ChannelKey: "alpha", MessageID: 2})
	if err != nil {
		t.Fatalf("创建存量请求失败: %v", err)
	}
	if got, err = s.GetRequest(ctx, legacy.ID); err != nil || got.BotID != 0 || got.BotUsername != "" {
		t.Fatalf("存量路径 bot 维度应为零值: %+v err=%v", got, err)
	}
}

func TestRequestFilterByBot(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 7)

	mk := func(botID int64, msgID int) {
		t.Helper()
		if _, err := s.CreateRequest(ctx, Request{
			UserID: 7, ChannelKey: "alpha", MessageID: msgID,
			BotID: botID, BotUsername: "b",
		}); err != nil {
			t.Fatalf("创建请求失败: %v", err)
		}
	}
	mk(111, 1)
	mk(111, 2)
	mk(222, 3)
	mk(0, 4)

	n, err := s.CountRequests(ctx, RequestFilter{BotID: 111})
	if err != nil || n != 2 {
		t.Fatalf("bot=111 筛选应为 2 条，得到 %d err=%v", n, err)
	}
	if n, err = s.CountRequests(ctx, RequestFilter{}); err != nil || n != 4 {
		t.Fatalf("不限 bot 应为 4 条，得到 %d err=%v", n, err)
	}
}

func TestUserSourceBotRoundtrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if _, err := s.CreateUser(ctx, User{
		ID: 9, Status: UserPending,
		SourceBotID: 222, SourceBotUsername: "spore_two",
	}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	u, err := s.GetUser(ctx, 9)
	if err != nil {
		t.Fatalf("读取用户失败: %v", err)
	}
	if u.SourceBotID != 222 || u.SourceBotUsername != "spore_two" {
		t.Fatalf("来源 bot 字段回读不符: %+v", u)
	}

	// Web 手动添加（零值）保持为 0
	mustUser(t, s, 10)
	if u, err = s.GetUser(ctx, 10); err != nil || u.SourceBotID != 0 {
		t.Fatalf("Web 添加用户来源 bot 应为 0: %+v err=%v", u, err)
	}
}

func TestStatsByBot(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 1)

	for _, in := range []seedReq{
		{1, "alpha", 1, RequestSucceeded, 100, "text", ""},
		{1, "alpha", 2, RequestFailed, 200, "", "X"},
		{1, "beta", 3, RequestSucceeded, 300, "photo", ""},
	} {
		seedRequest(t, s, in)
	}
	// 经 CreateRequest 的 bot 字段构造直观数据集
	s2 := openTestStore(t)
	mustUser(t, s2, 1)
	mk := func(botID int64, status string) {
		t.Helper()
		r, err := s2.CreateRequest(ctx, Request{
			UserID: 1, ChannelKey: "alpha", MessageID: int(botID),
			BotID: botID, BotUsername: "b",
		})
		if err != nil {
			t.Fatalf("创建请求失败: %v", err)
		}
		if status != RequestQueued {
			if err := s2.FinishRequest(ctx, r.ID, RequestResult{Status: status}); err != nil {
				t.Fatalf("落库终态失败: %v", err)
			}
		}
	}
	mk(111, RequestSucceeded)
	mk(111, RequestFailed)
	mk(222, RequestSucceeded)
	mk(0, RequestSucceeded)

	bots, err := s2.ListBotStats(ctx, StatsFilter{})
	if err != nil {
		t.Fatalf("聚合机器人统计失败: %v", err)
	}
	if len(bots) != 3 {
		t.Fatalf("应有三行（111/222/存量0），得到 %d：%+v", len(bots), bots)
	}
	// 按请求量倒序：111（2 条）第一
	if bots[0].BotID != 111 || bots[0].Total != 2 || bots[0].Succeeded != 1 || bots[0].Failed != 1 {
		t.Errorf("bot 111 统计不符: %+v", bots[0])
	}

	// bot 维度筛选：只统计该 bot
	totals, err := s2.RequestTotals(ctx, StatsFilter{BotID: 222})
	if err != nil {
		t.Fatalf("bot 筛选汇总失败: %v", err)
	}
	if totals.Total != 1 || totals.Succeeded != 1 {
		t.Errorf("bot=222 汇总不符: %+v", totals)
	}

	// 频道内按 bot 分布
	dist, err := s2.ListChannelBotStats(ctx, StatsFilter{ChannelKey: "alpha"})
	if err != nil {
		t.Fatalf("频道机器人分布失败: %v", err)
	}
	if len(dist) != 3 || dist[0].Total != 2 {
		t.Errorf("频道内按 bot 分布不符: %+v", dist)
	}
}
