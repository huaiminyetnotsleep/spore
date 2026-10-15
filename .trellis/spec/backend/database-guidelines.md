# 持久化与数据文件规范

## 当前状态

Spore 使用**内嵌 SQLite** 作为业务数据库：驱动为 `modernc.org/sqlite`（纯 Go、无 CGO，保住交叉编译），经标准库 `database/sql` 访问，由 `internal/store` 包统一封装。数据库文件为 `DATA_DIR/spore.db`。

容量目标：≤100 用户、约 5,000 请求/日（约 180 万行/年），远低于 SQLite 单文件上限；单实例单写者场景不引入 PostgreSQL/Redis 等外部依赖。

`data/` 目录现在是两类持久化的边界：

```text
data/
├── spore.db    # 业务数据库（用户/请求/用量/审计/事件/设置/Web 会话），internal/store 管理
├── session.json  # gotd/td 用户账号会话，等效账号控制权，mtproto 包以文件方式管理
├── peers.json    # Peer 的 ChannelID → AccessHash 缓存，mtproto 包以文件方式管理
└── tmp/          # 大媒体临时文件，可随时清理
```

`spore.db`（含 WAL/SHM 文件）与 session/peers/tmp 并存但互不接管：session 与 peers 缓存**不入库**，数据库业务表也**不存**它们的等价信息。

## 连接与迁移

- 打开数据库只经 `store.Open(ctx, path, log)`：连接级 PRAGMA 挂在 DSN 查询参数上（WAL、`busy_timeout=5000`、`foreign_keys=ON`、`synchronous=NORMAL`），保证连接池换连接后不丢失；连接数固定为 1，从根上避免 SQLite 单写者的写锁竞争。
- 迁移是**版本化、内嵌、只增不改**的：`migrations` 切片按序号对应 `PRAGMA user_version`，每个版本在独立事务内执行 DDL 并提交版本号，启动时自动推进、幂等。已发布的迁移脚本永不修改，新变更一律追加新条目。
- 用旧版程序打开版本更高的数据库必须报错拒绝（`STORE_MIGRATION_FAILED`），不允许降级迁移。
- `cmd/bot/main.go` 只做打开与 `defer Close` 编排，不包含业务 SQL。

## 数据范围（红线）

业务表清单：`users`、`requests`、`usage_daily`、`audit_log`、`events`、`settings`、`web_sessions`、`channel_bindings`（v24 起软解绑状态机：status active/unbound + unbind_reason/unbound_at，解绑不删行、重绑复活、unbound 行允许接管，物理删除仅管理端入口；v28 起另有 kind 聊天类型快照——channel/supergroup 绑定时记录，v28 前历史行为空，经重绑或信息刷新回填，管理端「监听转发频道」候选只列频道类型），`dump_entries`（转存频道干净副本坐标，v13；v16 起另有 `format_version` 列标记副本布局格式，查询只命中当前版本，历史行保留供审计；v24 起另有 `dump_channel_id` 列标记副本所在缓存频道——0=升级前存量永不命中，切换缓存频道后旧条目自动失效，复用自愈或管理端迁移工具重建）、`watch_sources`（监听源 /watch 配置与申请审批，v17；v18 增补类型与受理 bot 快照，listener 按 approved+enabled 过滤生效源；v28 起另有 forward_binding_channel_id 专属转发目标引用——申请人经 Bot 自助选择自己同路由的 active 绑定，绑定失效仅停止专属副本、缓存预热照常且不回退全局，物理删除绑定由外键 ON DELETE SET NULL 清引用，普通 upsert 不覆盖该策略字段；另有 global_mirror_enabled 逐源全局镜像开关——管理员源默认开保持历史行为、用户源默认关）、`watch_invite_requests`（私有邀请链接监听申请，v19：活动态 pending/waiting_telegram/waiting_bot 由对账循环推进，approved/rejected/failed 终态；v28 起另有 forward_binding_channel_id 目标暂存——「提交后立即选择」，激活时复验转移，rejected/approved 终态清空）、`watch_events`（监听转储逐次事实表，v18；只存消息/缓存坐标、路径、bot 与源快照，不存正文媒体）、`sent_messages`（引用回复锚点，v22：bot 发出消息坐标（bot_id+chat_id+message_id，坐标 bot 私有）→请求映射，/pin、/cancel 回复反查用；kind 含 channel_copy 频道副本组首坐标供事后补置顶；INSERT OR IGNORE 去重，不设 GC）。`error_logs`（错误日志中心，v25：请求管线与 Bot 相关环节错误逐条留痕——来源/环节/错误码/根因串/参数快照/request_id 归属；写入门面 internal/errlog 尽力而为 + 无归属请求 60 秒同键节流；保留天数 syscfg error_log_retention_days 周期自动清理 + 管理端手动批量/按时间段删除；只存错误文本与纯 ID 类参数，不存凭据/消息正文/媒体 URL）。

