package web

// 只读页面迁移的 API 契约测试：
// 每个新端点覆盖未认证 401 JSON、正常分页/筛选、参数非法 400 JSON 与
// ErrNotFound 404 JSON；SSR 页面行为不回归由 pages_test.go 既有断言保障。

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// seedPrivateRequest 建一条私有频道请求（requestLink 私有规则用）。
func seedPrivateRequest(t *testing.T, e *testEnv, userID int64, channelKey string, msgID int) store.Request {
	t.Helper()
	r, err := e.st.CreateRequest(context.Background(), store.Request{
		UserID: userID, SourceKind: store.SourcePrivate, ChannelKey: channelKey, MessageID: msgID,
	})
	if err != nil {
		t.Fatalf("创建私有请求失败: %v", err)
	}
	return r
}

// getAPIJSON 以已登录会话请求 API、解码 JSON 响应并返回响应体文本
// （供调用方追加断言；响应体已被读取关闭，响应头另行经 e.do 检查）。
func getAPIJSON(t *testing.T, e *testEnv, j *jar, path string, v any) string {
	t.Helper()
	resp := e.do(j, http.MethodGet, path, "", "")
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("GET %s 应为 JSON，得到 Content-Type %q", path, ct)
	}
	body := bodyOf(t, resp)
	decodeAPIJSON(t, body, v)
	return body
}

// requireBadRequest 断言响应是 400 JSON BAD_REQUEST。
func requireBadRequest(t *testing.T, resp *http.Response, path string) {
	t.Helper()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET %s 应返回 400，得到 %d", path, resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeBadRequest)
}

// ---- 未认证：全部新端点 401 JSON ----

func TestAPIPagesUnauthenticatedJSON401(t *testing.T) {
	e := newTestEnv(t, nil)
	j := newJar(t)

	for _, path := range []string{
		"/api/v1/users", "/api/v1/users/1",
		"/api/v1/requests", "/api/v1/requests/1",
		"/api/v1/channels", "/api/v1/channels/alpha",
		"/api/v1/events", "/api/v1/audit",
	} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("未认证 GET %s 应返回 401，得到 %d", path, resp.StatusCode)
		}
		requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
			bodyOf(t, resp), apiCodeUnauthorized)
	}
}

// ---- 用户列表 ----

func TestAPIUsersListPagingAndFilter(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	seedUser(t, e, 101, store.UserEnabled)
	seedUser(t, e, 102, store.UserPending)
	seedUser(t, e, 103, store.UserEnabled)

	// 分页信封与页大小上限内切片
	var page1 apiListEnvelope[apiUserRow]
	getAPIJSON(t, e, j, "/api/v1/users?page=1&page_size=2", &page1)
	if resp := e.do(j, http.MethodGet, "/api/v1/users", "", ""); resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("列表响应应禁止缓存，得到 Cache-Control %q", resp.Header.Get("Cache-Control"))
	} else {
		bodyOf(t, resp)
	}
	if page1.Total != 3 || page1.TotalPages != 2 || page1.Page != 1 || page1.PageSize != 2 {
		t.Errorf("分页信封不对: %+v", page1)
	}
	if len(page1.Items) != 2 {
		t.Fatalf("第一页应有 2 行，得到 %d", len(page1.Items))
	}
	var page2 apiListEnvelope[apiUserRow]
	getAPIJSON(t, e, j, "/api/v1/users?page=2&page_size=2", &page2)
	if len(page2.Items) != 1 || page2.Items[0].ID != 103 {
		t.Errorf("第二页应只剩用户 103: %+v", page2.Items)
	}

	// 状态筛选
	var enabled apiListEnvelope[apiUserRow]
	getAPIJSON(t, e, j, "/api/v1/users?status="+store.UserEnabled, &enabled)
	if enabled.Total != 2 {
		t.Errorf("enabled 筛选应命中 2 人，得到 %d", enabled.Total)
	}

	// 搜索（ID 精确、用户名子串不区分大小写）
	var byID apiListEnvelope[apiUserRow]
	getAPIJSON(t, e, j, "/api/v1/users?q=102", &byID)
	if byID.Total != 1 || byID.Items[0].ID != 102 {
		t.Errorf("按 ID 搜索应命中用户 102: %+v", byID.Items)
	}
	var byName apiListEnvelope[apiUserRow]
	getAPIJSON(t, e, j, "/api/v1/users?q=U101", &byName)
	if byName.Total != 1 || byName.Items[0].ID != 101 {
		t.Errorf("按用户名搜索应不区分大小写命中 101: %+v", byName.Items)
	}

	// 空结果：items 归一为 []（非 null）
	var empty apiListEnvelope[apiUserRow]
	body := getAPIJSON(t, e, j, "/api/v1/users?q=no-such-user", &empty)
	var raw map[string]json.RawMessage
	decodeAPIJSON(t, body, &raw)
	if string(raw["items"]) != "[]" {
		t.Errorf("空列表 items 应为 []，得到 %s", raw["items"])
	}
}

