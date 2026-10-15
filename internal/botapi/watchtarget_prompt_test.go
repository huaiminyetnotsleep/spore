package botapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/watch"
)

// fakeWatchForward 记录调用并按配置返回（/watchtarget 与选择面板路径）。
type fakeWatchForward struct {
	mu          sync.Mutex
	src         store.WatchSource // UserSourceByTarget 返回
	srcErr      error
	eligible    []store.ChannelBinding // 源路径候选
	inviteElig  []store.ChannelBinding // 邀请路径候选
	inviteErr   error
	view        watch.ForwardTargetView
	setErr      error
	setCalls    [][4]int64 // userID, sourceID, inviteID, targetID
	stageCalls  [][4]int64
	clearSrc    [][2]int64 // userID, sourceID
	clearInvite [][2]int64 // userID, inviteID
}

func (f *fakeWatchForward) UserSourceByTarget(context.Context, int64, string) (store.WatchSource, error) {
	return f.src, f.srcErr
}

func (f *fakeWatchForward) EligibleForwardTargets(context.Context, int64, int64) ([]store.ChannelBinding, error) {
	return f.eligible, nil
}

func (f *fakeWatchForward) EligibleForwardTargetsForInvite(context.Context, int64, int64) ([]store.ChannelBinding, error) {
	return f.inviteElig, f.inviteErr
}

func (f *fakeWatchForward) SetForwardTarget(_ context.Context, userID, sourceID, targetID int64) (store.WatchSource, error) {
	f.mu.Lock()
	f.setCalls = append(f.setCalls, [4]int64{userID, sourceID, 0, targetID})
	f.mu.Unlock()
	if f.setErr != nil {
		return store.WatchSource{}, f.setErr
	}
	return f.src, nil
}

func (f *fakeWatchForward) ClearForwardTarget(_ context.Context, userID, sourceID int64) (store.WatchSource, error) {
	f.mu.Lock()
	f.clearSrc = append(f.clearSrc, [2]int64{userID, sourceID})
	f.mu.Unlock()
	return f.src, nil
}

func (f *fakeWatchForward) StageInviteForwardTarget(_ context.Context, userID, inviteID, targetID int64) (store.WatchInviteRequest, error) {
	f.mu.Lock()
	f.stageCalls = append(f.stageCalls, [4]int64{userID, 0, inviteID, targetID})
	f.mu.Unlock()
	return store.WatchInviteRequest{}, nil
}

func (f *fakeWatchForward) ClearInviteForwardTarget(_ context.Context, userID, inviteID int64) (store.WatchInviteRequest, error) {
	f.mu.Lock()
	f.clearInvite = append(f.clearInvite, [2]int64{userID, inviteID})
	f.mu.Unlock()
	return store.WatchInviteRequest{}, nil
}

func (f *fakeWatchForward) ForwardTargetViewOf(context.Context, store.WatchSource) watch.ForwardTargetView {
	return f.view
}

// wireWatchTargetPanelForTest 复刻 updateHandler 的默认注入（测试自建
// prompter 以便直接驱动回调）。
func wireWatchTargetPanelForTest(t *testing.T, opt *Options, _ *fakeSender) *inputPrompter {
	t.Helper()
	prompter := newInputPrompter()
	opt.ShowWatchTargetSelection = func(ctx context.Context, fs delivery.Sender, sel watchTargetSelection, userID, chatID int64) {
		showWatchTargetSelection(ctx, *opt, fs, prompter, sel, userID, chatID)
	}
	return prompter
}

func okHandler(w http.ResponseWriter, _ *http.Request) {
	io.WriteString(w, `{"ok":true,"result":true}`)
}

// wtgtCallback 构造面板回调（from=面板所有者 7）。
func wtgtCallback(t *testing.T, data string, messageID int) *models.CallbackQuery {
	t.Helper()
	return &models.CallbackQuery{
		ID: "cb-" + data, From: models.User{ID: 7}, Data: data,
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: messageID, Chat: models.Chat{ID: 7, Type: models.ChatTypePrivate},
		}},
	}
}

const (
	wtgtSrcID    int64 = -1001234567801
	wtgtTargetID int64 = -1001234567802
	wtgtAltID    int64 = -1001234567803
	wtgtSrcText        = "-1001234567801"
)

