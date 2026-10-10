package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

func TestRecoveryCacheCountDoesNotProveSourceCoverage(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, err := s.InsertWatchEvent(ctx, WatchEvent{ChannelID: -100123, Username: "news", MessageID: 10, MemberIDs: []int{10, 11}, Path: WatchPathFallback})
	if err != nil {
		t.Fatal(err)
	}
	// Source 10 was split into two output documents. Source 11 has no cache.
	_, err = s.InsertDumpEntry(ctx, DumpEntry{ChannelKey: "news", MessageID: 10, DumpIDs: []int{100, 101}, DumpChannelID: -100999})
	if err != nil {
		t.Fatal(err)
	}
	items, _, err := s.RecoveryCandidates(ctx, RecoveryFilter{})
	if err != nil || len(items) != 1 || len(items[0].CacheCopies) != 0 || len(items[0].MemberIDs) != 2 {
		t.Fatalf("count promoted incomplete cache: %+v %v", items, err)
	}
}
func TestRecoveryUnknownSingletonMultiOutputCacheDropped(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 3)
	if _, err := s.ex.ExecContext(ctx, `INSERT INTO requests(user_id,source_kind,channel_key,message_id,status,requested_at) VALUES(3,'public','news',10,'succeeded',100)`); err != nil {
		t.Fatal(err)
	}
	// 无成员元数据的单消息历史被拆成两个输出：不能推断是相册还是分卷，弃用该缓存。
	if _, err := s.InsertDumpEntry(ctx, DumpEntry{ChannelKey: "news", MessageID: 10, DumpIDs: []int{100, 101}, DumpChannelID: -100999, CreatedAt: 90}); err != nil {
		t.Fatal(err)
	}
	items, _, err := s.RecoveryCandidates(ctx, RecoveryFilter{})
	if err != nil || len(items) != 1 || len(items[0].CacheCopies) != 0 || !reflect.DeepEqual(items[0].MemberIDs, []int{10}) {
		t.Fatalf("multi-output cache promoted: %+v %v", items, err)
	}
}
func TestRecoveryCreationCannotBypassUncertainOutput(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	j := recoveryFixture(t, s, 1)
	it, err := s.ClaimRecoveryItem(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	it.Status = "uncertain"
	it.SentIDs = []int{900}
	if err = s.FinishRecoveryItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRecoveryJobState(ctx, j.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateRecoveryJob(ctx, RecoveryJob{TargetChatID: j.TargetChatID, BotID: j.BotID}, []RecoveryItem{{ChannelKey: it.ChannelKey, MessageID: it.MessageID, MemberIDs: it.MemberIDs, CacheCopies: []RecoveryCopy{}}})
	if apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatalf("new job bypassed uncertain: %v", err)
	}
	_, total, err := s.ListRecoveryJobs(ctx, 1, 20)
	if err != nil || total != 1 {
		t.Fatalf("rejected create not atomic %d %v", total, err)
	}
}
func TestRecoveryMemberGrowthCannotBypassPriorOutput(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	j := recoveryFixture(t, s, 1)
	it, err := s.ClaimRecoveryItem(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	it.Status = "uncertain"
	it.SentIDs = []int{900}
	if err = s.FinishRecoveryItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRecoveryJobState(ctx, j.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	// 历史后补相册成员 [1,2]：身份仍是 news:1，交集命中旧 uncertain，必须拒绝。
	_, err = s.CreateRecoveryJob(ctx, RecoveryJob{TargetChatID: j.TargetChatID, BotID: j.BotID}, []RecoveryItem{{ChannelKey: "news", MessageID: 1, MemberIDs: []int{1, 2}, CacheCopies: []RecoveryCopy{}}})
	if apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatalf("grown members bypassed uncertain: %v", err)
	}
	// 部分 succeeded 重叠：旧 {2} 成功，新 [1,2] 须保持待处理（成员1仍未恢复，跳过会丢内容）。
	finishAt := func(target, bot int64, items []RecoveryItem, sent []int) {
		t.Helper()
		jb, err := s.CreateRecoveryJob(ctx, RecoveryJob{TargetChatID: target, BotID: bot}, items)
		if err != nil {
			t.Fatal(err)
		}
		ib, err := s.ClaimRecoveryItem(ctx, jb.ID)
		if err != nil {
			t.Fatal(err)
		}
		ib.Status = "succeeded"
		ib.SentIDs = sent
		if err = s.FinishRecoveryItem(ctx, ib); err != nil {
			t.Fatal(err)
		}
		if err = s.SetRecoveryJobState(ctx, jb.ID, "completed", ""); err != nil {
			t.Fatal(err)
		}
	}
	finishAt(-100998, 6, []RecoveryItem{{ChannelKey: "news", MessageID: 2, MemberIDs: []int{2}, CacheCopies: []RecoveryCopy{}}}, []int{800})
	j3, err := s.CreateRecoveryJob(ctx, RecoveryJob{TargetChatID: -100998, BotID: 6}, []RecoveryItem{{ChannelKey: "news", MessageID: 1, MemberIDs: []int{1, 2}, CacheCopies: []RecoveryCopy{}}})
	if err != nil {
		t.Fatal(err)
	}
	if j3.Pending != 1 || j3.Skipped != 0 {
		t.Fatalf("partial overlap must stay pending: %+v", j3)
	}
	// j3 保持待处理；单运行约束下先暂停，再继续下一段场景。
	if err = s.SetRecoveryJobState(ctx, j3.ID, "paused", ""); err != nil {
		t.Fatal(err)
	}
	// 全包含 succeeded：旧 [3,4] 成功后，其子集 [3] 整项跳过，不重发。
	finishAt(-100997, 7, []RecoveryItem{{ChannelKey: "news", MessageID: 3, MemberIDs: []int{3, 4}, CacheCopies: []RecoveryCopy{}}}, []int{700, 701})
	j5, err := s.CreateRecoveryJob(ctx, RecoveryJob{TargetChatID: -100997, BotID: 7}, []RecoveryItem{{ChannelKey: "news", MessageID: 3, MemberIDs: []int{3}, CacheCopies: []RecoveryCopy{}}})
	if err != nil {
		t.Fatal(err)
	}
	if j5.Skipped != 1 || j5.Pending != 0 {
		t.Fatalf("contained succeeded must skip: %+v", j5)
	}
}
func TestRecoveryClaimUsesMemberIntersection(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// 认领核对是创建与认领之间竞态的兜底。单运行器下合法 API 无法制造
	// 「创建后旧结果才覆盖同成员」的交错，这里在已完成任务的旧结果行上
	// 直接补写成员元数据，模拟创建之后才到达的历史信息。
	ja, err := s.CreateRecoveryJob(ctx, RecoveryJob{TargetChatID: -100999, BotID: 5}, []RecoveryItem{{ChannelKey: "news", MessageID: 9, MemberIDs: []int{9}, CacheCopies: []RecoveryCopy{}}})
	if err != nil {
		t.Fatal(err)
	}
	ia, err := s.ClaimRecoveryItem(ctx, ja.ID)
	if err != nil {
		t.Fatal(err)
	}
	ia.Status = "succeeded"
	ia.SentIDs = []int{444}
	if err = s.FinishRecoveryItem(ctx, ia); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRecoveryJobState(ctx, ja.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	// 创建新任务：此时旧结果成员是 [9]，与新候选 [1] 不相交，正常进入待处理。
	j, err := s.CreateRecoveryJob(ctx, RecoveryJob{TargetChatID: -100999, BotID: 5}, []RecoveryItem{{ChannelKey: "news", MessageID: 1, MemberIDs: []int{1}, CacheCopies: []RecoveryCopy{}}})
	if err != nil {
		t.Fatal(err)
	}
	if j.Pending != 1 {
		t.Fatalf("unrelated members must stay pending: %+v", j)
	}
	// 创建之后旧结果成员补写为 [1]：认领时按交集发现已完成输出并跳过。
	if _, err = s.ex.ExecContext(ctx, `UPDATE recovery_items SET member_ids_json='[1]' WHERE id=?`, ia.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.ClaimRecoveryItem(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "skipped" || len(got.SentIDs) != 1 || got.SentIDs[0] != 444 {
		t.Fatalf("claim ignored prior output: %+v", got)
	}
}
func TestRecoveryBackupSchemaValidation(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		ok        bool
	}{{"missing jobs", `DROP TABLE recovery_items;DROP TABLE recovery_jobs`, false}, {"missing coordinate column", `ALTER TABLE recovery_items DROP COLUMN cache_copies_json`, false}, {"genuine v25", `DROP TABLE recovery_items;DROP TABLE recovery_jobs;PRAGMA user_version=25`, true}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "backup.db")
			s, err := Open(ctx, path, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.ex.ExecContext(ctx, tc.sql); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			err = ValidateBackup(ctx, path)
			if (err == nil) != tc.ok {
				t.Fatalf("schema validation: %v", err)
			}
		})
	}
}

func TestRecoveryCandidatesHistoryUnion(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mustUser(t, s, 7)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := s.ex.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO requests(user_id,source_kind,channel_key,message_id,status,requested_at) VALUES(7,'public','@News',10,'succeeded',100),(7,'private','-10012345',11,'succeeded',110),(7,'public','other',99,'succeeded',300)`)
	if _, err := s.InsertWatchEvent(ctx, WatchEvent{ChannelID: -10012345, Username: "News", MessageID: 10, MemberIDs: []int{10, 11}, DumpIDs: []int{100, 101}, Path: WatchPathCopy, CreatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []DumpEntry{{ChannelKey: "news", MessageID: 10, DumpIDs: []int{100, 101}, DumpChannelID: -100999, CreatedAt: 90}, {ChannelKey: "-10012345", MessageID: 11, DumpIDs: []int{100, 101}, DumpChannelID: -100999, CreatedAt: 90}, {ChannelKey: "news", MessageID: 10, DumpIDs: []int{200, 201}, DumpChannelID: -100998, CreatedAt: 90}, {ChannelKey: "news", MessageID: 11, DumpIDs: []int{200, 201}, DumpChannelID: -100998, CreatedAt: 90}} {
		if _, err := s.InsertDumpEntry(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// Date/user scope selects a singleton request, but album metadata/cache rows
	// outside that date are still joined. No current config is involved.
	items, warnings, err := s.RecoveryCandidates(ctx, RecoveryFilter{ChannelKey: "@NEWS", UserID: 7, Since: 105, Until: 120})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ChannelKey != "12345" || !reflect.DeepEqual(items[0].MemberIDs, []int{10, 11}) || len(items[0].CacheCopies) != 2 {
		t.Fatalf("wrong union: %+v", items)
	}
	if len(warnings) == 0 {
		t.Fatal("missing warnings")
	}
	all, _, err := s.RecoveryCandidates(ctx, RecoveryFilter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("all history: %+v %v", all, err)
	}
}
func TestRecoveryLegacyCacheOwnershipNeverGuessed(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.InsertWatchEvent(ctx, WatchEvent{ChannelID: -100123, MessageID: 1, MemberIDs: []int{1, 2}, DumpIDs: []int{90, 91}, Path: WatchPathCopy}); err != nil {
		t.Fatal(err)
	}
	items, w, err := s.RecoveryCandidates(ctx, RecoveryFilter{})
	if err != nil || len(items) != 1 || len(items[0].CacheCopies) != 0 {
		t.Fatalf("unknown cache: %+v %v", items, err)
	}
	if !strings.Contains(strings.Join(w, ""), "归属未知") {
		t.Fatal(w)
	}
}
func recoveryFixture(t *testing.T, s *Store, n int) RecoveryJob {
	t.Helper()
	items := []RecoveryItem{}
	for id := 1; id <= n; id++ {
		ms := []int{id}
		items = append(items, RecoveryItem{ChannelKey: "news", MessageID: id, MemberIDs: ms, CacheCopies: []RecoveryCopy{}})
	}
	j, err := s.CreateRecoveryJob(context.Background(), RecoveryJob{TargetChatID: -100999, BotID: 5, TargetTitle: "target"}, items)
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func TestRecoveryLifecycleReconcileAndRetry(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	j := recoveryFixture(t, s, 3)
	it, err := s.ClaimRecoveryItem(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	it.Status = "succeeded"
	it.SentIDs = []int{30}
	it.Method = "cache"
	if err = s.FinishRecoveryItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	uncertain, err := s.ClaimRecoveryItem(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileRecovery(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRecoveryJob(ctx, j.ID)
	if err != nil || got.Status != "paused" || got.Succeeded != 1 || got.Uncertain != 1 || got.Pending != 1 {
		t.Fatalf("reconcile %+v %v", got, err)
	}
	if _, err = s.ControlRecoveryJob(ctx, j.ID, "retry"); apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatalf("uncertain retried: %v", err)
	}
	if _, err = s.ControlRecoveryJob(ctx, j.ID, "resume"); err != nil {
		t.Fatal(err)
	}
	next, err := s.ClaimRecoveryItem(ctx, j.ID)
	if err != nil || next.ID == it.ID || next.ID == uncertain.ID {
		t.Fatalf("replayed terminal %+v %v", next, err)
	}
	next.Status = "failed"
	next.SentIDs = []int{}
	if err = s.FinishRecoveryItem(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRecoveryJobState(ctx, j.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	got, err = s.ControlRecoveryJob(ctx, j.ID, "retry")
	if err != nil || got.Uncertain != 1 || got.Pending != 1 {
		t.Fatalf("safe retry %+v %v", got, err)
	}
}
func TestRecoveryConfirmedOutputsSkipAndBecomeCopies(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	j := recoveryFixture(t, s, 1)
	it, err := s.ClaimRecoveryItem(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	it.Status = "succeeded"
	it.SentIDs = []int{900}
	if err = s.FinishRecoveryItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRecoveryJobState(ctx, j.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	j2 := recoveryFixture(t, s, 1)
	if j2.Skipped != 1 {
		t.Fatalf("known output replays %+v", j2)
	}
	items := []RecoveryItem{{ChannelKey: it.ChannelKey, IdentityKey: it.IdentityKey, MemberIDs: []int{1}, CacheCopies: []RecoveryCopy{}}}
	if err = s.addRecoveryOutputs(ctx, items); err != nil || len(items[0].CacheCopies) != 1 || items[0].CacheCopies[0].ChatID != j.TargetChatID {
		t.Fatalf("output cache %+v %v", items, err)
	}
	var n int
	if err = s.ex.QueryRowContext(ctx, `SELECT COUNT(*) FROM dump_entries`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("ordinary index modified %d %v", n, err)
	}
}
func TestRecoveryAuditRollbackAndSQLFailures(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.ex.ExecContext(ctx, `CREATE TRIGGER reject_recovery_audit BEFORE INSERT ON audit_log WHEN NEW.action LIKE 'recovery.%' BEGIN SELECT RAISE(FAIL,'reject'); END`); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateRecoveryJob(ctx, RecoveryJob{TargetChatID: -1009, BotID: 1}, []RecoveryItem{{ChannelKey: "news", MessageID: 1, MemberIDs: []int{1}, CacheCopies: []RecoveryCopy{}, IdentityKey: "key"}})
	if apperr.From(err).Code != apperr.CodeStoreUnavailable {
		t.Fatal(err)
	}
	_, total, err := s.ListRecoveryJobs(ctx, 1, 20)
	if err != nil || total != 0 {
		t.Fatalf("audit rollback %d %v", total, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.RecoveryCandidates(ctx, RecoveryFilter{})
	if apperr.From(err).Code != apperr.CodeStoreUnavailable {
		t.Fatal(err)
	}
}
func TestRecoverySingleRunningAndClaim(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	j := recoveryFixture(t, s, 1)
	_, err := s.CreateRecoveryJob(ctx, RecoveryJob{TargetChatID: -1009, BotID: 1}, []RecoveryItem{{}})
	if apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatal(err)
	}
	it, err := s.ClaimRecoveryItem(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ClaimRecoveryItem(ctx, j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err = s.ControlRecoveryJob(ctx, j.ID, "pause"); apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatal(err)
	}
	it.Status = "pending"
	it.SentIDs = []int{}
	if err = s.FinishRecoveryItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ControlRecoveryJob(ctx, j.ID, "pause"); err != nil {
		t.Fatal(err)
	}
}
func TestRecoveryCandidateBoundsAndInvalidScope(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, f := range []RecoveryFilter{{Since: 200, Until: 100}, {UserID: -1}, {ChannelKey: "../../invalid"}} {
		if _, _, err := s.RecoveryCandidates(ctx, f); apperr.From(err).Code != apperr.CodeInvalidURL {
			t.Fatal(err)
		}
	}
	_, err := s.ex.ExecContext(ctx, `WITH RECURSIVE ids(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM ids WHERE n<?) INSERT INTO dump_entries(channel_key,message_id,dump_ids_json,format_version,created_at,dump_channel_id) SELECT 'bigscope',n,'[1]',1,100,-10099 FROM ids`, RecoveryCandidateLimit+1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.RecoveryCandidates(ctx, RecoveryFilter{ChannelKey: "bigscope"}); apperr.From(err).Code != apperr.CodeStoreConstraint {
		t.Fatalf("scope truncated %v", err)
	}
	small, _, err := s.RecoveryCandidates(ctx, RecoveryFilter{ChannelKey: "different"})
	if err != nil || len(small) != 0 {
		t.Fatalf("unrelated limit %d %v", len(small), err)
	}
}
