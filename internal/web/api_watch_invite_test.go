package web

// /api/v1/watch-invite-requests/* 与 watch-sources 邀请路径的 API 契约测试：
// 认证/CSRF、响应信封、受控错误映射，以及完整 invite hash 绝不下发。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/watch"
)

// fakeWatchManager 是 WatchManager 的测试假实现（仅实现被测方法；
// 未实现方法被调用会以 nil 接口 panic 暴露测试缺口）。
type fakeWatchManager struct {
	WatchManager

	listAll    []watch.SourceView
	listErr    error
	listReq    []store.WatchInviteRequest
	listReqErr error

	addResult watch.AdminAddResult
	addErr    error
	addTarget string
	addEnable bool

	approveID  int64
	approveSrc *store.WatchSource
	approveReq store.WatchInviteRequest
	approveErr error

	rejectID  int64
	rejectReq store.WatchInviteRequest
	rejectErr error

	retryID  int64
	retryReq store.WatchInviteRequest
	retrySrc *store.WatchSource
	retryErr error

	deleteID  int64
	deleteErr error

	eventDeleteIDs []int64
	eventDeleted   int64
	eventDeleteErr error
}

func (f *fakeWatchManager) ListAll(context.Context) ([]watch.SourceView, error) {
	return f.listAll, f.listErr
}

func (f *fakeWatchManager) ListInviteRequests(context.Context) ([]store.WatchInviteRequest, error) {
	return f.listReq, f.listReqErr
}

func (f *fakeWatchManager) AdminAdd(_ context.Context, _ string, target string, enabled bool) (watch.AdminAddResult, error) {
	f.addTarget, f.addEnable = target, enabled
	return f.addResult, f.addErr
}

func (f *fakeWatchManager) ApproveInviteRequest(_ context.Context, _ string, id int64) (store.WatchInviteRequest, *store.WatchSource, error) {
	f.approveID = id
	return f.approveReq, f.approveSrc, f.approveErr
}

func (f *fakeWatchManager) RejectInviteRequest(_ context.Context, _ string, id int64) (store.WatchInviteRequest, error) {
	f.rejectID = id
	return f.rejectReq, f.rejectErr
}

func (f *fakeWatchManager) RetryInviteRequest(_ context.Context, id int64) (store.WatchInviteRequest, *store.WatchSource, error) {
	f.retryID = id
	return f.retryReq, f.retrySrc, f.retryErr
}

func (f *fakeWatchManager) DeleteInviteRequest(_ context.Context, id int64) error {
	f.deleteID = id
	return f.deleteErr
}

func (f *fakeWatchManager) DeleteEvents(_ context.Context, ids []int64) (int64, error) {
	f.eventDeleteIDs = ids
	return f.eventDeleted, f.eventDeleteErr
}

func newWatchEnv(t *testing.T) (*testEnv, *jar, *fakeWatchManager, string) {
	t.Helper()
	fake := &fakeWatchManager{}
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		opt.Watch = fake
	})
	j := e.login(t)
	return e, j, fake, apiCSRFToken(t, e, j)
}

func TestAPIWatchInviteUnauthenticated(t *testing.T) {
	e := newTestEnv(t, nil)
	resp := e.do(&jar{}, http.MethodGet, "/api/v1/watch-sources", "", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未认证应 401，得到 %d", resp.StatusCode)
	}
}

func TestAPIWatchInviteServiceMissing(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	resp := e.do(j, http.MethodGet, "/api/v1/watch-sources", "", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未接入服务应 503，得到 %d", resp.StatusCode)
	}
}

