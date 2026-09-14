package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/joinmgr"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
)

// fakeChannelJoin 是 ChannelJoinManager 的测试假实现。
type fakeChannelJoin struct {
	listQuery joinmgr.RequestsQuery
	listRows  []joinmgr.JoinRequestView
	listTotal int
	listErr   error

	approveID   int64
	approveErr  error
	approveView joinmgr.JoinRequestView
	rejectID    int64
	rejectErr   error
	rejectView  joinmgr.JoinRequestView

	enforceErr error
	joinedRows []joinmgr.JoinedChannelView
	joinedErr  error

	deleteIDs []int64
	deleteOut []joinmgr.RequestDeleteOutcome

	leaveIDs []int64
	leaveOut []joinmgr.LeaveOutcome
}

func (f *fakeChannelJoin) ListRequests(_ context.Context, q joinmgr.RequestsQuery) ([]joinmgr.JoinRequestView, int, error) {
	f.listQuery = q
	return f.listRows, f.listTotal, f.listErr
}

func (f *fakeChannelJoin) Approve(_ context.Context, actor string, id int64) (joinmgr.JoinRequestView, error) {
	f.approveID = id
	return f.approveView, f.approveErr
}

func (f *fakeChannelJoin) Reject(_ context.Context, actor string, id int64) (joinmgr.JoinRequestView, error) {
	f.rejectID = id
	return f.rejectView, f.rejectErr
}

func (f *fakeChannelJoin) DeleteRequests(_ context.Context, ids []int64) []joinmgr.RequestDeleteOutcome {
	f.deleteIDs = ids
	if f.deleteOut != nil {
		return f.deleteOut
	}
	out := make([]joinmgr.RequestDeleteOutcome, 0, len(ids))
	for _, id := range ids {
		out = append(out, joinmgr.RequestDeleteOutcome{ID: id, OK: true})
	}
	return out
}

func (f *fakeChannelJoin) ReconcilePendingJoins(context.Context) error { return f.enforceErr }

func (f *fakeChannelJoin) ListJoined(context.Context) ([]joinmgr.JoinedChannelView, error) {
	return f.joinedRows, f.joinedErr
}

func (f *fakeChannelJoin) Leave(_ context.Context, ids []int64) []joinmgr.LeaveOutcome {
	f.leaveIDs = ids
	if f.leaveOut != nil {
		return f.leaveOut
	}
	out := make([]joinmgr.LeaveOutcome, 0, len(ids))
	for _, id := range ids {
		out = append(out, joinmgr.LeaveOutcome{ChannelID: id, OK: true})
	}
	return out
}

func (f *fakeChannelJoin) Enforce(context.Context) error { return f.enforceErr }

// newChannelJoinEnv 构造注入假服务的测试环境并完成登录。
func newChannelJoinEnv(t *testing.T) (*testEnv, *jar, *fakeChannelJoin, string) {
	t.Helper()
	fake := &fakeChannelJoin{}
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) {
		opt.ChannelJoin = fake
	})
	j := e.login(t)
	return e, j, fake, apiCSRFToken(t, e, j)
}

func TestAPIChannelJoinUnauthenticated(t *testing.T) {
	e := newTestEnv(t, nil)
	for _, path := range []string{"/api/v1/channel-join/requests", "/api/v1/channel-join/channels"} {
		resp := e.do(&jar{}, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s 未认证应 401，得到 %d", path, resp.StatusCode)
		}
	}
}

func TestAPIChannelJoinServiceMissing(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	resp := e.do(j, http.MethodGet, "/api/v1/channel-join/requests", "", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未接入服务应 503，得到 %d", resp.StatusCode)
	}
}

