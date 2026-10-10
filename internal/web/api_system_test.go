package web

// 系统设置 API（/api/v1/system/config）与运行设置绑定频道投递开关的 API 行为测试。

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
		t.Fatalf("绑定频道投递开关缺省应为 true: %+v", before)
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

func TestAPISettingsDumpChannels(t *testing.T) {
	binder := &fakeChannelBinder{verifyID: -1001234567890, verifyTitle: "Spore Cache"}
	e := newTestEnvOpts(t, func(_ *config.Config, o *Options) { o.Bindings = binder })
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()

	var before apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &before)
	if len(before.DumpChannels) != 0 || before.DumpChannelID != 0 || before.DumpChannelTitle != "" {
		t.Fatalf("缺省应未配置缓存频道: %+v", before)
	}

	// 添加：目标经 binding 校验后写入列表（默认启用），兼容派生值同步
	resp := e.apiPost(j, "/api/v1/settings", csrf, `{"dump_channels":[{"target":"@sporecache"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("添加缓存频道应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if binder.verifyTarget != "@sporecache" {
		t.Fatalf("应经 binding 校验目标，得到 %q", binder.verifyTarget)
	}
	saved := syscfg.LoadDumpChannels(ctx, e.st)
	if len(saved) != 1 || saved[0].ChannelID != -1001234567890 ||
		saved[0].Title != "Spore Cache" || !saved[0].Enabled {
		t.Fatalf("应保存启用的频道项: %+v", saved)
	}
	if !e.containsAction("settings.dump_channels") {
		t.Error("配置变更应写审计 settings.dump_channels")
	}
	var after apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &after)
	if len(after.DumpChannels) != 1 || after.DumpChannelID != -1001234567890 || after.DumpChannelTitle != "Spore Cache" {
		t.Fatalf("视图应含列表与派生值: %+v", after)
	}

	// 校验失败（非频道/无权限）：受控 400，配置不变
	binder.verifyErr = errors.New("not postable")
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"dump_channels":[{"target":"@bad"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("校验失败应 400，得到 %d", resp.StatusCode)
	}
	if got := syscfg.LoadDumpChannels(ctx, e.st); len(got) != 1 {
		t.Fatalf("校验失败不应改动配置: %+v", got)
	}

	// 重复添加同一目标：400
	binder.verifyErr = nil
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"dump_channels":[{"id":-1001234567890},{"target":"@sporecache"}]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("重复添加应 400，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}

	// 停用：按 ID 传 enabled=false；派生值归零（无启用频道）
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"dump_channels":[{"id":-1001234567890,"enabled":false}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("停用应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	saved = syscfg.LoadDumpChannels(ctx, e.st)
	if len(saved) != 1 || saved[0].Enabled {
		t.Fatalf("应保存停用状态: %+v", saved)
	}
	getAPIJSON(t, e, j, "/api/v1/settings", &after)
	if after.DumpChannelID != 0 || after.DumpChannelTitle != "" {
		t.Fatalf("无启用频道时派生值应为零值: %+v", after)
	}

	// 追加第二个频道（整体替换语义：已有项按 ID 保序保留，新增项停用加入）
	binder2 := &fakeChannelBinder{verifyID: -1001234567890, verifyTitle: "Cache A"}
	e2 := newTestEnvOpts(t, func(_ *config.Config, o *Options) { o.Bindings = binder2 })
	j2 := e2.login(t)
	csrf2 := apiCSRFToken(t, e2, j2)
	resp = e2.apiPost(j2, "/api/v1/settings", csrf2, `{"dump_channels":[{"target":"@cacheA"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("添加首个频道应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	binder2.verifyID, binder2.verifyTitle = -100777, "Cache B"
	resp = e2.apiPost(j2, "/api/v1/settings", csrf2,
		`{"dump_channels":[{"id":-1001234567890},{"target":"@cacheB","enabled":false}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("多频道添加应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	saved2 := syscfg.LoadDumpChannels(ctx, e2.st)
	if len(saved2) != 2 || saved2[0].ChannelID != -1001234567890 || !saved2[0].Enabled ||
		saved2[0].Title != "Cache A" || saved2[1].ChannelID != -100777 || saved2[1].Enabled {
		t.Fatalf("应保存两频道（第二项停用，首项标题保持快照）: %+v", saved2)
	}

	// 移除：整体替换为不含该项的列表（空列表 = 全部清除）
	resp = e2.apiPost(j2, "/api/v1/settings", csrf2, `{"dump_channels":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("清空应 200，得到 %d", resp.StatusCode)
	}
	if got := syscfg.LoadDumpChannels(ctx, e2.st); len(got) != 0 {
		t.Fatalf("清空后列表应为空: %+v", got)
	}
}

func TestEffectiveDumpChannelsFallbackChain(t *testing.T) {
	e := newTestEnv(t, func(c *config.Config) { c.DumpChannelID = -1009999999999 })
	ctx := context.Background()

	// settings 缺失：旧键与列表键都缺失 → 回落 env（折算为单条启用项）
	channels := syscfg.LoadEffectiveDumpChannels(ctx, e.st, e.srv.cfg.DumpChannelID)
	if len(channels) != 1 || channels[0].ChannelID != -1009999999999 || !channels[0].Enabled {
		t.Fatalf("settings 缺失应回落 env: %+v", channels)
	}
	// 旧键显式 0（Web 端旧版「清除配置」的产物）：必须覆盖 env，不得重新启用
	if err := e.st.SetSetting(ctx, "dump_channel_id", "0"); err != nil {
		t.Fatalf("写显式关闭失败: %v", err)
	}
	channels = syscfg.LoadEffectiveDumpChannels(ctx, e.st, e.srv.cfg.DumpChannelID)
	if len(channels) != 0 {
		t.Fatalf("旧键显式 0 应覆盖 env: %+v", channels)
	}
	// 旧键配置了频道：折算为单条启用项（标题取旧标题键快照）
	if err := e.st.SetSetting(ctx, "dump_channel_id", "-1001234567890"); err != nil {
		t.Fatalf("写旧键失败: %v", err)
	}
	if err := e.st.SetSetting(ctx, "dump_channel_title", `"Spore Cache"`); err != nil {
		t.Fatalf("写旧标题键失败: %v", err)
	}
	channels = syscfg.LoadEffectiveDumpChannels(ctx, e.st, e.srv.cfg.DumpChannelID)
	if len(channels) != 1 || channels[0].ChannelID != -1001234567890 ||
		channels[0].Title != "Spore Cache" || !channels[0].Enabled {
		t.Fatalf("旧键应折算为单条启用项: %+v", channels)
	}
	// 列表键写入（含空列表）即完全接管，优先于旧键与 env
	if err := syscfg.SaveDumpChannels(ctx, e.st, []syscfg.DumpChannel{
		{ChannelID: -100555, Title: "New", Enabled: true}}); err != nil {
		t.Fatalf("写列表键失败: %v", err)
	}
	channels = syscfg.LoadEffectiveDumpChannels(ctx, e.st, e.srv.cfg.DumpChannelID)
	if len(channels) != 1 || channels[0].ChannelID != -100555 {
		t.Fatalf("列表键应接管配置: %+v", channels)
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

func TestAPISettingsWatchForwardChannels(t *testing.T) {
	binder := &fakeChannelBinder{verifyID: -1001234567890, verifyTitle: "转发频道"}
	e := newTestEnvOpts(t, func(_ *config.Config, o *Options) { o.Bindings = binder })
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	ctx := context.Background()

	var before apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &before)
	if len(before.WatchForwardChannels) != 0 {
		t.Fatalf("缺省应未配置转发频道: %+v", before.WatchForwardChannels)
	}

	// 配置：逐项经 binding 校验后保存；两个目标解析到同一 ID → 去重为 1
	resp := e.apiPost(j, "/api/v1/settings", csrf, `{"watch_forward_channels":["@chan_a","-100999"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("配置转发频道应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	if binder.verifyTarget != "-100999" {
		t.Fatalf("应逐项经 binding 校验，最后校验目标 %q", binder.verifyTarget)
	}
	got := syscfg.LoadWatchForwardChannels(ctx, e.st)
	if len(got) != 1 || got[0].ChannelID != -1001234567890 || got[0].Title != "转发频道" {
		t.Fatalf("应保存解析去重后的数字 ID 与标题: %+v", got)
	}
	if !e.containsAction("settings.watch_forward_channels") {
		t.Error("配置变更应写审计 settings.watch_forward_channels")
	}

	// GET 回读
	var after apiSettingsView
	getAPIJSON(t, e, j, "/api/v1/settings", &after)
	if len(after.WatchForwardChannels) != 1 || after.WatchForwardChannels[0].ChannelID != -1001234567890 {
		t.Fatalf("设置视图应回读转发频道: %+v", after.WatchForwardChannels)
	}

	// 校验失败：受控 400，配置不变
	binder.verifyErr = errors.New("not postable")
	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"watch_forward_channels":["@bad"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("校验失败应 400，得到 %d", resp.StatusCode)
	}
	if got := syscfg.LoadWatchForwardChannels(ctx, e.st); len(got) != 1 {
		t.Fatalf("校验失败不应改动配置: %+v", got)
	}

	// 清空：整体替换语义（空数组 = 清空，回落缓存兜底）
	binder.verifyErr = nil
	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/settings", csrf, `{"watch_forward_channels":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("清空应 200，得到 %d", resp.StatusCode)
	}
	if got := syscfg.LoadWatchForwardChannels(ctx, e.st); len(got) != 0 {
		t.Fatalf("清空后应为空: %+v", got)
	}
}
