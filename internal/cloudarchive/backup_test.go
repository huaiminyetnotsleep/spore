package cloudarchive

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBackupRejectsShortPassword(t *testing.T) {
	if _, _, err := ExportBackup(validConfig(), "short", "test", time.Now()); err == nil || !strings.Contains(err.Error(), "至少需要 8 个字符") {
		t.Fatalf("短密码导出应拒绝，得到 %v", err)
	}
	if _, _, err := ImportBackup([]byte("zip"), "short"); err == nil || !strings.Contains(err.Error(), "至少需要 8 个字符") {
		t.Fatalf("短密码导入应拒绝，得到 %v", err)
	}
}

func TestBackupRoundTripAndManifestRedaction(t *testing.T) {
	cfg := validConfig()
	password := "correct horse battery staple"
	data, meta, err := ExportBackup(cfg, password, "test-version", time.Date(2026, 9, 9, 1, 2, 3, 0, time.UTC))
	if err != nil {
		t.Fatalf("导出失败: %v", err)
	}
	if len(data) > MaxBackupSize || meta.FormatVersion != 1 || meta.PackageSize != int64(len(data)) {
		t.Fatalf("导出摘要不正确: %+v size=%d", meta, len(data))
	}
	entries, err := readBackupEntries(data)
	if err != nil {
		t.Fatal(err)
	}
	manifest := string(entries[backupManifestName])
	for _, secret := range []string{"u@example.com", "obscured-value", `"options"`, password} {
		if strings.Contains(manifest, secret) {
			t.Fatalf("manifest 不得包含敏感值 %q: %s", secret, manifest)
		}
	}
	got, imported, err := ImportBackup(data, password)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	if got.Destinations[0].Options["pass"] != cfg.Destinations[0].Options["pass"] || imported.PayloadSHA256 != meta.PayloadSHA256 {
		t.Fatalf("配置或摘要往返不一致: %+v %+v", got, imported)
	}
}

func TestBackupRejectsWrongPasswordAndTampering(t *testing.T) {
	data, _, err := ExportBackup(validConfig(), "right-password", "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ImportBackup(data, "wrong-password"); !errors.Is(err, ErrInvalidBackupPassword) {
		t.Fatalf("错误密码应被识别，得到 %v", err)
	}

	entries, err := readBackupEntries(data)
	if err != nil {
		t.Fatal(err)
	}
	entries[backupPayloadName][0] ^= 0xff
	tampered := makeTestZip(t, map[string]testZipEntry{
		backupManifestName: {data: entries[backupManifestName]},
		backupPayloadName:  {data: entries[backupPayloadName]},
	})
	if _, _, err := ImportBackup(tampered, "right-password"); err == nil || !strings.Contains(err.Error(), "摘要") {
		t.Fatalf("篡改 payload 应在摘要校验拒绝，得到 %v", err)
	}
}

func TestBackupRejectsUnknownVersionAndMaliciousZIP(t *testing.T) {
	data, _, err := ExportBackup(validConfig(), "password", "test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := readBackupEntries(data)
	if err != nil {
		t.Fatal(err)
	}
	var manifest BackupManifest
	if err := json.Unmarshal(entries[backupManifestName], &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.FormatVersion = 99
	entries[backupManifestName], _ = json.Marshal(manifest)
	unknown := makeTestZip(t, map[string]testZipEntry{
		backupManifestName: {data: entries[backupManifestName]},
		backupPayloadName:  {data: entries[backupPayloadName]},
	})
	if _, _, err := ImportBackup(unknown, "password"); err == nil || !strings.Contains(err.Error(), "版本") {
		t.Fatalf("未知版本应拒绝，得到 %v", err)
	}

	cases := map[string]map[string]testZipEntry{
		"路径穿越": {
			"../manifest.json": {data: []byte("{}")}, backupPayloadName: {data: []byte("x")},
		},
		"未知项": {
			backupManifestName: {data: []byte("{}")}, "other.enc": {data: []byte("x")},
		},
		"目录": {
			backupManifestName: {data: []byte("{}")}, backupPayloadName: {data: []byte("x"), mode: os.ModeDir | 0o700},
		},
		"符号链接": {
			backupManifestName: {data: []byte("{}")}, backupPayloadName: {data: []byte("x"), mode: os.ModeSymlink | 0o777},
		},
		"超过两个项": {
			backupManifestName: {data: []byte("{}")}, backupPayloadName: {data: []byte("x")}, "extra": {data: []byte("x")},
		},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ImportBackup(makeTestZip(t, files), "password"); err == nil {
				t.Fatal("恶意 ZIP 应被拒绝")
			}
		})
	}

	oversized := makeTestZip(t, map[string]testZipEntry{
		backupManifestName: {data: bytes.Repeat([]byte("0"), MaxBackupEntrySize+1)},
		backupPayloadName:  {data: []byte("x")},
	})
	if _, _, err := ImportBackup(oversized, "password"); err == nil || !strings.Contains(err.Error(), "2 MiB") {
		t.Fatalf("单 entry 超限应拒绝，得到 %v", err)
	}
	if _, _, err := ImportBackup(make([]byte, MaxBackupSize+1), "password"); !errors.Is(err, ErrBackupTooLarge) {
		t.Fatalf("总包超限应拒绝，得到 %v", err)
	}
}