func TestAPIUsersListBadParams(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	for _, q := range []string{"page=0", "page=-1", "page=abc", "page=1099511627777", "page_size=0", "page_size=201"} {
		resp := e.do(j, http.MethodGet, "/api/v1/users?"+q, "", "")
		requireBadRequest(t, resp, "/api/v1/users?"+q)
	}
}

// ---- 用户详情 ----

func TestAPIUserDetail(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	u := seedUser(t, e, 201, store.UserEnabled)
	day := e.srv.now().In(e.srv.tz(context.Background())).Format(dayInputFormat)
	if err := e.st.IncrementUsage(context.Background(), u.ID, day, 7); err != nil {
		t.Fatalf("累加用量失败: %v", err)
	}
	seedRequest(t, e, u.ID, "alpha", 1, store.RequestSucceeded)
	seedRequest(t, e, u.ID, "alpha", 2, store.RequestFailed)

	var detail apiUserDetail
	getAPIJSON(t, e, j, "/api/v1/users/201", &detail)
	if detail.ID != 201 || detail.Status != store.UserEnabled || detail.Username != "u201" {
		t.Errorf("详情基础字段不对: %+v", detail)
	}
	if detail.UsedToday != 7 || detail.DailyLimit != u.DailyLimit || detail.RemainingToday != u.DailyLimit-7 {
		t.Errorf("当日用量不对: used=%d limit=%d remaining=%d",
			detail.UsedToday, detail.DailyLimit, detail.RemainingToday)
	}
	if detail.TotalRequests != 2 {
		t.Errorf("累计请求数应为 2，得到 %d", detail.TotalRequests)
	}
	if detail.SubmitIntervalSec <= 0 || detail.ConcurrentLimit <= 0 {
		t.Errorf("限额字段应为正: %+v", detail)
	}

	// 最近被拒文案：写入拒绝原因后返回码原文 + 受控中文
	if err := e.st.MarkUserDenied(context.Background(), u.ID, string(apperr.CodeQuotaExceeded), 0); err != nil {
		t.Fatalf("记录拒绝失败: %v", err)
	}
	getAPIJSON(t, e, j, "/api/v1/users/201", &detail)
	if detail.LastDeniedReason != string(apperr.CodeQuotaExceeded) {
		t.Errorf("拒绝原因原文应为 %s，得到 %q", apperr.CodeQuotaExceeded, detail.LastDeniedReason)
	}
	if !strings.Contains(detail.LastDeniedText, string(apperr.CodeQuotaExceeded)) ||
		!strings.Contains(detail.LastDeniedText, "额度") {
		t.Errorf("拒绝文案应含码与中文提示，得到 %q", detail.LastDeniedText)
	}

	// 不存在 → 404 JSON；非法 ID → 400 JSON
	resp := e.do(j, http.MethodGet, "/api/v1/users/999999", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的用户应 404，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeNotFound)
	resp = e.do(j, http.MethodGet, "/api/v1/users/abc", "", "")
	requireBadRequest(t, resp, "/api/v1/users/abc")
}

// ---- 请求列表与详情 ----