**禁止入库的数据类别**（数据范围由 schema 直接保证）：

- 消息正文、caption、Entity 内容、媒体本体（`requests` 只存 channel_key、message_id、状态、错误码与媒体诊断元数据；`dump_entries` 只存 bot 自有缓存频道内的干净副本消息坐标——与 `cloud_uploads.remote_path` 同类，不是媒体引用令牌。v12 的 `sent_chat_id`/`sent_message_ids_json` 为遗留列，自转存频道方案起停止写入）；
- Bot 运行池 Token、TG API Hash、手机号、验证码、session 内容、AccessHash 等可直接控制 Telegram 会话的凭据；`settings` 里的 Web 访问密钥只允许存 SHA-256 哈希等不可逆形式；
- 例外：Web 管理端通知通道与 GitHub OAuth 的可轮换外部服务凭据允许逐字段 AES-256-GCM 加密后存入 `settings`，根密钥必须仅来自环境变量并按用途经 HKDF 域分离；API、日志与审计不得返回密文或明文；
- 完整媒体 URL 与可直接重放用户账号的内容。

时间字段统一为 Unix 毫秒时间戳（int64），0/NULL 表示"尚未发生"；不使用字符串时间。可空文本以空字符串等价 NULL，读写由 `internal/store` 的 helper 归一，业务层不感知 NULL。

## 事务与并发

- SQLite 是单写者：保持**短事务**，禁止在事务内做网络调用或等待；连接数固定 1，写频与容量目标差三个数量级，不需要预聚合。
- 检查与扣减类操作（额度、并发数）必须在同一事务或单条 UPSERT 内完成，避免并发窗口超扣；相关改动必须配 `go test -race`。
- 简单累加/覆盖优先用单条 `INSERT ... ON CONFLICT DO UPDATE`，不引入显式事务。
- 写入失败必须返回并经 `internal/apperr` 分类（`STORE_UNAVAILABLE` / `STORE_MIGRATION_FAILED` / `STORE_CONSTRAINT`），不得吞错；"查无此行"用 `store.ErrNotFound` 区分。
- 审计日志清理必须把删除与新的清理审计放在同一事务；批量删除写 `audit.delete`，全量清除后保留一条 `audit.clear`，审计写入失败必须回滚删除，且清理审计不得复制被删记录的完整快照。

## 备份边界

- 单数据库备份/导出只覆盖业务数据库（以 SQLite 在线备份生成一致快照），不随单库备份流转 `session.json` 与 `peers.json`。管理端同时提供显式的手动 JSON 配置分项备份、所有 JSON 打包归档及全量（数据库 + JSON）备份能力，所有导出与导入均受管理员会话、CSRF 防护、防路径穿越校验与严格审计保护。
- 数据库内 AES-GCM 加密的 OAuth/通知凭据会以密文随备份流转；解密根密钥不进数据库和备份。恢复环境必须独立恢复原环境密钥，否则保留非敏感配置并要求重新填写凭据。
- 恢复数据库备份后需要重新扫码登录 MTProto，这是设计内行为，不是缺陷；若恢复包含 `session.json` 的全量备份，重启服务后可直接静默登录。
- CSV/备份导出动作必须写审计。

## R2 异地上云（internal/r2backup）

