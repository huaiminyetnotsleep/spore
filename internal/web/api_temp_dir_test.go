package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
)

func TestAPITempDirListAndClear(t *testing.T) {
	tempRoot := filepath.Join(t.TempDir(), "custom-temp")
	if err := os.MkdirAll(filepath.Join(tempRoot, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempRoot, "nested", "clip.mkv"), []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempRoot, "note.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := newTestEnvOpts(t, func(cfg *config.Config, _ *Options) { cfg.TempDir = tempRoot })
	j := e.login(t)

	resp := e.do(j, http.MethodGet, "/api/v1/temp-dir", "", "")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("GET temp-dir status/cache-control = %d/%q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	var view apiTempDirView
	decodeAPIJSON(t, bodyOf(t, resp), &view)
	if view.FileCount != 2 || view.Total != 10 || len(view.Files) != 2 {
		t.Fatalf("unexpected temp-dir summary: %+v", view)
	}
	if view.Files[0].Path != "nested/clip.mkv" || view.Files[0].SizeBytes != 5 || view.Files[1].Path != "note.txt" {
		t.Fatalf("unexpected temp-dir files: %+v", view.Files)
	}
	if strings.Contains(string(mustJSON(t, view)), tempRoot) {
		t.Fatal("API response leaked absolute TEMP_DIR")
	}

	csrf := apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/temp-dir/clear", csrf, "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear temp-dir status=%d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	var cleared struct {
		OK      bool           `json:"ok"`
		TempDir apiTempDirView `json:"temp_dir"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &cleared)
	if !cleared.OK || cleared.TempDir.FileCount != 0 || cleared.TempDir.Total != 0 {
		t.Fatalf("unexpected clear response: %+v", cleared)
	}
	if info, err := os.Stat(tempRoot); err != nil || !info.IsDir() {
		t.Fatalf("clear must preserve root dir: info=%v err=%v", info, err)
	}
}

func TestAPITempDirDeleteFilesAndValidatePaths(t *testing.T) {
	tempRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(tempRoot, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"nested/a.bin": "aaa", "nested/b.bin": "bbbb", "keep.bin": "k"} {
		if err := os.WriteFile(filepath.Join(tempRoot, filepath.FromSlash(name)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e := newTestEnvOpts(t, func(cfg *config.Config, _ *Options) { cfg.TempDir = tempRoot })
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	resp := e.apiPost(j, "/api/v1/temp-dir/delete", csrf, `{"paths":["nested/a.bin","nested/b.bin"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	var result struct {
		OK      bool           `json:"ok"`
		TempDir apiTempDirView `json:"temp_dir"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &result)
	if !result.OK || result.TempDir.FileCount != 1 || result.TempDir.Files[0].Path != "keep.bin" {
		t.Fatalf("unexpected delete result: %+v", result)
	}

	resp = e.apiPost(j, "/api/v1/temp-dir/delete", csrf, `{"paths":["../outside"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("traversal path should be rejected: status=%d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
}

func TestAPITempDirRejectsUnauthenticatedAndMissingCSRF(t *testing.T) {
	tempRoot := t.TempDir()
	e := newTestEnvOpts(t, func(cfg *config.Config, _ *Options) { cfg.TempDir = tempRoot })
	j := newJar(t)
	resp := e.do(j, http.MethodGet, "/api/v1/temp-dir", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status=%d", resp.StatusCode)
	}
	bodyOf(t, resp)

	j = e.login(t)
	resp = e.apiPost(j, "/api/v1/temp-dir/clear", "", "{}")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

func TestScanTempDirDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "linked-file")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	view, err := scanTempDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if view.FileCount != 0 || view.Total != 0 {
		t.Fatalf("symlink targets must not be enumerated: %+v", view)
	}
	if err := clearTempDir(root); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(outside, "secret")); err != nil || string(content) != "secret" {
		t.Fatalf("clear followed symlink target: content=%q err=%v", content, err)
	}
}

func TestScanTempDirMissingRootIsEmptyAndRootSymlinkIsRejected(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "not-created")
	view, err := scanTempDir(missing)
	if err != nil || view.FileCount != 0 || view.Files == nil {
		t.Fatalf("missing root should produce an empty list: view=%+v err=%v", view, err)
	}

	target := t.TempDir()
	secret := filepath.Join(target, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	rootLink := filepath.Join(t.TempDir(), "temp-link")
	if err := os.Symlink(target, rootLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := scanTempDir(rootLink); err == nil {
		t.Fatal("listing a symlink root must fail")
	}
	if err := clearTempDir(rootLink); err == nil {
		t.Fatal("clearing a symlink root must fail")
	}
	if content, err := os.ReadFile(secret); err != nil || string(content) != "secret" {
		t.Fatalf("root symlink target was changed: content=%q err=%v", content, err)
	}
}

func TestTempDirMaintenanceRejectsBusyQueue(t *testing.T) {
	root := t.TempDir()
	q := queue.New(1)
	e := newTestEnvOpts(t, func(cfg *config.Config, opt *Options) {
		cfg.TempDir = root
		opt.Queue = q
	})
	if err := q.Enqueue(queue.Job{ID: "test"}); err != nil {
		t.Fatal(err)
	}
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/temp-dir/clear", csrf, "{}")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("busy queue clear status=%d body=%s", resp.StatusCode, bodyOf(t, resp))
	}
	body := bodyOf(t, resp)
	if !strings.Contains(body, "队列中有待处理或正在执行的任务") {
		t.Fatalf("busy queue message missing: %s", body)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
