package web

// api_dumpcache_test.go — 缓存迁移 API 测试：认证/CSRF、依赖守卫、进度
// 合成视图（含兼容形状）、运行/明细分页与状态白名单、控制端点状态机与
// 审计。运行数据直接经测试库落（生产路径同源：store 层状态机）。

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// fakeDumpCache 是迁移工具的最小依赖假实现。
type fakeDumpCache struct {
	enabled  bool
	channels []int64
	from     int64
	err      error
	calls    int
}

func (f *fakeDumpCache) Enabled() bool { return f.enabled }
func (f *fakeDumpCache) Channels() ([]int64, bool) {
	return f.channels, len(f.channels) > 0
}
func (f *fakeDumpCache) StartMigrate(_ context.Context, from int64) error {
	f.calls++
	f.from = from
	return f.err
}

func newDumpMigrateEnv(t *testing.T, fake *fakeDumpCache) (*testEnv, *jar, string) {
	t.Helper()
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) { opt.DumpCache = fake })
	j := e.login(t)
	return e, j, apiCSRFToken(t, e, j)
}

// seedMigrateRun 落一条运行并推进到 paused/cancelled（running 即初始态）。
func seedMigrateRun(t *testing.T, e *testEnv, from int64, to []int64, status string) store.DumpMigrateRun {
	t.Helper()
	ctx := context.Background()
	run, err := e.st.CreateDumpMigrateRun(ctx, from, to, 5)
	if err != nil {
		t.Fatal(err)
	}
	switch status {
	case store.DumpMigrateRunning:
	case store.DumpMigratePaused:
		if err := e.st.SetDumpMigrateRunStatus(ctx, run.ID, store.DumpMigratePaused, ""); err != nil {
			t.Fatal(err)
		}
	case store.DumpMigrateCancelled:
		if err := e.st.SetDumpMigrateRunStatus(ctx, run.ID, store.DumpMigrateCancelled, "管理员取消"); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("测试不支持构造状态 %s", status)
	}
	return run
}

func TestAPIDumpMigrateAuthAndDependencyGate(t *testing.T) {
	e, j, csrf := newDumpMigrateEnv(t, &fakeDumpCache{channels: []int64{-100222}})
	for _, path := range []string{
		"/api/v1/dumpcache/migrate", "/api/v1/dumpcache/migrate/runs",
		"/api/v1/dumpcache/migrate/runs/1", "/api/v1/dumpcache/migrate/runs/1/items",
	} {
		requireAPIStatus(t, e.do(&jar{}, http.MethodGet, path, "", ""), path, http.StatusUnauthorized)
	}
	for _, path := range []string{
		"/api/v1/dumpcache/migrate", "/api/v1/dumpcache/migrate/pause",
		"/api/v1/dumpcache/migrate/resume", "/api/v1/dumpcache/migrate/cancel",
	} {
		requireAPIStatus(t, e.apiPost(&jar{}, path, "", `{}`), path, http.StatusUnauthorized)
		requireAPIStatus(t, e.apiPost(j, path, "wrong", `{}`), path, http.StatusForbidden)
	}
	// 依赖缺失（MTProto 未就绪）：全部迁移端点受控 503
	e.srv.dumpCache = nil
	requireAPIStatus(t, e.do(j, http.MethodGet, "/api/v1/dumpcache/migrate", "", ""), "progress", http.StatusServiceUnavailable)
	requireAPIStatus(t, e.apiPost(j, "/api/v1/dumpcache/migrate/pause", csrf, `{}`), "pause", http.StatusServiceUnavailable)
}

