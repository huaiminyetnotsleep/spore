package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/botpool"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/dumpcache"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
	"github.com/huaiminyetnotsleep/spore/internal/watch"
)

// mirrorCopySender 只记录 CopyMessages 的假发送器（回退镜像断言用）。
type mirrorCopySender struct {
	delivery.Sender
	mu     sync.Mutex
	copies []string // "from→to" 记录，按序
	failTo map[string]error
}

func (f *mirrorCopySender) CopyMessages(_ context.Context, from, to int64, _ []int) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := chatPair(from, to)
	f.copies = append(f.copies, key)
	if err := f.failTo[key]; err != nil {
		return nil, err
	}
	return []int{9001}, nil
}

func (f *mirrorCopySender) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.copies...)
}

func chatPair(from, to int64) string {
	return fmt.Sprintf("%d→%d", from, to)
}

func testSlog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestWatchDumpMirrorPlanParity 回退镜像与快路径共用投递计划：用户源
// （专属目标 + 管理员开启全局镜像）重传完成后，缓存中转复制到专属目标
// （源受理 Bot 精确路由，缺位即跳过不回退）与全局目标（主 bot），重合目标
// 只投一份，事件 targets 回写实际成功项。
func TestWatchDumpMirrorPlanParity(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/mirror.db", testSlog())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.CreateUser(ctx, store.User{ID: 7, Status: store.UserEnabled}); err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	const (
		srcID    int64 = -1001234567001
		userTgt  int64 = -1001234567002 // 专属目标（与全局重合）
		globalTg int64 = -1001234567003 // 仅全局
		cacheCh  int64 = -1001234567004 // 缓存频道
	)
	if _, err := st.UpsertWatchSource(ctx, store.WatchSource{
		ChannelID: srcID, Kind: "channel", Title: "回退源", Status: store.WatchApproved,
		Enabled: true, AddedBy: 7, BotID: 42,
	}); err != nil {
		t.Fatalf("写入源失败: %v", err)
	}
	if _, err := st.UpsertChannelBinding(ctx, store.ChannelBinding{
		ChannelID: userTgt, UserID: 7, BoundVia: store.BoundViaBot, BotID: 42, Title: "专属目标",
	}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	if _, err := st.SetWatchSourceForwardBinding(ctx, srcID, userTgt); err != nil {
		t.Fatalf("设置专属目标失败: %v", err)
	}
	if _, err := st.SetWatchSourceGlobalMirror(ctx, srcID, true); err != nil {
		t.Fatalf("开启全局镜像失败: %v", err)
	}
	if err := syscfg.SaveDumpChannels(ctx, st, []syscfg.DumpChannel{
		{ChannelID: cacheCh, Title: "缓存", Enabled: true},
	}); err != nil {
		t.Fatalf("保存缓存频道失败: %v", err)
	}
	if err := syscfg.SaveWatchForwardChannels(ctx, st, []syscfg.WatchForwardChannel{
		{ChannelID: userTgt, Title: "重合目标"}, {ChannelID: globalTg, Title: "全局目标"},
	}); err != nil {
		t.Fatalf("保存全局转发频道失败: %v", err)
	}
	// 回退事件行（request_id 关联重传任务）
	if _, err := st.InsertWatchEvent(ctx, store.WatchEvent{
		ChannelID: srcID, Title: "回退源", MessageID: 5, RequestID: 555,
		BotID: 42, Path: store.WatchPathFallback,
	}); err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}

	ws, err := watch.New(watch.Options{Store: st, Log: testSlog()})
	if err != nil {
		t.Fatalf("创建 watch 服务失败: %v", err)
	}
	exact := &mirrorCopySender{}
	primary := &mirrorCopySender{failTo: map[string]error{
		chatPair(cacheCh, globalTg): errMock("global down"), // 全局目标失败：单目标隔离
	}}
	pool := botpool.New()
	pool.Reset([]*botpool.Member{{ID: 42, Username: "bot42", Sender: exact}})
	dump := dumpcache.New(primary, nil, st, func() []int64 { return []int64{cacheCh} }, testSlog())
	m := watchDumpMirror{dump: dump, st: st, pool: pool, plan: ws.ForwardTargets, log: testSlog()}

	m.AfterSourceDump(ctx, 555, cacheCh, []int{9001, 9002})

	// 专属目标经受理 Bot 从缓存中转复制一次（重合的全局项去重，不再投）
	if got := exact.snapshot(); len(got) != 1 || got[0] != chatPair(cacheCh, userTgt) {
		t.Fatalf("专属目标应由受理 Bot 从缓存复制一次: %v", got)
	}
	// 全局目标（非重合项）走主 bot；失败被隔离，不影响专属目标与回写
	if got := primary.snapshot(); len(got) != 1 || got[0] != chatPair(cacheCh, globalTg) {
		t.Fatalf("全局目标应由主 bot 复制一次: %v", got)
	}
	ev, err := st.WatchEventByRequest(ctx, 555)
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(ev.Targets) != 2 || ev.Targets[0].ChannelID != cacheCh || ev.Targets[1].ChannelID != userTgt {
		t.Fatalf("事件目标应为缓存+成功的专属目标（失败全局项不记）: %+v", ev.Targets)
	}
}

