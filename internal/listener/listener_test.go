package listener

import (
	"context"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/dumpcache"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeCopySender 只实现 CopyMessages 的可编程假发送器（其余方法经接口
// 内嵌占位，本包路径不会触达）。
type fakeCopySender struct {
	delivery.Sender
	mu     sync.Mutex
	copies []copyRecord
	err    error
	nextID int
}

type copyRecord struct {
	from, to int64
	ids      []int
}

func (f *fakeCopySender) CopyMessages(_ context.Context, from, to int64, ids []int) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.copies = append(f.copies, copyRecord{from: from, to: to, ids: append([]int(nil), ids...)})
	if f.err != nil {
		return nil, f.err
	}
	out := make([]int, 0, len(ids))
	for range ids {
		f.nextID++
		out = append(out, 100+f.nextID)
	}
	return out, nil
}

func (f *fakeCopySender) snapshot() []copyRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]copyRecord(nil), f.copies...)
}

type enqueueRecord struct {
	kind, key string
	messageID int
}

// enqueueLog 线程安全的回退入队记录器（回调在计时器 goroutine 执行）。
type enqueueLog struct {
	mu    sync.Mutex
	calls []enqueueRecord
}

func (e *enqueueLog) record(kind, key string, messageID int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, enqueueRecord{kind: kind, key: key, messageID: messageID})
}

func (e *enqueueLog) snapshot() []enqueueRecord {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]enqueueRecord(nil), e.calls...)
}

// newFixture 构造带独立临时库的监听服务：dump 频道固定为 -100dump，
// 源行与场景由各用例自行写入。
func newFixture(t *testing.T) (*Service, *fakeCopySender, *store.Store, *enqueueLog) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/test.db", testLog())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const dumpChannel int64 = -100777
	dump := dumpcache.New(nil, nil, st, func() int64 { return dumpChannel }, testLog())
	snd := &fakeCopySender{}
	enqueued := &enqueueLog{}
	l := New(ctx, Options{
		Log:   testLog(),
		Store: st,
		Dump:  dump,
		EnqueueDump: func(_ context.Context, kind, key string, messageID int) (int64, error) {
			enqueued.record(kind, key, messageID)
			return 777, nil // 模拟关联 requests 行
		},
	})
	return l, snd, st, enqueued
}

func seedSource(t *testing.T, st *store.Store, src store.WatchSource) {
	t.Helper()
	if _, err := st.UpsertWatchSource(context.Background(), src); err != nil {
		t.Fatalf("写入监听源失败: %v", err)
	}
}

func mediaMsg(chatID int64, msgID int, group string) *models.Message {
	return &models.Message{
		ID:           msgID,
		MediaGroupID: group,
		Chat:         models.Chat{ID: chatID, Type: models.ChatTypeSupergroup},
		Video:        &models.Video{},
	}
}

// waitFor 轮询断言（相册聚合是异步计时器触发，测试侧等待收敛）。
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

// 相册聚合成批复制：同 media_group_id 的成员只发一次 CopyMessages
// （保组），公开源按每个成员消息 ID 落 username 与 -100 双键条目。
func TestAlbumAggregatedCopyWithDualKeys(t *testing.T) {
	l, snd, st, enqueued := newFixture(t)
	const chatID int64 = -1001234
	seedSource(t, st, store.WatchSource{
		ChannelID: chatID, Username: "MyChan", Title: "测试频道",
		Status: store.WatchApproved, Enabled: true,
	})
	l.OnMessage(mediaMsg(chatID, 11, "g1"), snd, 42, "watcher_bot")
	l.OnMessage(mediaMsg(chatID, 12, "g1"), snd, 42, "watcher_bot")
	l.OnMessage(mediaMsg(chatID, 13, "g1"), snd, 42, "watcher_bot")

	// 等待点直接对准断言条件（条目落库发生在复制返回之后，等复制数会竞态）
	ctx := context.Background()
	waitFor(t, func() bool {
		for _, id := range []int{11, 12, 13} {
			for _, key := range []string{"mychan", "-1001234"} {
				if _, ok := l.dump.Entry(ctx, key, id); !ok {
					return false
				}
			}
		}
		return true
	})
	copies := snd.snapshot()
	if len(copies) != 1 || copies[0].from != chatID || copies[0].to != -100777 {
		t.Fatalf("相册应整组一次复制: %+v", copies)
	}
	if len(copies[0].ids) != 3 || copies[0].ids[0] != 11 || copies[0].ids[2] != 13 {
		t.Fatalf("相册成员应按序整批: %+v", copies[0].ids)
	}
	if calls := enqueued.snapshot(); len(calls) != 0 {
		t.Fatalf("不应触发回退: %+v", calls)
	}
}

