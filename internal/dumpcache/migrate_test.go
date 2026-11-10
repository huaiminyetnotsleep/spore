package dumpcache

// migrate_test.go — 缓存迁移持久化运行的行为测试：完成/明细落库、暂停
// 继续、取消、并发发起守卫、幂等重跑（skipped）、源频道不可读中止、
// 单目标失败明细。控制动作模拟 Web Handler 直接写 DB 状态（生产路径
// 一致：Handler 即写、执行循环轮询感知）。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// 频道 ID 常量（-100… 形态贴近生产，便于读断言）。
const (
	srcCh  = int64(-100111)
	dstCh  = int64(-100222)
	dst1Ch = int64(-100333)
	dst2Ch = int64(-100444)
)

// blockingSender 在 CopyMessages 上阻塞直至放行（受控时序测试）。
type blockingSender struct {
	fakeSender
	entered chan struct{} // 每次调用进入时发信号
	release chan struct{} // 关闭后放行全部调用
}

func newBlockingSender() *blockingSender {
	return &blockingSender{entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (b *blockingSender) CopyMessages(ctx context.Context, from, to int64, ids []int) ([]int, error) {
	b.entered <- struct{}{}
	<-b.release
	return b.fakeSender.CopyMessages(ctx, from, to, ids)
}

// goneSender 模拟源频道不可读（chat not found 命中 channelGonePatterns）。
type goneSender struct{ fakeSender }

func (g *goneSender) CopyMessages(context.Context, int64, int64, []int) ([]int, error) {
	return nil, errors.New("chat not found")
}

// fastMigrateTimings 把限速与暂停轮询调到测试节奏（测试内同步恢复）。
func fastMigrateTimings(t *testing.T) {
	t.Helper()
	oldPace, oldPoll := migratePacing, migratePausePoll
	migratePacing, migratePausePoll = time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { migratePacing, migratePausePoll = oldPace, oldPoll })
}

// waitUntil 轮询等待条件成立（5s 超时失败）。
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// seedSource 在源频道落 n 条现行格式条目，返回游标列表。
func seedSource(t *testing.T, st *store.Store, channel int64, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 1; i <= n; i++ {
		if _, err := st.InsertDumpEntry(ctx, store.DumpEntry{
			ChannelKey: "news", MessageID: 100 + i, DumpIDs: []int{900 + i},
			DumpChannelID: channel}); err != nil {
			t.Fatal(err)
		}
	}
}

func latestRun(t *testing.T, st *store.Store) store.DumpMigrateRun {
	t.Helper()
	run, err := st.LatestDumpMigrateRun(context.Background())
	if err != nil {
		t.Fatalf("读取最新运行失败: %v", err)
	}
	return run
}

func runStatusIs(st *store.Store, id int64, status string) func() bool {
	return func() bool {
		s, err := st.DumpMigrateRunStatusOf(context.Background(), id)
		return err == nil && s == status
	}
}

func newMigrateService(t *testing.T, snd delivery.Sender, channels func() []int64) *Service {
	t.Helper()
	return New(snd, nil, openStore(t), channels, testLog())
}

func TestMigrateRunCompletesAndRecordsItems(t *testing.T) {
	fastMigrateTimings(t)
	ctx := context.Background()
	s := newMigrateService(t, &fakeSender{}, func() []int64 { return []int64{dstCh} })
	seedSource(t, s.st, srcCh, 3)

	if err := s.StartMigrate(ctx, srcCh); err != nil {
		t.Fatal(err)
	}
	run := latestRun(t, s.st)
	if run.Status != store.DumpMigrateRunning || run.Total != 3 {
		t.Fatalf("初始运行不符: %+v", run)
	}
	waitUntil(t, "迁移完成", runStatusIs(s.st, run.ID, store.DumpMigrateCompleted))

	run = latestRun(t, s.st)
	if run.Done != 3 || run.Skipped != 0 || run.Failed != 0 || run.FinishedAt == 0 {
		t.Fatalf("完成计数不符: %+v", run)
	}
	// 明细：3 条 migrated，各含目标频道新消息 ID
	items, total, err := s.st.ListDumpMigrateItems(ctx, run.ID, "migrated", 1, 20)
	if err != nil || total != 3 || len(items) != 3 {
		t.Fatalf("migrated 明细不符: total=%d err=%v", total, err)
	}
	for _, it := range items {
		if len(it.Results) != 1 || it.Results[0].ChannelID != dstCh || len(it.Results[0].MessageIDs) == 0 {
			t.Fatalf("明细结果不符: %+v", it)
		}
	}
	// 源行清理、目标行就绪
	if n, _ := s.st.CountDumpEntriesByChannel(ctx, srcCh); n != 0 {
		t.Fatalf("源行应清理, 剩 %d", n)
	}
	for i := 1; i <= 3; i++ {
		if _, err := s.st.LatestDumpEntry(ctx, "news", 100+i, dstCh); err != nil {
			t.Fatalf("目标频道应有现行条目 msg=%d: %v", 100+i, err)
		}
	}
}

func TestMigratePauseResumeCompletes(t *testing.T) {
	fastMigrateTimings(t)
	ctx := context.Background()
	bs := newBlockingSender()
	s := newMigrateService(t, bs, func() []int64 { return []int64{dstCh} })
	seedSource(t, s.st, srcCh, 2)

	if err := s.StartMigrate(ctx, srcCh); err != nil {
		t.Fatal(err)
	}
	run := latestRun(t, s.st)
	<-bs.entered // 首条复制进行中

	// 暂停（模拟 Handler 即写 DB）；放行后执行循环在批间/条间闸门停住
	if err := s.st.SetDumpMigrateRunStatus(ctx, run.ID, store.DumpMigratePaused, ""); err != nil {
		t.Fatal(err)
	}
	close(bs.release)
	// 暂停中的运行阻止再次发起
	if err := s.StartMigrate(ctx, srcCh); err == nil {
		t.Fatal("暂停中不应允许再次发起")
	}
	// updated_at 不再推进（执行已停）：以状态与计数稳定判定即可——
	// 首条已复制完成后落明细，然后闸门停住
	waitUntil(t, "至少一条明细落库", func() bool {
		run = latestRun(t, s.st)
		return run.Done+run.Skipped+run.Failed >= 1
	})

	// 继续：从断点恢复并最终完成
	if err := s.st.SetDumpMigrateRunStatus(ctx, run.ID, store.DumpMigrateRunning, ""); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "恢复后完成", runStatusIs(s.st, run.ID, store.DumpMigrateCompleted))
	run = latestRun(t, s.st)
	if run.Done != 2 {
		t.Fatalf("恢复后应完成全部: %+v", run)
	}
	if n, _ := s.st.CountDumpEntriesByChannel(ctx, srcCh); n != 0 {
		t.Fatalf("源行应全部清理, 剩 %d", n)
	}
}