// TestWatchTargetCommandShowsPanelAndSelects /watchtarget 命令 → 选择面板
// → 回调原子消费并落库；重放回调拿到失效提示且不重复设置。
func TestWatchTargetCommandShowsPanelAndSelects(t *testing.T) {
	fwd := &fakeWatchForward{
		src: store.WatchSource{ChannelID: wtgtSrcID, Title: "我的源", AddedBy: 7, BotID: 42,
			ForwardBindingChannelID: wtgtTargetID},
		eligible: []store.ChannelBinding{
			{ChannelID: wtgtTargetID, Title: "目标频道", BotID: 42},
			{ChannelID: wtgtAltID, Title: "备用频道", BotID: 42},
		},
		view: watch.ForwardTargetView{State: watch.ForwardTargetSet, ChannelID: wtgtTargetID, Title: "目标频道"},
	}
	opt, _, snd := newHarness(t, 4)
	opt.WatchForward = fwd
	prompter := wireWatchTargetPanelForTest(t, &opt, snd)

	run(opt, snd, "/watchtarget "+wtgtSrcText)
	texts := snd.texts()
	if len(texts) != 2 {
		t.Fatalf("应发送锚点+面板两条消息: %v", texts)
	}
	if !strings.Contains(texts[0], "我的源") || !strings.Contains(texts[1], "选择一个目标") {
		t.Fatalf("锚点与面板文案不符: %v", texts)
	}
	snd.mu.Lock()
	markups := append([]models.ReplyMarkup(nil), snd.markups...)
	snd.mu.Unlock()
	if len(markups) != 2 {
		t.Fatalf("应有两份 markup: %d", len(markups))
	}
	kb, ok := markups[1].(*models.InlineKeyboardMarkup)
	if !ok || len(kb.InlineKeyboard) == 0 {
		t.Fatalf("面板应有内联键盘: %+v", markups[1])
	}
	// 当前目标 ⭐ 标记
	if kb.InlineKeyboard[0][0].Text != "⭐ 目标频道" {
		t.Fatalf("当前目标应带 ⭐: %q", kb.InlineKeyboard[0][0].Text)
	}
	if strings.Contains(kb.InlineKeyboard[0][0].CallbackData, "1001") {
		t.Fatalf("回调不得携带频道 ID: %q", kb.InlineKeyboard[0][0].CallbackData)
	}

	bot := commandTestBot(t, okHandler)
	// 选择第 2 个候选（锚点消息 ID = 1）
	choose := wtgtCallback(t, promptWatchTargetCallbackPrefix+"1:1", 2)
	handlePromptCallback(context.Background(), opt, snd, bot, choose, prompter)
	handlePromptCallback(context.Background(), opt, snd, bot, choose, prompter) // 重放

	if len(fwd.setCalls) != 1 || fwd.setCalls[0] != [4]int64{7, wtgtSrcID, 0, wtgtAltID} {
		t.Fatalf("应恰好一次设置: %+v", fwd.setCalls)
	}
	// 结果文案落在面板编辑（finishPromptSubmission 编辑控制消息）
	found := false
	for _, txt := range snd.editedTexts() {
		if strings.Contains(txt, "已设置") && strings.Contains(txt, "备用频道") {
			found = true
		}
	}
	if !found {
		t.Fatalf("结果应回显所选目标: %v / %v", snd.texts(), snd.editedTexts())
	}
	// 重放回调得到失效提示且不再落库
	expired := false
	for _, txt := range snd.texts() {
		if strings.Contains(txt, "已失效") {
			expired = true
		}
	}
	if !expired || len(fwd.setCalls) != 1 {
		t.Fatalf("重放回调应失效提示且不重复设置: %v %+v", snd.texts(), fwd.setCalls)
	}
}

// TestWatchTargetClearCallback 清除按钮 → ClearForwardTarget。
func TestWatchTargetClearCallback(t *testing.T) {
	fwd := &fakeWatchForward{
		src:      store.WatchSource{ChannelID: wtgtSrcID, Title: "我的源", AddedBy: 7, BotID: 42},
		eligible: []store.ChannelBinding{{ChannelID: wtgtTargetID, Title: "目标频道", BotID: 42}},
	}
	opt, _, snd := newHarness(t, 4)
	opt.WatchForward = fwd
	prompter := wireWatchTargetPanelForTest(t, &opt, snd)

	run(opt, snd, "/watchtarget "+wtgtSrcText)
	bot := commandTestBot(t, okHandler)
	handlePromptCallback(context.Background(), opt, snd, bot,
		wtgtCallback(t, promptWatchTargetClearCallbackPrefix+"1", 2), prompter)

	if len(fwd.clearSrc) != 1 || fwd.clearSrc[0] != [2]int64{7, wtgtSrcID} {
		t.Fatalf("应清除源目标: %+v", fwd.clearSrc)
	}
	// 收尾：锚点消息删除（面板编辑为结果文案，不再单独发文本）
	if got := snd.deletedIDs(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("清除收尾应删除锚点消息: %v", got)
	}
}

