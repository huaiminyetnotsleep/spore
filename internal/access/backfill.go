package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/errlog"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// 云盘补存：管理端对
// 任意终态请求按原链接重抓取并上传网盘。放在本包与 Retry 同理：需要读请求、
// 写审计（store）并入队（queue），且必须绕过用户配额/频率/去重/并发——
// 管理端动作不占用户额度也不受提交间隔约束（特权入队）。云盘全局状态
//（开关开启、rclone 可用、目的地有效）由 Web 层在调用前判定，本包只负责
// 请求级资格与建行入队。

// 补存逐条跳过原因（与 docs/reference/api.md skip_reason 枚举对齐；cloud_disabled /
// rclone_unavailable 是云盘全局状态，不在本包判定）。
const (
	CloudBackfillSkipNotFound         = "not_found"
	CloudBackfillSkipNotFinished      = "not_finished"
	CloudBackfillSkipTextOnly         = "text_only"
	CloudBackfillSkipAlreadyArchiving = "already_archiving"
)

// CloudBackfillOutcome 是单条补存的执行结果。
type CloudBackfillOutcome struct {
	RequestID        int64  // 原请求 ID
	CreatedRequestID int64  // 新建 cloud 请求行 ID；跳过时为 0（队列满时也已建行）
	SkipReason       string // 空串 = 建行并入队成功；否则为跳过原因枚举
	QueueFull        bool   // 建行后入队失败（队列饱和），行已标记 failed(QUEUE_FULL)
}

// CloudBackfillEligibility 只读预检请求级资格（不含云盘全局状态），返回
// 跳过原因（空串 = 可补存）。供 Web 批量端点在云盘全局判定前先分流。
func (s *Service) CloudBackfillEligibility(ctx context.Context, requestID int64) (string, error) {
	return cloudBackfillSkip(ctx, s.store, requestID)
}

