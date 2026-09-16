package web

// CSV 导出测试：BOM 头、字段转义、行数上限截断与导出审计。

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// readCSV 断言 BOM 并解码全部行。
func readCSV(t *testing.T, resp *http.Response) [][]string {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("读取响应失败: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatalf("CSV 应带 UTF-8 BOM，前缀为 %v", raw[:min(8, len(raw))])
	}
	rows, err := csv.NewReader(bytes.NewReader(raw[3:])).ReadAll()
	if err != nil {
		t.Fatalf("CSV 解析失败: %v", err)
	}
	return rows
}

func TestRequestsCSV(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	seedUser(t, e, 1, store.UserEnabled)
	r := seedRequest(t, e, 1, "example_channel", 10, store.RequestSucceeded)
	if err := e.st.FinishRequest(context.Background(), r.ID, store.RequestResult{
		Status: store.RequestSucceeded, MediaType: "album", MediaTypes: []string{"photo", "video"},
		DeliveryMode: store.DeliveryModeUpload, FileName: "cat.jpg",
	}); err != nil {
		t.Fatalf("覆盖相册元数据失败: %v", err)
	}

	resp := e.do(j, "GET", "/requests/export.csv", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出应 200，得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/csv") {
		t.Fatalf("Content-Type 应为 text/csv，得到 %q", ct)
	}
	rows := readCSV(t, resp)
	if len(rows) != 2 { // 表头 + 1 行
		t.Fatalf("应有表头与一行数据，得到 %d 行", len(rows))
	}
	if rows[0][0] != "ID" || rows[1][2] != "example_channel" || rows[1][3] != "10" {
		t.Fatalf("行内容不符：%v", rows)
	}
	if rows[0][10] != "投递方式" || rows[1][10] != "上传" {
		t.Fatalf("投递方式列不符：%v", rows)
	}
	if rows[0][11] != "机器人" || rows[1][11] != "" {
		t.Fatalf("机器人列不符：%v", rows)
	}
	if rows[1][8] != "album" || rows[1][13] != "cat.jpg" || rows[0][18] != "媒体内容类型" || rows[1][18] != "photo+video" {
		t.Fatalf("媒体元数据不符：%v", rows[1])
	}
	if !e.containsAction("export.requests") {
		t.Error("导出应写审计")
	}
}

// 投递方式标记随记录导出：显式标注的各取值转中文标签。
func TestRequestsCSVDeliveryModeLabels(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	seedUser(t, e, 1, store.UserEnabled)

	for i, mode := range []string{
		store.DeliveryModeReference, store.DeliveryModeMixed, store.DeliveryModeText, store.DeliveryModeCloud,
	} {
		r, err := e.st.CreateRequest(context.Background(), store.Request{
			UserID: 1, SourceKind: store.SourcePublic, ChannelKey: "example",
			MessageID: 20 + i,
		})
		if err != nil {
			t.Fatalf("创建请求失败: %v", err)
		}
		if err := e.st.FinishRequest(context.Background(), r.ID, store.RequestResult{
			Status: store.RequestSucceeded, MediaType: "photo", DeliveryMode: mode,
		}); err != nil {
			t.Fatalf("落库终态失败: %v", err)
		}
	}

	resp := e.do(j, "GET", "/requests/export.csv", "", "")
	rows := readCSV(t, resp)
	got := map[string]bool{}
	for _, r := range rows[1:] {
		got[r[10]] = true
	}
	for _, want := range []string{"引用", "混合", "文本", "网盘"} {
		if !got[want] {
			t.Errorf("CSV 应包含投递方式 %q：%v", want, rows)
		}
	}
}

func TestCSVFieldEscaping(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	seedUser(t, e, 1, store.UserEnabled)
	// 频道键注入逗号/引号/换行：CSV 必须正确转义并能解析回原值
	weird := "we,ird\"cha\nnl"
	seedRequest(t, e, 1, weird, 1, store.RequestSucceeded)

	resp := e.do(j, "GET", "/requests/export.csv", "", "")
	rows := readCSV(t, resp)
	found := false
	for _, r := range rows[1:] {
		if r[2] == weird {
			found = true
		}
	}
	if !found {
		t.Fatalf("含特殊字符的频道键应被正确转义并可解析回：%v", rows)
	}
}

