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
	DumpChannelID int64 // 副本所在缓存频道 ID（0 = 升级前存量，查询永不命中）
	FormatVersion int   // 副本布局格式版本（见 DumpFormatVersion）
	CreatedAt     int64
}

// InsertDumpEntry 写入一条干净副本坐标；DumpIDs 为空属调用方违约（防御）。
// 格式版本恒写当前版本（调用方不参与选择）。
func (s *Store) InsertDumpEntry(ctx context.Context, in DumpEntry) (DumpEntry, error) {
	if len(in.DumpIDs) == 0 {
		return DumpEntry{}, apperr.New(apperr.CodeInternal, "缓存频道条目要求 DumpIDs 非空")
	}
	if in.DumpChannelID == 0 {
		return DumpEntry{}, apperr.New(apperr.CodeInternal, "缓存频道条目要求 DumpChannelID 非零")
	}
	if in.CreatedAt == 0 {
		in.CreatedAt = nowMillis()
	}
	idsJSON, err := json.Marshal(in.DumpIDs)
	if err != nil {
		return DumpEntry{}, apperr.Wrap(apperr.CodeInternal, fmt.Errorf("编码缓存频道消息 ID: %w", err))
	}
	res, err := s.ex.ExecContext(ctx, `INSERT INTO dump_entries
		(channel_key, message_id, dump_ids_json, dump_channel_id, format_version, created_at) VALUES (?,?,?,?,?,?)`,
		in.ChannelKey, in.MessageID, string(idsJSON), in.DumpChannelID, DumpFormatVersion, in.CreatedAt)
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

// LatestDumpEntry 取同链接最新一条**当前格式**且**属于指定缓存频道**的
// 干净副本坐标；无匹配返回 ErrNotFound。历史格式行（format_version <
// 当前版本）与他频道行（含升级前 dump_channel_id=0 的存量）都不命中：
// 旧副本可能是多 caption 形态或已随旧频道消失，复用会把问题带回用户
// 聊天——忽略后下次成功投递自愈。不设时间窗口：副本失效（被删除等）
// 由复制失败在调用方回落兜底。
func (s *Store) LatestDumpEntry(ctx context.Context, channelKey string, messageID int, dumpChannelID int64) (DumpEntry, error) {
	var e DumpEntry
	var idsJSON string
	err := s.ex.QueryRowContext(ctx,
		`SELECT id, channel_key, message_id, dump_ids_json, dump_channel_id, format_version, created_at
		FROM dump_entries WHERE channel_key = ? AND message_id = ? AND format_version = ? AND dump_channel_id = ?
		ORDER BY id DESC LIMIT 1`, channelKey, messageID, DumpFormatVersion, dumpChannelID).
		Scan(&e.ID, &e.ChannelKey, &e.MessageID, &idsJSON, &e.DumpChannelID, &e.FormatVersion, &e.CreatedAt)
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

// CountDumpEntriesByChannel 统计指定缓存频道的条目数（含格式历史行）。
// 管理端迁移工具用 0 统计升级前存量。
func (s *Store) CountDumpEntriesByChannel(ctx context.Context, dumpChannelID int64) (int64, error) {
	var n int64
	err := s.ex.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dump_entries WHERE dump_channel_id = ?`, dumpChannelID).Scan(&n)
	if err != nil {
		return 0, wrapDB("统计缓存频道条目", err)
	}
	return n, nil
}

// ListDumpEntriesByChannel 分页列出指定缓存频道的**当前格式**条目，
// afterID 起按 id 升序（迁移工具游标）。limit 上限 500。
func (s *Store) ListDumpEntriesByChannel(ctx context.Context, dumpChannelID, afterID int64, limit int) ([]DumpEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	rows, err := s.ex.QueryContext(ctx,
		`SELECT id, channel_key, message_id, dump_ids_json, dump_channel_id, format_version, created_at
		FROM dump_entries WHERE dump_channel_id = ? AND format_version = ? AND id > ?
		ORDER BY id LIMIT ?`, dumpChannelID, DumpFormatVersion, afterID, limit)
	if err != nil {
		return nil, wrapDB("列出缓存频道条目", err)
	}
	defer rows.Close()
	out := []DumpEntry{}
	for rows.Next() {
		var e DumpEntry
		var idsJSON string
		if err := rows.Scan(&e.ID, &e.ChannelKey, &e.MessageID, &idsJSON,
			&e.DumpChannelID, &e.FormatVersion, &e.CreatedAt); err != nil {
			return nil, wrapDB("扫描缓存频道条目行", err)
		}
		if err := json.Unmarshal([]byte(idsJSON), &e.DumpIDs); err != nil {
			return nil, wrapDB("解析缓存频道条目", fmt.Errorf("无效消息 ID 数组: %w", err))
		}
		out = append(out, e)
	}
	return out, wrapDB("遍历缓存频道条目行", rows.Err())
}

// UpdateDumpEntryCopy 迁移工具回写：副本已复制到新缓存频道，更新频道
// 归属与消息 ID（新频道内 ID 全新）。
func (s *Store) UpdateDumpEntryCopy(ctx context.Context, id, dumpChannelID int64, dumpIDs []int) error {
	if len(dumpIDs) == 0 {
		return apperr.New(apperr.CodeInternal, "迁移回写要求 DumpIDs 非空")
	}
	idsJSON, err := json.Marshal(dumpIDs)
	if err != nil {
		return apperr.Wrap(apperr.CodeInternal, fmt.Errorf("编码缓存频道消息 ID: %w", err))
	}
	res, err := s.ex.ExecContext(ctx,
		`UPDATE dump_entries SET dump_channel_id = ?, dump_ids_json = ? WHERE id = ?`,
		dumpChannelID, string(idsJSON), id)
	if err := affected(res, err, "回写缓存频道条目"); err != nil {
		return err
	}
	return nil
}

// DeleteDumpEntry 物理删除一条条目（迁移工具清理被新条目取代的旧行）。
func (s *Store) DeleteDumpEntry(ctx context.Context, id int64) error {
	res, err := s.ex.ExecContext(ctx, `DELETE FROM dump_entries WHERE id = ?`, id)
	if err := affected(res, err, "删除缓存频道条目"); err != nil {
		return err
	}
	return nil
}
