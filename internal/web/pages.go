package web

// 管理端共享辅助：运营时区与时间格式化、查询参数解析，以及业务统计
// （/api/v1/stats）口径的时间范围缺省填充。当前 SSR 管理页面已全部删除，
// 当前 SSR 渲染归零，本文件只保留 /api/v1 与 CSV 导出仍在使用的底层 helper。

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// 时间与文本格式化常量。
const (
	timeFormat     = "2006-01-02 15:04:05"
	dayInputFormat = "2006-01-02" // 筛选参数 since/until 的日期格式（运营时区）
)

// ---- 运营时区与格式化 ----

// tz 返回运营时区（access 设置；服务未注入时回退 UTC）。
func (s *Server) tz(ctx context.Context) *time.Location {
	if s.access != nil {
		return s.access.Location(ctx)
	}
	return time.UTC
}

// fmtTime 按运营时区格式化 Unix 毫秒时间戳；0（未发生）返回 "—"。
func fmtTime(ms int64, loc *time.Location) string {
	if ms == 0 {
		return "—"
	}
	return time.UnixMilli(ms).In(loc).Format(timeFormat)
}

// statusText 把用户/请求状态码转为中文标签。
func statusText(status string) string {
	switch status {
	case store.UserPending:
		return "待审批"
	case store.UserEnabled:
		return "已启用"
	case store.UserDisabled:
		return "已禁用"
	case store.UserArchived:
		return "已归档"
	case store.RequestQueued:
		return "排队中"
	case store.RequestProcessing:
		return "处理中"
	case store.RequestSucceeded:
		return "成功"
	case store.RequestFailed:
		return "失败"
	case store.RequestCancelled:
		return "已取消"
	}
	return status
}

// ---- 查询参数解析 ----

// timeRange 是解析后的时间范围筛选（Unix 毫秒，Until 不含）。
type timeRange struct {
	SinceDay string // 原始输入（回显用）
	UntilDay string
	Since    int64 // 0 = 不限
	Until    int64 // 0 = 不限
}

// parseTimeRange 解析 since/until 日期参数（运营时区的 YYYY-MM-DD）：
// since 取当日 00:00 起、until 取次日 00:00 前（当天全天）。
// 非法格式或 since > until 返回错误（调用方回 400）。
func parseTimeRange(r *http.Request, loc *time.Location) (timeRange, error) {
	tr := timeRange{
		SinceDay: strings.TrimSpace(r.URL.Query().Get("since")),
		UntilDay: strings.TrimSpace(r.URL.Query().Get("until")),
	}
	parseDay := func(v string) (time.Time, bool, error) {
		if v == "" {
			return time.Time{}, false, nil
		}
		t, err := time.ParseInLocation(dayInputFormat, v, loc)
		if err != nil {
			return time.Time{}, false, fmt.Errorf("日期 %q 不是合法的 YYYY-MM-DD 格式", v)
		}
		return t, true, nil
	}
	since, sinceSet, err := parseDay(tr.SinceDay)
	if err != nil {
		return tr, err
	}
	until, untilSet, err := parseDay(tr.UntilDay)
	if err != nil {
		return tr, err
	}
	if sinceSet && untilSet && since.After(until) {
		return tr, errors.New("开始日期不能晚于结束日期")
	}
	if sinceSet {
		tr.Since = since.UnixMilli()
	}
	if untilSet {
		tr.Until = until.AddDate(0, 0, 1).UnixMilli()
	}
	return tr, nil
}

// ---- 业务统计口径（/api/v1/stats）----

// fillDefaultStatsRange 在未提供时间范围时填充缺省近 7 天
// （运营时区，含当天）；已提供范围时原样返回。
// /api/v1/stats 的缺省统计口径。
func fillDefaultStatsRange(tr timeRange, now time.Time, loc *time.Location) timeRange {
	if tr.Since != 0 || tr.Until != 0 {
		return tr
	}
	tr.SinceDay = now.In(loc).AddDate(0, 0, -6).Format(dayInputFormat)
	if t, err := time.ParseInLocation(dayInputFormat, tr.SinceDay, loc); err == nil {
		tr.Since = t.UnixMilli()
	}
	tr.UntilDay = now.In(loc).Format(dayInputFormat)
	if t, err := time.ParseInLocation(dayInputFormat, tr.UntilDay, loc); err == nil {
		tr.Until = t.AddDate(0, 0, 1).UnixMilli()
	}
	return tr
}

// successRate 计算成功率：succeeded / (succeeded + failed)，无终态时为 0
// （与 store 统计 DAO 的口径一致，展示用）。
func successRate(succeeded, failed int) float64 {
	if done := succeeded + failed; done > 0 {
		return float64(succeeded) / float64(done)
	}
	return 0
}

// errorRate 计算失败率，与 successRate 使用相同的终态分母。
func errorRate(succeeded, failed int) float64 {
	if done := succeeded + failed; done > 0 {
		return float64(failed) / float64(done)
	}
	return 0
}

// dirSize 汇总目录树字节数；目录不存在返回 0（尽力统计，不可读条目跳过）。
func dirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			total += fi.Size()
		}
		return nil
	})
	return total
}
