# 配置参考

本页是 Spore 配置项的唯一权威参考，覆盖：环境变量、SQLite 持久化设置、配置优先级与生效时机、云盘配置文件以及敏感信息边界。

其他页面只保留操作所需的最小配置示例，并链接到本页；若与本页不一致，以本页（及其对应的当前实现）为准。

## 1. 配置来源与加载方式

Spore 的配置有三个来源：

| 来源 | 载体 | 说明 |
| --- | --- | --- |
| 环境变量 | 进程环境 / `.env` 文件 | 启动时通过 `godotenv` 读取 `.env`；**已存在的进程环境变量优先于 `.env`** |
| 数据库设置 | SQLite `settings` 表及个别业务表 | 经 Web 管理端修改，部分即时生效、部分重启生效 |
| 云盘配置文件 | `DATA_DIR/cloud-drive.json` | **独立于 SQLite**，不进数据库备份，详见第 6 节 |

加载行为要点：

- 源码没有运行中重新加载 `.env` 的机制。修改 `.env` 后必须重启进程（或重建容器）才生效。
- 环境变量在启动时载入并建立默认值；数据库中的合法设置按第 3 节的规则覆盖。
- 日志输出到 stderr，没有文件日志和轮转，`LOG_LEVEL` 控制级别。

---

## 2. 环境变量总表

"生效方式"一列中，"启动"表示进程启动时确定，修改需重启。

### 2.1 必填项

| 变量 | 默认值与校验 | 生效方式 | 说明与边界 |
| --- | --- | --- | --- |
| `BOT_TOKEN` | 必填；格式为 `数字:至少20位字母/数字/_/-`（Telegram Bot Token） | 启动 | 同时供 Bot API 与 Bot 身份 MTProto 登录；不得写入日志；Bot 会话另存 `data/bot-session.json` |
| `TG_API_ID` | 必填；正整数 | 启动 | 两个 MTProto 客户端共用；申请方式见 [Telegram API 凭据](../guide/telegram-api-credentials.md) |
| `TG_API_HASH` | 必填；非空 | 启动 | 不进日志、不写入业务数据 |

### 2.2 Telegram 登录

| 变量 | 默认值与校验 | 生效方式 | 说明与边界 |
| --- | --- | --- | --- |
| `LOGIN_MODE` | `auto`；可选 `auto` / `qr` / `phone` | 启动 | 决定用户号下一轮登录方式 |
| `TG_PHONE` | 默认空 | 登录阶段 | `LOGIN_MODE=phone` 时必填；`auto` 模式下可作为扫码失败时的回退凭据；手机号属敏感配置，不进日志 |

### 2.3 用户与投递

| 变量 | 默认值与校验 | 生效方式 | 说明与边界 |
| --- | --- | --- | --- |
| `ALLOWED_USER_IDS` | 默认空；逗号分隔的正整数 | 仅首次启动 | 只在 `users` 表为空时导入为已启用用户；此后白名单以数据库为准，修改该变量不会同步 |
| `BOT_API_URL` | 默认空（官方 Bot API）；非空必须是 `http`/`https` 且含 host | 启动 | 配置后（本地 Bot API 模式）上传上限放宽到 `MAX_FILE_SIZE`，并停用 Bot 身份 MTProto 大文件直传 |
| `DUMP_CHANNEL_ID` | 默认 0（关闭）；非空解析为整数频道 ID | 见第 3 节 | 数据库设置存在时优先（包括显式 0）；管理端保存后即时影响新任务与复用 |

### 2.4 目录、媒体与日志

| 变量 | 默认值与校验 | 生效方式 | 说明与边界 |
| --- | --- | --- | --- |
| `DATA_DIR` | 默认 `data` | 启动 | 存放数据库、Session、Peer 缓存、云盘配置；变更需停机迁移全部文件后重启 |
| `TEMP_DIR` | 默认 `<DATA_DIR>/tmp`；不允许与 `DATA_DIR` 相同 | 启动 | 媒体临时文件目录；启动只清理符合任务命名规则的孤儿文件，不会清空整个目录 |
| `MAX_FILE_SIZE` | 默认 2000 MiB；上限 2000 MiB | 启动默认；数据库可覆盖但重启生效 | 下载预检与 Bot API / MTProto 路由的共同上限；云盘下载同样受限 |
| `STREAM_LIMIT` | 默认 20 MiB；正整数（管理端范围 1–2000 MiB） | 同上 | 不超过该值的媒体走流式管道 |
| `IN_MEMORY_LIMIT` | 默认 512 MiB；必须满足 `STREAM_LIMIT ≤ 值 ≤ MAX_FILE_SIZE` | 启动；**仅环境变量，无数据库覆盖** | 单文件常驻内存上限；进程总量由 `MEMORY_BUDGET` 封顶 |
| `MEMORY_BUDGET` | 默认 1 GiB；可调 64 MiB–8 GiB | 环境默认；数据库覆盖（管理端 `memory_budget`）**即时生效** | 内存管道进程级总预算：预算不足的文件自动降级临时文件路径（边下边传），常驻内存被额度封顶而不随并发任务数放大；调小只影响新打开的媒体 |
| `TEMP_DIR_MAX_SIZE` | 默认 5 GiB；管理端范围 1 MiB–1 TiB，且不得小于 `MAX_FILE_SIZE` | 数据库覆盖；重启生效 | 超限时拒绝进入临时文件下载路径 |
| `LOG_LEVEL` | 默认 `info`；可选 `debug` / `info` / `warn` / `error` | 启动 | 日志只写 stderr；源码没有文件日志与轮转配置 |

