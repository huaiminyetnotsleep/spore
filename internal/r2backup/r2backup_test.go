package r2backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
)

func TestConfigValidate(t *testing.T) {
	valid := Config{
		Enabled:         true,
		AccountID:       "0123456789abcdef0123456789abcdef",
		AccessKeyID:     "akid-12345678",
		SecretAccessKey: "secret-12345678",
		Bucket:          "spore-backup",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("完整配置应通过校验: %v", err)
	}

	// 大写 Account ID 归一化后仍合法
	upper := valid
	upper.AccountID = strings.ToUpper(valid.AccountID)
	if err := upper.Validate(); err != nil {
		t.Fatalf("大写 Account ID 应归一化通过: %v", err)
	}

	// 关闭状态允许不完整草稿（字段可缺省）
	draft := Config{Enabled: false}
	if err := draft.Validate(); err != nil {
		t.Fatalf("关闭状态应允许不完整草稿: %v", err)
	}

	// 已填但非法的值即便关闭也报错（帮助及早发现粘贴错误）
	badDraft := Config{Enabled: false, AccountID: "short"}
	if err := badDraft.Validate(); err == nil {
		t.Fatal("已填非法 Account ID 应报错（即使未开启）")
	}

	// 开启 + 不完整 → 门禁报错
	on := draft
	on.Enabled = true
	if err := on.Validate(); err == nil || !strings.Contains(err.Error(), "完整填写") {
		t.Fatalf("开启且不完整应报门禁错误，得到: %v", err)
	}

	// 桶名非法
	bad := valid
	bad.Bucket = "Invalid_Bucket"
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "Bucket") {
		t.Fatalf("非法桶名应报错，得到: %v", err)
	}

	// Account ID 非十六进制
	badID := valid
	badID.AccountID = "zz23456789abcdef0123456789abcdef"
	if err := badID.Validate(); err == nil || !strings.Contains(err.Error(), "Account ID") {
		t.Fatalf("非法 Account ID 应报错，得到: %v", err)
	}
}

func TestConfigSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Enabled:         true,
		AccountID:       "0123456789abcdef0123456789abcdef",
		AccessKeyID:     "akid-12345678",
		SecretAccessKey: "secret-12345678",
		Bucket:          "spore-backup",
	}
	if err := Save(dir, cfg); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	fi, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatalf("配置文件应存在: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("配置文件权限应为 0600，得到 %o", fi.Mode().Perm())
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got != cfg {
		t.Fatalf("往返不一致: got %+v want %+v", got, cfg)
	}
	if got.Complete() != true {
		t.Fatal("完整配置 Complete 应为 true")
	}
}

func TestLoadMissingFileIsZeroConfig(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil || cfg != (Config{}) {
		t.Fatalf("缺失文件应返回零配置无错误，got %+v err %v", cfg, err)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("损坏配置应返回错误而非静默关闭")
	}
}

func TestUpdateStatusPreservesConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{
		Enabled:         true,
		AccountID:       "0123456789abcdef0123456789abcdef",
		AccessKeyID:     "akid-12345678",
		SecretAccessKey: "secret-12345678",
		Bucket:          "spore-backup",
	}
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := UpdateStatus(dir, 1727000000000, "R2 上传失败（X）"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != cfg.AccountID || got.SecretAccessKey != cfg.SecretAccessKey || got.Bucket != cfg.Bucket {
		t.Fatalf("状态回写不应改动连接字段: %+v", got)
	}
	if got.LastUploadAt != 1727000000000 || got.LastUploadError != "R2 上传失败（X）" {
		t.Fatalf("状态字段未落盘: %+v", got)
	}
}

// fakeStore 是 ObjectStore 桩：记录对象并注入 List/Delete 失败。
type fakeStore struct {
	objects    map[string]int64
	putErr     error
	listErr    error
	deleteErr  error
	listCalled bool
}

func newFakeStore(keys ...string) *fakeStore {
	fs := &fakeStore{objects: map[string]int64{}}
	for _, k := range keys {
		fs.objects[k] = 1
	}
	return fs
}

func (f *fakeStore) Put(_ context.Context, key, _, _ string, size int64) error {
	if f.putErr != nil {
		return f.putErr
	}
	f.objects[key] = size
	return nil
}

