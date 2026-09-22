package backup

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildFullZipContentsAndExclusion(t *testing.T) {
	dir := t.TempDir()
	// 数据目录：普通 JSON、凭据 JSON、点开头文件、子目录、非 JSON
	for name, content := range map[string]string{
		"session.json":     `{"s":1}`,
		"peers.json":       `{"p":1}`,
		"r2-backup.json":   `{"secret":"r2"}`,
		"cloud-drive.json": `{"secret":"cd"}`,
		".hidden.json":     `{"h":1}`,
		"notes.txt":        "text",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(dir, "snapshot.db")
	if err := os.WriteFile(snapshot, []byte("sqlite-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out.zip")

	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	stats, err := BuildFullZip(dir, snapshot, dest, []string{"r2-backup.json", "cloud-drive.json"}, now)
	if err != nil {
		t.Fatalf("打包失败: %v", err)
	}
	if stats.Files != 2 {
		t.Fatalf("应打包 2 个 JSON，got %d", stats.Files)
	}
	if stats.DBBytes != int64(len("sqlite-bytes")) || stats.TotalBytes == 0 {
		t.Fatalf("统计不符: %+v", stats)
	}

	raw, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("产物应为合法 ZIP: %v", err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	joined := strings.Join(names, ",")
	for _, want := range []string{"spore.db", "session.json", "peers.json", "manifest.json"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("包内应含 %s，实得 %v", want, names)
		}
	}
	for _, banned := range []string{"r2-backup.json", "cloud-drive.json", ".hidden.json", "notes.txt"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("包内不应含 %s，实得 %v", banned, names)
		}
	}
}

func TestBuildFullZipWithoutExclusionKeepsCredentials(t *testing.T) {
	// Web 手动全量导出口径不变：不排除任何 JSON
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cloud-drive.json"), []byte(`{"a":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(dir, "snapshot.db")
	if err := os.WriteFile(snapshot, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out.zip")
	if _, err := BuildFullZip(dir, snapshot, dest, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(dest)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "cloud-drive.json" {
			found = true
		}
	}
	if !found {
		t.Fatal("手动导出口径应保留 cloud-drive.json")
	}
}