// TestWatchTargetPageFlip 候选超一页时翻页回调更新面板。
func TestWatchTargetPageFlip(t *testing.T) {
	bindings := make([]store.ChannelBinding, 0, 10)
	for i := 0; i < 10; i++ {
		bindings = append(bindings, store.ChannelBinding{ChannelID: wtgtTargetID - int64(i), Title: "频道" + string(rune('A'+i)), BotID: 42})
	}
	fwd := &fakeWatchForward{
		src:      store.WatchSource{ChannelID: wtgtSrcID, Title: "我的源", AddedBy: 7, BotID: 42},
		eligible: bindings,
	}
	opt, _, snd := newHarness(t, 4)
	opt.WatchForward = fwd
	prompter := wireWatchTargetPanelForTest(t, &opt, snd)

	run(opt, snd, "/watchtarget "+wtgtSrcText)
	snd.mu.Lock()
	edits := len(snd.markupEdits)
	snd.mu.Unlock()
	if got := edits; got != 0 {
		t.Fatalf("初始面板不应有编辑: %d", got)
	}
	bot := commandTestBot(t, okHandler)
	handlePromptCallback(context.Background(), opt, snd, bot,
		wtgtCallback(t, promptWatchTargetPageCallbackPrefix+"1:1", 2), prompter)
	snd.mu.Lock()
	edits2 := len(snd.markupEdits)
	snd.mu.Unlock()
	if got := edits2; got != 1 {
		t.Fatalf("翻页应编辑面板一次: %d", got)
	}
	// 第二页应展示第 9/10 个候选（下标 8）而非第一页内容
	snd.mu.Lock()
	flipped := snd.markupEdits[len(snd.markupEdits)-1]
	snd.mu.Unlock()
	kb2, ok := flipped.(*models.InlineKeyboardMarkup)
	if !ok || len(kb2.InlineKeyboard) == 0 || kb2.InlineKeyboard[0][0].Text != "频道I" {
		t.Fatalf("第二页首个按钮应为第 9 个候选: %+v", flipped)
	}
}

// TestWatchTargetCallbackWrongOwner 非面板所有者回调 → 失效提示，不落库。
func TestWatchTargetCallbackWrongOwner(t *testing.T) {
	fwd := &fakeWatchForward{
		src:      store.WatchSource{ChannelID: wtgtSrcID, Title: "我的源", AddedBy: 7, BotID: 42},
		eligible: []store.ChannelBinding{{ChannelID: wtgtTargetID, Title: "目标频道", BotID: 42}},
	}
	opt, _, snd := newHarness(t, 4)
	opt.WatchForward = fwd
	prompter := wireWatchTargetPanelForTest(t, &opt, snd)

	run(opt, snd, "/watchtarget "+wtgtSrcText)
	bot := commandTestBot(t, okHandler)
	cb := &models.CallbackQuery{
		ID: "cb-x", From: models.User{ID: 8}, Data: promptWatchTargetCallbackPrefix + "1:0",
		Message: models.MaybeInaccessibleMessage{Message: &models.Message{
			ID: 2, Chat: models.Chat{ID: 8, Type: models.ChatTypePrivate},
		}},
	}
	handlePromptCallback(context.Background(), opt, snd, bot, cb, prompter)
	if len(fwd.setCalls) != 0 {
		t.Fatalf("他人回调不应落库: %+v", fwd.setCalls)
	}
}

