package binding

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tgbot "github.com/go-telegram/bot"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// pinMemberJSON 是带置顶权限的超级群组管理员。
const pinMemberJSON = `{"status":"administrator","user":{"id":12345,"is_bot":true},"can_pin_messages":true}`

// noPinMemberJSON 是只有发帖权限（无置顶权限）的群管理员：can_post_messages
// 是频道作用域权限，群管理员即使为 true 也不能用于置顶判定。
const noPinMemberJSON = `{"status":"administrator","user":{"id":12345,"is_bot":true},"can_post_messages":true}`

// editMemberJSON 是带「编辑消息」权限的频道管理员（频道置顶权限归属它）。
const editMemberJSON = `{"status":"administrator","user":{"id":12345,"is_bot":true},"can_post_messages":true,"can_edit_messages":true}`

const supergroupChatJSON = `{"id":-1001234567890,"type":"supergroup","title":"我的群","username":"mygroup"}`

// TestBindSupergroup 覆盖超级群组绑定的三条路径：带置顶权限可绑定；
// 缺置顶权限拒绝；话题群拒绝。
func TestBindSupergroup(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)

	t.Run("管理员带置顶权限可绑定", func(t *testing.T) {
		b, _ := newTestBot(t, supergroupChatJSON, pinMemberJSON, http.StatusOK)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{b})
		bound, err := svc.Bind(ctx, BindInput{UserID: 100, Target: "@mygroup", Via: store.BoundViaBot})
		if err != nil {
			t.Fatalf("带置顶权限的超级群组应可绑定: %v", err)
		}
		if bound.ChannelID != testChannelD {
			t.Fatalf("绑定 ID 不符: %+v", bound)
		}
	})

	t.Run("缺置顶权限拒绝", func(t *testing.T) {
		b, _ := newTestBot(t, supergroupChatJSON, noPinMemberJSON, http.StatusOK)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{b})
		_, err := svc.Bind(ctx, BindInput{UserID: 200, Target: "@mygroup", Via: store.BoundViaBot})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeChannelNotPostable {
			t.Fatalf("缺置顶权限应返回 CHANNEL_NOT_POSTABLE，得到 %v", err)
		}
	})

	t.Run("话题群拒绝", func(t *testing.T) {
		forumChatJSON := `{"id":-1001234567890,"type":"supergroup","title":"话题群","username":"forum","is_forum":true}`
		b, _ := newTestBot(t, forumChatJSON, pinMemberJSON, http.StatusOK)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{b})
		_, err := svc.Bind(ctx, BindInput{UserID: 200, Target: "@forum", Via: store.BoundViaBot})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != apperr.CodeChannelTargetInvalid {
			t.Fatalf("话题群应返回 CHANNEL_TARGET_INVALID，得到 %v", err)
		}
	})
}

// newCopyPinBot 构造可记录 copyMessages / pinChatMessage 调用的测试 bot；
// memberJSON 控制置顶可行性软提示的判定。
func newCopyPinBot(t *testing.T, memberJSON string) (*tgbot.Bot, *[]string) {
	t.Helper()
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/getMe") {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":12345,"is_bot":true,"first_name":"Spore","username":"spore_bot"}}`))
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/copyMessages"):
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"message_id":501},{"message_id":502}]}`))
		case strings.HasSuffix(r.URL.Path, "/pinChatMessage"):
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
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

// TestCopyToChannelsPin 覆盖副本置顶链路：pin 时对副本组首（返回首条）执行
// 置顶并计数；非 pin 时不调用置顶、结果为零值。
func TestCopyToChannelsPin(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	if _, err := s.UpsertChannelBinding(ctx, store.ChannelBinding{
		ChannelID: testChannelD, UserID: 100, Username: "mychan", Title: "我的频道", BoundVia: store.BoundViaBot,
	}); err != nil {
		t.Fatalf("预置绑定失败: %v", err)
	}

	t.Run("pin 时置顶组首", func(t *testing.T) {
		b, methods := newCopyPinBot(t, pinMemberJSON)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{b})

		ok, total := svc.CopyToChannels(ctx, testBotID, 100, 100, []int{11, 12}, true)
		if ok != 1 || total != 1 {
			t.Fatalf("置顶结果应为 1/1，得到 %d/%d", ok, total)
		}
		joined := strings.Join(*methods, ",")
		if !strings.Contains(joined, "copyMessages") || !strings.Contains(joined, "pinChatMessage") {
			t.Fatalf("应依次调用 copyMessages 与 pinChatMessage: %v", *methods)
		}
	})

	t.Run("非 pin 时不置顶", func(t *testing.T) {
		b, methods := newCopyPinBot(t, pinMemberJSON)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{b})

		ok, total := svc.CopyToChannels(ctx, testBotID, 100, 100, []int{11, 12}, false)
		if ok != 0 || total != 0 {
			t.Fatalf("非 pin 调用结果应为零值，得到 %d/%d", ok, total)
		}
		for _, m := range *methods {
			if strings.HasSuffix(m, "pinChatMessage") {
				t.Fatalf("非 pin 不应调用 pinChatMessage: %v", *methods)
			}
		}
	})
}

// TestPinCapabilityHint：频道管理员缺「编辑消息」权限时返回软提示；
// 权限齐全返回空串。
func TestPinCapabilityHint(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	if _, err := s.UpsertChannelBinding(ctx, store.ChannelBinding{
		ChannelID: testChannelD, UserID: 100, Username: "mychan", Title: "我的频道", BoundVia: store.BoundViaBot,
	}); err != nil {
		t.Fatalf("预置绑定失败: %v", err)
	}

	t.Run("缺编辑消息权限返回提示", func(t *testing.T) {
		// 频道管理员：无 can_edit_messages、无 can_pin_messages（频道恒 false）
		b, _ := newCopyPinBot(t, noPinMemberJSON)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{b})
		if hint := svc.PinCapabilityHint(ctx, 100); hint == "" {
			t.Fatal("缺置顶权限的频道应返回软提示")
		}
	})

	t.Run("权限齐全返回空串", func(t *testing.T) {
		b, _ := newCopyPinBot(t, editMemberJSON)
		svc, _ := New(Options{Store: s, Log: testLog()})
		svc.SetBots([]*tgbot.Bot{b})
		if hint := svc.PinCapabilityHint(ctx, 100); hint != "" {
			t.Fatalf("权限齐全应返回空串，得到 %q", hint)
		}
	})
}
