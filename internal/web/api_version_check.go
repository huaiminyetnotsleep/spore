package web

// GET /api/v1/version/check 检查更新 API：把当前服务版本与上游最新发布
// （GitHub Releases，经 ReleaseChecker 的 TTL 缓存）比较，供总览页"服务
// 版本"旁的刷新按钮与升级提示使用。当前版本无法解析（dev 构建、旧格式）
// 时 status=unknown 并仍下发上游最新版本；上游查询失败返回受控 503。

import (
	"net/http"
)

// apiVersionCheckView 是 GET /api/v1/version/check 的响应 DTO。
type apiVersionCheckView struct {
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version,omitempty"`
	// Status 是比较结论 raw 值：up_to_date | outdated | unknown（当前版本
	// 无法解析时为 unknown，中文标签由前端处理）。
	Status     string `json:"status"`
	ReleaseURL string `json:"release_url,omitempty"`
	CheckedAt  int64  `json:"checked_at"` // Unix 毫秒；缓存命中时为原始查询时间
}

func (s *Server) handleAPIVersionCheck(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.version_check"
	if s.release == nil {
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable, apiUserMessage(apiCodeUnavailable))
		return
	}
	info := s.release.LatestRelease(r.Context())
	if info.Version == "" {
		s.log.Warn("查询上游最新发布失败", "op", op)
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable, "查询最新版本失败，请检查服务器到 GitHub 的网络后重试。")
		return
	}
	view := apiVersionCheckView{
		CurrentVersion: s.version,
		LatestVersion:  info.Version,
		Status:         "unknown",
		ReleaseURL:     info.URL,
		CheckedAt:      info.CheckedAt.UnixMilli(),
	}
	if cmp, ok := compareSemver(s.version, info.Version); ok {
		if cmp < 0 {
			view.Status = "outdated"
		} else {
			view.Status = "up_to_date"
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPIJSON(w, http.StatusOK, view)
}
