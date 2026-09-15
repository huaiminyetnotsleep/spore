package web

// 高风险页面 API 契约测试：
// OAuth 配置/绑定、备份导出/上传/确认、受控重启与 MTProto 状态/重连。
// 覆盖未认证 401 JSON、CSRF 403 JSON、参数 400、成功路径审计与业务失败；
// 敏感断言：Secret 与扫码 URL 不出现在任何响应或审计，MTProto 状态字段
// 与 SSR /mtproto/status 一致（差异仅 qr_url → qr_available）。

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// auditAfterJSON 返回全部审计 after_json 拼接文本（敏感值不落审计断言用）。
func auditAfterJSON(t *testing.T, e *testEnv) string {
	t.Helper()
	entries, err := e.st.ListAudit(context.Background(), 100, 0)
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	var buf bytes.Buffer
	for _, en := range entries {
		buf.WriteString(en.AfterJSON)
		buf.WriteString("\n")
	}
	return buf.String()
}

// apiUploadBackup 以 multipart 发送备份上传（不经 apiReadJSON 的独立契约）。
func (e *testEnv) apiUploadBackup(j *jar, csrf, filename string, content []byte, withCSRF bool) *http.Response {
	e.t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("backup", filename)
	if err != nil {
		e.t.Fatalf("构造 multipart 失败: %v", err)
	}
	if _, err := fw.Write(content); err != nil {
		e.t.Fatalf("写入 multipart 失败: %v", err)
	}
	if err := w.Close(); err != nil {
		e.t.Fatalf("收尾 multipart 失败: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/api/v1/backup/import", &buf)
	if err != nil {
		e.t.Fatalf("构造上传请求失败: %v", err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if withCSRF {
		req.Header.Set(apiCSRFHeader, csrf)
	}
	if h := j.header(); h != "" {
		req.Header.Set("Cookie", h)
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		e.t.Fatalf("上传备份失败: %v", err)
	}
	j.store(resp)
	return resp
}

// ---- 认证与 CSRF：全部新端点 401/403/405 为 JSON ----

func TestAPIPrivilegedEndpointsAuthAndCSRF(t *testing.T) {
	e := newTestEnv(t, nil)

	// 未认证：JSON 401，绝不重定向到登录 HTML
	j := newJar(t)
	for _, path := range []string{
		"/api/v1/oauth/settings", "/api/v1/backup", "/api/v1/mtproto/status",
	} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("未认证 GET %s 应 401，得到 %d", path, resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), apiCodeUnauthorized)
	}
	for _, path := range []string{
		"/api/v1/oauth/settings", "/api/v1/oauth/bind", "/api/v1/oauth/unbind",
		"/api/v1/backup/export", "/api/v1/backup/import/confirm",
		"/api/v1/restart", "/api/v1/mtproto/relogin",
	} {
		resp := e.do(j, http.MethodPost, path, "application/json", "{}")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("未认证 POST %s 应 401，得到 %d", path, resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), apiCodeUnauthorized)
	}

	// 已认证但缺/错 CSRF：JSON 403
	j = e.login(t)
	for _, path := range []string{
		"/api/v1/oauth/settings", "/api/v1/oauth/bind", "/api/v1/oauth/unbind",
		"/api/v1/backup/export", "/api/v1/backup/import/confirm",
		"/api/v1/restart", "/api/v1/mtproto/relogin",
	} {
		for name, csrf := range map[string]string{"缺失": "", "错误": "wrong-token"} {
			resp := e.apiPost(j, path, csrf, "{}")
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s CSRF %s 应 403，得到 %d", path, name, resp.StatusCode)
			}
			requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
				bodyOf(t, resp), apiCodeCSRFFailed)
		}
	}

	// GET 打写端点：405 JSON（Allow: POST）
	for _, path := range []string{
		"/api/v1/oauth/bind", "/api/v1/backup/export",
		"/api/v1/restart", "/api/v1/mtproto/relogin",
	} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET %s 应 405，得到 %d", path, resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), apiCodeMethodNotAllowed)
	}
}

