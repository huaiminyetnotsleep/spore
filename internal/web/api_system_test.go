package web

// 系统设置 API（/api/v1/system/config）与运行设置频道同步开关的 API 行为测试。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

func TestAPISystemConfigGetDefault(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	var view sysConfigView
	getAPIJSON(t, e, j, "/api/v1/system/config", &view)
	if view.SystemName != "Spore" {
		t.Fatalf("未配置时应返回缺省名称，得到 %q", view.SystemName)
	}
}

func TestAPISystemConfigSaveAndAudit(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	resp := e.apiPost(j, "/api/v1/system/config", csrf, `{"system_name":"  我的提取站  "}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK         bool   `json:"ok"`
		SystemName string `json:"system_name"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || out.SystemName != "我的提取站" {
		t.Errorf("保存结果不对: %+v", out)
	}
	if got := syscfg.Name(context.Background(), e.st); got != "我的提取站" {
		t.Errorf("名称应即时生效，得到 %q", got)
	}
	if !e.containsAction("settings.system_name") {
		t.Error("名称变更应写审计 settings.system_name")
	}

	// 值未变（规范化后相同）不重复写审计
	actions := len(e.auditActions())
	resp = e.apiPost(j, "/api/v1/system/config", csrf, `{"system_name":"我的提取站"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("同值保存应 200，得到 %d", resp.StatusCode)
	}
	if got := len(e.auditActions()); got != actions {
		t.Errorf("同值保存不应写审计，审计数 %d → %d", actions, got)
	}

	// 参数拒绝：空名称 / 超长 / 控制字符；名称不变
	for name, bad := range map[string]string{
		"空名称":  `{"system_name":"   "}`,
		"超长名称": `{"system_name":"` + strings.Repeat("字", 33) + `"}`,
		"控制字符": `{"system_name":"bad\u0007name"}`,
		"缺字段":  `{}`,
	} {
		resp = e.apiPost(j, "/api/v1/system/config", csrf, bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s 应 400，得到 %d（body=%s）", name, resp.StatusCode, bodyOf(t, resp))
			continue
		}
		var env apiErrorEnvelope
		decodeAPIJSON(t, bodyOf(t, resp), &env)
		if env.Error.Code != apiCodeBadRequest || env.Error.Message == "" {
			t.Errorf("%s 错误响应不对: %+v", name, env.Error)
		}
	}
	if got := syscfg.Name(context.Background(), e.st); got != "我的提取站" {
		t.Errorf("参数拒绝后名称应保持不变，得到 %q", got)
	}
}

func TestAPISettingsChannelCopySwitch(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()

	var before apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &before)
	if !before.ChannelCopyEnabled {
		t.Fatalf("频道同步开关缺省应为 true: %+v", before)
	}

	// 关闭：即时生效并写审计
	resp := e.apiPost(j, "/api/v1/settings", csrf, `{"channel_copy_enabled":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("关闭开关应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if LoadChannelCopyEnabled(ctx, e.st) {
		t.Error("关闭后 LoadChannelCopyEnabled 应为 false")
	}
	if !e.containsAction("settings.channel_copy_enabled") {
		t.Error("开关变更应写审计 settings.channel_copy_enabled")
	}

	// 不变更（nil）保持原值
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"dedup_window_min":15}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("不带开关的保存应 200，得到 %d", resp.StatusCode)
	}
	if LoadChannelCopyEnabled(ctx, e.st) {
		t.Error("未携带开关字段的保存不应改动开关")
	}

	// 重新打开
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"channel_copy_enabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重开开关应 200，得到 %d", resp.StatusCode)
	}
	if !LoadChannelCopyEnabled(ctx, e.st) {
		t.Error("重开后开关应为 true")
	}
}