// TestWatchAfterSubmitOffersPanel /watch 成功（本人源、受理 Bot 为当前 Bot）
// 立即弹面板；受理 Bot 不符时只回结果不弹。
func TestWatchAfterSubmitOffersPanel(t *testing.T) {
	src := store.WatchSource{ChannelID: wtgtSrcID, Title: "我的源", AddedBy: 7, BotID: 42}
	fwd := &fakeWatchForward{
		eligible: []store.ChannelBinding{{ChannelID: wtgtTargetID, Title: "目标频道", BotID: 42}},
	}
	opt, _, snd := newHarness(t, 4)
	opt.Watch = &fakeWatch{out: watch.SubmitOutcome{Kind: watch.SubmitActive, Title: "我的源", Channel: src}}
	opt.WatchForward = fwd
	opt.Bot = NewBotRef(42, "bot42")
	wireWatchTargetPanelForTest(t, &opt, snd)

	run(opt, snd, "/watch "+wtgtSrcText)
	texts := snd.texts()
	if len(texts) != 3 { // 结果 + 锚点 + 面板
		t.Fatalf("成功提交应立即弹面板: %v", texts)
	}

	// 受理 Bot 不符（源经 bot99 提交，当前 bot42）：不弹面板
	opt2, _, snd2 := newHarness(t, 4)
	opt2.Watch = &fakeWatch{out: watch.SubmitOutcome{Kind: watch.SubmitActive, Title: "我的源",
		Channel: store.WatchSource{ChannelID: wtgtSrcID, AddedBy: 7, BotID: 99}}}
	opt2.WatchForward = fwd
	opt2.Bot = NewBotRef(42, "bot42")
	wireWatchTargetPanelForTest(t, &opt2, snd2)
	run(opt2, snd2, "/watch "+wtgtSrcText)
	if got := len(snd2.texts()); got != 1 {
		t.Fatalf("路由不符不应弹面板: %v", snd2.texts())
	}
}

// TestWatchAfterSubmitOffersInvitePanel 邀请活动申请结果带申请 ID → 面板
// 走暂存路径；回调 StageInviteForwardTarget。
func TestWatchAfterSubmitOffersInvitePanel(t *testing.T) {
	fwd := &fakeWatchForward{
		inviteElig: []store.ChannelBinding{{ChannelID: wtgtTargetID, Title: "目标频道", BotID: 42}},
	}
	opt, _, snd := newHarness(t, 4)
	opt.Watch = &fakeWatch{out: watch.SubmitOutcome{Kind: watch.SubmitInvitePending, Title: "私有频道", RequestID: 5}}
	opt.WatchForward = fwd
	prompter := wireWatchTargetPanelForTest(t, &opt, snd)

	run(opt, snd, "/watch https://t.me/+somehash")
	texts := snd.texts()
	if len(texts) != 3 {
		t.Fatalf("邀请待审批也应弹面板: %v", texts)
	}
	if !strings.Contains(texts[2], "生效后自动启用") {
		t.Fatalf("邀请面板文案应说明生效后启用: %q", texts[2])
	}
	bot := commandTestBot(t, okHandler)
	// 消息序：结果(1)、锚点(2)、面板(3)——回调 key 是锚点 ID
	handlePromptCallback(context.Background(), opt, snd, bot,
		wtgtCallback(t, promptWatchTargetCallbackPrefix+"2:0", 3), prompter)
	if len(fwd.stageCalls) != 1 || fwd.stageCalls[0] != [4]int64{7, 0, 5, wtgtTargetID} {
		t.Fatalf("应暂存到申请 5: %+v", fwd.stageCalls)
	}
}

// TestWatchTargetEmptyCandidatesGuidance 无兼容绑定时给 /bind 引导；
// 旧源无受理 Bot 时引导重新 /watch。
func TestWatchTargetEmptyCandidatesGuidance(t *testing.T) {
	fwd := &fakeWatchForward{src: store.WatchSource{ChannelID: wtgtSrcID, Title: "我的源", AddedBy: 7, BotID: 42}}
	opt, _, snd := newHarness(t, 4)
	opt.WatchForward = fwd
	wireWatchTargetPanelForTest(t, &opt, snd)
	run(opt, snd, "/watchtarget "+wtgtSrcText)
	last := snd.texts()[len(snd.texts())-1]
	if !strings.Contains(last, "/bind") {
		t.Fatalf("空候选应引导 /bind: %q", last)
	}

	// 旧源（bot_id=0）：仅 /watchtarget 路径可达（/watch 后置 offer 有 BotID 守卫）
	fwd2 := &fakeWatchForward{src: store.WatchSource{ChannelID: wtgtSrcID, Title: "旧源", AddedBy: 7, BotID: 0}}
	opt2, _, snd2 := newHarness(t, 4)
	opt2.WatchForward = fwd2
	wireWatchTargetPanelForTest(t, &opt2, snd2)
	run(opt2, snd2, "/watchtarget "+wtgtSrcText)
	last2 := snd2.texts()[len(snd2.texts())-1]
	if !strings.Contains(last2, "重新发送 /watch") {
		t.Fatalf("旧源应引导重新 /watch: %q", last2)
	}
}