// 私有源只落数字键；无相册标识的单条消息独立成批（两条消息两次复制）。
func TestPrivateSourceSingleMessages(t *testing.T) {
	l, snd, st, _ := newFixture(t)
	const chatID int64 = -1005678
	seedSource(t, st, store.WatchSource{
		ChannelID: chatID, Title: "私有频道",
		Status: store.WatchApproved, Enabled: true,
	})
	l.OnMessage(mediaMsg(chatID, 21, ""), snd, 0, "")
	l.OnMessage(mediaMsg(chatID, 22, ""), snd, 0, "")
	waitFor(t, func() bool { return len(snd.snapshot()) == 2 })
	if _, ok := l.dump.Entry(context.Background(), "-1005678", 21); !ok {
		t.Error("私有源应有数字键条目")
	}
}

// 已有条目（重复投递/多 bot 池）整批跳过，不再复制。
func TestExistingEntrySkipsBatch(t *testing.T) {
	l, snd, st, _ := newFixture(t)
	const chatID int64 = -1001234
	seedSource(t, st, store.WatchSource{
		ChannelID: chatID, Username: "mychan", Status: store.WatchApproved, Enabled: true,
	})
	if _, err := st.InsertDumpEntry(context.Background(), store.DumpEntry{
		ChannelKey: "-1001234", MessageID: 31, DumpIDs: []int{500},
		DumpChannelID: -100777,
	}); err != nil {
		t.Fatalf("预置条目失败: %v", err)
	}
	l.OnMessage(mediaMsg(chatID, 31, ""), snd, 0, "")
	time.Sleep(2 * time.Second)
	if got := snd.snapshot(); len(got) != 0 {
		t.Fatalf("已有条目应跳过复制: %+v", got)
	}
}

// 受保护内容不走服务端复制，直接回退特权入队（相册只入队首条）。
func TestProtectedFallsBackToEnqueue(t *testing.T) {
	l, snd, st, enqueued := newFixture(t)
	const chatID int64 = -1004321
	seedSource(t, st, store.WatchSource{
		ChannelID: chatID, Username: "protected", Status: store.WatchApproved, Enabled: true,
	})
	m1 := mediaMsg(chatID, 41, "g2")
	m1.HasProtectedContent = true
	m2 := mediaMsg(chatID, 42, "g2")
	l.OnMessage(m1, snd, 42, "watcher_bot")
	l.OnMessage(m2, snd, 42, "watcher_bot")
	waitFor(t, func() bool { return len(enqueued.snapshot()) == 1 })
	e := enqueued.snapshot()[0]
	if e.kind != store.SourcePublic || e.key != "protected" || e.messageID != 41 {
		t.Fatalf("回退应取公开键与首条 ID: %+v", e)
	}
	if got := snd.snapshot(); len(got) != 0 {
		t.Fatalf("受保护内容不应服务端复制: %+v", got)
	}
	// 事件在入队回调返回后落库：等待点对准事件本身
	var events []store.WatchEvent
	waitFor(t, func() bool {
		events = listEvents(t, l)
		return len(events) == 1
	})
	if events[0].Path != store.WatchPathFallback ||
		events[0].RequestID != 777 || events[0].MessageID != 41 || events[0].BotID != 42 {
		t.Fatalf("回退事件应带关联请求与 bot: %+v", events)
	}
}

