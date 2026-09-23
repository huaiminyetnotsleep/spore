package web

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// seedErrorLog 落一条错误日志供端点测试。
func seedErrorLog(t *testing.T, e *testEnv, in store.ErrorLog) store.ErrorLog {
	t.Helper()
	row, err := e.st.InsertErrorLog(context.Background(), in)
	if err != nil {
		t.Fatalf("写入错误日志失败: %v", err)
	}
	return row
}

func TestAPIErrorLogsList(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	a := seedErrorLog(t, e, store.ErrorLog{
		Source: store.ErrorSourceRequest, Code: "BOT_SEND_FAILED", Stage: "send",
		Message: "任务失败", Detail: "FLOOD_WAIT_9: 3000",
		Context: map[string]any{"bot_id": int64(42)}, RequestID: 7,
	})
	_ = seedErrorLog(t, e, store.ErrorLog{
		Source: store.ErrorSourceCloud, Code: "CLOUD_NETWORK", Message: "测试失败",
		CreatedAt: a.CreatedAt + 1000,
	})

	// 全量倒序
	var list struct {
		Items []store.ErrorLog `json:"items"`
		Total int              `json:"total"`
	}
	body := getAPIJSON(t, e, j, "/api/v1/error-logs", &list)
	_ = body
	if list.Total != 2 || list.Items[0].Source != store.ErrorSourceCloud {
		t.Fatalf("全量应 2 行且最新在前: %+v", list)
	}

	// 筛选组合：来源 + 请求 ID
	getAPIJSON(t, e, j, "/api/v1/error-logs?source=request&request_id=7", &list)
	if list.Total != 1 || list.Items[0].ID != a.ID {
		t.Fatalf("组合筛选应命中第一行: %+v", list)
	}
	// detail 与上下文随行下发（管理端展示根因）
	if list.Items[0].Detail != "FLOOD_WAIT_9: 3000" || list.Items[0].Context["bot_id"] != float64(42) {
		t.Fatalf("根因与参数应下发: %+v", list.Items[0])
	}

	// 非法筛选参数 → 400
	if resp := e.do(j, http.MethodGet, "/api/v1/error-logs?source=bogus", "", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法来源应 400，得到 %d", resp.StatusCode)
	}
	if resp := e.do(j, http.MethodGet, "/api/v1/error-logs?severity=fatal", "", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法级别应 400，得到 %d", resp.StatusCode)
	}
	if resp := e.do(j, http.MethodGet, "/api/v1/error-logs?request_id=0", "", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("request_id=0 应 400，得到 %d", resp.StatusCode)
	}

	// 未认证 → 401
	if resp := e.do(&jar{}, http.MethodGet, "/api/v1/error-logs", "", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未认证应 401，得到 %d", resp.StatusCode)
	}
}

func TestAPIErrorLogsDeleteByID(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	a := seedErrorLog(t, e, store.ErrorLog{Source: store.ErrorSourceRequest, Message: "a"})
	b := seedErrorLog(t, e, store.ErrorLog{Source: store.ErrorSourceRequest, Message: "b"})

	resp := e.apiPost(j, "/api/v1/error-logs/delete", csrf,
		`{"ids":[`+strconv.FormatInt(a.ID, 10)+`]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("按 ID 删除应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	var got struct {
		OK      bool  `json:"ok"`
		Deleted int64 `json:"deleted"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &got)
	if !got.OK || got.Deleted != 1 {
		t.Fatalf("响应信封不符: %+v", got)
	}
	if _, total, _ := e.st.ListErrorLogs(context.Background(), store.ErrorLogsQuery{}); total != 1 {
		t.Fatalf("应剩 1 行，得到 %d", total)
	}

	// 空 / 超限 / 非法 ID / 与时间段模式混用 → 400；缺 CSRF → 403
	if resp := e.apiPost(j, "/api/v1/error-logs/delete", csrf, `{"ids":[]}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 ids 应 400，得到 %d", resp.StatusCode)
	}
	long := `{"ids":[` + repeatID(t, 101) + `]}`
	if resp := e.apiPost(j, "/api/v1/error-logs/delete", csrf, long); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("超 100 个 ids 应 400，得到 %d", resp.StatusCode)
	}
	if resp := e.apiPost(j, "/api/v1/error-logs/delete", csrf, `{"ids":[-1]}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("负数 ID 应 400，得到 %d", resp.StatusCode)
	}
	if resp := e.apiPost(j, "/api/v1/error-logs/delete", csrf,
		`{"ids":[1],"before":123}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("两种模式混用应 400，得到 %d", resp.StatusCode)
	}
	if resp := e.apiPost(j, "/api/v1/error-logs/delete", "", `{"ids":[1]}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("缺 CSRF 应 403，得到 %d", resp.StatusCode)
	}
	_ = b
}

func TestAPIErrorLogsDeleteByRange(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	a := seedErrorLog(t, e, store.ErrorLog{Source: store.ErrorSourceRequest, Message: "旧"})
	b := seedErrorLog(t, e, store.ErrorLog{Source: store.ErrorSourceCloud, Code: "CLOUD_NETWORK",
		Message: "新", CreatedAt: a.CreatedAt + 60_000})

	// 无时间界拒绝；after >= before 拒绝；非法来源拒绝
	if resp := e.apiPost(j, "/api/v1/error-logs/delete", csrf, `{}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("无时间界应 400，得到 %d", resp.StatusCode)
	}
	if resp := e.apiPost(j, "/api/v1/error-logs/delete", csrf,
		`{"after":200,"before":100}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("after>=before 应 400，得到 %d", resp.StatusCode)
	}
	if resp := e.apiPost(j, "/api/v1/error-logs/delete", csrf,
		`{"before":9999999999999,"source":"bogus"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法来源应 400，得到 %d", resp.StatusCode)
	}

	// before 覆盖旧行、叠加来源条件（request 命中 a，cloud 的 b 保留）
	body := `{"before":` + strconv.FormatInt(a.CreatedAt+1, 10) + `,"source":"request"}`
	resp := e.apiPost(j, "/api/v1/error-logs/delete", csrf, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("按时间段删除应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	var got struct {
		OK      bool  `json:"ok"`
		Deleted int64 `json:"deleted"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &got)
	if !got.OK || got.Deleted != 1 {
		t.Fatalf("应删除 1 行: %+v", got)
	}
	rows, total, _ := e.st.ListErrorLogs(context.Background(), store.ErrorLogsQuery{})
	if total != 1 || rows[0].ID != b.ID {
		t.Fatalf("应只剩 cloud 行: %d %+v", total, rows)
	}

	// 审计已记录（mode=range）
	audits, err := e.st.ListAudit(context.Background(), 10, 0)
	if err != nil || len(audits) == 0 {
		t.Fatalf("删除动作应写审计: %v %d", err, len(audits))
	}
	if audits[0].Action != "error_logs.delete" {
		t.Fatalf("最新审计应为 error_logs.delete: %+v", audits[0])
	}
}

// repeatID 生成 "1,1,…" 形式的 n 个正整数（超限用例）。
func repeatID(t *testing.T, n int) string {
	t.Helper()
	if n <= 0 {
		t.Fatal("n 必须为正")
	}
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ","
		}
		s += "1"
	}
	return s
}