- 定时备份循环按 `backup_local_enabled`（缺省 `true`）与 R2 开关独立选择目标；本地保留关闭且 R2 启用时，在 `data/` 下生成带受控前缀的临时快照，打包上传后无论成功失败均删除，不创建 `data/backups/` 持久快照；本地快照与 R2 分别按 `backup_keep_count` 轮转。上传 ZIP 打包口径与 Web 手动全量导出共用 `backup.BuildFullZip`。
- **凭据不出机器**：R2 四项配置只落 `data/r2-backup.json`，不进 settings 表（否则随 DB 快照进备份件 = 自带钥匙）；上传版 ZIP 必须排除 `r2-backup.json` 与 `cloud-drive.json`，Web 手动全量导出口径不变。
- 远端对象名 `spore-full-backup-YYYYMMDD-HHMMSS.zip`（字典序 = 时间序），轮转 = List + 排序 + 删超份，幂等无状态；不用 R2 原生 lifecycle。
- 调度循环每轮重读配置（修改即时生效）；上传失败复用 `backup.failed` 事件（受控中文场景经 `ClassifyError` 分类，原始错误只进日志）；`last_backup_at` 记录成功生成快照时间（含 R2-only 临时快照），不因上传失败回拨。
- R2 API（GET/POST `/api/v1/backup/r2`、POST `/api/v1/backup/r2/test`）密钥只回掩码、POST 掩码/空串沿用、审计不落密钥；间隔/份数与设置 API 同键同审计（`settings.backup_interval` / `settings.backup_keep`），`backup_local_enabled` 缺省开启且默认保持旧行为；间隔为正时至少需启用本地保留或完整 R2 配置。管理端编辑入口在备份页。

## 测试

- DAO 测试一律用 `t.TempDir()` 建独立临时库，**绝不**指向真实 `data/` 或提交任何 `.db` fixture。
- 必测场景：迁移幂等（重复打开版本不变）、用户状态流转、请求阶段更新、日用量按 (user, day) 累加与重置、事件按 key 去重合并、设置读写、会话滑动续期；审计清理需验证删除范围、保留清理记录和事务回滚语义。
- 涉及共享状态或并发的改动追加 `-race`。

## 安全与权限

- `data/`、`.env`、`spore.db` 和 Session 永不提交 Git；数据库虽不含凭据，也含用户行为记录，同等保密。
- 不把 Token、Session、手机号写入日志、错误提示、审计的 before/after JSON 或文档示例；审计快照只放业务字段。
- 生产环境限制 `data/` 文件权限；容器运行用户必须能写数据库与临时目录。

## 常见错误

- 修改已发布的迁移脚本而不是追加新版本，导致新旧部署数据库结构漂移。
- 绕过 `internal/store` 直接 `database/sql` 拼 SQL，绕过 NULL/时间归一与错误分类约定。
- 在长事务里等网络回调，把 SQLite 单连接卡死（所有查询都会排队）。
- 把 `TEMP_DIR` 配成 `DATA_DIR` 或其不受控父目录，清理逻辑威胁 Session 与数据库。
- 在测试中使用真实 `data/spore.db`，污染线上行为记录。
- 为绕过包边界，在多个包各自读写数据库或各自维护一份缓存规则。

## Scenario: Web 管理增强的持久化与恢复契约

### 1. Scope / Trigger

- Trigger：Web 管理端新增用户资料同步、请求完整链接、媒体配置、GitHub OAuth 配置、备份导入和受控重启，涉及 SQLite settings、跨层 API、Secret 和启动期文件替换。
- Scope：`internal/store` 负责数据库/文件恢复与迁移；`internal/web` 负责 HTTP、认证、CSRF、审计和页面；`cmd/bot` 负责启动期应用与信号编排；`internal/mtproto` 负责生命周期内的 Telegram 用户查询。

### 2. Signatures

- `func (s *Store) UpdateUserProfile(ctx context.Context, id int64, username, displayName string) error`
- `func ValidateBackup(ctx context.Context, path string) error`
- `func ReadPendingImport(dataDir string) (PendingImport, error)`
- `func WritePendingImport(dataDir, actor string, now time.Time) (PendingImport, error)`
- `func ApplyPendingImport(ctx context.Context, dbPath, dataDir string, log *slog.Logger) (bool, error)`
- `POST /users/{id}/refresh-profile`、`GET/POST /backup` 导入动作、`POST /restart`：均要求有效 Web session；变更请求还要求 session CSRF。
- 环境变量：`WEB_OAUTH_ENCRYPTION_KEY` 可为 32 字节原文、64 位十六进制或解码后为 32 字节的 Base64；用于 AES-256-GCM，不写入数据库。

