package store

import (
	"context"
	"fmt"
)

// BackupTo 用 SQLite 在线备份（VACUUM INTO）把当前数据库写入 destPath，
// 生成某一时刻的一致快照。导出内容仅业务数据库：
// session.json / peers.json 不在数据库文件内，天然不随备份走。
//
// 约束：目标文件必须不存在（SQLite 对 VACUUM INTO 的硬性要求），
// 由调用方以随机临时文件名保证；VACUUM 不能在事务内运行，
// 因此本方法只在连接池（非事务视图）上调用。
func (s *Store) BackupTo(ctx context.Context, destPath string) error {
	if _, err := s.ex.ExecContext(ctx, "VACUUM INTO ?", destPath); err != nil {
		return wrapDB("备份数据库", fmt.Errorf("VACUUM INTO: %w", err))
	}
	return nil
}
