package monitor

import "github.com/huaiminyetnotsleep/spore/internal/store"

type accumulator struct {
	latestRSS, latestTemp                   *int64
	downloadBytes, uploadBytes              float64
	cpuPercent                              float64
	duration                                float64
	hasRSS, hasTemp, hasDownload, hasUpload bool
	hasCPU                                  bool
}

func (a *accumulator) add(point Point) {
	if point.RSSBytes != nil {
		a.latestRSS = cloneInt64(point.RSSBytes)
		a.hasRSS = true
	}
	if point.TempDirBytes != nil {
		a.latestTemp = cloneInt64(point.TempDirBytes)
		a.hasTemp = true
	}
	if point.DownloadBytesPerSecond != nil {
		a.downloadBytes += *point.DownloadBytesPerSecond
		a.hasDownload = true
	}
	if point.UploadBytesPerSecond != nil {
		a.uploadBytes += *point.UploadBytesPerSecond
		a.hasUpload = true
	}
	if point.CPUPercent != nil {
		a.cpuPercent += *point.CPUPercent
		a.hasCPU = true
	}
	a.duration++
}

func (a *accumulator) sample(at int64) (store.SystemMetricSample, bool) {
	if a.duration == 0 {
		return store.SystemMetricSample{}, false
	}
	out := store.SystemMetricSample{SampledAt: at, RSSBytes: cloneInt64(a.latestRSS), TempDirBytes: cloneInt64(a.latestTemp)}
	if a.hasDownload {
		v := a.downloadBytes / a.duration
		out.DownloadBytesPerSecond = &v
	}
	if a.hasUpload {
		v := a.uploadBytes / a.duration
		out.UploadBytesPerSecond = &v
	}
	if a.hasCPU {
		v := a.cpuPercent / a.duration
		out.CPUPercent = &v
	}
	return out, true
}

func (a *accumulator) reset() { *a = accumulator{} }