func TestAPISettingsTGReuseSwitch(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()

	var before apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &before)
	if !before.TGReuseEnabled {
		t.Fatalf("链接复用开关缺省应为 true: %+v", before)
	}

	// 关闭：即时生效并写审计
	resp := e.apiPost(j, "/api/v1/settings", csrf, `{"tg_reuse_enabled":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("关闭开关应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if LoadTGReuseEnabled(ctx, e.st) {
		t.Error("关闭后 LoadTGReuseEnabled 应为 false")
	}
	if !e.containsAction("settings.tg_reuse_enabled") {
		t.Error("开关变更应写审计 settings.tg_reuse_enabled")
	}

	// 不变更（nil）保持原值
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"dedup_window_min":15}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("不带开关的保存应 200，得到 %d", resp.StatusCode)
	}
	if LoadTGReuseEnabled(ctx, e.st) {
		t.Error("未携带开关字段的保存不应改动开关")
	}

	// 重新打开
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"tg_reuse_enabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("重开开关应 200，得到 %d", resp.StatusCode)
	}
	if !LoadTGReuseEnabled(ctx, e.st) {
		t.Error("重开后开关应为 true")
	}
}

func TestAPISettingsDumpChannel(t *testing.T) {
	binder := &fakeChannelBinder{verifyID: -1001234567890, verifyTitle: "Spore Cache"}
	e := newTestEnvOpts(t, func(_ *config.Config, o *Options) { o.Bindings = binder })
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()

	var before apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &before)
	if before.DumpChannelID != 0 || before.DumpChannelTitle != "" {
		t.Fatalf("缺省应未配置缓存频道: %+v", before)
	}

	// 配置：目标经 binding 校验后保存数字 ID 与标题
	resp := e.apiPost(j, "/api/v1/settings", csrf, `{"dump_channel":"@sporecache"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("配置缓存频道应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if binder.verifyTarget != "@sporecache" {
		t.Fatalf("应经 binding 校验目标，得到 %q", binder.verifyTarget)
	}
	if got := LoadDumpChannelID(ctx, e.st); got != -1001234567890 {
		t.Fatalf("应保存数字 ID: %d", got)
	}
	if got := LoadDumpChannelTitle(ctx, e.st); got != "Spore Cache" {
		t.Fatalf("应保存标题: %q", got)
	}
	if !e.containsAction("settings.dump_channel") {
		t.Error("配置变更应写审计 settings.dump_channel")
	}

	// 校验失败（非频道/无权限）：受控 400，配置不变
	binder.verifyErr = errors.New("not postable")
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"dump_channel":"@bad"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("校验失败应 400，得到 %d", resp.StatusCode)
	}
	if got := LoadDumpChannelID(ctx, e.st); got != -1001234567890 {
		t.Fatalf("校验失败不应改动配置: %d", got)
	}

	// 清除：空串 → 0
	binder.verifyErr = nil
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"dump_channel":""}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("清除应 200，得到 %d", resp.StatusCode)
	}
	if got := LoadDumpChannelID(ctx, e.st); got != 0 {
		t.Fatalf("清除后应为 0: %d", got)
	}
}

func TestEffectiveDumpChannelIDSettingsOverrideEnv(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.DumpChannelID = -1009999999999 })
	ctx := context.Background()

	// settings 缺失：回落 env
	if got := LoadEffectiveDumpChannelID(ctx, e.st, e.srv.cfg.DumpChannelID); got != -1009999999999 {
		t.Fatalf("settings 缺失应回落 env，得到 %d", got)
	}
	// Web 清除落显式 0：必须覆盖 env，不得重新启用
	if err := e.st.SetSetting(ctx, settingKeyDumpChannelID, "0"); err != nil {
		t.Fatalf("写显式关闭失败: %v", err)
	}
	if got := LoadEffectiveDumpChannelID(ctx, e.st, e.srv.cfg.DumpChannelID); got != 0 {
		t.Fatalf("settings 显式 0 应覆盖 env，得到 %d", got)
	}
}

func TestAPISettingsMaxRequestAttempts(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()

	// 缺省值 3
	var before apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &before)
	if before.MaxRequestAttempts != syscfg.DefaultMaxRequestAttempts {
		t.Fatalf("最大尝试次数缺省应为 %d: %+v", syscfg.DefaultMaxRequestAttempts, before)
	}

	// 修改为 5：即时生效并写审计
	resp := e.apiPost(j, "/api/v1/settings", csrf, `{"max_request_attempts":5}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if got := syscfg.LoadMaxRequestAttempts(ctx, e.st); got != 5 {
		t.Errorf("保存后 LoadMaxRequestAttempts 应为 5，得到 %d", got)
	}
	if !e.containsAction("settings.request_retry") {
		t.Error("变更应写审计 settings.request_retry")
	}

	// 不变更（nil）保持原值
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"dedup_window_min":15}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("不带字段的保存应 200，得到 %d", resp.StatusCode)
	}
	if got := syscfg.LoadMaxRequestAttempts(ctx, e.st); got != 5 {
		t.Errorf("未携带字段的保存不应改动配置，得到 %d", got)
	}

	// 越界 → 400 且不写库（写操作后 token 轮换）
	csrf = apiCSRFToken(t, e, j)
	for _, bad := range []string{`{"max_request_attempts":0}`, `{"max_request_attempts":11}`} {
		resp = e.apiPost(j, "/api/v1/settings", csrf, bad)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("越界值 %s 应 400，得到 %d", bad, resp.StatusCode)
		}
	}
	if got := syscfg.LoadMaxRequestAttempts(ctx, e.st); got != 5 {
		t.Errorf("校验失败后旧值应保留，得到 %d", got)
	}
}
