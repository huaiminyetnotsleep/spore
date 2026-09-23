// Package errlog 是错误日志中心的写入门面：请求管线与 bot 相关环节的
// 错误经 Record 逐条落入 store 的 error_logs 表，供管理端筛选查询根因。
// 与 notify 事件中心互补——events 按 key 合并聚合计数（管要不要通知），
// 本包管"到底发生了什么"（逐条明细 + 原始根因串）。
//
// 语义契约（同 watch_events 的"观测面不是业务面"）：
//   - 尽力而为：写库失败只记 slog.Warn，绝不影响业务调用方；
//   - nil *Service 为合法值（未装配/测试环境），全部方法 no-op；
//   - detail 统一截断（与 requests.error_detail 的 v23 规则一致：300
//     字符 + 省略号），只存错误文本，调用方不得传入凭据/消息正文；
//   - 无归属请求（RequestID=0）的同键错误（来源+错误码+环节）在窗口期
//     内去重，防止生命周期类错误刷库；带 RequestID 的任务错误不节流。
package errlog

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// detailLimit 是 detail 字段的截断上限（字符数），与 queue 包
// errorDetailLimit（requests.error_detail，v23）保持同一规则。
const detailLimit = 300

// throttleWindow 是无归属请求同键错误的去重窗口。
const throttleWindow = time.Minute

// defaultCleanupInterval 是保留清理的执行周期（与 monitor 系统指标
// 清理同量级：按天保留不需要更频繁）。
const defaultCleanupInterval = time.Hour

// Record 是一条错误日志的业务字段。Message 必填（受控中文描述）；
// Source 走 store 白名单；其余字段可零值。
type Record struct {
	Source    string         // 错误域：store.ErrorSource* 常量
	Code      string         // apperr 错误码；空串=未分类
	Stage     string         // 环节名（fetch/send/upload/pin…）；空串=未标注
	Severity  string         // store.ErrorSeverity*；空串回落 error
	Message   string         // 受控中文描述（发生了什么）
	Detail    string         // 原始错误串；服务内截断
	Context   map[string]any // 参数快照（纯 ID/名称类值）
	RequestID int64          // 关联 requests 行；0=无归属请求
}

// Options 配置错误日志服务。Store 为必填；其余为零值缺省。
type Options struct {
	Store           *store.Store
	Log             *slog.Logger
	Now             func() time.Time // 测试注入时钟；缺省 time.Now
	CleanupInterval time.Duration    // 缺省 1 小时
}

// Service 是错误日志写入门面；nil 指针合法（全部方法 no-op）。
type Service struct {
	store *store.Store
	log   *slog.Logger
	now   func() time.Time

	cleanupInterval time.Duration

	mu       sync.Mutex
	lastSeen map[string]time.Time // 无归属请求同键错误的最近记录时间
}

// New 创建错误日志服务。
func New(options Options) *Service {
	if options.Log == nil {
		options.Log = slog.Default()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &Service{
		store:           options.Store,
		log:             options.Log,
		now:             options.Now,
		cleanupInterval: options.CleanupInterval,
		lastSeen:        make(map[string]time.Time),
	}
}

// Record 落一条错误日志（尽力而为）。上下文参数必须可 JSON 序列化；
// 不可序列化时降级为空上下文（不丢整行）。
func (s *Service) Record(ctx context.Context, rec Record) {
	if s == nil || s.store == nil {
		return
	}
	if !s.passThrottle(rec) {
		return
	}
	if len(rec.Context) > 0 {
		if _, err := json.Marshal(rec.Context); err != nil {
			s.log.Warn("错误日志上下文不可序列化，降级为空", "source", rec.Source, "error", err.Error())
			rec.Context = nil
		}
	}
	if _, err := s.store.InsertErrorLog(ctx, store.ErrorLog{
		Source:    rec.Source,
		Code:      rec.Code,
		Stage:     rec.Stage,
		Severity:  rec.Severity,
		Message:   rec.Message,
		Detail:    TruncateDetail(rec.Detail),
		Context:   rec.Context,
		RequestID: rec.RequestID,
	}); err != nil {
		s.log.Warn("错误日志落库失败", "source", rec.Source, "code", rec.Code, "error", err.Error())
	}
}

// passThrottle 判断本条是否放行：带归属请求的错误（诊断价值高）直通；
// 无归属请求的同键错误（source|code|stage）在窗口内只记首条。放行时
// 顺手清理过期键，防 map 无界增长。
func (s *Service) passThrottle(rec Record) bool {
	if rec.RequestID != 0 {
		return true
	}
	key := rec.Source + "|" + rec.Code + "|" + rec.Stage
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, ok := s.lastSeen[key]; ok && now.Sub(at) < throttleWindow {
		return false
	}
	s.lastSeen[key] = now
	if len(s.lastSeen) > 1024 { // 超界时清理全部过期键（窗口短，量级天然有界）
		for k, at := range s.lastSeen {
			if now.Sub(at) >= throttleWindow {
				delete(s.lastSeen, k)
			}
		}
	}
	return true
}

// TruncateDetail 截断根因文本：超长按字符截断加省略号（与 v23
// errorDetailText 同规则）。导出供调用方复用（如 cloud_uploads.detail）。
func TruncateDetail(text string) string {
	if runes := []rune(text); len(runes) > detailLimit {
		return string(runes[:detailLimit]) + "…"
	}
	return text
}

// Run 周期执行保留清理，直到 ctx 取消。清理失败只记 Warn 不终止循环；
// 保留天数每轮从 syscfg 读取（管理端修改后即时生效）。
func (s *Service) Run(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	interval := s.cleanupInterval
	if interval <= 0 {
		interval = defaultCleanupInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Cleanup(ctx)
		}
	}
}

// Cleanup 按当前保留天数删除过期错误日志。启动时由装配层调用一次，
// 之后由 Run 周期执行。
func (s *Service) Cleanup(ctx context.Context) {
	if s == nil || s.store == nil {
		return
	}
	days := syscfg.LoadErrorLogRetentionDays(ctx, s.store)
	cutoff := s.now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	n, err := s.store.DeleteErrorLogsBefore(ctx, cutoff)
	if err != nil {
		s.log.Warn("清理过期错误日志失败", "error", err.Error())
		return
	}
	if n > 0 {
		s.log.Info("已清理过期错误日志", "count", n, "retention_days", days)
	}
}

// FromError 便捷构造：从任意错误提取 apperr 码与根因串（与 queue 的
// errorDetailText 同语义：优先 Cause 链最内层原始文本，未分类错误回落
// 原始 err.Error()，码为 INTERNAL_ERROR）。
func FromError(source, stage, message string, err error, context map[string]any) Record {
	ae := apperr.From(err)
	return Record{
		Source:  source,
		Code:    string(ae.Code),
		Stage:   stage,
		Message: message,
		Detail:  detailOf(ae),
		Context: context,
	}
}

// detailOf 返回错误的根因文本：优先 AppError 的 Cause（保留错误链上下文，
// 如 "FLOOD_WAIT_X: 3000"、Bot API 响应描述），无 cause 时用内部描述，
// 最后回落码字符串——与 queue.errorDetailText（v23）保持同一规则。
func detailOf(err error) string {
	ae := apperr.From(err)
	if ae.Cause != nil {
		return ae.Cause.Error()
	}
	if ae.Message != "" {
		return ae.Message
	}
	return string(ae.Code)
}