func TestMigrateCancelStopsAndAllowsRestart(t *testing.T) {
	fastMigrateTimings(t)
	ctx := context.Background()
	bs := newBlockingSender()
	s := newMigrateService(t, bs, func() []int64 { return []int64{dstCh} })
	seedSource(t, s.st, srcCh, 2)

	if err := s.StartMigrate(ctx, srcCh); err != nil {
		t.Fatal(err)
	}
	run := latestRun(t, s.st)
	<-bs.entered

	// 取消（Handler 即写终态）；执行循环感知后静默退出，不再补终态
	if err := s.st.SetDumpMigrateRunStatus(ctx, run.ID, store.DumpMigrateCancelled, "管理员取消"); err != nil {
		t.Fatal(err)
	}
	close(bs.release)
	waitUntil(t, "状态稳定为取消", runStatusIs(s.st, run.ID, store.DumpMigrateCancelled))
	// 终态不可被完成写回翻转（SetDumpMigrateFinished 守卫）——静默观察期
	time.Sleep(50 * time.Millisecond)
	if got, _ := s.st.DumpMigrateRunStatusOf(ctx, run.ID); got != store.DumpMigrateCancelled {
		t.Fatalf("取消态被翻转: %s", got)
	}

	// 取消后可立即重新发起（唯一活跃索引不再命中）
	if err := s.StartMigrate(ctx, srcCh); err != nil {
		t.Fatalf("取消后应可重新发起: %v", err)
	}
	run2 := latestRun(t, s.st)
	if run2.ID == run.ID {
		t.Fatal("应创建新运行")
	}
}

func TestMigrateGuardRejectsConcurrentStart(t *testing.T) {
	fastMigrateTimings(t)
	ctx := context.Background()
	bs := newBlockingSender()
	s := newMigrateService(t, bs, func() []int64 { return []int64{dstCh} })
	seedSource(t, s.st, srcCh, 1)

	if err := s.StartMigrate(ctx, srcCh); err != nil {
		t.Fatal(err)
	}
	if err := s.StartMigrate(ctx, srcCh); err == nil {
		t.Fatal("运行中再次发起应被拒绝")
	}
	close(bs.release)
	waitUntil(t, "完成", runStatusIs(s.st, latestRun(t, s.st).ID, store.DumpMigrateCompleted))
	// 完成后可再发起
	if err := s.StartMigrate(ctx, srcCh); err != nil {
		t.Fatalf("完成后应可再发起: %v", err)
	}
}

