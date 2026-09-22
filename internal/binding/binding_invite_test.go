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
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

type fakeInviteResolver struct {
	info      mtproto.InviteInfo
	checkErr  error
	joinID    int64
	joinTitle string
	already   bool
	joinErr   error
	checks    int
	joins     int
}

func (f *fakeInviteResolver) CheckInvite(context.Context, string) (mtproto.InviteInfo, error) {
	f.checks++
	return f.info, f.checkErr
}

func (f *fakeInviteResolver) JoinInvite(context.Context, string, mtproto.JoinOptions) (int64, string, bool, error) {
	f.joins++
	return f.joinID, f.joinTitle, f.already, f.joinErr
}

func inviteService(t *testing.T, resolver InviteResolver) (*Service, *store.Store) {
	t.Helper()
	s := openStore(t)
	mustUser(t, s, 100)
	bot, _ := newTestBot(t, channelChatJSON, adminMemberJSON, http.StatusOK)
	svc, err := New(Options{Store: s, Invite: resolver, Log: testLog()})
	if err != nil {
		t.Fatalf("创建服务失败: %v", err)
	}
	svc.SetBots([]*tgbot.Bot{bot})
	return svc, s
}

func TestBindInviteJoinsAndRecordsSource(t *testing.T) {
	resolver := &fakeInviteResolver{
		info:      mtproto.InviteInfo{Title: "预览标题", IsChannel: true},
		joinID:    1234567890,
		joinTitle: "我的频道",
	}
	svc, st := inviteService(t, resolver)

	bound, err := svc.Bind(context.Background(), BindInput{
		UserID: 100,
		Target: "https://t.me/+AbCdEf123",
		Via:    store.BoundViaBot,
	})
	if err != nil {
		t.Fatalf("邀请绑定失败: %v", err)
	}
	if bound.ChannelID != testChannelD {
		t.Fatalf("绑定频道 ID 应归一为 %d，得到 %d", testChannelD, bound.ChannelID)
	}
	if resolver.checks != 1 || resolver.joins != 1 {
		t.Fatalf("应预检一次并加入一次: checks=%d joins=%d", resolver.checks, resolver.joins)
	}
	recs, err := st.ListActiveJoinedChannels(context.Background())
	if err != nil || len(recs) != 1 {
		t.Fatalf("应写入一条加入留痕: recs=%+v err=%v", recs, err)
	}
	if recs[0].ChannelID != 1234567890 || recs[0].JoinedVia != store.JoinedViaBindResolve || recs[0].JoinedBy != 100 {
		t.Fatalf("绑定邀请留痕不符: %+v", recs[0])
	}
}

func TestBindInviteAlreadyJoinedSkipsJoinAndSourceOverwrite(t *testing.T) {
	resolver := &fakeInviteResolver{info: mtproto.InviteInfo{
		Title: "我的频道", IsChannel: true, AlreadyJoined: true, ChannelID: 1234567890,
	}}
	svc, st := inviteService(t, resolver)

	if _, err := svc.Bind(context.Background(), BindInput{
		UserID: 100,
		Target: "https://t.me/joinchat/AbCdEf123",
		Via:    store.BoundViaWeb,
	}); err != nil {
		t.Fatalf("已加入频道的邀请绑定失败: %v", err)
	}
	if resolver.joins != 0 {
		t.Fatalf("已是成员不应再次 JoinInvite，得到 %d 次", resolver.joins)
	}
	recs, err := st.ListActiveJoinedChannels(context.Background())
	if err != nil {
		t.Fatalf("读取加入留痕失败: %v", err)
	}
	if len(recs) != 0 {
		t.Fatalf("已是成员不应覆盖既有加入来源: %+v", recs)
	}
}