// listEvents 读取该测试库的全部预热事件（时间倒序取首条前按 id 升序断言
// 更直观，这里直接复用分页查询）。
func listEvents(t *testing.T, l *Service) []store.WatchEvent {
	t.Helper()
	events, _, err := l.st.ListWatchEvents(context.Background(), store.WatchEventsQuery{Page: 1, PageSize: 50})
	if err != nil {
		t.Fatalf("查询预热事件失败: %v", err)
	}
	return events
}

// 未配置源 / 待审批 / 暂停的聊天静默丢弃；纯文本不预热。
func TestInactiveSourceAndTextIgnored(t *testing.T) {
	l, snd, st, _ := newFixture(t)
	seedSource(t, st, store.WatchSource{
		ChannelID: -100999, Status: store.WatchPending, Enabled: true,
	})
	l.OnMessage(mediaMsg(-100999, 51, ""), snd, 0, "")                               // 待审批 → 忽略
	l.OnMessage(mediaMsg(-100888, 52, ""), snd, 0, "")                               // 未配置 → 忽略
	l.OnMessage(&models.Message{ID: 53, Chat: models.Chat{ID: -100888}}, snd, 0, "") // 无媒体 → 忽略
	time.Sleep(2 * time.Second)
	if got := snd.snapshot(); len(got) != 0 {
		t.Fatalf("非生效源不应转储: %+v", got)
	}
}

// 编译期断言：假发送器满足 Sender 接口（CopyMessages 之外的方法由内嵌
// 接口占位，不触达）。
var _ delivery.Sender = (*fakeCopySender)(nil)

// logCapture 捕获日志输出的 io.Writer（跳过原因断言用）。
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, string(p))
	return len(p), nil
}

func newLogCapture() *logCapture { return &logCapture{} }

func (c *logCapture) Enabled(context.Context, slog.Level) bool { return true }

func (c *logCapture) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.Key + "=" + a.Value.String())
		return true
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, b.String())
	return nil
}
func (c *logCapture) WithAttrs(attrs []slog.Attr) slog.Handler { return c }
func (c *logCapture) WithGroup(name string) slog.Handler       { return c }

