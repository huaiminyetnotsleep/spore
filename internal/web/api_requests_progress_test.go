package web

// 请求记录 API 的实时进度 enrich 测试：processing 状态且 worker 已在共享
// 注册表登记的记录下发 progress；排队与终态记录不携带该字段。

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/progress"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func TestAPIRequestProgressEnrichment(t *testing.T) {
	reg := progress.NewRegistry()
	e := newTestEnvOpts(t, func(_ *config.Config, opt *Options) { opt.Progress = reg })
	j := e.login(t)

	seedUser(t, e, 701, store.UserEnabled)

	// processing 且已注册：下发进度快照
	processing := store.Request{UserID: 701, SourceKind: store.SourcePublic, ChannelKey: "alpha", MessageID: 1}
	created, err := e.st.CreateRequest(context.Background(), processing)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.MarkRequestStarted(context.Background(), created.ID, 0); err != nil {
		t.Fatal(err)
	}
	reg.Register(created.ID)
	reg.AddTotal(created.ID, 1000)
	reg.AddDownloaded(created.ID, 400)
	reg.AddUploaded(created.ID, 100)

	var list apiListEnvelope[apiRequestRow]
	getAPIJSON(t, e, j, "/api/v1/requests", &list)
	if len(list.Items) != 1 {
		t.Fatalf("应返回 1 条，得到 %d", len(list.Items))
	}
	p := list.Items[0].Progress
	if p == nil {
		t.Fatalf("processing 记录应携带 progress：%+v", list.Items[0])
	}
	if p.TotalBytes != 1000 || p.DownloadedBytes != 400 || p.UploadedBytes != 100 {
		t.Errorf("进度快照数值不符: %+v", p)
	}

	var detail apiRequestDetail
	getAPIJSON(t, e, j, "/api/v1/requests/"+strconv.FormatInt(created.ID, 10), &detail)
	if detail.Progress == nil || detail.Progress.DownloadedBytes != 400 {
		t.Errorf("详情应携带同样的 progress: %+v", detail.Progress)
	}

	// queued 记录：即使注册表残留（防御）也不下发
	queued := store.Request{UserID: 701, SourceKind: store.SourcePublic, ChannelKey: "alpha", MessageID: 2}
	queuedRow, err := e.st.CreateRequest(context.Background(), queued)
	if err != nil {
		t.Fatal(err)
	}
	var queuedList apiListEnvelope[apiRequestRow]
	getAPIJSON(t, e, j, "/api/v1/requests", &queuedList)
	for _, row := range queuedList.Items {
		if row.ID == queuedRow.ID && row.Progress != nil {
			t.Errorf("queued 记录不应携带 progress: %+v", row)
		}
	}

	// 任务收尾（Done）后：processing 行也不再下发
	reg.Done(created.ID)
	var afterDone apiListEnvelope[apiRequestRow]
	getAPIJSON(t, e, j, "/api/v1/requests", &afterDone)
	for _, row := range afterDone.Items {
		if row.ID == created.ID && row.Progress != nil {
			t.Errorf("Done 后不应再携带 progress: %+v", row)
		}
	}
}

// progress 未接线（nil 注册表）时接口保持可用且不下发进度。
func TestAPIRequestProgressNotWired(t *testing.T) {
	e := newTestEnv(t, nil)
	j := e.login(t)

	seedUser(t, e, 702, store.UserEnabled)
	r := store.Request{UserID: 702, SourceKind: store.SourcePublic, ChannelKey: "alpha", MessageID: 1}
	created, err := e.st.CreateRequest(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.MarkRequestStarted(context.Background(), created.ID, 0); err != nil {
		t.Fatal(err)
	}

	resp := e.do(j, http.MethodGet, "/api/v1/requests", "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("未接线时列表应正常返回，得到 %d", resp.StatusCode)
	}
	var list apiListEnvelope[map[string]any]
	getAPIJSON(t, e, j, "/api/v1/requests", &list)
	for _, row := range list.Items {
		if _, ok := row["progress"]; ok {
			t.Errorf("未接线时不应下发 progress 字段: %+v", row)
		}
	}
}