### 2.5 并发与 worker

| 变量 | 默认值与校验 | 生效方式 | 说明与边界 |
| --- | --- | --- | --- |
| `WORKER_COUNT` | 默认 1；环境变量只校验 ≥1 | 启动固定；数据库覆盖（1–16）后同样重启生效 | `.env.example` 注释写 1–16，但环境变量解析实际只有下限 |
| `DOWNLOAD_THREADS` | 默认 4；1–16 | 环境默认；数据库覆盖即时发布 | worker 在任务开始时读取，当前任务保持旧值，新任务用新值；1 为单线程 |
| `UPLOAD_THREADS` | 默认 4；1–16 | 同上 | 每次大文件上传读取快照；在途上传不被中断 |
| `DOWNLOAD_CONNECTIONS` | 默认 4；1–16 | 数据库覆盖即时影响新请求 | 在途请求不中断；底层连接池物理上限固定 16 |
| `UPLOAD_CONNECTIONS` | 默认 4；1–16 | 同上（Bot MTProto 上传侧） | 同上 |

### 2.6 Web 管理端

| 变量 | 默认值与校验 | 生效方式 | 说明与边界 |
| --- | --- | --- | --- |
| `WEB_ADDR` | 默认 `127.0.0.1:8080`；必须为 `host:port` | 启动 | Compose 部署固定覆盖为 `0.0.0.0:8080`，宿主只暴露回环端口 |
| `WEB_TRUSTED_PROXY` | 默认 `false`；接受 `1/true/yes`、`0/false/no` | 启动 | 仅影响审计来源 IP 与 OAuth 回调地址推导；不作为鉴权依据 |
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` | 默认空；必须成对配置 | 启动 | 数据库 OAuth 配置不存在时的回退；管理端保存过配置后数据库优先；修改需重启；Secret 不进日志，入库为 AES-GCM 密文 |
| `WEB_OAUTH_ENCRYPTION_KEY` | 默认空；接受 32 字节原文、64 位 hex，或解码后为 32 字节的 base64 | 启动 | 加密数据库中的 OAuth Secret 与云盘配置备份候选；更换后旧密文可能无法解密；缺失时云盘配置备份相关端点不可用（受控 503），其余功能不受影响；GitHub OAuth 配置见 [GitHub 登录](../guide/github-oauth.md) |

### 2.7 事件与通知

| 变量 | 默认值与校验 | 生效方式 | 说明与边界 |
| --- | --- | --- | --- |
| `NOTIFY_COOLDOWN_MIN` | 默认 30；正整数（分钟） | 启动 | 同一事件重复触发时的通知冷却 |
| `EVENT_BOT_FAIL_THRESHOLD` | 默认 3；正整数 | 启动 | 发送连续失败的事件阈值，成功后清零 |
| `EVENT_TASK_FAIL_THRESHOLD` | 默认 5；正整数 | 启动 | 任务连续失败的事件阈值，成功后清零 |
| `EVENT_DISK_LIMIT_GB` | 默认 1.0；有限正数（GB） | 启动 | 临时目录占用阈值；任务开始时检查，至少间隔 1 分钟 |

### 2.8 云盘

| 变量 | 默认值与校验 | 生效方式 | 说明与边界 |
| --- | --- | --- | --- |
| `RCLONE_BIN` | 默认空（在 `PATH` 中查找 `rclone`）；非空时必须存在且可执行 | 每次调用前探测 | 外部环境变化在运行进程内不可见，实际变更通常需重启；Docker 镜像已内置固定版本 rclone |

云盘目的地、凭据与开关**不在环境变量中**，见第 6 节。

---

## 3. 配置优先级与生效时机

### 3.1 优先级规则

整体规则：环境变量建立默认值 → 数据库中的合法值覆盖。

1. 以下设置在数据库中存在合法值时**覆盖环境默认**：
   - `worker_count`（合法范围 1–16）；
   - 媒体三项 `max_file_size` / `stream_limit` / `temp_dir_max_size`：三项**整体校验**，任一非法则整套回退环境配置并产生 `media.config_invalid` 事件；
   - 传输四项 `download_threads` / `upload_threads` / `download_connections` / `upload_connections`：逐键覆盖，非法、越界或损坏的值被忽略并回退环境默认；
   - `dump_channel_id`：数据库键存在即优先，**包括显式 0（关闭）**；键缺失或非法时回落 `DUMP_CHANNEL_ID`；
   - GitHub OAuth：存在合法数据库配置时优先于 `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET`。
2. `queue_capacity` 没有环境变量：数据库合法值（1–4096）覆盖默认 64。
3. `IN_MEMORY_LIMIT` 没有数据库覆盖，只能通过环境变量设置。`MEMORY_BUDGET` 有数据库覆盖（`memory_budget`，管理端改后即时生效，非法/越界值回退环境默认）。
4. `RCLONE_BIN` 不进入数据库，也不进入启动配置结构，每次调用 rclone 前按第 2.8 节规则探测。
5. 云盘配置完全独立于第 1、2 条规则（见第 6 节）。

### 3.2 即时生效与重启生效

| 变更 | 生效时机 |
| --- | --- |
| 修改 `.env` / 环境变量 | 重启进程（或重建容器） |
| `queue_capacity`、`worker_count` | 重启生效 |
| 媒体三项（`max_file_size` / `stream_limit` / `temp_dir_max_size`） | 重启生效；当前进程的媒体配置不会在线替换 |
| `timezone`、`dedup_window_min`、`system_name` | 即时（每次提交、查询或文案渲染时读取） |
| `channel_copy_enabled`、`tg_reuse_enabled`、缓存频道 ID | 即时（每次任务成功副本、复用前读取，影响新任务） |
| 受邀频道 `join_*` 六项 | 即时 |
| 传输四项 | 即时发布；细节见下 |
| 云盘配置经管理端恢复 / 回滚 | 在线生效；已在途任务沿用旧目的地快照，新任务读取新配置 |
| 数据库备份导入 | 管理端确认后，**下次启动**时应用 |
| 手工修改 `cloud-drive.json` 文件 | 没有文件监听，通常需重启 |

传输四项的即时语义：

- 下载线程数在任务开始时拷贝，当前任务保持旧值；
- 上传线程数在每次大文件上传时读取；
- 连接数限制只影响新发起的文件请求，不取消在途请求。
- 管理端的保存/清除在数据库事务内整体发布，数据库失败时内存快照不变。

---

## 4. SQLite 持久化设置

以下键保存在 SQLite 中（`DATA_DIR/spore.db` 的 `settings` 表及个别业务表），可经 Web 管理端修改：

| 键 / 数据 | 默认值 | 生效语义 |
| --- | --- | --- |
| `timezone` | `Asia/Shanghai` | 惰性读取；影响运营日分桶、额度重置与去重展示 |
| `dedup_window_min` | 10（分钟） | 即时影响普通链接去重；云盘同目的地成功复用不受该窗口限制 |
| `queue_capacity` | 64；合法 1–4096 | 重启生效（内存队列启动时构造） |
| `worker_count` | 环境默认（通常 1）；数据库合法 1–16 | 重启生效 |
| `max_file_size` / `stream_limit` / `temp_dir_max_size` | 回退环境默认（2000 MiB / 20 MiB / 5 GiB） | 重启生效；无效覆盖回退环境配置并产生事件 |
| `channel_copy_enabled` | `true` | 每次任务成功副本投递前读取，即时生效；关闭后绑定关系保留 |
| `tg_reuse_enabled` | `true` | 每次任务复用前读取，即时生效 |
| `dump_channel_id` / `dump_channel_title` | 0 / 空 | 即时生效；显式 0 可覆盖环境变量；标题仅展示 |
| `system_name` | `Spore` | 不缓存，每次读取；Bot 文案、事件标题、页面标题即时生效 |
| `join_enabled` 等 `join_*` 六项 | 关 / 关 / 需审核 / 20 / 开 / 开 | 即时生效（语义见[使用指南](../guide/usage.md)第 10 节） |
| 传输四项（数据库覆盖值） | 各自环境默认（通常 4） | 事务内整体发布；线程/连接语义见第 3.2 节 |
| `last_backup_at` | 0 | 数据库备份导出后写入 Unix 毫秒时间；仅页面状态展示 |
| `access_key_hash` | 首次启动自动生成 | 只存 SHA-256 哈希；明文仅在生成时输出一次；重置会使全部 Web 会话失效 |
| `github_binding` | 未绑定 | 只保存 GitHub 数字 ID、登录名和时间；解绑会使全部 Web 会话失效 |
| `github_oauth_config` | 无 | Client Secret 以 AES-GCM 密文入库；数据库配置优先于环境变量 |
| `users.cloud_download`（用户表列） | 0（跟随角色） | 即时生效；owner 默认允许、普通用户默认拒绝；显式允许/拒绝优先，且与全局云盘开关同时成立才可用 |
| `web_sessions` | 登录后写入 | 会话 ID 只存哈希；数据库导入会清空全部会话 |
| `system_metric_samples` | 保留 48 小时 | 每 30 秒写入聚合样本，每小时清理过期数据 |

---

## 5. 管理端设置页与字段映射

| 管理端页面 | 对应设置 |
| --- | --- |
| 运行设置（`/admin/settings`） | `timezone`、`queue_capacity`、`worker_count`、媒体三项、传输四项；展示配置值/运行值差异与待重启原因 |
| 系统设置（`/admin/settings/system`） | `system_name` |
| GitHub 登录（`/admin/settings/oauth`） | `github_oauth_config`、`github_binding` |
| 频道设置（`/admin/channel-settings`） | `channel_copy_enabled`、缓存频道（ID 与标题）、`tg_reuse_enabled`、`dedup_window_min` |
| 受邀设置（`/admin/join-settings`） | `join_*` 六项 |
| 云盘下载（`/admin/cloud-drive`） | `cloud-drive.json`：全局开关、默认目的地、目的地列表与凭据（见第 6 节） |
| 数据备份（`/admin/backup`） | 数据库导出/导入；展示 `last_backup_at` 与待应用的导入候选 |
| 用户详情（`/admin/users/:id`） | 用户限额四项（间隔/额度/并发/绑定上限）、`cloud_download` 三态 |

管理端 API 的请求与响应细节见 [API 参考](./api.md)。

---

## 6. 云盘配置文件（cloud-drive.json）

云盘目的地是**全局**配置，存储在 `DATA_DIR/cloud-drive.json`，独立于 SQLite：

- 文件权限 `0600`，写入为临时文件加重命名的原子操作。
- **不进入数据库备份**。数据库备份只包含 SQLite 业务库；迁移云盘配置需单独备份本文件，或使用管理端的云盘配置备份。
- 内容为全局开关、默认目的地和目的地列表；每个目的地包含名称、类型、路径前缀和 rclone options。
- 名称必须以小写字母开头，只含小写字母、数字和连字符（最长 32 位）且唯一；路径前缀不能是绝对路径、不能包含 `..`。
- 管理端保存为**全量替换**并即时生效；`enabled=false` 时可保存不完整草稿。
- 外部手工修改文件没有热加载，通常需重启。
- 管理端的云盘配置备份为加密 ZIP：导入分两阶段（先校验生成候选，确认后整体替换），替换在线生效并保留最近一次回滚点。
- 目的地凭据只在调用 rclone 时以 `RCLONE_CONFIG_*` 环境变量传给子进程，不写日志、不写错误文本。

云盘的目的地操作、已验证的网盘类型和排障见[下载功能](../guide/download.md)。

---

## 7. 敏感信息边界

| 数据 | 存储与传递方式 |
| --- | --- |
| Web 访问密钥 | `settings` 只存 SHA-256 哈希；明文只在首次启动（或 CLI 重置）时输出一次；重置会使全部 Web 会话失效 |
| GitHub OAuth Secret | 以 AES-GCM 密文入库；根密钥 `WEB_OAUTH_ENCRYPTION_KEY` 只来自环境变量，不进数据库、不进备份 |
| 网盘凭据（rclone options） | 保存在 `cloud-drive.json`（非整体加密）；API 返回与审计中敏感键掩码；调用 rclone 时仅以 `RCLONE_CONFIG_*` 子进程环境变量传递 |
| 用户号 / Bot 号 MTProto Session | `data/session.json`、`data/bot-session.json`，不进数据库、不进数据库备份 |
| Peer 缓存 | `data/peers.json`，同上；丢失后按需重建 |
| 媒体临时文件 | `TEMP_DIR` 内，任务结束（成功、失败、取消）即清理 |

已知脱敏边界（如实记录，不做绝对承诺）：

- 错误文本中的凭据替换逻辑只处理**长度至少 4 字符**的 option 值；短于 4 字符的值不会被替换。
- `cloud-drive.json` 本身不是加密存储；API 掩码不等于磁盘加密，文件安全依赖目录权限与宿主安全。
- 数据库备份不含云盘凭据、Session、Peer 缓存与 `.env`；反之，云盘配置备份也不包含数据库内容。

---

## 8. 相关页面

- 首次部署与 `.env` 最小配置：[部署指南](../guide/deployment.md)
- 云盘目的地配置与运维：[下载功能](../guide/download.md)
- 升级、备份恢复与迁移操作：[运维手册](../ops/operations.md)
- 管理端设置相关 API：[API 参考](./api.md)
- 处理链路与模块职责：[架构](./architecture.md)
