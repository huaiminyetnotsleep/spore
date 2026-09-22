package web

// R2 定时备份配置 API 契约测试：GET 脱敏视图、POST 掩码/空值沿用密钥、
// 开启门禁与参数校验 400、间隔/份数写 syscfg、审计动作与测试端点的前置
// 校验（真实连通性不在此测——TestConnection 走网络，由 r2backup 单测与
// 真机验收覆盖）。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/r2backup"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

func r2PostBody(t *testing.T, payload any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// seedR2Config 直接落一份完整配置文件（绕过 API，供 GET/掩码用例预置）。
func seedR2Config(t *testing.T, dataDir string, cfg r2backup.Config) {
	t.Helper()
	if err := r2backup.Save(dataDir, cfg); err != nil {
		t.Fatalf("预置 R2 配置失败: %v", err)
	}
}

func validR2Config() r2backup.Config {
	return r2backup.Config{
		Enabled:         true,
		AccountID:       "0123456789abcdef0123456789abcdef",
		AccessKeyID:     "akid-12345678",
		SecretAccessKey: "secret-12345678",
		Bucket:          "spore-backup",
	}
}

func TestAPIBackupR2GetMasksSecrets(t *testing.T) {
	e := newTestEnv(t, nil)
	dataDir := t.TempDir()
	e.srv.cfg.DataDir = dataDir
	seedR2Config(t, dataDir, validR2Config())
	_ = syscfg.SetBackupIntervalHours(t.Context(), e.st, 3)

	j := e.login(t)
	resp := e.do(j, http.MethodGet, "/api/v1/backup/r2", "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET 状态码 %d", resp.StatusCode)
	}
	var view struct {
		IntervalHours int `json:"interval_hours"`
		KeepCount     int `json:"keep_count"`
		R2            struct {
			Enabled         bool   `json:"enabled"`
			Complete        bool   `json:"complete"`
			AccountID       string `json:"account_id"`
			Bucket          string `json:"bucket"`
			Endpoint        string `json:"endpoint"`
			AccessKeyMasked string `json:"access_key_id"`
			SecretMasked    string `json:"secret_access_key"`
		} `json:"r2"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if view.IntervalHours != 3 || view.KeepCount != syscfg.DefaultBackupKeepCount {
		t.Fatalf("间隔/份数口径不符: %+v", view)
	}
	if !view.R2.Enabled || !view.R2.Complete {
		t.Fatalf("R2 状态不符: %+v", view.R2)
	}
	if view.R2.AccessKeyMasked != r2SecretMask || view.R2.SecretMasked != r2SecretMask {
		t.Fatalf("密钥必须掩码回显: %+v", view.R2)
	}
	if !strings.Contains(view.R2.Endpoint, ".r2.cloudflarestorage.com") {
		t.Fatalf("端点应由 Account ID 拼出: %s", view.R2.Endpoint)
	}
}

func TestAPIBackupR2PostMaskKeepsSecret(t *testing.T) {
	e := newTestEnv(t, nil)
	dataDir := t.TempDir()
	e.srv.cfg.DataDir = dataDir
	seedR2Config(t, dataDir, validR2Config())

	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	body := r2PostBody(t, map[string]any{
		"r2": map[string]any{
			"enabled":           true,
			"account_id":        "0123456789abcdef0123456789abcdef",
			"access_key_id":     r2SecretMask,
			"secret_access_key": "",
			"bucket":            "spore-backup",
		},
	})
	resp := e.apiPost(j, "/api/v1/backup/r2", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST 状态码 %d", resp.StatusCode)
	}
	got, err := r2backup.Load(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessKeyID != "akid-12345678" || got.SecretAccessKey != "secret-12345678" {
		t.Fatalf("掩码/空值应沿用已存密钥: %+v", got)
	}
	if !e.containsAction("backup.r2_config") {
		t.Fatal("应写 backup.r2_config 审计")
	}
}

func TestAPIBackupR2PostEnableIncompleteRejected(t *testing.T) {
	e := newTestEnv(t, nil)
	dataDir := t.TempDir()
	e.srv.cfg.DataDir = dataDir

	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	body := r2PostBody(t, map[string]any{
		"r2": map[string]any{"enabled": true, "account_id": "0123456789abcdef0123456789abcdef"},
	})
	resp := e.apiPost(j, "/api/v1/backup/r2", csrf, body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("开启且不完整应 400，got %d", resp.StatusCode)
	}
}

func TestAPIBackupR2PostIntervalValidation(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)

	resp := e.apiPost(j, "/api/v1/backup/r2", csrf,
		r2PostBody(t, map[string]any{"interval_hours": 999}))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法间隔应 400，got %d", resp.StatusCode)
	}

	// 合法间隔落库并写与设置页同名的审计
	resp = e.apiPost(j, "/api/v1/backup/r2", csrf,
		r2PostBody(t, map[string]any{"interval_hours": 12, "keep_count": 5}))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("合法保存应 200，got %d", resp.StatusCode)
	}
	if got := syscfg.LoadBackupIntervalHours(t.Context(), e.st); got != 12 {
		t.Fatalf("间隔应写入 syscfg，got %d", got)
	}
	if got := syscfg.LoadBackupKeepCount(t.Context(), e.st); got != 5 {
		t.Fatalf("份数应写入 syscfg，got %d", got)
	}
	if !e.containsAction("settings.backup_interval") || !e.containsAction("settings.backup_keep") {
		t.Fatal("应沿用设置页同名审计动作")
	}
}

func TestAPIBackupR2TestRequiresCompleteConfig(t *testing.T) {
	e := newTestEnv(t, nil)
	dataDir := t.TempDir()
	e.srv.cfg.DataDir = dataDir // 无配置文件

	j := e.login(t)
	csrf := e.sessionCSRF(t, j)
	resp := e.apiPost(j, "/api/v1/backup/r2/test", csrf, "{}")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("未配置即测试应 400，got %d", resp.StatusCode)
	}
}

func TestAPIBackupR2Unauthenticated(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.cfg.DataDir = t.TempDir()
	resp := e.do(newJar(t), http.MethodGet, "/api/v1/backup/r2", "", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未认证 GET 应 401，got %d", resp.StatusCode)
	}
}
