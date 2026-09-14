package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// CancelOutcome 是批量取消的逐条结果。Result 取 cancelled、conflict、not_found
// 或 failed；ErrorCode 仅用于管理员界面区分受控失败原因。
type CancelOutcome struct {
	RequestID int64
	Result    string
	ErrorCode string
}

// CanCancel 报告请求取消控制器是否已接入。
func (s *Service) CanCancel() bool { return s.canceller != nil }

// Cancel 将 queued/processing 请求原子置为 cancelled，写入 request.cancel 审计，
// 然后向已取出的 worker 发送取消原因。排队任务没有活动 cancel 函数时，出队
// 阶段的 MarkRequestStarted 条件 claim 会阻止它执行。
func (s *Service) Cancel(ctx context.Context, actor string, requestID int64) error {
	if s.canceller == nil {
		return apperr.New(apperr.CodeStoreUnavailable, "请求取消控制器未接入")
	}
	if requestID <= 0 {
		return apperr.New(apperr.CodeStoreConstraint, "请求 ID 必须为正整数")
	}

	at := s.now().UnixMilli()
	err := s.store.Tx(ctx, func(tx *store.Store) error {
		before, err := tx.CancelRequest(ctx, requestID, at)
		if err != nil {
			return err
		}
		return tx.AppendAudit(ctx, store.AuditEntry{
			Actor:  actor,
			Action: "request.cancel",
			Target: fmt.Sprintf("request:%d", requestID),
			BeforeJSON: mustJSON(map[string]any{
				"status": before.Status, "error_code": before.ErrorCode,
				"user_id": before.UserID, "channel_key": before.ChannelKey,
				"message_id": before.MessageID,
			}),
			AfterJSON: mustJSON(map[string]any{
				"status":      store.RequestCancelled,
				"error_code":  string(apperr.CodeRequestCancelled),
				"finished_at": at,
			}),
		})
	})
	if err != nil {
		return err
	}

	// 数据库状态先成功，再发送信号。信号发送是幂等的：排队任务未命中，
	// 活动任务重复命中也只会把同一个 context 标记为取消。
	s.canceller.CancelRequest(requestID)
	return nil
}

// CancelOwnByLink 取消该用户名下与链接匹配（channel_key + message_id）的全部
// queued/processing 请求，返回实际取消数。归属由查询的 user_id 限定保证；
// 审计 actor 记为 "user:<id>"，与 Web 管理端的 "admin" 区分。
// 列表与取消之间任务自然结束的竞态（conflict/not_found）不计入数量。
func (s *Service) CancelOwnByLink(ctx context.Context, userID int64, ref tmeurl.SourceRef) (int, error) {
	requests, err := s.store.ListUnfinishedByUserAndLink(ctx, userID, ChannelKey(ref), ref.MessageID)
	if err != nil {
		return 0, err
	}
	actor := fmt.Sprintf("user:%d", userID)
	cancelled := 0
	for _, r := range requests {
		err := s.Cancel(ctx, actor, r.ID)
		if err == nil {
			cancelled++
			continue
		}
		// 已结束/已取消/已被删除的竞态按未取消计；存储故障向上返回由调用方转用户文案
		if !errors.Is(err, store.ErrNotFound) && apperr.From(err).Code != apperr.CodeStoreConstraint {
			return cancelled, err
		}
	}
	return cancelled, nil
}

// CancelMany 按调用方明确给出的 ID 顺序逐条取消。单条冲突或不存在不影响
// 其他条目；存储故障也保留在对应条目的受控结果中，便于管理员追踪。
func (s *Service) CancelMany(ctx context.Context, actor string, requestIDs []int64) []CancelOutcome {
	out := make([]CancelOutcome, 0, len(requestIDs))
	for _, id := range requestIDs {
		item := CancelOutcome{RequestID: id}
		err := s.Cancel(ctx, actor, id)
		switch {
		case err == nil:
			item.Result = "cancelled"
		case errors.Is(err, store.ErrNotFound):
			item.Result = "not_found"
		case apperr.From(err).Code == apperr.CodeStoreConstraint:
			item.Result = "conflict"
			item.ErrorCode = string(apperr.CodeStoreConstraint)
		default:
			item.Result = "failed"
			item.ErrorCode = string(apperr.From(err).Code)
		}
		out = append(out, item)
	}
	return out
}
