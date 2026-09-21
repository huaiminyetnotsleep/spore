package web

// 自动置顶（v20）的 Web 层测试：请求行/详情下发 pin、pin_ok、pin_total；
// pin=1 筛选（其余取值忽略）；用户 auto_pin 开关端点。

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func TestAPIRequestsPinFieldsAndFilter(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	seedUser(t, e, 801, store.UserEnabled)
	plain, err := e.st.CreateRequest(context.Background(), store.Request{
		UserID: 801, SourceKind: store.SourcePublic, ChannelKey: "alpha", MessageID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	pinRow, err := e.st.CreateRequest(context.Background(), store.Request{
		UserID: 801, SourceKind: store.SourcePublic, ChannelKey: "alpha", MessageID: 2, Pin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetRequestPinResult(context.Background(), pinRow.ID, 2, 3); err != nil {
		t.Fatal(err)
	}

	// 列表行携带 pin 三字段
	var list apiListEnvelope[apiRequestRow]
	getAPIJSON(t, e, j, "/api/v1/requests", &list)
	byID := map[int64]apiRequestRow{}
	for _, row := range list.Items {
		byID[row.ID] = row
	}
	if row := byID[plain.ID]; row.Pin {
		t.Errorf("普通行不应标记置顶: %+v", row)
	}
	row := byID[pinRow.ID]
	if !row.Pin || row.PinOK != 2 || row.PinTotal != 3 {
		t.Errorf("置顶行字段不符: %+v", row)
	}

	// pin=1 只返回置顶行；其他取值忽略（等价全部）
	var filtered apiListEnvelope[apiRequestRow]
	getAPIJSON(t, e, j, "/api/v1/requests?pin=1", &filtered)
	if len(filtered.Items) != 1 || filtered.Items[0].ID != pinRow.ID {
		t.Fatalf("pin=1 应只返回置顶行: %+v", filtered.Items)
	}
	getAPIJSON(t, e, j, "/api/v1/requests?pin=0", &list)
	if len(list.Items) != 2 {
		t.Fatalf("pin=0 应忽略该筛选返回全部: %d", len(list.Items))
	}

	// 详情同样携带置顶字段
	var detail apiRequestDetail
	getAPIJSON(t, e, j, fmt.Sprintf("/api/v1/requests/%d", pinRow.ID), &detail)
	if !detail.Pin || detail.PinOK != 2 || detail.PinTotal != 3 {
		t.Fatalf("详情置顶字段不符: %+v", detail)
	}
}

func TestAPIUserSetAutoPin(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	csrf := apiCSRFToken(t, e, j)

	if _, err := e.st.CreateUser(context.Background(), store.User{ID: 44, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}

	// 默认关闭
	var detail apiUserDetail
	getAPIJSON(t, e, j, "/api/v1/users/44", &detail)
	if detail.AutoPin {
		t.Fatalf("auto_pin 应默认关闭: %+v", detail)
	}

	// 开启：200 + 回显 true；列表行与详情同步
	resp := e.apiPost(j, "/api/v1/users/44/auto-pin", csrf, `{"auto_pin":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("开启应 200，得到 %d（body=%s）", resp.StatusCode, bodyOf(t, resp))
	}
	getAPIJSON(t, e, j, "/api/v1/users/44", &detail)
	if !detail.AutoPin {
		t.Fatalf("开启后详情应回显 auto_pin: %+v", detail)
	}
	var users apiListEnvelope[apiUserRow]
	getAPIJSON(t, e, j, "/api/v1/users", &users)
	for _, row := range users.Items {
		if row.ID == 44 && !row.AutoPin {
			t.Fatalf("开启后列表行应回显 auto_pin: %+v", row)
		}
	}

	// 非布尔取值 → 400
	if resp := e.apiPost(j, "/api/v1/users/44/auto-pin", csrf, `{"auto_pin":"yes"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非布尔 auto_pin 应 400，得到 %d", resp.StatusCode)
	}
}