// TestWatchTargetErrorTextSentinel 哨兵错误取服务层文案，存储错误回落码表。
func TestWatchTargetErrorTextSentinel(t *testing.T) {
	if got := watchTargetErrorText(watch.ErrForwardTargetLoop); got != watch.ErrForwardTargetLoop.Error() {
		t.Fatalf("哨兵应直显: %q", got)
	}
	if got := watchTargetErrorText(store.ErrNotFound); !strings.Contains(got, "没有找到") {
		t.Fatalf("ErrNotFound 应转中文: %q", got)
	}
	if got := watchTargetErrorText(errors.New("sql: no such table")); strings.Contains(got, "sql") {
		t.Fatalf("未知错误不得透出原文: %q", got)
	}
}

// TestWatchlistForwardTargetStatus /watchlist 展示专属目标状态行与邀请暂存。
func TestWatchlistForwardTargetStatus(t *testing.T) {
	opt, _, snd := newHarness(t, 4)
	opt.Watch = &fakeWatch{
		rows: []store.WatchSource{
			{ChannelID: -1001, Title: "频道A", Status: store.WatchApproved, Enabled: true, AddedBy: 7},
			{ChannelID: -1002, Title: "频道B", Status: store.WatchApproved, Enabled: true, AddedBy: 7},
		},
		invites: []store.WatchInviteRequest{{ID: 3, Title: "私邀", Status: store.WatchInvitePending, ForwardBindingChannelID: -1003001}},
	}
	opt.WatchForward = &switchFakeWatchForward{
		set:  watch.ForwardTargetView{State: watch.ForwardTargetSet, Title: "我的目标"},
		stal: watch.ForwardTargetView{State: watch.ForwardTargetStale, ChannelID: -1009999},
	}
	run(opt, snd, "/watchlist")
	got := snd.texts()[0]
	if !strings.Contains(got, "专属转发：我的目标") {
		t.Fatalf("应显示已设置目标: %q", got)
	}
	if !strings.Contains(got, "目标已失效") || !strings.Contains(got, "/watchtarget 重选") {
		t.Fatalf("应显示失效目标与重选入口: %q", got)
	}
	if !strings.Contains(got, "已选专属目标") {
		t.Fatalf("邀请暂存应显示: %q", got)
	}
}

// switchFakeWatchForward 按源 ID 交替返回 set/stale 视图（列表状态行测试）。
type switchFakeWatchForward struct {
	set  watch.ForwardTargetView
	stal watch.ForwardTargetView
}

func (f *switchFakeWatchForward) UserSourceByTarget(context.Context, int64, string) (store.WatchSource, error) {
	return store.WatchSource{}, store.ErrNotFound
}
func (f *switchFakeWatchForward) EligibleForwardTargets(context.Context, int64, int64) ([]store.ChannelBinding, error) {
	return nil, nil
}
func (f *switchFakeWatchForward) EligibleForwardTargetsForInvite(context.Context, int64, int64) ([]store.ChannelBinding, error) {
	return nil, nil
}
func (f *switchFakeWatchForward) SetForwardTarget(context.Context, int64, int64, int64) (store.WatchSource, error) {
	return store.WatchSource{}, nil
}
func (f *switchFakeWatchForward) ClearForwardTarget(context.Context, int64, int64) (store.WatchSource, error) {
	return store.WatchSource{}, nil
}
func (f *switchFakeWatchForward) StageInviteForwardTarget(context.Context, int64, int64, int64) (store.WatchInviteRequest, error) {
	return store.WatchInviteRequest{}, nil
}
func (f *switchFakeWatchForward) ClearInviteForwardTarget(context.Context, int64, int64) (store.WatchInviteRequest, error) {
	return store.WatchInviteRequest{}, nil
}
func (f *switchFakeWatchForward) ForwardTargetViewOf(_ context.Context, src store.WatchSource) watch.ForwardTargetView {
	if src.ChannelID == -1001 {
		return f.set
	}
	return f.stal
}
