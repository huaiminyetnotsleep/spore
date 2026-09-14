package web

// 备份导出测试（经 /api/v1/backup/export）：合法 SQLite 快照、
// 临时文件清理、审计与最近备份时间。

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// sqliteHeader 是 SQLite 数据库文件的魔数（前 16 字节）。
var sqliteHeader = []byte("SQLite format 3\x00")

func TestBackupExport(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	seedUser(t, e, 1, store.UserEnabled)
	seedRequest(t, e, 1, "example_channel", 1, store.RequestSucceeded)

	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/backup/export", csrf, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("备份导出应 200，得到 %d", resp.StatusCode)
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="spore-backup-`) {
		t.Fatalf("文件名应含日期，得到 %q", cd)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取备份失败: %v", err)
	}
	if !bytes.HasPrefix(body, sqliteHeader) {
		t.Fatalf("备份应为合法 SQLite 文件，前缀为 %q", body[:16])
	}
	if int64(len(body)) <= 0 {
		t.Fatal("备份不应为空")
	}

	// 审计与最近备份时间
	if !e.containsAction("backup.export") {
		t.Error("备份导出应写审计")
	}
	v, ok, err := e.st.GetSetting(context.Background(), settingKeyLastBackupAt)
	if err != nil || !ok {
		t.Fatalf("应记录最近备份时间: ok=%v err=%v", ok, err)
	}
	var ms int64
	if err := json.Unmarshal([]byte(v), &ms); err != nil || ms == 0 {
		t.Fatalf("最近备份时间格式不符：%q err=%v", v, err)
	}

	// 临时文件清理：数据库目录不应残留备份临时文件
	entries, err := os.ReadDir(filepath.Dir(e.srv.dbPath))
	if err != nil {
		t.Fatalf("读取数据目录失败: %v", err)
	}
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), "spore-backup-") {
			t.Errorf("临时备份文件未清理：%s", en.Name())
		}
	}
}

func TestBackupSnapshotRestorable(t *testing.T) {
	// 备份是某一时刻的独立快照：源库后续变更不影响已导出内容
	e := newTestEnv(t, nil)
	j := e.login(t)
	seedUser(t, e, 1, store.UserEnabled)

	csrf := apiCSRFToken(t, e, j)
	resp := e.apiPost(j, "/api/v1/backup/export", csrf, "")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取备份失败: %v", err)
	}

	// 源库追加数据后，用快照另开一个库校验内容仍是导出时的状态
	seedUser(t, e, 2, store.UserPending)
	snapPath := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(snapPath, body, 0o600); err != nil {
		t.Fatalf("写快照失败: %v", err)
	}
	snap, err := store.Open(context.Background(), snapPath, testLogger())
	if err != nil {
		t.Fatalf("快照应可作为数据库打开: %v", err)
	}
	defer snap.Close()
	users, err := snap.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("读取快照用户失败: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("快照应只含导出时的 1 个用户，得到 %d", len(users))
	}
}

func TestBackupPage(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	// 旧备份页面入口已删除，备份操作只通过 /api/v1 提供。
	resp := e.do(j, "GET", "/backup", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("旧 /backup 路径应返回 404，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}