func (c *logCapture) contains(substr string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.lines {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

// 跳过原因可见：已注册源的无媒体消息、待审批源、暂停源分别带可定位的
// reason；跳过日志带发送者身份（from_is_bot）。
func TestSkipReasonsLogged(t *testing.T) {
	capture := newLogCapture()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/test.db", testLog())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dump := dumpcache.New(nil, nil, st, func() int64 { return -100777 }, testLog())
	l := New(ctx, Options{Log: slog.New(capture), Store: st, Dump: dump})
	const chatID int64 = -1001234
	seedSource(t, st, store.WatchSource{
		ChannelID: chatID, Title: "测试频道", Status: store.WatchPending, Enabled: true,
	})

	snd := &fakeCopySender{}
	fromBot := &models.User{ID: 777, IsBot: true}

	// 待审批源的媒体消息：带"待审批"原因
	l.OnMessage(mediaMsg(chatID, 51, ""), snd, 0, "")
	// 同一源的纯文本消息：待审批缓存命中，仍是待审批原因（缓存窗口内）
	l.OnMessage(&models.Message{
		ID: 52, Chat: models.Chat{ID: chatID, Type: models.ChatTypeSupergroup}, From: fromBot,
	}, snd, 0, "")

	waitFor(t, func() bool {
		return capture.contains("源待审批") && capture.contains("from_is_bot=true")
	})
	if capture.contains("消息无媒体") {
		t.Error("非生效源不应报告无媒体原因")
	}
	if got := snd.snapshot(); len(got) != 0 {
		t.Fatalf("非生效源不应转储: %+v", got)
	}

	// 源生效后（新建服务绕开负缓存），同一源的文本消息报告"无媒体"
	if _, err := st.UpsertWatchSource(ctx, store.WatchSource{
		ChannelID: chatID, Title: "测试频道", Status: store.WatchApproved, Enabled: true,
	}); err != nil {
		t.Fatalf("更新源失败: %v", err)
	}
	l2 := New(ctx, Options{Log: slog.New(capture), Store: st, Dump: dump})
	l2.OnMessage(&models.Message{
		ID: 53, Chat: models.Chat{ID: chatID, Type: models.ChatTypeSupergroup}, From: fromBot,
	}, snd, 0, "")
	waitFor(t, func() bool { return capture.contains("消息无媒体") })
}

// 普通群组消息在 botapi 路由层就被跳过（带原因），不会进 listener。
func TestBasicGroupRoutingSkipLogged(t *testing.T) {
	// 该行为在 botapi handler 内联日志，核心断言在 botapi 包测试覆盖；
	// 这里仅固化 listener 对 channel/supergroup 之外类型的防御性丢弃。
	capture := newLogCapture()
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/test.db", testLog())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	dump := dumpcache.New(nil, nil, st, func() int64 { return -100777 }, testLog())
	l := New(ctx, Options{Log: slog.New(capture), Store: st, Dump: dump})

	basic := &models.Message{ID: 61, Chat: models.Chat{ID: -99, Type: models.ChatTypeGroup},
		Video: &models.Video{}}
	l.OnMessage(basic, &fakeCopySender{}, 0, "")
	waitFor(t, func() bool { return capture.contains("聊天类型不支持") })
}

// nil 接收者不得 panic（方法值在装配时序错误下会绑定 nil 接收者，
// 2026-09-21 SIGSEGV 回归）。
func TestNilReceiverDoesNotPanic(t *testing.T) {
	var l *Service
	l.OnMessage(mediaMsg(-1001234, 71, ""), &fakeCopySender{}, 0, "")
}

// containsSkip 检查服务日志中是否出现过指定子串（跳过原因断言用）。
func (s *Service) containsSkip(substr string) bool {
	lc, ok := s.log.Handler().(*logCapture)
	if !ok {
		return false
	}
	return lc.contains(substr)
}

// 多 bot 池重复/交错投递：同一相册被两个受理 bot 各投递一遍（成员重复、
// 乱序到达）时，批内按消息 ID 升序去重，仍只整组复制一次——copyMessages
// 要求 message_ids 严格递增，乱序重复会被 Telegram 整批拒绝。
func TestDuplicateInterleavedDeliveryCopiesOnce(t *testing.T) {
	l, snd, st, enqueued := newFixture(t)
	const chatID int64 = -1002345
	seedSource(t, st, store.WatchSource{
		ChannelID: chatID, Username: "DupChan", Title: "重复投递",
		Status: store.WatchApproved, Enabled: true,
	})
	// bot_a 与 bot_b 的更新交错：11 重复、13 先于 12 到达
	l.OnMessage(mediaMsg(chatID, 11, "g9"), snd, 42, "bot_a")
	l.OnMessage(mediaMsg(chatID, 11, "g9"), snd, 43, "bot_b")
	l.OnMessage(mediaMsg(chatID, 13, "g9"), snd, 42, "bot_a")
	l.OnMessage(mediaMsg(chatID, 12, "g9"), snd, 43, "bot_b")

	// 等待点对准条目落库（复制返回之后才写）
	ctx := context.Background()
	waitFor(t, func() bool {
		for _, id := range []int{11, 12, 13} {
			for _, key := range []string{"dupchan", "-1002345"} {
				if _, ok := l.dump.Entry(ctx, key, id); !ok {
					return false
				}
			}
		}
		return true
	})
	copies := snd.snapshot()
	if len(copies) != 1 || copies[0].from != chatID || copies[0].to != -100777 {
		t.Fatalf("应只整组复制一次: %+v", copies)
	}
	if !sort.IntsAreSorted(copies[0].ids) || len(copies[0].ids) != 3 {
		t.Fatalf("复制 ID 应去重后严格递增: %+v", copies[0].ids)
	}
	if calls := enqueued.snapshot(); len(calls) != 0 {
		t.Fatalf("不应触发回退: %+v", calls)
	}
}
