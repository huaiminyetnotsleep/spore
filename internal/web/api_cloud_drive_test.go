package web

// 云盘下载 API 契约测试：配置视图/保存校验文案/连通性测试、
// 单条与批量补存的资格矩阵与队列满收尾、请求详情附 cloud_uploads。
// 未认证/CSRF 分支沿用 api.go 契约，此处按新端点逐一覆盖。

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// fakeCloudSink 是云盘端点测试用的 Sink 桩：Ping 可注入失败。
type fakeCloudSink struct {
	pingErr error
}

func (f *fakeCloudSink) Upload(context.Context, cloudarchive.Destination, cloudarchive.UploadSpec) error {
	return nil
}

func (f *fakeCloudSink) Ping(context.Context, cloudarchive.Destination) error {
	return f.pingErr
}

func (f *fakeCloudSink) VerifyUploaded(context.Context, cloudarchive.Destination, []string) (bool, error) {
	return false, nil
}

// setRcloneBin 让 cloudarchive.BinPath 的探测结果确定化：available=true 时
// 指向一个存在的可执行占位文件（探测只 stat 不执行），false 时指向缺失路径。
func setRcloneBin(t *testing.T, available bool) {
	t.Helper()
	if available {
		p := filepath.Join(t.TempDir(), "rclone")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("创建 rclone 占位失败: %v", err)
		}
		t.Setenv("RCLONE_BIN", p)
		return
	}
	t.Setenv("RCLONE_BIN", filepath.Join(t.TempDir(), "missing-rclone"))
}

// newCloudTestEnv 装配带云盘配置管理器与 Sink 桩的测试环境。
func newCloudTestEnv(t *testing.T, cfg cloudarchive.Config, sink cloudarchive.Sink) *testEnv {
	t.Helper()
	setRcloneBin(t, true)
	return newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		mgr := cloudarchive.NewManager(filepath.Join(t.TempDir(), cloudarchive.FileName), testLogger())
		if cfg.Enabled || len(cfg.Destinations) > 0 {
			if err := mgr.Save(cfg); err != nil {
				t.Fatalf("预置云盘配置失败: %v", err)
			}
		}
		opt.CloudCfg = mgr
		opt.CloudSink = sink
	})
}

// enabledCloudCfg 返回合法的已开启配置（目的地 mega-1）。
func enabledCloudCfg() cloudarchive.Config {
	return cloudarchive.Config{
		Enabled:            true,
		DefaultDestination: "mega-1",
		Destinations: []cloudarchive.Destination{{
			Name: "mega-1", Type: "mega", PathPrefix: "spore", Enabled: true,
			Options: map[string]string{"user": "bot@example.com", "pass": "obscured-secret"},
		}},
	}
}

// seedFinishedRequest 建一条指定终态与投递方式的请求（cancelled 经
// CancelRequest 条件更新落库）。
func seedFinishedRequest(t *testing.T, e *testEnv, userID int64, channel string, msgID int, status, mode string) store.Request {
	t.Helper()
	r := seedRequest(t, e, userID, channel, msgID, status)
	switch status {
	case store.RequestQueued, store.RequestProcessing:
		return r
	case store.RequestCancelled:
		if _, err := e.st.CancelRequest(context.Background(), r.ID, 0); err != nil {
			t.Fatalf("置为取消失败: %v", err)
		}
	default:
		if err := e.st.FinishRequest(context.Background(), r.ID, store.RequestResult{
			Status: status, DeliveryMode: mode, MediaType: "photo",
		}); err != nil {
			t.Fatalf("落库终态失败: %v", err)
		}
	}
	r.DeliveryMode = mode
	return r
}

// ---- 未认证 ----

