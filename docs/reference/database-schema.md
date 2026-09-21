# Spore 数据库设计参考

> 本文是 Spore 业务数据库（SQLite）的表结构、字段语义与持久化边界的**唯一对外参考**。
> 实现事实来源是 `internal/store/migrate.go`（建表与迁移）与 `internal/store/` 各 DAO 文件；本文描述稳定契约，不复制 SQL。
>
> **维护约定**：表结构、字段、索引、`settings` 逻辑键或接口持久化映射发生变化时，必须同步更新本文件，保持文档与实现一致。

## 目录

- [1. 定位与运行方式](#_1-定位与运行方式)
- [2. Schema 总览](#_2-schema-总览)
- [3. 表数据字典](#_3-表数据字典)
- [4. 表关系与约束](#_4-表关系与约束)
- [5. 索引清单](#_5-索引清单)
- [6. settings 逻辑 Schema](#_6-settings-逻辑-schema)
- [7. 数据库外的持久化](#_7-数据库外的持久化)
- [8. API 与持久化映射](#_8-api-与持久化映射)
- [9. 备份、导入与版本兼容](#_9-备份-导入与版本兼容)
- [10. 迁移历史](#_10-迁移历史)

---

## 1. 定位与运行方式

| 项 | 值 |
| --- | --- |
| 数据库 | 内嵌 SQLite，驱动 `modernc.org/sqlite`（纯 Go、无 CGO，保住交叉编译） |
| 文件 | `DATA_DIR/spore.db`（运行期伴随 `-wal` / `-shm` 侧文件） |
| 访问层 | `internal/store` 是唯一入口（`store.Open`）；业务 SQL 不出现在其他包 |
| 连接 | 连接池固定 1 连接（SQLite 单写者，从根上消除写锁竞争） |
| 连接级 PRAGMA | `journal_mode=WAL`、`busy_timeout=5000`、`foreign_keys=ON`、`synchronous=NORMAL` |
| schema 版本 | `PRAGMA user_version`（当前 **19**）；启动时自动迁移，数据库版本高于程序支持时拒绝启动 |
| 迁移规则 | 版本化、内嵌、**只增不改**：每个版本在独立事务内执行 DDL 并同事务写入 `user_version`；已发布迁移永不修改，新变更一律追加新版本 |

容量目标：≤100 用户、约 5,000 请求/日（约 180 万行/年），远低于 SQLite 单文件上限，不引入 PostgreSQL/Redis 等外部数据库。

通用字段约定（适用于全部业务表）：

- 时间字段统一 **Unix 毫秒时间戳**（int64）；`0`/NULL 表示"尚未发生"，写入可空时间存 NULL、读取归一为 0。不使用字符串时间（唯一例外：`usage_daily.day` 是运营时区 `YYYY-MM-DD` 日键）。
- 可空文本以空字符串等价 NULL，读写由 `internal/store` 的 helper 归一，业务层不感知 NULL。
- JSON 数组/对象统一存 TEXT 列，列名以 `_json` 后缀标识。

## 2. Schema 总览

当前版本 v20 包含 **16 张业务表、16 个显式索引、4 个数据库外键**：

- 无触发器、无视图、无 CHECK 约束；状态枚举与取值白名单由应用层（DAO）校验，见各表说明。
- `sqlite_sequence` 是 SQLite 为 `AUTOINCREMENT`（`cloud_uploads`、`dump_entries`、`watch_invite_requests`）自动维护的内部表，**不属于业务 schema**。
- 频道没有独立表：频道维度的一切数据都是 `requests` 行的聚合（见 [第 4 节](#_4-表关系与约束)）。

| 表 | 用途 | 引入版本 |
| --- | --- | --- |
| `users` | Telegram 用户主档：状态、owner、限额、拒绝记录 | v1 |
| `requests` | 一次提取请求的全生命周期，也是频道/业务统计的唯一事实来源 | v1 |
| `usage_daily` | (用户, 运营日) 的当日用量与重置次数 | v1 |
| `audit_log` | 管理员/系统变更审计 | v1 |
| `events` | 系统异常事件，按 key 去重合并 | v1 |
| `settings` | 运行时键值配置（逻辑键见第 6 节） | v1 |
| `web_sessions` | 管理端会话（只存 ID 哈希） | v1 |
| `channel_bindings` | 用户频道绑定（任务成功后复制内容的目标频道） | v4 |
| `join_requests` | `/join` 频道加入申请与审批记录 | v6 |
| `joined_channels` | 读取账号实际加入频道的留痕 | v6 |
| `system_metric_samples` | 进程资源与传输速率低频采样 | v7 |
| `cloud_uploads` | 云盘上传逐文件记录 | v10 |
| `dump_entries` | 缓存频道"干净副本"消息坐标（复用来源） | v13 |
| `watch_sources` | 监听源（/watch）配置与申请审批（预热缓存频道） | v17 |
| `watch_events` | 监听转储逐次留痕（哪个 bot 在哪个源转发了哪些消息） | v18 |
| `watch_invite_requests` | 私有邀请链接监听申请的异步处理状态与安全展示快照 | v19 |

## 3. 表数据字典

### 3.1 users

Telegram 用户主档，主键即 Telegram User ID。状态流转：`/start` 创建 `pending` → 管理端审批 `enabled` → `disabled` / `archived`（可恢复）；owner 不可停用/归档。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | INTEGER | PRIMARY KEY | Telegram 用户 ID |
| `status` | TEXT | NOT NULL | `pending` / `enabled` / `disabled` / `archived`（应用层白名单校验） |
| `is_owner` | INTEGER | NOT NULL DEFAULT 0 | owner 标记；唯一性由应用层维护（设置 owner 时单语句清零其他用户），无数据库唯一索引 |
| `username` | TEXT | 可空 | 用户名快照（审批/资料刷新时取得） |
| `display_name` | TEXT | 可空 | 显示名快照 |
| `note` | TEXT | 可空 | 管理员备注 |
| `submit_interval_sec` | INTEGER | NOT NULL DEFAULT 10 | 提交间隔（秒） |
| `daily_limit` | INTEGER | NOT NULL DEFAULT 50 | 每日额度 |
| `concurrent_limit` | INTEGER | NOT NULL DEFAULT 2 | 未完成任务并发上限 |
| `created_at` | INTEGER | NOT NULL | 创建时间（Unix 毫秒） |
| `first_used_at` | INTEGER | 可空 | 首次使用时间 |
| `last_used_at` | INTEGER | 可空 | 最近使用时间 |
| `archived_at` | INTEGER | 可空 | 归档时间 |
| `last_denied_at` | INTEGER | 可空 | 最近拒绝时间 |
| `last_denied_reason` | TEXT | 可空 | 最近拒绝的错误码 |
| `bind_limit` | INTEGER | NOT NULL DEFAULT 0 | 频道绑定数量上限（v5）：0 = 跟随角色默认（普通 1 / owner 3），1–20 为显式值 |
| `cloud_download` | INTEGER | NOT NULL DEFAULT 0 | 云盘下载权限三态（v11）：0 = 跟随角色默认（owner 允许 / 普通拒绝），1 = 显式允许，2 = 显式拒绝；与全局开关是 AND 关系 |
| `source_bot_id` | INTEGER | NOT NULL DEFAULT 0 | 来源 bot 数字 ID（v15，首次 `/start` 的受理 bot）；0 = 存量行或 Web 手动添加 |
| `source_bot_username` | TEXT | NOT NULL DEFAULT '' | 来源 bot 用户名快照（v15，展示自持，bot 移出池后仍可读） |
| `auto_pin` | INTEGER | NOT NULL DEFAULT 0 | 自动置顶偏好（v20）：1 = 该用户的普通任务提交即默认标记置顶（云盘/缓存补写任务不适用） |

### 3.2 requests

一次提取请求的全生命周期记录；频道统计、业务统计、DC 分布、Bot 分布全部聚合自本表。状态机：`queued` → `processing` → `succeeded` / `failed` / `cancelled`；重试复用同一行，`attempt` 累计（上限为动态配置 `max_request_attempts`，默认 3；管理端可重置计数）

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | INTEGER | PRIMARY KEY | 请求 ID |
| `user_id` | INTEGER | NOT NULL，FK → `users(id)` | 提交用户 |
| `source_kind` | TEXT | 可空 | `public`（用户名链接）/ `private`（`t.me/c/` 内部链接） |
| `channel_key` | TEXT | NOT NULL | 频道标识：公开频道为用户名，私有频道为 `-100` 前缀内部 ID |
| `message_id` | INTEGER | NOT NULL | 源消息 ID |
| `status` | TEXT | NOT NULL | `queued` / `processing` / `succeeded` / `failed` / `cancelled` |
| `attempt` | INTEGER | NOT NULL DEFAULT 1 | 已尝试次数 |
| `error_code` | TEXT | 可空 | 失败原因错误码 |
| `media_type` | TEXT | 可空 | 主媒体类型；多成员相册统一 `album`；空串 = 未记录（失败于消息转换前） |
| `file_size` | INTEGER | 可空 | 媒体大小（诊断元数据） |
| `file_name` | TEXT | 可空 | 媒体文件名 |
| `requested_at` | INTEGER | NOT NULL | 请求时间 |
| `queued_at` | INTEGER | 可空 | 入队时间 |
| `started_at` | INTEGER | 可空 | 开始处理时间 |
| `finished_at` | INTEGER | 可空 | 终态时间 |
| `duration_ms` | INTEGER | 可空 | 总耗时（毫秒） |
| `delivery_mode` | TEXT | NOT NULL DEFAULT 'upload' | 投递方式（v2）：`upload` 下载上传 / `text` 纯文本 / `cloud` 云盘下载（`/download` 或补存）/ `reuse` 缓存频道复用命中 / `dump` 缓存补写 / `split` 分卷拆分（超限媒体切段整组投递）/ `mixed`（历史遗留，仅旧记录）/ `reference`（历史遗留，机制已移除） |
| `source_media_dc_ids_json` | TEXT | 可空 | 源媒体所在 Telegram DC ID 去重数组（v8）；文本、旧记录为 NULL |
| `media_types_json` | TEXT | 可空 | 请求实际包含的去重媒体类型数组（v9）；相册用它区分纯图片/纯视频/混合 |
| `parent_request_id` | INTEGER | 可空，**无外键** | 补存链路指向的原请求 ID（v10）；普通请求为 NULL |
| `cloud_destination` | TEXT | NOT NULL DEFAULT '' | 云盘请求的目的地名称（v10）；重试/补存重新入队时据此恢复；普通请求空串 |
| `sent_chat_id` | INTEGER | NOT NULL DEFAULT 0 | **遗留列**（v12）：旧"用户聊天坐标复用"的投递目标聊天；现行链路不再写入，仅保留历史行读取兼容 |
| `sent_message_ids_json` | TEXT | NOT NULL DEFAULT '' | **遗留列**（v12）：同上，已发送消息 ID 数组；被 `dump_entries` 方案取代 |
| `bot_id` | INTEGER | NOT NULL DEFAULT 0 | 受理 bot 数字 ID（v15）；0 = 存量行或非 Bot 通道创建 |
| `bot_username` | TEXT | NOT NULL DEFAULT '' | 受理 bot 用户名快照（v15） |
| `pin` | INTEGER | NOT NULL DEFAULT 0 | 自动置顶标记（v20）：1 = 任务成功后需在用户绑定的频道/群组置顶副本组首（`/pin <链接>` 单次指定或用户 `auto_pin` 偏好） |
| `pin_ok` | INTEGER | NOT NULL DEFAULT 0 | 置顶成功的目标数（v20，worker 收尾回写）；重试时清零 |
| `pin_total` | INTEGER | NOT NULL DEFAULT 0 | 参与置顶的目标总数（v20，worker 收尾回写）；重试时清零 |

进程退出中断的 `queued`/`processing` 行在下次启动被批量置 `failed(INTERRUPTED)`。

### 3.3 usage_daily

按 (用户, 运营日) 聚合的当日用量。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `user_id` | INTEGER | NOT NULL，复合主键 | 用户 ID |
| `day` | TEXT | NOT NULL，复合主键 | 运营时区日键 `YYYY-MM-DD`（全库唯一的字符串"时间"字段） |
| `used` | INTEGER | NOT NULL DEFAULT 0 | 当日已用次数 |
| `reset_count` | INTEGER | NOT NULL DEFAULT 0 | 当日额度重置次数 |

### 3.4 audit_log

管理员与系统变更的审计流水。典型来源：用户审批/状态/限额、频道绑定与加入审批、请求 retry/cancel/delete/补存、登录登出、密钥重置、设置与通知变更、备份导出导入、事件 resolve/recover。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | INTEGER | PRIMARY KEY | 条目 ID |
| `at` | INTEGER | NOT NULL | 时间（Unix 毫秒） |
| `actor` | TEXT | 可空 | 操作者（`admin` / `system` 等） |
| `action` | TEXT | NOT NULL | 动作标识（如 `auth.login`、`user.status`、`request.cloud_archive`） |
| `target` | TEXT | 可空 | 操作对象 |
| `before_json` | TEXT | 可空 | 变更前快照（原始 JSON；只放业务字段，不含凭据） |
| `after_json` | TEXT | 可空 | 变更后快照（同上） |

清理语义：批量删除与 `audit.delete` 记录同事务；全量清除与 `audit.clear` 同事务，清除后表内仅保留这一条。

### 3.5 events

系统异常事件，按 `key` 去重合并（UPSERT：`count` 累加、`last_at` 更新）；已 `resolved` 的事件再次发生会重新打开。`last_notified_at` 支撑 30 分钟通知冷却。屏蔽/静音只影响提醒，不删除事件。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | INTEGER | PRIMARY KEY | 事件 ID |
| `key` | TEXT | NOT NULL，UNIQUE | 去重键（隐式唯一索引） |
| `severity` | TEXT | NOT NULL | 级别（`info` / `warn` / `error`） |
| `message` | TEXT | NOT NULL | 受控中文描述 |
| `count` | INTEGER | NOT NULL DEFAULT 1 | 合并发生次数 |
| `first_at` | INTEGER | NOT NULL | 首次发生时间 |
| `last_at` | INTEGER | NOT NULL | 最近发生时间 |
| `last_notified_at` | INTEGER | 可空 | 最近通知时间（NULL = 从未通知） |
| `status` | TEXT | NOT NULL DEFAULT 'open' | `open` / `resolved` |

### 3.6 settings

通用键值表，物理结构只有两列；逻辑键（运行设置、系统设置、通知配置、OAuth、Bot 暂停等）运行期按需写入，**新增逻辑键不需要迁移**，详见[第 6 节](#_6-settings-逻辑-schema)。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `key` | TEXT | PRIMARY KEY | 逻辑键名 |
| `value_json` | TEXT | NOT NULL | 值（JSON 文本） |

### 3.7 web_sessions

管理端 Web 会话。只存会话 ID 的 SHA-256 哈希，不存原始会话 ID。每次认证请求滑动续期；登录时惰性清理过期行；密钥重置 / OAuth 绑定与解绑 / 数据库导入会清空全部会话。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id_hash` | TEXT | PRIMARY KEY | 会话 ID 的 SHA-256 哈希 |
| `created_at` | INTEGER | NOT NULL | 创建时间 |
| `expires_at` | INTEGER | NOT NULL | 过期时间（滑动续期） |
| `csrf_token` | TEXT | NOT NULL | 会话级 CSRF token |
| `ip` | TEXT | 可空 | 登录 IP |
| `user_agent` | TEXT | 可空 | 登录 User-Agent |

### 3.8 channel_bindings

用户频道绑定：任务成功后把内容复制到归属用户的绑定频道。`channel_id` 是 Bot API 的频道数字 ID（`-100` 前缀）作主键，**同一频道只归属一个用户**。不存凭据或 access hash，只存展示所需的 username/title。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `channel_id` | INTEGER | PRIMARY KEY | Bot API 频道数字 ID（负数，`-100` 前缀） |
| `user_id` | INTEGER | NOT NULL，FK → `users(id)` | 归属用户 |
| `username` | TEXT | 可空 | 频道公开用户名（私有频道为空） |
| `title` | TEXT | 可空 | 绑定时取得的频道标题 |
| `bound_via` | TEXT | NOT NULL DEFAULT 'bot' | 绑定来源：`bot`（用户 `/bind`）/ `web`（管理端） |
| `created_at` | INTEGER | NOT NULL | 绑定时间 |
| `updated_at` | INTEGER | NOT NULL | 最近更新时间（绑定服务刷新标题/用户名时更新；未下发到 API DTO） |

### 3.9 join_requests

`/join` 频道加入申请与审批记录。`invite_hash` 落库是审批延时执行所必需；展示层一律脱敏（`masked_hash`），不入日志。状态：`pending` → `approved` / `rejected`（加入失败置 `failed`）。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | INTEGER | PRIMARY KEY | 申请 ID |
| `user_id` | INTEGER | NOT NULL，FK → `users(id)` | 申请人 |
| `invite_hash` | TEXT | NOT NULL | 邀请链接哈希（敏感，只脱敏下发） |
| `channel_title` | TEXT | 可空 | 频道标题 |
| `participants` | INTEGER | 可空 | 频道成员数（提交时快照） |
| `status` | TEXT | NOT NULL | `pending` / `approved` / `rejected` / `failed`（应用层白名单校验） |
| `requested_at` | INTEGER | NOT NULL | 申请时间 |
| `reviewed_at` | INTEGER | 可空 | 审批时间（0/NULL = 未审批） |
| `reviewed_by` | TEXT | 可空 | 审批人（会话哈希标识） |
| `note` | TEXT | 可空 | 审批备注（如请求制频道等待频道侧批准的说明） |

同用户对同一邀请链接的重复 pending 申请靠应用层查重去重，**没有数据库唯一索引**。

### 3.10 joined_channels

读取账号实际加入频道的**留痕表**，不是实时频道列表的唯一来源（实时列表 = MTProto 对话遍历 + 本表留痕）。不存 access_hash（数据红线）。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `channel_id` | INTEGER | PRIMARY KEY | 频道数字 ID |
| `title` | TEXT | 可空 | 频道标题 |
| `username` | TEXT | 可空 | 频道公开用户名 |
| `kind` | TEXT | NOT NULL DEFAULT 'channel' | 对象类型（当前恒为 `channel`） |
| `joined_via` | TEXT | NOT NULL DEFAULT 'external' | 留痕来源：`join_command`（owner `/join` 即时）/ `approved`（审批加入）/ `external`（外部拉入或无留痕）/ `watch_source`（私有邀请监听源流程加入） |
| `joined_by` | INTEGER | 可空，FK → `users(id)` | 触发加入的用户；NULL = 外部加入或未关联用户 |
| `joined_at` | INTEGER | NOT NULL | 加入时间 |
| `left_at` | INTEGER | 可空 | 退出时间（NULL = 仍在加入中） |

### 3.11 system_metric_samples

进程资源与传输速率的低频历史采样（4h/1d 档位的数据源；realtime 档位来自 monitor 内存窗口）。监控服务按固定周期聚合后按主键 upsert，并按保留期清理旧行。NULL = 探针不可用。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `sampled_at` | INTEGER | PRIMARY KEY | 采样时间（Unix 毫秒） |
| `rss_bytes` | INTEGER | 可空 | 进程常驻内存 |
| `temp_dir_bytes` | INTEGER | 可空 | 临时目录占用 |
| `download_bytes_per_second` | REAL | 可空 | 全服务下载聚合速率 |
| `upload_bytes_per_second` | REAL | 可空 | 全服务上传聚合速率（发送方已消费字节） |
| `cpu_percent` | REAL | 可空 | 进程 CPU 占用率（v14，0–100，占全部核心） |

### 3.12 cloud_uploads

云盘任务的逐文件上传记录（`/download` 与管理端补存共用）。注意：`request_id` 无外键，删除请求行**不会**级联清理上传明细（历史痕迹有意保留，无独立清理 DAO）。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | 记录 ID |
| `request_id` | INTEGER | NOT NULL，**无外键** | 所属请求（业务关联） |
| `destination` | TEXT | NOT NULL | 目的地名称 |
| `remote_path` | TEXT | NOT NULL | 远端完整路径（不含目的地前缀） |
| `file_name` | TEXT | NOT NULL DEFAULT '' | 文件名（空串 = 未记录） |
| `status` | TEXT | NOT NULL | `uploading` / `succeeded` / `failed` |
| `error_code` | TEXT | NOT NULL DEFAULT '' | 失败错误码（成功为空串） |
| `bytes` | INTEGER | NOT NULL DEFAULT 0 | 已上传字节数 |
| `created_at` | INTEGER | NOT NULL | 开始时间 |
| `finished_at` | INTEGER | NOT NULL DEFAULT 0 | 结束时间（0 = 未结束） |

### 3.13 dump_entries

任务成功投递后同步写入 bot 自有**缓存频道**的"干净副本"消息坐标（无脚注 caption，跨用户复用的唯一来源）。只存 bot 自有频道内的消息坐标，不存正文/媒体/凭据。同链接可有多条，"取当前格式最新"生效；副本被删时复用失败自动回落完整链路并重写副本自愈。生命周期独立于 `requests`（无外键）。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | 条目 ID |
| `channel_key` | TEXT | NOT NULL | 源链接的频道标识 |
| `message_id` | INTEGER | NOT NULL | 源消息 ID |
| `dump_ids_json` | TEXT | NOT NULL | 缓存频道内的消息 ID 数组（相册保组，按发送顺序） |
| `format_version` | INTEGER | NOT NULL DEFAULT 0 | 副本布局格式版本（v16）：0 = 历史行（相册多 caption 旧形态），1 = "恰好组首一条合并 caption"；查询只命中当前版本，历史坐标保留供审计，复用回落完整投递后自愈重写 |
| `created_at` | INTEGER | NOT NULL | 写入时间 |

### 3.14 watch_sources

监听源（/watch，v17；v18 增补 `kind`/`bot_id`/`bot_username`）：配置的源频道/超级群组由 Bot 接收新帖并自动转储缓存频道预热 `dump_entries`（重复链接直接命中复用）。管理员 Web 添加天然 approved；用户 `/watch` 申请按配置走审批（pending → approved/rejected）。`added_by=0` 表示管理员添加（无外键：0 语义不是用户行）。listener 按 `status='approved' AND enabled=1` 过滤生效源。只存标识与状态，不存消息内容（数据范围红线）。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `channel_id` | INTEGER | PRIMARY KEY | 源频道/超级群组数字 ID（Bot API -100 形态） |
| `kind` | TEXT | NOT NULL DEFAULT '' | `channel` / `supergroup`（展示用；配置时快照） |
| `username` | TEXT | NULL | 公开源用户名（无 @；私有源为空）。供公开源双键复用（t.me/username 与 t.me/c 两种链接形态都命中条目） |
| `title` | TEXT | NULL | 展示标题（配置时快照） |
| `status` | TEXT | NOT NULL | `pending` / `approved` / `rejected`（DAO 白名单校验；Review 仅允许 pending → approved/rejected） |
| `enabled` | INTEGER | NOT NULL DEFAULT 1 | approved 行的独立暂停开关 |
| `added_by` | INTEGER | NOT NULL DEFAULT 0 | 0 = 管理员 Web 添加；>0 = 申请人用户 ID |
| `bot_id` | INTEGER | NOT NULL DEFAULT 0 | 用户 /watch 的受理 bot ID（与 `requests.bot_id` v15 同语义；0 = Web 添加） |
| `bot_username` | TEXT | NOT NULL DEFAULT '' | 受理 bot 用户名快照（展示自持，bot 移出池后历史仍可读） |
| `reviewed_by` | TEXT | NULL | 审批人（session idHash 或 admin）；未审批为空 |
| `created_at` | INTEGER | NOT NULL | 首次写入时间（Upsert 冲突更新不重置） |
| `updated_at` | INTEGER | NOT NULL | 最近更新时间 |

### 3.15 watch_events

监听转储逐次留痕（业务统计与「监听记录」页的事实表）：哪个 bot、在哪个源、转发了哪些消息、缓存频道落点与路径。每次转储成功（copy）或回退入队（fallback）各落一行；已存在条目的跳过不落。只存 ID 与元数据（数据范围红线）；源标题/用户名与 bot 用户名存快照，源或 bot 删除后记录仍可读。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | 事件 ID |
| `channel_id` | INTEGER | NOT NULL | 源频道/群组 ID（-100 形态） |
| `username` / `title` | TEXT | NOT NULL DEFAULT '' | 源快照（NOT NULL 列直接存空串，不经 nullStr） |
| `message_id` | INTEGER | NOT NULL | 定位消息 ID（相册取首条成员） |
| `member_ids_json` | TEXT | NOT NULL | 转发的源消息 ID 数组（相册为全部成员） |
| `dump_ids_json` | TEXT | NOT NULL | 缓存频道落点消息 ID 数组（fallback 为空数组） |
| `request_id` | INTEGER | NOT NULL DEFAULT 0 | 关联 `requests` 行（仅 fallback；0 = 无） |
| `bot_id` | INTEGER | NOT NULL DEFAULT 0 | 执行转储的 bot |
| `bot_username` | TEXT | NOT NULL DEFAULT '' | bot 用户名快照 |
| `path` | TEXT | NOT NULL | `copy`（服务端复制）\| `fallback`（受保护重传，DAO 白名单校验） |
| `created_at` | INTEGER | NOT NULL | 事件时间 |

### 3.16 watch_invite_requests

私有邀请链接监听申请的异步处理记录。活动状态为 `pending` / `waiting_telegram` / `waiting_bot`，终态为 `approved` / `rejected` / `failed`。`user_id=0` 表示管理员 Web 路径创建（与 `watch_sources.added_by` 同约定，无数据库外键）；`>0` 为申请人用户 ID。完整 `invite_hash` 只供加入流程内部使用，Go 模型以 `json:"-"` 禁止序列化（对外字段为 `masked_hash`，频道标题与申请时间分别以 `channel_title` / `created_at` 下发）；保留策略由服务层在状态流转时控制：`pending` / `waiting_telegram` 保留完整 hash、`failed` 保留以便重试，`waiting_bot` / `approved` / `rejected` 清理完整 hash，仅留 `masked_hash` 供安全展示。

| 字段 | 类型 | 约束 | 说明 |
| --- | --- | --- | --- |
| `id` | INTEGER | PRIMARY KEY AUTOINCREMENT | 申请 ID |
| `user_id` | INTEGER | NOT NULL DEFAULT 0 | 申请人；0 = 管理员路径（无外键） |
| `invite_hash` | TEXT | 可空 | 完整邀请 hash（敏感；活动阶段内部使用，按保留策略清理） |
| `masked_hash` | TEXT | NOT NULL | 脱敏展示值，完整 hash 清理后仍保留 |
| `status` | TEXT | NOT NULL | `pending` / `waiting_telegram` / `waiting_bot` / `approved` / `rejected` / `failed` |
| `channel_id` | INTEGER | NOT NULL DEFAULT 0 | 邀请解析成功后的频道/群组 ID；0 = 尚未解析 |
| `kind` | TEXT | NOT NULL DEFAULT '' | `channel` / `supergroup` 快照 |
| `username` | TEXT | NOT NULL DEFAULT '' | 公开用户名快照；私有源可为空 |
| `title` | TEXT | NOT NULL DEFAULT '' | 标题快照（模型 JSON 字段 `channel_title`） |
| `participants` | INTEGER | NOT NULL DEFAULT 0 | 频道成员数快照；0 = 未解析 |
| `enabled` | INTEGER | NOT NULL DEFAULT 1 | 监听开关；由调用方显式传值（用户路径 true，管理员可预录入 false 停用态） |
| `reviewed_by` | TEXT | NOT NULL DEFAULT '' | 审批人标识（会话哈希或 admin）；未审批为空串 |
| `note` | TEXT | NOT NULL DEFAULT '' | 备注（状态更新时覆盖写入，空串即清除） |
| `bot_id` | INTEGER | NOT NULL DEFAULT 0 | 受理 bot ID |
| `bot_username` | TEXT | NOT NULL DEFAULT '' | 受理 bot 用户名快照 |
| `requested_at` | INTEGER | NOT NULL | 申请时间（Unix 毫秒；模型 JSON 字段 `created_at`） |
| `updated_at` | INTEGER | NOT NULL | 最近状态、频道信息、审批人或 hash 清理更新时间 |

活动列表、全局/按用户计数及相同 hash 查重只统计三个活动状态；终态历史行不阻止同一邀请重新申请。管理列表（全部状态）待审批在前、其余按申请时间倒序；Bot 用户列表按申请时间倒序。

## 4. 表关系与约束

### 4.1 数据库外键（均指向 `users(id)`，均 NO ACTION）

| 表.列 | 引用 |
| --- | --- |
| `requests.user_id` | `users(id)` |
| `channel_bindings.user_id` | `users(id)` |
| `join_requests.user_id` | `users(id)` |
| `joined_channels.joined_by` | `users(id)` |

### 4.2 逻辑关联（有意不设外键）

| 列 | 指向 | 原因 |
| --- | --- | --- |
| `requests.parent_request_id` | `requests.id` | 补存目标行可能先于该列存在；仅做展示跳转 |
| `cloud_uploads.request_id` | `requests.id` | 上传明细独立保留，不随请求删除级联清理 |
| `dump_entries.(channel_key, message_id)` | 同链接 `requests` 行 | 缓存副本生命周期独立于任何一条请求 |

### 4.3 应用层约束（数据库不保证）

- `users.status`、`requests.status`、`join_requests.status`、`cloud_uploads.status`、`events.status` 等枚举由 DAO 白名单校验，无 CHECK 约束。
- `users.is_owner` 全局唯一性由设置 owner 的业务语句维护。
- `join_requests` 无 `(user_id, invite_hash, status)` 唯一索引，pending 去重靠应用层查重。
- `watch_sources.status` 由 DAO 白名单校验；重复审批（非 pending 行）返回 STORE_CONSTRAINT；上限校验（总数/每用户）在 watch 服务应用层完成。
- `watch_events` 只增不改；`watch_events.request_id` 关联行可能不存在（请求可被清理），仅做展示跳转。
- `watch_invite_requests.status` 由 DAO 白名单校验；活动状态集合统一用于列表、计数和 hash 查重；完整 hash 清理后只保留 `masked_hash`，保留策略（`failed` 便于重试、`waiting_telegram` 保留，`waiting_bot`/`approved`/`rejected` 清理）由服务层控制；`user_id=0` 为管理员路径（应用层约定，无数据库外键，`enabled` 由调用方显式传值）。
- 频道无独立表：管理端"频道统计/频道详情"是 `requests` 的纯聚合；删除频道 = 删除该频道的全部 `requests` 行。

## 5. 索引清单

| 索引 | 表 | 列 | 引入 | 服务场景 |
| --- | --- | --- | --- | --- |
| `idx_requests_user_requested` | `requests` | (user_id, requested_at) | v1 | 用户维度列表与用量统计 |
| `idx_requests_channel_requested` | `requests` | (channel_key, requested_at) | v1 | 频道聚合、趋势 |
| `idx_requests_status` | `requests` | (status) | v1 | 状态筛选、未完成任务计数 |
| `idx_requests_user_channel_message` | `requests` | (user_id, channel_key, message_id) | v1 | 同用户同链接去重 |
| `idx_audit_at_id` | `audit_log` | (at, id) | v3 | 审计时间范围过滤 + 倒序分页 |
| `idx_channel_bindings_user` | `channel_bindings` | (user_id) | v4 | 按用户查绑定 |
| `idx_join_requests_status` | `join_requests` | (status, requested_at) | v6 | 审批列表按状态筛选 |
| `idx_join_requests_user` | `join_requests` | (user_id, requested_at) | v6 | 按用户查申请 |
| `idx_joined_channels_active` | `joined_channels` | (left_at, joined_at) | v6 | 活跃/已退出统计 |
| `idx_cloud_uploads_request` | `cloud_uploads` | (request_id) | v10 | 请求详情的上传明细 |
| `idx_requests_channel_message_status` | `requests` | (channel_key, message_id, status) | v12 | 跨用户缓存复用查询（不带 user_id 前缀） |
| `idx_dump_entries_link` | `dump_entries` | (channel_key, message_id, id) | v13 | 同链接取最新副本 |
| `idx_watch_events_channel` | `watch_events` | (channel_id, id) | v18 | 按监听源倒序查询转储记录 |
| `idx_watch_invite_requests_active` | `watch_invite_requests` | (status, requested_at, id) | v19 | 活动申请恢复列表与全局计数 |
| `idx_watch_invite_requests_user_active` | `watch_invite_requests` | (user_id, status, requested_at, id) | v19 | 按用户统计活动申请 |
| `idx_watch_invite_requests_hash_active` | `watch_invite_requests` | (invite_hash, status) | v19 | 相同完整 hash 的活动申请查重 |

隐式索引：`events.key` 的 UNIQUE 索引，以及各主键索引（含 `usage_daily` 复合主键）。

## 6. settings 逻辑 Schema

`settings` 物理上只有 `key` / `value_json` 两列；以下逻辑键运行期按需写入（首次保存时才出现，新增键**不需要迁移**）。生效方式（即时/重启）以[配置参考](./configuration.md)为唯一权威来源。

| 域 | 键 | 值形态 | 说明 |
| --- | --- | --- | --- |
| 运营 | `timezone` | 字符串 | 运营时区（IANA 名称） |
| 运营 | `dedup_window_min` | 数值 | 重复链接去重窗口（分钟） |
| 运行设置 | `queue_capacity` | 数值 | 队列容量（重启生效） |
| 运行设置 | `worker_count` | 数值 | worker 数，1–16（重启生效，覆盖环境变量） |
| 运行设置 | `max_file_size` / `stream_limit` / `temp_dir_max_size` | 字节数值 | 媒体参数（重启生效） |
| 运行设置 | `memory_budget` | 字节数值 | 内存管道进程级预算（即时生效） |
| 运行设置 | `max_links_per_message` | 数值 | 单条消息最大有效链接数 |
| 运行设置 | `channel_copy_enabled` / `tg_reuse_enabled` | 布尔 | 频道副本同步 / 缓存频道复用总开关 |
| 运行设置 | `dump_channel_id` / `dump_channel_title` | 数值 / 字符串 | 缓存频道（settings 优先于 `DUMP_CHANNEL_ID` 环境变量） |
| 运行设置 | `last_backup_at` | 毫秒时间戳 | 最近备份时间 |
| 系统 | `system_name` | 字符串 | 系统名称（缺省 `Spore`） |
| 频道加入 | `join_enabled` / `join_auto_leave_external` / `join_require_approval` / `join_mute_enabled` / `join_archive_enabled` | 布尔 | `/join` 总开关与行为配置 |
| 频道加入 | `join_max_channels` | 数值 | 活跃加入数量上限（0 = 不限） |
| 传输 | `download_threads` / `upload_threads` / `download_connections` / `upload_connections` | 数值 | 传输并发的数据库覆盖（覆盖环境默认值） |
| 安全 | `access_key_hash` | 字符串 | Web 访问密钥的 SHA-256 哈希（**只存不可逆哈希**） |
| OAuth | `github_oauth_config` | JSON | GitHub OAuth 配置；Client Secret 为 AES-256-GCM 密文字段（根密钥 `WEB_OAUTH_ENCRYPTION_KEY` 仅来自环境变量，按用途 HKDF 域分离） |
| OAuth | `github_binding` | JSON | 管理员 GitHub 账号绑定信息 |
| Bot 池 | `bots_paused` | JSON | 手动暂停的 bot 列表（重启/重连后保持） |
| 通知 | `notification_channels` | JSON | 通知通道配置；Bot Token、Webhook URL 与签名密钥为 AES-256-GCM 密文字段 |
| 通知 | `notification_policy` | JSON | 通知策略与静音计划（与凭据文档分离，策略写入不触碰凭据） |

敏感边界：明文凭据一律不入 `settings`；上表仅有的两类例外（通知通道凭据、OAuth Client Secret）是**可轮换的外部服务凭据**，必须以密文保存，API 响应、日志与审计不得出现密文或明文。

## 7. 数据库外的持久化

以下数据**不在 SQLite 中**，由各包以文件方式管理；它们不随数据库备份导出，也不要在数据库文档或表设计中虚构对应字段。

| 文件/目录 | 内容 | 管理方 |
| --- | --- | --- |
| `data/session.json` | 用户号 MTProto 会话（等效账号控制权） | `internal/mtproto` |
| `data/bot-session.json` / `bot-session-<botID>.json` | Bot 身份 MTProto 会话（多机器人池每 bot 一份） | `internal/mtproto` |
| `data/peers.json` | ChannelID → AccessHash 缓存 | `internal/mtproto` |
| `data/bots.json` | 机器人池 token 列表 | `internal/botlist` |
| `data/cloud-drive.json` | 云盘配置与网盘凭据 | `internal/cloudarchive` |
| `data/pending-cloud-drive.json` | 云盘配置导入候选标记 | `internal/cloudarchive` |
| `data/pending-import.db` / `pending-import.json` | 数据库备份导入候选与确认标记 | `internal/store` |
| `data/spore.db.rollback-*` | 导入前的回滚副本 | `internal/store` |
| `data/tmp/` | 大媒体临时文件（任务结束即清理） | `internal/media` |
| `spore.db-wal` / `spore.db-shm` | SQLite WAL 侧文件（非独立数据） | SQLite |

## 8. API 与持久化映射

本表按管理端 API 功能域回答"数据从哪来、写到哪"。完整端点契约（字段、错误码、分页）见 [API 参考](./api.md)；API 变化时请同步更新本表对应行。

| API / 功能域 | 读取 | 写入 | 说明 |
| --- | --- | --- | --- |
| `GET /healthz` | 无 | 无 | 纯进程探针 |
| `GET /readyz` | `settings`（一次读取验证） | 无 | 数据库可读写验证 |
| 登录 / 登出 / 会话（`/api/v1/login*`、`/api/v1/session*`） | `settings.access_key_hash`、`settings.github_*`、`web_sessions` | `web_sessions`（创建/续期/删除/惰性清理）、`audit_log` | 会话只存 ID 哈希 |
| `GET /api/v1/overview` | `users`、`requests`、`join_requests`、`joined_channels`、`settings` | 无 | 队列/Bot/MTProto/文件状态来自内存，不落库 |
| `GET /api/v1/version/check` | 无 | 无 | 上游 Releases 查询 + 服务端缓存 |
| `GET /api/v1/system-metrics` | `system_metric_samples`（4h/1d 档） | 无 | realtime 档来自 monitor 内存窗口 |
| `GET /api/v1/stats` | `requests`、`users` | 无 | 全部指标与分布聚合自 `requests` |
| 用户管理（`/api/v1/users*`） | `users`、`usage_daily`、`requests`（聚合） | `users`、`usage_daily`、`audit_log` | `effective_*`、`remaining_today`、`total_requests` 为派生字段；reset-quota 写 `usage_daily.reset_count` |
| 申请审批（`/api/v1/applications*`） | `users`（`status=pending`） | `users`、`audit_log` | approve → `enabled`，reject → `disabled` |
| 请求记录（`/api/v1/requests*`） | `requests`、`users`、`cloud_uploads`（详情） | `requests`、`audit_log` | retry/cancel/delete 只改 `requests` 行并写审计 |
| 云盘配置（`GET/PUT /api/v1/cloud-drive`、`/test`） | 无 | 无 | 读写 `data/cloud-drive.json` 文件，不进数据库 |
| 云盘补存（`/api/v1/requests/*/cloud-archive*`） | `requests`、`cloud_uploads` | `requests`（新建 `delivery_mode=cloud` 行）、`audit_log` | worker 执行期写 `cloud_uploads`；同链接成功记录用于远端复用判定 |
| 缓存补写（`/api/v1/requests/*/dump-backfill*`） | `requests`、`dump_entries`、`settings`（缓存频道） | `requests`（新建 `delivery_mode=dump` 行）、`audit_log` | worker 执行期写 `dump_entries`；全程不向原用户发消息 |
| 云盘配置备份（`/api/v1/cloud-drive/backup*`） | 无 | 无 | 独立加密 ZIP 流程，只操作 `data/` 文件 |
| 频道统计（`/api/v1/channels*`） | `requests`（聚合） | `requests`（删除时） | 频道删除 = 删除该频道全部请求行 |
| 频道绑定（`/api/v1/channel-bindings*`） | `channel_bindings`、`users` | `channel_bindings`、`audit_log` | 绑定校验经 Bot API `getChat`/`getChatMember`，结果落库 |
| 频道加入（`/api/v1/channel-join/*`） | `join_requests`、`joined_channels`、`users` | `join_requests`、`joined_channels`、`audit_log` | 已加入频道列表 = MTProto 实时遍历 + 本表留痕；`invite_hash` 只下发脱敏值 |
| 事件中心（`/api/v1/events*`） | `events` | `events`、`audit_log` | 手动 resolve 与系统自动恢复共用语义 |
| 审计日志（`/api/v1/audit*`） | `audit_log` | `audit_log` | 删除与清理审计同事务；clear 后仅保留一条 `audit.clear` |
| 运行设置 / 系统设置（`/api/v1/settings`、`/api/v1/system/config`） | `settings` | `settings`、`audit_log` | 逻辑键见第 6 节；值未变化时不写审计 |
| 通知设置（`/api/v1/notification/*`） | `settings.notification_*` | `settings`、`audit_log` | 凭据密文保存；策略与静音计划在 `notification_policy` |
| OAuth（`/api/v1/oauth/*`、`/auth/github*`） | `settings.github_*` | `settings`、`web_sessions`（绑定/解绑清空全部会话）、`audit_log` | Secret 密文；响应与审计不回显 |
| 数据备份（`/api/v1/backup*`） | 全库快照（`VACUUM INTO`） | `settings.last_backup_at`、`audit_log` | 导入候选在启动期应用，语义见第 9 节 |
| 受控重启（`/api/v1/restart`） | 无 | `audit_log` | SIGTERM 优雅退出，不写业务表 |
| MTProto 状态 / 重连（`/api/v1/mtproto/*`） | 无 | 无 | 会话状态机在内存 |
| 机器人池（`/api/v1/bots*`） | `data/bots.json`、`settings.bots_paused`、内存运行态 | `data/bots.json`、`settings.bots_paused`、`audit_log` | token 只进不出（不回显） |
| CSV 导出（`/users/export.csv` 等） | `users`、`usage_daily`、`requests` | 无 | 与对应列表接口同源；导出动作写审计 |

Bot 与 worker 侧的关键写入（无 HTTP 端点，补全全景）：

- `/start`：创建或刷新 `users` 的 pending 行；
- 链接提交（准入链）：单事务内检查限额并写 `requests`（queued 行）、累加 `usage_daily`、更新 `users` 的使用/拒绝时间；
- worker：按阶段更新 `requests` 状态与终态；云盘任务写 `cloud_uploads`；成功投递写 `dump_entries` 缓存副本坐标；
- 绑定、加入、审批等管理动作与系统事件：写 `audit_log` / `events`。

## 9. 备份、导入与版本兼容

**导出**：`VACUUM INTO` 生成在线一致快照，只含业务数据库；不含 Session、peers、云盘配置、临时媒体（见第 7 节）。

**导入校验**（`ValidateBackup`）：普通文件、SQLite 可打开、`integrity_check=ok`、`user_version ≤ 当前迁移版本`（高版本拒绝），且必要表存在、必要列存在：

- 基础集：`users(id,status,username,display_name)`、`requests(id,user_id,source_kind,channel_key,message_id,status)`、`usage_daily(user_id,day,used)`、`audit_log(id,at,action)`、`events(id,key,severity,message,status)`、`settings(key,value_json)`、`web_sessions(id_hash,expires_at,csrf_token)`；
- 按备份版本校验的结构：v8 `requests.source_media_dc_ids_json`、v9 `requests.media_types_json`、v11 `users.cloud_download`、v19 `watch_invite_requests.id`（用于确认 v19 表存在）。

低版本备份导入后由当前 Store 补齐迁移；**新增迁移时必须评估**：新表/新列是否属于"低版本备份导入后必须存在"的兼容面，若是则扩展上述按版本校验并同步更新本节与对应测试。

**应用**：导入需显式确认并写入 marker，下次启动原子替换；替换时保留**当前库**的 `settings`（访问密钥、OAuth、运行配置）、清空 `web_sessions`、写 `backup.import.applied` 审计；旧库保留带时间戳的 rollback 副本。

## 10. 迁移历史

规则：已发布迁移永不修改；新变更一律追加新版本，并同步更新本文第 2–5 节与本表。

| 版本 | 变更 |
| --- | --- |
| v1 | 初始基线：`users`、`requests`、`usage_daily`、`audit_log`、`events`、`settings`、`web_sessions` 与 4 个 requests 索引 |
| v2 | `requests.delivery_mode`（默认 `upload`，旧行自动归属历史语义） |
| v3 | `audit_log` 时间分页索引 `idx_audit_at_id` |
| v4 | `channel_bindings` 表与用户索引 |
| v5 | `users.bind_limit` |
| v6 | `join_requests`、`joined_channels` 表与索引 |
| v7 | `system_metric_samples` 表 |
| v8 | `requests.source_media_dc_ids_json` |
| v9 | `requests.media_types_json` |
| v10 | `cloud_uploads` 表与索引；`requests.parent_request_id`、`requests.cloud_destination` |
| v11 | `users.cloud_download` |
| v12 | `requests.sent_chat_id`、`requests.sent_message_ids_json`（现为遗留列）与 `idx_requests_channel_message_status` |
| v13 | `dump_entries` 表与索引 |
| v14 | `system_metric_samples.cpu_percent` |
| v15 | `requests.bot_id`、`requests.bot_username`；`users.source_bot_id`、`users.source_bot_username` |
| v16 | `dump_entries.format_version`（缓存副本布局格式版本；历史行不再命中复用） |
| v17 | `watch_sources` 监听源配置与申请审批表 |
| v18 | 重建 `watch_sources` 补齐类型/受理 bot 字段；新增 `watch_events` 与频道索引 |
| v19 | `watch_invite_requests` 私有邀请链接监听申请表（管理员路径 `user_id=0`，无外键）及活动状态、用户与 hash 索引 |
| v20 | `requests.pin`、`requests.pin_ok`、`requests.pin_total`；`users.auto_pin`（自动置顶标记、结果回写与用户级偏好） |
