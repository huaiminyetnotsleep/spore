package store

import (
	"context"
	"fmt"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// migrations 是按版本递增的迁移脚本，migrations[i] 对应 user_version = i+1。
// 只增不改：已随历史版本发布的脚本永不修改，新变更一律追加新条目，
// 并保持旧条目逐字节稳定（的升级约定）。
var migrations = []string{
	// v1：产品化基线——七张业务表与请求索引。
	// 刻意不存消息正文、caption 与媒体本体：数据范围由 schema 直接保证。
	`CREATE TABLE users (
	id INTEGER PRIMARY KEY,
	status TEXT NOT NULL,
	is_owner INTEGER NOT NULL DEFAULT 0,
	username TEXT,
	display_name TEXT,
	note TEXT,
	submit_interval_sec INTEGER NOT NULL DEFAULT 10,
	daily_limit INTEGER NOT NULL DEFAULT 50,
	concurrent_limit INTEGER NOT NULL DEFAULT 2,
	created_at INTEGER NOT NULL,
	first_used_at INTEGER,
	last_used_at INTEGER,
	archived_at INTEGER,
	last_denied_at INTEGER,
	last_denied_reason TEXT
);

CREATE TABLE requests (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id),
	source_kind TEXT,
	channel_key TEXT NOT NULL,
	message_id INTEGER NOT NULL,
	status TEXT NOT NULL,
	attempt INTEGER NOT NULL DEFAULT 1,
	error_code TEXT,
	media_type TEXT,
	file_size INTEGER,
	file_name TEXT,
	requested_at INTEGER NOT NULL,
	queued_at INTEGER,
	started_at INTEGER,
	finished_at INTEGER,
	duration_ms INTEGER
);

CREATE INDEX idx_requests_user_requested ON requests(user_id, requested_at);
CREATE INDEX idx_requests_channel_requested ON requests(channel_key, requested_at);
CREATE INDEX idx_requests_status ON requests(status);
CREATE INDEX idx_requests_user_channel_message ON requests(user_id, channel_key, message_id);

CREATE TABLE usage_daily (
	user_id INTEGER NOT NULL,
	day TEXT NOT NULL,
	used INTEGER NOT NULL DEFAULT 0,
	reset_count INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, day)
);

CREATE TABLE audit_log (
	id INTEGER PRIMARY KEY,
	at INTEGER NOT NULL,
	actor TEXT,
	action TEXT NOT NULL,
	target TEXT,
	before_json TEXT,
	after_json TEXT
);

CREATE TABLE events (
	id INTEGER PRIMARY KEY,
	key TEXT NOT NULL UNIQUE,
	severity TEXT NOT NULL,
	message TEXT NOT NULL,
	count INTEGER NOT NULL DEFAULT 1,
	first_at INTEGER NOT NULL,
	last_at INTEGER NOT NULL,
	last_notified_at INTEGER,
	status TEXT NOT NULL DEFAULT 'open'
);

CREATE TABLE settings (
	key TEXT PRIMARY KEY,
	value_json TEXT NOT NULL
);

CREATE TABLE web_sessions (
	id_hash TEXT PRIMARY KEY,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	csrf_token TEXT NOT NULL,
	ip TEXT,
	user_agent TEXT
);`,

	// v2：requests 新增投递方式标记——记录本次请求的媒体最终
	// 经"引用"（Bot API file_id）还是"下载上传"送达。带 DEFAULT 的
	// ADD COLUMN 使旧行自动取 'upload'，即引用功能引入前的历史语义。
	`ALTER TABLE requests ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'upload';`,

	// v3：审计页支持时间范围查询后，为过滤与倒序分页提供组合索引。
	`CREATE INDEX idx_audit_at_id ON audit_log(at, id);`,

	// v4：用户频道绑定——每个用户把机器人拉进自己的频道并设为管理员后
	// 绑定；任务成功后把内容复制到所属用户的绑定频道。channel_id 为
	// Bot API 的频道数字 ID（-100 前缀）作主键，同一频道只归属一个用户；
	// 不存任何凭据或 access hash（数据范围红线），仅存展示所需的
	// username/title。时间 Unix 毫秒。
	`CREATE TABLE channel_bindings (
	channel_id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id),
	username TEXT,
	title TEXT,
	bound_via TEXT NOT NULL DEFAULT 'bot',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE INDEX idx_channel_bindings_user ON channel_bindings(user_id);`,

	// v5：用户级频道绑定数量上限——0 表示未单独配置（跟随角色默认：
	// 普通用户 1、owner 3），1–20 为显式值；在 Web 用户详情页按用户配置。
	`ALTER TABLE users ADD COLUMN bind_limit INTEGER NOT NULL DEFAULT 0;`,

	// v6：频道加入管理（/join 命令与 Web 审批页）——join_requests 记录
	// 用户提交的加入申请（invite_hash 为审批延时执行所必需；UI/审计脱敏，
	// 不入日志），joined_channels 留痕用户号实际加入的频道（来源 join_command
	// = owner 即时加入 / approved = 审批通过 / external = 检测到的外部拉入）。
	// 不存 access_hash 等凭据（数据范围红线）；时间 Unix 毫秒。
	`CREATE TABLE join_requests (
	id INTEGER PRIMARY KEY,
	user_id INTEGER NOT NULL REFERENCES users(id),
	invite_hash TEXT NOT NULL,
	channel_title TEXT,
	participants INTEGER,
	status TEXT NOT NULL,
	requested_at INTEGER NOT NULL,
	reviewed_at INTEGER,
	reviewed_by TEXT,
	note TEXT
);

CREATE INDEX idx_join_requests_status ON join_requests(status, requested_at);
CREATE INDEX idx_join_requests_user ON join_requests(user_id, requested_at);

CREATE TABLE joined_channels (
	channel_id INTEGER PRIMARY KEY,
	title TEXT,
	username TEXT,
	kind TEXT NOT NULL DEFAULT 'channel',
	joined_via TEXT NOT NULL DEFAULT 'external',
	joined_by INTEGER REFERENCES users(id),
	joined_at INTEGER NOT NULL,
	left_at INTEGER
);

CREATE INDEX idx_joined_channels_active ON joined_channels(left_at, joined_at);`,

	// v7：系统资源与传输速率的低频历史采样。探针不可用时对应列为 NULL；
	// sampled_at 使用 Unix 毫秒，并由监控服务按固定周期聚合后 upsert。
	`CREATE TABLE system_metric_samples (
		sampled_at INTEGER PRIMARY KEY,
		rss_bytes INTEGER NULL,
		temp_dir_bytes INTEGER NULL,
		download_bytes_per_second REAL NULL,
		upload_bytes_per_second REAL NULL
	);`,

	// v8：请求记录保存源媒体所在的 Telegram DC ID 去重数组；
	// 纯文本、旧记录或未知媒体保持 NULL/空数组，不回填历史消息。
	`ALTER TABLE requests ADD COLUMN source_media_dc_ids_json TEXT;`,

	// v9：保存请求中实际包含的去重媒体类型。相册的 media_type 统一为 album，
	// media_types_json 用于区分纯图片、纯视频和混合相册；旧记录不回填。
	`ALTER TABLE requests ADD COLUMN media_types_json TEXT;`,

	// v10：/download 云盘下载——cloud_uploads 记录每次网盘上传的目的地、
	// 远端路径、状态、字节数与错误码（只存路径/状态/计数，不存媒体内容
	// 与网盘凭据，数据范围红线）；时间沿用 Unix 毫秒（0 表示尚未发生）。
	// requests.parent_request_id 记录管理端补存指向的原请求（普通请求 NULL），
	// 不设外键——补存目标可能先于本列存在，且补存行只做展示跳转。
	// requests.cloud_destination 记录云盘请求的目的地名称：重试/补存重新入队
	// 时据此恢复 Job.CloudDest（普通请求空串，destination 常量语义）。
	`CREATE TABLE cloud_uploads (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id INTEGER NOT NULL,
	destination TEXT NOT NULL,
	remote_path TEXT NOT NULL,
	file_name TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	error_code TEXT NOT NULL DEFAULT '',
	bytes INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	finished_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_cloud_uploads_request ON cloud_uploads(request_id);

ALTER TABLE requests ADD COLUMN parent_request_id INTEGER;

ALTER TABLE requests ADD COLUMN cloud_destination TEXT NOT NULL DEFAULT '';`,

	// v11：用户级云盘下载权限三态——0 跟随角色默认（owner 允许、普通
	// 用户拒绝），1 显式允许，2 显式拒绝；在 Web 用户详情页按用户配置。
	// 与全局开关（cloud-drive.json enabled）是 AND 关系。
	`ALTER TABLE users ADD COLUMN cloud_download INTEGER NOT NULL DEFAULT 0;`,

	// v12：TG 链接复用（copyMessages 直拷）——requests 记录每次成功投递的
	// 消息坐标：sent_chat_id 为投递目标聊天（私有聊天即用户 ID），
	// sent_message_ids_json 为按发送顺序的已发送消息 ID 数组（相册整组）。
	// 两列是 bot 自己已投递消息的坐标（哪个聊天的哪条消息），非消息正文/
	// caption/媒体本体/凭据/access hash（数据范围红线），与 cloud_uploads
	// 存远端路径同类；worker 据此对重复链接经 Bot API copyMessages 服务端
	// 复制，失败自动回落完整下载上传。复用查询不带 user_id（跨用户复用），
	// 现有 idx_requests_user_channel_message 以 user_id 为前缀不适用，
	// 补 (channel_key, message_id, status) 组合索引。
	`ALTER TABLE requests ADD COLUMN sent_chat_id INTEGER NOT NULL DEFAULT 0;

ALTER TABLE requests ADD COLUMN sent_message_ids_json TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_requests_channel_message_status ON requests(channel_key, message_id, status);`,

	// v13：转存频道统一复用——dump_entries 记录每次成功投递同步写入 bot
	// 自有缓存频道的"干净副本"消息坐标（无脚注 caption，跨用户复用唯一
	// 来源；整条 copyMessages 复制不受媒体大小限制）。只存 bot 自有频道内
	// 的消息坐标，非消息正文/媒体本体/凭据（数据范围红线），与 requests
	// 复用坐标同类。同链接可有多条（新副本覆盖旧副本语义由"取最新"实现）。
	`CREATE TABLE dump_entries (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	channel_key TEXT NOT NULL,
	message_id INTEGER NOT NULL,
	dump_ids_json TEXT NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE INDEX idx_dump_entries_link ON dump_entries(channel_key, message_id, id);`,

	// v14：系统指标增加进程 CPU 占用率采样（REAL NULL，0–100 占全部核心）；
	// 探针不可用时为 NULL，旧采样行不回填。
	`ALTER TABLE system_metric_samples ADD COLUMN cpu_percent REAL NULL;`,

	// v15：多机器人池——requests 记录受理 bot（哪个 bot 收到链接并投递），
	// users 记录来源 bot（首次 /start 经哪个 bot 提交申请）。bot_id 为
	// Telegram bot 账号的数字 ID（getMe），bot_username 为受理时的用户名
	// 快照（展示自持，bot 移出池后历史记录仍可读）。0/空 = 存量行或非
	// Bot 通道创建（Web 手动添加用户），展示为"—"。不存 token（数据范围
	// 红线：凭据只在 env 与 bots.json）。
	`ALTER TABLE requests ADD COLUMN bot_id INTEGER NOT NULL DEFAULT 0;

ALTER TABLE requests ADD COLUMN bot_username TEXT NOT NULL DEFAULT '';

ALTER TABLE users ADD COLUMN source_bot_id INTEGER NOT NULL DEFAULT 0;

ALTER TABLE users ADD COLUMN source_bot_username TEXT NOT NULL DEFAULT '';`,

	// v16：dump_entries 缓存副本格式版本——相册 caption 布局修复后副本
	// canonical 形态变为"恰好组首一条合并 caption"（相册多成员 caption 会
	// 被客户端抑制组级展示位），历史副本（默认版本 0）可能仍是多 caption
	// 旧形态，复用会把问题带回用户聊天：LatestDumpEntry 只命中当前版本，
	// 历史坐标保留供审计，复用回落完整投递后由 WriteClean 自愈重写。
	// 只存整数版本号，非正文/caption（数据范围红线不变）。
	`ALTER TABLE dump_entries ADD COLUMN format_version INTEGER NOT NULL DEFAULT 0;`,

	// v17：监听源预热缓存（watch_sources，/watch 指令 + Web 管理端）——
	// 管理端配置或用户申请（可审批）的源频道/超级群组，bot 以管理员身份
	// 接收新帖并自动转储缓存频道预热 dump_entries，重复链接直接命中复用。
	// channel_id 为 Bot API -100 形态主键；username 供公开源双键复用
	//（t.me/username 与 t.me/c 两种链接形态都命中）。status 区分用户申请
	//（pending 待审批 / approved / rejected）；added_by=0 为管理员 Web 直接
	// 添加（天然 approved），>0 为申请人用户 ID（无 FK：0 语义不是用户行）。
	// enabled 是独立暂停开关（approved 但暂停监听）。
	// 注意：本条必须保持 2026-09-21 首次发布的字节形态——有部署在首个
	// 形态上启动过（缺 kind/bot 列），后续增量一律走 v18+ 追加。
	`CREATE TABLE watch_sources (
channel_id INTEGER PRIMARY KEY,
username TEXT,
title TEXT,
status TEXT NOT NULL,
enabled INTEGER NOT NULL DEFAULT 1,
added_by INTEGER NOT NULL DEFAULT 0,
reviewed_by TEXT,
created_at INTEGER NOT NULL,
updated_at INTEGER NOT NULL
);`,

	// v18：监听源结构收尾（部分部署的 v17 库缺列/缺表）。重建 watch_sources
	// 为最终形态——INSERT 只引用 v17 全形态共有的列，兼容缺 kind、缺
	// bot_id/bot_username 或两者皆缺的中间结构（既有行保留，新增列取默认
	// 值）；watch_events 用 IF NOT EXISTS 幂等创建（新库 v17 后本迁移同样
	// 适用，两种路径收敛到同一最终结构）。
	`CREATE TABLE watch_sources_v18 (
channel_id INTEGER PRIMARY KEY,
kind TEXT NOT NULL DEFAULT '',
username TEXT,
title TEXT,
status TEXT NOT NULL,
enabled INTEGER NOT NULL DEFAULT 1,
added_by INTEGER NOT NULL DEFAULT 0,
bot_id INTEGER NOT NULL DEFAULT 0,
bot_username TEXT NOT NULL DEFAULT '',
reviewed_by TEXT,
created_at INTEGER NOT NULL,
updated_at INTEGER NOT NULL
);

INSERT INTO watch_sources_v18 (channel_id, username, title, status, enabled, added_by, reviewed_by, created_at, updated_at)
SELECT channel_id, username, title, status, enabled, added_by, reviewed_by, created_at, updated_at FROM watch_sources;

DROP TABLE watch_sources;

ALTER TABLE watch_sources_v18 RENAME TO watch_sources;

CREATE TABLE IF NOT EXISTS watch_events (
id INTEGER PRIMARY KEY AUTOINCREMENT,
channel_id INTEGER NOT NULL,
username TEXT NOT NULL DEFAULT '',
title TEXT NOT NULL DEFAULT '',
message_id INTEGER NOT NULL,
member_ids_json TEXT NOT NULL DEFAULT '',
dump_ids_json TEXT NOT NULL DEFAULT '',
request_id INTEGER NOT NULL DEFAULT 0,
bot_id INTEGER NOT NULL DEFAULT 0,
bot_username TEXT NOT NULL DEFAULT '',
path TEXT NOT NULL,
created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_watch_events_channel ON watch_events(channel_id, id);`,

	// v19：私有邀请链接监听申请——watch_invite_requests 保存 /watch 私有
	// invite hash 的异步处理状态。user_id=0 为管理员 Web 路径（与
	// watch_sources.added_by 同约定，无数据库外键）；enabled 是独立监听
	// 开关（由调用方显式传值，服务层用户路径传 true、管理员可预录入
	// false 停用态），participants/reviewed_by/note 支撑管理列表展示与
	// 审批留痕。完整 hash 只在活动阶段用于 Telegram/Bot 协作，进入终态
	// 后由服务层清理；masked_hash 独立保留供 API 安全展示。channel_* 是
	// 邀请解析成功后的频道快照，bot_* 是受理 bot 快照。
	`CREATE TABLE watch_invite_requests (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id INTEGER NOT NULL DEFAULT 0,
	invite_hash TEXT,
	masked_hash TEXT NOT NULL,
	status TEXT NOT NULL,
	channel_id INTEGER NOT NULL DEFAULT 0,
	kind TEXT NOT NULL DEFAULT '',
	username TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	participants INTEGER NOT NULL DEFAULT 0,
	enabled INTEGER NOT NULL DEFAULT 1,
	reviewed_by TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	bot_id INTEGER NOT NULL DEFAULT 0,
	bot_username TEXT NOT NULL DEFAULT '',
	requested_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
	);

	CREATE INDEX idx_watch_invite_requests_active
	ON watch_invite_requests(status, requested_at, id);

	CREATE INDEX idx_watch_invite_requests_user_active
	ON watch_invite_requests(user_id, status, requested_at, id);

	CREATE INDEX idx_watch_invite_requests_hash_active
	ON watch_invite_requests(invite_hash, status);`,

	// v20：自动置顶——requests.pin 标记任务成功后是否需要在用户绑定的
	// 频道/群组置顶副本组首（/pin <链接> 单次指定或用户 auto_pin 偏好），
	// pin_ok/pin_total 由 worker 收尾回写置顶结果（成功数/参与置顶的目标
	// 总数，供管理端详情展示）；users.auto_pin 是用户级偏好开关。
	`ALTER TABLE requests ADD COLUMN pin INTEGER NOT NULL DEFAULT 0;
ALTER TABLE requests ADD COLUMN pin_ok INTEGER NOT NULL DEFAULT 0;
ALTER TABLE requests ADD COLUMN pin_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN auto_pin INTEGER NOT NULL DEFAULT 0;`,

	// v21：绑定路由到 bot——channel_bindings.bot_id 记录绑定经哪台 bot
	// 建立并通过硬校验（Bot /bind 指令路径为接收命令的 bot；Web 管理端
	// 无 bot 上下文存 0）。副本/置顶投递按受理 bot 路由：bot_id > 0 的
	// 绑定只接受该 bot 受理的任务（消息坐标 bot 私有，跨 bot 不可复制，
	// 乱投必然失败）；bot_id = 0 为通配（历史行与 Web 绑定），任意受理
	// bot 均尝试投递、失败优雅降级。
	`ALTER TABLE channel_bindings ADD COLUMN bot_id INTEGER NOT NULL DEFAULT 0;`,

	// v22：引用回复交互锚点——sent_messages 记录 bot 发出的消息坐标到
	// 请求的映射（kind：status 进度占位 / media 用户私聊投递 / channel_copy
	// 绑定频道副本组首 / failure 失败通知），供 /pin、/cancel 回复消息时
	// 反查对应请求（bot_id+chat_id+message_id 三元组定位，坐标 bot 私有）。
	// 只存运营坐标，不存正文/媒体/凭据（红线，与 dump_entries 同类）。
	// 写入用 INSERT OR IGNORE：唯一索引去重，重试重发的新消息 ID 天然不冲突。
	`CREATE TABLE sent_messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	request_id INTEGER NOT NULL,
	bot_id INTEGER NOT NULL,
	chat_id INTEGER NOT NULL,
	message_id INTEGER NOT NULL,
	kind TEXT NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE UNIQUE INDEX idx_sent_messages_msg
ON sent_messages(bot_id, chat_id, message_id);

CREATE INDEX idx_sent_messages_request
ON sent_messages(request_id);`,
}

// migrate 把数据库推进到 migrations 的最新版本，幂等：已应用的版本跳过。
// 每个版本在独立事务内执行，PRAGMA user_version 与 DDL 同事务提交，
// 中途崩溃时事务回滚，重启后从断点安全重放。
func (s *Store) migrate(ctx context.Context) error {
	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return wrapDB("读取数据库版本", err)
	}
	if current > len(migrations) {
		return apperr.New(apperr.CodeStoreMigration,
			fmt.Sprintf("数据库版本 %d 高于本程序支持的 %d，请勿用旧版程序打开新数据库", current, len(migrations)))
	}
	for v := current; v < len(migrations); v++ {
		if err := s.execMigration(ctx, v+1, migrations[v]); err != nil {
			return err
		}
		s.log.Info("数据库迁移完成", "version", v+1)
	}
	return nil
}

// execMigration 在单事务内执行一个版本的脚本并写入 user_version。
func (s *Store) execMigration(ctx context.Context, version int, script string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapDB("开始迁移事务", err)
	}
	defer tx.Rollback() // Commit 成功后 Rollback 是无害的 no-op

	if _, err := tx.ExecContext(ctx, script); err != nil {
		return apperr.Wrap(apperr.CodeStoreMigration, fmt.Errorf("执行迁移 v%d: %w", version, err))
	}
	// PRAGMA 语句不接受绑定参数；version 来自包内切片长度，无注入面
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return apperr.Wrap(apperr.CodeStoreMigration, fmt.Errorf("写入迁移版本 v%d: %w", version, err))
	}
	if err := tx.Commit(); err != nil {
		return apperr.Wrap(apperr.CodeStoreMigration, fmt.Errorf("提交迁移 v%d: %w", version, err))
	}
	return nil
}
