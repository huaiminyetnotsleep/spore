package web

// 机器人管理 API（/api/v1/bots）测试：列表合并 env ∪ 文件、增删走
// bots.json（重启生效）、审计留痕；token 任何响应不回显。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/botlist"
	"github.com/huaiminyetnotsleep/spore/internal/config"
)

const (
	testTokenA = "1111111111:ABCDEFGHIJKLMNOPqrstuv"
	testTokenB = "2222222222:ABCDEFGHIJKLMNOPqrstuv"
)

// newBotsTestEnv 构造带机器人列表管理器的测试环境；envTokens 写入 cfg。
func newBotsTestEnv(t *testing.T, envTokens []string) *testEnv {
	t.Helper()
	var e *testEnv
	e = newTestEnvOpts(t, func(c *config.Config, opt *Options) {
		c.BotTokens = envTokens
		if len(envTokens) > 0 {
			c.BotToken = envTokens[0]
		}
		opt.BotList = botlist.NewManager(t.TempDir(), testLogger())
	})
	return e
}

func TestAPIBotsListEnvOnly(t *testing.T) {
	e := newBotsTestEnv(t, []string{testTokenA})
	j := e.login(t)

	var view botsView
	getAPIJSON(t, e, j, "/api/v1/bots", &view)
	if len(view.Bots) != 1 {
		t.Fatalf("env 单 bot 应有一行，得到 %+v", view.Bots)
	}
	row := view.Bots[0]
	if !row.Primary || row.Source != "env" || row.BotID != 1111111111 {
		t.Fatalf("env 主 bot 行不符: %+v", row)
	}
	if !row.RestartPending {
		t.Errorf("进程未接入的条目应标记待重启: %+v", row)
	}
	// token 只进不出：响应体不得包含 token 值
	body := getAPIJSON(t, e, j, "/api/v1/bots", &view)
	if strings.Contains(body, testTokenA) {
		t.Fatalf("响应不得回显 token：%s", body)
	}
}

func TestAPIBotsAddAndDelete(t *testing.T) {
	e := newBotsTestEnv(t, []string{testTokenA})
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)

	// 非法 token → 400
	resp := e.apiPost(j, "/api/v1/bots/add", csrf, `{"token":"not-a-token"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 token 应 400，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	// 合法添加 → 200 + 文件条目（待重启）+ 审计
	resp = e.apiPost(j, "/api/v1/bots/add", csrf, `{"token":"`+testTokenB+`"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("添加应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out BotMutationResultAlias
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if !out.OK || len(out.Bots) != 2 || !out.NeedApply {
		t.Fatalf("添加后视图不符: %+v", out)
	}
	fileRow := out.Bots[1]
	if fileRow.Source != "file" || fileRow.BotID != 2222222222 || !fileRow.RestartPending {
		t.Fatalf("文件条目应为待重启的 file 来源: %+v", fileRow)
	}
	if !e.containsAction("bots.add") {
		t.Error("添加应写审计")
	}

	// 重复添加 → 400
	resp = e.apiPost(j, "/api/v1/bots/add", csrf, `{"token":"`+testTokenB+`"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("重复添加应 400，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)

	// 按 bot id 删除文件条目
	resp = e.apiPost(j, "/api/v1/bots/2222222222/delete", csrf, "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("删除应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var delOut BotMutationResultAlias
	decodeAPIJSON(t, bodyOf(t, resp), &delOut)
	// 测试环境无运行时身份：env 条目保持待重启标记，仅校验行数
	if len(delOut.Bots) != 1 {
		t.Fatalf("删除后应只剩 env 条目: %+v", delOut)
	}
	if !e.containsAction("bots.remove") {
		t.Error("删除应写审计")
	}

	// 删除 env 来源 → 400（env 不在文件配置中）
	resp = e.apiPost(j, "/api/v1/bots/1111111111/delete", csrf, "{}")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("删除 env 条目应 400，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

func TestAPIBotsUnavailableWithoutManager(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	// 未注入 BotList：受控 503
	resp := e.do(j, "GET", "/api/v1/bots", "", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("缺失管理器应 503，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}

// BotMutationResultAlias 与 api_bots.go 的匿名响应结构对齐（测试解码用）。
type BotMutationResultAlias struct {
	OK          bool        `json:"ok"`
	Bots        []apiBotRow `json:"bots"`
	MaxBots     int         `json:"max_bots"`
	NeedApply   bool        `json:"need_apply"`
	RestartHint string      `json:"restart_hint"`
}

// fakeBotRuntime 记录暂停/恢复调用。
type fakeBotRuntime struct {
	pausedIDs  []int64
	resumedIDs []int64
}

func (f *fakeBotRuntime) PauseBot(_ context.Context, botID int64) error {
	f.pausedIDs = append(f.pausedIDs, botID)
	return nil
}

func (f *fakeBotRuntime) ResumeBot(_ context.Context, botID int64) error {
	f.resumedIDs = append(f.resumedIDs, botID)
	return nil
}

// TestAPIBotPauseResume：暂停/恢复端点落 settings（重启保持）、调用运行时
// 控制（即时生效）、写审计，列表回显 paused 状态。
func TestAPIBotPauseResume(t *testing.T) {
	var runtime fakeBotRuntime
	e := newTestEnvOpts(t, func(c *config.Config, opt *Options) {
		c.BotTokens = []string{testTokenA}
		opt.BotList = botlist.NewManager(t.TempDir(), testLogger())
		opt.BotRuntimeControl = &runtime
	})
	j := e.login(t)
	csrf := e.sessionCSRF(t, j)

	// 暂停 → 200 + paused 回显 + 运行时调用 + 审计
	resp := e.apiPost(j, "/api/v1/bots/1111111111/pause", csrf, "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("暂停应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out BotMutationResultAlias
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if len(out.Bots) != 1 || !out.Bots[0].Paused {
		t.Fatalf("暂停后列表应回显 paused: %+v", out.Bots)
	}
	if len(runtime.pausedIDs) != 1 || runtime.pausedIDs[0] != 1111111111 {
		t.Fatalf("应调用运行时暂停: %+v", runtime.pausedIDs)
	}
	if !e.containsAction("bot.pause") {
		t.Error("暂停应写审计")
	}

	// 持久化验证：重启语义 = 新 Server 读同一数据库仍看到暂停态
	paused := LoadPausedBots(context.Background(), e.st)
	if !paused[1111111111] {
		t.Fatal("暂停态应持久化在 settings")
	}

	// 恢复 → 200 + 清除 paused + 运行时恢复调用 + 审计
	resp = e.apiPost(j, "/api/v1/bots/1111111111/resume", csrf, "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("恢复应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	decodeAPIJSON(t, bodyOf(t, resp), &out)
	if len(out.Bots) != 1 || out.Bots[0].Paused {
		t.Fatalf("恢复后 paused 应为 false: %+v", out.Bots)
	}
	if len(runtime.resumedIDs) != 1 || runtime.resumedIDs[0] != 1111111111 {
		t.Fatalf("应调用运行时恢复: %+v", runtime.resumedIDs)
	}
	if !e.containsAction("bot.resume") {
		t.Error("恢复应写审计")
	}
	if len(LoadPausedBots(context.Background(), e.st)) != 0 {
		t.Fatal("恢复后暂停集合应为空")
	}
}