// CloudBackfill 对终态请求执行云盘补存：事务内复核资格（单连接下事务即
// 互斥，防并发重复建行）→ 新建 delivery_mode=cloud 的请求行（沿用原
// user/ref/chat，parent_request_id 指向原行）→ 写审计；提交后特权入队
// （绕过配额/频率/去重，复用 Retry 的提交后入队模式）。入队失败把新行
// 标记 failed(QUEUE_FULL)（可经现有重试入口重试），不作为 error 返回。
// error 仅表示存储故障；资格不满足经 Outcome.SkipReason 表达。
func (s *Service) CloudBackfill(ctx context.Context, actor string, requestID int64, cloudDest string) (CloudBackfillOutcome, error) {
	// 先在事务外读取并重建来源链接（行数据异常时不动库、只报内部错误，
	// 与 Retry 同款防御）；事务内以快照复核资格。
	req, err := s.store.GetRequest(ctx, requestID)
	if errors.Is(err, store.ErrNotFound) {
		return CloudBackfillOutcome{RequestID: requestID, SkipReason: CloudBackfillSkipNotFound}, nil
	}
	if err != nil {
		return CloudBackfillOutcome{}, err
	}
	ref, ok := RefFromRequest(req)
	if !ok {
		return CloudBackfillOutcome{}, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("请求 %d 的频道键无法重建来源链接", requestID))
	}

	now := s.now().UnixMilli()
	out := CloudBackfillOutcome{RequestID: requestID}
	var createdID int64
	err = s.store.Tx(ctx, func(tx *store.Store) error {
		skip, err := cloudBackfillSkip(ctx, tx, requestID)
		if err != nil {
			return err
		}
		if skip != "" {
			out.SkipReason = skip
			return nil
		}
		created, err := tx.CreateRequest(ctx, store.Request{
			UserID:           req.UserID,
			SourceKind:       req.SourceKind,
			ChannelKey:       req.ChannelKey,
			MessageID:        req.MessageID,
			DeliveryMode:     store.DeliveryModeCloud,
			ParentRequestID:  requestID,
			CloudDestination: cloudDest,
			RequestedAt:      now,
			QueuedAt:         now,
		})
		if err != nil {
			return err
		}
		createdID = created.ID
		out.CreatedRequestID = created.ID
		return tx.AppendAudit(ctx, store.AuditEntry{
			Actor:  actor,
			Action: "request.cloud_archive",
			Target: fmt.Sprintf("request:%d", requestID),
			BeforeJSON: mustJSON(map[string]any{
				"status": req.Status, "delivery_mode": req.DeliveryMode,
			}),
			AfterJSON: mustJSON(map[string]any{
				"created_request_id": created.ID,
				"destination":        cloudDest,
				"delivery_mode":      store.DeliveryModeCloud,
			}),
		})
	})
	if err != nil {
		return CloudBackfillOutcome{}, err
	}
	if out.SkipReason != "" || createdID == 0 {
		return out, nil
	}

	// 事务提交后入队：补存无 Bot 占位消息（StatusMsgID=0），结果回传沿用
	// 原用户私聊（与原请求同 chat），worker 完成后发确认文本。
	job := queue.NewJob(req.UserID, req.UserID, ref, 0, createdID)
	job.CloudDest = cloudDest
	if err := s.queue.Enqueue(job); err != nil {
		// 队列满竞态：新行标记 failed(QUEUE_FULL)，可经现有重试入口重试；
		// 云盘请求无论成败保持 cloud 投递标记（列表筛选"网盘"口径完整）
		s.log.Warn("云盘补存入队失败（队列已满）",
			"request_id", createdID, "parent_request_id", requestID)
		out.QueueFull = true
		s.errLog.Record(ctx, errlog.Record{
			Source:    store.ErrorSourceRequest,
			Code:      string(apperr.CodeQueueFull),
			Stage:     "enqueue",
			Severity:  store.ErrorSeverityError,
			Message:   "云盘补存入队失败（队列已满），请求标记失败",
			Context:   map[string]any{"user_id": req.UserID, "parent_request_id": requestID, "channel_key": req.ChannelKey, "message_id": req.MessageID},
			RequestID: createdID,
		})
		if ferr := s.store.FinishRequest(ctx, createdID, store.RequestResult{
			Status:       store.RequestFailed,
			ErrorCode:    string(apperr.CodeQueueFull),
			DeliveryMode: store.DeliveryModeCloud,
		}); ferr != nil {
			s.log.Error("标记云盘补存队列满状态未落库",
				"request_id", createdID, "error", ferr.Error())
		}
		return out, nil
	}
	s.log.Info("云盘补存已入队",
		"request_id", createdID, "parent_request_id", requestID,
		"user_id", req.UserID, "job_id", job.ID)
	return out, nil
}

// cloudBackfillSkip 判定请求级补存资格（st 传事务视图可在事务内复核）：
// 请求存在、已终态（succeeded/failed/cancelled）、非纯文本、无在途补存行。
// 返回跳过原因（空串 = 通过）；error 仅存储故障。
func cloudBackfillSkip(ctx context.Context, st *store.Store, requestID int64) (string, error) {
	r, err := st.GetRequest(ctx, requestID)
	if errors.Is(err, store.ErrNotFound) {
		return CloudBackfillSkipNotFound, nil
	}
	if err != nil {
		return "", err
	}
	switch r.Status {
	case store.RequestSucceeded, store.RequestFailed, store.RequestCancelled:
	default:
		return CloudBackfillSkipNotFinished, nil
	}
	if r.DeliveryMode == store.DeliveryModeText {
		return CloudBackfillSkipTextOnly, nil
	}
	inFlight, err := st.HasUnfinishedCloudBackfill(ctx, requestID)
	if err != nil {
		return "", err
	}
	if inFlight {
		return CloudBackfillSkipAlreadyArchiving, nil
	}
	return "", nil
}
