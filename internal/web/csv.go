package web

// CSV 导出：消息记录、用户用量与频道统计三处导出，
// 复用页面同款筛选参数；UTF-8 BOM（兼容 Excel）+ encoding/csv 流式写出，
// 行数上限保护（达到上限时置 X-Export-Truncated 并停止），动作写审计。

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// CSV 导出约束。csvMaxRows 用变量承载：测试可临时调小以确定性验证截断保护
// （生产值 10 万行，见下方默认赋值）。
var (
	csvMaxRows   = 100000 // 单次导出行数上限（内存与响应体保护）
	csvBatchSize = 500    // 请求记录分批拉取大小
)

// csvWriter 包装响应写入：BOM 头 + csv 编码器；错误时静默停止
// （客户端断连时继续写没有意义，调用方以返回错误记日志）。
type csvWriter struct {
	w   *csv.Writer
	err error
}

// newCSVResponse 初始化 CSV 响应（UTF-8 BOM）并写表头。
// truncated 表示结果超过行数上限被截断（须在 BOM 写出前确定，随头一并下发）。
func newCSVResponse(w http.ResponseWriter, filename string, truncated bool, header []string) *csvWriter {
	h := w.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	h.Set("X-Content-Type-Options", "nosniff")
	if truncated {
		h.Set("X-Export-Truncated", "true")
	}
	// UTF-8 BOM：Excel 依赖它识别编码。BOM 写出即提交响应头，
	// 因此截断等元信息必须在调用前确定。
	if _, err := w.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		return &csvWriter{err: err}
	}
	cw := &csvWriter{w: csv.NewWriter(w)}
	cw.row(header)
	return cw
}

// row 写一行；出错后变为 no-op（错误保留供调用方判断）。
func (c *csvWriter) row(fields []string) {
	if c.err != nil {
		return
	}
	if err := c.w.Write(fields); err != nil {
		c.err = err
	}
}

// flush 刷新缓冲并返回首个错误。
func (c *csvWriter) flush() error {
	if c.err != nil {
		return c.err
	}
	c.w.Flush()
	return c.w.Error()
}

// csvBadParam 响应 CSV 导出的参数错误（400 纯文本受控文案）。
// 当前 SSR 结果页已删除；导出是下载端点而非页面，错误不用 HTML 表达，
// 文案为固定受控值，不回显请求参数。
func (s *Server) csvBadParam(w http.ResponseWriter, r *http.Request, op string) {
	s.log.Warn("导出请求参数非法", "op", op, "path", r.URL.Path)
	http.Error(w, apiUserMessage(apiCodeBadRequest), http.StatusBadRequest)
}

// csvServerError 记录日志并响应 500 纯文本受控文案（不透出内部细节）。
func (s *Server) csvServerError(w http.ResponseWriter, r *http.Request, op string, err error) {
	ae := apperr.From(err)
	s.log.Error("导出处理失败", "op", op, "path", r.URL.Path, "code", ae.Code, "error", err.Error())
	http.Error(w, "操作未能完成，请稍后重试（详情见服务日志）。", http.StatusInternalServerError)
}