func TestPendingStoreEncryptsCandidateAndAppliesOnce(t *testing.T) {
	dir := t.TempDir()
	store, err := NewPendingStore(dir, bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatal(err)
	}
	cfg := validConfig()
	meta := BackupMetadata{FormatVersion: 1, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), AppVersion: "test", PayloadSHA256: strings.Repeat("a", 64), PackageSize: 123, DestinationNames: []string{"mega-1"}}
	if err := store.Save(cfg, meta, time.Now()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.Path())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("候选文件权限应为 0600: info=%v err=%v", info, err)
	}
	onDisk, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"u@example.com", "obscured-value", `"options"`} {
		if bytes.Contains(onDisk, []byte(secret)) {
			t.Fatalf("候选文件不得含明文 %q: %s", secret, onDisk)
		}
	}
	called := false
	if err := store.Apply(func(got Config, status PendingStatus) error {
		called = true
		if got.Destinations[0].Options["pass"] != "obscured-value" || !status.Pending {
			t.Fatalf("候选解密结果不正确: %+v %+v", got, status)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("应执行 Apply 回调")
	}
	if _, err := os.Stat(store.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("应用成功后应删除候选: %v", err)
	}
	if err := store.Apply(func(Config, PendingStatus) error { return nil }); !errors.Is(err, ErrPendingNotFound) {
		t.Fatalf("候选只能应用一次，得到 %v", err)
	}
}

func TestManagerRestoreRollbackAndConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	m := NewManager(path, nil)
	old := validConfig()
	if err := m.Save(old); err != nil {
		t.Fatal(err)
	}
	candidate := validConfig()
	candidate.DefaultDestination = "drive-2"
	candidate.Destinations[0].Name = "drive-2"
	if err := m.Restore(candidate); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().DefaultDestination != "drive-2" || !m.HasRollback() {
		t.Fatalf("恢复后快照/回滚点不正确: %+v", m.Snapshot())
	}
	if err := m.Rollback(); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().DefaultDestination != old.DefaultDestination {
		t.Fatalf("回滚未恢复旧配置: %+v", m.Snapshot())
	}
	if m.HasRollback() {
		t.Fatal("回滚成功后应消费最近一次回滚点")
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cfg := validConfig()
			if i%2 == 0 {
				_ = m.Save(cfg)
			} else {
				_ = m.Restore(cfg)
			}
		}(i)
	}
	wg.Wait()
	if err := m.Snapshot().Validate(); err != nil {
		t.Fatalf("并发写后快照必须有效: %v", err)
	}
	if _, err := LoadFile(path); err != nil {
		t.Fatalf("并发写后文件必须有效: %v", err)
	}
}

type testZipEntry struct {
	data []byte
	mode os.FileMode
}

func makeTestZip(t *testing.T, files map[string]testZipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, entry := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if entry.mode != 0 {
			h.SetMode(entry.mode)
		} else {
			h.SetMode(0o600)
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(w, bytes.NewReader(entry.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