func TestCSVRowCapTruncation(t *testing.T) {
	// 调小上限验证截断保护（测试后恢复，避免影响其他用例）
	old := csvMaxRows
	csvMaxRows = 3
	defer func() { csvMaxRows = old }()

	e := newTestEnv(t, nil)
	j := e.login(t)
	seedUser(t, e, 1, store.UserEnabled)
	for i := 0; i < 5; i++ {
		seedRequest(t, e, 1, "example_channel", 100+i, store.RequestQueued)
	}

	resp := e.do(j, "GET", "/requests/export.csv", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("导出应 200，得到 %d", resp.StatusCode)
	}
	if ht := resp.Header.Get("X-Export-Truncated"); ht != "true" {
		t.Fatalf("截断时应设置 X-Export-Truncated，得到 %q", ht)
	}
	rows := readCSV(t, resp)
	if len(rows) != 4 { // 表头 + 3 行（上限）
		t.Fatalf("应截断到上限行数，得到 %d 行", len(rows))
	}
}

func TestChannelsCSVAndUsersCSV(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)
	seedUser(t, e, 1, store.UserEnabled)
	seedUser(t, e, 2, store.UserDisabled)
	seedRequest(t, e, 1, "alpha", 1, store.RequestSucceeded)
	seedRequest(t, e, 1, "alpha", 2, store.RequestFailed)

	resp := e.do(j, "GET", "/channels/export.csv", "", "")
	rows := readCSV(t, resp)
	if len(rows) != 2 || rows[1][0] != "alpha" || rows[1][1] != "2" {
		t.Fatalf("频道导出内容不符：%v", rows)
	}
	if !e.containsAction("export.channels") {
		t.Error("频道导出应写审计")
	}

	resp = e.do(j, "GET", "/users/export.csv", "", "")
	rows = readCSV(t, resp)
	if len(rows) != 3 { // 表头 + 2 用户
		t.Fatalf("用户导出行数不符：%v", rows)
	}
	byID := map[string][]string{}
	for _, r := range rows[1:] {
		byID[r[0]] = r
	}
	if r := byID["1"]; r[6] != "2" || r[7] != "1" || r[8] != "1" {
		t.Fatalf("用户用量口径不符：%v", r)
	}
	if r := byID["2"]; r[3] != "已禁用" {
		t.Fatalf("用户状态不符：%v", r)
	}
	if !e.containsAction("export.users") {
		t.Error("用户导出应写审计")
	}
}

func TestRequestLinkRejectsMalformedStructuredFields(t *testing.T) {
	cases := []struct {
		name string
		rq   store.Request
	}{
		{name: "private zero id", rq: store.Request{SourceKind: store.SourcePrivate, ChannelKey: "-1000", MessageID: 1}},
		{name: "public short username", rq: store.Request{SourceKind: store.SourcePublic, ChannelKey: "abc", MessageID: 1}},
		{name: "public path injection", rq: store.Request{SourceKind: store.SourcePublic, ChannelKey: "good/name", MessageID: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestLink(tc.rq); got != "" {
				t.Fatalf("异常结构化记录应输出空 URL，得到 %q", got)
			}
		})
	}
}

func TestCSVCopyIsIndependent(t *testing.T) {
	// 频道导出 CSV 复用页面同款筛选参数（日期范围）：限定空区间 → 仅表头
	e := newTestEnv(t, nil)
	j := e.login(t)
	seedUser(t, e, 1, store.UserEnabled)
	seedRequest(t, e, 1, "alpha", 1, store.RequestSucceeded)

	resp := e.do(j, "GET", "/channels/export.csv?since=2020-01-01&until=2020-01-02", "", "")
	rows := readCSV(t, resp)
	if len(rows) != 1 {
		t.Fatalf("无匹配时应只有表头，得到 %v", rows)
	}
}
