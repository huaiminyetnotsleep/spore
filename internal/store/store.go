package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"

	sqlite "modernc.org/sqlite"
	lib "modernc.org/sqlite/lib"
)

// ErrNotFound 表示按主键定位的目标行不存在（或 UPDATE/DELETE 未命中），
// 用于区分"没有数据"与存储故障。
var ErrNotFound = errors.New("store: record not found")

// executor 抽象 *sql.DB 与 *sql.Tx 的公共执行接口，
// 使各 DAO 方法既能在连接池上运行，也能绑定到事务（见 Tx）。
type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Store 是 SQLite 存储层入口：持有连接并承载各聚合的 DAO 方法
// （见同包 users.go / requests.go 等文件）。Store 可被多个 goroutine 并发使用；
// 事务视图（Tx 传入的 Store）上的 DAO 读写与外层共享唯一连接。
type Store struct {
	db  *sql.DB  // 连接池：仅供迁移与开启事务使用；DAO 一律经 ex 执行
	ex  executor // 实际执行入口：默认是 db 本身，事务视图内是 *sql.Tx
	log *slog.Logger
}

// Open 打开（必要时创建）path 处的 SQLite 数据库并执行版本化迁移。
// 打开或迁移失败返回经 apperr 分类的错误，由调用方决定是否退出进程。
func Open(ctx context.Context, path string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, apperr.Wrap(apperr.CodeStoreUnavailable, fmt.Errorf("打开数据库 %s: %w", path, err))
	}
	// SQLite 是单写者：限定单连接从根上消除写锁竞争（SQLITE_BUSY），
	// 对容量目标（≤100 用户、约 5000 请求/日）不构成瓶颈。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	// sql.Open 是惰性的，先 Ping 让"文件无法打开"这类错误在此处
	// 被归类为 STORE_UNAVAILABLE，而不是混进迁移失败。
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, apperr.Wrap(apperr.CodeStoreUnavailable, fmt.Errorf("连接数据库 %s: %w", path, err))
	}

	s := &Store{db: db, ex: db, log: log}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	log.Info("数据库已就绪", "path", path, "version", len(migrations))
	return s, nil
}

// Tx 在单个事务内执行 fn：fn 收到一个绑定到该事务的 Store 视图，
// 其上的 DAO 读写都参与该事务；fn 返回错误即回滚，否则提交。
// 供"检查与扣减必须同事务"的准入类操作使用（数据库规范）。
//
// 约束：事务内禁止网络调用或长等待（SQLite 单写者，见 dsn 注释）；
// 也不得在 fn 内再使用外层 Store 的方法——连接池只有一条连接，
// 而它正被本事务占用，外层调用会一直阻塞等待连接释放。
func (s *Store) Tx(ctx context.Context, fn func(tx *Store) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB("开始事务", err)
	}
	defer tx.Rollback() // Commit 成功后 Rollback 是无害 no-op

	view := &Store{db: s.db, ex: tx, log: s.log}
	if err := fn(view); err != nil {
		return err
	}
	return wrapDB("提交事务", tx.Commit())
}

// Close 关闭底层连接池；重复调用安全。
func (s *Store) Close() error {
	return s.db.Close()
}

// dsn 把数据库文件路径转为带连接级 PRAGMA 的 DSN。
// PRAGMA 挂在 DSN 查询参数上而非一次性 Exec：database/sql 的连接池
// 会为每条新连接应用这些参数，避免 per-connection 设置在换连接后丢失。
func dsn(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path // 路径异常时按原样交给驱动报错
	}
	q := make(url.Values)
	q.Add("_pragma", "busy_timeout(5000)")  // 写锁短暂竞争时等待而非立即失败
	q.Add("_pragma", "journal_mode(WAL)")   // WAL：读写不互斥，崩溃安全
	q.Add("_pragma", "foreign_keys(1)")     // 启用 requests.user_id 外键约束
	q.Add("_pragma", "synchronous(NORMAL)") // WAL 下的推荐档位
	return abs + "?" + q.Encode()
}

// wrapDB 把底层 SQL 错误归类为 STORE_UNAVAILABLE；err 为 nil 时返回 nil。
func wrapDB(op string, err error) error {
	if err == nil {
		return nil
	}
	return apperr.Wrap(apperr.CodeStoreUnavailable, fmt.Errorf("%s: %w", op, err))
}

// nowMillis 返回 Unix 毫秒时间戳，是本包全部时间字段的统一格式。
func nowMillis() int64 { return time.Now().UnixMilli() }

// nullStr 把空字符串归一为 NULL（可空文本列的写入约定）。
func nullStr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// nullInt64 把 0 归一为 NULL（可空整数列的写入约定）。
func nullInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// affected 统一处理 UPDATE/DELETE 结果：底层错误归类为 STORE_UNAVAILABLE，
// 命中 0 行归一为 ErrNotFound。
func affected(res sql.Result, err error, op string) error {
	if err != nil {
		return wrapDB(op, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return wrapDB(op, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// isConstraintErr 判断底层错误是否为 SQLite 约束冲突（主键/唯一/外键）。
// 扩展结果码的高 8 位是子码，取低 8 位主码比对。
func isConstraintErr(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code()&0xFF == lib.SQLITE_CONSTRAINT
}