func TestAPICloudDriveUnauthenticated(t *testing.T) {
	e := newCloudTestEnv(t, cloudarchive.Config{}, &fakeCloudSink{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/cloud-drive"},
		{http.MethodPut, "/api/v1/cloud-drive"},
		{http.MethodPost, "/api/v1/cloud-drive/test"},
		{http.MethodPost, "/api/v1/requests/1/cloud-archive"},
		{http.MethodPost, "/api/v1/requests/cloud-archive-batch"},
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
}

// ---- E1：配置视图与保存 ----

func TestAPICloudDriveGetInitialView(t *testing.T) {
	e := newCloudTestEnv(t, cloudarchive.Config{}, &fakeCloudSink{})
	j := e.login(t)

	var view apiCloudDriveView
	body := getAPIJSON(t, e, j, "/api/v1/cloud-drive", &view)
	if view.Enabled || view.DefaultDestination != "" || len(view.Destinations) != 0 {
		t.Errorf("未配置时应为零值视图: %+v", view)
	}
	if !view.RcloneAvailable {
		t.Error("占位 rclone 存在时 rclone_available 应为 true")
	}
	// 契约字段恰为四个
	var raw map[string]any
	decodeAPIJSON(t, body, &raw)
	for _, key := range []string{"enabled", "default_destination", "rclone_available", "destinations"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("视图缺少字段 %q（body=%s）", key, body)
		}
	}
	if len(raw) != 4 {
		t.Errorf("视图应只含 4 个顶层字段，得到 %d", len(raw))
	}
}

func TestAPICloudDrivePutFlow(t *testing.T) {
	e := newCloudTestEnv(t, cloudarchive.Config{}, &fakeCloudSink{})
	bin := filepath.Join(t.TempDir(), "rclone-obscure")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nif [ \"$1\" = \"obscure\" ]; then cat >/dev/null; printf 'obscured-from-api\\n'; exit 0; fi\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("创建假 rclone 失败: %v", err)
	}
	t.Setenv("RCLONE_BIN", bin)
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	put := func(body string, token string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPut, e.ts.URL+"/api/v1/cloud-drive", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set(apiCSRFHeader, token)
		}
		if h := j.header(); h != "" {
			req.Header.Set("Cookie", h)
		}
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// CSRF 缺失 → 403
	resp := put(`{"enabled":false}`, "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("PUT 缺 CSRF 应 403，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeCSRFFailed)

	// 校验失败：名称不合法 + 默认目的地悬空（enabled=true 门禁）
	resp = put(`{"enabled":true,"default_destination":"mega-1","destinations":[
		{"name":"Bad_Name","type":"mega","enabled":true,"options":{}}
	]}`, csrf)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法配置应 400，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var env apiErrorEnvelope
	decodeAPIJSON(t, bodyOf(t, resp), &env)
	if env.Error.Code != apiCodeBadRequest || env.Error.Message == "" {
		t.Errorf("校验失败应带受控中文文案: %+v", env.Error)
	}
	if !strings.Contains(env.Error.Message, "名称不合法") {
		t.Errorf("文案应指出名称问题，得到 %q", env.Error.Message)
	}

	// 合法草稿（enabled=false 允许不完整）→ 200 + 保存后视图
	resp = put(`{"enabled":false,"default_destination":"","destinations":[
		{"name":"mega-1","type":"mega","path_prefix":"spore","enabled":true,
			"options":{"user":"bot@example.com","pass":"p@ss&$quoted"}}
	]}`, csrf)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("合法草稿应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var saved struct {
		OK     bool              `json:"ok"`
		Config apiCloudDriveView `json:"config"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &saved)
	if !saved.OK || len(saved.Config.Destinations) != 1 {
		t.Fatalf("保存响应应带 ok 与视图: %+v", saved)
	}
	d := saved.Config.Destinations[0]
	if d.Options["pass"] != cloudSecretMask || d.PathPrefix != "spore" {
		t.Errorf("敏感 options 应掩码且前缀应回显: %+v", d)
	}
	// 管理台未修改敏感字段时会回传掩码；服务端应沿用原始凭据。
	resp = put(`{"enabled":false,"default_destination":"","destinations":[
		{"name":"mega-1","type":"mega","path_prefix":"spore","enabled":true,
		 "options":{"user":"bot@example.com","pass":"********"}}
	]}`, csrf)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("掩码回传保存应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if got := e.srv.cloudCfg.Snapshot().Destinations[0].Options["pass"]; got != "obscured-from-api" {
		t.Fatalf("掩码回传应保留已混淆凭据，得到 %q", got)
	}

	// GET 反映保存结果；审计留痕且不含 options 值
	var view apiCloudDriveView
	getAPIJSON(t, e, j, "/api/v1/cloud-drive", &view)
	if view.Enabled || len(view.Destinations) != 1 || view.Destinations[0].Name != "mega-1" {
		t.Errorf("GET 应反映保存后的配置: %+v", view)
	}
	entries, err := e.st.ListAudit(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, en := range entries {
		if en.Action != "cloud_drive.update" {
			continue
		}
		found = true
		if strings.Contains(en.BeforeJSON+en.AfterJSON, "obscured-from-api") ||
			strings.Contains(en.BeforeJSON+en.AfterJSON, "p@ss&$quoted") ||
			strings.Contains(en.BeforeJSON+en.AfterJSON, "bot@example.com") {

			t.Errorf("审计不得包含 options 值: %s / %s", en.BeforeJSON, en.AfterJSON)
		}
	}
	if !found {
		t.Error("保存配置应写 cloud_drive.update 审计")
	}

	before, err := os.ReadFile(e.srv.cloudCfg.Path())
	if err != nil {
		t.Fatalf("读取失败前配置快照失败: %v", err)
	}
	failBin := filepath.Join(t.TempDir(), "rclone-obscure-fail")
	if err := os.WriteFile(failBin, []byte("#!/bin/sh\ncat >/dev/null\nexit 7\n"), 0o755); err != nil {
		t.Fatalf("创建失败用假 rclone 失败: %v", err)
	}
	t.Setenv("RCLONE_BIN", failBin)
	resp = put(`{"enabled":false,"default_destination":"","destinations":[
		{"name":"mega-1","type":"mega","path_prefix":"spore","enabled":true,
			"options":{"user":"bot@example.com","pass":"new-secret&$"}}
	]}`, csrf)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("obscure 失败应 503，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	bodyOf(t, resp)
	after, err := os.ReadFile(e.srv.cloudCfg.Path())
	if err != nil {
		t.Fatalf("读取失败后配置快照失败: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("obscure 失败时不得写入配置文件")
	}
	if got := e.srv.cloudCfg.Snapshot().Destinations[0].Options["pass"]; got != "obscured-from-api" {
		t.Fatalf("obscure 失败时内存快照不得改变，得到 %q", got)
	}
}

func TestAPICloudDriveTestEndpoint(t *testing.T) {
	e := newCloudTestEnv(t, enabledCloudCfg(), &fakeCloudSink{})
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)

	post := func(body string) *http.Response {
		t.Helper()
		return e.apiPost(j, "/api/v1/cloud-drive/test", csrf, body)
	}

	// 成功
	resp := post(`{"name":"mega-1"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("测试成功应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || out.Message == "" {
		t.Errorf("成功应 ok=true 且带确认文案: %+v", out)
	}

	// 目的地不存在 → 400
	resp = post(`{"name":"ghost"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("目的地不存在应 400，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeBadRequest)

	// 失败：分类中文原因，不透出底层错误细节
	failSink := &fakeCloudSink{pingErr: apperr.New(apperr.CodeCloudAuthFailed, "rclone 退出码 4: user=bot pass=obscured-secret")}
	e.srv.cloudSink = failSink
	resp = post(`{"name":"mega-1"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("测试失败仍应 200 信封，得到 %d", resp.StatusCode)
	}
	body := bodyOf(t, resp)
	decodeAPIJSON(t, body, &out)
	if out.OK {
		t.Error("失败应为 ok=false")
	}
	if want := apperr.UserText(apperr.CodeCloudAuthFailed); out.Message != want {
		t.Errorf("失败文案应为 %q，得到 %q", want, out.Message)
	}
	for _, secret := range []string{"obscured-secret", "退出码"} {
		if strings.Contains(body, secret) {
			t.Errorf("测试失败响应不得透出底层细节 %q：%s", secret, body)
		}
	}
}

// ---- E2：单条补存资格矩阵 ----

func TestAPIRequestCloudArchiveMatrix(t *testing.T) {
	e := newCloudTestEnv(t, enabledCloudCfg(), &fakeCloudSink{})
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	seedUser(t, e, 801, store.UserEnabled)

	post := func(path, body string) *http.Response {
		t.Helper()
		return e.apiPost(j, path, csrf, body)
	}

	t.Run("ID 非法", func(t *testing.T) {
		resp := post("/api/v1/requests/abc/cloud-archive", `{}`)
		requireBadRequest(t, resp, "cloud-archive")
	})

	t.Run("请求不存在 404", func(t *testing.T) {
		resp := post("/api/v1/requests/9999/cloud-archive", `{}`)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("应 404，得到 %d", resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeNotFound)
	})

	t.Run("未终态 409", func(t *testing.T) {
		r := seedRequest(t, e, 801, "chan", 1, store.RequestQueued)
		resp := post("/api/v1/requests/"+strconv.FormatInt(r.ID, 10)+"/cloud-archive", `{}`)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("应 409，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), string(apperr.CodeStoreConstraint))
	})

	t.Run("纯文本 409", func(t *testing.T) {
		r := seedFinishedRequest(t, e, 801, "chan", 2, store.RequestSucceeded, store.DeliveryModeText)
		resp := post("/api/v1/requests/"+strconv.FormatInt(r.ID, 10)+"/cloud-archive", `{}`)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("应 409，得到 %d", resp.StatusCode)
		}
		bodyOf(t, resp)
	})

	t.Run("云盘未开启 503", func(t *testing.T) {
		e2 := newCloudTestEnv(t, cloudarchive.Config{}, &fakeCloudSink{})
		j2 := e2.login(t)
		csrf2 := e2.sessionCSRF(t, j2)
		seedUser(t, e2, 802, store.UserEnabled)
		r := seedFinishedRequest(t, e2, 802, "chan", 3, store.RequestSucceeded, store.DeliveryModeReference)
		// 预置一个已启用目的地但总开关关闭（destination 解析可通过）
		if err := e2.srv.cloudCfg.Save(cloudarchive.Config{
			Enabled:            false,
			DefaultDestination: "mega-1",
			Destinations:       enabledCloudCfg().Destinations,
		}); err != nil {
			t.Fatal(err)
		}
		resp := e2.apiPost(j2, "/api/v1/requests/"+strconv.FormatInt(r.ID, 10)+"/cloud-archive", csrf2, `{}`)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("应 503，得到 %d", resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeUnavailable)
	})

	t.Run("rclone 不可用 503", func(t *testing.T) {
		e2 := newCloudTestEnv(t, enabledCloudCfg(), &fakeCloudSink{})
		setRcloneBin(t, false)
		j2 := e2.login(t)
		csrf2 := e2.sessionCSRF(t, j2)
		seedUser(t, e2, 803, store.UserEnabled)
		r := seedFinishedRequest(t, e2, 803, "chan", 4, store.RequestFailed, store.DeliveryModeUpload)
		resp := e2.apiPost(j2, "/api/v1/requests/"+strconv.FormatInt(r.ID, 10)+"/cloud-archive", csrf2, `{}`)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("应 503，得到 %d", resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeUnavailable)
	})

	t.Run("目的地无效 400", func(t *testing.T) {
		r := seedFinishedRequest(t, e, 801, "chan", 5, store.RequestSucceeded, store.DeliveryModeUpload)
		resp := post("/api/v1/requests/"+strconv.FormatInt(r.ID, 10)+"/cloud-archive",
			`{"destination":"ghost"}`)
		requireBadRequest(t, resp, "cloud-archive")
	})

	t.Run("已在途补存 409", func(t *testing.T) {
		r := seedFinishedRequest(t, e, 801, "chan", 6, store.RequestSucceeded, store.DeliveryModeUpload)
		if _, err := e.st.CreateRequest(context.Background(), store.Request{
			UserID: 801, SourceKind: store.SourcePublic, ChannelKey: "chan", MessageID: 6,
			DeliveryMode: store.DeliveryModeCloud, ParentRequestID: r.ID, CloudDestination: "mega-1",
		}); err != nil {
			t.Fatal(err)
		}
		resp := post("/api/v1/requests/"+strconv.FormatInt(r.ID, 10)+"/cloud-archive", `{}`)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("应 409，得到 %d", resp.StatusCode)
		}
		bodyOf(t, resp)
	})

	t.Run("成功建行并入队", func(t *testing.T) {
		r := seedFinishedRequest(t, e, 801, "chan", 7, store.RequestSucceeded, store.DeliveryModeReference)
		resp := post("/api/v1/requests/"+strconv.FormatInt(r.ID, 10)+"/cloud-archive",
			`{"destination":"mega-1"}`)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
		}
		var out struct {
			OK               bool  `json:"ok"`
			CreatedRequestID int64 `json:"created_request_id"`
		}
		decodeAPIJSON(t, bodyOf(t, resp), &out)
		if !out.OK || out.CreatedRequestID == 0 {
			t.Fatalf("应返回新建行 ID: %+v", out)
		}
		row, err := e.st.GetRequest(context.Background(), out.CreatedRequestID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != store.RequestQueued || row.DeliveryMode != store.DeliveryModeCloud ||
			row.ParentRequestID != r.ID || row.CloudDestination != "mega-1" || row.UserID != 801 {
			t.Errorf("新行字段不符: %+v", row)
		}
		if !e.containsAction("request.cloud_archive") {
			t.Error("补存应写审计")
		}
	})
}

// 队列饱和：新行已建并标记 failed(QUEUE_FULL)，端点返回 503 QUEUE_FULL。
func TestAPIRequestCloudArchiveQueueFull(t *testing.T) {
	setRcloneBin(t, true)
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		mgr := cloudarchive.NewManager(filepath.Join(t.TempDir(), cloudarchive.FileName), testLogger())
		if err := mgr.Save(enabledCloudCfg()); err != nil {
			t.Fatal(err)
		}
		opt.CloudCfg = mgr
		opt.CloudSink = &fakeCloudSink{}
		opt.Queue = queue.New(0) // 容量 0：Enqueue 必失败
	})
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	seedUser(t, e, 804, store.UserEnabled)
	r := seedFinishedRequest(t, e, 804, "chan", 8, store.RequestSucceeded, store.DeliveryModeUpload)

	resp := e.apiPost(j, "/api/v1/requests/"+strconv.FormatInt(r.ID, 10)+"/cloud-archive", csrf, `{}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("应 503，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), string(apperr.CodeQueueFull))

	// 新行保留且标记 QUEUE_FULL（可经现有重试入口重试）
	rows, err := e.st.ListRequests(context.Background(), store.RequestFilter{UserID: 804})
	if err != nil {
		t.Fatal(err)
	}
	var backfill store.Request
	found := false
	for _, row := range rows {
		if row.ParentRequestID == r.ID {
			backfill, found = row, true
		}
	}
	if !found {
		t.Fatal("应已创建补存行")
	}
	if backfill.Status != store.RequestFailed || backfill.ErrorCode != "QUEUE_FULL" ||
		backfill.DeliveryMode != store.DeliveryModeCloud {
		t.Errorf("队列满收尾应 failed(QUEUE_FULL)/cloud: %+v", backfill)
	}
}

// ---- E2：批量补存 ----

func TestAPIRequestsCloudArchiveBatch(t *testing.T) {
	e := newCloudTestEnv(t, enabledCloudCfg(), &fakeCloudSink{})
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	seedUser(t, e, 805, store.UserEnabled)

	post := func(body string) *http.Response {
		t.Helper()
		return e.apiPost(j, "/api/v1/requests/cloud-archive-batch", csrf, body)
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

	// 混合批次：不存在 / 未终态 / 纯文本 / 可补存
	queued := seedRequest(t, e, 805, "chan", 11, store.RequestQueued)
	text := seedFinishedRequest(t, e, 805, "chan", 12, store.RequestSucceeded, store.DeliveryModeText)
	ok := seedFinishedRequest(t, e, 805, "chan", 13, store.RequestCancelled, store.DeliveryModeUpload)

	body := `{"request_ids":[9999,` + strconv.FormatInt(queued.ID, 10) + `,` +
		strconv.FormatInt(text.ID, 10) + `,` + strconv.FormatInt(ok.ID, 10) + `]}`
	resp = post(body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批量应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK      bool                       `json:"ok"`
		Results []apiCloudArchiveBatchItem `json:"results"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || len(out.Results) != 4 {
		t.Fatalf("应返回 4 条摘要: %+v", out)
	}
	wantSkips := map[int64]string{
		9999:      "not_found",
		queued.ID: "not_finished",
		text.ID:   "text_only",
	}
	for _, item := range out.Results {
		if want, okSkip := wantSkips[item.RequestID]; okSkip {
			if item.SkipReason != want || item.CreatedRequestID != 0 {
				t.Errorf("请求 %d 应 skip %s: %+v", item.RequestID, want, item)
			}
			continue
		}
		if item.RequestID == ok.ID {
			if item.SkipReason != "" || item.CreatedRequestID == 0 || item.QueueFull {
				t.Errorf("可补存条目应建行: %+v", item)
			}
		}
	}
}

// 批量队列满：逐条建行并标记 QUEUE_FULL，摘要携带 queue_full=true。
func TestAPIRequestsCloudArchiveBatchQueueFull(t *testing.T) {
	setRcloneBin(t, true)
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		mgr := cloudarchive.NewManager(filepath.Join(t.TempDir(), cloudarchive.FileName), testLogger())
		if err := mgr.Save(enabledCloudCfg()); err != nil {
			t.Fatal(err)
		}
		opt.CloudCfg = mgr
		opt.CloudSink = &fakeCloudSink{}
		opt.Queue = queue.New(0)
	})
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	seedUser(t, e, 806, store.UserEnabled)
	r := seedFinishedRequest(t, e, 806, "chan", 14, store.RequestSucceeded, store.DeliveryModeUpload)

	resp := e.apiPost(j, "/api/v1/requests/cloud-archive-batch", csrf,
		`{"request_ids":[`+strconv.FormatInt(r.ID, 10)+`]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批量队列满不是请求级错误，应 200，得到 %d（body=%s）",
			resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK      bool                       `json:"ok"`
		Results []apiCloudArchiveBatchItem `json:"results"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if len(out.Results) != 1 || !out.Results[0].QueueFull || out.Results[0].CreatedRequestID == 0 {
		t.Fatalf("应建行并标记队列满: %+v", out.Results)
	}
	row, err := e.st.GetRequest(context.Background(), out.Results[0].CreatedRequestID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Status != store.RequestFailed || row.ErrorCode != "QUEUE_FULL" {
		t.Errorf("行应标记 QUEUE_FULL: %+v", row)
	}
}

// ---- E3：详情附 cloud_uploads 与 parent_request_id ----

func TestAPIRequestDetailCloudUploads(t *testing.T) {
	e := newCloudTestEnv(t, enabledCloudCfg(), &fakeCloudSink{})
	j := e.login(t)
	seedUser(t, e, 807, store.UserEnabled)
	r := seedFinishedRequest(t, e, 807, "chan", 15, store.RequestSucceeded, store.DeliveryModeUpload)

	// 普通请求：parent 为 0、cloud_uploads 为空数组（非 null）
	var detail apiRequestDetail
	body := getAPIJSON(t, e, j, "/api/v1/requests/"+strconv.FormatInt(r.ID, 10), &detail)
	if detail.ParentRequestID != 0 {
		t.Errorf("普通请求 parent 应为 0，得到 %d", detail.ParentRequestID)
	}
	if detail.CloudUploads == nil || len(detail.CloudUploads) != 0 {
		t.Errorf("无记录时 cloud_uploads 应为空数组: %+v（body=%s）", detail.CloudUploads, body)
	}

	// 补存行：parent 指向原请求；上传记录字段完整下发
	cloudRow, err := e.st.CreateRequest(context.Background(), store.Request{
		UserID: 807, SourceKind: store.SourcePublic, ChannelKey: "chan", MessageID: 15,
		DeliveryMode: store.DeliveryModeCloud, ParentRequestID: r.ID, CloudDestination: "mega-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	up, err := e.st.InsertCloudUpload(context.Background(), store.CloudUpload{
		RequestID: cloudRow.ID, Destination: "mega-1",
		RemotePath: "spore/chan/2026-09-09/cat.jpg", FileName: "cat.jpg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.FinishCloudUpload(context.Background(), up.ID,
		store.CloudUploadSucceeded, "", 12345, 1700000000000); err != nil {
		t.Fatal(err)
	}

	detail = apiRequestDetail{}
	body = getAPIJSON(t, e, j, "/api/v1/requests/"+strconv.FormatInt(cloudRow.ID, 10), &detail)
	if detail.ParentRequestID != r.ID {
		t.Errorf("补存行 parent 应指向原请求 %d，得到 %d", r.ID, detail.ParentRequestID)
	}
	if len(detail.CloudUploads) != 1 {
		t.Fatalf("应返回 1 条上传记录（body=%s）", body)
	}
	got := detail.CloudUploads[0]
	if got.Destination != "mega-1" || got.RemotePath != "spore/chan/2026-09-09/cat.jpg" ||
		got.FileName != "cat.jpg" || got.Status != store.CloudUploadSucceeded ||
		got.ErrorCode != "" || got.Bytes != 12345 || got.FinishedAt != 1700000000000 {
		t.Errorf("上传记录字段不符: %+v", got)
	}

	// 列表行不追加新字段（cloud 行靠 delivery_mode 标签区分）
	var list apiListEnvelope[map[string]any]
	getAPIJSON(t, e, j, "/api/v1/requests", &list)
	for _, row := range list.Items {
		if _, ok := row["cloud_uploads"]; ok {
			t.Errorf("列表行不应包含 cloud_uploads: %+v", row)
		}
		if _, ok := row["parent_request_id"]; ok {
			t.Errorf("列表行不应包含 parent_request_id: %+v", row)
		}
	}
}
