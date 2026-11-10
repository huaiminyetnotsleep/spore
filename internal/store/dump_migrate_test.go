package store

import (
	"context"
	"errors"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// newMigrateRun 创建一条 running 运行的便捷封装。
func newMigrateRun(t *testing.T, s *Store, from int64, to []int64, total int64) DumpMigrateRun {
	t.Helper()
	run, err := s.CreateDumpMigrateRun(context.Background(), from, to, total)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestDumpMigrateRunLifecycleAndCounts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	run := newMigrateRun(t, s, -100, []int64{-200, -300}, 3)

	// 三条明细：migrated / skipped / failed，聚合计数经查询得出
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.InsertDumpMigrateItem(ctx, DumpMigrateItem{RunID: run.ID, ChannelKey: "news", MessageID: 10,
		Status: "migrated", Results: []DumpMigrateItemResult{
			{ChannelID: -200, MessageIDs: []int{1, 2}},
			{ChannelID: -300, MessageIDs: []int{5}},
		}}))
	must(s.InsertDumpMigrateItem(ctx, DumpMigrateItem{RunID: run.ID, ChannelKey: "news", MessageID: 11, Status: "skipped"}))
	must(s.InsertDumpMigrateItem(ctx, DumpMigrateItem{RunID: run.ID, ChannelKey: "news", MessageID: 12,
		Status: "failed", Results: []DumpMigrateItemResult{{ChannelID: -300, Error: "目标频道失权"}}, Error: "复制失败"}))

	got, err := s.GetDumpMigrateRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != DumpMigrateRunning || got.FromChannelID != -100 || got.Total != 3 ||
		got.Done != 1 || got.Skipped != 1 || got.Failed != 1 {
		t.Fatalf("run 聚合不符: %+v", got)
	}
	if len(got.To) != 2 || got.To[0] != -200 || got.To[1] != -300 {
		t.Fatalf("目标频道快照不符: %+v", got.To)
	}

	// 自然结束写 completed
	must(s.SetDumpMigrateFinished(ctx, run.ID, ""))
	got, _ = s.GetDumpMigrateRun(ctx, run.ID)
	if got.Status != DumpMigrateCompleted || got.FinishedAt == 0 {
		t.Fatalf("完成态不符: %+v", got)
	}
	// 终态后再转换一律冲突
	if err := s.SetDumpMigrateRunStatus(ctx, run.ID, DumpMigratePaused, ""); !isStoreConstraint(err) {
		t.Fatalf("终态再转换应冲突, got %v", err)
	}
}

