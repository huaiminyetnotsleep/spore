package store

// sent_messages.go — 引用回复交互锚点：bot 发出的消息坐标到请求的映射。
// /pin、/cancel 支持用户回复（reply）bot 消息触发命令，本表把消息坐标
// （bot_id+chat_id+message_id，坐标 bot 私有）反查回 requests 行。写入
// 只在各发送点顺手记录返回的 message ID（此前即被丢弃），读取只在命令
// 回复路径。kind：status 进度占位 / media 用户私聊投递 / channel_copy
// 绑定频道副本组首（事后补置顶的定位依据）/ failure 失败通知。
// 只存运营坐标，不存正文/媒体/凭据（红线，与 dump_entries 同类）。
// 不做 GC：行极小且与 requests 同生命周期，容量目标内可忽略。

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// 消息类别（sent_messages.kind）。
const (
	SentKindStatus      = "status"       // 提交/重试补发的进度占位提示
	SentKindMedia       = "media"        // 投递到用户私聊的媒体/文本消息
	SentKindChannelCopy = "channel_copy" // 复制到绑定频道的副本组首
	SentKindFailure     = "failure"      // 任务失败通知
)

// SentMessage 是 sent_messages 的行模型。
type SentMessage struct {
	ID        int64
	RequestID int64
	BotID     int64
	ChatID    int64
	MessageID int64
	Kind      string
	CreatedAt int64
}

// InsertSentMessages 批量写入消息坐标（INSERT OR IGNORE：唯一索引
// (bot_id, chat_id, message_id) 去重，重复坐标静默跳过——重试重发的新
// 消息 ID 天然不冲突，防御性重放也不报错）。CreatedAt 为 0 取当前时间。
// 空切片直接返回（发送失败路径不落库）。单条多行 INSERT，无需显式事务。
func (s *Store) InsertSentMessages(ctx context.Context, in []SentMessage) error {
	if len(in) == 0 {
		return nil
	}
	now := nowMillis()
	values := make([]any, 0, len(in)*6)
	placeholders := make([]string, 0, len(in))
	for _, m := range in {
		at := m.CreatedAt
		if at == 0 {
			at = now
		}
		values = append(values, m.RequestID, m.BotID, m.ChatID, m.MessageID, m.Kind, at)
		placeholders = append(placeholders, "(?,?,?,?,?,?)")
	}
	if _, err := s.ex.ExecContext(ctx, `INSERT OR IGNORE INTO sent_messages
		(request_id, bot_id, chat_id, message_id, kind, created_at) VALUES `+
		strings.Join(placeholders, ","), values...); err != nil {
		return wrapDB("写入消息坐标", err)
	}
	return nil
}

// FindSentMessage 按坐标三元组反查锚点行；查无返回 ErrNotFound。
func (s *Store) FindSentMessage(ctx context.Context, botID, chatID, messageID int64) (SentMessage, error) {
	var m SentMessage
	err := s.ex.QueryRowContext(ctx, `SELECT id, request_id, bot_id, chat_id, message_id, kind, created_at
		FROM sent_messages WHERE bot_id=? AND chat_id=? AND message_id=?`, botID, chatID, messageID).
		Scan(&m.ID, &m.RequestID, &m.BotID, &m.ChatID, &m.MessageID, &m.Kind, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return SentMessage{}, ErrNotFound
	}
	if err != nil {
		return SentMessage{}, wrapDB("查询消息坐标", err)
	}
	return m, nil
}

// ListChannelCopiesByRequest 返回请求的绑定频道副本组首坐标（按写入序），
// 供事后补置顶逐目标 PinChatMessage。
func (s *Store) ListChannelCopiesByRequest(ctx context.Context, requestID int64) ([]SentMessage, error) {
	rows, err := s.ex.QueryContext(ctx, `SELECT id, request_id, bot_id, chat_id, message_id, kind, created_at
		FROM sent_messages WHERE request_id=? AND kind=? ORDER BY id`, requestID, SentKindChannelCopy)
	if err != nil {
		return nil, wrapDB("查询频道副本坐标", err)
	}
	defer rows.Close()
	out := make([]SentMessage, 0, 2)
	for rows.Next() {
		var m SentMessage
		if err := rows.Scan(&m.ID, &m.RequestID, &m.BotID, &m.ChatID, &m.MessageID, &m.Kind, &m.CreatedAt); err != nil {
			return nil, wrapDB("扫描频道副本坐标行", err)
		}
		out = append(out, m)
	}
	return out, wrapDB("遍历频道副本坐标行", rows.Err())
}

// MarkRequestPinQueued 对在途请求置顶补标：/pin 回复在途任务的消息时，
// 仅当状态仍为 queued/processing 才置 pin=1（worker 收尾读的是请求行，
// 后补的标记天然被继承）。affected=0 返回 false——状态已迁移到终态，
// 由调用方转事后补置顶或友好提示；user_id 限定归属。
func (s *Store) MarkRequestPinQueued(ctx context.Context, userID, requestID int64) (bool, error) {
	res, err := s.ex.ExecContext(ctx,
		`UPDATE requests SET pin=1 WHERE id=? AND user_id=? AND status IN (?, ?)`,
		requestID, userID, RequestQueued, RequestProcessing)
	if err != nil {
		return false, wrapDB("补标请求置顶", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, wrapDB("读取补标置顶结果", err)
	}
	return n > 0, nil
}

// MarkRequestPin 无条件置 pin=1（user_id 限定归属）：事后补置顶路径用，
// 让补置顶结果能经 SetRequestPinResult（WHERE pin=1）回写，管理端详情
// 与"已置顶"判定随请求行走。
func (s *Store) MarkRequestPin(ctx context.Context, userID, requestID int64) error {
	_, err := s.ex.ExecContext(ctx, `UPDATE requests SET pin=1 WHERE id=? AND user_id=?`, requestID, userID)
	if err != nil {
		return wrapDB("标记请求置顶", err)
	}
	return nil
}
