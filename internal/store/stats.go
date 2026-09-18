package store

import (
	"context"
	"database/sql"
	"sort"
	"strconv"
	"strings"
)

// 统计查询 DAO：全部基于 requests 行聚合，
// 不触发任何频道访问。约定：参数化 + 显式列清单 + Unix 毫秒时间；
// 现有索引（user/channel/status 组合）在容量目标（约 5,000 请求/日）下足够，
// 无需预聚合表与补充索引。

// defaultStatsLimit 是未指定 Limit 时聚合列表（排行类查询）的大小。
const defaultStatsLimit = 20

// StatsFilter 是聚合查询的统一筛选条件，零值字段不参与过滤。
// 与 RequestFilter 的区别：聚合按维度分组，Limit/Offset 作用于分组结果。
type StatsFilter struct {
	ChannelKey string // 限定单频道；空 = 全部
	UserID     int64  // 限定单用户；0 = 全部
	BotID      int64  // 限定受理 bot；0 = 全部（含存量行 bot_id=0）
	Since      int64  // requested_at >= Since（Unix 毫秒）
	Until      int64  // requested_at < Until（Unix 毫秒）
	Limit      int    // <=0 时取 defaultStatsLimit
	Offset     int
}

// where 组装筛选条件的 WHERE 片段（含前导空格；无条件时为空串）。
func (f StatsFilter) where() (string, []any) {
	var conds []string
	var args []any
	if f.ChannelKey != "" {
		conds = append(conds, "channel_key = ?")
		args = append(args, f.ChannelKey)
	}
	if f.UserID > 0 {
		conds = append(conds, "user_id = ?")
		args = append(args, f.UserID)
	}
	if f.BotID > 0 {
		conds = append(conds, "bot_id = ?")
		args = append(args, f.BotID)
	}
	if f.Since > 0 {
		conds = append(conds, "requested_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until > 0 {
		conds = append(conds, "requested_at < ?")
		args = append(args, f.Until)
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (f StatsFilter) limit() int {
	if f.Limit <= 0 {
		return defaultStatsLimit
	}
	return f.Limit
}

// ---- 频道聚合----

// ChannelStats 是单个频道的请求聚合。成功率只对终态请求计算：
// succeeded / (succeeded + failed)，无终态时为 0。
type ChannelStats struct {
	ChannelKey    string
	Total         int // 请求量（含未完成）
	Succeeded     int
	Failed        int
	SuccessRate   float64 // [0,1]
	LastRequested int64   // 筛选范围内该频道最近一次请求时间
}

// ListChannelStats 按频道聚合请求统计，按请求量倒序（频道排行，同量按频道键稳定排序）。
func (s *Store) ListChannelStats(ctx context.Context, f StatsFilter) ([]ChannelStats, error) {
	where, args := f.where()
	rows, err := s.ex.QueryContext(ctx, `SELECT channel_key, COUNT(*),
			COALESCE(SUM(status = ?), 0), COALESCE(SUM(status = ?), 0), MAX(requested_at)
		FROM requests`+where+`
		GROUP BY channel_key
		ORDER BY COUNT(*) DESC, channel_key
		LIMIT ? OFFSET ?`,
		append(append([]any{RequestSucceeded, RequestFailed}, args...), f.limit(), f.Offset)...)
	if err != nil {
		return nil, wrapDB("聚合频道统计", err)
	}
	defer rows.Close()
	var out []ChannelStats
	for rows.Next() {
		var c ChannelStats
		if err := rows.Scan(&c.ChannelKey, &c.Total, &c.Succeeded, &c.Failed, &c.LastRequested); err != nil {
			return nil, wrapDB("扫描频道统计行", err)
		}
		c.SuccessRate = successRate(c.Succeeded, c.Failed)
		out = append(out, c)
	}
	return out, wrapDB("遍历频道统计行", rows.Err())
}

// CountChannelStats 统计筛选范围内的频道数量（频道排行分页的总数）。
// 与 ListChannelStats 使用同一 WHERE 组装，保证分页口径一致。
func (s *Store) CountChannelStats(ctx context.Context, f StatsFilter) (int, error) {
	where, args := f.where()
	var n int
	err := s.ex.QueryRowContext(ctx,
		"SELECT COUNT(DISTINCT channel_key) FROM requests"+where, args...).Scan(&n)
	return n, wrapDB("统计频道数", err)
}

// ---- 按日趋势 ----

// TrendPoint 是按日趋势的一个数据点。
type TrendPoint struct {
	Day       string // YYYY-MM-DD（按调用方传入的运营时区偏移对齐当地日）
	Total     int
	Succeeded int
	Failed    int
}

// ListRequestTrend 按日聚合请求趋势，按日期升序。
// utcOffsetSec 是运营时区相对 UTC 的当前偏移（秒），由调用方以
// time.Time.Zone() 从运营时区计算（如 Asia/Shanghai 恒为 8*3600）；
// 含夏令时切换的时区在历史分桶上会有一小时级偏差，容量目标下可接受。
func (s *Store) ListRequestTrend(ctx context.Context, f StatsFilter, utcOffsetSec int) ([]TrendPoint, error) {
	where, args := f.where()
	// 日键 = (requested_at + 偏移) 按秒折算后取 UTC 日期：先把时间轴平移到
	// 当地时区再做整除，保证 00:00 边界与运营时区对齐。
	rows, err := s.ex.QueryContext(ctx, `SELECT strftime('%Y-%m-%d', (requested_at + ?) / 1000, 'unixepoch'),
			COUNT(*), COALESCE(SUM(status = ?), 0), COALESCE(SUM(status = ?), 0)
		FROM requests`+where+`
		GROUP BY 1
		ORDER BY 1`,
		append([]any{int64(utcOffsetSec) * 1000, RequestSucceeded, RequestFailed}, args...)...)
	if err != nil {
		return nil, wrapDB("聚合请求趋势", err)
	}
	defer rows.Close()
	var out []TrendPoint
	for rows.Next() {
		var p TrendPoint
		if err := rows.Scan(&p.Day, &p.Total, &p.Succeeded, &p.Failed); err != nil {
			return nil, wrapDB("扫描趋势行", err)
		}
		out = append(out, p)
	}
	return out, wrapDB("遍历趋势行", rows.Err())
}

// ---- 分布（媒体类型 / 错误码）----

// DistPoint 是分组分布的一个条目。Key 为空串表示"未记录"
// （失败发生在消息转换之前时没有媒体类型可落）。
type DistPoint struct {
	Key   string
	Count int
}

// ListMediaTypeDist 统计请求的媒体类型分布（全部状态；按 Count 倒序）。
func (s *Store) ListMediaTypeDist(ctx context.Context, f StatsFilter) ([]DistPoint, error) {
	where, args := f.where()
	rows, err := s.ex.QueryContext(ctx, `SELECT COALESCE(media_type, ''), COUNT(*)
		FROM requests`+where+`
		GROUP BY 1
		ORDER BY COUNT(*) DESC, 1`,
		args...)
	if err != nil {
		return nil, wrapDB("聚合媒体类型分布", err)
	}
	defer rows.Close()
	return scanDist(rows, "扫描媒体分布行")
}

// ListDeliveryModeDist 统计投递方式分布（全部终态与在途请求；按 Count 倒序）。
// 取值集合固定（upload/text/cloud/reuse/dump 及历史 reference/mixed），
// 无需 Limit 截断。
func (s *Store) ListDeliveryModeDist(ctx context.Context, f StatsFilter) ([]DistPoint, error) {
	where, args := f.where()
	rows, err := s.ex.QueryContext(ctx, `SELECT COALESCE(delivery_mode, ''), COUNT(*)
		FROM requests`+where+`
		GROUP BY 1
		ORDER BY COUNT(*) DESC, 1`,
		args...)
	if err != nil {
		return nil, wrapDB("聚合投递方式分布", err)
	}
	defer rows.Close()
	return scanDist(rows, "扫描投递方式分布行")
}

// ListErrorDist 统计失败请求的错误码分布（仅 status = failed；按 Count 倒序）。
func (s *Store) ListErrorDist(ctx context.Context, f StatsFilter) ([]DistPoint, error) {
	where, args := f.where()
	if where == "" {
		where = " WHERE status = ?"
	} else {
		where += " AND status = ?"
	}
	args = append(args, RequestFailed)
	rows, err := s.ex.QueryContext(ctx, `SELECT COALESCE(error_code, ''), COUNT(*)
		FROM requests`+where+`
		GROUP BY 1
		ORDER BY COUNT(*) DESC, 1`,
		args...)
	if err != nil {
		return nil, wrapDB("聚合错误分布", err)
	}
	defer rows.Close()
	return scanDist(rows, "扫描错误分布行")
}

func scanDist(rows *sql.Rows, op string) ([]DistPoint, error) {
	var out []DistPoint
	for rows.Next() {
		var p DistPoint
		if err := rows.Scan(&p.Key, &p.Count); err != nil {
			return nil, wrapDB(op, err)
		}
		out = append(out, p)
	}
	return out, wrapDB("遍历分布行", rows.Err())
}

// ---- 分布（源媒体 DC）----

// DCTrendPoint 是按日源媒体 DC 分布的一个数据点。
type DCTrendPoint struct {
	Day  string      // YYYY-MM-DD（按调用方传入的运营时区偏移对齐当地日）
	Dist []DistPoint // 当日各 DC 计数；Key 为 DC ID 十进制串，空串 = 未记录
}

// ListDCDist 统计请求源媒体所在 Telegram DC 的分布（全部状态；按 Count 倒
// 序、Key 升序）。一条请求涉及多个 DC 时在每个 DC 各计一次；空数组/NULL
// 计入空串键（未记录）。DC 以 JSON 数组落列（无法 SQL GROUP BY），沿用
// Top 用户排行的内存聚合模式，由筛选范围控制扫描规模。
func (s *Store) ListDCDist(ctx context.Context, f StatsFilter) ([]DistPoint, error) {
	where, args := f.where()
	rows, err := s.ex.QueryContext(ctx,
		`SELECT source_media_dc_ids_json FROM requests`+where, args...)
	if err != nil {
		return nil, wrapDB("聚合 DC 分布", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var dcJSON sql.NullString
		if err := rows.Scan(&dcJSON); err != nil {
			return nil, wrapDB("扫描 DC 分布行", err)
		}
		ids, err := decodeDCIDs(dcJSON)
		if err != nil {
			return nil, wrapDB("解析请求媒体 DC", err)
		}
		countDCRow(counts, ids)
	}
	if err := wrapDB("遍历 DC 分布行", rows.Err()); err != nil {
		return nil, err
	}
	return dcPointsByCount(counts), nil
}

// ListDCTrend 按日聚合源媒体 DC 分布，按日期升序；日内条目按 Key 升序、
// 空串（未记录）固定末位，保证堆叠柱状图序列跨日颜色稳定。
// utcOffsetSec 语义同 ListRequestTrend。
func (s *Store) ListDCTrend(ctx context.Context, f StatsFilter, utcOffsetSec int) ([]DCTrendPoint, error) {
	where, args := f.where()
	rows, err := s.ex.QueryContext(ctx, `SELECT strftime('%Y-%m-%d', (requested_at + ?) / 1000, 'unixepoch'),
			source_media_dc_ids_json
		FROM requests`+where+`
		ORDER BY 1`,
		append([]any{int64(utcOffsetSec) * 1000}, args...)...)
	if err != nil {
		return nil, wrapDB("聚合 DC 日分布", err)
	}
	defer rows.Close()

	var out []DCTrendPoint
	counts := make(map[string]int)
	for rows.Next() {
		var day string
		var dcJSON sql.NullString
		if err := rows.Scan(&day, &dcJSON); err != nil {
			return nil, wrapDB("扫描 DC 日分布行", err)
		}
		ids, err := decodeDCIDs(dcJSON)
		if err != nil {
			return nil, wrapDB("解析请求媒体 DC", err)
		}
		if len(out) == 0 || out[len(out)-1].Day != day {
			if len(out) > 0 {
				out[len(out)-1].Dist = dcPointsByKey(counts)
			}
			counts = make(map[string]int)
			out = append(out, DCTrendPoint{Day: day})
		}
		countDCRow(counts, ids)
	}
	if err := wrapDB("遍历 DC 日分布行", rows.Err()); err != nil {
		return nil, err
	}
	if len(out) > 0 {
		out[len(out)-1].Dist = dcPointsByKey(counts)
	}
	return out, nil
}

// countDCRow 把一行解码出的 DC 计入计数：空数组/NULL 计入空串键（未记录），
// 跨多 DC 的请求在每个 DC 各计一次。
func countDCRow(counts map[string]int, ids []int) {
	if len(ids) == 0 {
		counts[""]++
		return
	}
	for _, id := range ids {
		counts[strconv.Itoa(id)]++
	}
}

// dcPointsByCount 计数映射 → 分布条目：Count 倒序、Key 升序（与媒体/错误分布一致）。
func dcPointsByCount(counts map[string]int) []DistPoint {
	out := make([]DistPoint, 0, len(counts))
	for key, count := range counts {
		out = append(out, DistPoint{Key: key, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// dcPointsByKey 计数映射 → 单日趋势条目：Key 升序、空串（未记录）末位。
func dcPointsByKey(counts map[string]int) []DistPoint {
	out := make([]DistPoint, 0, len(counts))
	for key, count := range counts {
		out = append(out, DistPoint{Key: key, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Key == "") != (out[j].Key == "") {
			return out[j].Key == ""
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// ---- 用户用量口径（区别于 usage_daily 额度口径）----

// UserRequestStats 是按用户聚合的请求统计。
type UserRequestStats struct {
	UserID        int64
	Total         int
	Succeeded     int
	Failed        int
	LastRequested int64 // 筛选范围内该用户最近一次请求时间
}

// ListUserRequestStats 按用户聚合请求统计，按请求量倒序（用量排行）。
// 用户名等展示信息由调用方（Web）以 ≤100 的用户表在内存中关联。
func (s *Store) ListUserRequestStats(ctx context.Context, f StatsFilter) ([]UserRequestStats, error) {
	where, args := f.where()
	rows, err := s.ex.QueryContext(ctx, `SELECT user_id, COUNT(*),
			COALESCE(SUM(status = ?), 0), COALESCE(SUM(status = ?), 0), MAX(requested_at)
		FROM requests`+where+`
		GROUP BY user_id
		ORDER BY COUNT(*) DESC, user_id
		LIMIT ? OFFSET ?`,
		append(append([]any{RequestSucceeded, RequestFailed}, args...), f.limit(), f.Offset)...)
	if err != nil {
		return nil, wrapDB("聚合用户用量", err)
	}
	defer rows.Close()
	var out []UserRequestStats
	for rows.Next() {
		var u UserRequestStats
		if err := rows.Scan(&u.UserID, &u.Total, &u.Succeeded, &u.Failed, &u.LastRequested); err != nil {
			return nil, wrapDB("扫描用户用量行", err)
		}
		out = append(out, u)
	}
	return out, wrapDB("遍历用户用量行", rows.Err())
}

// ---- 机器人聚合----

// BotStats 是按受理 bot 聚合的请求统计。BotID=0（存量行/非 Bot 通道创建）
// 照常返回一行，由调用方展示为"未知"，保证总量对得上。
type BotStats struct {
	BotID         int64
	BotUsername   string // 受理时快照的代表值（同一 bot 用户名变更时取字典序最大，仅展示用）
	Total         int
	Succeeded     int
	Failed        int
	LastRequested int64
}

// ListBotStats 按受理 bot 聚合请求统计，按请求量倒序（bot 排行）。
// 受 StatsFilter 其余条件（时间/频道/用户）筛选。
func (s *Store) ListBotStats(ctx context.Context, f StatsFilter) ([]BotStats, error) {
	where, args := f.where()
	rows, err := s.ex.QueryContext(ctx, `SELECT bot_id, COALESCE(MAX(bot_username), ''), COUNT(*),
			COALESCE(SUM(status = ?), 0), COALESCE(SUM(status = ?), 0), MAX(requested_at)
		FROM requests`+where+`
		GROUP BY bot_id
		ORDER BY COUNT(*) DESC, bot_id
		LIMIT ? OFFSET ?`,
		append(append([]any{RequestSucceeded, RequestFailed}, args...), f.limit(), f.Offset)...)
	if err != nil {
		return nil, wrapDB("聚合机器人统计", err)
	}
	defer rows.Close()
	var out []BotStats
	for rows.Next() {
		var b BotStats
		if err := rows.Scan(&b.BotID, &b.BotUsername, &b.Total, &b.Succeeded, &b.Failed, &b.LastRequested); err != nil {
			return nil, wrapDB("扫描机器人统计行", err)
		}
		out = append(out, b)
	}
	return out, wrapDB("遍历机器人统计行", rows.Err())
}

// ChannelBotStats 是频道内按受理 bot 的请求分布条目。
type ChannelBotStats struct {
	BotID       int64
	BotUsername string
	Total       int
	Succeeded   int
	Failed      int
}

// ListChannelBotStats 聚合指定频道内各 bot 的请求分布，按请求量倒序；
// 频道的其他统计维度（时间筛选）由 f 携带，ChannelKey 必须非空。
func (s *Store) ListChannelBotStats(ctx context.Context, f StatsFilter) ([]ChannelBotStats, error) {
	where, args := f.where()
	rows, err := s.ex.QueryContext(ctx, `SELECT bot_id, COALESCE(MAX(bot_username), ''), COUNT(*),
			COALESCE(SUM(status = ?), 0), COALESCE(SUM(status = ?), 0)
		FROM requests`+where+`
		GROUP BY bot_id
		ORDER BY COUNT(*) DESC, bot_id`,
		append([]any{RequestSucceeded, RequestFailed}, args...)...)
	if err != nil {
		return nil, wrapDB("聚合频道机器人分布", err)
	}
	defer rows.Close()
	var out []ChannelBotStats
	for rows.Next() {
		var b ChannelBotStats
		if err := rows.Scan(&b.BotID, &b.BotUsername, &b.Total, &b.Succeeded, &b.Failed); err != nil {
			return nil, wrapDB("扫描频道机器人分布行", err)
		}
		out = append(out, b)
	}
	return out, wrapDB("遍历频道机器人分布行", rows.Err())
}

// ---- 管理概览总量----

// RequestTotals 是筛选范围内的请求总量与结果分布。
type RequestTotals struct {
	Total       int
	Succeeded   int
	Failed      int
	Unfinished  int // queued + processing
	ActiveUsers int // 范围内提交过请求的去重用户数
}

// RequestTotals 汇总筛选范围内的请求总量与活跃用户数（无分组、不分页）。
func (s *Store) RequestTotals(ctx context.Context, f StatsFilter) (RequestTotals, error) {
	where, args := f.where()
	var t RequestTotals
	err := s.ex.QueryRowContext(ctx, `SELECT COUNT(*),
			COALESCE(SUM(status = ?), 0), COALESCE(SUM(status = ?), 0),
			COALESCE(SUM(status IN (?, ?)), 0), COUNT(DISTINCT user_id)
		FROM requests`+where,
		append([]any{RequestSucceeded, RequestFailed, RequestQueued, RequestProcessing}, args...)...).
		Scan(&t.Total, &t.Succeeded, &t.Failed, &t.Unfinished, &t.ActiveUsers)
	return t, wrapDB("汇总请求总量", err)
}

// successRate 计算成功率：succeeded / (succeeded + failed)，无终态时为 0。
func successRate(succeeded, failed int) float64 {
	if done := succeeded + failed; done > 0 {
		return float64(succeeded) / float64(done)
	}
	return 0
}

// CountRequestsByStatus 统计各执行状态的请求数（Web 总览页队列指标）：
// queued 是已入队待处理、processing 是 worker 处理中。
func (s *Store) CountRequestsByStatus(ctx context.Context) (queued, processing int, err error) {
	err = s.ex.QueryRowContext(ctx, `SELECT
			COALESCE(SUM(status = ?), 0), COALESCE(SUM(status = ?), 0)
		FROM requests`, RequestQueued, RequestProcessing).
		Scan(&queued, &processing)
	return queued, processing, wrapDB("统计各状态请求数", err)
}
