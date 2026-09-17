package web

import (
	"archive/zip"
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
)

func newBackupTestDataDir(t *testing.T) (*testEnv, string) {
	t.Helper()
	dir := t.TempDir()
	e := newTestEnv(t, func(c *config.Config) {
		c.DataDir = dir
	})
	return e, dir
}

func (e *testEnv) doBackupMultipart(j *jar, req *http.Request, csrf string) *http.Response {
	e.t.Helper()
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

func TestBackupJSONFilesGet(t *testing.T) {
	e, dir := newBackupTestDataDir(t)
	j := e.login(t)

	// 在数据目录中写入测试 JSON 文件
	_ = os.WriteFile(filepath.Join(dir, "session.json"), []byte(`{"session":"test"}`), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "cloud-drive.json"), []byte(`{"cloud":"drive"}`), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "not-json.txt"), []byte(`hello`), 0o600)

	var view apiBackupView
	getAPIJSON(t, e, j, "/api/v1/backup", &view)

	if len(view.JSONFiles) != 2 {
		t.Fatalf("应发现 2 个 JSON 文件，实际发现 %d 个: %+v", len(view.JSONFiles), view.JSONFiles)
	}
	foundSession := false
	foundCloud := false
	for _, f := range view.JSONFiles {
		if f.Name == "session.json" {
			foundSession = true
			if !strings.Contains(f.Description, "会话") {
				t.Errorf("session.json 描述不符合预期：%q", f.Description)
			}
		}
		if f.Name == "cloud-drive.json" {
			foundCloud = true
		}
	}
	if !foundSession || !foundCloud {
		t.Errorf("未能全部匹配 JSON 文件: %+v", view.JSONFiles)
	}
}

