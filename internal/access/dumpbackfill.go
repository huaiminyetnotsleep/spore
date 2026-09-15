package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// 缓存补写（"转存缓存频道"）：管理端对任意终态请求按原链接重取源消息，
// 直接向缓存频道发送干净副本并落 dump_entries，全程不向原用户发送任何
// 消息。放在本包与 CloudBackfill 同理：需要读请求、写审计（store）并入队
// （queue），且必须绕过用户配额/频率/去重/并发——管理端动作不占用户额度
// 也不受提交间隔约束（特权入队）。缓存频道全局配置（是否配置）由 Web 层
// 在调用前判定（dump_disabled），本包只负责请求级资格与建行入队；worker
// 侧执行时还有一次 dump_entries 复核（覆盖并发窗口）与配置解析。

// 缓存补写逐条跳过原因（与 docs/reference/api.md skip_reason 枚举对齐；
// dump_disabled 是缓存频道全局状态，不在本包判定）。
const (
	DumpBackfillSkipNotFound      = "not_found"
	DumpBackfillSkipNotFinished   = "not_finished"
	DumpBackfillSkipAlreadyDumped = "already_dumped"
)

// DumpBackfillOutcome 是单条缓存补写的执行结果。
type DumpBackfillOutcome struct {
	RequestID        int64  // 原请求 ID
	CreatedRequestID int64  // 新建 dump 请求行 ID；跳过时为 0（队列满时也已建行）
	SkipReason       string // 空串 = 建行并入队成功；否则为跳过原因枚举
	QueueFull        bool   // 建行后入队失败（队列饱和），行已标记 failed(QUEUE_FULL)
}

// DumpBackfillEligibility 只读预检请求级资格，返回跳过原因（空串 = 可补写）。
// 供 Web 层在缓存频道全局判定前先分流。
func (s *Service) DumpBackfillEligibility(ctx context.Context, requestID int64) (string, error) {
	return dumpBackfillSkip(ctx, s.store, requestID)
}

// DumpBackfill 对终态请求执行缓存补写：事务内复核资格（单连接下事务即
// 互斥，防并发重复建行）→ 新建 delivery_mode=dump 的请求行（沿用原
// user/ref/chat，parent_request_id 指向原行）→ 写审计；提交后特权入队
// （绕过配额/频率/去重，复用 CloudBackfill 的提交后入队模式）。入队失败
// 把新行标记 failed(QUEUE_FULL)（可经现有重试入口重试，重试保留 dump
// 投递方式），不作为 error 返回。error 仅表示存储故障；资格不满足经
// Outcome.SkipReason 表达。
func (s *Service) DumpBackfill(ctx context.Context, actor string, requestID int64) (DumpBackfillOutcome, error) {
	// 先在事务外读取并重建来源链接（行数据异常时不动库、只报内部错误，
	// 与 Retry/CloudBackfill 同款防御）；事务内以快照复核资格。
	req, err := s.store.GetRequest(ctx, requestID)
	if errors.Is(err, store.ErrNotFound) {
		return DumpBackfillOutcome{RequestID: requestID, SkipReason: DumpBackfillSkipNotFound}, nil
	}
	if err != nil {
		return DumpBackfillOutcome{}, err
	}
	ref, ok := RefFromRequest(req)
	if !ok {
		return DumpBackfillOutcome{}, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("请求 %d 的频道键无法重建来源链接", requestID))
	}

	now := s.now().UnixMilli()
	out := DumpBackfillOutcome{RequestID: requestID}
	var createdID int64
	err = s.store.Tx(ctx, func(tx *store.Store) error {
		skip, err := dumpBackfillSkip(ctx, tx, requestID)
		if err != nil {
			return err
		}
		if skip != "" {
			out.SkipReason = skip
			return nil
		}
		created, err := tx.CreateRequest(ctx, store.Request{
			UserID:          req.UserID,
			SourceKind:      req.SourceKind,
			ChannelKey:      req.ChannelKey,
			MessageID:       req.MessageID,
			DeliveryMode:    store.DeliveryModeDump,
			ParentRequestID: requestID,
			RequestedAt:     now,
			QueuedAt:        now,
		})
		if err != nil {
			return err
		}
		createdID = created.ID
		out.CreatedRequestID = created.ID
		return tx.AppendAudit(ctx, store.AuditEntry{
			Actor:  actor,
			Action: "request.dump_backfill",
			Target: fmt.Sprintf("request:%d", requestID),
			BeforeJSON: mustJSON(map[string]any{
				"status": req.Status, "delivery_mode": req.DeliveryMode,
			}),
			AfterJSON: mustJSON(map[string]any{
				"created_request_id": created.ID,
				"delivery_mode":      store.DeliveryModeDump,
			}),
		})
	})
	if err != nil {
		return DumpBackfillOutcome{}, err
	}
	if out.SkipReason != "" || createdID == 0 {
		return out, nil
	}

	// 事务提交后入队：补写无 Bot 占位消息（StatusMsgID=0），且全程不向
	// 用户发送任何消息（成功失败都不发，仅落库供 Web 列表查看）。
	job := queue.NewJob(req.UserID, req.UserID, ref, 0, createdID)
	job.DumpOnly = true
	if err := s.queue.Enqueue(job); err != nil {
		// 队列满竞态：新行标记 failed(QUEUE_FULL)，可经现有重试入口重试；
		// dump 行保持 dump 投递标记（列表筛选"缓存补写"口径完整）
		s.log.Warn("缓存补写入队失败（队列已满）",
			"request_id", createdID, "parent_request_id", requestID)
		out.QueueFull = true
		if ferr := s.store.FinishRequest(ctx, createdID, store.RequestResult{
			Status:       store.RequestFailed,
			ErrorCode:    string(apperr.CodeQueueFull),
			DeliveryMode: store.DeliveryModeDump,
		}); ferr != nil {
			s.log.Error("标记缓存补写队列满状态未落库",
				"request_id", createdID, "error", ferr.Error())
		}
		return out, nil
	}
	s.log.Info("缓存补写已入队",
		"request_id", createdID, "parent_request_id", requestID,
		"user_id", req.UserID, "job_id", job.ID)
	return out, nil
}

// dumpBackfillSkip 判定请求级补写资格（st 传事务视图可在事务内复核）：
// 请求存在、已终态（succeeded/failed/cancelled）、缓存频道无同链接副本
// （已有条目跳过——不自愈语义：条目对应消息可能仍有效，重复补写只会
// 浪费队列资源）。返回跳过原因（空串 = 通过）；error 仅存储故障。
func dumpBackfillSkip(ctx context.Context, st *store.Store, requestID int64) (string, error) {
	r, err := st.GetRequest(ctx, requestID)
	if errors.Is(err, store.ErrNotFound) {
		return DumpBackfillSkipNotFound, nil
	}
	if err != nil {
		return "", err
	}
	switch r.Status {
	case store.RequestSucceeded, store.RequestFailed, store.RequestCancelled:
	default:
		return DumpBackfillSkipNotFinished, nil
	}
	if _, err := st.LatestDumpEntry(ctx, r.ChannelKey, r.MessageID); err == nil {
		return DumpBackfillSkipAlreadyDumped, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	return "", nil
}