// TestWatchDumpMirrorExactBotMissingSkipsUserTarget 受理 Bot 缺位：专属目标
// 跳过且绝不回退主 bot；全局目标不受影响。
func TestWatchDumpMirrorExactBotMissingSkipsUserTarget(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, t.TempDir()+"/mirror2.db", testSlog())
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.CreateUser(ctx, store.User{ID: 7, Status: store.UserEnabled}); err != nil {
		t.Fatalf("建用户失败: %v", err)
	}
	const (
		srcID   int64 = -1001234567101
		userTgt int64 = -1001234567102
		globalT int64 = -1001234567103
		cacheC  int64 = -1001234567104
	)
	if _, err := st.UpsertWatchSource(ctx, store.WatchSource{
		ChannelID: srcID, Kind: "channel", Title: "回退源", Status: store.WatchApproved,
		Enabled: true, AddedBy: 7, BotID: 42,
	}); err != nil {
		t.Fatalf("写入源失败: %v", err)
	}
	if _, err := st.UpsertChannelBinding(ctx, store.ChannelBinding{
		ChannelID: userTgt, UserID: 7, BoundVia: store.BoundViaBot, BotID: 42, Title: "专属目标",
	}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	if _, err := st.SetWatchSourceForwardBinding(ctx, srcID, userTgt); err != nil {
		t.Fatalf("设置专属目标失败: %v", err)
	}
	if _, err := st.SetWatchSourceGlobalMirror(ctx, srcID, true); err != nil {
		t.Fatalf("开启全局镜像失败: %v", err)
	}
	if err := syscfg.SaveDumpChannels(ctx, st, []syscfg.DumpChannel{
		{ChannelID: cacheC, Title: "缓存", Enabled: true},
	}); err != nil {
		t.Fatalf("保存缓存频道失败: %v", err)
	}
	if err := syscfg.SaveWatchForwardChannels(ctx, st, []syscfg.WatchForwardChannel{
		{ChannelID: globalT, Title: "全局目标"},
	}); err != nil {
		t.Fatalf("保存全局转发频道失败: %v", err)
	}
	if _, err := st.InsertWatchEvent(ctx, store.WatchEvent{
		ChannelID: srcID, Title: "回退源", MessageID: 6, RequestID: 556,
		BotID: 42, Path: store.WatchPathFallback,
	}); err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}

	ws, _ := watch.New(watch.Options{Store: st, Log: testSlog()})
	primary := &mirrorCopySender{}
	pool := botpool.New() // 池内无 bot42：精确路由缺位
	dump := dumpcache.New(primary, nil, st, func() []int64 { return []int64{cacheC} }, testSlog())
	m := watchDumpMirror{dump: dump, st: st, pool: pool, plan: ws.ForwardTargets, log: testSlog()}

	m.AfterSourceDump(ctx, 556, cacheC, []int{9003})

	if got := primary.snapshot(); len(got) != 1 || got[0] != chatPair(cacheC, globalT) {
		t.Fatalf("主 bot 只应复制全局目标（专属目标不回退）: %v", got)
	}
	ev, err := st.WatchEventByRequest(ctx, 556)
	if err != nil {
		t.Fatalf("读取事件失败: %v", err)
	}
	if len(ev.Targets) != 2 || ev.Targets[1].ChannelID != globalT {
		t.Fatalf("事件目标应为缓存+全局（缺位专属目标不记）: %+v", ev.Targets)
	}
}

type errMock string

func (e errMock) Error() string { return string(e) }
