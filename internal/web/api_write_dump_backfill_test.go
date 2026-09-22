package web

// 缓存补写（转存缓存频道）API 契约测试：单条与批量的资格矩阵
//（not_found / not_finished / already_dumped / dump_disabled）、
// 队列满收尾、审计留痕。未认证/CSRF 分支沿用 api.go 契约。

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// testDumpChannelID 与缓存补写测试环境配套的缓存频道数字 ID。
const testDumpChannelID int64 = -1001234567890

// newDumpTestEnv 装配已配置缓存频道的测试环境（dumpDisabled=false 时）。
func newDumpTestEnv(t *testing.T, dumpDisabled bool, queueOpts ...*queue.Queue) *testEnv {
	t.Helper()
	return newTestEnvOpts(t, func(cfg *config.Config, opt *Options) {
		if !dumpDisabled {
			cfg.DumpChannelID = testDumpChannelID
		}
		if len(queueOpts) > 0 {
			opt.Queue = queueOpts[0]
		}
	})
}

// TestAPIDumpBackfillUnauthenticated：两个写端点未认证一律 401 JSON；
// GET 打写端点按 mountAPIWrite 契约返回 405 JSON（优先于认证检查）。
func TestAPIDumpBackfillUnauthenticated(t *testing.T) {
	e := newDumpTestEnv(t, false)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/requests/1/dump-backfill"},
		{http.MethodPost, "/api/v1/requests/dump-backfill-batch"},
	} {
		req, err := http.NewRequest(tc.method, e.ts.URL+tc.path, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("未认证 %s %s 应返回 401，得到 %d", tc.method, tc.path, resp.StatusCode)
		}
		bodyOf(t, resp)
	}
	req, err := http.NewRequest(http.MethodGet, e.ts.URL+"/api/v1/requests/1/dump-backfill", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET 打写端点应返回 405，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

// TestAPIRequestDumpBackfillSuccess：终态请求单条补写 → 200 + 新行
// （dump 投递方式、parent 指向原行）+ 审计留痕。
func TestAPIRequestDumpBackfillSuccess(t *testing.T) {
	e := newDumpTestEnv(t, false)
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	seedUser(t, e, 815, store.UserEnabled)
	src := seedFinishedRequest(t, e, 815, "chan", 16, store.RequestSucceeded, store.DeliveryModeUpload)

	resp := e.apiPost(j, "/api/v1/requests/"+strconv.FormatInt(src.ID, 10)+"/dump-backfill", csrf, "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("单条补写应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK               bool  `json:"ok"`
		CreatedRequestID int64 `json:"created_request_id"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || out.CreatedRequestID == 0 {
		t.Fatalf("应建行: %+v", out)
	}
	row, err := e.st.GetRequest(context.Background(), out.CreatedRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RequestQueued || row.DeliveryMode != store.DeliveryModeDump ||
		row.ParentRequestID != src.ID {
		t.Errorf("新行字段不符: %+v", row)
	}
	assertAuditAction(t, e, "request.dump_backfill")
}

// TestAPIRequestDumpBackfillSkipMatrix：单条跳过原因 → 错误信封状态码。
func TestAPIRequestDumpBackfillSkipMatrix(t *testing.T) {
	e := newDumpTestEnv(t, false)
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	seedUser(t, e, 816, store.UserEnabled)

	// not_found → 404
	resp := e.apiPost(j, "/api/v1/requests/9999/dump-backfill", csrf, "{}")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在请求应 404，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	// not_finished → 409
	queued := seedRequest(t, e, 816, "chan", 11, store.RequestQueued)
	resp = e.apiPost(j, "/api/v1/requests/"+strconv.FormatInt(queued.ID, 10)+"/dump-backfill", csrf, "{}")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("未终态应 409，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	// already_dumped → 409
	src := seedFinishedRequest(t, e, 816, "chan", 17, store.RequestSucceeded, store.DeliveryModeUpload)
	if _, err := e.st.InsertDumpEntry(context.Background(), store.DumpEntry{
		ChannelKey: src.ChannelKey, MessageID: src.MessageID, DumpIDs: []int{501},
		DumpChannelID: -100777,
	}); err != nil {
		t.Fatalf("落缓存条目失败: %v", err)
	}
	resp = e.apiPost(j, "/api/v1/requests/"+strconv.FormatInt(src.ID, 10)+"/dump-backfill", csrf, "{}")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("已有副本应 409，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	// dump_disabled → 503（缓存频道未配置）
	e2 := newDumpTestEnv(t, true)
	j2 := e2.login(t)
	csrf2 := e2.sessionCSRF(t, j2)
	seedUser(t, e2, 817, store.UserEnabled)
	fin := seedFinishedRequest(t, e2, 817, "chan", 18, store.RequestSucceeded, store.DeliveryModeUpload)
	resp = e2.apiPost(j2, "/api/v1/requests/"+strconv.FormatInt(fin.ID, 10)+"/dump-backfill", csrf2, "{}")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("缓存频道未配置应 503，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

// TestAPIRequestsDumpBackfillBatch：数量边界 + 混合批次逐条摘要。
func TestAPIRequestsDumpBackfillBatch(t *testing.T) {
	e := newDumpTestEnv(t, false)
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	seedUser(t, e, 818, store.UserEnabled)

	post := func(body string) *http.Response {
		t.Helper()
		return e.apiPost(j, "/api/v1/requests/dump-backfill-batch", csrf, body)
	}

	// 数量边界
	resp := post(`{"request_ids":[]}`)
	requireBadRequest(t, resp, "batch")
	ids := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		ids = append(ids, strconv.Itoa(i+1))
	}
	resp = post(`{"request_ids":[` + strings.Join(ids, ",") + `]}`)
	requireBadRequest(t, resp, "batch")
	resp = post(`{"request_ids":[0,1]}`)
	requireBadRequest(t, resp, "batch")

	// 混合批次：不存在 / 未终态 / 纯文本可补写 / 已有副本跳过 / 可补写
	queued := seedRequest(t, e, 818, "chan", 11, store.RequestQueued)
	text := seedFinishedRequest(t, e, 818, "chan", 12, store.RequestSucceeded, store.DeliveryModeText)
	duped := seedFinishedRequest(t, e, 818, "chan", 13, store.RequestCancelled, store.DeliveryModeUpload)
	if _, err := e.st.InsertDumpEntry(context.Background(), store.DumpEntry{
		ChannelKey: duped.ChannelKey, MessageID: duped.MessageID, DumpIDs: []int{502},
		DumpChannelID: -100777,
	}); err != nil {
		t.Fatalf("落缓存条目失败: %v", err)
	}
	ok := seedFinishedRequest(t, e, 818, "chan", 14, store.RequestSucceeded, store.DeliveryModeUpload)

	body := `{"request_ids":[9999,` + strconv.FormatInt(queued.ID, 10) + `,` +
		strconv.FormatInt(text.ID, 10) + `,` + strconv.FormatInt(duped.ID, 10) + `,` +
		strconv.FormatInt(ok.ID, 10) + `]}`
	resp = post(body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批量应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK      bool                       `json:"ok"`
		Results []apiDumpBackfillBatchItem `json:"results"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || len(out.Results) != 5 {
		t.Fatalf("应返回 5 条摘要: %+v", out)
	}
	wantSkips := map[int64]string{
		9999:      "not_found",
		queued.ID: "not_finished",
		duped.ID:  "already_dumped",
	}
	created := map[int64]bool{text.ID: true, ok.ID: true}
	for _, item := range out.Results {
		if want, has := wantSkips[item.RequestID]; has {
			if item.SkipReason != want || item.CreatedRequestID != 0 {
				t.Errorf("请求 %d 应 skip %s: %+v", item.RequestID, want, item)
			}
			continue
		}
		if created[item.RequestID] {
			if item.SkipReason != "" || item.CreatedRequestID == 0 || item.QueueFull {
				t.Errorf("可补写条目应建行: %+v", item)
			}
		}
	}
}

// TestAPIRequestsDumpBackfillBatchQueueFull：容量 0 队列下逐条建行并标记
// QUEUE_FULL，摘要携带 queue_full=true，新行保持 dump 投递方式。
func TestAPIRequestsDumpBackfillBatchQueueFull(t *testing.T) {
	e := newDumpTestEnv(t, false, queue.New(0))
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	seedUser(t, e, 819, store.UserEnabled)
	r := seedFinishedRequest(t, e, 819, "chan", 19, store.RequestSucceeded, store.DeliveryModeUpload)

	resp := e.apiPost(j, "/api/v1/requests/dump-backfill-batch", csrf,
		`{"request_ids":[`+strconv.FormatInt(r.ID, 10)+`]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批量队列满不是请求级错误，应 200，得到 %d（body=%s）",
			resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK      bool                       `json:"ok"`
		Results []apiDumpBackfillBatchItem `json:"results"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if len(out.Results) != 1 || !out.Results[0].QueueFull || out.Results[0].CreatedRequestID == 0 {
		t.Fatalf("应建行并标记队列满: %+v", out.Results)
	}
	row, err := e.st.GetRequest(context.Background(), out.Results[0].CreatedRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RequestFailed || row.ErrorCode != "QUEUE_FULL" ||
		row.DeliveryMode != store.DeliveryModeDump {
		t.Errorf("行应标记 QUEUE_FULL/dump: %+v", row)
	}
}

// assertAuditAction 断言审计日志中存在指定动作（成功动作必须写审计）。
func assertAuditAction(t *testing.T, e *testEnv, action string) {
	t.Helper()
	entries, err := e.st.ListAudit(context.Background(), 50, 0)
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	for _, entry := range entries {
		if entry.Action == action {
			return
		}
	}
	t.Fatalf("审计中应存在动作 %s", action)
}
