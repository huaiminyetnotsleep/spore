package binding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	tgbot "github.com/go-telegram/bot"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// newPinExistingBot 构造可控制 pinChatMessage 成败的测试 bot：copyMessages
// 恒成功（返回 501/502），pinChatMessage 对 failChat 恒失败、其余成功；
// getChatMember 返回带置顶权限的群管理员。记录 pin 调用参数。
func newPinExistingBot(t *testing.T, failChat int64) (*tgbot.Bot, *[]string) {
	t.Helper()
	var pins []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":12345,"is_bot":true,"first_name":"Spore","username":"spore_bot"}}`))
		case strings.HasSuffix(r.URL.Path, "/copyMessages"):
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"message_id":501},{"message_id":502}]}`))
		case strings.HasSuffix(r.URL.Path, "/pinChatMessage"):
			// go-telegram/bot 以 multipart/form-data 发请求体
			_ = r.ParseMultipartForm(1 << 20)
			chatID, _ := strconv.ParseInt(r.FormValue("chat_id"), 10, 64)
			msgID, _ := strconv.Atoi(r.FormValue("message_id"))
			pins = append(pins, pinCall{chat: chatID, msg: msgID}.String())
			if chatID == failChat {
				_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: not enough rights"}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case strings.HasSuffix(r.URL.Path, "/getChatMember"):
			_, _ = w.Write([]byte(`{"ok":true,"result":` + pinMemberJSON + `}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(srv.Close)
	b, err := tgbot.New("12345:test-token", tgbot.WithServerURL(srv.URL))
	if err != nil {
		t.Fatalf("构造测试 Bot 失败: %v", err)
	}
	return b, &pins
}

type pinCall struct {
	chat int64
	msg  int
}

func (p pinCall) String() string {
	b, _ := json.Marshal(p)
	return string(b)
}

// TestCopyToChannelsPersistsCopyCoords 覆盖副本坐标落库：requestID 非 0 时
// 每个发送成功的绑定落一条 channel_copy（组首坐标，受理 bot 归属）；
// requestID 为 0 时跳过。
func TestCopyToChannelsPersistsCopyCoords(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	if _, err := s.UpsertChannelBinding(ctx, store.ChannelBinding{
		ChannelID: testChannelD, UserID: 100, Username: "mychan", Title: "我的频道", BoundVia: store.BoundViaBot,
	}); err != nil {
		t.Fatalf("预置绑定失败: %v", err)
	}
	req, err := s.CreateRequest(ctx, store.Request{UserID: 100, ChannelKey: "src", MessageID: 1})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}

	b, _ := newPinExistingBot(t, 0)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})

	svc.CopyToChannels(ctx, req.ID, testBotID, 100, 100, []int{11, 12}, false)
	copies, err := s.ListChannelCopiesByRequest(ctx, req.ID)
	if err != nil || len(copies) != 1 {
		t.Fatalf("应落一条频道副本坐标: %+v err=%v", copies, err)
	}
	if copies[0].ChatID != testChannelD || copies[0].MessageID != 501 ||
		copies[0].BotID != testBotID || copies[0].Kind != store.SentKindChannelCopy {
		t.Fatalf("副本坐标不符: %+v", copies[0])
	}

	// requestID=0：无持久化任务不落锚点
	svc.CopyToChannels(ctx, 0, testBotID, 100, 100, []int{11}, false)
	if copies, _ = s.ListChannelCopiesByRequest(ctx, req.ID); len(copies) != 1 {
		t.Fatalf("requestID=0 不应追加坐标: %+v", copies)
	}
}