func TestAPIWatchSourcesListIncludesInviteRequests(t *testing.T) {
	e, j, fake, _ := newWatchEnv(t)
	fake.listReq = []store.WatchInviteRequest{{
		ID: 1, UserID: 7, MaskedHash: "AbCd…5678", InviteHash: "AbCdEfGh12345678",
		Status: store.WatchInvitePending, Title: "私有频道",
	}}
	resp := e.do(j, http.MethodGet, "/api/v1/watch-sources", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	body := bodyOf(t, resp)
	if strings.Contains(body, "AbCdEfGh12345678") {
		t.Fatalf("响应不得包含完整 invite_hash: %s", body)
	}
	var got struct {
		Items          []any                      `json:"items"`
		InviteRequests []store.WatchInviteRequest `json:"invite_requests"`
	}
	decodeAPIJSON(t, body, &got)
	if len(got.Items) != 0 || len(got.InviteRequests) != 1 ||
		got.InviteRequests[0].MaskedHash != "AbCd…5678" {
		t.Fatalf("邀请申请列表不符: %+v", got)
	}
}

func TestAPIWatchSourcesAddInviteTarget(t *testing.T) {
	e, j, fake, csrf := newWatchEnv(t)
	fake.addResult = watch.AdminAddResult{InviteRequest: &store.WatchInviteRequest{
		ID: 3, MaskedHash: "AbCd…5678", InviteHash: "AbCdEfGh12345678",
		Status: store.WatchInviteWaitingTelegram,
	}}

	resp := e.apiPost(j, "/api/v1/watch-sources/add", csrf,
		`{"target":"https://t.me/+AbCdEfGh12345678","enabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("邀请添加应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if fake.addTarget != "https://t.me/+AbCdEfGh12345678" || !fake.addEnable {
		t.Fatalf("请求参数未透传: target=%q enabled=%v", fake.addTarget, fake.addEnable)
	}
	body := bodyOf(t, resp)
	if strings.Contains(body, "AbCdEfGh12345678") {
		t.Fatalf("响应不得包含完整 invite_hash: %s", body)
	}
	var got struct {
		OK            bool                      `json:"ok"`
		Source        *store.WatchSource        `json:"source"`
		InviteRequest *store.WatchInviteRequest `json:"invite_request"`
	}
	decodeAPIJSON(t, body, &got)
	if !got.OK || got.Source != nil || got.InviteRequest == nil ||
		got.InviteRequest.Status != store.WatchInviteWaitingTelegram {
		t.Fatalf("响应信封不符: %+v", got)
	}

	// 无 CSRF → 403；空 target → 400
	if resp := e.apiPost(j, "/api/v1/watch-sources/add", "", "{}"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("缺 CSRF 应 403，得到 %d", resp.StatusCode)
	}
	if resp := e.apiPost(j, "/api/v1/watch-sources/add", csrf, `{"target":""}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 target 应 400，得到 %d", resp.StatusCode)
	}
}

func TestAPIWatchSourcesAddMTProtoOffline(t *testing.T) {
	e, j, fake, csrf := newWatchEnv(t)
	fake.addErr = mtproto.ErrMembershipUnavailable
	resp := e.apiPost(j, "/api/v1/watch-sources/add", csrf, `{"target":"https://t.me/+AbCd"}`)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("读取账号离线应 503，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	var got struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &got)
	if got.Error.Code != apiCodeMTProtoOffline {
		t.Fatalf("应返回 MTPROTO_OFFLINE: %+v", got.Error)
	}
}

func TestAPIWatchInviteApprove(t *testing.T) {
	e, j, fake, csrf := newWatchEnv(t)
	fake.approveReq = store.WatchInviteRequest{ID: 5, Status: store.WatchInviteApproved}
	fake.approveSrc = &store.WatchSource{ChannelID: -1001234567890, Title: "私有频道"}

	resp := e.apiPost(j, "/api/v1/watch-invite-requests/5/approve", csrf, "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("审批应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if fake.approveID != 5 {
		t.Fatalf("申请 ID 未透传: %d", fake.approveID)
	}
	var got struct {
		InviteRequest store.WatchInviteRequest `json:"invite_request"`
		Source        *store.WatchSource       `json:"source"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &got)
	if got.InviteRequest.Status != store.WatchInviteApproved || got.Source == nil {
		t.Fatalf("审批响应应含申请与源: %+v", got)
	}

	// 已处理 → 409
	fake.approveErr = apperr.New(apperr.CodeStoreConstraint, "已处理")
	if resp := e.apiPost(j, "/api/v1/watch-invite-requests/5/approve", csrf, "{}"); resp.StatusCode != http.StatusConflict {
		t.Fatalf("重复审批应 409，得到 %d", resp.StatusCode)
	}
	// 不存在 → 400
	fake.approveErr = store.ErrNotFound
	if resp := e.apiPost(j, "/api/v1/watch-invite-requests/5/approve", csrf, "{}"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("不存在应 400，得到 %d", resp.StatusCode)
	}
}

func TestAPIWatchInviteRejectRetryDelete(t *testing.T) {
	e, j, fake, csrf := newWatchEnv(t)
	fake.rejectReq = store.WatchInviteRequest{ID: 6, Status: store.WatchInviteRejected}
	if resp := e.apiPost(j, "/api/v1/watch-invite-requests/6/reject", csrf, "{}"); resp.StatusCode != http.StatusOK {
		t.Fatalf("拒绝应 200，得到 %d", resp.StatusCode)
	}
	if fake.rejectID != 6 {
		t.Fatalf("拒绝 ID 未透传: %d", fake.rejectID)
	}

	fake.retryReq = store.WatchInviteRequest{ID: 6, Status: store.WatchInviteWaitingBot}
	if resp := e.apiPost(j, "/api/v1/watch-invite-requests/6/retry", csrf, "{}"); resp.StatusCode != http.StatusOK {
		t.Fatalf("重试应 200，得到 %d", resp.StatusCode)
	}
	if fake.retryID != 6 {
		t.Fatalf("重试 ID 未透传: %d", fake.retryID)
	}

	// 重试遇读取账号离线 → 503 受控
	fake.retryErr = mtproto.ErrMembershipUnavailable
	if resp := e.apiPost(j, "/api/v1/watch-invite-requests/6/retry", csrf, "{}"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("离线重试应 503，得到 %d", resp.StatusCode)
	}
	fake.retryErr = nil

	if resp := e.apiPost(j, "/api/v1/watch-invite-requests/6/delete", csrf, "{}"); resp.StatusCode != http.StatusOK {
		t.Fatalf("删除应 200，得到 %d", resp.StatusCode)
	}
	if fake.deleteID != 6 {
		t.Fatalf("删除 ID 未透传: %d", fake.deleteID)
	}
	// 删除不存在 → 400
	fake.deleteErr = errors.Join(store.ErrNotFound)
	if resp := e.apiPost(j, "/api/v1/watch-invite-requests/6/delete", csrf, "{}"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("删除不存在应 400，得到 %d", resp.StatusCode)
	}
}

func TestAPIWatchEventsDelete(t *testing.T) {
	e, j, fake, csrf := newWatchEnv(t)
	fake.eventDeleted = 2

	resp := e.apiPost(j, "/api/v1/watch-events/delete", csrf, `{"ids":[3,7]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("删除应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if len(fake.eventDeleteIDs) != 2 || fake.eventDeleteIDs[0] != 3 {
		t.Fatalf("事件 ID 未透传: %v", fake.eventDeleteIDs)
	}
	var got struct {
		OK      bool  `json:"ok"`
		Deleted int64 `json:"deleted"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &got)
	if !got.OK || got.Deleted != 2 {
		t.Fatalf("响应信封不符: %+v", got)
	}

	// 空 / 超限 / 非法 ID → 400；缺 CSRF → 403
	if resp := e.apiPost(j, "/api/v1/watch-events/delete", csrf, `{"ids":[]}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 ids 应 400，得到 %d", resp.StatusCode)
	}
	long := `{"ids":[` + strings.TrimSuffix(strings.Repeat("1,", 101), ",") + `]}`
	if resp := e.apiPost(j, "/api/v1/watch-events/delete", csrf, long); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("超 100 个 ids 应 400，得到 %d", resp.StatusCode)
	}
	if resp := e.apiPost(j, "/api/v1/watch-events/delete", csrf, `{"ids":[-1]}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("负数 ID 应 400，得到 %d", resp.StatusCode)
	}
	if resp := e.apiPost(j, "/api/v1/watch-events/delete", "", `{"ids":[3]}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("缺 CSRF 应 403，得到 %d", resp.StatusCode)
	}
}