func TestAPIJoinRequestsList(t *testing.T) {
	e, j, fake, _ := newChannelJoinEnv(t)
	fake.listRows = []joinmgr.JoinRequestView{{
		ID: 1, UserID: 100, ChannelTitle: "私有频道", Status: "pending",
		MaskedHash: "AbCd…5678",
	}}
	fake.listTotal = 7

	resp := e.do(j, http.MethodGet,
		"/api/v1/channel-join/requests?status=pending&user_id=100&keyword=%E9%A2%91%E9%81%93&since=2026-09-01&until=2026-09-08&page=2&page_size=10",
		"", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	q := fake.listQuery
	if q.Status != "pending" || q.UserID != 100 || q.TitleKeyword != "频道" ||
		q.Page != 2 || q.PageSize != 10 {
		t.Fatalf("筛选参数未正确传递: %+v", q)
	}
	if q.Since == 0 || q.Until == 0 || q.Until <= q.Since {
		t.Fatalf("时间范围未正确传递: %d %d", q.Since, q.Until)
	}
	body := bodyOf(t, resp)
	if strings.Contains(body, "AbCdEfGh12345678") {
		t.Fatalf("响应不应包含完整 invite_hash")
	}
	var got struct {
		Items []joinmgr.JoinRequestView `json:"items"`
		Total int                       `json:"total"`
		Page  int                       `json:"page"`
	}
	decodeAPIJSON(t, body, &got)
	if len(got.Items) != 1 || got.Items[0].MaskedHash != "AbCd…5678" || got.Total != 7 || got.Page != 2 {
		t.Fatalf("分页信封不符: %+v", got)
	}

	// 非法参数 → 400
	for name, path := range map[string]string{
		"非法page":    "/api/v1/channel-join/requests?page=0",
		"非法user_id": "/api/v1/channel-join/requests?user_id=abc",
		"非法日期":      "/api/v1/channel-join/requests?since=20260901",
	} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s 应 400，得到 %d", name, resp.StatusCode)
		}
	}
}

func TestAPIJoinRequestReview(t *testing.T) {
	e, j, fake, csrf := newChannelJoinEnv(t)
	fake.approveView = joinmgr.JoinRequestView{ID: 3, Status: "approved"}

	resp := e.apiPost(j, "/api/v1/channel-join/requests/3/approve", csrf, "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("同意应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if fake.approveID != 3 {
		t.Fatalf("申请 ID 未传递: %d", fake.approveID)
	}

	// 拒绝（写操作后 token 轮换，重新获取）
	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/channel-join/requests/5/reject", csrf, "{}")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("拒绝应 200，得到 %d", resp.StatusCode)
	}
	if fake.rejectID != 5 {
		t.Fatalf("拒绝 ID 未传递: %d", fake.rejectID)
	}

	// 非法 ID
	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/channel-join/requests/abc/approve", csrf, "{}")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 ID 应 400，得到 %d", resp.StatusCode)
	}
}

func TestAPIJoinRequestReviewErrors(t *testing.T) {
	e, j, fake, csrf := newChannelJoinEnv(t)
	// 已处理 → 409（STORE_CONSTRAINT）
	fake.approveErr = apperr.New(apperr.CodeStoreConstraint, "该申请已被处理")
	resp := e.apiPost(j, "/api/v1/channel-join/requests/1/approve", csrf, "{}")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("重复审批应 409，得到 %d", resp.StatusCode)
	}
	// MTProto 离线 → 503
	csrf = apiCSRFToken(t, e, j)
	fake.approveErr = mtproto.ErrMembershipUnavailable
	resp = e.apiPost(j, "/api/v1/channel-join/requests/1/approve", csrf, "{}")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("离线应 503，得到 %d", resp.StatusCode)
	}
}

func TestAPIJoinRequestsDelete(t *testing.T) {
	e, j, fake, csrf := newChannelJoinEnv(t)

	payload, _ := json.Marshal(map[string]any{"ids": []int64{1, 2, 3}})
	resp := e.apiPost(j, "/api/v1/channel-join/requests/delete", csrf, string(payload))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批量删除应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if len(fake.deleteIDs) != 3 {
		t.Fatalf("删除 ID 未传递: %v", fake.deleteIDs)
	}
	var got struct {
		OK       bool                           `json:"ok"`
		Outcomes []joinmgr.RequestDeleteOutcome `json:"outcomes"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &got)
	if !got.OK || len(got.Outcomes) != 3 {
		t.Fatalf("删除结果不符: %+v", got)
	}

	// 全部失败（如全是 pending）→ ok=false 但仍 200 逐条反馈
	csrf = apiCSRFToken(t, e, j)
	fake.deleteOut = []joinmgr.RequestDeleteOutcome{{ID: 9, OK: false, Error: "待审批记录不可删除，请先同意或拒绝"}}
	resp = e.apiPost(j, "/api/v1/channel-join/requests/delete", csrf, `{"ids":[9]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("部分失败也应 200，得到 %d", resp.StatusCode)
	}
	decodeAPIJSON(t, bodyOf(t, resp), &got)
	if got.OK || len(got.Outcomes) != 1 || got.Outcomes[0].OK {
		t.Fatalf("全失败结果不符: %+v", got)
	}

	// 空列表 → 400
	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/channel-join/requests/delete", csrf, `{"ids":[]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空列表应 400，得到 %d", resp.StatusCode)
	}
}