func TestAPIRequestsListPagingAndFilter(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	seedUser(t, e, 301, store.UserEnabled)
	seedRequest(t, e, 301, "alpha", 1, store.RequestSucceeded)
	seedRequest(t, e, 301, "alpha", 2, store.RequestFailed)
	seedRequest(t, e, 301, "beta", 3, store.RequestSucceeded)

	var all apiListEnvelope[apiRequestRow]
	getAPIJSON(t, e, j, "/api/v1/requests", &all)
	if all.Total != 3 || len(all.Items) != 3 {
		t.Fatalf("应返回全部 3 条，得到 total=%d items=%d", all.Total, len(all.Items))
	}
	// 按请求时间倒序（同刻按 ID 倒序）
	if all.Items[0].ID < all.Items[len(all.Items)-1].ID {
		t.Errorf("应按时间倒序: %+v", all.Items)
	}
	// 列表行与 SSR/CSV 同源下发完整原始消息链接（message_url 单一来源）
	if want := "https://t.me/beta/3"; all.Items[0].MessageURL != want {
		t.Errorf("列表行应带完整消息链接 %s，得到 %q", want, all.Items[0].MessageURL)
	}

	var failed apiListEnvelope[apiRequestRow]
	getAPIJSON(t, e, j, "/api/v1/requests?status="+store.RequestFailed, &failed)
	if failed.Total != 1 || failed.Items[0].ErrorCode != "MEDIA_DOWNLOAD_FAILED" {
		t.Errorf("failed 筛选不对: %+v", failed)
	}

	var channel apiListEnvelope[apiRequestRow]
	getAPIJSON(t, e, j, "/api/v1/requests?channel=beta", &channel)
	if channel.Total != 1 || channel.Items[0].ChannelKey != "beta" {
		t.Errorf("频道筛选不对: %+v", channel)
	}

	// 分页
	var paged apiListEnvelope[apiRequestRow]
	getAPIJSON(t, e, j, "/api/v1/requests?page=2&page_size=2", &paged)
	if paged.Total != 3 || paged.TotalPages != 2 || len(paged.Items) != 1 {
		t.Errorf("分页不对: %+v", paged)
	}

	// 非法参数：user_id 非正整数、since 非日期、分页越界
	for _, q := range []string{"user_id=abc", "user_id=0", "since=2026/08/01", "until=2026-13-01", "page_size=500"} {
		resp := e.do(j, http.MethodGet, "/api/v1/requests?"+q, "", "")
		requireBadRequest(t, resp, "/api/v1/requests?"+q)
	}
}

func TestAPIRequestDetail(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	seedUser(t, e, 401, store.UserEnabled)
	rq := seedRequest(t, e, 401, "alpha", 7, store.RequestFailed)
	priv := seedPrivateRequest(t, e, 401, "-1001234567", 9)

	var detail apiRequestDetail
	getAPIJSON(t, e, j, "/api/v1/requests/"+strconv.FormatInt(rq.ID, 10), &detail)
	if detail.MediaType != "photo" || detail.FileSize != 12345 || detail.FileName != "cat.jpg" {
		t.Errorf("媒体诊断字段不对: %+v", detail)
	}
	if detail.ErrorText == "" || detail.AttemptMax != 3 || detail.Username != "u401" {
		t.Errorf("错误文案/尝试上限/所属用户不对: %+v", detail)
	}
	if detail.ChannelLinkText != "alpha#7" {
		t.Errorf("链接文本应为 alpha#7，得到 %q", detail.ChannelLinkText)
	}

	// 私有链接按 requestLink 同一规则生成；仅出现在受保护响应体
	var privDetail apiRequestDetail
	getAPIJSON(t, e, j, "/api/v1/requests/"+strconv.FormatInt(priv.ID, 10), &privDetail)
	if want := "https://t.me/c/1234567/9"; privDetail.MessageURL != want {
		t.Errorf("私有 message_url 应为 %s，得到 %q", want, privDetail.MessageURL)
	}

	// 公开链接
	if detail.MessageURL != "https://t.me/alpha/7" {
		t.Errorf("公开 message_url 应为 https://t.me/alpha/7，得到 %q", detail.MessageURL)
	}

	resp := e.do(j, http.MethodGet, "/api/v1/requests/999999", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的请求应 404，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeNotFound)
}

