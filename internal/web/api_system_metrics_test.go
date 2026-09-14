package web

import (
	"net/http"
	"testing"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/monitor"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

func TestAPISystemMetricsRangesAndValidation(t *testing.T) {
	e := newTestEnv(t, nil)
	e.srv.monitor = monitor.New(monitor.Options{Store: e.st})
	j := e.login(t)
	now := e.clock.Now().UnixMilli()
	rss, temp, down, up := int64(1024), int64(2048), 3.5, 4.5
	if err := e.st.UpsertSystemMetricSample(t.Context(), store.SystemMetricSample{
		SampledAt: now - time.Hour.Milliseconds(), RSSBytes: &rss, TempDirBytes: &temp,
		DownloadBytesPerSecond: &down, UploadBytesPerSecond: &up,
	}); err != nil {
		t.Fatalf("写入系统指标失败: %v", err)
	}
	var view apiSystemMetricsView
	getAPIJSON(t, e, j, "/api/v1/system-metrics?range=4h", &view)
	if view.Range != "4h" || view.SampleIntervalMS != 30000 || len(view.Points) != 1 {
		t.Fatalf("4h 响应不对: %+v", view)
	}
	if view.Points[0].RSSBytes == nil || *view.Points[0].RSSBytes != rss {
		t.Fatalf("RSS 未保留: %+v", view.Points[0])
	}
	if resp := e.do(j, http.MethodGet, "/api/v1/system-metrics?range=bad", "", ""); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 range 应 400，得到 %d", resp.StatusCode)
	} else {
		bodyOf(t, resp)
	}
	if resp := e.do(j, http.MethodGet, "/api/v1/system-metrics?range=1d", "", ""); resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("监控响应应 no-store，得到 %q", resp.Header.Get("Cache-Control"))
	} else {
		bodyOf(t, resp)
	}
}
