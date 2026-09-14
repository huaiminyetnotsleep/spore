package web

import (
	"net/http"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/monitor"
)

type apiSystemMetricPoint struct {
	At                     int64    `json:"at"`
	RSSBytes               *int64   `json:"rss_bytes"`
	TempDirBytes           *int64   `json:"temp_dir_bytes"`
	DownloadBytesPerSecond *float64 `json:"download_bytes_per_second"`
	UploadBytesPerSecond   *float64 `json:"upload_bytes_per_second"`
	CPUPercent             *float64 `json:"cpu_percent"`
}

type apiSystemMetricsView struct {
	Range            string                 `json:"range"`
	Since            int64                  `json:"since"`
	Until            int64                  `json:"until"`
	SampleIntervalMS int64                  `json:"sample_interval_ms"`
	Points           []apiSystemMetricPoint `json:"points"`
}

func (s *Server) handleAPISystemMetrics(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.system_metrics"
	if s.monitor == nil {
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable, apiUserMessage(apiCodeUnavailable))
		return
	}
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "realtime"
	}
	now := s.now()
	var since, until time.Time
	var points []monitor.Point
	var err error
	var interval time.Duration
	switch rangeName {
	case "realtime":
		interval = 2 * time.Second
		until = now
		since = now.Add(-15 * time.Minute)
		points = s.monitor.Realtime(since, until)
	case "4h":
		interval = 30 * time.Second
		until = now
		since = now.Add(-4 * time.Hour)
		points, err = s.monitor.Historical(r.Context(), since, until, 0)
	case "1d":
		interval = 2 * time.Minute
		until = now
		since = now.Add(-24 * time.Hour)
		points, err = s.monitor.Historical(r.Context(), since, until, 2*time.Minute)
	default:
		writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, apiUserMessage(apiCodeBadRequest))
		return
	}
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	view := apiSystemMetricsView{Range: rangeName, Since: since.UnixMilli(), Until: until.UnixMilli(), SampleIntervalMS: interval.Milliseconds(), Points: make([]apiSystemMetricPoint, 0, len(points))}
	for _, point := range points {
		view.Points = append(view.Points, apiSystemMetricPoint{At: point.SampledAt.UnixMilli(), RSSBytes: point.RSSBytes, TempDirBytes: point.TempDirBytes, DownloadBytesPerSecond: point.DownloadBytesPerSecond, UploadBytesPerSecond: point.UploadBytesPerSecond, CPUPercent: point.CPUPercent})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPIJSON(w, http.StatusOK, view)
}