func TestAPIDumpMigrateProgressCompatView(t *testing.T) {
	e, j, _ := newDumpMigrateEnv(t, &fakeDumpCache{channels: []int64{-100222}})
	ctx := context.Background()

	// 从未发起：progress 零视图但字段齐全（旧形状兼容）
	var view struct {
		Configured bool                    `json:"configured"`
		Progress   dumpMigrateProgressView `json:"progress"`
	}
	getAPIJSON(t, e, j, "/api/v1/dumpcache/migrate", &view)
	if !view.Configured || view.Progress.Running || view.Progress.RunID != 0 || view.Progress.To == nil {
		t.Fatalf("零视图不符: %+v", view)
	}

	// 有运行：进度由运行行合成（paused 状态、聚合计数）
	run := seedMigrateRun(t, e, -100111, []int64{-100222}, store.DumpMigratePaused)
	for i := 0; i < 2; i++ {
		if err := e.st.InsertDumpMigrateItem(ctx, store.DumpMigrateItem{
			RunID: run.ID, ChannelKey: "news", MessageID: 100 + i, Status: "migrated"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.st.InsertDumpMigrateItem(ctx, store.DumpMigrateItem{
		RunID: run.ID, ChannelKey: "news", MessageID: 102, Status: "failed", Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	getAPIJSON(t, e, j, "/api/v1/dumpcache/migrate", &view)
	p := view.Progress
	if !p.Paused || p.Running || p.RunID != run.ID || p.From != -100111 || p.Total != 5 ||
		p.Done != 2 || p.Failed != 1 || p.Skipped != 0 || p.StartedAt == 0 {
		t.Fatalf("进度合成不符: %+v", p)
	}
	if len(p.To) != 1 || p.To[0] != -100222 {
		t.Fatalf("目标快照不符: %+v", p.To)
	}
}

func TestAPIDumpMigrateStartRejectsAndCreates(t *testing.T) {
	fake := &fakeDumpCache{channels: []int64{-100222}}
	e, j, csrf := newDumpMigrateEnv(t, fake)

	// 空来源 → 400；服务拒绝（已在进行中）→ 400 + 受控文案
	requireAPIStatus(t, e.apiPost(j, "/api/v1/dumpcache/migrate", csrf, `{}`), "start", http.StatusBadRequest)
	fake.err = context.DeadlineExceeded
	requireAPIStatus(t, e.apiPost(j, "/api/v1/dumpcache/migrate", csrf, `{"from_channel_id":-100111}`), "start", http.StatusBadRequest)
	if fake.calls != 1 || fake.from != -100111 {
		t.Fatalf("发起参数不符: %+v", fake)
	}

	// 成功发起：200 + {ok, progress}（旧形状）
	fake.err = nil
	run := seedMigrateRun(t, e, -100111, []int64{-100222}, store.DumpMigrateRunning)
	fake.calls = 0
	resp := e.apiPost(j, "/api/v1/dumpcache/migrate", csrf, `{"from_channel_id":-100111}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("发起失败: %s", bodyOf(t, resp))
	}
	var out struct {
		OK       bool                    `json:"ok"`
		Progress dumpMigrateProgressView `json:"progress"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || out.Progress.RunID != run.ID || !out.Progress.Running {
		t.Fatalf("发起响应不符: %+v", out)
	}
}

func TestAPIDumpMigrateRunsAndItems(t *testing.T) {
	e, j, _ := newDumpMigrateEnv(t, &fakeDumpCache{channels: []int64{-100222}})
	ctx := context.Background()
	run := seedMigrateRun(t, e, -100111, []int64{-100222}, store.DumpMigrateRunning)
	for i, status := range []string{"migrated", "skipped", "failed"} {
		if err := e.st.InsertDumpMigrateItem(ctx, store.DumpMigrateItem{
			RunID: run.ID, ChannelKey: "news", MessageID: 100 + i, Status: status}); err != nil {
			t.Fatal(err)
		}
	}

	var runs apiListEnvelope[store.DumpMigrateRun]
	getAPIJSON(t, e, j, "/api/v1/dumpcache/migrate/runs?page=1&page_size=10", &runs)
	if runs.Total != 1 || len(runs.Items) != 1 || runs.Items[0].ID != run.ID || runs.Items[0].Done != 1 {
		t.Fatalf("运行列表不符: %+v", runs)
	}

	var single store.DumpMigrateRun
	getAPIJSON(t, e, j, "/api/v1/dumpcache/migrate/runs/"+strconv.FormatInt(run.ID, 10), &single)
	if single.ID != run.ID || single.Failed != 1 || single.Skipped != 1 {
		t.Fatalf("运行详情不符: %+v", single)
	}

	var items apiListEnvelope[store.DumpMigrateItem]
	getAPIJSON(t, e, j, "/api/v1/dumpcache/migrate/runs/"+strconv.FormatInt(run.ID, 10)+"/items?status=failed", &items)
	if items.Total != 1 || len(items.Items) != 1 || items.Items[0].Status != "failed" {
		t.Fatalf("明细筛选不符: %+v", items)
	}
	// 非法状态 → 400；不存在的运行 → 404
	requireAPIStatus(t, e.do(j, http.MethodGet, "/api/v1/dumpcache/migrate/runs/"+strconv.FormatInt(run.ID, 10)+"/items?status=pending", "", ""), "items", http.StatusBadRequest)
	requireAPIStatus(t, e.do(j, http.MethodGet, "/api/v1/dumpcache/migrate/runs/9999", "", ""), "run404", http.StatusNotFound)
}

func TestAPIDumpMigrateControls(t *testing.T) {
	e, j, csrf := newDumpMigrateEnv(t, &fakeDumpCache{channels: []int64{-100222}})
	ctx := context.Background()

	// 无运行 → 409（状态冲突，与设计/同类控制端点约定一致）
	requireAPIStatus(t, e.apiPost(j, "/api/v1/dumpcache/migrate/pause", csrf, `{}`), "pause-empty", http.StatusConflict)

	// running → pause ✓ → resume ✓ → cancel ✓（各写审计）
	run := seedMigrateRun(t, e, -100111, []int64{-100222}, store.DumpMigrateRunning)
	for _, step := range []struct{ action, want string }{
		{"pause", store.DumpMigratePaused}, {"resume", store.DumpMigrateRunning}, {"cancel", store.DumpMigrateCancelled},
	} {
		resp := e.apiPost(j, "/api/v1/dumpcache/migrate/"+step.action, csrf, `{}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s 失败: %s", step.action, bodyOf(t, resp))
		}
		var out store.DumpMigrateRun
		decodeAPIJSON(t, bodyOf(t, resp), &out)
		if out.Status != step.want {
			t.Fatalf("%s 后状态 %s ≠ %s", step.action, out.Status, step.want)
		}
		if !e.containsAction("dumpcache.migrate") {
			t.Fatalf("%s 应写审计", step.action)
		}
	}
	if got, _ := e.st.DumpMigrateRunStatusOf(ctx, run.ID); got != store.DumpMigrateCancelled {
		t.Fatalf("终态不符: %s", got)
	}
	// 终态后再控制 → 409（当前迁移状态不允许…）
	requireAPIStatus(t, e.apiPost(j, "/api/v1/dumpcache/migrate/pause", csrf, `{}`), "pause-terminal", http.StatusConflict)
	// 状态不匹配（completed 运行上 resume）→ 409
	requireAPIStatus(t, e.apiPost(j, "/api/v1/dumpcache/migrate/resume", csrf, `{}`), "resume-terminal", http.StatusConflict)
}