// ---- OAuth 配置 ----

func TestAPIOAuthSettingsGetNeverLeaksSecret(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) {
		c.GitHubClientID = "test-client-id"
		c.GitHubClientSecret = "env-secret-value"
	})
	j := e.login(t)

	var view apiOAuthSettingsView
	body := getAPIJSON(t, e, j, "/api/v1/oauth/settings", &view)
	if strings.Contains(body, "env-secret-value") {
		t.Fatalf("响应不得出现环境变量 Secret：%s", body)
	}
	if !view.GitHubConfigured || !view.Configured || !view.Enabled {
		t.Errorf("环境变量配置下通道应可用: %+v", view)
	}
	if view.ClientID != "test-client-id" || !view.SecretSet {
		t.Errorf("Client ID 与 Secret 状态不符: %+v", view)
	}
	if view.Bound {
		t.Errorf("未绑定时 Bound 应为 false: %+v", view)
	}
	if cc := func() string {
		resp := e.do(j, http.MethodGet, "/api/v1/oauth/settings", "", "")
		defer bodyOf(t, resp)
		return resp.Header.Get("Cache-Control")
	}(); cc != "no-store" {
		t.Errorf("配置读取应 no-store，得到 %q", cc)
	}
}

func TestAPIOAuthSettingsSaveAndClear(t *testing.T) {
	e := newTestEnvOpts(t, func(c *config.Config, _ *Options) {
		c.OAuthEncryptionKey = []byte(strings.Repeat("k", 32))
	})
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	// 保存并启用：配置立即生效；Secret 不出现在响应与审计
	resp := e.apiPost(j, "/api/v1/oauth/settings", csrf,
		`{"action":"save","client_id":"client-id","client_secret":"plain-secret","enabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	body := bodyOf(t, resp)
	if strings.Contains(body, "plain-secret") {
		t.Fatalf("保存响应不得回显 Secret：%s", body)
	}
	var out struct {
		apiWriteOK
		Settings apiOAuthSettingsView `json:"settings"`
	}
	decodeAPIJSON(t, body, &out)
	if !out.OK || !out.Settings.Configured || !out.Settings.Enabled || !out.Settings.SecretSet {
		t.Errorf("保存后的配置状态不符: %+v", out.Settings)
	}
	if _, _, enabled := e.srv.oauthCredentials(context.Background()); !enabled {
		t.Fatal("保存并启用后 OAuth 应立即可用")
	}
	if !e.containsAction("oauth.config") {
		t.Error("保存应写审计")
	}
	if strings.Contains(auditAfterJSON(t, e), "plain-secret") {
		t.Error("审计不得包含 Secret 明文")
	}

	// 参数拒绝：未知 action 与缺失 Client ID → 400 受控文案
	resp = e.apiPost(j, "/api/v1/oauth/settings", csrf, `{"action":"nuke"}`)
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)
	resp = e.apiPost(j, "/api/v1/oauth/settings", csrf, `{"action":"save"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺失 Client ID 应 400，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)

	// 清除凭据：配置回到未配置，审计只记动作
	resp = e.apiPost(j, "/api/v1/oauth/settings", csrf, `{"action":"clear"}`)
	requireAPIStatus(t, resp, "clear", http.StatusOK)
	if _, _, enabled := e.srv.oauthCredentials(context.Background()); enabled {
		t.Fatal("清除后 OAuth 不应继续可用")
	}
	if !e.containsAction("oauth.credentials.clear") {
		t.Error("清除凭据应写审计")
	}
}

// ---- OAuth 绑定/解绑 ----

func TestAPIOAuthBindUsesExistingCallback(t *testing.T) {
	const (
		adminID    = int64(777)
		adminLogin = "binding-admin"
	)
	fg := newFakeGitHub(t,
		map[string]string{"bind-code": "tok-bind"},
		map[string]githubUser{"tok-bind": {ID: adminID, Login: adminLogin}})
	e := newOAuthServer(t, fg)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	// bind：返回受控跳转 URL（指向 GitHub 授权地址并携带一次性 state）
	resp := e.apiPost(j, "/api/v1/oauth/bind", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bind 应 200，得到 %d", resp.StatusCode)
	}
	var out struct {
		apiWriteOK
		AuthorizeURL string `json:"authorize_url"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK {
		t.Fatal("bind 应返回 ok")
	}
	loc, err := url.Parse(out.AuthorizeURL)
	if err != nil || !strings.HasPrefix(loc.String(), fg.srv.URL+"/login/oauth/authorize") {
		t.Fatalf("authorize_url 应指向 GitHub 授权地址，得到 %q err=%v", out.AuthorizeURL, err)
	}
	if loc.Query().Get("client_id") != "test-client-id" {
		t.Errorf("authorize_url 应携带 client_id，得到 %q", loc.Query().Get("client_id"))
	}
	if !strings.HasSuffix(loc.Query().Get("redirect_uri"), "/auth/github/callback") {
		t.Errorf("redirect_uri 应指向既有回调路由，得到 %q", loc.Query().Get("redirect_uri"))
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("authorize_url 应携带一次性 state")
	}

	// 前端跳转后回调沿用既有路由：当前结果页改为 302 到 GitHub 设置页横幅
	q := url.Values{"code": {"bind-code"}, "state": {state}}
	resp = e.do(j, "GET", "/auth/github/callback?"+q.Encode(), "", "")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("回调应 302，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/settings/oauth?oauth=bound" {
		t.Fatalf("绑定成功应重定向到设置页横幅，得到 %q", loc)
	}
	bodyOf(t, resp)
	if binding, ok, _ := loadGitHubBinding(context.Background(), e.st); !ok || binding.ID != adminID {
		t.Fatalf("绑定应写入 settings：%v %v", ok, binding)
	}
	if !e.containsAction("oauth.bind") {
		t.Error("绑定应写审计")
	}
	if resp := e.do(j, "GET", "/admin", "", ""); resp.StatusCode != http.StatusFound {
		t.Fatalf("绑定后原会话应失效，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
}

func TestAPIOAuthUnbind(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	// 未绑定时解绑 → 400 受控文案
	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/oauth/unbind", csrf, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("未绑定解绑应 400，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)

	// 已绑定时解绑：绑定删除、全部会话失效、审计
	if err := saveGitHubBinding(context.Background(), e.st,
		GitHubBinding{ID: 42, Login: "someone", BoundAt: 1}); err != nil {
		t.Fatalf("写入绑定失败: %v", err)
	}
	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/oauth/unbind", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("解绑应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		apiWriteOK
		Relogin bool `json:"relogin"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || !out.Relogin {
		t.Errorf("解绑结果应要求重新登录: %+v", out)
	}
	if _, ok, _ := loadGitHubBinding(context.Background(), e.st); ok {
		t.Fatal("解绑后应删除绑定")
	}
	if !e.containsAction("oauth.unbind") {
		t.Error("解绑应写审计")
	}
	if resp := e.do(j, "GET", "/api/v1/session", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("解绑后全部会话应失效，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
}

func TestAPIOAuthBindUnconfigured(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/oauth/bind", csrf, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("未配置通道时 bind 应 400，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)
}

// ---- 备份 ----

func TestAPIBackupStatus(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	var view apiBackupView
	getAPIJSON(t, e, j, "/api/v1/backup", &view)
	if view.DBPath == "" {
		t.Error("备份状态应包含数据库路径")
	}
	if view.Pending || view.LastBackupAt != 0 {
		t.Errorf("初始状态不应有待导入且未备份: %+v", view)
	}
}

func TestAPIBackupExportAndImportFlow(t *testing.T) {
	dataDir := t.TempDir()
	e := newTestEnvOpts(t, func(c *config.Config, _ *Options) { c.DataDir = dataDir })
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	seedUser(t, e, 1, store.UserEnabled)

	// 导出：流式 .db 响应，头与 SSR 导出一致
	resp := e.apiPost(j, "/api/v1/backup/export", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出应 200，得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("导出 Content-Type 应为 application/octet-stream，得到 %q", ct)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="spore-backup-`) {
		t.Errorf("导出应带附件文件名，得到 %q", cd)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("备份含业务行为记录，导出响应应 no-store，得到 %q", cc)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取导出内容失败: %v", err)
	}
	resp.Body.Close()
	if !bytes.HasPrefix(body, sqliteHeader) {
		t.Fatal("导出应为合法 SQLite 快照")
	}
	if !e.containsAction("backup.export") {
		t.Error("导出应写审计")
	}

	// 状态：最近备份时间已落，待导入为空
	var view apiBackupView
	getAPIJSON(t, e, j, "/api/v1/backup", &view)
	if view.LastBackupAt == 0 || view.Pending {
		t.Errorf("导出后的状态不符: %+v", view)
	}

	// 上传合法快照 → 待确认；确认 → marker confirmed
	upload := e.apiUploadBackup(j, csrf, "spore-backup.db", body, true)
	if upload.StatusCode != http.StatusOK {
		t.Fatalf("上传应 200，得到 %d（body=%s）", upload.StatusCode, bodyOf(t, upload))
	}
	var upOut struct {
		apiWriteOK
		Message string        `json:"message"`
		Backup  apiBackupView `json:"backup"`
	}
	decodeAPIJSON(t, bodyOf(t, upload), &upOut)
	if !upOut.OK || !upOut.Backup.Pending || upOut.Backup.PendingState != "待确认" {
		t.Errorf("上传后的待导入状态不符: %+v", upOut.Backup)
	}
	if !e.containsAction("backup.import.validated") {
		t.Error("上传校验通过应写审计")
	}

	// 确认导入：写 marker（下次启动应用），当前数据库未变
	resp = e.apiPost(j, "/api/v1/backup/import/confirm", csrf, `{"confirm":"import"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("确认应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	marker, err := store.ReadPendingImport(dataDir)
	if err != nil || marker.Status != "confirmed" || marker.SHA256 == "" {
		t.Fatalf("确认后应有 confirmed marker：%+v err=%v", marker, err)
	}
	if !e.containsAction("backup.import.confirmed") {
		t.Error("确认导入应写审计")
	}
	getAPIJSON(t, e, j, "/api/v1/backup", &view)
	if view.PendingState != "confirmed" {
		t.Errorf("确认后状态应 confirmed，得到 %q", view.PendingState)
	}

	// 拒绝路径：扩展名 / 内容 / 缺失文件
	resp = e.apiUploadBackup(j, csrf, "backup.txt", body, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非 .db 上传应 400，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)
	if !e.auditContains("backup.import.rejected", "invalid_extension") {
		t.Error("扩展名拒绝应写脱敏审计")
	}

	resp = e.apiUploadBackup(j, csrf, "broken.db", []byte("not a sqlite file at all"), true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("损坏内容应 400，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)
	if !e.auditContains("backup.import.rejected", "invalid_backup") {
		t.Error("校验拒绝应写脱敏审计")
	}

	// 空文件：multipart 缺失 backup 表单域 → 400
	resp = e.apiUploadBackup(j, csrf, "", nil, true)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空文件应 400，得到 %d", resp.StatusCode)
	}

	// 确认拒绝：错误 confirm 值与无待导入文件（清理 marker 与候选库后）
	for _, path := range []string{store.PendingImportMarkerPath(dataDir), store.PendingImportPath(dataDir)} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Fatalf("清理待导入文件失败: %v", err)
		}
	}
	resp = e.apiPost(j, "/api/v1/backup/import/confirm", csrf, `{"confirm":"nope"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("错误确认值应 400，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)
	resp = e.apiPost(j, "/api/v1/backup/import/confirm", csrf, `{"confirm":"import"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("无待导入文件应 400，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)

	// 上传缺 CSRF → 403 JSON
	resp = e.apiUploadBackup(j, csrf, "spore-backup.db", body, false)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("缺 CSRF 上传应 403，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeCSRFFailed)
}

// auditContains 判断审计中是否存在指定动作且 after_json 含指定原因标记。
func (e *testEnv) auditContains(action, marker string) bool {
	e.t.Helper()
	entries, err := e.st.ListAudit(context.Background(), 100, 0)
	if err != nil {
		e.t.Fatalf("读取审计失败: %v", err)
	}
	for _, en := range entries {
		if en.Action == action && strings.Contains(en.AfterJSON, marker) {
			return true
		}
	}
	return false
}

// ---- 受控重启 ----

func TestAPIRestartWithFakeFunc(t *testing.T) {
	called := make(chan struct{}, 1)
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		opt.RestartFunc = func() error { called <- struct{}{}; return nil }
	})
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	// 确认字段缺失/错误 → 400
	resp := e.apiPost(j, "/api/v1/restart", csrf, "")
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)
	resp = e.apiPost(j, "/api/v1/restart", csrf, `{"confirm":"nope"}`)
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)
	if e.containsAction("admin.restart") {
		t.Error("未确认的请求不应触发重启审计")
	}

	// 正确确认 → 202 + 审计 + RestartFunc 被调用
	resp = e.apiPost(j, "/api/v1/restart", csrf, `{"confirm":"restart"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("重启应 202，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		apiWriteOK
		Restarting bool `json:"restarting"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || !out.Restarting {
		t.Errorf("重启结果不符: %+v", out)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("重启函数未被调用")
	}
	if !e.containsAction("admin.restart") {
		t.Error("重启应写审计")
	}
}

func TestRestartRejectsDuplicateSubmission(t *testing.T) {
	called := make(chan struct{}, 2)
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		opt.RestartFunc = func() error { called <- struct{}{}; return nil }
	})
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	resp := e.apiPost(j, "/api/v1/restart", csrf, `{"confirm":"restart"}`)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("首次重启应 202，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	bodyOf(t, resp)

	resp = e.apiPost(j, "/api/v1/restart", csrf, `{"confirm":"restart"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("重复重启应 409，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeConflict)

	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("首次重启函数未被调用")
	}
	select {
	case <-called:
		t.Fatal("重复提交不应再次调用重启函数")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAPIRestartUnavailable(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	resp := e.apiPost(j, "/api/v1/restart", csrf, `{"confirm":"restart"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未接入重启应 503，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeUnavailable)
	if !e.containsAction("admin.restart.unavailable") {
		t.Error("重启不可用应写脱敏审计")
	}
}

// ---- MTProto 状态与重连 ----

type apiMTProtoStatusBody struct {
	State        string `json:"state"`
	UpdatedAt    int64  `json:"updated_at"`
	QRAvailable  bool   `json:"qr_available"`
	LastError    string `json:"last_error"`
	BotState     string `json:"bot_state"`
	BotDCID      int    `json:"bot_dc_id"`
	BotUpdatedAt int64  `json:"bot_updated_at"`
}

type fakeBotStatus struct{ snap mtproto.BotStatusSnapshot }

func (f fakeBotStatus) Status() mtproto.BotStatusSnapshot { return f.snap }

type fakeBotIdentity struct{ id BotIdentity }

func (f fakeBotIdentity) BotIdentity() (BotIdentity, bool) { return f.id, f.id.ID != 0 }

func TestAPIMTProtoStatus(t *testing.T) {
	e, fake := newMTTestEnv(t)
	j := e.login(t)

	// 离线初始
	fake.set(mtproto.StatusSnapshot{State: mtproto.StateOffline, UpdatedAt: 12345})
	var apiBody apiMTProtoStatusBody
	getAPIJSON(t, e, j, "/api/v1/mtproto/status", &apiBody)
	if apiBody.State != "offline" || apiBody.UpdatedAt != 12345 || apiBody.QRAvailable {
		t.Fatalf("离线状态不符: %+v", apiBody)
	}
	// 旧页面入口已删除，MTProto 状态只通过 /api/v1 获取。
	resp := e.do(j, http.MethodGet, "/mtproto/status", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("旧 /mtproto/status 应返回 404，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	// 登录中带扫码 URL：API 只给 qr_available，绝不下发 qr_url 本身
	fake.set(mtproto.StatusSnapshot{
		State: mtproto.StateLoginPending, QRURL: "tg://login?token=abc",
		LastError: "上次错误", UpdatedAt: 999,
	})
	resp = e.do(j, http.MethodGet, "/api/v1/mtproto/status", "", "")
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("状态响应应 no-store，得到 %q", cc)
	}
	body := bodyOf(t, resp)
	decodeAPIJSON(t, body, &apiBody)
	if apiBody.State != "login_pending" || !apiBody.QRAvailable || apiBody.LastError != "上次错误" {
		t.Fatalf("登录中状态不符: %+v", apiBody)
	}
	if strings.Contains(body, "qr_url") || strings.Contains(body, "tg://") {
		t.Fatalf("API 状态响应不得包含扫码 URL：%s", body)
	}

	// 未接入：返回 state=unknown
	e2 := newTestEnv(t, nil)
	j2 := e2.login(t)
	var unknown struct {
		State string `json:"state"`
	}
	getAPIJSON(t, e2, j2, "/api/v1/mtproto/status", &unknown)
	if unknown.State != "unknown" {
		t.Errorf("未接入应返回 unknown，得到 %q", unknown.State)
	}
}

func TestAPIMTProtoStatusIncludesBotDC(t *testing.T) {
	fake := &fakeMTProto{snap: mtproto.StatusSnapshot{State: mtproto.StateReady, UpdatedAt: 10}}
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		opt.MTProto = fake
		opt.BotMTProto = fakeBotStatus{snap: mtproto.BotStatusSnapshot{
			State: mtproto.BotStateReady, DCID: 5, UpdatedAt: 20,
		}}
	})
	j := e.login(t)
	var body apiMTProtoStatusBody
	getAPIJSON(t, e, j, "/api/v1/mtproto/status", &body)
	if body.State != mtproto.StateReady || body.BotState != mtproto.BotStateReady || body.BotDCID != 5 || body.BotUpdatedAt != 20 {
		t.Fatalf("Bot 状态/DC 不符: %+v", body)
	}
}

func TestAPIMTProtoRelogin(t *testing.T) {
	e, fake := newMTTestEnv(t)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	// 离线触发成功：审计 + 状态机推进
	resp := e.apiPost(j, "/api/v1/mtproto/relogin", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("离线重连应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if fake.triggerCount() != 1 {
		t.Fatalf("应触发一次重连，得到 %d", fake.triggerCount())
	}
	if !e.containsAction("mtproto.relogin") {
		t.Error("重连应写审计")
	}

	// 非离线状态 → 409 冲突
	fake.set(mtproto.StatusSnapshot{State: mtproto.StateReady})
	resp = e.apiPost(j, "/api/v1/mtproto/relogin", csrf, "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("就绪状态重连应 409，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeConflict)

	// 触发失败 → 500 受控文案（不误报成功、不透出底层错误）
	fake.failTrig = true
	fake.set(mtproto.StatusSnapshot{State: mtproto.StateOffline})
	resp = e.apiPost(j, "/api/v1/mtproto/relogin", csrf, "")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("触发失败应 500，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), "INTERNAL_ERROR")

	// 未接入 → 503 受控不可用
	e2 := newTestEnv(t, nil)
	j2 := e2.login(t)
	csrf2 := apiCSRFToken(t, e2, j2)
	resp = e2.apiPost(j2, "/api/v1/mtproto/relogin", csrf2, "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未接入重连应 503，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), "TELEGRAM_UNAVAILABLE")
}