func (f *fakeStore) List(_ context.Context, prefix string) ([]string, error) {
	f.listCalled = true
	if f.listErr != nil {
		return nil, f.listErr
	}
	var names []string
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			names = append(names, k)
		}
	}
	return names, nil
}

func (f *fakeStore) Delete(_ context.Context, key string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.objects, key)
	return nil
}

func ts(hour int) time.Time { return time.Date(2026, 9, 22, hour, 0, 0, 0, time.UTC) }

func TestBackupKeyLexicographicOrder(t *testing.T) {
	a, b := BackupKey(ts(1)), BackupKey(ts(23))
	if !strings.HasPrefix(a, "spore-full-backup-") || !strings.HasSuffix(a, ".zip") {
		t.Fatalf("key 命名不符契约: %s", a)
	}
	if !(a < b) {
		t.Fatalf("时间戳 key 字典序应随时间递增: %s vs %s", a, b)
	}
}

func TestUploadAndRotate(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "full.zip")
	if err := os.WriteFile(zipPath, []byte("zip-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 预置 8 份（01:00–22:00 每 3 小时一档），keep=8 上传第 9 份后删最旧
	fs := newFakeStore(
		BackupKey(ts(1)), BackupKey(ts(4)), BackupKey(ts(7)), BackupKey(ts(10)),
		BackupKey(ts(13)), BackupKey(ts(16)), BackupKey(ts(19)), BackupKey(ts(22)),
	)
	res, err := UploadAndRotate(context.Background(), fs, zipPath, 8, ts(23))
	if err != nil {
		t.Fatalf("上传+轮转失败: %v", err)
	}
	if res.Removed != 1 {
		t.Fatalf("应删除 1 份最旧备份，got %d", res.Removed)
	}
	if _, ok := fs.objects[BackupKey(ts(1))]; ok {
		t.Fatal("最旧备份应被删除")
	}
	if len(fs.objects) != 8 {
		t.Fatalf("远端应保留 8 份，got %d", len(fs.objects))
	}
	if res.SizeBytes != int64(len("zip-bytes")) {
		t.Fatalf("大小应取自文件: %d", res.SizeBytes)
	}

	// keep 超出现有份数：只上传不删除
	res, err = UploadAndRotate(context.Background(), fs, zipPath, 50, ts(23))
	if err != nil || res.Removed != 0 {
		t.Fatalf("keep 超额时不应删除: res %+v err %v", res, err)
	}

	// 上传失败：无副作用
	fail := newFakeStore(BackupKey(ts(1)))
	fail.putErr = errors.New("boom")
	if _, err := UploadAndRotate(context.Background(), fail, zipPath, 8, ts(2)); err == nil {
		t.Fatal("上传失败应返回错误")
	}
	if len(fail.objects) != 1 {
		t.Fatalf("上传失败不应轮转，got %d 份", len(fail.objects))
	}
	if fail.listCalled {
		t.Fatal("上传失败不应触发 List")
	}
}

func TestUploadSuccessRotationFailure(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "full.zip")
	if err := os.WriteFile(zipPath, []byte("zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs := newFakeStore(BackupKey(ts(1)))
	fs.listErr = errors.New("list boom")
	res, err := UploadAndRotate(context.Background(), fs, zipPath, 8, ts(2))
	if err == nil {
		t.Fatal("轮转失败应返回错误")
	}
	if res.Key == "" {
		t.Fatal("上传已成功：结果应带 key 供调用方区分场景")
	}
	if _, ok := fs.objects[BackupKey(ts(2))]; !ok {
		t.Fatal("新对象应已上传")
	}
}

func TestClassifyErrorScenes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"密钥无效", minio.ErrorResponse{Code: "InvalidAccessKeyId"}, "密钥无效"},
		{"桶不存在", minio.ErrorResponse{Code: "NoSuchBucket"}, "存储桶不存在"},
		{"超时", context.DeadlineExceeded, "超时"},
		{"未知", errors.New("weird"), "R2 上传失败"},
	}
	for _, c := range cases {
		if got := ClassifyError(c.err); !strings.Contains(got, c.want) {
			t.Fatalf("%s: 场景应含 %q，got %q", c.name, c.want, got)
		}
	}
}