// handleRequestsCSV 导出消息记录：筛选参数与 /api/v1/requests 同源
// （buildRequestFilter 单一组装入口）。
func (s *Server) handleRequestsCSV(w http.ResponseWriter, r *http.Request, sess session) {
	ctx := r.Context()
	filter, _, _, err := buildRequestFilter(r, s.tz(ctx))
	if err != nil {
		s.csvBadParam(w, r, "导出消息记录")
		return
	}
	// 先统计总数判定截断（头必须在 BOM 写出前确定）
	total, err := s.st.CountRequests(ctx, filter)
	if err != nil {
		s.csvServerError(w, r, "统计导出行数", err)
		return
	}
	truncated := total > csvMaxRows
	limit := total
	if limit > csvMaxRows {
		limit = csvMaxRows
	}

	loc := s.tz(ctx)
	cw := newCSVResponse(w, "requests.csv", truncated, []string{
		"ID", "用户ID", "频道标识", "消息ID", "来源", "状态", "尝试次数", "错误码",
		"媒体类型", "源媒体DC", "投递方式", "机器人", "自动置顶", "置顶结果", "文件大小(字节)", "文件名", "请求时间", "完成时间", "耗时(毫秒)", "message_url", "媒体内容类型",
	})
	written := 0
	offset := 0
	for written < limit {
		filter.Limit, filter.Offset = csvBatchSize, offset
		rows, err := s.st.ListRequests(ctx, filter)
		if err != nil {
			// 已开始流式输出，无法改写状态码：记录后终止
			s.log.Error("导出消息记录失败", "error", err.Error())
			return
		}
		if len(rows) == 0 {
			break
		}
		for _, rq := range rows {
			if written >= limit {
				break
			}
			cw.row([]string{
				strconv.FormatInt(rq.ID, 10),
				strconv.FormatInt(rq.UserID, 10),
				rq.ChannelKey,
				strconv.Itoa(rq.MessageID),
				sourceKindText(rq.SourceKind),
				statusText(rq.Status),
				strconv.Itoa(rq.Attempt),
				rq.ErrorCode,
				rq.MediaType,
				sourceMediaDCText(rq.SourceMediaDCIDs),
				deliveryModeText(rq.DeliveryMode),
				botDisplay(rq.BotID, rq.BotUsername),
				pinMark(rq.Pin),
				pinResultText(rq.Pin, rq.PinOK, rq.PinTotal),
				strconv.FormatInt(rq.FileSize, 10),
				rq.FileName,
				fmtTime(rq.RequestedAt, loc),
				fmtTime(rq.FinishedAt, loc),
				strconv.FormatInt(rq.DurationMs, 10),
				requestLink(rq),
				strings.Join(rq.MediaTypes, "+"),
			})
			written++
		}
		if len(rows) < csvBatchSize {
			break
		}
		offset += csvBatchSize
	}
	if err := cw.flush(); err != nil {
		s.log.Warn("消息记录导出流中断", "error", err.Error())
	}
	s.audit(ctx, "export.requests", "requests", map[string]any{
		"rows": written, "truncated": truncated})
}

// botDisplay 渲染受理 bot：@username 优先，空用户名回退数字 ID；
// 0（存量行/非 Bot 通道）留空。
func botDisplay(botID int64, username string) string {
	if botID == 0 {
		return ""
	}
	if username != "" {
		return "@" + username
	}
	return strconv.FormatInt(botID, 10)
}

// pinMark 渲染自动置顶标记列：pin 任务显示"是"，其余留空。
func pinMark(pin bool) string {
	if pin {
		return "是"
	}
	return ""
}

// pinResultText 渲染置顶结果列：非 pin 行留空；pin 行为"成功/总数"
// （0/0 表示未产生副本——纯文本任务或完成时无绑定）。
func pinResultText(pin bool, ok, total int) string {
	if !pin {
		return ""
	}
	return fmt.Sprintf("%d/%d", ok, total)
}

// sourceKindText 把来源类型转为中文。
func sourceKindText(k string) string {
	if k == store.SourcePrivate {
		return "私有频道"
	}
	if k == store.SourcePublic {
		return "公开频道"
	}
	return k
}

// deliveryModeText 把投递方式标记转为中文（与 SPA DELIVERY_MODE_LABELS 同源语义：
// 引用 / 上传 / 混合 / 文本 / 复用 / 网盘）；未知取值原样输出（statusText 的回退惯例）。
func sourceMediaDCText(ids []int) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("DC %d", id)
	}
	return strings.Join(parts, ", ")
}

func deliveryModeText(mode string) string {
	switch mode {
	case store.DeliveryModeReference:
		return "引用直发"
	case store.DeliveryModeUpload:
		return "媒体投递"
	case store.DeliveryModeMixed:
		return "混合投递"
	case store.DeliveryModeText:
		return "文本投递"
	case store.DeliveryModeReuse:
		return "缓存直发"
	case store.DeliveryModeCloud:
		return "网盘转存"
	case store.DeliveryModeDump:
		return "缓存补写"
	case store.DeliveryModeSplit:
		return "分卷投递"
	default:
		return mode
	}
}

