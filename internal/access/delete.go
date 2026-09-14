package access

import (
	"context"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// 记录删除（Web 管理端触发的清理操作）：requests 行只记录执行事实，
// 硬删除即彻底移除统计口径中的该行。为避免与 worker 竞态
// （内存队列仍持有 RequestID，行被删除后终态写入只会失败告警），
// 两类删除都只接受无未完成行的目标。

// DeleteRequest 硬删除单条请求记录，写审计。拒绝分支：
//   - 请求不存在：store.ErrNotFound（调用方转"目标不存在"）；
//   - 状态非终态（queued/processing）：STORE_CONSTRAINT。
//
// cancelled 也是已结束状态，沿用终态记录可清理的既有规则。
func (s *Service) DeleteRequest(ctx context.Context, actor string, requestID int64) error {
	return s.store.Tx(ctx, func(tx *store.Store) error {
		r, err := tx.GetRequest(ctx, requestID)
		if err != nil {
			return err
		}
		if r.Status != store.RequestSucceeded && r.Status != store.RequestFailed && r.Status != store.RequestCancelled {
			return apperr.Wrap(apperr.CodeStoreConstraint,
				fmt.Errorf("请求 %d 状态为 %s，未结束的记录不可删除", requestID, r.Status))
		}
		if err := tx.DeleteRequest(ctx, requestID); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, store.AuditEntry{
			Actor:  actor,
			Action: "request.delete",
			Target: fmt.Sprintf("request:%d", requestID),
			BeforeJSON: mustJSON(map[string]any{
				"status": r.Status, "user_id": r.UserID,
				"channel_key": r.ChannelKey, "message_id": r.MessageID,
			}),
		})
	})
}

// DeleteOutcome 是请求批量删除的逐条结果。
type DeleteOutcome struct {
	RequestID int64
	Result    string
	ErrorCode string
}

// DeleteMany 按输入顺序逐条删除请求记录；单条失败不回滚其他项。
func (s *Service) DeleteMany(ctx context.Context, actor string, requestIDs []int64) []DeleteOutcome {
	out := make([]DeleteOutcome, 0, len(requestIDs))
	for _, id := range requestIDs {
		item := DeleteOutcome{RequestID: id}
		err := s.DeleteRequest(ctx, actor, id)
		switch {
		case err == nil:
			item.Result = "deleted"
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

// DeleteAuditEntries 原子删除明确给出的审计行，并留下不复制旧快照的清理审计。
func (s *Service) DeleteAuditEntries(ctx context.Context, actor string, ids []int64) (int, error) {
	deleted := 0
	err := s.store.Tx(ctx, func(tx *store.Store) error {
		var err error
		deleted, err = tx.DeleteAuditByIDs(ctx, ids)
		if err != nil {
			return err
		}
		return tx.AppendAudit(ctx, store.AuditEntry{
			At:     s.now().UnixMilli(),
			Actor:  actor,
			Action: "audit.delete",
			Target: "audit_log",
			AfterJSON: mustJSON(map[string]any{
				"requested": len(ids), "deleted": deleted, "ids": ids,
			}),
		})
	})
	return deleted, err
}

// ClearAudit 原子清除全部旧审计，并保留一条本次清理记录。
func (s *Service) ClearAudit(ctx context.Context, actor string) (int, error) {
	deleted := 0
	err := s.store.Tx(ctx, func(tx *store.Store) error {
		var err error
		deleted, err = tx.DeleteAllAudit(ctx)
		if err != nil {
			return err
		}
		return tx.AppendAudit(ctx, store.AuditEntry{
			At:     s.now().UnixMilli(),
			Actor:  actor,
			Action: "audit.clear",
			Target: "audit_log",
			AfterJSON: mustJSON(map[string]any{
				"deleted": deleted, "retained_cleanup_record": true,
			}),
		})
	})
	return deleted, err
}

// DeleteChannelRequests 硬删除频道的全部请求记录（频道是 requests 的纯聚合，
// 删行即从统计中移除该频道），写审计（含删除前的聚合快照）。拒绝分支：
//   - 频道无任何记录：store.ErrNotFound；
//   - 仍有未完成请求：STORE_CONSTRAINT。
//
// 返回删除行数。
func (s *Service) DeleteChannelRequests(ctx context.Context, actor string, channelKey string) (int, error) {
	deleted := 0
	err := s.store.Tx(ctx, func(tx *store.Store) error {
		stats, err := tx.ListChannelStats(ctx, store.StatsFilter{ChannelKey: channelKey})
		if err != nil {
			return err
		}
		if len(stats) == 0 {
			return store.ErrNotFound
		}
		if unfinished, err := tx.CountUnfinishedByChannel(ctx, channelKey); err != nil {
			return err
		} else if unfinished > 0 {
			return apperr.Wrap(apperr.CodeStoreConstraint,
				fmt.Errorf("频道 %s 有 %d 条未完成请求，不可删除", channelKey, unfinished))
		}
		deleted, err = tx.DeleteRequestsByChannel(ctx, channelKey)
		if err != nil {
			return err
		}
		c := stats[0]
		return tx.AppendAudit(ctx, store.AuditEntry{
			Actor:  actor,
			Action: "channel.delete",
			Target: "channel:" + channelKey,
			BeforeJSON: mustJSON(map[string]any{
				"requests": c.Total, "succeeded": c.Succeeded, "failed": c.Failed,
			}),
			AfterJSON: mustJSON(map[string]any{"deleted": deleted}),
		})
	})
	return deleted, err
}
