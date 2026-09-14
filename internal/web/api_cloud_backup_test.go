package web

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/config"
)

func newCloudBackupTestEnv(t *testing.T, cfg cloudarchive.Config) *testEnv {
	t.Helper()
	return newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		mgr := cloudarchive.NewManager(filepath.Join(t.TempDir(), cloudarchive.FileName), testLogger())
		if err := mgr.Save(cfg); err != nil {
			t.Fatalf("预置配置失败: %v", err)
		}
		opt.CloudCfg = mgr
		opt.CloudBackupKey = bytes.Repeat([]byte("k"), 32)
	})
}

func (e *testEnv) cloudBackupUpload(j *jar, csrf, password string, data []byte) *http.Response {
	e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("backup", "cloud-drive.zip")
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		e.t.Fatal(err)
	}
	if err := mw.WriteField("password", password); err != nil {
		e.t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		e.t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+"/api/v1/cloud-drive/backup/import", &body)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if csrf != "" {
		req.Header.Set(apiCSRFHeader, csrf)
	}
	req.Header.Set("Cookie", j.header())
	resp, err := e.ts.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return resp
}

func TestAPICloudBackupAuthAndCSRF(t *testing.T) {
	e := newCloudBackupTestEnv(t, enabledCloudCfg())
	anon := newJar(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/cloud-drive/backup/status"},
		{http.MethodPost, "/api/v1/cloud-drive/backup/export"},
		{http.MethodPost, "/api/v1/cloud-drive/backup/import"},
		{http.MethodPost, "/api/v1/cloud-drive/backup/import/confirm"},
		{http.MethodDelete, "/api/v1/cloud-drive/backup/pending"},
		{http.MethodPost, "/api/v1/cloud-drive/backup/rollback"},
	} {
		resp := e.do(anon, tc.method, tc.path, "application/json", "{}")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("未认证 %s %s 应 401，得到 %d", tc.method, tc.path, resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeUnauthorized)
	}

	j := e.login(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/cloud-drive/backup/export"},
		{http.MethodPost, "/api/v1/cloud-drive/backup/import/confirm"},
		{http.MethodDelete, "/api/v1/cloud-drive/backup/pending"},
		{http.MethodPost, "/api/v1/cloud-drive/backup/rollback"},
	} {
		resp := e.do(j, tc.method, tc.path, "application/json", "{}")
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("缺 CSRF %s %s 应 403，得到 %d", tc.method, tc.path, resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeCSRFFailed)
	}
	resp := e.cloudBackupUpload(j, "", "password", []byte("zip"))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("上传缺 CSRF 应 403，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

func TestAPICloudBackupRejectsShortPassword(t *testing.T) {
	e := newCloudBackupTestEnv(t, enabledCloudCfg())
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/cloud-drive/backup/export", csrf,
		`{"password":"short","password_confirmation":"short"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("短密码导出应 400，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	body := bodyOf(t, resp)
	if !strings.Contains(body, "至少需要 8 个字符") {
		t.Fatalf("短密码应返回受控长度提示: %s", body)
	}
}

func TestAPICloudBackupTwoPhaseRestoreRollbackAndRedaction(t *testing.T) {
	old := enabledCloudCfg()
	e := newCloudBackupTestEnv(t, old)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	password := "backup-secret-password"

	// 导出是 no-store ZIP attachment，响应与审计都不泄露 password/options。
	resp := e.apiPost(j, "/api/v1/cloud-drive/backup/export", csrf,
		`{"password":"`+password+`","password_confirmation":"`+password+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("导出响应头不正确: %v", resp.Header)
	}
	exported, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	candidate := enabledCloudCfg()
	candidate.DefaultDestination = "drive-2"
	candidate.Destinations[0].Name = "drive-2"
	candidate.Destinations[0].Options["user"] = "candidate@example.com"
	candidate.Destinations[0].Options["pass"] = "candidate-secret"
	candidateZip, _, err := cloudarchive.ExportBackup(candidate, password, "other", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	// 错误密码受控拒绝，当前配置不变。
	resp = e.cloudBackupUpload(j, csrf, "wrong-password", candidateZip)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("错误密码应 400，得到 %d", resp.StatusCode)
	}
	badBody := bodyOf(t, resp)
	if strings.Contains(badBody, password) || strings.Contains(badBody, "candidate-secret") {
		t.Fatalf("错误响应泄露敏感值: %s", badBody)
	}
	if e.srv.cloudCfg.Snapshot().DefaultDestination != old.DefaultDestination {
		t.Fatal("错误密码不得应用配置")
	}

	// 上传正确包只进入候选状态，文件与内存快照均未改变。
	resp = e.cloudBackupUpload(j, csrf, password, candidateZip)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("上传应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	bodyOf(t, resp)
	if e.srv.cloudCfg.Snapshot().DefaultDestination != old.DefaultDestination {
		t.Fatal("上传验证阶段不得应用配置")
	}
	pendingBytes, err := os.ReadFile(e.srv.cloudPending.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{password, "candidate@example.com", "candidate-secret", `"options"`} {
		if bytes.Contains(pendingBytes, []byte(secret)) {
			t.Fatalf("候选文件不得包含敏感值 %q", secret)
		}
	}

	var status apiCloudBackupStatus
	getAPIJSON(t, e, j, "/api/v1/cloud-drive/backup/status", &status)
	if !status.Pending || status.RollbackAvailable || len(status.DestinationNames) != 1 || status.DestinationNames[0] != "drive-2" {
		t.Fatalf("上传后的状态不正确: %+v", status)
	}

	// 确认后整体替换并在线更新 Manager 快照，同时建立 rollback。
	resp = e.apiPost(j, "/api/v1/cloud-drive/backup/import/confirm", csrf, `{"confirm":"import_cloud_drive"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("确认应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	bodyOf(t, resp)
	got := e.srv.cloudCfg.Snapshot()
	if got.DefaultDestination != "drive-2" || got.Destinations[0].Options["pass"] != "candidate-secret" {
		t.Fatalf("确认后配置未整体替换: %+v", got)
	}
	if !e.srv.cloudCfg.HasRollback() {
		t.Fatal("确认恢复后应有 rollback")
	}

	// 回滚恢复上一配置并在线发布。
	resp = e.apiPost(j, "/api/v1/cloud-drive/backup/rollback", csrf, `{"confirm":"rollback_cloud_drive"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("回滚应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	bodyOf(t, resp)
	if e.srv.cloudCfg.Snapshot().DefaultDestination != old.DefaultDestination {
		t.Fatalf("回滚未恢复旧配置: %+v", e.srv.cloudCfg.Snapshot())
	}
	if e.srv.cloudCfg.HasRollback() {
		t.Fatal("回滚成功后应消费回滚点")
	}

	// 所有审计只含允许摘要，不含密码/options 值。
	audit := auditAfterJSON(t, e)
	for _, secret := range []string{password, "candidate@example.com", "candidate-secret", "obscured-secret", `"options"`} {
		if strings.Contains(audit, secret) {
			t.Fatalf("审计不得包含敏感值 %q: %s", secret, audit)
		}
	}
	for _, action := range []string{"cloud_drive.backup.export", "cloud_drive.backup.upload", "cloud_drive.backup.confirm", "cloud_drive.backup.applied", "cloud_drive.backup.rollback"} {
		if !e.containsAction(action) {
			t.Errorf("缺少审计动作 %s", action)
		}
	}
	_ = exported
}

func TestAPICloudBackupCancelAndMissingKey(t *testing.T) {
	e := newCloudBackupTestEnv(t, enabledCloudCfg())
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	zipData, _, err := cloudarchive.ExportBackup(enabledCloudCfg(), "password", "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp := e.cloudBackupUpload(j, csrf, "password", zipData)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("上传失败: %d %s", resp.StatusCode, bodyOf(t, resp))
	}
	bodyOf(t, resp)

	req, err := http.NewRequest(http.MethodDelete, e.ts.URL+"/api/v1/cloud-drive/backup/pending", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Cookie", j.header())
	req.Header.Set(apiCSRFHeader, csrf)
	resp, err = e.ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("取消应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	bodyOf(t, resp)
	if !e.containsAction("cloud_drive.backup.cancel") {
		t.Fatal("取消应写审计")
	}

	missing := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		opt.CloudCfg = cloudarchive.NewManager(filepath.Join(t.TempDir(), cloudarchive.FileName), testLogger())
	})
	jm := missing.login(t)
	resp = missing.do(jm, http.MethodGet, "/api/v1/cloud-drive/backup/status", "", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("缺服务端 key 应 503，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}
