package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/recovery"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

type fakeRecoveryManager struct {
	calls      []string
	in         recovery.Input
	id         int64
	action     string
	status     string
	page, size int
	err        error
}

func (f *fakeRecoveryManager) Preview(_ context.Context, in recovery.Input) (recovery.Preview, error) {
	f.calls = append(f.calls, "preview")
	f.in = in
	return recovery.Preview{Total: 2, WithCache: 1, WithoutCache: 1, BotID: 7, TargetChatID: -10020}, f.err
}
func (f *fakeRecoveryManager) Create(_ context.Context, in recovery.Input) (store.RecoveryJob, error) {
	f.calls = append(f.calls, "create")
	f.in = in
	return store.RecoveryJob{ID: 5, TargetChatID: -10020, BotID: 7, Status: "running"}, f.err
}
func (f *fakeRecoveryManager) Control(_ context.Context, id int64, action string) (store.RecoveryJob, error) {
	f.calls = append(f.calls, "control")
	f.id, f.action = id, action
	return store.RecoveryJob{ID: id, Status: "paused"}, f.err
}
func (f *fakeRecoveryManager) List(_ context.Context, page, size int) ([]store.RecoveryJob, int, error) {
	f.calls = append(f.calls, "list")
	f.page, f.size = page, size
	return []store.RecoveryJob{{ID: 5, Status: "paused"}}, 21, f.err
}
func (f *fakeRecoveryManager) Get(_ context.Context, id int64) (store.RecoveryJob, error) {
	f.calls = append(f.calls, "get")
	f.id = id
	return store.RecoveryJob{ID: id}, f.err
}
func (f *fakeRecoveryManager) Items(_ context.Context, id int64, status string, page, size int) ([]store.RecoveryItem, int, error) {
	f.calls = append(f.calls, "items")
	f.id, f.status, f.page, f.size = id, status, page, size
	return nil, 0, f.err
}

func newRecoveryEnv(t *testing.T) (*testEnv, *jar, *fakeRecoveryManager, string) {
	t.Helper()
	fake := &fakeRecoveryManager{}
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) { opt.Recovery = fake })
	j := e.login(t)
	return e, j, fake, apiCSRFToken(t, e, j)
}

func TestAPIRecoveryAuthenticationAndCSRF(t *testing.T) {
	e, j, fake, _ := newRecoveryEnv(t)
	for _, path := range []string{"/api/v1/recovery/jobs", "/api/v1/recovery/jobs/5", "/api/v1/recovery/jobs/5/items"} {
		resp := e.do(&jar{}, http.MethodGet, path, "", "")
		requireAPIStatus(t, resp, path, http.StatusUnauthorized)
	}
	for _, path := range []string{"/api/v1/recovery/preview", "/api/v1/recovery/jobs", "/api/v1/recovery/jobs/5/pause", "/api/v1/recovery/jobs/5/resume", "/api/v1/recovery/jobs/5/cancel", "/api/v1/recovery/jobs/5/retry"} {
		requireAPIStatus(t, e.apiPost(&jar{}, path, "", `{}`), path, http.StatusUnauthorized)
		for _, csrf := range []string{"", "wrong"} {
			requireAPIStatus(t, e.apiPost(j, path, csrf, `{}`), path, http.StatusForbidden)
		}
	}
	if len(fake.calls) != 0 {
		t.Fatalf("拒绝请求不得触发恢复服务: %v", fake.calls)
	}
}

func TestAPIRecoveryDependencyGates(t *testing.T) {
	for _, missing := range []string{"recovery", "access"} {
		t.Run(missing, func(t *testing.T) {
			e, j, fake, csrf := newRecoveryEnv(t)
			if missing == "recovery" {
				e.srv.recovery = nil
			} else {
				e.srv.access = nil
			}
			for _, path := range []string{"/api/v1/recovery/preview", "/api/v1/recovery/jobs", "/api/v1/recovery/jobs/5/resume"} {
				requireAPIStatus(t, e.apiPost(j, path, csrf, `{"target":"-10020"}`), path, http.StatusServiceUnavailable)
			}
			if len(fake.calls) != 0 {
				t.Fatalf("依赖缺失不得变更: %v", fake.calls)
			}
		})
	}
}

func TestAPIRecoveryInputValidation(t *testing.T) {
	e, j, fake, csrf := newRecoveryEnv(t)
	for _, path := range []string{"/api/v1/recovery/preview", "/api/v1/recovery/jobs"} {
		for _, body := range []string{
			`{}`, `{"target":" "}`, `{"target":"-10020","bot_id":-1}`,
			`{"target":"-10020","filter":{"user_id":-2}}`,
			`{"target":"-10020","filter":{"since":-1}}`,
			`{"target":"-10020","filter":{"since":100,"until":100}}`,
			`{"target":"-10020","filter":{"since":100,"until":99}}`,
			`{"target":"-10020"} {}`, `{broken`,
		} {
			requireAPIStatus(t, e.apiPost(j, path, csrf, body), path, http.StatusBadRequest)
		}
		requireAPIStatus(t, e.apiPostCT(j, path, csrf, "text/plain", `{"target":"-10020"}`), path, http.StatusBadRequest)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("非法参数不得调用业务服务: %v", fake.calls)
	}
}