// TestPinExistingCopies 覆盖事后补置顶：两个副本目标（一个仍绑定带标题、
// 一个已解绑退化为数字 ID、其副本 bot 已下线回退主 bot），全部置顶成功；
// 请求行置 pin=1 并回写 2/2。
func TestPinExistingCopies(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	if _, err := s.UpsertChannelBinding(ctx, store.ChannelBinding{
		ChannelID: testChannelD, UserID: 100, Username: "mychan", Title: "我的频道", BoundVia: store.BoundViaBot,
	}); err != nil {
		t.Fatalf("预置绑定失败: %v", err)
	}
	req, err := s.CreateRequest(ctx, store.Request{UserID: 100, ChannelKey: "src", MessageID: 2})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	unbound := int64(-100999000)
	if err := s.InsertSentMessages(ctx, []store.SentMessage{
		{RequestID: req.ID, BotID: testBotID, ChatID: testChannelD, MessageID: 501, Kind: store.SentKindChannelCopy},
		// 已解绑目标 + 副本 bot 已移出池（回退主 bot）
		{RequestID: req.ID, BotID: 88888, ChatID: unbound, MessageID: 777, Kind: store.SentKindChannelCopy},
		// 非副本坐标不参与
		{RequestID: req.ID, BotID: testBotID, ChatID: 100, MessageID: 101, Kind: store.SentKindMedia},
	}); err != nil {
		t.Fatalf("预置副本坐标失败: %v", err)
	}

	b, pins := newPinExistingBot(t, 0)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})

	outcome, found, err := svc.PinExistingCopies(ctx, 100, req.ID)
	if err != nil || !found {
		t.Fatalf("有副本坐标应执行补置顶: found=%v err=%v", found, err)
	}
	if outcome.OK != 2 || outcome.Total != 2 {
		t.Fatalf("补置顶结果应为 2/2，得到 %d/%d", outcome.OK, outcome.Total)
	}
	if len(outcome.Targets) != 2 || outcome.Targets[0].Label != "我的频道" ||
		!outcome.Targets[0].Pinned || outcome.Targets[1].Label != "-100999000" || !outcome.Targets[1].Pinned {
		t.Fatalf("目标与标签不符: %+v", outcome.Targets)
	}
	joined := strings.Join(*pins, ",")
	if !strings.Contains(joined, pinCall{chat: testChannelD, msg: 501}.String()) ||
		!strings.Contains(joined, pinCall{chat: unbound, msg: 777}.String()) {
		t.Fatalf("应按组首坐标逐目标 pinChatMessage: %v", *pins)
	}
	got, err := s.GetRequest(ctx, req.ID)
	if err != nil || !got.Pin || got.PinOK != 2 || got.PinTotal != 2 {
		t.Fatalf("补置顶后请求应回写 2/2 且 pin=1: %+v err=%v", got, err)
	}
}

// TestPinExistingCopiesNoRows：无副本坐标 found=false，不调 pinChatMessage。
func TestPinExistingCopiesNoRows(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	req, err := s.CreateRequest(ctx, store.Request{UserID: 100, ChannelKey: "src", MessageID: 3})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	b, pins := newPinExistingBot(t, 0)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})

	outcome, found, err := svc.PinExistingCopies(ctx, 100, req.ID)
	if err != nil || found || outcome.Total != 0 {
		t.Fatalf("无副本应 found=false 且零值: found=%v outcome=%+v err=%v", found, outcome, err)
	}
	if len(*pins) != 0 {
		t.Fatalf("无副本不应调用 pinChatMessage: %v", *pins)
	}
}

// TestPinExistingCopiesPartialFailure：单目标置顶失败计入 1/2，失败目标
// 保留在 Targets（Pinned=false）供文案点名。
func TestPinExistingCopiesPartialFailure(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	mustUser(t, s, 100)
	if _, err := s.UpsertChannelBinding(ctx, store.ChannelBinding{
		ChannelID: testChannelD, UserID: 100, Username: "mychan", Title: "我的频道", BoundVia: store.BoundViaBot,
	}); err != nil {
		t.Fatalf("预置绑定失败: %v", err)
	}
	req, err := s.CreateRequest(ctx, store.Request{UserID: 100, ChannelKey: "src", MessageID: 4})
	if err != nil {
		t.Fatalf("创建请求失败: %v", err)
	}
	failing := int64(-100555000)
	if err := s.InsertSentMessages(ctx, []store.SentMessage{
		{RequestID: req.ID, BotID: testBotID, ChatID: testChannelD, MessageID: 501, Kind: store.SentKindChannelCopy},
		{RequestID: req.ID, BotID: testBotID, ChatID: failing, MessageID: 601, Kind: store.SentKindChannelCopy},
	}); err != nil {
		t.Fatalf("预置副本坐标失败: %v", err)
	}

	b, _ := newPinExistingBot(t, failing)
	svc, _ := New(Options{Store: s, Log: testLog()})
	svc.SetBots([]*tgbot.Bot{b})

	outcome, found, err := svc.PinExistingCopies(ctx, 100, req.ID)
	if err != nil || !found {
		t.Fatalf("补置顶应执行: found=%v err=%v", found, err)
	}
	if outcome.OK != 1 || outcome.Total != 2 {
		t.Fatalf("部分失败应为 1/2，得到 %d/%d", outcome.OK, outcome.Total)
	}
	if !outcome.Targets[0].Pinned || outcome.Targets[0].Label != "我的频道" ||
		outcome.Targets[1].Pinned || outcome.Targets[1].Label != "-100555000" {
		t.Fatalf("失败/成功目标应分开标记: %+v", outcome.Targets)
	}
	got, _ := s.GetRequest(ctx, req.ID)
	if got.PinOK != 1 || got.PinTotal != 2 {
		t.Fatalf("结果回写应为 1/2: %+v", got)
	}
}