### 3. Contracts

- `users.username` 和 `users.display_name` 可为空；资料刷新成功才覆盖旧值，失败不得清空旧资料。Telegram 查询只能使用 Bot 已观察资料或已有 AccessHash，上层不得把裸 User ID 查询扩展为用户枚举。
- 请求完整 URL 从 `source_kind/channel_key/message_id` 规范化生成；公开为 `https://t.me/<username>/<id>`，私有为 `https://t.me/c/<internal-id>/<id>`。页面和 requests CSV 的 `message_url` 必须复用同一生成函数；日志仍禁止完整 URL。
- 备份只接受 Spore SQLite `.db` 快照。`ValidateBackup` 检查普通文件、SQLite 可打开、`PRAGMA integrity_check=ok`、`user_version <= 当前迁移版本` 和必需表列；迁移新增的必需列必须按备份 `user_version` 分版本校验，既拒绝“版本号已抬高但列缺失”的损坏备份，也允许真实低版本备份在导入后由当前 Store 迁移；高版本拒绝。
- 待导入文件使用 `pending-import.db`，显式确认才写 `pending-import.json`；无 `confirmed` marker 的启动不得替换数据库。应用前处理 WAL/SHM、保存 rollback 副本并原子替换。
- 导入恢复业务表，但复制当前库的访问密钥、GitHub/OAuth 和运行时 settings，清空候选库 `web_sessions`；不触碰 `session.json`、`peers.json`、`tmp/`。失败不得形成部分成功，marker 应转为 `failed` 并保留恢复线索。
- OAuth Client Secret 只能在内存中加密/解密，数据库保存密文和 nonce；页面只显示已设置/脱敏状态，审计只记动作和状态。数据库 OAuth 配置优先于旧 `GITHUB_CLIENT_ID/SECRET` 环境变量，新的 OAuth 请求即时读取。
- `max_file_size`/`stream_limit` settings 使用字节数；Web 接受 1MB–2GB，`stream_limit <= max_file_size`，官方 Bot API 模式上限 50MB。保存后下次启动覆盖环境默认值，运行中的任务不切换。

### 4. Validation & Error Matrix

| 条件 | 处理 |
| --- | --- |
| 备份不存在、非普通文件、扩展名不是 `.db` 或超过上传限制 | 拒绝、删除临时上传文件、写 `backup.import.rejected` 脱敏审计 |
| SQLite 打不开、完整性不是 `ok`、缺必要表列或版本高于当前 | 拒绝导入，当前库不变；不支持强制/部分导入 |
| 待导入 marker 缺失/状态非 `confirmed`/摘要不匹配 | 跳过或标记失败，不替换当前库 |
| 当前库 WAL/SHM 存在 | 先单连接 checkpoint，再复制 rollback；替换前清理侧文件 |
| OAuth 主密钥缺失/长度无效或密文无法解密 | Web 修改拒绝；OAuth 登录关闭但访问密钥通道保持可用 |
| 媒体值不在 1MB–2GB、阈值大于上限或官方模式超过 50MB | 拒绝保存，settings 不变 |
| Web 重启未登录、CSRF/确认字段错误或 RestartFunc 不可用 | 拒绝或返回受控不可用状态；不执行 Shell |
| 用户资料 Telegram 查询无上下文/失败 | 保留旧资料，页面显示受控原因，不创建用户 |

### 5. Good / Base / Bad Cases

- Good：先把上传写入权限为 0600 的临时文件并完整校验；确认后才写 marker；启动时 checkpoint、rollback、原子替换，成功后清 marker。
- Base：settings 使用 JSON 数值存字节；Web 输入转换后校验；新 OAuth 请求按当前 settings 读取配置；页面/CSV 共用 `requestLink`。
- Bad：直接替换运行中的 `spore.db` 而不处理 `-wal/-shm`；把备份中的旧 Web session/访问密钥恢复为当前认证状态；把 OAuth Secret 写入审计；用裸 User ID 调用 `users.getUsers`。

