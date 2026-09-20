package access

import (
	"context"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// 受控重试：把 failed 请求重置回 queued 并重新入队。
// 放在本包而非 queue：重试需要读用户状态、写审计（store）并入队（queue），
// access 已同时依赖两者，不产生新的循环。

// MaxRequestAttempts 是单个请求的累计尝试上限（含首次）。
const MaxRequestAttempts = 3

// Retry 对失败请求执行受控重试（Web 管理端触发）。拒绝分支各自返回明确的
// apperr 错误码：
//   - 请求不存在：store.ErrNotFound（调用方转"目标不存在"）；
//   - 状态非 failed：STORE_CONSTRAINT（与现有数据冲突）；
//   - 累计尝试已达上限：RETRY_EXHAUSTED；
//   - 所属用户未启用：USER_DISABLED；
//   - 内存队列已满：QUEUE_FULL（含事务提交后入队失败的竞态收尾）。
//
// 重试不扣减额度（原请求入队时已扣），attempt+1 复用同一行并写审计。
func (s *Service) Retry(ctx context.Context, actor string, requestID int64) error {
	req, err := s.store.GetRequest(ctx, requestID)
	if err != nil {
		return err
	}
	ref, ok := RefFromRequest(req)
	if !ok {
		// 行数据异常，无法重建来源链接：不动行、只报内部错误
		return apperr.New(apperr.CodeInternal,
			fmt.Sprintf("请求 %d 的频道键无法重建来源链接", requestID))
	}

	now := s.now().UnixMilli()
	err = s.store.Tx(ctx, func(tx *store.Store) error {
		// 事务内复核（单连接下事务即互斥），防止与并发的另一次重试、
		// 或期间发生的状态变更竞态导致重复入队/超上限
		r, err := tx.GetRequest(ctx, requestID)
		if err != nil {
			return err
		}
		if err := checkRetryable(ctx, tx, r); err != nil {
			return err
		}
		// 队列满在事务内预检（与 Submit 第 6 步同款系统性保护）
		if s.queue.Full() {
			return apperr.New(apperr.CodeQueueFull, "内存队列已满")
		}
		if err := tx.RetryRequest(ctx, requestID, now); err != nil {
			return err
		}
		return tx.AppendAudit(ctx, store.AuditEntry{
			Actor:      actor,
			Action:     "request.retry",
			Target:     fmt.Sprintf("request:%d", requestID),
			BeforeJSON: mustJSON(map[string]any{"status": r.Status, "attempt": r.Attempt, "error_code": r.ErrorCode}),
			AfterJSON:  mustJSON(map[string]any{"status": store.RequestQueued, "attempt": r.Attempt + 1}),
		})
	})
	if err != nil {
		return err
	}

	// 事务提交后重新入队（复用 Submit 的提交后入队模式）：
	// 私聊回传目标即用户本人（Bot 私聊 chat ID 与 User ID 相同）。
	// 云盘请求的目的地名称在 requests 行上，重试时据此恢复任务路由
	// （裸链接行为零值不变）；缓存补写行（delivery_mode=dump，如队列满
	// 标记 QUEUE_FULL 后的重试）必须保留 DumpOnly 路由，否则会误走普通
	// 投递路径向用户重发消息。
	job := queue.NewJob(req.UserID, req.UserID, ref, 0, requestID)
	job.CloudDest = req.CloudDestination
	job.DumpOnly = req.DeliveryMode == store.DeliveryModeDump
	// 重试任务带不上原占位提示（原占位在失败收尾时已删除，进程中断场景则
	// 遗留为冻结的旧进度）：标记补发，worker 认领后向用户补一条占位，重试
	// 进度才能在 Bot 里实时展示。仅缓存补写任务保持静默（全程不打扰用户，
	// 与首次执行一致）。
	job.NeedsStatusPrompt = !job.DumpOnly
	// 受理 bot 归属随行恢复：保持 0 会让重试回退主 bot 投递——受理自池 bot
	// 的任务重试后媒体与提示会改由主 bot 发出（用户未 /start 主 bot 时投递
	// 直接失败），频道副本与脚注同样错归。
	job.BotID = req.BotID
	if err := s.queue.Enqueue(job); err != nil {
		// 队列满竞态：与 Submit 同款收尾，行标 failed(QUEUE_FULL)，
		// 本次 attempt 已递增（额度口径不受影响——重试本就不扣减）
		s.log.Warn("重试事务提交后入队失败（队列已满）", "request_id", requestID)
		if ferr := s.store.FinishRequest(ctx, requestID, store.RequestResult{
			Status:    store.RequestFailed,
			ErrorCode: string(apperr.CodeQueueFull),
		}); ferr != nil {
			s.log.Error("标记重试队列满状态未落库", "request_id", requestID, "error", ferr.Error())
		}
		return apperr.New(apperr.CodeQueueFull, "重试入队时队列已满")
	}
	s.log.Info("请求已重试入队",
		"request_id", requestID, "job_id", job.ID, "user_id", req.UserID, "attempt", req.Attempt+1)
	return nil
}

// checkRetryable 校验重试前置条件（三条拒绝分支）。st 是当前执行视图：
// 事务内调用时必须传事务视图（tx），避免在单连接被事务占用时另起查询。
func checkRetryable(ctx context.Context, st *store.Store, r store.Request) error {
	if r.Status != store.RequestFailed {
		return apperr.Wrap(apperr.CodeStoreConstraint,
			fmt.Errorf("请求 %d 状态为 %s，仅 failed 可重试", r.ID, r.Status))
	}
	if r.Attempt >= MaxRequestAttempts {
		return apperr.New(apperr.CodeRetryExhausted,
			fmt.Sprintf("请求 %d 已尝试 %d 次（上限 %d）", r.ID, r.Attempt, MaxRequestAttempts))
	}
	u, err := st.GetUser(ctx, r.UserID)
	if err != nil {
		return err // 请求行外键约束下用户行必在，ErrNotFound 仅作防御性透传
	}
	if u.Status != store.UserEnabled {
		return apperr.Wrap(apperr.CodeUserDisabled,
			fmt.Errorf("请求 %d 所属用户 %d 状态为 %s", r.ID, u.ID, u.Status))
	}
	return nil
}