// 投递方式标记随列表与详情下发：显式标注各取值 + 未标注回落 upload。
func TestAPIRequestsDeliveryMode(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	seedUser(t, e, 601, store.UserEnabled)

	modes := []string{store.DeliveryModeReference, store.DeliveryModeMixed, store.DeliveryModeText}
	var ids []int64
	for i, mode := range modes {
		r := seedRequest(t, e, 601, "alpha", 10+i, store.RequestSucceeded)
		// seedRequest 已落默认终态，这里覆盖为显式标记
		if err := e.st.FinishRequest(context.Background(), r.ID, store.RequestResult{
			Status: store.RequestSucceeded, MediaType: "album",
			MediaTypes:       []string{"photo", "video"},
			SourceMediaDCIDs: []int{2, 4},
			DeliveryMode:     mode,
		}); err != nil {
			t.Fatalf("落库终态失败: %v", err)
		}
		ids = append(ids, r.ID)
	}

	var all apiListEnvelope[apiRequestRow]
	getAPIJSON(t, e, j, "/api/v1/requests", &all)
	got := map[string]bool{}
	for _, row := range all.Items {
		got[row.DeliveryMode] = true
	}
	for _, want := range modes {
		if !got[want] {
			t.Errorf("列表应下发 delivery_mode=%q：%v", want, all.Items)
		}
	}
	if len(all.Items) == 0 || !slices.Equal(all.Items[0].SourceMediaDCIDs, []int{2, 4}) {
		t.Errorf("列表应下发源媒体 DC：%v", all.Items)
	}
	if !slices.Equal(all.Items[0].MediaTypes, []string{"photo", "video"}) {
		t.Errorf("列表应下发相册成员类型：%v", all.Items)
	}

	var detail apiRequestDetail
	getAPIJSON(t, e, j, "/api/v1/requests/"+strconv.FormatInt(ids[0], 10), &detail)
	if detail.DeliveryMode != store.DeliveryModeReference {
		t.Errorf("详情应下发 delivery_mode=reference，得到 %q", detail.DeliveryMode)
	}
	if !slices.Equal(detail.SourceMediaDCIDs, []int{2, 4}) {
		t.Errorf("详情应下发源媒体 DC，得到 %v", detail.SourceMediaDCIDs)
	}
	if detail.MediaType != "album" || !slices.Equal(detail.MediaTypes, []string{"photo", "video"}) {
		t.Errorf("详情应下发相册类型与成员构成，得到 %+v", detail)
	}
}

// ---- 频道统计 ----

func TestAPIChannelsListAndDetail(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	seedUser(t, e, 501, store.UserEnabled)
	seedRequest(t, e, 501, "alpha", 1, store.RequestSucceeded)
	seedRequest(t, e, 501, "alpha", 2, store.RequestFailed)
	seedRequest(t, e, 501, "beta", 3, store.RequestSucceeded)

	var list apiListEnvelope[apiChannelRow]
	getAPIJSON(t, e, j, "/api/v1/channels", &list)
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("应有 2 个频道，得到 total=%d items=%d", list.Total, len(list.Items))
	}
	first := list.Items[0]
	if first.Key != "alpha" || first.Total != 2 || first.Succeeded != 1 || first.Failed != 1 {
		t.Errorf("alpha 聚合不对: %+v", first)
	}
	if first.SuccessRate <= 0 || first.SuccessRate >= 1 {
		t.Errorf("成功率应为 (0,1) 浮点，得到 %f", first.SuccessRate)
	}

	// 时间范围筛选（今天的请求全部落在范围内；过滤出不存在的范围）
	var none apiListEnvelope[apiChannelRow]
	getAPIJSON(t, e, j, "/api/v1/channels?since=2020-01-01&until=2020-01-02", &none)
	if none.Total != 0 || len(none.Items) != 0 {
		t.Errorf("空范围应无频道，得到 %+v", none)
	}

	// 非法日期 → 400
	resp := e.do(j, http.MethodGet, "/api/v1/channels?since=bad-date", "", "")
	requireBadRequest(t, resp, "/api/v1/channels?since=bad-date")

	// 详情：头部全时段聚合 + 时间范围内趋势/分布
	var detail apiChannelDetail
	getAPIJSON(t, e, j, "/api/v1/channels/alpha", &detail)
	if detail.Key != "alpha" || detail.Stats.Total != 2 {
		t.Errorf("频道详情头部统计不对: %+v", detail)
	}
	if len(detail.Trend) == 0 || detail.Trend[0].Total != 2 {
		t.Errorf("趋势应覆盖当日 2 条请求: %+v", detail.Trend)
	}
	if len(detail.MediaDist) == 0 || detail.MediaDist[0].Key != "photo" {
		t.Errorf("媒体分布不对: %+v", detail.MediaDist)
	}
	if len(detail.ErrorDist) == 0 || detail.ErrorDist[0].Key != "MEDIA_DOWNLOAD_FAILED" {
		t.Errorf("错误分布不对: %+v", detail.ErrorDist)
	}

	// 分布中的空键语义：不校验具体值，但响应必须是合法 JSON 且带统计字段
	if detail.Stats.SuccessRate < 0 || detail.Stats.SuccessRate > 1 {
		t.Errorf("详情成功率应归一在 [0,1]，得到 %f", detail.Stats.SuccessRate)
	}

	// 不存在的频道 → 404 JSON
	resp = e.do(j, http.MethodGet, "/api/v1/channels/no-such-channel", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的频道应 404，得到 %d", resp.StatusCode)
	}
	requireJSONError(t, resp.StatusCode, resp.Header.Get("Content-Type"),
		bodyOf(t, resp), apiCodeNotFound)
}