func TestAPIRecoveryPreviewAndCreate(t *testing.T) {
	e, j, fake, csrf := newRecoveryEnv(t)
	body := `{"target":" -10020 ","bot_id":7,"filter":{"channel_key":" 123 ","user_id":9,"since":100,"until":200}}`
	resp := e.apiPost(j, "/api/v1/recovery/preview", csrf, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("预检失败: %s", bodyOf(t, resp))
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Error("预检不可缓存")
	}
	var preview recovery.Preview
	decodeAPIJSON(t, bodyOf(t, resp), &preview)
	if preview.Total != 2 || preview.WithCache != 1 || preview.TargetChatID != -10020 {
		t.Fatalf("预检响应: %+v", preview)
	}
	if fake.in.Target != "-10020" || fake.in.Filter.ChannelKey != "123" || fake.in.Filter.UserID != 9 || fake.in.BotID != 7 || fake.in.Filter.Until != 200 {
		t.Fatalf("筛选参数丢失: %+v", fake.in)
	}
	if len(fake.calls) != 1 || fake.calls[0] != "preview" {
		t.Fatal("预检不得创建任务")
	}
	if e.containsAction("recovery.create") {
		t.Fatal("预检不得记录成功创建审计")
	}
	resp = e.apiPost(j, "/api/v1/recovery/jobs", csrf, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建失败: %s", bodyOf(t, resp))
	}
	var job store.RecoveryJob
	decodeAPIJSON(t, bodyOf(t, resp), &job)
	if job.ID != 5 || job.TargetChatID != -10020 || job.Status != "running" {
		t.Fatalf("创建响应: %+v", job)
	}
}

func TestAPIRecoveryListsAndControls(t *testing.T) {
	e, j, fake, csrf := newRecoveryEnv(t)
	var jobs apiListEnvelope[store.RecoveryJob]
	getAPIJSON(t, e, j, "/api/v1/recovery/jobs?page=2&page_size=20", &jobs)
	if jobs.Total != 21 || jobs.TotalPages != 2 || fake.page != 2 || fake.size != 20 || len(jobs.Items) != 1 {
		t.Fatalf("任务分页: %+v", jobs)
	}
	var items apiListEnvelope[store.RecoveryItem]
	getAPIJSON(t, e, j, "/api/v1/recovery/jobs/5/items?status=uncertain&page_size=10", &items)
	if items.Items == nil || fake.id != 5 || fake.status != "uncertain" || fake.size != 10 {
		t.Fatalf("项目分页: %+v", items)
	}
	var job store.RecoveryJob
	getAPIJSON(t, e, j, "/api/v1/recovery/jobs/5", &job)
	if job.ID != 5 {
		t.Fatalf("任务详情: %+v", job)
	}
	for _, action := range []string{"pause", "resume", "cancel", "retry"} {
		path := "/api/v1/recovery/jobs/5/" + action
		requireAPIStatus(t, e.apiPost(j, path, csrf, `{}`), path, http.StatusOK)
		if fake.id != 5 || fake.action != action {
			t.Fatalf("动作路由错误: %+v", fake)
		}
	}
	for _, path := range []string{"/api/v1/recovery/jobs?page=0", "/api/v1/recovery/jobs?page_size=201", "/api/v1/recovery/jobs/x", "/api/v1/recovery/jobs/5/items?status=anything", "/api/v1/recovery/jobs/5/items?page=-1"} {
		requireAPIStatus(t, e.do(j, http.MethodGet, path, "", ""), path, http.StatusBadRequest)
	}
}

func TestAPIRecoveryErrorsDoNotLeakDetails(t *testing.T) {
	e, j, fake, csrf := newRecoveryEnv(t)
	for _, tc := range []struct {
		err  error
		want int
	}{
		{store.ErrNotFound, http.StatusNotFound},
		{apperr.New(apperr.CodeStoreConstraint, "secret-detail"), http.StatusConflict},
		{apperr.New(apperr.CodeBotDisabled, "secret-detail"), http.StatusServiceUnavailable},
		{apperr.New(apperr.CodeSendTargetInvalid, "secret-detail"), http.StatusConflict},
		{apperr.Wrap(apperr.CodeNetworkError, errors.New("secret-detail")), http.StatusServiceUnavailable},
		{errors.New("secret-detail"), http.StatusInternalServerError},
	} {
		fake.err = tc.err
		resp := e.apiPost(j, "/api/v1/recovery/jobs/5/resume", csrf, `{}`)
		body := bodyOf(t, resp)
		if resp.StatusCode != tc.want || strings.Contains(body, "secret-detail") {
			t.Fatalf("错误信封泄漏或状态错误 %d: %s", resp.StatusCode, body)
		}
		var out apiErrorEnvelope
		decodeAPIJSON(t, body, &out)
		if out.Error.Code == "" || out.Error.Message == "" {
			t.Fatalf("错误信封缺字段: %s", body)
		}
	}
}