func TestDumpMigratePauseResumeCancelTransitions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// pause 只能来自 running
	run := newMigrateRun(t, s, -100, []int64{-200}, 5)
	if err := s.SetDumpMigrateRunStatus(ctx, run.ID, DumpMigratePaused, ""); err != nil {
		t.Fatal(err)
	}
	// 重复暂停冲突
	if err := s.SetDumpMigrateRunStatus(ctx, run.ID, DumpMigratePaused, ""); !isStoreConstraint(err) {
		t.Fatalf("重复暂停应冲突, got %v", err)
	}
	// resume 只能来自 paused
	if err := s.SetDumpMigrateRunStatus(ctx, run.ID, DumpMigrateRunning, ""); err != nil {
		t.Fatal(err)
	}
	// resume 再 resume 冲突
	if err := s.SetDumpMigrateRunStatus(ctx, run.ID, DumpMigrateRunning, ""); !isStoreConstraint(err) {
		t.Fatalf("重复继续应冲突, got %v", err)
	}
	// cancel 可来自 running；finished_at 落值
	if err := s.SetDumpMigrateRunStatus(ctx, run.ID, DumpMigrateCancelled, "管理员取消"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetDumpMigrateRun(ctx, run.ID)
	if got.Status != DumpMigrateCancelled || got.LastError != "管理员取消" || got.FinishedAt == 0 {
		t.Fatalf("取消态不符: %+v", got)
	}
	// 取消后 goroutine 的完成写回是 no-op，不得翻转状态
	if err := s.SetDumpMigrateFinished(ctx, run.ID, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetDumpMigrateRun(ctx, run.ID)
	if got.Status != DumpMigrateCancelled {
		t.Fatalf("取消后完成写回应为 no-op: %+v", got)
	}

	// paused 态也可直接 cancel
	run2 := newMigrateRun(t, s, -101, []int64{-200}, 1)
	if err := s.SetDumpMigrateRunStatus(ctx, run2.ID, DumpMigratePaused, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDumpMigrateRunStatus(ctx, run2.ID, DumpMigrateCancelled, ""); err != nil {
		t.Fatal(err)
	}
}

func TestDumpMigrateListAndItemsFilter(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	run := newMigrateRun(t, s, -100, []int64{-200}, 3)
	for i, status := range []string{"migrated", "skipped", "failed", "migrated"} {
		if err := s.InsertDumpMigrateItem(ctx, DumpMigrateItem{RunID: run.ID, ChannelKey: "news",
			MessageID: 10 + i, Status: status}); err != nil {
			t.Fatal(err)
		}
	}

	runs, total, err := s.ListDumpMigrateRuns(ctx, 1, 20)
	if err != nil || total != 1 || len(runs) != 1 || runs[0].Done != 2 {
		t.Fatalf("运行列表不符: %+v total=%d err=%v", runs, total, err)
	}
	if _, err := s.LatestDumpMigrateRun(ctx); err != nil {
		t.Fatalf("最近运行应存在: %v", err)
	}

	// 状态筛选 + 分页
	items, total, err := s.ListDumpMigrateItems(ctx, run.ID, "migrated", 1, 1)
	if err != nil || total != 2 || len(items) != 1 || items[0].Status != "migrated" {
		t.Fatalf("明细筛选不符: %+v total=%d err=%v", items, total, err)
	}
	// 全量按 id 升序
	all, _, err := s.ListDumpMigrateItems(ctx, run.ID, "", 1, 20)
	if err != nil || len(all) != 4 || all[0].MessageID != 10 {
		t.Fatalf("明细全量不符: %+v err=%v", all, err)
	}
	// 非法状态受控拒绝
	if _, _, err := s.ListDumpMigrateItems(ctx, run.ID, "pending", 1, 20); apperr.From(err).Code != apperr.CodeInvalidURL {
		t.Fatalf("非法状态应拒绝, got %v", err)
	}
	// 不存在的 run 返回 ErrNotFound
	if _, err := s.GetDumpMigrateRun(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("无行应 ErrNotFound, got %v", err)
	}
}

func TestDumpMigrateReconcile(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	// 唯一活跃索引保证同时至多一个 running/paused：按顺序覆盖各前置态
	// ① paused 运行 → interrupted
	runPaused := newMigrateRun(t, s, -101, []int64{-200}, 1)
	if err := s.SetDumpMigrateRunStatus(ctx, runPaused.ID, DumpMigratePaused, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileDumpMigrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetDumpMigrateRun(ctx, runPaused.ID); got.Status != DumpMigrateInterrupted || got.FinishedAt == 0 {
		t.Fatalf("paused 运行应收敛为 interrupted: %+v", got)
	}

	// ② running 运行 → interrupted（此时前一运行已终态，可再创建活跃行）
	runLive := newMigrateRun(t, s, -100, []int64{-200}, 1)
	if err := s.ReconcileDumpMigrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetDumpMigrateRun(ctx, runLive.ID); got.Status != DumpMigrateInterrupted {
		t.Fatalf("running 运行应收敛为 interrupted: %+v", got)
	}

	// ③ 终态运行不被触碰
	runDone := newMigrateRun(t, s, -102, []int64{-200}, 1)
	if err := s.SetDumpMigrateFinished(ctx, runDone.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.ReconcileDumpMigrate(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetDumpMigrateRun(ctx, runDone.ID); got.Status != DumpMigrateCompleted {
		t.Fatalf("终态不应被触碰: %+v", got)
	}
}

func TestDumpMigrateRunPruneKeepsRecent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 先造 dumpMigrateKeepRuns 条终态运行
	for i := 0; i < dumpMigrateKeepRuns; i++ {
		run := newMigrateRun(t, s, int64(-100-i), []int64{-200}, 1)
		if err := s.InsertDumpMigrateItem(ctx, DumpMigrateItem{RunID: run.ID, ChannelKey: "news",
			MessageID: i, Status: "migrated"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetDumpMigrateFinished(ctx, run.ID, ""); err != nil {
			t.Fatal(err)
		}
	}
	// 再发起一条：最老的一条（含明细）应被清理，总数保持 dumpMigrateKeepRuns
	fresh := newMigrateRun(t, s, -999, []int64{-200}, 1)
	_, total, err := s.ListDumpMigrateRuns(ctx, 1, 100)
	if err != nil || total != dumpMigrateKeepRuns {
		t.Fatalf("保留数量不符: total=%d err=%v", total, err)
	}
	if got, err := s.GetDumpMigrateRun(ctx, 1); !errors.Is(err, ErrNotFound) || got.ID != 0 {
		t.Fatalf("最老运行应已清理: %+v %v", got, err)
	}
	// 最新运行仍在且为本次发起
	latest, err := s.LatestDumpMigrateRun(ctx)
	if err != nil || latest.ID != fresh.ID || latest.Status != DumpMigrateRunning {
		t.Fatalf("最新运行不符: %+v %v", latest, err)
	}
	// 被清理运行的明细级联删除
	if _, total, err := s.ListDumpMigrateItems(ctx, 1, "", 1, 20); err != nil || total != 0 {
		t.Fatalf("被清理运行明细应删除: total=%d err=%v", total, err)
	}
}

// isStoreConstraint 判定是否为受控 STORE_CONSTRAINT。
func isStoreConstraint(err error) bool {
	var ae *apperr.AppError
	return errors.As(err, &ae) && ae.Code == apperr.CodeStoreConstraint
}