// handleChannelsCSV 导出频道统计：时间范围筛选与 /api/v1/channels 同源
// （parseTimeRange 单一解析入口）。
func (s *Server) handleChannelsCSV(w http.ResponseWriter, r *http.Request, sess session) {
	ctx := r.Context()
	tr, err := parseTimeRange(r, s.tz(ctx))
	if err != nil {
		s.csvBadParam(w, r, "导出频道统计")
		return
	}
	// 多取一行判定是否超上限（截断标头须在 BOM 写出前确定）
	stats, err := s.st.ListChannelStats(ctx, store.StatsFilter{
		Since: tr.Since, Until: tr.Until, Limit: csvMaxRows + 1,
	})
	if err != nil {
		s.csvServerError(w, r, "聚合频道统计", err)
		return
	}
	truncated := len(stats) > csvMaxRows
	if truncated {
		stats = stats[:csvMaxRows]
	}
	loc := s.tz(ctx)
	cw := newCSVResponse(w, "channels.csv", truncated, []string{
		"频道标识", "请求量", "成功数", "失败数", "成功率", "最近请求时间",
	})
	for _, c := range stats {
		rate := "—"
		if done := c.Succeeded + c.Failed; done > 0 {
			rate = strconv.FormatFloat(float64(c.Succeeded)/float64(done)*100, 'f', 1, 64) + "%"
		}
		cw.row([]string{
			c.ChannelKey,
			strconv.Itoa(c.Total),
			strconv.Itoa(c.Succeeded),
			strconv.Itoa(c.Failed),
			rate,
			fmtTime(c.LastRequested, loc),
		})
	}
	if err := cw.flush(); err != nil {
		s.log.Warn("频道统计导出流中断", "error", err.Error())
	}
	s.audit(ctx, "export.channels", "channels",
		map[string]any{"rows": len(stats), "truncated": truncated})
}

// handleUsersCSV 导出用户用量：请求口径统计 + 当日额度口径用量。
func (s *Server) handleUsersCSV(w http.ResponseWriter, r *http.Request, sess session) {
	ctx := r.Context()
	users, err := s.st.ListUsers(ctx)
	if err != nil {
		s.csvServerError(w, r, "列出用户", err)
		return
	}
	stats, err := s.st.ListUserRequestStats(ctx, store.StatsFilter{Limit: csvMaxRows})
	if err != nil {
		s.csvServerError(w, r, "聚合用户用量", err)
		return
	}
	loc := s.tz(ctx)
	day := s.now().In(loc).Format(dayInputFormat)

	truncated := len(users) > csvMaxRows
	if truncated {
		users = users[:csvMaxRows]
	}
	cw := newCSVResponse(w, "users.csv", truncated, []string{
		"用户ID", "用户名", "显示名", "状态", "owner", "备注",
		"累计请求数", "成功数", "失败数", "当日已用", "每日额度", "最近请求时间",
	})
	byID := make(map[int64]store.UserRequestStats, len(stats))
	for _, st := range stats {
		byID[st.UserID] = st
	}
	ownerMark := func(b bool) string {
		if b {
			return "是"
		}
		return "否"
	}
	for _, u := range users {
		st := byID[u.ID]
		usage, err := s.st.GetUsage(ctx, u.ID, day)
		if err != nil {
			s.log.Warn("读取用户当日用量失败", "user_id", u.ID, "error", err.Error())
		}
		cw.row([]string{
			strconv.FormatInt(u.ID, 10),
			u.Username,
			u.DisplayName,
			statusText(u.Status),
			ownerMark(u.IsOwner),
			u.Note,
			strconv.Itoa(st.Total),
			strconv.Itoa(st.Succeeded),
			strconv.Itoa(st.Failed),
			strconv.Itoa(usage.Used),
			strconv.Itoa(u.DailyLimit),
			fmtTime(st.LastRequested, loc),
		})
	}
	if err := cw.flush(); err != nil {
		s.log.Warn("用户用量导出流中断", "error", err.Error())
	}
	s.audit(ctx, "export.users", "users", map[string]any{"rows": len(users)})
}
