package access

// reply_anchor.go — 引用回复交互的访问层入口：/pin、/cancel 回复 bot 消息
// 时，先经 ResolveOwnSentMessage 把消息坐标（bot_id+chat_id+message_id，
// 坐标 bot 私有）反查回请求行（归属不匹配与坐标未命中同样返回
// ErrNotFound，不泄露他人请求的存在性），再走取消/置顶分支。
// RecordStatusMessage 供 botapi 提交链顺手落库占位消息坐标（本包是
// botapi 与 store 之间的既有门面，botapi 不直接持库）。

import (
	"context"
	"errors"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// RecordStatusMessage 落库一条进度占位消息坐标（kind=status）。提交链在
// Access.Submit 成功后调用（RequestID 可用时）；尽力而为语义由调用方决定
// （botapi 只记日志，不阻断提交回复）。
func (s *Service) RecordStatusMessage(ctx context.Context, botID, chatID, messageID, requestID int64) error {
	return s.store.InsertSentMessages(ctx, []store.SentMessage{{
		RequestID: requestID,
		BotID:     botID,
		ChatID:    chatID,
		MessageID: messageID,
		Kind:      store.SentKindStatus,
	}})
}

// ResolveOwnSentMessage 按坐标反查锚点并取请求行；坐标未命中、请求行
// 缺失或归属不匹配（他人请求）均返回 ErrNotFound。botID 是接收回复的
// bot（多 bot 池：坐标按 bot 隔离，跨 bot 查不到）。
func (s *Service) ResolveOwnSentMessage(ctx context.Context, userID, botID, chatID, messageID int64) (store.SentMessage, store.Request, error) {
	m, err := s.store.FindSentMessage(ctx, botID, chatID, messageID)
	if err != nil {
		return store.SentMessage{}, store.Request{}, err
	}
	r, err := s.store.GetRequest(ctx, m.RequestID)
	if err != nil {
		return store.SentMessage{}, store.Request{}, err
	}
	if r.UserID != userID {
		return store.SentMessage{}, store.Request{}, store.ErrNotFound
	}
	return m, r, nil
}

// CancelOwnByID 取消该用户名下的在途请求（/cancel 回复路径），返回实际
// 取消数（0 或 1）。归属由 user_id 比对限定；请求已自然结束/已取消的
// 竞态（conflict/not_found）返回 0 与 nil，由命令层按请求状态出友好文案
// 而非报错——调用方已先经 ResolveOwnSentMessage 拿到请求状态，通常不
// 会走到这些分支。
func (s *Service) CancelOwnByID(ctx context.Context, userID, requestID int64) (int, error) {
	r, err := s.store.GetRequest(ctx, requestID)
	if err != nil {
		return 0, err
	}
	if r.UserID != userID {
		return 0, store.ErrNotFound
	}
	if err := s.Cancel(ctx, fmt.Sprintf("user:%d", userID), requestID); err != nil {
		if errors.Is(err, store.ErrNotFound) || apperr.From(err).Code == apperr.CodeStoreConstraint {
			return 0, nil
		}
		return 0, err
	}
	return 1, nil
}

// MarkOwnRequestPin 对在途请求补置顶标记（/pin 回复在途任务消息）。
// 状态条件更新见 store.MarkRequestPinQueued；affected=0 表示任务已到
// 终态，由调用方转事后补置顶路径。
func (s *Service) MarkOwnRequestPin(ctx context.Context, userID, requestID int64) (bool, error) {
	return s.store.MarkRequestPinQueued(ctx, userID, requestID)
}