func TestAPIJoinedChannelsList(t *testing.T) {
	e, j, fake, _ := newChannelJoinEnv(t)
	fake.joinedRows = []joinmgr.JoinedChannelView{{
		ChannelID: 42, Title: "私有频道", Kind: "channel", Source: "approved",
	}}

	resp := e.do(j, http.MethodGet, "/api/v1/channel-join/channels", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表应 200，得到 %d", resp.StatusCode)
	}
	var got struct {
		Items []joinmgr.JoinedChannelView `json:"items"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &got)
	if len(got.Items) != 1 || got.Items[0].ChannelID != 42 {
		t.Fatalf("列表数据不符: %+v", got.Items)
	}

	// MTProto 离线 → 503（Enforce 失败继续，列表错误统一反馈）
	fake.joinedErr = mtproto.ErrMembershipUnavailable
	resp = e.do(j, http.MethodGet, "/api/v1/channel-join/channels", "", "")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("离线应 503，得到 %d", resp.StatusCode)
	}
}

func TestAPIJoinedChannelsLeave(t *testing.T) {
	e, j, fake, csrf := newChannelJoinEnv(t)

	payload, _ := json.Marshal(map[string]any{"channel_ids": []int64{42, 43}})
	resp := e.apiPost(j, "/api/v1/channel-join/channels/leave", csrf, string(payload))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("批量退出应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	if len(fake.leaveIDs) != 2 || fake.leaveIDs[0] != 42 || fake.leaveIDs[1] != 43 {
		t.Fatalf("退出 ID 未传递: %v", fake.leaveIDs)
	}

	// 空 ID 列表 → 400（写操作后 token 轮换）
	csrf = apiCSRFToken(t, e, j)
	resp = e.apiPost(j, "/api/v1/channel-join/channels/leave", csrf, `{"channel_ids":[]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空列表应 400，得到 %d", resp.StatusCode)
	}
}

func TestAPISettingsJoinConfig(t *testing.T) {
	e, j, _, csrf := newChannelJoinEnv(t)

	// 写入配置
	payload, _ := json.Marshal(map[string]any{
		"join_enabled": true, "join_auto_leave_external": true, "join_require_approval": false,
		"join_max_channels": 30, "join_mute_enabled": false, "join_archive_enabled": true,
	})
	resp := e.apiPost(j, "/api/v1/settings", csrf, string(payload))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存应 200，得到 %d: %s", resp.StatusCode, bodyOf(t, resp))
	}
	var saved struct {
		Settings apiSettingsView `json:"settings"`
	}
	decodeAPIJSON(t, bodyOf(t, resp), &saved)
	if !saved.Settings.JoinEnabled || !saved.Settings.JoinAutoLeaveExternal ||
		saved.Settings.JoinRequireApproval ||
		saved.Settings.JoinMaxChannels != 30 || saved.Settings.JoinMuteEnabled {
		t.Fatalf("保存后回读不符: %+v", saved.Settings)
	}

	// 回读
	resp = e.do(j, http.MethodGet, "/api/v1/settings", "", "")
	decodeAPIJSON(t, bodyOf(t, resp), &saved)
	if !saved.Settings.JoinEnabled || saved.Settings.JoinMaxChannels != 30 {
		t.Fatalf("回读不符: %+v", saved.Settings)
	}

	// 非法上限 → 400（写操作后 token 轮换）
	csrf = apiCSRFToken(t, e, j)
	bad, _ := json.Marshal(map[string]any{"join_max_channels": 999})
	resp = e.apiPost(j, "/api/v1/settings", csrf, string(bad))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法上限应 400，得到 %d", resp.StatusCode)
	}
}