// ---- 事件列表 ----

func TestAPIEventsList(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	if err := e.st.UpsertEvent(context.Background(), store.Event{
		Key: "mtproto.session_invalid", Severity: "error", Message: "会话失效"}); err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}
	if err := e.st.UpsertEvent(context.Background(), store.Event{
		Key: "mtproto.reconnected", Severity: "info", Message: "已恢复"}); err != nil {
		t.Fatalf("写入事件失败: %v", err)
	}
	if err := e.st.ResolveEventByKey(context.Background(), "mtproto.reconnected"); err != nil {
		t.Fatalf("解决事件失败: %v", err)
	}

	var all apiListEnvelope[apiEventRow]
	getAPIJSON(t, e, j, "/api/v1/events", &all)
	if all.Total != 2 || len(all.Items) != 2 {
		t.Fatalf("应有 2 个事件，得到 total=%d", all.Total)
	}

	var open apiListEnvelope[apiEventRow]
	getAPIJSON(t, e, j, "/api/v1/events?status=open", &open)
	if open.Total != 1 || open.Items[0].Key != "mtproto.session_invalid" {
		t.Errorf("open 筛选不对: %+v", open)
	}

	var resolved apiListEnvelope[apiEventRow]
	getAPIJSON(t, e, j, "/api/v1/events?status=resolved", &resolved)
	if resolved.Total != 1 || resolved.Items[0].Status != store.EventResolved {
		t.Errorf("resolved 筛选不对: %+v", resolved)
	}

	// 分页与信封
	var paged apiListEnvelope[apiEventRow]
	getAPIJSON(t, e, j, "/api/v1/events?page=1&page_size=1", &paged)
	if paged.Total != 2 || paged.TotalPages != 2 || len(paged.Items) != 1 {
		t.Errorf("分页不对: %+v", paged)
	}
}

// ---- 审计列表 ----

// seedAudit 批量写入审计记录（供审计 API 测试构造数据）。
func seedAudit(t *testing.T, e *testEnv, entries ...store.AuditEntry) {
	t.Helper()
	for _, en := range entries {
		if err := e.st.AppendAudit(context.Background(), en); err != nil {
			t.Fatalf("写审计失败: %v", err)
		}
	}
}

