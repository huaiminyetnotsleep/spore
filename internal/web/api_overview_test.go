package web

// GET /api/v1/overview 快照契约测试：瘦身后只携带服务状态/队列/用户快照
// 与 join 全时段统计；不再解析 since/until。范围类指标与图表数据的
// 行为由 api_stats_test.go 覆盖。

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

func TestAPIOverviewSnapshotJoinTally(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	ctx := context.Background()

	// 种子数据：用户 2 人 + 申请 4 态各 1 + 留痕五来源中的四类有数据
	//（command 在加入 1 退出 1、approved 在加入 2 退出 1、external/bind 各在加入 1）
	seedUser(t, e, 101, store.UserEnabled)
	seedUser(t, e, 102, store.UserPending)
	for i, status := range []string{
		store.JoinPending, store.JoinApproved, store.JoinRejected, store.JoinFailed,
	} {
		r, _, err := e.st.CreateJoinRequest(ctx, store.JoinRequest{
			UserID: 101, InviteHash: strings.Repeat("a", 16) + string(rune('0'+i)),
		})
		if err != nil {
			t.Fatalf("写入加入申请失败: %v", err)
		}
		if status != store.JoinPending {
			if _, err := e.st.ReviewJoinRequest(ctx, r.ID, status, "admin", "", 0); err != nil {
				t.Fatalf("迁移申请终态失败: %v", err)
			}
		}
	}
	seedJoined := []struct {
		id  int64
		via string
	}{
		{1, store.JoinedViaCommand}, {2, store.JoinedViaCommand},
		{3, store.JoinedViaApproved}, {4, store.JoinedViaApproved},
		{5, store.JoinedViaApproved}, {6, store.JoinedViaExternal},
		{7, store.JoinedViaBindResolve},
	}
	for _, s := range seedJoined {
		if err := e.st.UpsertJoinedChannel(ctx, store.JoinedChannelRecord{
			ChannelID: s.id, JoinedVia: s.via, JoinedBy: 101,
		}); err != nil {
			t.Fatalf("写入已加入频道留痕失败: %v", err)
		}
	}
	if _, err := e.st.MarkJoinedChannelsLeft(ctx, []int64{2, 5}, 0); err != nil {
		t.Fatalf("标记退出失败: %v", err)
	}

	body := getAPIJSON(t, e, j, "/api/v1/overview?since=2026-01-01&until=2026-01-02", &struct{}{})
	var view apiOverviewView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatalf("解码 overview 失败: %v", err)
	}

	// 快照字段仍在
	if view.Version != Version || view.Workers != 2 {
		t.Errorf("服务快照字段不对: version=%q workers=%d", view.Version, view.Workers)
	}
	if view.Users.Total != 2 || view.Users.Enabled != 1 || view.Users.Pending != 1 {
		t.Errorf("用户计数不对: %+v", view.Users)
	}
	// 不再回显时间范围（since/until 被忽略且无该字段）
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		t.Fatalf("解码 overview 原始字段失败: %v", err)
	}
	for _, key := range []string{"since_day", "until_day"} {
		if _, ok := raw[key]; ok {
			t.Errorf("overview 不应再返回 %q（body=%s）", key, body)
		}
	}
	if _, ok := raw["join"]; !ok {
		t.Fatalf("overview 应包含 join 快照字段（body=%s）", body)
	}

	// join 快照与种子一致；max_channels 来自 syscfg 缺省值
	wantMax := syscfg.LoadJoinConfig(ctx, e.st).MaxChannels
	if view.Join.Pending != 1 || view.Join.Approved != 1 || view.Join.Rejected != 1 || view.Join.Failed != 1 {
		t.Errorf("申请状态计数不对: %+v", view.Join)
	}
	if view.Join.ActiveJoined != 5 || view.Join.ExternalActive != 1 || view.Join.LeftTotal != 2 {
		t.Errorf("频道加入计数不对: %+v", view.Join)
	}
	if view.Join.MaxChannels != wantMax {
		t.Errorf("max_channels 应为 %d（0=不限），得到 %d", wantMax, view.Join.MaxChannels)
	}
	wantDist := map[string]int{
		store.JoinedViaCommand: 1, store.JoinedViaApproved: 2,
		store.JoinedViaExternal: 1, store.JoinedViaWatchSource: 0,
		store.JoinedViaBindResolve: 1,
	}
	if len(view.Join.SourceDist) != 5 {
		t.Fatalf("来源分布应固定五行（含 watch_source 0 行）: %+v", view.Join.SourceDist)
	}
	for _, row := range view.Join.SourceDist {
		if row.Count != wantDist[row.Key] {
			t.Errorf("来源 %s 计数应为 %d，得到 %d", row.Key, wantDist[row.Key], row.Count)
		}
	}
}