func TestBackupExportJSON(t *testing.T) {
	e, dir := newBackupTestDataDir(t)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	targetContent := `{"test":"peers"}`
	_ = os.WriteFile(filepath.Join(dir, "peers.json"), []byte(targetContent), 0o600)

	// 1. 成功导出
	resp := e.apiPost(j, "/api/v1/backup/export/json", csrf, `{"name":"peers.json"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出 JSON 应 200，得到 %d", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="peers.json"`) {
		t.Fatalf("Content-Disposition 应包含文件名: %q", cd)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != targetContent {
		t.Fatalf("导出内容不一致: got %q want %q", string(b), targetContent)
	}
	if !e.containsAction("backup.export.json") {
		t.Error("单 JSON 导出应写审计")
	}

	// 2. 文件不存在
	resp = e.apiPost(j, "/api/v1/backup/export/json", csrf, `{"name":"non-exist.json"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的文件应 404，得到 %d", resp.StatusCode)
	}

	// 3. 路径穿越与非法文件名
	for _, badName := range []string{"../app.json", "dir/sub.json", ".hidden.json", "bad.txt"} {
		resp = e.apiPost(j, "/api/v1/backup/export/json", csrf, `{"name":"`+badName+`"}`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("非法名称 %q 应 400，得到 %d", badName, resp.StatusCode)
		}
	}
}

func TestBackupExportAllJSON(t *testing.T) {
	e, dir := newBackupTestDataDir(t)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	_ = os.WriteFile(filepath.Join(dir, "session.json"), []byte(`{"s":1}`), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "bots.json"), []byte(`{"b":2}`), 0o600)

	resp := e.apiPost(j, "/api/v1/backup/export/all-json", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出所有 JSON 应 200，得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("Content-Type 应为 application/zip，得到 %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("返回内容应为合法的 ZIP 文件: %v", err)
	}

	names := make(map[string]bool)
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["session.json"] || !names["bots.json"] || !names["manifest.json"] {
		t.Fatalf("ZIP 包缺少必要条目: %+v", names)
	}
	if !e.containsAction("backup.export.all_json") {
		t.Error("导出所有 JSON 应写审计")
	}
}

func TestBackupExportFull(t *testing.T) {
	e, dir := newBackupTestDataDir(t)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	_ = os.WriteFile(filepath.Join(dir, "cloud-drive.json"), []byte(`{"type":"full_test"}`), 0o600)

	resp := e.apiPost(j, "/api/v1/backup/export/full", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("全量导出应 200，得到 %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("全量导出应返回合法的 ZIP: %v", err)
	}

	names := make(map[string]bool)
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["spore.db"] || !names["cloud-drive.json"] || !names["manifest.json"] {
		t.Fatalf("全量 ZIP 缺少必要文件: %+v", names)
	}
	if !e.containsAction("backup.export.full") {
		t.Error("全量导出应写审计")
	}
}

func TestBackupImportJSON(t *testing.T) {
	e, dir := newBackupTestDataDir(t)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	// 1. 正常导入
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("target_name", "custom.json")
	fw, _ := mw.CreateFormFile("file", "custom.json")
	_, _ = fw.Write([]byte(`{"imported":true}`))
	_ = mw.Close()

	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/v1/backup/import/json", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp := e.doBackupMultipart(j, req, csrf)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("单 JSON 导入应 200，得到 %d: %s", resp.StatusCode, string(b))
	}
	saved, err := os.ReadFile(filepath.Join(dir, "custom.json"))
	if err != nil || string(saved) != `{"imported":true}` {
		t.Fatalf("导入文件写入异常: %v content=%q", err, string(saved))
	}
	if !e.containsAction("backup.import.json") {
		t.Error("导入 JSON 应写审计")
	}

	// 2. 非法 JSON
	body.Reset()
	mw = multipart.NewWriter(&body)
	_ = mw.WriteField("target_name", "bad.json")
	fw, _ = mw.CreateFormFile("file", "bad.json")
	_, _ = fw.Write([]byte(`{not-json`))
	_ = mw.Close()

	req, _ = http.NewRequest(http.MethodPost, e.ts.URL+"/api/v1/backup/import/json", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp = e.doBackupMultipart(j, req, csrf)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("语法错误的 JSON 应 400，得到 %d", resp.StatusCode)
	}

	// 3. 非法文件名
	body.Reset()
	mw = multipart.NewWriter(&body)
	_ = mw.WriteField("target_name", "../escape.json")
	fw, _ = mw.CreateFormFile("file", "escape.json")
	_, _ = fw.Write([]byte(`{}`))
	_ = mw.Close()

	req, _ = http.NewRequest(http.MethodPost, e.ts.URL+"/api/v1/backup/import/json", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp = e.doBackupMultipart(j, req, csrf)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("跨目录目标文件名应 400，得到 %d", resp.StatusCode)
	}
}

func TestBackupImportAllJSON(t *testing.T) {
	e, dir := newBackupTestDataDir(t)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	// 1. 成功批量导入
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	_ = writeZipEntry(zw, "batch1.json", []byte(`{"key1":"val1"}`))
	_ = writeZipEntry(zw, "batch2.json", []byte(`{"key2":"val2"}`))
	_ = writeZipEntry(zw, "manifest.json", []byte(`{"manifest":true}`))
	_ = zw.Close()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "backup.zip")
	_, _ = fw.Write(zipBuf.Bytes())
	_ = mw.Close()

	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/v1/backup/import/all-json", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp := e.doBackupMultipart(j, req, csrf)
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("批量导入所有 JSON 应 200，得到 %d: %s", resp.StatusCode, string(b))
	}
	if b1, err := os.ReadFile(filepath.Join(dir, "batch1.json")); err != nil || string(b1) != `{"key1":"val1"}` {
		t.Fatalf("batch1.json 还原失败: %v", err)
	}
	if b2, err := os.ReadFile(filepath.Join(dir, "batch2.json")); err != nil || string(b2) != `{"key2":"val2"}` {
		t.Fatalf("batch2.json 还原失败: %v", err)
	}
	if !e.containsAction("backup.import.all_json") {
		t.Error("批量导入 JSON 应写审计")
	}

	// 2. Zip Slip 恶意包
	zipBuf.Reset()
	zw = zip.NewWriter(&zipBuf)
	_ = writeZipEntry(zw, "../evil.json", []byte(`{"evil":true}`))
	_ = zw.Close()

	body.Reset()
	mw = multipart.NewWriter(&body)
	fw, _ = mw.CreateFormFile("file", "evil.zip")
	_, _ = fw.Write(zipBuf.Bytes())
	_ = mw.Close()

	req, _ = http.NewRequest(http.MethodPost, e.ts.URL+"/api/v1/backup/import/all-json", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp = e.doBackupMultipart(j, req, csrf)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Zip Slip 恶意包应 400，得到 %d", resp.StatusCode)
	}
}
