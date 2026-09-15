package web

// 缓存补写（"转存缓存频道"）API：
//   - POST /api/v1/requests/{id}/dump-backfill：单条补写；
//   - POST /api/v1/requests/dump-backfill-batch：批量补写（1–100 条），
//     公共逻辑见 dumpBackfill——请求级资格与特权入队在
//     internal/access.DumpBackfill（绕过用户配额/频率/去重），缓存频道
//     全局配置（是否配置）在本层判定（dump_disabled），判定与 worker 侧
//     dumpcache 的 channelID 闭包同源（LoadEffectiveDumpChannelID）。
// 补写任务不向用户发送任何消息，仅写缓存频道副本并落 dump_entries。
// 全部走 apiAuth/apiCSRF 与统一 JSON 信封；错误文案受控。

import (
	"context"
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// dumpSkipDisabled 是缓存频道全局状态类跳过原因（与
// docs/reference/api.md skip_reason 枚举对齐；请求级三类见
// access.DumpBackfillSkip*）。
const (
	dumpSkipDisabled     = "dump_disabled"
	maxDumpBackfillBatch = 100
)

// dumpBackfill 是单条与批量端点的公共补写逻辑。检查顺序与 skip_reason
// 语义一致：请求存在 → 终态 → 缓存频道已配置 → 无已有副本。返回 error
// 仅表示存储故障（批量端点此时中止并返回 503，已建行不回滚）。
func (s *Server) dumpBackfill(ctx context.Context, requestID int64) (dumpBackfillOutcome, error) {
	skip, err := s.access.DumpBackfillEligibility(ctx, requestID)
	if err != nil {
		return dumpBackfillOutcome{}, err
	}
	if skip != "" {
		return dumpBackfillOutcome{SkipReason: skip}, nil
	}
	// 配置读取失败回落环境变量（LoadEffectiveDumpChannelID 语义），不会
	// 把存储故障误判成"未配置"
	if LoadEffectiveDumpChannelID(ctx, s.st, s.cfg.DumpChannelID) == 0 {
		return dumpBackfillOutcome{SkipReason: dumpSkipDisabled}, nil
	}
	out, err := s.access.DumpBackfill(ctx, "admin", requestID)
	if err != nil {
		return dumpBackfillOutcome{}, err
	}
	// 事务内复核的竞态跳过（如并发补写命中 already_dumped）同样透传
	return dumpBackfillOutcome{
		CreatedRequestID: out.CreatedRequestID,
		SkipReason:       out.SkipReason,
		QueueFull:        out.QueueFull,
	}, nil
}

// dumpBackfillOutcome 是单条补写的公共执行结果：SkipReason 非空表示未建行
// （单条端点转错误信封、批量端点转 skip_reason）；QueueFull 表示已建行但
// 入队失败，行标记 failed(QUEUE_FULL)，可经现有重试入口重试（保留 dump
// 投递方式）。
type dumpBackfillOutcome struct {
	CreatedRequestID int64
	SkipReason       string
	QueueFull        bool
}

// writeDumpBackfillSkipErr 把单条补写的跳过原因映射为错误信封
// （状态码语义见 docs/reference/api.md「缓存补写」）。
func (s *Server) writeDumpBackfillSkipErr(w http.ResponseWriter, r *http.Request, op, skip string) {
	s.log.Warn("缓存补写请求被拒绝", "op", op, "path", r.URL.Path, "skip_reason", skip)
	switch skip {
	case access.DumpBackfillSkipNotFound:
		s.writeAPIAppErr(w, r, op, store.ErrNotFound)
	case access.DumpBackfillSkipNotFinished:
		writeAPIError(w, http.StatusConflict, string(apperr.CodeStoreConstraint),
			"该请求尚未结束，仅终态请求可转存缓存频道。")
	case access.DumpBackfillSkipAlreadyDumped:
		writeAPIError(w, http.StatusConflict, string(apperr.CodeStoreConstraint),
			"缓存频道已有该链接的副本，无需重复转存。")
	case dumpSkipDisabled:
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
			"缓存频道未配置，请先在系统设置中配置缓存频道。")
	default:
		s.writeAPIAppErr(w, r, op, apperr.New(apperr.CodeInternal, "未知缓存补写跳过原因: "+skip))
	}
}

// handleAPIRequestDumpBackfill 单条补写（认证 + CSRF）。
func (s *Server) handleAPIRequestDumpBackfill(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.dump_backfill"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	out, err := s.dumpBackfill(r.Context(), id)
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	if out.SkipReason != "" {
		s.writeDumpBackfillSkipErr(w, r, op, out.SkipReason)
		return
	}
	if out.QueueFull {
		// 行已创建并标记 failed(QUEUE_FULL)，可经现有重试入口重试
		s.log.Warn("缓存补写入队时队列已满", "op", op, "request_id", id,
			"created_request_id", out.CreatedRequestID)
		writeAPIError(w, http.StatusServiceUnavailable, string(apperr.CodeQueueFull),
			apiUserMessage(string(apperr.CodeQueueFull)))
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		CreatedRequestID int64 `json:"created_request_id"`
	}{apiWriteOK{OK: true}, out.CreatedRequestID})
}

// apiDumpBackfillBatchItem 是批量补写的逐条摘要：
// 创建成功带 created_request_id；资格不满足带 skip_reason；
// 入队失败（队列饱和）带 created_request_id 且 queue_full=true。
type apiDumpBackfillBatchItem struct {
	RequestID        int64  `json:"request_id"`
	CreatedRequestID int64  `json:"created_request_id,omitempty"`
	SkipReason       string `json:"skip_reason,omitempty"`
	QueueFull        bool   `json:"queue_full,omitempty"`
}

// handleAPIRequestsDumpBackfillBatch 批量补写（认证 + CSRF）：与单条端点
// 相同的资格校验与建行入队逻辑，逐条独立执行、互不回滚；复用队列容量控制
// （入队失败逐条标记 QUEUE_FULL）。存储故障中止整个响应（已建行保持有效）。
func (s *Server) handleAPIRequestsDumpBackfillBatch(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.requests.dump_backfill_batch"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	var in struct {
		RequestIDs []int64 `json:"request_ids"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if len(in.RequestIDs) == 0 || len(in.RequestIDs) > maxDumpBackfillBatch {
		s.apiBadRequest(w, r, op, "请选择 1 到 100 条请求记录。")
		return
	}
	for _, id := range in.RequestIDs {
		if id <= 0 {
			s.apiBadRequest(w, r, op, "请求 ID 必须为正整数。")
			return
		}
	}
	ctx := r.Context()
	results := make([]apiDumpBackfillBatchItem, 0, len(in.RequestIDs))
	for _, id := range in.RequestIDs {
		item := apiDumpBackfillBatchItem{RequestID: id}
		out, err := s.dumpBackfill(ctx, id)
		if err != nil {
			// 存储故障：中止响应（503），此前已建行的补写任务保持有效
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		item.SkipReason = out.SkipReason
		item.CreatedRequestID = out.CreatedRequestID
		item.QueueFull = out.QueueFull
		results = append(results, item)
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Results []apiDumpBackfillBatchItem `json:"results"`
	}{apiWriteOK{OK: true}, results})
}