// 空库时 join 快照为零值 + 五来源 0 行（join 功能未启用也照常下发）。
func TestAPIOverviewJoinZeroState(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	var view apiOverviewView
	getAPIJSON(t, e, j, "/api/v1/overview", &view)
	if view.Join.Pending != 0 || view.Join.ActiveJoined != 0 || view.Join.LeftTotal != 0 {
		t.Errorf("空库 join 计数应为零: %+v", view.Join)
	}
	if len(view.Join.SourceDist) != 5 {
		t.Fatalf("来源分布应固定五行（含 0）: %+v", view.Join.SourceDist)
	}
	for _, row := range view.Join.SourceDist {
		if row.Count != 0 {
			t.Errorf("空库来源 %s 计数应为 0，得到 %d", row.Key, row.Count)
		}
	}
}

// overview 响应禁止缓存（快照随实时状态变化）。
func TestAPIOverviewNoStoreHeader(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	resp := e.do(j, http.MethodGet, "/api/v1/overview", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview 应返回 200，得到 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("overview 应禁止缓存，得到 Cache-Control %q", got)
	}
	bodyOf(t, resp)
}

// 接入机器人身份随快照下发（总览页展示 Name 与 @username）；
// 未注入或未就绪时字段整体省略，前端显示"未接入"。
func TestAPIOverviewBotIdentity(t *testing.T) {
	t.Run("已就绪时下发", func(t *testing.T) {
		e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
			opt.BotIdentity = fakeBotIdentity{id: BotIdentity{ID: 42, Name: "Spore Bot", Username: "spore_bot"}}
		})
		j := e.login(t)
		var view apiOverviewView
		body := getAPIJSON(t, e, j, "/api/v1/overview", &view)
		if view.Bot == nil || view.Bot.ID != 42 || view.Bot.Name != "Spore Bot" || view.Bot.Username != "spore_bot" {
			t.Fatalf("机器人身份不符: %+v", view.Bot)
		}
		if !strings.Contains(body, `"bot":{"id":42,"name":"Spore Bot","username":"spore_bot"}`) {
			t.Fatalf("overview 应下发 bot 身份字段：%s", body)
		}
	})

	t.Run("未注入时省略", func(t *testing.T) {
		e := newTestEnv(t, nil)
		j := e.login(t)
		var view apiOverviewView
		body := getAPIJSON(t, e, j, "/api/v1/overview", &view)
		if view.Bot != nil {
			t.Fatalf("未注入时 bot 应为 nil: %+v", view.Bot)
		}
		if strings.Contains(body, `"bot":`) {
			t.Fatalf("未注入时不应下发 bot 字段：%s", body)
		}
	})
}

// Bot MTProto 会话状态与当前主 DC 随 health 下发；未接入时字段整体省略。
func TestAPIOverviewBotSession(t *testing.T) {
	t.Run("ready 带当前 DC", func(t *testing.T) {
		e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
			opt.BotMTProto = fakeBotStatus{snap: mtproto.BotStatusSnapshot{
				State: mtproto.BotStateReady, DCID: 4, UpdatedAt: 30,
			}}
		})
		j := e.login(t)
		var view apiOverviewView
		body := getAPIJSON(t, e, j, "/api/v1/overview", &view)
		if view.Health.BotMTProtoState != mtproto.BotStateReady || view.Health.BotMTProtoDCID != 4 {
			t.Fatalf("Bot 会话状态/DC 不符: %+v", view.Health)
		}
		if !strings.Contains(body, `"bot_mtproto_dc_id":4`) {
			t.Fatalf("overview 应下发 bot_mtproto_dc_id：%s", body)
		}
	})

	t.Run("offline 时 DC 清零省略", func(t *testing.T) {
		e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
			opt.BotMTProto = fakeBotStatus{snap: mtproto.BotStatusSnapshot{
				State: mtproto.BotStateOffline,
			}}
		})
		j := e.login(t)
		var view apiOverviewView
		body := getAPIJSON(t, e, j, "/api/v1/overview", &view)
		if view.Health.BotMTProtoState != mtproto.BotStateOffline {
			t.Fatalf("Bot 会话应为 offline: %+v", view.Health)
		}
		if strings.Contains(body, "bot_mtproto_dc_id") {
			t.Fatalf("离线时不应下发 DC 字段：%s", body)
		}
	})

	t.Run("未接入时省略整个字段", func(t *testing.T) {
		e := newTestEnv(t, nil)
		j := e.login(t)
		body := getAPIJSON(t, e, j, "/api/v1/overview", &struct{}{})
		if strings.Contains(body, "bot_mtproto_state") {
			t.Fatalf("未接入时不应下发 bot_mtproto_state：%s", body)
		}
	})
}

// Options.Version 注入的构建期版本优先于内置缺省常量（CI ldflags 注入链路：
// cmd/bot 把 main.version 传给 web.Options）；未注入时回退缺省的行为由
// TestAPIOverviewSnapshotJoinTally 的 Version 比对覆盖。
func TestAPIOverviewVersionOverride(t *testing.T) {
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		opt.Version = "v9.9.9-e2e"
	})
	j := e.login(t)
	var view apiOverviewView
	getAPIJSON(t, e, j, "/api/v1/overview", &view)
	if view.Version != "v9.9.9-e2e" {
		t.Fatalf("应展示注入的构建期版本，得到 %q", view.Version)
	}
}