func TestAPIAuditList(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	seedAudit(t, e,
		store.AuditEntry{At: 1000, Actor: "admin", Action: "user.enable", Target: "user:1",
			BeforeJSON: `{"status":"pending"}`, AfterJSON: `{"status":"enabled"}`},
		store.AuditEntry{At: 2000, Actor: "system", Action: "backup.export"},
		store.AuditEntry{At: 3000, Actor: "admin", Action: "quota.reset", Target: "user:2",
			AfterJSON: `not-json`},
	)

	var all apiListEnvelope[apiAuditRow]
	body := getAPIJSON(t, e, j, "/api/v1/audit", &all)
	if resp := e.do(j, http.MethodGet, "/api/v1/audit", "", ""); resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("列表响应应禁止缓存，得到 Cache-Control %q", resp.Header.Get("Cache-Control"))
	} else {
		bodyOf(t, resp)
	}
	// 信封形状：种子 3 条 + e.login 固定写入的 1 条 auth.login；缺省分页参数生效
	if all.Total != 4 || len(all.Items) != 4 || all.Page != 1 || all.PageSize != apiDefaultPageSize {
		t.Fatalf("分页信封不对: %+v", all)
	}
	// 时间倒序（同 ListAudit 口径）：登录条目时间戳取真实时钟，必然最晚
	wantOrder := []string{"auth.login", "quota.reset", "backup.export", "user.enable"}
	for i, action := range wantOrder {
		if all.Items[i].Action != action {
			t.Fatalf("第 %d 行应为 %s，得到 %s", i, action, all.Items[i].Action)
		}
	}
	first := all.Items[1]
	if first.Actor != "admin" || first.Target != "user:2" || first.CreatedAt != 3000 {
		t.Errorf("行字段不对: %+v", first)
	}

	// before/after 保持 JSON 原始值下发：合法 JSON 原样、空记录为 null 字面量、
	// 普通文本回退为 JSON 字符串（与 SSR prettyJSON 回退语义一致）
	var rawEnvelope struct {
		Items []struct {
			Action string          `json:"action"`
			Before json.RawMessage `json:"before"`
			After  json.RawMessage `json:"after"`
		} `json:"items"`
	}
	decodeAPIJSON(t, body, &rawEnvelope)
	enable := rawEnvelope.Items[3]
	if string(enable.Before) != `{"status":"pending"}` || string(enable.After) != `{"status":"enabled"}` {
		t.Errorf("before/after 应保持原始 JSON: %s / %s", enable.Before, enable.After)
	}
	blank := rawEnvelope.Items[2]
	if string(blank.Before) != "null" || string(blank.After) != "null" {
		t.Errorf("无快照应为 null 字面量: %s / %s", blank.Before, blank.After)
	}
	if got := string(rawEnvelope.Items[1].After); got != `"not-json"` {
		t.Errorf("非法 JSON 文本应回退为字符串字面量，得到 %s", got)
	}
}

func TestAPIAuditListTimeFilter(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	seedAudit(t, e,
		store.AuditEntry{At: 1000, Action: "action.first"},
		store.AuditEntry{At: 2000, Action: "action.second"},
	)

	var filtered apiListEnvelope[apiAuditRow]
	getAPIJSON(t, e, j, "/api/v1/audit?since=1970-01-01&until=1970-01-01", &filtered)
	if filtered.Total != 2 || len(filtered.Items) != 2 {
		t.Fatalf("日期筛选总数与列表应同口径: %+v", filtered)
	}
	var beforeEpoch apiListEnvelope[apiAuditRow]
	getAPIJSON(t, e, j, "/api/v1/audit?until=1969-12-31", &beforeEpoch)
	if beforeEpoch.Total != 0 || len(beforeEpoch.Items) != 0 {
		t.Fatalf("epoch 以前的上界不应被当作未设置: %+v", beforeEpoch)
	}
	resp := e.do(j, http.MethodGet, "/api/v1/audit?since=2026-08-03&until=2026-08-02", "", "")
	requireBadRequest(t, resp, "/api/v1/audit reverse range")
}

func TestAPIAuditListPagingAndBadParams(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	seedAudit(t, e,
		store.AuditEntry{At: 1000, Action: "action.first"},
		store.AuditEntry{At: 2000, Action: "action.second"},
		store.AuditEntry{At: 3000, Action: "action.third"},
	)

	var p1 apiListEnvelope[apiAuditRow]
	getAPIJSON(t, e, j, "/api/v1/audit?page=1&page_size=2", &p1)
	// 种子 3 条 + e.login 固定写入的 1 条 auth.login（真实时钟，排最前）
	if p1.Total != 4 || p1.TotalPages != 2 || len(p1.Items) != 2 {
		t.Fatalf("第一页分页不对: %+v", p1)
	}
	if p1.Items[0].Action != "auth.login" || p1.Items[1].Action != "action.third" {
		t.Errorf("第一页顺序不对: %s / %s", p1.Items[0].Action, p1.Items[1].Action)
	}
	var p2 apiListEnvelope[apiAuditRow]
	getAPIJSON(t, e, j, "/api/v1/audit?page=2&page_size=2", &p2)
	if len(p2.Items) != 2 || p2.Items[0].Action != "action.second" || p2.Items[1].Action != "action.first" {
		t.Errorf("第二页应剩最早两条: %+v", p2.Items)
	}

	// 越界页为空且 items 归一为 []（非 null）
	var beyond apiListEnvelope[apiAuditRow]
	body := getAPIJSON(t, e, j, "/api/v1/audit?page=9&page_size=2", &beyond)
	if beyond.Total != 4 || len(beyond.Items) != 0 {
		t.Errorf("越界页应为空: %+v", beyond)
	}
	var raw map[string]json.RawMessage
	decodeAPIJSON(t, body, &raw)
	if string(raw["items"]) != "[]" {
		t.Errorf("空页 items 应为 []，得到 %s", raw["items"])
	}

	// 非法分页参数 → 400 JSON
	for _, q := range []string{"page=0", "page=-1", "page=abc", "page_size=0", "page_size=201"} {
		resp := e.do(j, http.MethodGet, "/api/v1/audit?"+q, "", "")
		requireBadRequest(t, resp, "/api/v1/audit?"+q)
	}
}