func TestMigrateIdempotentRerunSkips(t *testing.T) {
	fastMigrateTimings(t)
	ctx := context.Background()
	// 源频道仍在启用集合：首跑迁移到 dstCh 且保留源行
	s := newMigrateService(t, &fakeSender{}, func() []int64 { return []int64{srcCh, dstCh} })
	seedSource(t, s.st, srcCh, 2)

	if err := s.StartMigrate(ctx, srcCh); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "首跑完成", runStatusIs(s.st, latestRun(t, s.st).ID, store.DumpMigrateCompleted))
	run1 := latestRun(t, s.st)
	if run1.Done != 2 {
		t.Fatalf("首跑应全部迁移: %+v", run1)
	}

	// 二跑：全部目标已有现行条目 → skipped，源行保留（源频道启用）
	if err := s.StartMigrate(ctx, srcCh); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "二跑完成", runStatusIs(s.st, latestRun(t, s.st).ID, store.DumpMigrateCompleted))
	run2 := latestRun(t, s.st)
	if run2.Done != 0 || run2.Skipped != 2 || run2.Failed != 0 {
		t.Fatalf("二跑应全部跳过: %+v", run2)
	}
}

func TestMigrateSourceGoneAbortsRun(t *testing.T) {
	fastMigrateTimings(t)
	ctx := context.Background()
	s := newMigrateService(t, &goneSender{}, func() []int64 { return []int64{dstCh} })
	seedSource(t, s.st, srcCh, 2)

	if err := s.StartMigrate(ctx, srcCh); err != nil {
		t.Fatal(err)
	}
	run := latestRun(t, s.st)
	waitUntil(t, "源不可读中止", func() bool {
		r := latestRun(t, s.st)
		return r.Status == store.DumpMigrateCompleted && r.LastError != ""
	})
	// 首条失败明细带目标级错误；中止原因落在 last_error
	items, total, err := s.st.ListDumpMigrateItems(ctx, run.ID, "failed", 1, 20)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("失败明细不符: total=%d err=%v", total, err)
	}
	if items[0].Results[0].Error == "" {
		t.Fatalf("失败明细应含目标级错误: %+v", items[0])
	}
}

func TestMigratePartialFailureRecordsFailedItem(t *testing.T) {
	fastMigrateTimings(t)
	ctx := context.Background()
	fs := &fakeSender{failCopyTo: dst2Ch}
	s := newMigrateService(t, fs, func() []int64 { return []int64{dst1Ch, dst2Ch} })
	seedSource(t, s.st, srcCh, 1)

	if err := s.StartMigrate(ctx, srcCh); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "迁移结束", runStatusIs(s.st, latestRun(t, s.st).ID, store.DumpMigrateCompleted))
	run := latestRun(t, s.st)
	if run.Failed != 1 || run.Done != 0 {
		t.Fatalf("部分失败计数不符: %+v", run)
	}
	items, _, err := s.st.ListDumpMigrateItems(ctx, run.ID, "failed", 1, 20)
	if err != nil || len(items) != 1 {
		t.Fatalf("失败明细应一条: %v", err)
	}
	it := items[0]
	if it.Error == "" || len(it.Results) != 2 {
		t.Fatalf("明细应含错误摘要与双目标结果: %+v", it)
	}
	for _, r := range it.Results {
		if r.ChannelID == dst1Ch && (len(r.MessageIDs) == 0 || r.Error != "") {
			t.Fatalf("成功目标应带新消息 ID: %+v", r)
		}
		if r.ChannelID == dst2Ch && (r.Error == "" || len(r.MessageIDs) != 0) {
			t.Fatalf("失败目标应带错误: %+v", r)
		}
	}
	// 成功目标照常落条目，源行保留（未全部成功不清理）
	if _, err := s.st.LatestDumpEntry(ctx, "news", 101, dst1Ch); err != nil {
		t.Fatalf("成功目标应有条目: %v", err)
	}
	if n, _ := s.st.CountDumpEntriesByChannel(ctx, srcCh); n != 1 {
		t.Fatalf("未全部成功源行应保留, 剩 %d", n)
	}
}