### 6. Tests Required

- Store：备份完整性/版本/必需列校验、低版本迁移、高版本拒绝、marker 单次应用、WAL/SHM 清理、rollback、settings 保留和 web session 清除。
- Web：上传大小/扩展名/CSRF/二次确认、失败审计、待导入页面、`message_url` 页面与 CSV 一致、媒体边界、OAuth AES-GCM/脱敏/即时生效、重启不可用与成功审计。
- Cross-layer：Bot `/start` 和请求更新资料但不改变状态/额度；MTProto offline/无 AccessHash 不查询任意用户；运行 `go test ./... -race`。
- Acceptance：隔离 `DATA_DIR` 执行导出 → 上传 → 校验 → 确认 → 重启 → 启动应用，验证业务数据恢复、当前安全 settings 保留、旧会话清除、Session 文件不变。

### 7. Wrong vs Correct

#### Wrong

```go
// 直接替换在线数据库，也没有清理 WAL/SHM 或保存回滚副本。
os.Rename(uploaded, dbPath)
```

#### Correct

```go
// 先校验并写入 pending marker；启动阶段单独 checkpoint、保存 rollback，
// 清理 SQLite 侧文件后再原子替换，失败保持原库并留下可恢复状态。
if err := ValidateBackup(ctx, candidate); err != nil {
    return err
}
_, err := WritePendingImport(dataDir, actor, now)
```

## 高风险备份失败清理契约

### 1. Scope / Trigger

- Trigger：实现或修改备份上传、候选库校验、启动期导入、rollback 副本或 SQLite WAL/SHM 处理。
- Scope：覆盖 `internal/web` multipart 临时文件与 `internal/store` 数据库副本的所有成功、失败和取消路径。

### 2. Signatures

- `func (s *Server) runBackupImportUpload(w http.ResponseWriter, r *http.Request) (notice, errMsg string)`：`ParseMultipartForm` 后负责清理 `r.MultipartForm`。
- `func copyFile(src, dst string) error`：复制或目标文件 `Close` 失败时删除不完整 `dst`。
- `func removeSQLiteSidecars(path string) error`：替换前清理 `path-wal` 与 `path-shm`。

### 3. Contracts

- 上传临时文件权限为 0600，校验/重命名成功后由所有权转移；其他返回路径删除临时文件。
- `Request.MultipartForm.RemoveAll()` 必须在解析后注册 defer，不能只关闭 `FormFile` 返回的句柄。
- rollback 副本只有完整复制并成功关闭后才可视为存在；失败不得留下可被后续流程误用的半文件。

### 4. Validation & Error Matrix

| 条件 | 处理 |
| --- | --- |
| multipart 解析失败/超限 | 受控 400，清理 parser 临时文件 |
| 校验失败/待确认保存失败 | 受控 400，删除上传临时文件，当前库不变 |
| rollback 复制或 Close 失败 | 删除目标副本，标记导入失败，不替换当前库 |
| 替换前 WAL/SHM 存在 | 单连接 checkpoint 后清理侧文件，再原子替换 |

### 5. Good / Base / Bad Cases

- Good：临时资源创建后立即注册 defer；复制使用完成标记；成功只在完整关闭后发布路径。
- Base：候选文件先落盘并 `ValidateBackup`，确认后写 marker，启动时保留 rollback。
- Bad：只 `file.Close()` 不调用 `MultipartForm.RemoveAll()`，或 `io.Copy` 失败后保留截断 rollback 文件。

### 6. Tests Required

- Web：大 multipart 上传成功/失败后断言 parser 临时文件不残留；错误响应不包含本地路径。
- Store：注入/模拟复制与关闭失败，断言目标 rollback 文件被删除；验证 WAL/SHM 清理和当前库不变。
- Full：执行 `go test ./...` 与 `go test ./... -race`。

### 7. Wrong vs Correct

#### Wrong

```go
file, _, _ := r.FormFile("backup")
defer file.Close()
// ParseMultipartForm 产生的临时文件可能残留
```

#### Correct

```go
if err := r.ParseMultipartForm(32 << 20); err != nil {
    return "", "备份上传失败，请重试。"
}
defer r.MultipartForm.RemoveAll()
```