// 仅剩 e.login 固定写入的 1 条 auth.login 时的信封形状；越界页 items
// 归一为 []（前端不必区分 null 与 []）。
func TestAPIAuditListOnlyLoginEntry(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	var env apiListEnvelope[apiAuditRow]
	body := getAPIJSON(t, e, j, "/api/v1/audit", &env)
	if env.Total != 1 || env.TotalPages != 1 || len(env.Items) != 1 || env.Items[0].Action != "auth.login" {
		t.Fatalf("信封不对: %+v", env)
	}
	var raw struct {
		Items []struct {
			Before json.RawMessage `json:"before"`
			After  json.RawMessage `json:"after"`
		} `json:"items"`
	}
	decodeAPIJSON(t, body, &raw)
	if string(raw.Items[0].Before) != "null" {
		t.Errorf("登录条目无快照应编码为 null，得到 %s", raw.Items[0].Before)
	}

	// 越界页：items 归一为 []
	var beyond apiListEnvelope[apiAuditRow]
	body = getAPIJSON(t, e, j, "/api/v1/audit?page=3", &beyond)
	if beyond.Total != 1 || len(beyond.Items) != 0 {
		t.Fatalf("越界页应为空: %+v", beyond)
	}
	var rawBeyond map[string]json.RawMessage
	decodeAPIJSON(t, body, &rawBeyond)
	if string(rawBeyond["items"]) != "[]" {
		t.Errorf("空页 items 应为 []，得到 %s", rawBeyond["items"])
	}
}

// ---- 总览与业务统计 ----

