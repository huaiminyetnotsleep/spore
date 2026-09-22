package binding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbot "github.com/go-telegram/bot"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// newAdviceBot 构造指定 ID 的测试 bot：getChat 返回 chatJSON，getChatMember
// 返回 memberJSON。用于多 bot 池的逐台判定测试（bot ID 取自 token 前缀）。
func newAdviceBot(t *testing.T, id int64, chatJSON, memberJSON string) *tgbot.Bot {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":` + itoa(id) + `,"is_bot":true,"first_name":"Spore","username":"spore_bot"}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/getChat") {
			_, _ = w.Write([]byte(`{"ok":true,"result":` + chatJSON + `}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/getChatMember") {
			if memberJSON == "" {
				// memberJSON 为空模拟 bot 不在群：Telegram 拒绝成员查询
				_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request:"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":` + memberJSON + `}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	t.Cleanup(srv.Close)
	b, err := tgbot.New(itoa(id)+":test-token", tgbot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatalf("构造测试 Bot 失败: %v", err)
	}
	return b
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// TestBindPerBotJudgement 覆盖逐 bot 判定：绑定硬校验只卡接收请求的 bot，
// 其余 bot 未就绪在 advice 里点名、不拦绑定。
func TestBindPerBotJudgement(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	pinAdmin := pinMemberJSON
	noPinAdmin := noPinMemberJSON
	plainMember := `{"status":"member","user":{"id":12345,"is_bot":true}}`

	t.Run("其余 bot 缺置顶权限不拦绑定并点名", func(t *testing.T) {
		good := newAdviceBot(t, 12345, supergroupChatJSON, pinAdmin)
		bad := newAdviceBot(t, 67890, supergroupChatJSON, noPinAdmin)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{good, bad})
		bound, advice, err := svc.BindWithAdvice(ctx, BindInput{UserID: 100, Target: "@mygroup", Via: store.BoundViaBot, BotID: 12345})
		if err != nil {
			t.Fatalf("接收 bot 达标时其余 bot 缺权限不应拦绑定: %v", err)
		}
		if bound.ChannelID != testChannelD {
			t.Fatalf("绑定 ID 不符: %+v", bound)
		}
		if !strings.Contains(advice, "机器人 67890") || !strings.Contains(advice, "置顶消息") {
			t.Fatalf("advice 应点名 67890 缺置顶消息权限: %q", advice)
		}
	})

	t.Run("其余 bot 不在群点名无法读取成员身份", func(t *testing.T) {
		good := newAdviceBot(t, 12345, supergroupChatJSON, pinAdmin)
		absent := newAdviceBot(t, 67890, supergroupChatJSON, "")
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{good, absent})
		_, advice, err := svc.BindWithAdvice(ctx, BindInput{UserID: 100, Target: "@mygroup", Via: store.BoundViaBot, BotID: 12345})
		if err != nil {
			t.Fatalf("其余 bot 不在群不应拦绑定: %v", err)
		}
		if !strings.Contains(advice, "机器人 67890") || !strings.Contains(advice, "无法读取成员身份") {
			t.Fatalf("advice 应点名 67890 无法读取成员身份: %q", advice)
		}
	})

	t.Run("接收 bot 缺置顶权限仍拒绝", func(t *testing.T) {
		bad := newAdviceBot(t, 67890, supergroupChatJSON, noPinAdmin)
		good := newAdviceBot(t, 12345, supergroupChatJSON, pinAdmin)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{good, bad})
		_, advice, err := svc.BindWithAdvice(ctx, BindInput{UserID: 100, Target: "@mygroup", Via: store.BoundViaBot, BotID: 67890})
		if err == nil {
			t.Fatalf("接收 bot 缺置顶权限应拒绝绑定: advice=%q", advice)
		}
	})

	t.Run("其余 bot 是普通成员点名副本可用置顶不可用", func(t *testing.T) {
		good := newAdviceBot(t, 12345, supergroupChatJSON, pinAdmin)
		member := newAdviceBot(t, 67890, supergroupChatJSON, plainMember)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{good, member})
		_, advice, err := svc.BindWithAdvice(ctx, BindInput{UserID: 100, Target: "@mygroup", Via: store.BoundViaBot, BotID: 12345})
		if err != nil {
			t.Fatalf("其余 bot 普通成员不应拦绑定: %v", err)
		}
		if !strings.Contains(advice, "机器人 67890") || !strings.Contains(advice, "普通成员") {
			t.Fatalf("advice 应点名 67890 普通成员缺置顶: %q", advice)
		}
	})

	t.Run("全部达标 advice 为空", func(t *testing.T) {
		good := newAdviceBot(t, 12345, supergroupChatJSON, pinAdmin)
		good2 := newAdviceBot(t, 67890, supergroupChatJSON, pinAdmin)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{good, good2})
		_, advice, err := svc.BindWithAdvice(ctx, BindInput{UserID: 100, Target: "@mygroup", Via: store.BoundViaBot, BotID: 12345})
		if err != nil || advice != "" {
			t.Fatalf("全部达标应绑定成功且无提示: %v %q", err, advice)
		}
	})
}

// TestPinCapabilityHintMemberFlagged：普通成员（非管理员）也应触发置顶软提示。
func TestPinCapabilityHintMemberFlagged(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	if _, err := s.UpsertChannelBinding(ctx, store.ChannelBinding{
		ChannelID: testChannelD, UserID: 100, Username: "mychan", Title: "我的频道", BoundVia: store.BoundViaBot,
	}); err != nil {
		t.Fatalf("预置绑定失败: %v", err)
	}
	b, _ := newCopyPinBot(t, `{"status":"member","user":{"id":12345,"is_bot":true}}`)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})
	if hint := svc.PinCapabilityHint(ctx, 100); hint == "" {
		t.Fatal("普通成员应返回置顶软提示")
	}
}

// TestBindPersistsRoutingBot：Bot 路径绑定把接收 bot 落进路由归属，
// Web 路径（BotID=0）存通配（同用户重绑幂等更新路由字段）。
func TestBindPersistsRoutingBot(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	good := newAdviceBot(t, 12345, supergroupChatJSON, pinMemberJSON)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{good})

	bound, _, err := svc.BindWithAdvice(ctx, BindInput{UserID: 100, Target: "@mygroup", Via: store.BoundViaBot, BotID: 12345})
	if err != nil || bound.BotID != 12345 {
		t.Fatalf("Bot 路径绑定应记录路由 bot 12345: %+v %v", bound, err)
	}
	// 同用户换 Web 入口重绑（幂等更新）→ 路由归属改为通配
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mygroup", Via: store.BoundViaWeb}); err != nil {
		t.Fatalf("重绑失败: %v", err)
	}
	got, err := s.GetChannelBinding(ctx, bound.ChannelID)
	if err != nil || got.BotID != 0 {
		t.Fatalf("Web 重绑应把路由归属改为通配 0: %+v %v", got, err)
	}
}

// TestCopyToChannelsRouting：投递按绑定路由过滤——绑定属于其他 bot 时跳过
// （pin 时计入 Skipped），通配绑定照常投递。
func TestCopyToChannelsRouting(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	seed := func(channelID int64, botID int64, title string) {
		t.Helper()
		if _, err := s.UpsertChannelBinding(ctx, store.ChannelBinding{
			ChannelID: channelID, UserID: 100, Username: "u" + itoa(channelID), Title: title,
			BoundVia: store.BoundViaBot, BotID: botID,
		}); err != nil {
			t.Fatalf("预置绑定失败: %v", err)
		}
	}
	const ownBot, otherBot = int64(12345), int64(67890)
	seed(-1001111111111, ownBot, "本bot绑定")
	seed(-1002222222222, 0, "通配绑定")
	seed(-1003333333333, otherBot, "他bot绑定")

	b, methods := newCopyPinBot(t, pinMemberJSON)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})

	outcome := svc.CopyToChannels(ctx, ownBot, 100, 100, []int{11}, true)
	if outcome.Total != 2 || outcome.OK != 2 {
		t.Fatalf("应投递 2 个目标（本 bot + 通配），得到 %d/%d", outcome.OK, outcome.Total)
	}
	if len(outcome.Skipped) != 1 || !strings.Contains(outcome.Skipped[0], "他bot绑定") {
		t.Fatalf("Skipped 应含他 bot 绑定显示名: %+v", outcome.Skipped)
	}
	copies := 0
	for _, m := range *methods {
		if strings.HasSuffix(m, "/copyMessages") {
			copies++
		}
	}
	if copies != 2 {
		t.Fatalf("应只对 2 个目标执行 copyMessages: %v", *methods)
	}

	// 非 pin 调用恒返回零值（含路由跳过）
	if got := svc.CopyToChannels(ctx, otherBot, 100, 100, []int{11}, false); got.Total != 0 || got.OK != 0 || len(got.Skipped) != 0 {
		t.Fatalf("非 pin 调用应返回零值: %+v", got)
	}
}
