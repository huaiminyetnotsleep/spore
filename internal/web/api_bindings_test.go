package web

import (
	"context"
	"net/http"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"

	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// fakeChannelBinder 是 ChannelBinder 的测试假实现：记录绑定入参、按配置返回。
type fakeChannelBinder struct {
	bindIn   binding.BindInput
	bindErr  error
	bindOut  store.ChannelBinding
	unbindID string
	unbindIn struct {
		userID   int64
		target   string
		anyOwner bool
	}
	unbindErr error
	unbindOut store.ChannelBinding
	// DeleteBinding（物理删除）的可编程失败
	deleteErr error
	listRows  []store.ChannelBindingWithUser
	// VerifyChannel（缓存频道配置校验）的可编程返回
	verifyTarget string
	verifyErr    error
	verifyID     int64
	verifyTitle  string
}

func (f *fakeChannelBinder) Bind(_ context.Context, in binding.BindInput) (store.ChannelBinding, error) {
	f.bindIn = in
	return f.bindOut, f.bindErr
}

func (f *fakeChannelBinder) Unbind(_ context.Context, userID int64, target string, anyOwner bool) (store.ChannelBinding, error) {
	f.unbindIn.userID, f.unbindIn.target, f.unbindIn.anyOwner = userID, target, anyOwner
	return f.unbindOut, f.unbindErr
}

func (f *fakeChannelBinder) DeleteBinding(_ context.Context, channelID int64, _ string) (store.ChannelBinding, error) {
	if f.deleteErr != nil {
		return store.ChannelBinding{}, f.deleteErr
	}
	return store.ChannelBinding{ChannelID: channelID, UserID: 1, Status: store.BindingStatusActive}, nil
}

func (f *fakeChannelBinder) ListAll(context.Context) ([]store.ChannelBindingWithUser, error) {
	return f.listRows, nil
}

func (f *fakeChannelBinder) ListAllByUser(_ context.Context, userID int64) ([]store.ChannelBindingWithUser, error) {
	rows := make([]store.ChannelBindingWithUser, 0)
	for _, row := range f.listRows {
		if row.UserID == userID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (f *fakeChannelBinder) VerifyChannel(_ context.Context, target string) (int64, string, error) {
	f.verifyTarget = target
	if f.verifyErr != nil {
		return 0, "", f.verifyErr
	}
	return f.verifyID, f.verifyTitle, nil
}

// newBindingsEnv 构造注入假绑定服务的测试环境并完成登录，返回会话与假实现。
func newBindingsEnv(t *testing.T) (*testEnv, *jar, *fakeChannelBinder, string) {
	t.Helper()
	fake := &fakeChannelBinder{}
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		opt.Bindings = fake
	})
	j := e.login(t)
	return e, j, fake, apiCSRFToken(t, e, j)
}

func TestAPIChannelBindingsUnauthenticated(t *testing.T) {
	e := newTestEnv(t, nil)
	resp := e.do(&jar{}, http.MethodGet, "/api/v1/channel-bindings", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未认证应 401，得到 %d", resp.StatusCode)
	}
}

func TestAPIChannelBindingsList(t *testing.T) {
	e, j, fake, _ := newBindingsEnv(t)
	fake.listRows = []store.ChannelBindingWithUser{{
		ChannelBinding: store.ChannelBinding{ChannelID: -1001234567890, UserID: 7,
			Username: "mychan", Title: "我的频道", BoundVia: "bot",
			CreatedAt: 1757030400000, UpdatedAt: 1757030400000},
		UserUsername:    "alice",
		UserDisplayName: "Alice",
	}}

	resp := e.do(j, http.MethodGet, "/api/v1/channel-bindings", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表应 200，得到 %d", resp.StatusCode)
	}
	var got struct {
		Items []apiChannelBindingRow `json:"items"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &got)
	if len(got.Items) != 1 {
		t.Fatalf("应有 1 条绑定，得到 %d", len(got.Items))
	}
	row := got.Items[0]
	if row.ChannelID != -1001234567890 || row.UserID != 7 || row.UserUsername != "alice" ||
		row.BoundVia != "bot" || row.CreatedAt != 1757030400000 {
		t.Fatalf("列表行不符: %+v", row)
	}
}

func TestAPIChannelBindingsListByUser(t *testing.T) {
	e, j, fake, _ := newBindingsEnv(t)
	fake.listRows = []store.ChannelBindingWithUser{
		{ChannelBinding: store.ChannelBinding{ChannelID: -1001, UserID: 7}},
		{ChannelBinding: store.ChannelBinding{ChannelID: -1002, UserID: 8}},
	}
	var got struct {
		Items []apiChannelBindingRow `json:"items"`
	}
	getAPIJSON(t, e, j, "/api/v1/channel-bindings?user_id=7", &got)
	if len(got.Items) != 1 || got.Items[0].UserID != 7 {
		t.Fatalf("按用户筛选不对: %+v", got.Items)
	}
	resp := e.do(j, http.MethodGet, "/api/v1/channel-bindings?user_id=abc", "", "")
	requireBadRequest(t, resp, "/api/v1/channel-bindings?user_id=abc")
}

func TestAPIChannelBindingAddValidation(t *testing.T) {
	e, j, _, csrf := newBindingsEnv(t)

	cases := []struct {
		name string
		body string
		want int
	}{
		{"缺 user_id", `{"target":"@mychan"}`, http.StatusBadRequest},
		{"缺 target", `{"user_id":7}`, http.StatusBadRequest},
		{"用户不存在", `{"user_id":999,"target":"@mychan"}`, http.StatusBadRequest},
	}
	for _, c := range cases {
		resp := e.apiPost(j, "/api/v1/channel-bindings", csrf, c.body)
		if resp.StatusCode != c.want {
			t.Errorf("%s 应 %d，得到 %d", c.name, c.want, resp.StatusCode)
		}
	}
}

func TestAPIChannelBindingAddSuccess(t *testing.T) {
	e, j, fake, csrf := newBindingsEnv(t)
	if _, err := e.st.CreateUser(context.Background(), store.User{ID: 7, Status: store.UserEnabled}); err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	fake.bindOut = store.ChannelBinding{ChannelID: -1001234567890, UserID: 7,
		Username: "mychan", Title: "我的频道", BoundVia: "web", CreatedAt: 1757030400000}

	resp := e.apiPost(j, "/api/v1/channel-bindings", csrf, `{"user_id":7,"target":"@mychan"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("绑定应成功，得到 %d", resp.StatusCode)
	}
	if fake.bindIn.UserID != 7 || fake.bindIn.Target != "@mychan" || fake.bindIn.Via != "web" {
		t.Fatalf("服务入参不符: %+v", fake.bindIn)
	}
	var got struct {
		OK      bool                 `json:"ok"`
		Binding apiChannelBindingRow `json:"binding"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &got)
	if !got.OK || got.Binding.ChannelID != -1001234567890 || got.Binding.Title != "我的频道" {
		t.Fatalf("响应不符: %+v", got)
	}
}

func TestAPIChannelBindingDelete(t *testing.T) {
	t.Run("成功（负数频道 ID）", func(t *testing.T) {
		e, j, fake, csrf := newBindingsEnv(t)
		fake.unbindOut = store.ChannelBinding{ChannelID: -1001234567890, UserID: 7, Title: "我的频道"}

		resp := e.apiPost(j, "/api/v1/channel-bindings/-1001234567890/delete", csrf, "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("解绑应成功，得到 %d", resp.StatusCode)
		}
		if !fake.unbindIn.anyOwner || fake.unbindIn.userID != 0 || fake.unbindIn.target != "-1001234567890" {
			t.Fatalf("解绑应以管理语义调用服务: %+v", fake.unbindIn)
		}
	})

	t.Run("不存在 → 404", func(t *testing.T) {
		e, j, fake, csrf := newBindingsEnv(t)
		fake.unbindErr = store.ErrNotFound
		resp := e.apiPost(j, "/api/v1/channel-bindings/-1001234567890/delete", csrf, "")
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("不存在应 404，得到 %d", resp.StatusCode)
		}
	})
}
