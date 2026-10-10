package watch

// 监听源探活测试：连续失败阈值、健康即恢复、停用/移除源剪枝、按源路由
// 受理 bot、事件出口注入。

import (
	"context"
	"errors"
	"sync"
	"testing"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// fakeHealthBot 可编程的探活 Bot 客户端：failChats 中的 chat_id 探测失败。
type fakeHealthBot struct {
	id        int64
	mu        sync.Mutex
	failChats map[int64]bool
	calls     []int64
}

func (f *fakeHealthBot) ID() int64 { return f.id }

func (f *fakeHealthBot) GetChat(_ context.Context, params *tgbot.GetChatParams) (*models.ChatFullInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	chatID := params.ChatID.(int64) // 生产只传 -100 数字 ID；断言失败即测试缺陷
	f.calls = append(f.calls, chatID)
	if f.failChats[chatID] {
		return nil, errors.New("Forbidden: bot is not a member of the channel chat")
	}
	return &models.ChatFullInfo{}, nil
}

func (f *fakeHealthBot) GetChatMember(context.Context, *tgbot.GetChatMemberParams) (*models.ChatMember, error) {
	return nil, errors.New("not implemented")
}

// setFakeClients 直接注入假客户端（绕过 SetBots 的 *tgbot.Bot 具体类型）。
func setFakeClients(s *Service, clients []botClient) {
	s.botMu.Lock()
	defer s.botMu.Unlock()
	s.botClients = clients
}

// recordingHealthEvents 记录探活事件上报。
type recordingHealthEvents struct {
	mu          sync.Mutex
	unavailable [][]string // 每次 WatchSourcesUnavailable 的 titles
	recovered   int        // WatchSourcesRecovered 次数
}

func (r *recordingHealthEvents) WatchSourcesUnavailable(_ context.Context, titles []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unavailable = append(r.unavailable, titles)
}

func (r *recordingHealthEvents) WatchSourcesRecovered(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recovered++
}

func (r *recordingHealthEvents) snapshot() ([][]string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.unavailable, r.recovered
}

// seedSource 落一条 approved+enabled 的监听源；停用场景落库后经
// SetWatchSourceEnabled 显式关闭（WatchSource 零值 Enabled=false 无法与
// "未填"区分，不做条件默认）。
func seedSource(t *testing.T, st *store.Store, src store.WatchSource) {
	t.Helper()
	src.Status = store.WatchApproved
	src.Enabled = true
	if _, err := st.UpsertWatchSource(context.Background(), src); err != nil {
		t.Fatalf("落监听源失败: %v", err)
	}
}

// 连续两轮失败才判不可用并上报事件；恢复可达后下一轮自动解决事件。
func TestCheckSourcesFailureThresholdAndRecovery(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()
	seedSource(t, st, store.WatchSource{ChannelID: -1001, Title: "源A", BotID: 100})
	bot := &fakeHealthBot{id: 100, failChats: map[int64]bool{-1001: true}}
	setFakeClients(svc, []botClient{bot})
	ev := &recordingHealthEvents{}
	svc.SetEvents(ev)

	svc.CheckSources(ctx)
	if unavailable, recovered := ev.snapshot(); len(unavailable) != 0 || recovered != 0 {
		t.Fatalf("单次失败不应告警: %v %d", unavailable, recovered)
	}

	svc.CheckSources(ctx)
	unavailable, _ := ev.snapshot()
	if len(unavailable) != 1 || len(unavailable[0]) != 1 || unavailable[0][0] != "源A" {
		t.Fatalf("连续两轮失败应上报不可用源: %v", unavailable)
	}

	// 源恢复可达：下一轮自动解决事件
	bot.mu.Lock()
	delete(bot.failChats, -1001)
	bot.mu.Unlock()
	svc.CheckSources(ctx)
	if _, recovered := ev.snapshot(); recovered != 1 {
		t.Fatal("恢复可达应自动解决事件")
	}
}

// 一开始就健康：不产生任何事件。
func TestCheckSourcesHealthySilent(t *testing.T) {
	svc, st := newService(t)
	seedSource(t, st, store.WatchSource{ChannelID: -1002, Username: "healthy", BotID: 100})
	setFakeClients(svc, []botClient{&fakeHealthBot{id: 100}})
	ev := &recordingHealthEvents{}
	svc.SetEvents(ev)

	svc.CheckSources(context.Background())
	if unavailable, recovered := ev.snapshot(); len(unavailable) != 0 || recovered != 0 {
		t.Fatalf("健康源不应产生事件: %v %d", unavailable, recovered)
	}
}

// 停用源不参与探活；已判不可用的源被移除/停用后剪枝出集合并解决事件。
func TestCheckSourcesSkipsAndPrunesInactiveSources(t *testing.T) {
	svc, st := newService(t)
	ctx := context.Background()
	seedSource(t, st, store.WatchSource{ChannelID: -1003, Title: "停用源"})
	seedSource(t, st, store.WatchSource{ChannelID: -1004, Title: "待删源"})
	if _, err := st.SetWatchSourceEnabled(ctx, -1003, false); err != nil {
		t.Fatalf("停用监听源失败: %v", err)
	}
	bot := &fakeHealthBot{id: 100, failChats: map[int64]bool{-1003: true, -1004: true}}
	setFakeClients(svc, []botClient{bot})
	ev := &recordingHealthEvents{}
	svc.SetEvents(ev)

	svc.CheckSources(ctx) // 停用源不计失败
	svc.CheckSources(ctx) // 第二轮：仅待删源达到阈值
	unavailable, _ := ev.snapshot()
	if len(unavailable) != 1 || len(unavailable[0]) != 1 || unavailable[0][0] != "待删源" {
		t.Fatalf("停用源不应进入不可用集合: %v", unavailable)
	}

	// 移除待删源后：集合剪枝，事件解决
	if _, err := st.DeleteWatchSource(ctx, -1004); err != nil {
		t.Fatalf("移除监听源失败: %v", err)
	}
	svc.CheckSources(ctx)
	if _, recovered := ev.snapshot(); recovered != 1 {
		t.Fatal("不可用源被移除后应剪枝并解决事件")
	}
}

// 探测走源的受理 bot（BotID 匹配），Web 添加的源（BotID=0）走主 bot：
// 仅主 bot 探测失败时，只有 Web 源被判不可用。
func TestCheckSourcesRoutesByBotID(t *testing.T) {
	svc, st := newService(t)
	seedSource(t, st, store.WatchSource{ChannelID: -1005, Title: "Web源", BotID: 0})
	seedSource(t, st, store.WatchSource{ChannelID: -1006, Title: "用户源", BotID: 200})
	primary := &fakeHealthBot{id: 100, failChats: map[int64]bool{-1005: true, -1006: true}}
	accepted := &fakeHealthBot{id: 200}
	setFakeClients(svc, []botClient{primary, accepted})
	ev := &recordingHealthEvents{}
	svc.SetEvents(ev)

	for i := 0; i < healthFailThreshold; i++ {
		svc.CheckSources(context.Background())
	}
	unavailable, _ := ev.snapshot()
	if len(unavailable) == 0 || len(unavailable[len(unavailable)-1]) != 1 || unavailable[len(unavailable)-1][0] != "Web源" {
		t.Fatalf("仅 Web 源应不可用（用户源走受理 bot）: %v", unavailable)
	}
	if len(accepted.calls) == 0 {
		t.Fatal("用户源应由受理 bot 探测")
	}
}

// Bot 未就绪（无客户端）时整轮跳过，不改变既有判定。
func TestCheckSourcesSkipsWithoutBots(t *testing.T) {
	svc, st := newService(t)
	seedSource(t, st, store.WatchSource{ChannelID: -1007, Title: "源"})
	ev := &recordingHealthEvents{}
	svc.SetEvents(ev)

	svc.CheckSources(context.Background())
	if unavailable, _ := ev.snapshot(); len(unavailable) != 0 {
		t.Fatalf("无 Bot 客户端不应产生事件: %v", unavailable)
	}
}

// 未注入事件出口时不 panic（nil 跳过上报）。
func TestCheckSourcesWithoutEventsSink(t *testing.T) {
	svc, st := newService(t)
	seedSource(t, st, store.WatchSource{ChannelID: -1008, Title: "源"})
	setFakeClients(svc, []botClient{&fakeHealthBot{id: 100, failChats: map[int64]bool{-1008: true}}})

	for i := 0; i < healthFailThreshold+1; i++ {
		svc.CheckSources(context.Background()) // 不 panic 即通过
	}
}
