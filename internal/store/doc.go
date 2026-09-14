// Package store 提供 SQLite 持久化：连接管理、版本化迁移与各聚合 DAO
// （users / requests / usage_daily / audit_log / events / settings / web_sessions）。
//
// 约定：
//   - 数据库文件为 DATA_DIR/spore.db，驱动 modernc.org/sqlite（纯 Go、无 CGO），
//     WAL 模式、busy_timeout 5000ms、外键约束开启、单连接（SQLite 单写者）。
//   - 时间字段统一为 Unix 毫秒时间戳（int64）；0 表示"尚未发生"，写入时存 NULL，
//     读取时 NULL 归一为 0。本包不使用字符串时间。
//   - 可空文本（username、note 等）以空字符串等价 NULL，读取时 NULL 归一为 ""。
//   - 消息正文、caption、媒体本体与任何凭据（Token/Session/手机号）不入库，
//     由 schema 层面直接保证数据范围。
//   - 写入失败必须向上返回经 internal/apperr 分类的错误，不得忽略。
//
// data/session.json、data/peers.json 与 data/tmp/ 仍由 mtproto / media 包
// 以文件方式管理，不属于本包职责，也不随数据库备份导出。
package store