func TestBindInviteErrorClassification(t *testing.T) {
	cases := []struct {
		name     string
		resolver InviteResolver
		want     apperr.Code
	}{
		{
			name:     "解析器未接入",
			resolver: nil,
			want:     apperr.CodeChannelInviteUnresolved,
		},
		{
			name: "普通群组预检拒绝且不加入",
			resolver: &fakeInviteResolver{info: mtproto.InviteInfo{
				IsChannel: false,
			}},
			want: apperr.CodeChannelInviteInvalid,
		},
		{
			name: "失效邀请",
			resolver: &fakeInviteResolver{
				checkErr: apperr.New(apperr.CodeInvalidURL, "invalid invite"),
			},
			want: apperr.CodeChannelInviteInvalid,
		},
		{
			name: "读取账号离线",
			resolver: &fakeInviteResolver{
				checkErr: mtproto.ErrMembershipUnavailable,
			},
			want: apperr.CodeChannelInviteUnresolved,
		},
		{
			name: "频道加入需审核",
			resolver: &fakeInviteResolver{
				info:    mtproto.InviteInfo{IsChannel: true},
				joinErr: mtproto.ErrJoinRequestSent,
			},
			want: apperr.CodeChannelInviteUnresolved,
		},
		{
			name: "Telegram 限流透传",
			resolver: &fakeInviteResolver{
				checkErr: apperr.New(apperr.CodeRateLimited, "rate limited"),
			},
			want: apperr.CodeRateLimited,
		},
		{
			name: "PEER_FLOOD 透传",
			resolver: &fakeInviteResolver{
				checkErr: apperr.New(apperr.CodePeerFlood, "peer flood"),
			},
			want: apperr.CodePeerFlood,
		},
		{
			name: "网络错误透传",
			resolver: &fakeInviteResolver{
				checkErr: apperr.New(apperr.CodeNetworkError, "network"),
			},
			want: apperr.CodeNetworkError,
		},
		{
			name: "Telegram 服务端错误透传",
			resolver: &fakeInviteResolver{
				checkErr: apperr.New(apperr.CodeTelegramServer, "server"),
			},
			want: apperr.CodeTelegramServer,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := inviteService(t, tc.resolver)
			_, err := svc.Bind(context.Background(), BindInput{
				UserID: 100,
				Target: "https://t.me/+AbCdEf123",
				Via:    store.BoundViaBot,
			})
			if got := apperr.From(err).Code; got != tc.want {
				t.Fatalf("错误码应为 %s，得到 %s（err=%v）", tc.want, got, err)
			}
			if f, ok := tc.resolver.(*fakeInviteResolver); ok && !f.info.IsChannel && f.joins != 0 {
				t.Fatalf("普通群组预检失败后不应加入，得到 %d 次", f.joins)
			}
		})
	}
}

func newMetadataBot(t *testing.T, id int64, getChatOK bool) *tgbot.Bot {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":` + itoa(id) + `,"is_bot":true,"first_name":"Spore"}}`))
		case strings.HasSuffix(r.URL.Path, "/getChat"):
			if !getChatOK {
				_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":` + channelChatJSON + `}`))
		case strings.HasSuffix(r.URL.Path, "/getChatMember"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"administrator","user":{"id":` + itoa(id) + `,"is_bot":true},"can_post_messages":true}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	bot, err := tgbot.New(itoa(id)+":test-token", tgbot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatalf("构造测试 Bot 失败: %v", err)
	}
	return bot
}

func TestBindUsesReceivingBotForMetadata(t *testing.T) {
	st := openStore(t)
	mustUser(t, st, 100)
	primary := newMetadataBot(t, 12345, false)
	receiving := newMetadataBot(t, 67890, true)
	svc, _ := New(Options{Store: st, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{primary, receiving})

	bound, _, err := svc.BindWithAdvice(context.Background(), BindInput{
		UserID: 100,
		Target: "@mychan",
		Via:    store.BoundViaBot,
		BotID:  67890,
	})
	if err != nil {
		t.Fatalf("应使用接收 bot 查询私有频道元数据: %v", err)
	}
	if bound.BotID != 67890 {
		t.Fatalf("路由 bot 应为 67890，得到 %d", bound.BotID)
	}
}

func TestClassifyInviteResolveErrorUnknownPreserved(t *testing.T) {
	err := classifyInviteResolveError(errors.New("boom"))
	if got := apperr.From(err).Code; got != apperr.CodeInternal {
		t.Fatalf("未知错误应保持 INTERNAL_ERROR，得到 %s", got)
	}
}
