package binding

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	tgbot "github.com/go-telegram/bot"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func testLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), testLog())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

const (
	testBotID    = int64(12345)
	testChannelD = int64(-1001234567890)
)

// newTestBot 用本地 httptest 服务器模拟 Bot API（getChat / getChatMember），
// 构造可注入 Service 的 *tgbot.Bot。
func newTestBot(t *testing.T, chatJSON, memberJSON string, status int) (*tgbot.Bot, *[]string) {
	t.Helper()
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":12345,"is_bot":true,"first_name":"Spore","username":"spore_bot"}}`))
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/getChat"):
			_, _ = w.Write([]byte(`{"ok":true,"result":` + chatJSON + `}`))
		case strings.HasSuffix(r.URL.Path, "/getChatMember"):
			_, _ = w.Write([]byte(`{"ok":true,"result":` + memberJSON + `}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	b, err := tgbot.New("12345:test-token", tgbot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatalf("构造测试 Bot 失败: %v", err)
	}
	return b, &methods
}

const adminMemberJSON = `{"status":"administrator","user":{"id":12345,"is_bot":true},"can_post_messages":true}`
const channelChatJSON = `{"id":-1001234567890,"type":"channel","title":"我的频道","username":"mychan"}`

func mustUser(t *testing.T, s *store.Store, id int64) {
	t.Helper()
	if _, err := s.CreateUser(context.Background(), store.User{ID: id, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
}

func TestParseChannelTarget(t *testing.T) {
	cases := []struct {
		in        string
		wantID    int64
		wantUname string
		wantErr   bool
	}{
		{in: "@mychan", wantUname: "mychan"},
		{in: "mychan", wantUname: "mychan"},
		{in: "https://t.me/mychan", wantUname: "mychan"},
		{in: "https://t.me/mychan/", wantUname: "mychan"},
		{in: "t.me/c/1234567890/7", wantID: -1001234567890},
		{in: "https://t.me/c/1234567890/7?single", wantID: -1001234567890},
		{in: "-1001234567890", wantID: -1001234567890},
		{in: "1234567890", wantID: -1001234567890}, // 正整数按频道内部 ID 归一
		{in: "", wantErr: true},
		{in: "@ab", wantErr: true},           // 用户名过短
		{in: "t.me/mychan/7", wantErr: true}, // 消息链接
		{in: "t.me/c/abc/7", wantErr: true},  // 私有链接 ID 非法
		{in: "-123", wantErr: true},          // 非 -100 形态的短负数
		{in: "123", wantErr: true},           // 过短正数不是频道 ID
	}
	for _, c := range cases {
		got, err := ParseChannelTarget(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q 应解析失败，得到 %+v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q 解析失败: %v", c.in, err)
			continue
		}
		if got.ChannelID != c.wantID || got.Username != c.wantUname {
			t.Errorf("%q → (%d,%q)，期望 (%d,%q)", c.in, got.ChannelID, got.Username, c.wantID, c.wantUname)
		}
	}
}

func TestBindSuccess(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	b, _ := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, err := New(Options{Store: s, Log: testLog()})
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	svc.SetBots([]*tgbot.Bot{b})

	bound, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaBot})
	if err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	if bound.ChannelID != testChannelD || bound.UserID != 100 || bound.Title != "我的频道" || bound.Username != "mychan" {
		t.Fatalf("绑定记录不符: %+v", bound)
	}
	// 重复绑定幂等
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "-1001234567890", Via: store.BoundViaWeb}); err != nil {
		t.Fatalf("重复绑定应幂等: %v", err)
	}
	mine, err := svc.ListByUser(ctx, 100)
	if err != nil || len(mine) != 1 {
		t.Fatalf("应只有一条绑定: %v %v", mine, err)
	}
	if mine[0].BoundVia != store.BoundViaWeb {
		t.Fatalf("重复绑定应刷新来源: %+v", mine[0])
	}
}

func TestBindOwnershipConflict(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	mustUser(t, s, 200)

	b, _ := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})

	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaBot}); err != nil {
		t.Fatalf("首次绑定失败: %v", err)
	}
	_, err := svc.Bind(ctx, BindInput{UserID: 200, Target: "@mychan", Via: store.BoundViaBot})
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != apperr.CodeChannelAlreadyBound {
		t.Fatalf("他人绑定应返回 CHANNEL_ALREADY_BOUND，得到 %v", err)
	}
}

func TestBindVerificationFailures(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	t.Run("机器人不是管理员", func(t *testing.T) {
		b, _ := newTestBot(t, channelChatJSON,
			`{"status":"member","user":{"id":12345,"is_bot":true}}`, http.StatusOK)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{b})
		_, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaBot})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeChannelNotPostable {
			t.Fatalf("普通成员应返回 CHANNEL_NOT_POSTABLE，得到 %v", err)
		}
	})

	t.Run("管理员但无发言权限", func(t *testing.T) {
		b, _ := newTestBot(t, channelChatJSON,
			`{"status":"administrator","user":{"id":12345,"is_bot":true},"can_post_messages":false}`, http.StatusOK)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{b})
		_, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaBot})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeChannelNotPostable {
			t.Fatalf("无发言权限应返回 CHANNEL_NOT_POSTABLE，得到 %v", err)
		}
	})

	t.Run("普通群仍拒绝（仅频道/超级群组可绑定）", func(t *testing.T) {
		b, _ := newTestBot(t, `{"id":-1001,"type":"group","title":"普通群"}`, adminMemberJSON, http.StatusOK)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{b})
		_, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "-1001", Via: store.BoundViaBot})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeChannelTargetInvalid {
			t.Fatalf("普通群应返回 CHANNEL_TARGET_INVALID，得到 %v", err)
		}
	})

	t.Run("目标标识非法（不触网）", func(t *testing.T) {
		svc, _ := New(Options{Store: s, Log: testLog()})
		_, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "!!", Via: store.BoundViaBot})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeChannelTargetInvalid {
			t.Fatalf("非法标识应返回 CHANNEL_TARGET_INVALID，得到 %v", err)
		}
	})
}

func TestUnbindScope(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	mustUser(t, s, 200)

	b, _ := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})

	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaBot}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	// 他人按用户名解绑：找不到（不暴露他人绑定）
	if _, err := svc.Unbind(ctx, 200, "@mychan", false); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("他人解绑应 ErrNotFound，得到 %v", err)
	}
	// 本人按用户名解绑成功
	removed, err := svc.Unbind(ctx, 100, "t.me/mychan", false)
	if err != nil || removed.ChannelID != testChannelD {
		t.Fatalf("本人解绑失败: %v %+v", err, removed)
	}
	// 管理端 anyOwner 解绑任意绑定
	if _, err := svc.Bind(ctx, BindInput{UserID: 200, Target: "@mychan", Via: store.BoundViaBot}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	if _, err := svc.Unbind(ctx, 0, "-1001234567890", true); err != nil {
		t.Fatalf("管理端解绑失败: %v", err)
	}
}

func TestListAllWithUser(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	b, _ := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaWeb}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}
	rows, err := svc.ListAll(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("全量列表失败: %v %v", rows, err)
	}
	if rows[0].UserID != 100 || rows[0].ChannelID != testChannelD {
		t.Fatalf("列表行不符: %+v", rows[0])
	}
}

func TestCopyToChannelsNoBotIsNoop(t *testing.T) {
	s := openStore(t)
	svc, _ := New(Options{Store: s, Log: testLog()})
	// bot 未注入 & 无绑定：不得 panic
	svc.CopyToChannels(context.Background(), 0, 1, 1, []int{1, 2}, false)
}

const privateChatJSON = `{"id":-1009876543210,"type":"channel","title":"私有频道"}`

// chatJSONWith 构造指定频道 ID/用户名/标题的 getChat 返回 JSON。
func chatJSONWith(id int64, username, title string) string {
	return fmt.Sprintf(`{"id":%d,"type":"channel","title":%q,"username":%q}`,
		id, title, username)
}

func TestChannelURL(t *testing.T) {
	if got := ChannelURL(store.ChannelBinding{ChannelID: testChannelD, Username: "mychan"}); got != "https://t.me/mychan" {
		t.Fatalf("公开频道链接不符: %s", got)
	}
	if got := ChannelURL(store.ChannelBinding{ChannelID: testChannelD}); got != "https://t.me/c/1234567890/1" {
		t.Fatalf("私有频道链接不符: %s", got)
	}
}

func TestRefreshChannelBecomesPrivate(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	b, _ := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaBot}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	// 频道转为私有：getChat 返回同 ID 但无 username
	svc.SetBots([]*tgbot.Bot{mustBot(t, chatJSONWith(testChannelD, "", "我的频道"))})
	rows, err := svc.ListByUser(ctx, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("列表失败: %v %v", rows, err)
	}
	if rows[0].Username != "" {
		t.Fatalf("转私有后 username 应被清空: %+v", rows[0])
	}
	if got, _ := s.GetChannelBinding(ctx, testChannelD); got.Username != "" {
		t.Fatalf("转私有后应持久化清空: %+v", got)
	}
	links, err := svc.PublicChannelLinks(ctx, 100)
	if err != nil || len(links) != 1 {
		t.Fatalf("构建链接失败: %v %v", links, err)
	}
	if links[0].URL != "https://t.me/c/1234567890/1" || links[0].Label != "我的频道" {
		t.Fatalf("转私有后脚注链接不符: %+v", links[0])
	}
}

func TestRefreshChannelBecomesPublic(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	b, _ := newTestBot(t, privateChatJSON, adminMemberJSON, http.StatusOK)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "-1009876543210", Via: store.BoundViaBot}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	// 频道转为公开：getChat 返回同 ID 但带新 username
	svc.SetBots([]*tgbot.Bot{mustBot(t, chatJSONWith(-1009876543210, "nowpublic", "私有频道"))})
	rows, err := svc.ListByUser(ctx, 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("列表失败: %v %v", rows, err)
	}
	if rows[0].Username != "nowpublic" {
		t.Fatalf("转公开后应写入新 username: %+v", rows[0])
	}
	links, err := svc.PublicChannelLinks(ctx, 100)
	if err != nil || len(links) != 1 {
		t.Fatalf("构建链接失败: %v %v", links, err)
	}
	if links[0].URL != "https://t.me/nowpublic" || links[0].Label != "@nowpublic" {
		t.Fatalf("转公开后脚注链接不符: %+v", links[0])
	}
}

func TestRefreshChannelGoneKeepsSnapshot(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	b, _ := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaBot}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	// GetChat 失败（Bot 被移出频道等）：沿用旧快照，不报错
	svc.SetBots([]*tgbot.Bot{mustBotWithStatus(t, chatJSONWith(testChannelD, "", ""), http.StatusBadRequest)})
	rows, err := svc.ListByUser(ctx, 100)
	if err != nil || len(rows) != 1 || rows[0].Username != "mychan" {
		t.Fatalf("GetChat 失败应沿用快照: %v %v", rows, err)
	}
}

func TestRefreshTTLThrottle(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	b, methods := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaBot}); err != nil {
		t.Fatalf("绑定失败: %v", err)
	}

	countOf := func(paths []string) int {
		n := 0
		for _, p := range paths {
			if strings.HasSuffix(p, "/getChat") {
				n++
			}
		}
		return n
	}
	base := countOf(*methods) // Bind 校验阶段的 getChat 基线

	// 发送路径（force=false）：TTL 内两次 PublicChannelLinks 只触发一次 getChat
	for i := 0; i < 2; i++ {
		if _, err := svc.PublicChannelLinks(ctx, 100); err != nil {
			t.Fatalf("构建链接失败: %v", err)
		}
	}
	if n := countOf(*methods); n != base+1 {
		t.Fatalf("TTL 内应只刷新一次，getChat 次数 = %d（基线 %d）", n, base)
	}

	// 交互路径（/channels → ListByUser 强制刷新）：不受 TTL 限制
	if _, err := svc.ListByUser(ctx, 100); err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if n := countOf(*methods); n != base+2 {
		t.Fatalf("强制刷新应绕过 TTL，getChat 次数 = %d（基线 %d）", n, base)
	}
}

// mustBot / mustBotWithStatus 是 newTestBot 的便捷封装，只关注 getChat 返回。
func mustBot(t *testing.T, chatJSON string) *tgbot.Bot {
	t.Helper()
	b, _ := newTestBot(t, chatJSON, adminMemberJSON, http.StatusOK)
	return b
}

func mustBotWithStatus(t *testing.T, chatJSON string, status int) *tgbot.Bot {
	t.Helper()
	b, _ := newTestBot(t, chatJSON, adminMemberJSON, status)
	return b
}

func TestPublicChannelLinks(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	// owner 默认上限 3，可绑公开 + 私有两个频道
	if err := s.SetOwner(ctx, 100, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}

	b, _ := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})
	// 公开频道绑定
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaBot}); err != nil {
		t.Fatalf("绑定公开频道失败: %v", err)
	}
	// 私有频道绑定（无 username，有 title）：换对应频道的测试 bot
	privBot, _ := newTestBot(t, privateChatJSON, adminMemberJSON, http.StatusOK)
	svc.SetBots([]*tgbot.Bot{privBot})
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "-1009876543210", Via: store.BoundViaBot}); err != nil {
		t.Fatalf("绑定私有频道失败: %v", err)
	}

	links, err := svc.PublicChannelLinks(ctx, 100)
	if err != nil {
		t.Fatalf("构建链接失败: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("应有 2 条链接，得到 %d", len(links))
	}
	// 两次绑定可能落在同一毫秒：created_at 平局时按 channel_id 排序
	//（私有 -100… 为负在前），链接顺序不构成契约，按集合断言。
	got := map[string]string{}
	for _, l := range links {
		got[l.Label] = l.URL
	}
	if got["@mychan"] != "https://t.me/mychan" {
		t.Fatalf("公开频道链接不符: %+v", links)
	}
	if got["私有频道"] != "https://t.me/c/9876543210/1" {
		t.Fatalf("私有频道链接不符: %+v", links)
	}
}

func TestBindLimit(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100) // 普通用户：默认上限 1

	b, _ := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})

	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "-1001234567890", Via: store.BoundViaBot}); err != nil {
		t.Fatalf("首次绑定失败: %v", err)
	}
	// 同一频道重绑：幂等不受限
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mychan", Via: store.BoundViaWeb}); err != nil {
		t.Fatalf("幂等重绑不受上限约束: %v", err)
	}

	// 换一个频道（把测试机器人的 getChat 换成另一个 ID）
	b2, _ := newTestBot(t, `{"id":-1009999999999,"type":"channel","title":"另一个"}`, adminMemberJSON, http.StatusOK)
	svc.SetBots([]*tgbot.Bot{b2})
	_, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "-1009999999999", Via: store.BoundViaBot})
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != apperr.CodeChannelBindLimit {
		t.Fatalf("超限应返回 CHANNEL_BIND_LIMIT，得到 %v", err)
	}
}

func TestBindLimitOwnerAndCustom(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	if _, err := s.CreateUser(ctx, store.User{ID: 100, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	if err := s.SetOwner(ctx, 100, true); err != nil {
		t.Fatalf("设置 owner 失败: %v", err)
	}

	b, _ := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})

	// owner 默认上限 3
	ids := []string{"-1001234567890", "-1009999999999", "-1008888888888"}
	for _, id := range ids {
		b2, _ := newTestBot(t,
			`{"id":`+id+`,"type":"channel","title":"t`+id+`"}`, adminMemberJSON, http.StatusOK)
		svc.SetBots([]*tgbot.Bot{b2})
		if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: id, Via: store.BoundViaBot}); err != nil {
			t.Fatalf("owner 绑定 %s 应成功: %v", id, err)
		}
	}
	b3, _ := newTestBot(t, `{"id":-1007777777777,"type":"channel","title":"第四个"}`, adminMemberJSON, http.StatusOK)
	svc.SetBots([]*tgbot.Bot{b3})
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "-1007777777777", Via: store.BoundViaBot}); err == nil {
		t.Fatal("owner 第 4 个绑定应被拒绝")
	}

	// 用户级配置：把该用户上限改为 2（原 3 个绑定已超限，但重绑已有频道
	// 幂等放行；换成新的第 4 个频道才被拒）
	if err := s.UpdateUserBindLimit(ctx, 100, 2); err != nil {
		t.Fatalf("更新用户绑定上限失败: %v", err)
	}
	rebind, _ := newTestBot(t,
		`{"id":-1001234567890,"type":"channel","title":"t-1001234567890"}`, adminMemberJSON, http.StatusOK)
	svc.SetBots([]*tgbot.Bot{rebind})
	if _, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "-1001234567890", Via: store.BoundViaWeb}); err != nil {
		t.Fatalf("幂等重绑不受新上限影响: %v", err)
	}
}

func TestParseChannelTargetMalformedPrivateLink(t *testing.T) {
	// 畸形私有链接应返回受控错误而不是 panic（splitTMe 越界回归）
	for _, input := range []string{"t.me/c", "https://t.me/c", "t.me/c/"} {
		if _, err := ParseChannelTarget(input); err == nil {
			t.Fatalf("%q 应解析失败", input)
		}
	}
}

func TestParseChannelTargetInviteLinkNotAChannelTarget(t *testing.T) {
	// 邀请链接不是频道目标；/watch 的邀请分流在服务层先行判断
	for _, input := range []string{
		"https://t.me/+AbCdEfGh12345678",
		"https://t.me/joinchat/AbCdEfGh12345678",
	} {
		if _, err := ParseChannelTarget(input); err == nil {
			t.Fatalf("邀请链接 %q 不应被当作频道目标解析成功", input)
		}
	}
}

func TestBotChannelIDConversion(t *testing.T) {
	cases := map[int64]int64{1234567890: -1001234567890, 1: -1001, 999999999999: -100999999999999}
	for in, want := range cases {
		if got := BotChannelID(in); got != want {
			t.Errorf("BotChannelID(%d) = %d, 期望 %d", in, got, want)
		}
	}
}
