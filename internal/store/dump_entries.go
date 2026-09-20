package store

// dump_entries.go — 转存频道复用条目：每次成功投递同步写入 bot 自有缓存
// 频道的"干净副本"（无脚注 caption）消息坐标。重复链接复用时取同链接最新
// 条目，经 copyMessages 从缓存频道整条复制到目标聊天（服务端复制不受媒体
// 大小限制，相册保组）。只存频道内消息坐标，非正文/媒体/凭据（红线）。
//
// FormatVersion 标记副本布局格式：历史行（版本 0）可能是"相册多成员
// caption"的旧形态，LatestDumpEntry 不再命中——坐标保留供审计，复用回落
// 完整投递后由下次 WriteClean 自愈写入当前版本。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// DumpFormatVersion 是当前缓存副本的布局格式版本。1 = 相册"恰好组首一条
// 合并 caption"布局（2026-09 起，routerSender 发送前归一化 + WriteClean
// 只重写组首）；0 = 历史行（迁移默认值，可能为多 caption 旧形态）。
const DumpFormatVersion = 1

// DumpEntry 是 dump_entries 的行模型。
type DumpEntry struct {
	ID            int64
	ChannelKey    string
	MessageID     int
	DumpIDs       []int // 缓存频道内按序消息 ID（与投递条目一一对应）
	FormatVersion int   // 副本布局格式版本（见 DumpFormatVersion）
	CreatedAt     int64
}

// InsertDumpEntry 写入一条干净副本坐标；DumpIDs 为空属调用方违约（防御）。
// 格式版本恒写当前版本（调用方不参与选择）。
func (s *Store) InsertDumpEntry(ctx context.Context, in DumpEntry) (DumpEntry, error) {
	if len(in.DumpIDs) == 0 {
		return DumpEntry{}, apperr.New(apperr.CodeInternal, "缓存频道条目要求 DumpIDs 非空")
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = nowMillis()
	}
	idsJSON, err := json.Marshal(in.DumpIDs)
	if err != nil {
		return DumpEntry{}, apperr.Wrap(apperr.CodeInternal, fmt.Errorf("编码缓存频道消息 ID: %w", err))
	}
	res, err := s.ex.ExecContext(ctx, `INSERT INTO dump_entries
		(channel_key, message_id, dump_ids_json, format_version, created_at) VALUES (?,?,?,?,?)`,
		in.ChannelKey, in.MessageID, string(idsJSON), DumpFormatVersion, in.CreatedAt)
	if err != nil {
		return DumpEntry{}, wrapDB("写入缓存频道条目", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return DumpEntry{}, wrapDB("读取缓存频道条目 ID", err)
	}
	in.ID = id
	in.FormatVersion = DumpFormatVersion
	return in, nil
}

// LatestDumpEntry 取同链接最新一条**当前格式**的干净副本坐标；无匹配返回
// ErrNotFound。历史格式行（format_version < 当前版本）不命中：旧副本可能
// 是多 caption 形态，复用会把问题带回用户聊天——忽略后下次成功投递自愈。
// 不设时间窗口：副本失效（被删除等）由复制失败在调用方回落兜底。
func (s *Store) LatestDumpEntry(ctx context.Context, channelKey string, messageID int) (DumpEntry, error) {
	var e DumpEntry
	var idsJSON string
	err := s.ex.QueryRowContext(ctx,
		`SELECT id, channel_key, message_id, dump_ids_json, format_version, created_at
		FROM dump_entries WHERE channel_key = ? AND message_id = ? AND format_version = ?
		ORDER BY id DESC LIMIT 1`, channelKey, messageID, DumpFormatVersion).
		Scan(&e.ID, &e.ChannelKey, &e.MessageID, &idsJSON, &e.FormatVersion, &e.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return DumpEntry{}, ErrNotFound
	}
	if err != nil {
		return DumpEntry{}, wrapDB("查询缓存频道条目", err)
	}
	if strings.TrimSpace(idsJSON) == "" {
		return DumpEntry{}, wrapDB("解析缓存频道条目", fmt.Errorf("空消息 ID 数组"))
	}
	if err := json.Unmarshal([]byte(idsJSON), &e.DumpIDs); err != nil {
		return DumpEntry{}, wrapDB("解析缓存频道条目", fmt.Errorf("无效消息 ID 数组: %w", err))
	}
	return e, nil
}
