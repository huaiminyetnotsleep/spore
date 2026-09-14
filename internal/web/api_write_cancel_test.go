package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func TestAPIRequestCancelAndBatch(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)
	seedUser(t, e, 1, store.UserEnabled)
	first := seedRequest(t, e, 1, "example", 1, store.RequestQueued)
	second := seedRequest(t, e, 1, "example", 2, store.RequestQueued)
	if err := e.st.FinishRequest(context.Background(), second.ID, store.RequestResult{Status: store.RequestSucceeded}); err != nil {
		t.Fatal(err)
	}

	path := "/api/v1/requests/" + strconv.FormatInt(first.ID, 10) + "/cancel"
	resp := e.apiPost(j, path, csrf, "")
	requireAPIStatus(t, resp, path, http.StatusOK)
	got, err := e.st.GetRequest(context.Background(), first.ID)
	if err != nil || got.Status != store.RequestCancelled || got.ErrorCode != "REQUEST_CANCELLED" {
		t.Fatalf("单条取消未落库: %+v err=%v", got, err)
	}
	if !e.containsAction("request.cancel") {
		t.Fatal("取消应写 request.cancel 审计")
	}

	resp = e.apiPost(j, "/api/v1/requests/cancel", csrf, `{"ids":[`+
		strconv.FormatInt(second.ID, 10)+`,999]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批量取消应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	var out struct {
		OK      bool              `json:"ok"`
		Results []apiCancelResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(bodyOf(t, resp)), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || len(out.Results) != 2 {
		t.Fatalf("批量响应不对: %+v", out)
	}
	if out.Results[0].Result != "conflict" || out.Results[1].Result != "not_found" {
		t.Fatalf("批量逐条结果不对: %+v", out.Results)
	}
}

func TestAPIRequestCancelAuthAndCSRF(t *testing.T) {
	e := newTestEnv(t, nil)
	path := "/api/v1/requests/1/cancel"
	resp := e.do(newJar(t), http.MethodPost, path, "application/json", "{}")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未认证取消应 401，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeUnauthorized)

	j := e.login(t)
	resp = e.apiPost(j, path, "wrong-token", "")
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("错误 CSRF 取消应 403，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"), bodyOf(t, resp), apiCodeCSRFFailed)
}