func TestAPIOverviewFullPage(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	seedUser(t, e, 601, store.UserEnabled)
	seedUser(t, e, 602, store.UserPending)
	seedRequest(t, e, 601, "alpha", 1, store.RequestSucceeded)
	seedRequest(t, e, 601, "alpha", 2, store.RequestFailed)

	var view apiOverviewView
	getAPIJSON(t, e, j, "/api/v1/overview", &view)
	if resp := e.do(j, http.MethodGet, "/api/v1/overview", "", ""); resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("总览响应应禁止缓存，得到 Cache-Control %q", resp.Header.Get("Cache-Control"))
	} else {
		bodyOf(t, resp)
	}
	if view.Version != Version || view.Addr != e.srv.cfg.WebAddr || view.Workers != e.srv.cfg.WorkerCount {
		t.Errorf("服务信息不对: %+v", view)
	}
	if view.StartedAt == 0 {
		t.Errorf("启动时间必须有值: %+v", view)
	}
	if view.Health.MTProtoState != "unknown" {
		t.Errorf("未接入 MTProto 时状态应为 unknown，得到 %q", view.Health.MTProtoState)
	}
	if !view.Health.StoreOK || view.Health.DBPath == "" {
		t.Errorf("健康字段不对: %+v", view.Health)
	}
	if view.Queue == nil || view.Queue.Cap != 8 {
		t.Errorf("队列指标不对: %+v", view.Queue)
	}
	if view.Users.Total != 2 || view.Users.Enabled != 1 || view.Users.Pending != 1 {
		t.Errorf("用户计数不对: %+v", view.Users)
	}

	// overview 不再解析时间参数（原 since/until 语义迁至 /api/v1/stats）
	if resp := e.do(j, http.MethodGet, "/api/v1/overview?since=nope", "", ""); resp.StatusCode != http.StatusOK {
		t.Errorf("overview 不再解析日期参数，应保持 200，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
}

func TestAPIStatsFullPage(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	seedUser(t, e, 601, store.UserEnabled)
	seedRequest(t, e, 601, "alpha", 1, store.RequestSucceeded)
	seedRequest(t, e, 601, "alpha", 2, store.RequestFailed)

	// 宽范围下断言数据点（种子时间戳为真实时钟，与服务注入时钟不同源，
	// 与 SSR TestOverviewPage 同一处理）
	var view apiStatsView
	getAPIJSON(t, e, j, "/api/v1/stats?since=2026-01-01&until=2027-01-01", &view)
	if view.Requests.Total != 2 || view.Requests.Succeeded != 1 || view.Requests.Failed != 1 {
		t.Errorf("请求指标不对: %+v", view.Requests)
	}
	if view.Requests.SuccessRate <= 0 || view.Requests.SuccessRate >= 1 {
		t.Errorf("成功率应为 (0,1)，得到 %f", view.Requests.SuccessRate)
	}
	if view.Requests.ErrorRate <= 0 || view.Requests.ErrorRate >= 1 {
		t.Errorf("错误率应为 (0,1)，得到 %f", view.Requests.ErrorRate)
	}
	if len(view.Requests.Trend) == 0 {
		t.Fatal("请求趋势不应为空")
	}
	if len(view.Requests.TopChannels) != 1 || view.Requests.TopChannels[0].Key != "alpha" ||
		view.Requests.TopChannels[0].Succeeded != 1 || view.Requests.TopChannels[0].Failed != 1 {
		t.Errorf("主要频道不对: %+v", view.Requests.TopChannels)
	}
	if len(view.Requests.TopUsers) != 1 || view.Requests.TopUsers[0].ID != 601 {
		t.Errorf("主要用户不对: %+v", view.Requests.TopUsers)
	}
	if len(view.Requests.MediaDist) == 0 || len(view.Requests.ErrorDist) == 0 {
		t.Errorf("分布不应为空: media=%v error=%v",
			view.Requests.MediaDist, view.Requests.ErrorDist)
	}
	if len(view.Requests.ErrorDist) != 1 || view.Requests.ErrorDist[0].Ratio != 1 {
		t.Errorf("错误原因占比不对: %+v", view.Requests.ErrorDist)
	}

	// 自定义时间范围：空范围时指标归零，趋势保留该范围的空日期点。
	getAPIJSON(t, e, j, "/api/v1/stats?since=2020-01-01&until=2020-01-02", &view)
	if view.Requests.Total != 0 || view.SinceDay != "2020-01-01" {
		t.Errorf("自定义范围不生效: since=%s total=%d", view.SinceDay, view.Requests.Total)
	}
	if len(view.Requests.Trend) != 2 || view.Requests.Trend[0].Day != "2020-01-01" ||
		view.Requests.Trend[0].ErrorRate != nil || view.Requests.Trend[1].Total != 0 {
		t.Errorf("空范围趋势应保留两个无样本日期: %+v", view.Requests.Trend)
	}

	// 非法日期 → 400
	resp := e.do(j, http.MethodGet, "/api/v1/stats?since=nope", "", "")
	requireBadRequest(t, resp, "/api/v1/stats?since=nope")
}

// CSV 下载入口不被 API 迁移影响：同源 URL 与筛选参数沿用 SSR 行为。
func TestAPIMigrationKeepsCSVEndpoints(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	seedUser(t, e, 701, store.UserEnabled)
	seedRequest(t, e, 701, "alpha", 1, store.RequestSucceeded)

	for _, path := range []string{
		"/users/export.csv", "/requests/export.csv?status=succeeded", "/channels/export.csv",
	} {
		resp := e.do(j, http.MethodGet, path, "", "")
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s 应 200，得到 %d", path, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
			t.Errorf("GET %s 应为 CSV，得到 %q", path, ct)
		}
		bodyOf(t, resp)
	}

	// 旧页面入口已删除，SPA 页面只通过 /api/v1 获取数据。
	resp := e.do(j, http.MethodGet, "/users", "", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("旧 /users 路径应返回 404，得到 %d", resp.StatusCode)
	}
	bodyOf(t, resp)
}
