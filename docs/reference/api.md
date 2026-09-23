# Spore 管理端 API 参考

本文档描述 Web 管理端的 JSON API（`/api/v1`）与配套功能端点的当前契约，字段名即 JSON tag，以 `internal/web/` 当前实现为准持续维护。各接口读写的数据库表见[数据库设计参考](./database-schema.md)的映射表。

> **维护约定**：`/api/v1` 及相关端点发生**新增、修改、删除**时，必须同步更新本文件，保持文档与实现一致。

## 目录

- [统一约定](#统一约定)
- [1. 公开端点（无认证）](#_1-公开端点-无认证)
- [2. 会话与总览](#_2-会话与总览)
- [3. 用户管理](#_3-用户管理)
- [4. 申请审批](#_4-申请审批)
- [5. 请求记录](#_5-请求记录)
- [5a. 云盘下载](#_5a-云盘下载)
- [5b. 缓存补写（转存缓存频道）](#_5b-缓存补写-转存缓存频道)
- [5c. 云盘配置备份与恢复](#_5c-云盘配置备份与恢复)
- [6. 频道统计](#_6-频道统计)
- [6a. 频道加入（受邀频道）](#_6a-频道加入-受邀频道)
- [7. 事件中心](#_7-事件中心)
- [8. 审计日志](#_8-审计日志)
- [9. 运行设置](#_9-运行设置)
- [9a. 系统设置（系统身份）](#_9a-系统设置-系统身份)
- [9b. 通知设置](#_9b-通知设置)
- [10. GitHub OAuth 配置](#_10-github-oauth-配置)
- [11. 数据备份](#_11-数据备份)
- [12. 系统（重启 / MTProto）](#_12-系统-重启-mtproto)
- [13. 功能端点（非 JSON）](#_13-功能端点-非-json)
- [附录：错误码总表](#附录-错误码总表)

---

## 统一约定

### Base URL 与传输

- 管理端唯一入口是 SPA（`/admin`），全部业务变更经 `/api/v1` JSON API，不存在服务端 HTML 页面。
- 除注明"公开"外，所有端点都需要会话认证；未认证返回 `401` JSON，绝不重定向到 HTML。
- 请求/响应编码 `application/json; charset=utf-8`；JSON 请求体上限 **64KB**（备份上传走 multipart 专用上限）。
- 缓存头按响应类型区分：列表/详情等读取响应与文件下载（CSV、二维码、备份 ZIP/`.db`）统一携带 `Cache-Control: no-store`，禁止任何缓存层留存；部分以 JSON 直接返回的写操作成功响应（如用户/设置/云盘配置保存）当前未统一携带该头。

### 认证与会话

| 项 | 值 |
| --- | --- |
| 会话 Cookie | `spore_session`（HttpOnly / Secure / Lax），有效期 12h，滑动续期 |
| 登录 CSRF Cookie | `spore_login_csrf`（双提交，Secure / HttpOnly / Lax，2h） |

登录级 CSRF 与会话级 CSRF 是**两套独立机制**：

- **登录前**（无会话）：先 `GET /api/v1/login/csrf` 取 token，`POST /api/v1/login` 时经 `X-CSRF-Token` 头回传（与服务端 Cookie 比对）。
- **会话内**：`GET /api/v1/session` 引导时下发 `csrf_token`，此后所有**写操作**（POST / PUT / DELETE）都带 `X-CSRF-Token` 头；缺失或不匹配返回 `403 CSRF_FAILED`。

### 统一错误信封

所有 JSON 错误响应结构一致：

```json
{ "error": { "code": "STABLE_CODE", "message": "受控中文提示" } }
```

`code` 是稳定的大写下划线字符串（见[附录](#附录-错误码总表)）；`message` 面向管理员展示，不含底层错误细节、Session、Secret 或 token。业务错误的状态映射：查无此行 `404`、存储约束/状态冲突 `409`、存储不可用/队列饱和 `503`、其余 `500`。

### 分页信封

列表端点统一返回：

```json
{ "items": [...], "page": 1, "page_size": 50, "total": 100, "total_pages": 2 }
```

查询参数：`page`（≥1，缺省 1）、`page_size`（1–200，缺省 50）；非法值返回 `400`（API 不做静默收敛）。`items` 为空时是 `[]` 而非 `null`。

### 字段语义

- 时间戳一律 **Unix 毫秒**，`0` 表示尚未发生。
- 状态/级别/错误原因等字段下发 raw 码（如 `status: "enabled"`），中文标签由前端统一转换。
- 比率字段（`success_rate`、`error_rate`、`ratio`）取值 `[0,1]`，无样本时为 `0`（趋势点的 `error_rate` 无样本时为 `null`）。
- 空串表示"未记录"（如 `media_type`）。
- 写操作成功响应统一携带 `"ok": true`，可附加操作摘要字段。
- 时间范围筛选（`since`/`until`）为**运营时区**的 `YYYY-MM-DD` 日期：`since` 取当日 00:00 起、`until` 取当日全天（含）。非法格式或 `since > until` 返回 `400`。
- 未匹配的 `/api/v1` 路径或方法返回 JSON `404`/`405`，不会收到 HTML。

---

## 1. 公开端点（无认证）

### GET /healthz

进程存活探针。响应 `200` `{"status":"ok"}`，无鉴权、无状态、不含敏感信息。

### GET /readyz

业务就绪探针：配置已加载 + 数据库可读写（一次 settings 读取验证，2s 超时）。`200` `{"status":"ready"}` / `503` `{"status":"unavailable"}`。

### GET /api/v1/login/csrf

公开。签发或复用登录级双提交 CSRF Cookie（`spore_login_csrf`），返回 token。

| 响应字段 | 类型 | 说明 |
| --- | --- | --- |
| `csrf_token` | string | 登录 CSRF token，`POST /api/v1/login` 时经 `X-CSRF-Token` 回传 |
| `github_enabled` | bool | GitHub 登录通道是否已配置（控制登录页入口显隐） |

### POST /api/v1/login

公开。访问密钥登录：限流 → 登录 CSRF → 密钥哈希常数时间比对 → 签发会话。

请求体：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `access_key` | string | 是 | 管理访问密钥（首次启动打印在日志中） |

响应 `200` `{"ok":true}`，同时种下 `spore_session` Cookie。

错误：`429 WEB_LOGIN_LOCKED`（IP/全局限流锁定）、`403 WEB_CSRF_INVALID`、`400`（请求体非法/缺密钥）、`401 WEB_AUTH_FAILED`（密钥错误）、`500 STORE_UNAVAILABLE`（读取访问密钥哈希失败）。

### GET /auth/github

公开。发起 GitHub OAuth 登录：`302` 跳转 GitHub 授权页（scope `read:user`，服务端签发单次 state，10 分钟有效）。通道未配置时 `302 /admin/settings/oauth?oauth=error`。

### GET /auth/github/callback

公开。OAuth 回调：校验 state 并与 GitHub 交换账号。

- **登录模式**成功：`302 /admin`；失败：`302 /admin/settings/oauth?oauth=error`。
- **绑定模式**成功：`302 /admin/settings/oauth?oauth=bound`（绑定会使全部会话失效，需重新登录）；失败：`?oauth=error`。

跳转参数只含固定枚举，不透传 code/state/错误原文。

---

## 2. 会话与总览

### GET /api/v1/session

会话引导（认证）。返回登录态、当前用户与会话级 CSRF token。

| 响应字段 | 类型 | 说明 |
| --- | --- | --- |
| `authenticated` | bool | 恒为 `true`（未认证到不了这里） |
| `user.name` | string | `"admin"`（单管理员模型） |
| `user.github_login` | string | GitHub 绑定登录名；未绑定时省略 |
| `csrf_token` | string | 会话级 CSRF token，写操作经 `X-CSRF-Token` 回传 |
| `expires_at` | int64 | 滑动续期后的会话过期时间（Unix 毫秒） |

### POST /api/v1/session/logout

登出（认证 + CSRF）。删除当前会话、清除 Cookie 并写审计。响应 `{"ok":true}`；前端随后跳转 `/admin/login`。

### GET /api/v1/overview

总览页实时快照（认证）。无查询参数（时间范围类指标已拆至 `GET /api/v1/stats`）。

响应 `apiOverviewView`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `version` | string | 应用版本（CI 构建经 -ldflags 注入 git tag/SHA；未注入时回退内置缺省） |
| `started_at` | int64 | 进程启动时间（Unix 毫秒） |
| `addr` | string | Web 监听地址 |
| `workers` | int | 媒体处理 worker 数 |
| `health` | object | 服务健康快照，见下 |
| `queue` | object \| 缺省 | 内存队列指标 `{len, cap}`；队列未注入时省略 |
| `bot` | object \| 缺省 | 接入的 Bot API 机器人身份（主 bot）：`{id, name, username}`（getMe 快照；`name` 为 first_name，`username` 不含 `@`）；Bot 未就绪或身份查询未成功时整体省略，前端显示"未接入" |
| `bots` | array \| 缺省 | 多机器人池全部成员 `{id, name, username, primary, online, conflict, paused}`（装配顺序，主 bot 在前；`conflict` 消息拉取冲突，`paused` 管理端手动暂停——均表示该 bot 暂不接收新消息）；空池或旧后端省略 |
| `requests` | object | 请求行状态计数 `{queued_rows, processing_rows}`（全时段当前值） |
| `users` | object | 用户状态计数 `{total, enabled, pending, disabled, archived}` |
| `join` | object | 频道加入全时段快照，见下 |

`health`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `store_ok` | bool | 数据库连通性 |
| `mtproto_state` | string | `ready \| login_pending \| offline \| unknown`（raw） |
| `mtproto_error` | string | 最近一次 MTProto 错误；无则省略 |
| `db_size_bytes` | int64 | 数据库文件大小 |
| `db_path` | string | 数据库文件路径 |
| `temp_dir_bytes` | int64 | 临时目录占用 |
| `temp_dir` | string | 临时目录路径 |
| `github_configured` | bool | GitHub 通道是否已配置 |

`join`（全时段快照，不随时间范围变化；join 功能关闭时历史数据照常下发）：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `pending` / `approved` / `rejected` / `failed` | int | 加入申请各状态计数 |
| `active_joined` | int | 当前加入中的频道总数（全部来源合计） |
| `external_active` | int | 外部拉入且当前仍加入的频道数 |
| `left_total` | int | 已退出频道总数（全部来源合计） |
| `max_channels` | int | 加入数量上限（syscfg；0 = 不限） |
| `source_dist` | array | 来源分布固定五行 `{key: join_command\|approved\|external\|watch_source\|bind_resolve, count}`（含 0；count 为该来源当前加入数） |

错误：`500/503`（存储类，经统一映射）。

### GET /api/v1/version/check

检查更新（认证）。把当前服务版本与上游最新发布（GitHub Releases）比较，供总览页"服务版本"旁的刷新按钮与升级提示使用。查询参数 `force=1`（可选）：跳过服务端缓存直接查询上游（手动刷新按钮使用）；缺省命中缓存窗口（1 小时），`checked_at` 为原始查询时间（可能早于本次请求）。

响应：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `current_version` | string | 当前服务版本（构建期注入；未注入时为内置缺省） |
| `latest_version` | string | 上游最新发布 tag（如 `v1.2.0`） |
| `status` | string | 比较结论：`up_to_date` \| `outdated` \| `unknown`（`unknown` = 当前版本无法解析为纯数字点分格式，如 `dev` 构建，此时仍下发 `latest_version` 供自行判断） |
| `release_url` | string | 上游发布页链接 |
| `checked_at` | int64 | 查询时间（Unix 毫秒，来自服务端缓存时为原始查询时间） |

错误：`503`（上游查询失败——网络不可达/被限流，或版本检查通道未注入；受控错误码 `SERVICE_UNAVAILABLE`）；响应 `Cache-Control: no-store`。

### GET /api/v1/system-metrics

系统资源与传输监控（认证）。查询参数 `range` 可选值为 `realtime`、`4h`、`1d`，缺省为 `realtime`。

| 响应字段 | 类型 | 说明 |
| --- | --- | --- |
| `range` | string | 实际档位：`realtime` / `4h` / `1d` |
| `since` / `until` | int64 | 滚动时间边界（Unix 毫秒，`until` 为开区间） |
| `sample_interval_ms` | int64 | 当前档位的采样/聚合间隔 |
| `points` | array | `{at, rss_bytes, temp_dir_bytes, download_bytes_per_second, upload_bytes_per_second, cpu_percent}`；探针不可用时对应值为 `null`。`cpu_percent` 为进程 CPU 占用率（0–100，占全部核心） |

`realtime` 返回最近 15 分钟内存采样（约 2 秒粒度）；`4h` 返回最近 4 小时 30 秒持久化样本；`1d` 返回最近 24 小时按 2 分钟桶聚合的样本。速率为全服务活跃任务聚合 bytes/s，上传值表示发送方已消费字节而非远端确认。无样本时 `points` 为空，不用 0 补齐。

错误：`400`（range 非法）、`503`（监控服务未接入）、`500/503`（存储类，经统一映射）；响应 `Cache-Control: no-store`。

### GET /api/v1/stats

业务统计页数据（认证）。查询参数：`since`/`until`（`YYYY-MM-DD`，可选；缺省运营时区近 7 天含当天）；`all=1`（可选；全量统计，忽略 `since`/`until`，`since_day`/`until_day` 回显空串）；`bot_id`（可选；多机器人池限定受理 bot，正整数）。

响应 `apiStatsView`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `since_day` / `until_day` | string | 实际生效的时间范围回显（`YYYY-MM-DD`）；全量模式为空串 |
| `requests` | object | 时间范围内的请求指标与图表数据，见下 |

`requests`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `total` / `succeeded` / `failed` / `unfinished` | int | 时间范围内的请求指标 |
| `active_users` | int | 时间范围内活跃用户数 |
| `success_rate` / `error_rate` | float | `[0,1]`，无终态时为 0 |
| `trend` | array | 按日趋势 `{day, total, succeeded, failed, error_rate(可 null)}`，范围内空日期补齐 |
| `top_channels` | array | Top 5 频道 `{key, total, succeeded, failed, success_rate, last_requested_at}` |
| `top_users` | array | Top 10 用户 `{id, username, display_name, total, succeeded, failed, success_rate, last_requested_at}` |
| `media_dist` | array | 媒体类型分布 `{key, count}` |
| `delivery_dist` | array | 投递方式分布 `{key, count}`；按 `delivery_mode` 分组（全部状态，与 `media_dist` 口径一致） |
| `error_dist` | array | 错误原因排行 `{key, count, ratio}`；Top 5 之外合并为 `key: "__other__"` |
| `dc_dist` | array | 源媒体所在 Telegram DC 分布 `{key, count}`；`key` 为 DC ID 十进制串，空串 = 未记录；跨多个 DC 的请求在每个 DC 各计一次 |
| `dc_trend` | array | 按日源媒体 DC 分布 `{day, dist: [{key, count}]}`；范围内空日期补齐（`dist` 为空数组）；全量模式只含有数据日期 |
| `bot_dist` | array | 按受理 bot 的分布 `{bot_id, bot_username, total, succeeded, failed, last_requested_at}`（多机器人池；不受 `bot_id` 筛选影响，展示全量分布；`bot_id=0` 为存量行/非 Bot 通道创建，前端显示"未知"） |

错误：`400`（日期格式、since 晚于 until）、`500/503`（存储类，经统一映射）；分布类聚合失败时留空并记 Warn（尽力而为）。

---

## 3. 用户管理

### GET /api/v1/users

用户列表（认证）。查询参数：

| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `q` | string | 搜索：ID 精确/前缀、用户名/显示名/备注包含（大小写不敏感） |
| `status` | string | `pending \| enabled \| disabled \| archived` |
| `page` / `page_size` | int | 分页 |

行结构 `apiUserRow`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | int64 | Telegram 用户 ID |
| `username` / `display_name` / `note` | string | 资料 |
| `status` | string | raw 状态码 |
| `is_owner` | bool | 是否 owner |
| `last_used_at` | int64 | 最近使用时间（0 = 从未） |
| `total_requests` | int | 累计请求数（不限时间范围） |
| `has_total_requests` | bool | 聚合无该用户行时为 `false`（区分 0 与无记录） |
| `cloud_download` | int | 用户级云盘下载权限 raw 三态（`0` 跟随角色默认 / `1` 显式允许 / `2` 显式拒绝） |
| `effective_cloud_download` | bool | 生效的云盘下载权限；`cloud_download` 为 0 时按角色解析（owner `true` / 普通用户 `false`） |

### GET /api/v1/users/\{id\}

用户详情（认证）。`apiUserDetail`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` / `username` / `display_name` / `status` / `is_owner` / `note` | — | 同列表行 |
| `created_at` / `first_used_at` / `last_used_at` / `archived_at` | int64 | 各阶段时间（0 = 未发生） |
| `last_denied_at` | int64 | 最近拒绝时间 |
| `last_denied_reason` | string | 最近拒绝的 raw 错误码 |
| `last_denied_text` | string | 拒绝码对应的受控中文文案 |
| `used_today` / `daily_limit` / `remaining_today` | int | 当日用量与剩余额度（运营时区日） |
| `submit_interval_sec` / `concurrent_limit` | int | 提交间隔（秒）与并发上限 |
| `bind_limit` | int | 用户级频道绑定数量上限 raw 值（0 = 跟随角色默认） |
| `effective_bind_limit` | int | 生效的频道绑定数量上限；`bind_limit` 为 0 时按角色解析（普通 1 / owner 3） |
| `cloud_download` | int | 用户级云盘下载权限 raw 三态（`0` 跟随角色默认 / `1` 显式允许 / `2` 显式拒绝） |
| `effective_cloud_download` | bool | 生效的云盘下载权限；`cloud_download` 为 0 时按角色解析（owner `true` / 普通用户 `false`）。仅约束 Bot `/download`，与全局开关（cloud-drive.json `enabled`）是 AND 关系；管理端补存不受限 |
| `total_requests` | int | 累计请求数 |

错误：`400`（ID 非法）、`404`。

### POST /api/v1/users

手动添加用户（认证 + CSRF），默认 `enabled`。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `user_id` | int64 | 是 | Telegram 用户 ID，正整数 |
| `note` | string | 否 | 备注 |

响应 `{"ok":true,"user_id":<id>}`。错误：`409 STORE_CONSTRAINT`（该用户已存在）。

### POST /api/v1/users/{id}/limits

调整限额（认证 + CSRF）。前三项均可缺省（保持原值）；提供值必须为正整数且不超上限。

| 请求字段 | 类型 | 上限 | 说明 |
| --- | --- | --- | --- |
| `submit_interval_sec` | int | 86400 | 提交间隔（秒） |
| `daily_limit` | int | 100000 | 每日额度 |
| `concurrent_limit` | int | 100 | 并发上限 |
| `bind_limit` | int | 20 | 频道绑定数量上限；`0` = 恢复跟随角色默认（普通 1 / owner 3），缺省保持不变 |

响应 `{"ok":true}`。错误：`400`（取值非法，message 列出具体字段）、`404`。

### POST /api/v1/users/{id}/enable · /disable · /archive · /restore

状态流转（认证 + CSRF），无请求体。`restore` 即重新启用。响应 `{"ok":true,"status":"<目标状态>"}`。错误：`404`；`409 STORE_CONSTRAINT`（owner 不能被停用/归档，须先转移 owner）；状态未变化时幂等成功。

### POST /api/v1/users/{id}/reset-quota

重置当日已用额度（认证 + CSRF）。响应 `{"ok":true}`。错误：`404`。

### POST /api/v1/users/{id}/set-owner

设置/取消 owner（认证 + CSRF）。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `owner` | bool | 是 | 设为 owner 后会补发暂存事件 |

响应 `{"ok":true,"is_owner":<bool>}`。错误：`400`（缺字段）、`404`。

> 本节全部管理写操作依赖 access 服务：该可选依赖未注入时统一返回 `503 SERVICE_UNAVAILABLE`（其余各节有同类可选依赖的端点亦同）。

### POST /api/v1/users/{id}/cloud-download

设置用户级云盘下载权限（认证 + CSRF）。即时生效（下一次 `/download` 预检与 Submit 复核按新值校验）；owner 可被显式拒绝，无豁免。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `cloud_download` | int | 是 | `0` = 跟随角色默认（owner 允许 / 普通用户拒绝）、`1` = 显式允许、`2` = 显式拒绝 |

响应 `{"ok":true,"cloud_download":<mode>,"effective_cloud_download":<bool>}`。写 `user.set_cloud_download` 审计（记录前后三态值）。错误：`400`（缺字段或取值非法）、`404`。

### POST /api/v1/users/{id}/refresh-profile

从当前可用 Telegram 上下文刷新用户资料（认证 + CSRF）。响应 `{"ok":true}`。错误：`503 TELEGRAM_UNAVAILABLE`（Telegram 通道未就绪，保留原资料）、`404`。

---

## 4. 申请审批

### GET /api/v1/applications

待审批申请列表（认证）。**不分页**，按申请先后（ID 升序）。

```json
{ "items": [{ "id": 123, "username": "...", "display_name": "...", "applied_at": 1700000000000 }] }
```

`applied_at` 即用户创建时间（Unix 毫秒）。

### POST /api/v1/applications/{id}/approve · /reject

批准 / 拒绝申请（认证 + CSRF），无请求体。

响应：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `ok` | bool | `true` |
| `notified` | bool | Bot 通知是否成功；`false` 时通知失败不回滚审批，管理员需手动跟进 |
| `status` | string | 审批后的用户状态（approve → `enabled`，reject → `disabled`） |

错误：`400`（ID 非法）、`404`。

---

## 5. 请求记录

### GET /api/v1/requests

消息记录列表（认证）。查询参数：

| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `user_id` | int64 | 正整数 |
| `bot_id` | int64 | 正整数；多机器人池限定受理 bot（缺省 = 全部） |
| `status` | string | `queued \| processing \| succeeded \| failed \| cancelled` |
| `channel` | string | 频道标识 |
| `media_type` | string | 媒体类型；多成员 Telegram 相册统一为 `album` |
| `delivery_mode` | string | 投递方式：`upload \| mixed \| text \| cloud \| reuse \| dump \| split`（`mixed` 为历史遗留值，仅旧记录使用；`reference`（源引用直发）机制已移除，仅历史记录可能保留该值；`split` 为分卷拆分投递） |
| `error_code` | string | 错误码 |
| `since` / `until` | string | 运营时区 `YYYY-MM-DD` |
| `page` / `page_size` | int | 分页 |

行结构 `apiRequestRow`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` / `user_id` | int64 | 记录与用户 ID |
| `username` | string | 所属用户的用户名（可为空） |
| `display_name` | string | 所属用户的显示名（可为空） |
| `source_kind` | string | `public \| private` |
| `channel_key` | string | 频道标识（私有频道为 `-100` 前缀内部 ID） |
| `message_id` | int | 源消息 ID |
| `message_url` | string | 规范化原始消息链接（`t.me/...`；结构化字段异常时为空串） |
| `status` | string | raw 状态码 |
| `attempt` | int | 已尝试次数 |
| `error_code` | string | 失败原因 raw 码 |
| `media_type` | string | 空串 = 未记录（失败于消息转换前）；多成员相册为 `album` |
| `media_types` | string[] | 请求包含的去重媒体类型；相册用于区分 `photo`、`video` 或二者组合，caption 不参与；旧记录为空数组 |
| `source_media_dc_ids` | int[] | 源媒体所在 Telegram DC ID 去重列表；文本、旧记录或未知时为空数组；不表示消息或用户地理位置 |
| `delivery_mode` | string | `upload \| mixed \| text \| cloud \| reuse \| dump \| split`（`mixed` 为历史遗留值；`reference`（源引用直发）已移除，仅历史记录可能保留；`cloud` 为云盘下载：`/download` 指令或管理端补存创建；`reuse` 为缓存频道干净副本复用命中；`dump` 为管理端缓存补写创建；`split` 为分卷拆分投递：超过单文件上限的媒体切段后经相册整组送达） |
| `bot_id` | int64 | 受理 bot 的 Telegram 账号 ID（多机器人池归属）；`0` = 存量行/非 Bot 通道创建，前端显示"—" |
| `bot_username` | string \| 缺省 | 受理时的 bot 用户名快照（不含 `@`）；空串省略 |
| `requested_at` | int64 | 请求时间 |
| `duration_ms` | int64 | 总耗时 |

### GET /api/v1/requests/\{id\}

请求详情（认证）：在列表行基础上追加：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `channel_link_text` | string | 链接显示文本（频道标识#消息ID） |
| `username` | string | 所属用户的用户名（可为空） |
| `display_name` | string | 所属用户的显示名（可为空） |
| `error_text` | string | 错误码对应的受控中文文案 |
| `attempt_max` | int | 尝试上限（累计含首次） |
| `file_name` / `file_size` | string / int64 | 媒体文件名与大小 |
| `queued_at` / `started_at` / `finished_at` | int64 | 各阶段时间（0 = 未发生） |
| `parent_request_id` | int64 | 补存来源：云盘补存新建的请求行指向原请求 ID；普通请求为 `0` |
| `cloud_uploads` | array | 云盘上传记录（该请求经 `/download` 或补存上传网盘的逐文件结果），见下；无记录时为 `[]` |

`cloud_uploads` 行：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `destination` | string | 目的地名称 |
| `remote_path` | string | 远端完整路径（不含目的地前缀） |
| `file_name` | string | 文件名（空串 = 未记录） |
| `status` | string | `uploading \| succeeded \| failed` |
| `error_code` | string | 失败原因 raw 码；成功时为空串 |
| `bytes` | int64 | 已上传字节数 |
| `created_at` / `finished_at` | int64 | 开始/结束时间（0 = 未发生） |

### POST /api/v1/requests/{id}/retry

受控重试（认证 + CSRF），无请求体。仅失败状态的请求可重试；不扣用户额度。响应 `{"ok":true}`。累计尝试上限为动态配置 `max_request_attempts`（默认 3，可配 1–10，见[配置参考](./configuration.md)）。

错误：`404`；`409 STORE_CONSTRAINT`（仅失败状态可重试）、`409 RETRY_EXHAUSTED`（已达当前配置的最大尝试次数）、`409 USER_DISABLED`（所属用户未启用）；`503 QUEUE_FULL`（内存队列已满）。

### POST /api/v1/requests/{id}/reset_attempts

重置单条请求的累计尝试计数（认证 + CSRF），无请求体。仅失败状态的请求可重置：`attempt` 清回 1，状态、错误码与时间戳保持不变，**不自动重新入队**（清零后经 retry 端点显式重试）；计数已为 1 时幂等成功且不写审计。响应 `{"ok":true}`。

错误：`404`；`409 STORE_CONSTRAINT`（仅失败状态可重置）。

### POST /api/v1/requests/{id}/cancel

取消单条 queued/processing 请求（认证 + CSRF），无请求体。响应 `{"ok":true}`。错误：`404`；`409 STORE_CONSTRAINT`（"该请求已结束或已取消"）。

### POST /api/v1/requests/cancel

批量取消（认证 + CSRF）。只处理调用方明确勾选的 ID，不扩展筛选范围；单条结果独立，互不回滚。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `ids` | int64[] | 是 | 1–50 个正整数 |

响应：

```json
{ "ok": true, "results": [{ "id": 1, "result": "cancelled", "error_code": "" }] }
```

`result` 为单条取消结果：`cancelled`（成功）、`conflict`（已结束或已取消）、`not_found`（不存在）、`failed`（存储失败，带 `error_code`）；`error_code` 仅失败时有值。错误：`400`（数量越界/ID 非法）、`503 STORE_UNAVAILABLE`（取消控制器未接入）。

### POST /api/v1/requests/{id}/delete

删除单条请求记录（认证 + CSRF）。**仅终态记录可删**（succeeded/failed/cancelled）。响应 `{"ok":true}`。错误：`404`；`409 STORE_CONSTRAINT`（记录尚未结束）。

### POST /api/v1/requests/delete

批量删除请求记录（认证 + CSRF）。请求体为 `{"ids":[1,2]}`，接受 1–50 个正整数，只处理明确提交的 ID。每条记录独立复用单条终态检查、删除和审计事务，允许部分成功并保持输入顺序。

响应：

```json
{ "ok": true, "results": [{ "id": 1, "result": "deleted" }] }
```

`result` 为 `deleted`、`conflict`（未终态）、`not_found` 或 `failed`；失败项可带 `error_code`。错误：`400`（数量越界/ID 非法）。

---

## 5a. 云盘下载

`/download` 指令（媒体直接上传网盘）与「存到网盘」补存功能的配置与操作端点。云盘功能由配置文件 `data/cloud-drive.json` 的全局开关 `enabled` 控制（默认关闭，管理端「云盘下载」页编辑，写入凭据不进数据库）；补存端点要求功能已开启且 rclone 二进制可用（镜像内置固定版本，可用 `RCLONE_BIN` 覆盖；不可用时补存端点直接拒绝）。`cloud.disabled` 事件由进程启动探测与周期复查产生，不是补存端点的行为。网盘注册、参数对照与运维说明见 [operations.md](../ops/operations.md) 云盘下载章节。

### GET /api/v1/cloud-drive

读取云盘下载配置与运行状态（认证）。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `enabled` | bool | 云盘下载全局开关（默认 `false`；关闭时授权用户 `/download` 收「云盘下载功能未开启」，裸链接不受影响） |
| `default_destination` | string | 默认目的地名；`/download` 不指定目的地时使用 |
| `rclone_available` | bool | rclone 二进制是否可用 |
| `destinations` | array | 目的地列表，见下 |

`destinations` 行：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `name` | string | 目的地唯一名称，`^[a-z][a-z0-9-]{0,31}$`（禁 `_`，避免 rclone 环境变量映射撞名） |
| `type` | string | rclone 后端类型（如 `mega`、`s3`；`webdav` 为未来预留） |
| `path_prefix` | string | 远端路径前缀 |
| `enabled` | bool | 该目的地是否启用 |
| `options` | object | rclone 后端参数键值对；响应中的 `pass`/`2fa`/`secret`/`token`/`key` 等敏感值统一为 `********` 掩码，未修改时 PUT 原样回传该掩码即可保留服务端原值；新凭据须提交实际值，管理端提交 MEGA 原始 `pass` 后服务端会自动执行 `rclone obscure` |

示例（`destinations` 等字段与配置文件 `data/cloud-drive.json` 同构）：

```json
{
  "enabled": false,
  "default_destination": "mega-1",
  "rclone_available": true,
  "destinations": [
    { "name": "mega-1", "enabled": true, "type": "mega",
      "path_prefix": "spore",
      "options": { "user": "...", "pass": "********", "2fa": "********" } }
  ]
}
```

### PUT /api/v1/cloud-drive

保存云盘下载配置（认证 + CSRF）。请求体与 GET 视图同构（`rclone_available` 为服务端探测值，不接受提交）；服务端全量校验后原子写回 `data/cloud-drive.json`（0600）。敏感 option 若原样回传响应中的 `********` 掩码，服务端沿用当前已保存值；更换凭据时提交新值。管理端提交 MEGA 原始 `pass` 即可，服务端会在落盘前自动转换为 rclone 混淆值；直接编辑配置文件时仍需填写混淆值。

校验规则（任一失败返回 `400`，message 为受控中文文案）：

- `name` 唯一且匹配 `^[a-z][a-z0-9-]{0,31}$`；
- `type` 非空；`options` 的键与值均为非空字符串；
- `default_destination` 必须指向已启用的目的地；
- `enabled=true` 时以上必须全部成立；`enabled=false` 时允许保存不完整草稿（仅做结构校验）。

响应 `200`：`{"ok":true,"config":{...}}`，`config` 为保存后的视图（字段同 GET 响应）。保存成功写 `cloud_drive.update` 审计（只记开关、默认目的地与目的地名称列表，不含 options 值）。

### POST /api/v1/cloud-drive/test

测试目的地连通性（认证 + CSRF）：对指定目的地执行 rclone 只读探测（`lsd --max-depth 1`）。支持**测试已保存的目的地**与在弹窗保存前**测试草稿目的地参数**两种方式。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `name` | string | 条件必填 | 目的地名称（未提供 `destination` 时必填，须已存在于当前配置中） |
| `destination` | object | 条件必填 | 草稿目的地参数对象（用于在新增或修改保存前自测连通性）。提供时优先按草稿参数测试 |
| └ `name` | string | 是 | 目的地名称（需符合 `^[a-z][a-z0-9-]{0,31}$` 规则） |
| └ `type` | string | 是 | 后端类型（例如 `mega`、`s3`、`webdav` 等） |
| └ `path_prefix` | string | 否 | 远端路径前缀（可为空或根目录） |
| └ `options` | map[string]string | 是 | 参数键值对。若编辑既有目的地，掩码值（如 `••••••••`、`********`）会自动复用原已保存的敏感凭据；MEGA 原始密码在测试前会自动混淆 |

请求体示例 1（测试已保存目的地）：

```json
{
  "name": "mega-backup"
}
```

请求体示例 2（保存前自测草稿目的地参数）：

```json
{
  "destination": {
    "name": "mega-draft",
    "type": "mega",
    "path_prefix": "spore",
    "options": {
      "user": "alice@example.com",
      "pass": "my_raw_password"
    }
  }
}
```

响应：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `ok` | bool | 连通是否成功 |
| `message` | string | 成功确认或失败的分类中文原因（如凭据错误、网络异常、限流等） |

错误：`400`（请求体非法、既有目的地不存在或草稿目的地参数不合法）；`503 SERVICE_UNAVAILABLE`（云盘管理器未接入或 rclone 不可用）。

> 网盘管理类接口有限流风险（MEGA 连续快速调用可能触发封禁），测试按钮按需点击，不要高频或自动化轮询。测试过程均为只读探测，不会修改任何持久化配置。

### POST /api/v1/requests/{id}/cloud-archive

单条补存（认证 + CSRF）：对**任意终态**请求（正常转发、失败、取消均可）按原链接创建或复用云盘下载请求并上传网盘，不再重发回 Telegram。同一原请求、同一目的地最近的补存子行若为 `failed`，会原地重置后再次入队；最近补存已成功/取消或尚无补存记录时新建子行。执行阶段若同一用户、同一链接与目的地已有成功云盘记录且远端核验通过，会复用已上传结果而不是重复下载上传。管理端动作绕过用户配额与去重窗口。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `destination` | string | 否 | 目的地名称；缺省用 `default_destination` |

资格校验：请求存在、已终态、`delivery_mode != text`（纯文本无媒体可存）、云盘功能开启、rclone 可用、该请求无在途补存任务。通过后复用最近的 failed 补存子行，或新建 `delivery_mode=cloud` 的请求行（沿用原用户与链接，`parent_request_id` 指向原请求，状态 `queued`）并入队执行；执行阶段源消息已被删除时目标行以明确错误失败。动作写 `request.cloud_archive` 审计。

响应 `200`：`{"ok":true,"created_request_id":12}`（本次新建或复用的 cloud 请求行 ID；为兼容既有 API 保留字段名），该行可在请求列表（「网盘」标签）与详情页（「补存自 #id」）查看。

错误：`400`（ID 非法/请求体非法/目的地不存在或未启用——缺省目的地也未配置时同样拒绝）；`404`（请求不存在）；`409 STORE_CONSTRAINT`（资格校验未通过：未终态、纯文本请求或已有在途补存）；`503 SERVICE_UNAVAILABLE`（云盘功能未开启或 rclone 不可用）；`503 QUEUE_FULL`（队列已满：目标请求行标记 `QUEUE_FULL` 失败，可再次补存或经现有重试入口重试）。

### POST /api/v1/requests/cloud-archive-batch

批量补存（认证 + CSRF）：与单条端点相同的资格校验及新建/复用入队逻辑，逐条独立执行、互不回滚；复用队列容量控制，同样会命中同链接云盘成功复用。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `request_ids` | int64[] | 是 | 1–100 个正整数（单次上限 100 条） |
| `destination` | string | 否 | 目的地名称；缺省用 `default_destination` |

响应：

```json
{ "ok": true, "results": [{ "request_id": 1, "created_request_id": 12 }] }
```

`results` 为逐条摘要 `{request_id, created_request_id?, skip_reason?, queue_full?}`：

- 创建或复用成功：带 `created_request_id`（本次目标 cloud 请求行 ID）；
- 资格不满足：带 `skip_reason`，不建行也不复用；
- 入队失败（队列饱和）：带 `created_request_id` 与 `"queue_full": true`，目标行标记 `QUEUE_FULL` 失败，可再次补存或经现有重试入口重试。

存储故障中止整个批量响应（`503`），此前已新建或复用入队的补存任务保持有效。

`skip_reason` 枚举：

| 值 | 说明 |
| --- | --- |
| `not_found` | 请求不存在 |
| `not_finished` | 请求未终态（排队或处理中） |
| `text_only` | 纯文本请求，无可存媒体 |
| `cloud_disabled` | 云盘下载功能未开启 |
| `rclone_unavailable` | rclone 二进制不可用 |
| `already_archiving` | 该请求已有在途补存任务 |

错误：`400`（数量越界/ID 非法/请求体非法/目的地不存在或未启用——缺省目的地也未配置时同样拒绝）。

---

## 5b. 缓存补写（转存缓存频道）

本组端点对**任意终态**请求记录（成功、失败、取消，含纯文本）按原链接重新获取源消息，把无脚注干净副本直接发送到缓存频道（即 TG 链接复用的副本来源）并落副本坐标（`dump_entries`），供同链接后续提交复用。与云盘补存不同，缓存补写**全程不向原用户发送任何消息**（成功失败都不发），仅产出缓存频道副本与新的 `delivery_mode=dump` 请求行。缓存频道未在系统设置或 `DUMP_CHANNEL_ID` 配置时本组功能不可用（`dump_disabled`）。

### POST /api/v1/requests/{id}/dump-backfill

单条缓存补写（认证 + CSRF）。无请求体（空 JSON `{}` 亦可）。

资格校验：请求存在、已终态、缓存频道已配置（settings 的缓存频道或 `DUMP_CHANNEL_ID` 环境变量）、缓存频道无同链接副本。通过后新建 `delivery_mode=dump` 的请求行（沿用原用户与链接，`parent_request_id` 指向原请求，状态 `queued`）并入队执行；执行阶段会再次复核副本存在性与缓存频道配置（覆盖并发窗口），源消息已被删除时新行以明确错误失败。动作写 `request.dump_backfill` 审计。

响应 `200`：`{"ok":true,"created_request_id":12}`（新建 dump 请求行 ID），该行可在请求列表（「缓存补写」投递方式标签）与详情页查看，处理进度照常展示。

错误：`404`（请求不存在）；`409 STORE_CONSTRAINT`（资格校验未通过：未终态或缓存频道已有该链接副本）；`503 SERVICE_UNAVAILABLE`（缓存频道未配置）；`503 QUEUE_FULL`（队列已满：新请求行标记 `QUEUE_FULL` 失败，可经现有重试入口重试，重试保留缓存补写投递方式）。

> 缓存补写任务与所有任务一致仅在 MTProto 用户会话就绪期间执行；服务离线期排队任务会被丢弃并标记 `INTERRUPTED`，可在 Web 侧重试。

### POST /api/v1/requests/dump-backfill-batch

批量缓存补写（认证 + CSRF）：与单条端点相同的资格校验与建行入队逻辑，逐条独立执行、互不回滚；复用队列容量控制。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `request_ids` | int64[] | 是 | 1–100 个正整数（单次上限 100 条） |

响应：

```json
{ "ok": true, "results": [{ "request_id": 1, "created_request_id": 12 }] }
```

`results` 为逐条摘要 `{request_id, created_request_id?, skip_reason?, queue_full?}`，语义与[批量云盘补存](#post-apiv1requestscloud-archive-batch)一致：创建成功带 `created_request_id`；资格不满足带 `skip_reason` 不建行；入队失败带 `created_request_id` 与 `"queue_full": true`。存储故障中止整个批量响应（`503`），此前已建行的补写任务保持有效。

`skip_reason` 枚举：

| 值 | 说明 |
| --- | --- |
| `not_found` | 请求不存在 |
| `not_finished` | 请求未终态（排队或处理中） |
| `already_dumped` | 缓存频道已有该链接的有效副本。有效性经试探复制判定（与同链接复用同源）：副本消息已被删除（如在缓存频道客户端手动删除）时自动放行重新补写，重写后按最新副本坐标生效 |
| `dump_disabled` | 缓存频道未配置 |

错误：`400`（数量越界/ID 非法/请求体非法）。

---

## 5c. 云盘配置备份与恢复

本组端点备份和恢复 `data/cloud-drive.json`，与第 11 节的 SQLite 数据库备份完全独立。数据库导出**不包含**云盘配置或网盘凭据；云盘配置包也不包含数据库、Session、`.env` 或媒体文件。所有端点均要求管理员会话，POST / DELETE 还要求会话级 CSRF；状态读取响应与 ZIP 下载携带 `Cache-Control: no-store`（部分写操作成功响应的缓存头说明见[统一约定](#统一约定)）。

备份包是格式版本 `1` 的加密 ZIP，固定且仅包含：

```text
manifest.json
cloud-drive.json.enc
```

`manifest.json` 记录格式版本、Argon2id 参数、AES-256-GCM nonce、加密载荷的大小与 SHA-256、创建时间、应用版本和目的地名称摘要，不包含 `options` 值。`cloud-drive.json.enc` 是以管理员提供的备份密码经 Argon2id 派生 256 位密钥后，使用 AES-256-GCM 加密的完整配置。salt 与 nonce 每次随机生成。

> 备份密码只在当前导出或上传验证请求的内存中使用，不记录、不持久化，也不会写入日志或审计。密码遗忘时无法解密或恢复该备份，服务端没有找回或重置机制。

云盘配置备份状态字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `pending` | bool | 是否存在已验证、待确认的候选 |
| `format_version` | int | 候选包格式版本；当前为 `1`，无候选时省略 |
| `created_at` | string | manifest 中的 RFC 3339 创建时间；无候选时省略 |
| `uploaded_at` | int64 | 候选上传时间（Unix 毫秒）；无候选时省略 |
| `app_version` | string | 生成备份包的 Spore 版本；无候选时省略 |
| `sha256` | string | 加密 payload 的 SHA-256；无候选时省略 |
| `size` | int64 | 加密备份包字节数；无候选时省略 |
| `destination_names` | string[] | manifest 中的目的地名称摘要，不含任何 option 值 |
| `rollback_available` | bool | 是否存在最近一次可回滚配置 |

### GET /api/v1/cloud-drive/backup/status

读取候选与回滚状态（认证）。不返回候选明文、网盘 options 或备份密码。

响应示例：

```json
{
  "pending": true,
  "format_version": 1,
  "created_at": "2026-09-10T07:00:00Z",
  "uploaded_at": 1789023600000,
  "app_version": "dev",
  "sha256": "8f0a1b...",
  "size": 1240,
  "destination_names": ["mega-1", "archive-s3"],
  "rollback_available": true
}
```

无候选时返回 `{"pending":false,"rollback_available":false}`（若已有回滚点，后者为 `true`）。

### POST /api/v1/cloud-drive/backup/export

导出加密云盘配置（认证 + CSRF）。JSON 请求体：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `password` | string | 是 | 本次备份密码，至少 8 个字符 |
| `password_confirmation` | string | 是 | 必须与 `password` 完全一致 |

```json
{"password":"<至少 8 个字符的备份密码>","password_confirmation":"<同一备份密码>"}
```


成功返回 `application/zip` 附件，而不是 JSON；包内完整配置（包括真实网盘凭据）只存在于 AES-256-GCM 加密载荷中。响应带 `Content-Disposition: attachment` 与 `Cache-Control: no-store`。客户端不得把密码写入 URL、文件名、localStorage 或命令行历史。

### POST /api/v1/cloud-drive/backup/import

上传并验证候选（认证 + CSRF）。请求必须为 `multipart/form-data`：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `backup` | file | 是 | 加密 ZIP，上传上限 4 MiB |
| `password` | string | 是 | 创建该备份时使用的密码，至少 8 个字符，仅用于本次解密验证 |

服务端要求 ZIP 恰好包含两个规定 entry；拒绝目录、符号链接、绝对路径、`..` 路径、未知 entry、未知格式版本、超出 2 MiB 的单 entry 解压结果、异常压缩比、载荷 hash/大小不一致、错误密码以及无法通过完整配置校验的内容。

上传成功只保存**已验证候选**，不会修改当前文件或内存中的运行配置：

```json
{
  "ok": true,
  "candidate": {
    "pending": true,
    "format_version": 1,
    "created_at": "2026-09-10T07:00:00Z",
    "uploaded_at": 1789023600000,
    "app_version": "dev",
    "sha256": "8f0a1b...",
    "size": 1240,
    "destination_names": ["mega-1", "archive-s3"]
  }
}
```

密码错误、包被篡改或无法认证时统一返回受控 `400 BAD_REQUEST`，不区分敏感底层原因。候选会以服务端敏感配置根密钥派生的独立密钥再次加密保存；确认请求不需要再次提交备份密码。新上传的有效候选替换旧候选，但仍不影响当前配置。

### POST /api/v1/cloud-drive/backup/import/confirm

确认并应用候选（认证 + CSRF）。请求字段 `confirm` 必须为固定值 `import_cloud_drive`。

确认后先将当前完整配置保存为唯一的 `cloud-drive.json.rollback-latest`，再以候选**整体替换** `data/cloud-drive.json` 并发布新的 Manager 快照，在线立即生效，无需重启。已经开始且持有目的地快照的在途任务继续使用旧配置；确认后创建的新任务使用新配置。

成功响应：

```json
{"ok":true,"rollback_available":true}
```

错误：`400 BAD_REQUEST`（确认值不符）、`409 CONFLICT`（没有待确认候选）、`503 SERVICE_UNAVAILABLE`（服务端候选解密能力不可用）。失败时保持当前文件和内存快照不变。

### DELETE /api/v1/cloud-drive/backup/pending

取消并删除待确认候选（认证 + CSRF），不修改当前配置，也不删除 rollback。成功返回 `{"ok":true}`；当前没有候选时返回 `409 CONFLICT`。

### POST /api/v1/cloud-drive/backup/rollback

恢复最近一次配置（认证 + CSRF）。请求字段 `confirm` 必须为固定值 `rollback_cloud_drive`。

成功后整体恢复 `cloud-drive.json.rollback-latest` 并在线立即发布；在途任务继续旧配置，新任务使用回滚后的配置。回滚成功后该回滚点被消费并删除。

成功响应：

```json
{"ok":true}
```

错误：`400 BAD_REQUEST`（确认值不符）、`409 CONFLICT`（没有可用回滚点）、`503 SERVICE_UNAVAILABLE`（配置管理器不可用）。失败时保持当前配置不变。

安全审计只记录格式版本、加密载荷哈希（`sha256` 是 AES-256-GCM 加密 payload 的摘要，不是整个 ZIP 包的摘要）、大小、目的地名称/数量、动作与结果；导出、上传、确认、取消和回滚过程中，备份密码与 `options` 值均不得出现在日志、审计、错误响应或常规 JSON 响应中。

---

## 6. 频道统计

频道数据全部来自 `requests` 行聚合，不触发任何频道访问。

### GET /api/v1/channels

频道排行（认证）。查询参数：`since`/`until`（`YYYY-MM-DD`）、`page`/`page_size`。

行结构 `apiChannelRow`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `key` | string | 频道标识 |
| `total` / `succeeded` / `failed` | int | 时间范围内请求量 |
| `success_rate` | float | `[0,1]` |
| `last_requested_at` | int64 | 最近请求时间（0 = 从未） |

### GET /api/v1/channels/\{key\}

频道详情（认证）。查询参数：`since`/`until`。头部统计（`stats`）为**全时段**聚合，趋势与分布应用时间范围。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `key` | string | 频道标识 |
| `stats` | object | 全时段聚合，结构同 `apiChannelRow` |
| `trend` | array | 按日趋势 `{day, total, succeeded, failed}`（`day` 为运营时区当地日） |
| `media_dist` | array | 媒体类型分布 `{key, count}` |
| `error_dist` | array | 错误原因分布 `{key, count}` |
| `since_day` / `until_day` | string | 时间范围回显 |

错误：`400`（key/日期非法）、`404`（该频道在请求记录中无数据）。

### POST /api/v1/channels/{key}/delete

删除该频道的**全部**请求记录（认证 + CSRF；频道是 requests 的纯聚合，无独立频道表）。响应 `{"ok":true,"deleted":<行数>}`。错误：`400`（key 非法）；`404`（该频道没有任何请求记录）；`409 STORE_CONSTRAINT`（仍有排队或处理中的请求）。

### GET /api/v1/channel-bindings

频道绑定列表（认证）：全部用户的绑定记录，按绑定时间倒序，不分页（量级与用户同阶）。可选查询参数 `user_id`（正整数）按归属用户筛选。响应 `{"items":[apiChannelBindingRow]}`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `channel_id` | int64 | Bot API 频道数字 ID（`-100…`），主键 |
| `user_id` | int64 | 绑定归属用户的 Telegram ID |
| `username` | string | 频道公开用户名（无 @），私有频道为空串 |
| `title` | string | 绑定时取得的频道标题 |
| `bound_via` | string | `bot`（用户 /bind 指令）\| `web`（管理端） |
| `status` | string | `active` 有效 \| `unbound` 已解绑留痕（v24 软解绑：解绑不删行，重新绑定同频道即复活，unbound 行允许其他用户接管） |
| `unbind_reason` | string \| 缺省 | 解绑原因：`manual`（手动）\| `channel_gone`（频道已失效自动解绑）；`active` 行省略 |
| `unbound_at` | int64 \| 缺省 | 解绑时间（Unix 毫秒）；`active` 行省略 |
| `created_at` | int64 | 绑定时间（Unix 毫秒） |
| `user_username` | string | 所属用户的用户名（用户被硬删除后为空串） |
| `user_display_name` | string | 所属用户的显示名 |

业务语义：任务成功后，提取内容会在发给用户之后复制一份到该用户**有效**（`active`）的绑定频道；同一频道同时只归属一个用户。频道失效（不存在/停用/被封）时副本投递自动软解绑（`channel_gone`）并私聊通知归属用户；Bot 被移出/权限不足只提醒不解绑。

### POST /api/v1/channel-bindings

为指定用户绑定频道（认证 + CSRF）。公开标识直接经 Bot API 校验；邀请链接先由 MTProto 读取账号预检并实际加入以解析频道 ID。随后经主 Bot 的 `getChat`/`getChatMember` 校验：机器人必须是目标频道的管理员（有发言权限）或创建者；同一频道已被其他用户绑定时拒绝。

请求体：

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `user_id` | int64 | 是 | 绑定归属用户的 Telegram ID（须已存在） |
| `target` | string | 是 | `@username`、`t.me/频道`、`t.me/c/…`、`t.me/+…` / `t.me/joinchat/…` 邀请链接，或 `-100…` 频道 ID |

邀请链接只让读取账号加入，不会自动添加 Bot；调用前仍须把主 Bot 设为目标管理员。响应 `{"ok":true,"binding":apiChannelBindingRow}`。错误：`400 CHANNEL_TARGET_INVALID`（频道标识无法识别）；`400 CHANNEL_INVITE_INVALID`（邀请无效/过期/普通群组）；`503 CHANNEL_INVITE_UNRESOLVED`（读取账号离线、加入需审核或暂未取得频道 ID）；`400`（参数非法/用户不存在）；`409 CHANNEL_NOT_POSTABLE` / `CHANNEL_NOT_PINNABLE`（Bot 权限不足）；`409 CHANNEL_ALREADY_BOUND`（已被其他用户绑定）；`409 CHANNEL_BIND_LIMIT`（达到绑定上限）。网络、Telegram 服务端、限流与 `PEER_FLOOD` 保留对应受控错误码并返回 `503`。

### POST /api/v1/channel-bindings/{id}/delete

解除指定频道 ID 的绑定（认证 + CSRF；管理端可解绑任意用户的绑定）。路径 `{id}` 为 `channel_id`（负数，形如 `-1001234567890`）。v24 起为**软解绑**：记录保留、状态置 `unbound`（原因 `manual`），重新绑定同频道即复活。响应 `{"ok":true,"binding":apiChannelBindingRow}`。错误：`400`（ID 非法）；`404`（绑定不存在）。审计：`channel.unbind`。

### POST /api/v1/channel-bindings/delete

物理删除绑定记录（单条/批量共用，认证 + CSRF；v24 起）。请求体：`{"channel_ids":[…]}`（1–100 个频道 ID）。删除即清行（与软解绑留痕相对）；`active` 行删除等价强制解绑 + 清痕。单条失败不中断其余。响应：

```json
{ "ok": true, "deleted": 2,
  "results": [ {"channel_id": -100…, "ok": true}, {"channel_id": -100…, "ok": false, "error": "…"} ] }
```

错误：`400`（数量/ID 非法）。审计：逐条 `channel_binding.delete` + 汇总 `channel_binding.bulk_delete`。

---

## 6a. 频道加入（受邀频道）

频道加入管理端点与 Bot `/join` 共用同一服务（`internal/joinmgr`）：审批记录、已加入频道实时列表与退出。join 服务或 Telegram 用户号不可用时相关端点返回受控 `503`。加入总开关、审核、上限等配置经[运行设置](#_9-运行设置)的 `join_*` 字段维护。

### GET /api/v1/channel-join/requests

加入申请列表（认证），含历史记录，服务端分页。查询参数：

| 参数 | 类型 | 说明 |
| --- | --- | --- |
| `status` | string | `pending \| approved \| rejected \| failed`；空为全部 |
| `user_id` | int64 | 正整数，按申请用户筛选 |
| `keyword` | string | 频道标题模糊匹配 |
| `since` / `until` | string | 运营时区 `YYYY-MM-DD` |
| `page` / `page_size` | int | 分页 |

行结构 `JoinRequestView`（`invite_hash` 属敏感数据，仅下发脱敏的 `masked_hash`）：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` / `user_id` | int64 | 申请与用户 ID |
| `channel_title` | string | 频道标题 |
| `status` | string | raw 状态码 |
| `requested_at` / `reviewed_at` | int64 | 申请/审批时间（0 = 未审批） |
| `reviewed_by` | string | 审批人（会话哈希标识） |
| `note` | string | 审批备注（如请求制频道等待频道管理员批准的说明） |
| `masked_hash` | string | 脱敏后的邀请链接哈希 |

错误：`400`（筛选/日期/分页非法）；`503 SERVICE_UNAVAILABLE`（join 服务未注入，或 Telegram 用户号离线）；`500/503`（存储类，经统一映射）。

### POST /api/v1/channel-join/requests/{id}/approve · /reject

审批加入申请（认证 + CSRF），无请求体。同意会实际执行 Telegram 加入（读取账号，非 Bot）：邀请链接为"请求制"时系统发出加入请求并把申请置 `approved`，等待频道侧批准；链接失效等加入失败会把申请置 `failed`。拒绝仅更新状态并 Bot 私聊通知用户。写 `channel_join.approve` / `channel_join.reject` 审计。

响应 `{"ok":true,"request":JoinRequestView}`。错误：`400`（ID 非法）；`404`（申请不存在）；`409 STORE_CONSTRAINT`（申请已被处理，或 join 总开关已关闭）；`503`（join 服务未注入，或 Telegram 用户号离线）。

### POST /api/v1/channel-join/requests/delete

批量删除审批记录（认证 + CSRF）。请求体 `{"ids":[...]}`，1–100 个正整数；仅终态（approved/rejected/failed）可删，pending 逐条失败；逐条独立、允许部分成功，删除至少一条时写 `channel_join.requests.delete` 审计（只记请求数与删除数）。

响应：

```json
{ "ok": true, "outcomes": [{ "id": 1, "ok": true }] }
```

`ok` 为是否至少删除一条；`outcomes` 行为 `{id, ok, error?}`。错误：`400`（数量越界/ID 非法/请求体非法）。

### GET /api/v1/channel-join/channels

已加入频道实时列表（认证），不分页：来自 Telegram 用户号对话遍历，叠加数据库留痕。读取前会执行一次总开关熔断（总开关关闭时自动退出外部拉入的频道）与请求制频道懒对账，两者失败仅记日志、不影响列表读取。响应 `{"items":[JoinedChannelView]}`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `channel_id` | int64 | 频道数字 ID |
| `title` / `username` | string | 标题与公开用户名（无 @；私有频道为空串） |
| `kind` | string | 对象类型 raw 码（当前为 `channel`） |
| `source` | string | 留痕来源：`join_command`（/join 即时）\| `approved`（审批加入）\| `external`（外部拉入或无留痕） |
| `joined_by` | int64 | 留痕归属用户 ID（0 = 无） |
| `joined_at` | int64 | 留痕加入时间（0 = 未知） |
| `creator` | bool | 是否该频道创建者（创建者不可退出） |

错误：`503 SERVICE_UNAVAILABLE`（join 服务未注入，或 Telegram 用户号离线）；`500/503`（存储类，经统一映射）。

### POST /api/v1/channel-join/channels/leave

批量退出频道（认证 + CSRF）。请求体 `{"channel_ids":[...]}`，1–100 个正整数；逐条独立执行，部分失败不影响其余；成功退出至少一个时写 `channel_join.leave` 审计（只记请求数与退出数）。

响应：

```json
{ "ok": true, "outcomes": [{ "channel_id": -1001234567890, "ok": true }] }
```

`ok` 为是否至少退出一个频道；创建者频道、未加入的 ID 与 Telegram 离线按行失败并带 `error`。错误：`400`（数量越界/ID 非法/请求体非法）；`503`（join 服务未注入）。


### GET /api/v1/watch-sources

监听源配置列表（认证），不分页；待审批排最前。行包含源标识/类型/状态、申请人资料、受理 Bot 快照及预热条数/最近预热时间。响应另含 `invite_requests`：全部私有邀请链接申请（`watch_invite_requests`，pending 优先，其余按申请时间倒序）。行字段：`id`、`user_id`（0 = 管理员发起）、`masked_hash`（脱敏邀请码，完整邀请链接绝不下发）、`status`（`pending` / `waiting_telegram` / `waiting_bot` / `approved` / `rejected` / `failed`）、`channel_id`（解析成功后的 Bot API `-100` ID，0 = 未解析）、`channel_title`、`participants`、`enabled`、`bot_id` / `bot_username`、`reviewed_by`、`note`、`created_at` / `updated_at`、`user_username` / `user_display_name`。

### POST /api/v1/watch-sources/add

管理员直接添加监听源（认证 + CSRF）。请求体 `{"target":"@用户名/t.me 链接/-100 ID 或 t.me/+… 私有邀请链接","enabled":true}`；不走用户准入/上限/审批。普通标识目标必须是频道或超级群组，且 Bot 已是管理员，响应含 `source`。`target` 为私有邀请链接时走邀请流程：读取账号加入 → Bot 管理员校验；响应含 `invite_request`（等待状态时）或同时含 `invite_request` + `source`（已激活）。邀请无效返回 `INVALID_INVITE_URL`（400），读取账号离线返回 `MTPROTO_OFFLINE`（503）。

### POST /api/v1/watch-sources/{id}/approve · /reject · /toggle · /delete · /leave

监听源管理写操作（认证 + CSRF）：审批申请、暂停/恢复、仅删配置，或把池内全部 Bot `leaveChat` 退出源并删配置。已缓存副本保留。

### POST /api/v1/watch-invite-requests/{id}/approve · /reject · /retry · /delete

私有邀请链接申请的审批操作（认证 + CSRF）：`approve` 仅对 `pending` 生效，同意后立即推进状态机（读取账号加入 → Bot 校验 → 激活或转等待态），响应含最新 `invite_request` 与激活产出的 `source`（未激活时省略）；`reject` 对 `pending` / 等待态生效，清理邀请码并通知申请人；`retry` 对 `waiting_telegram` / `waiting_bot` / `failed` 生效，立即重试一轮；`delete` 任意状态硬删除记录。

### GET /api/v1/watch-events

监听记录（认证，服务端分页）。查询参数：`channel_id`（可选，按源筛选）、`path`（可选，`copy` / `fallback`，按转储方式筛选）、`page`、`page_size`。响应行：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | int64 | 事件 ID |
| `channel_id` / `username` / `title` | int64 / string | 源标识与快照 |
| `message_id` / `member_ids` | int / int[] | 定位消息及转发成员（相册为全部成员） |
| `message_url` | string | 规范化源消息链接 |
| `dump_ids` | int[] | 缓存频道落点；fallback 时为空 |
| `request_id` | int64 | 受保护内容回退管线关联的请求行（0 = 无） |
| `bot_id` / `bot_username` | int64 / string | 执行转储的 Bot |
| `path` | string | `copy`（服务端复制）或 `fallback`（受保护重传） |
| `created_at` | int64 | Unix 毫秒 |

### POST /api/v1/watch-events/delete

删除预热事件留痕（认证 + CSRF，单条/批量共用）。请求体 `{"ids":[…]}`（1–100 个正整数事件 ID），响应 `{"ok":true,"deleted":<实际删除行数>}`。仅删除留痕记录，已缓存副本不受影响。

### GET /api/v1/watch-stats

业务统计页「监听源」Tab 的统计接口（认证）。查询参数与 `GET /api/v1/stats` 完全同款：`since` / `until`（运营时区 `YYYY-MM-DD`）、`all=1`、`bot_id`。响应包含：

- `sources`：当前监听源状态计数；
- `by_source`：按源的转储批次、消息条数、最近转储；
- `by_bot`：各 Bot 转储批次；
- `by_user`：按监听源归属用户聚合；
- `trend`：按运营时区自然日的转储趋势（升序，范围缺日由前端补零）；
- `since_day` / `until_day`：实际生效范围回显。

---

## 6c. 错误日志中心

### GET /api/v1/error-logs

错误日志列表（认证，服务端分页，id 倒序）。逐条记录请求管线与 Bot 相关环节的错误明细，与事件中心互补（事件按 key 聚合管通知，本表管逐条根因）。查询参数：`source`（可选，`request` / `botapi` / `cloud` / `backup` / `watch` / `mtproto`）、`code`（可选，按 apperr 错误码精确匹配）、`severity`（可选，`error` / `warn`）、`request_id`（可选，非零整数——请求详情页「查看相关日志」深链）、`created_after` / `created_before`（可选，Unix 毫秒时间范围，before 为开区间上界）、`page`、`page_size`（上限 100）。

响应行：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | int64 | 日志 ID |
| `source` | string | 错误域：`request`（请求管线）/ `botapi`（Bot 收发）/ `cloud`（网盘）/ `backup`（备份）/ `watch`（监听源）/ `mtproto`（用户号会话） |
| `code` | string | apperr 错误码；空串 = 未分类 |
| `stage` | string | 环节名（`fetch` / `download` / `split` / `send` / `upload` / `pin` / `enqueue` / `test` / `maintenance` 等）；空串 = 未标注 |
| `severity` | string | `error`（任务失败或进程级异常）/ `warn`（尽力而为操作失败） |
| `message` | string | 受控中文描述（发生了什么） |
| `detail` | string | 原始错误串（与 requests.error_detail 同规则截断）；空串 = 无根因文本 |
| `context` | object | 参数快照（纯 ID/名称类值：job_id、bot_id、channel_key、destination 等） |
| `request_id` | int64 | 关联请求行（0 = 无归属请求） |
| `created_at` | int64 | Unix 毫秒 |

### POST /api/v1/error-logs/delete

删除错误日志（认证 + CSRF），两种模式二选一，响应均为 `{"ok":true,"deleted":<实际删除行数>}`：

- 按-ID 批量删：`{"ids":[…]}`（1–100 个正整数）；
- 按时间段删：`{"after":<ms>,"before":<ms>,"source":"","code":""}` —— 至少一个时间界（before 为开区间上界），可叠加 `source` / `code` 条件（清理「某时间之前的某类错误」场景）；前端先查条数再二次确认。

删除只影响留痕；日常清理交给自动保留策略（settings 键 `error_log_retention_days`，缺省 30 天，errlog 清理循环每小时执行）。两种模式均写审计。

---

## 7. 事件中心

### GET /api/v1/events

事件列表（认证）。查询参数：`status`（`open \| resolved`，可选）、`page`/`page_size`。

行结构 `apiEventRow`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | int64 | 事件 ID |
| `key` | string | 事件去重键 |
| `severity` | string | 级别（raw）：`critical`（封禁类，穿透静音计划）/ `error` / `warn` / `info` |
| `message` | string | 受控中文描述 |
| `count` | int | 合并发生次数 |
| `first_at` / `last_at` | int64 | 首次/最近发生时间 |
| `last_notified_at` | int64 | 最近通知时间（0 = 从未通知） |
| `status` | string | `open \| resolved` |

### POST /api/v1/events/{id}/resolve

把事件标记为已解决（认证 + CSRF），无请求体。与系统自动恢复共用 resolve + 审计语义。响应 `{"ok":true}`。错误：`404`。

---

## 8. 审计日志

### GET /api/v1/audit

审计日志（认证）。查询参数：`since`/`until`（运营时区的 `YYYY-MM-DD`，结束日期包含全天）、`page`/`page_size`。时间倒序，筛选条件同时作用于列表与 `total`。

行结构 `apiAuditRow`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | int64 | 条目 ID |
| `actor` | string | 操作者（`admin` / `system` 等） |
| `action` | string | 动作（如 `auth.login`、`user.status`） |
| `target` | string | 操作对象（可为空串） |
| `created_at` | int64 | 时间 |
| `before` / `after` | object \| null | 变更快照，存储中的**原始 JSON 原样下发**（无快照为 `null`），字段不做解释 |

### POST /api/v1/audit/delete

批量删除明确选择的审计日志（认证 + CSRF）。请求体为 `{"ids":[1,2]}`，接受 1–200 个正整数；不存在或重复 ID 按实际命中数处理。删除与新写入的 `audit.delete` 处于同一事务，清理审计只记录 ID 和计数，不复制被删快照。

响应：`{"ok":true,"deleted":2}`。错误：`400`（数量越界/ID 非法）。

### POST /api/v1/audit/clear

清除全部历史审计（认证 + CSRF），**忽略当前时间筛选**。请求体必须为 `{"confirm":"clear_audit"}`。全表删除与新写入的 `audit.clear` 处于同一事务；任何一步失败都会回滚。

响应：`{"ok":true,"deleted":<旧记录数>}`。成功后审计表保留且仅保留一条本次 `audit.clear`，响应中的 `deleted` 不包含该新记录。错误：`400`（确认值错误）。

> 应用内审计可由管理员清理，不等同于外部不可篡改的合规审计账本。

---

## 9. 运行设置

### GET /api/v1/settings

读取可编辑运营设置与生效状态（认证）。注意"配置值（重启生效）"与"当前进程值"并存的字段对。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `timezone` | string | 运营时区（IANA 名称；即时生效） |
| `dedup_window_min` | int | 重复链接去重窗口（分钟；即时生效） |
| `max_links_per_message` | int | 单条普通消息或 `/download` 最大有效链接数（1–50；即时生效） |
| `queue_capacity` | int | 队列容量配置值 |
| `queue_runtime` | int | 当前进程队列容量（0 = 未接入） |
| `queue_same` | bool | 配置值是否与运行值一致（`false` 表示待重启生效） |
| `worker_count` | int | worker 并发配置值（数据库覆盖，1–16；重启生效） |
| `worker_count_runtime` | int | 当前进程 worker 数 |
| `worker_count_same` | bool | worker 配置值是否已生效 |
| `max_file_size_bytes` / `stream_limit_bytes` / `temp_dir_max_size_bytes` | int64 | 媒体参数配置值（字节；重启生效） |
| `max_file_size_runtime_bytes` / `stream_limit_runtime_bytes` / `temp_dir_max_size_runtime_bytes` | int64 | 当前进程值 |
| `media_same` | bool | 三项媒体配置是否都已生效 |
| `memory_budget_bytes` | int64 | 内存管道进程级预算（字节；即时生效，settings 值优先于环境变量 `MEMORY_BUDGET`） |
| `channel_copy_enabled` | bool | 频道副本同步总开关（即时生效；缺省 `true`） |
| `tg_reuse_enabled` | bool | 缓存频道复用总开关（即时生效；缺省 `true`） |
| `dump_channel_id` | int64 | 缓存频道数字 ID（0 = 未配置；即时生效，settings 值优先于环境变量 `DUMP_CHANNEL_ID`） |
| `dump_channel_title` | string | 缓存频道标题（展示用） |
| `join_enabled` | bool | 频道加入总开关（即时生效；缺省 `false`） |
| `join_auto_leave_external` | bool | 自动退出外部拉入频道（惰性检测；缺省 `false`） |
| `join_require_approval` | bool | 普通用户加入需号主审批（缺省 `true`） |
| `join_max_channels` | int | 活跃加入频道数量上限（0–200，0 = 不限；缺省 20） |
| `join_mute_enabled` | bool | 加入后静音（缺省 `true`） |
| `join_archive_enabled` | bool | 加入后归档（缺省 `true`） |
| `watch_apply_enabled` | bool | 监听源用户自助申请（/watch）开关（即时生效；缺省 `false`；管理员添加不受限） |
| `watch_require_approval` | bool | 用户申请需审批后生效（缺省 `true`；false = 免审批直接生效；号主恒直接生效） |
| `watch_max_sources` | int | 监听源总数上限（0–200，0 = 不限；仅约束用户申请；缺省 20） |
| `watch_per_user_limit` | int | 每用户申请上限（0–20，0 = 不限；缺省 3） |
| `max_request_attempts` | int | 单个请求累计尝试上限（1–10，含首次；即时生效；缺省 3） |
| `backup_interval_hours` | int | 自动备份间隔小时（0–168，0 = 关闭；即时生效；缺省 6）。管理端编辑入口在备份页（见 `POST /api/v1/backup/r2`） |
| `backup_keep_count` | int | 自动备份保留份数（1–50；即时生效；缺省 8，默认间隔下约 48 小时窗口）。管理端编辑入口在备份页 |
| `error_log_retention_days` | int | 错误日志保留天数（1–365；即时生效；缺省 30），过期行由 errlog 清理循环每小时删除 |
| `download_threads` / `upload_threads` / `download_connections` / `upload_connections` | int | 传输并发当前生效值（1–16） |
| `download_threads_env` / `upload_threads_env` / `download_connections_env` / `upload_connections_env` | int | 对应环境变量默认值 |
| `download_threads_overridden` / `upload_threads_overridden` / `download_connections_overridden` / `upload_connections_overridden` | bool | 该项是否存在数据库覆盖（缺省 `false` = 跟随环境默认） |
| `last_backup_at` | int64 | 最近备份时间（0 = 从未） |

### POST /api/v1/settings

保存运营设置（认证 + CSRF）。按 时区 → 去重窗口 → 单次最大链接数 → 频道副本开关 → 复用开关 → 缓存频道 → 频道加入 → 最大尝试次数 → 队列容量 → worker 数 → 媒体参数 → 传输并发顺序逐项校验。生效方式：时区、去重窗口、单次最大链接数、频道副本开关、复用开关、缓存频道、频道加入、最大尝试次数、内存预算、传输并发**即时生效**；队列容量、worker 数、媒体参数**重启生效**。空缺字段保持不变；值未变化时不写审计。

| 请求字段 | 类型 | 校验 |
| --- | --- | --- |
| `timezone` | string | IANA 时区名（如 `Asia/Shanghai`） |
| `dedup_window_min` | int | 1–1440 分钟；缺省保持不变 |
| `max_links_per_message` | int | 1–50；缺省保持不变，保存后即时影响新输入 |
| `channel_copy_enabled` | bool | 频道副本同步总开关；缺省保持不变 |
| `tg_reuse_enabled` | bool | 缓存频道复用总开关；缺省保持不变 |
| `dump_channel` | string | 缓存频道目标：`@用户名` / `t.me` 链接 / `-100` 数字 ID；经 Bot 校验（频道存在且 Bot 可发帖）后保存数字 ID；空串清除配置（显式 `0` 覆盖环境变量） |
| `join_enabled` / `join_auto_leave_external` / `join_require_approval` / `join_mute_enabled` / `join_archive_enabled` | bool | 频道加入配置，逐项可选 |
| `join_max_channels` | int | 0–200（0 = 不限） |
| `watch_apply_enabled` / `watch_require_approval` | bool | 监听源（/watch）申请配置，逐项可选 |
| `watch_max_sources` | int | 0–200（0 = 不限） |
| `watch_per_user_limit` | int | 0–20（0 = 不限） |
| `max_request_attempts` | int | 1–10（累计含首次）；缺省保持不变，保存后即时影响重试校验 |
| `backup_interval_hours` | int | 0–168（0 = 关闭自动备份）；缺省保持不变，保存后即时生效。管理端编辑入口已挪至备份页（`POST /api/v1/backup/r2` 同键），本端点保留兼容 |
| `backup_keep_count` | int | 1–50；缺省保持不变，保存后即时生效（轮转保留最近 N 份）。管理端编辑入口已挪至备份页，本端点保留兼容 |
| `error_log_retention_days` | int | 1–365；缺省保持不变，保存后即时生效（错误日志自动保留天数，清理循环每轮重读）。编辑入口在运行设置页 |
| `queue_capacity` | int | 1–4096；缺省保持不变 |
| `worker_count` | int | 1–16；缺省保持不变 |
| `max_file_size` + `max_file_unit` | string | 数值 + 单位（`MB`/`GB`）；缺省保持不变，**两项必须成对填写** |
| `stream_limit` + `stream_limit_unit` | string | 同上，与 `max_file_size` 成对填写且必须 ≤ `max_file_size` |
| `temp_dir_max_size` + `temp_dir_max_size_unit` | string | 同上；可独立填写 |
| `memory_budget` + `memory_budget_unit` | string | 内存预算（64MB–8GB）；数值 + 单位（`MB`/`GB`），可独立填写，即时生效 |
| `download_threads` / `upload_threads` / `download_connections` / `upload_connections` | int | 传输并发覆盖，1–16；设置与恢复同一项会冲突拒绝 |
| `clear_transfer_overrides` | string[] | 恢复跟随环境默认的传输项键名（如 `download_threads`），与对应 set 同请求提交会被拒绝 |

媒体三项整体校验 1MB–2000MB；内存预算校验 64MB–8GB。前序已生效项不因后续项失败回滚。

响应 `{"ok":true,"settings":{...同 GET 响应}}`。错误：`400`（参数非法，message 为受控中文文案）。

---

## 9a. 系统设置（系统身份）

系统身份配置与运营设置（第 9 节）分离；本期仅系统名称，读写经单一来源 internal/syscfg，变更写审计 `settings.system_name`，**即时生效**于 Bot 帮助/欢迎文案、事件通知标题、审批通知与管理端品牌栏/登录页/浏览器标题。

### GET /api/v1/system/config

读取系统身份配置（认证）。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `system_name` | string | 系统名称（未配置或非法时为缺省值 `Spore`） |

### POST /api/v1/system/config

保存系统名称（认证 + CSRF）。

| 请求字段 | 类型 | 校验 |
| --- | --- | --- |
| `system_name` | string | 去首尾空白后 1–32 个字符（按 rune 计），拒绝控制字符 |

响应 `{"ok":true,"system_name":"<规范化后的名称>"}`。错误：`400`（名称非法，message 为受控中文文案）。

---

## 9b. 通知设置

通知通道凭据存放于 `settings.notification_channels`，通知策略与静音计划存放于独立的 `settings.notification_policy`；策略写入不会读取、携带或覆盖凭据。Bot Token、Webhook URL 与签名密钥以 AES-256-GCM 密文保存，任何读取响应、日志和审计都不含明文；空敏感字段表示沿用已保存值。配置随数据库备份，恢复环境需保留原 `WEB_OAUTH_ENCRYPTION_KEY`。

自动事件消息统一带有当前系统名称、通知类型和严重级别。Telegram 文本头部格式为“系统名称 · 通知类型 · 级别”；Webhook/适配器应使用受控结构化字段，不依赖解析中文正文。标题、正文和动态字段不包含 Token、Webhook URL、Secret、消息原文或底层错误。

### GET /api/v1/notification/config

读取脱敏通知配置（认证）。响应顶层字段：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `version` | int | 配置文档版本，当前为 1 |
| `automatic_events` | bool | 真实系统事件自动通知总开关；旧文档缺失时为 `false` |
| `bot` | object | `enabled`、`chat_id`、`has_token`、`credential {available,message?}` |
| `webhook` | object | `enabled`、`format`、`has_url`、`has_secret`、`credential` 及格式专属非敏感字段 |

`credential.available=false` 表示环境加密密钥缺失/变更或密文损坏；服务端保留非敏感配置和旧密文，管理员可重新填写凭据覆盖。

### PUT /api/v1/notification/config

原子保存总开关、Bot 与 Webhook 配置（认证 + CSRF）。任一通道校验失败时不写入任何配置。

```json
{
  "automatic_events": false,
  "bot": {"enabled": true, "token": "123456:...", "chat_id": "-1001234567890"},
  "webhook": {
    "enabled": true,
    "format": "generic",
    "url": "https://example.com/hooks/xxx",
    "secret": "可选签名密钥",
    "generic_signature_header": "X-Spore-Signature"
  }
}
```

- `bot.token`、`webhook.url`、`webhook.secret` 为空字符串时沿用已保存密文。
- `format` 可选 `generic` / `feishu` / `dingtalk` / `discord`；URL 仅允许 HTTPS 且不得包含 userinfo。
- 飞书字段：`feishu_open_ids[]`、`feishu_at_all`；钉钉字段：`dingtalk_mobiles[]`、`dingtalk_at_all`；Discord 字段：`discord_user_ids[]`、`discord_role_ids[]`、`discord_everyone`。
- 缺少有效 `WEB_OAUTH_ENCRYPTION_KEY` 时，新凭据拒绝保存。
- `automatic_events` 控制系统事件是否交给通知设置中的已启用通道投递；关闭时保留现有 owner Bot 兼容通知，策略仍可在管理端保存。

响应 `{"ok":true,"message":"通知配置已保存。","config":{...脱敏视图}}`；写审计 `settings.notification`，审计只含变更字段名与受控生效说明。

### GET /api/v1/notification/event-catalog

返回认证用户可见的完整编译期事件目录，不依赖历史 `events` 表。响应为数组，每项固定字段：`type`、`category`、`type_label`、`severity`、`title`、`description`、`supports_recovery`。当前类别为 `system_alert` / `system_recovery` / `activity`，严重级别为 `info` / `warn` / `error` / `critical`（封禁类：`mtproto.banned` / `bot.banned`，穿透静音计划与最低级别门槛）。`activity` 为活动通知（`web.admin_login` 管理后台登录成功、`user.application` 新用户申请、`channel.join_request` 频道加入申请）：逐次即时推送、不写入事件中心、不受冷却与最低严重级别约束，仍可按类别/事件/渠道在策略中开关（默认开启）。

### GET /api/v1/notification/policy

读取通知策略（认证）。响应字段：

```json
{
  "version": 1,
  "minimum_severity": "warn",
  "categories": {
    "system_alert": {"admin_badge": true, "bot": true, "webhook": true},
    "system_recovery": {"admin_badge": true, "bot": true, "webhook": true},
    "activity": {"admin_badge": true, "bot": true, "webhook": true}
  },
  "events": {}
}
```

缺少 `notification_policy` 文档时返回代码内默认策略；存量策略文档缺少后加类别时加载即自动补齐（各渠道默认开启）。`minimum_severity` 只约束 `system_alert` / `system_recovery` 类别的外部 `bot` / `webhook` 投递（活动通知不受约束）；事件渠道显式 `enabled` 可覆盖最低级别。

### PUT /api/v1/notification/policy

全量校验并替换策略（认证 + CSRF）。请求字段与 GET 相同；类别、事件类型、严重级别和覆盖值均拒绝未知枚举。单事件的 `admin_badge` / `bot` / `webhook` / `recovery` 仅允许 `inherit` / `enabled` / `disabled`。成功响应 `{"ok":true,"message":"通知策略已保存。","policy":{...}}`，写审计 `settings.notification_policy`，不包含凭据。

### GET /api/v1/notification/mutes

读取静音计划数组（认证）。每项字段：`id`、`name`、`match_mode`、`category`、`event_types`、`channels`、`starts_at`、`ends_at`、`permanent`、`enabled`。

- `match_mode`：`all` / `category` / `events`。
- `channels`：`admin_badge` / `bot` / `webhook`，至少一个。
- `starts_at=0` 表示立即开始；时间为 Unix 毫秒。
- `permanent=true` 时 `ends_at` 必须为 0；否则结束时间必须晚于开始时间和当前时间。
- 最多 100 个计划；名称最多 100 个字符；指定事件最多 50 个且必须来自事件目录。

### POST /api/v1/notification/mutes

创建静音计划（认证 + CSRF）。请求体使用上述字段但不需要 `id`；服务端忽略客户端 `id` 并生成稳定 ID。成功返回创建后的静音对象，HTTP `201`，写审计 `notification.mute.create`。

### PUT /api/v1/notification/mutes/\{id\}

全量更新静音计划（认证 + CSRF）。路径 `id` 为准，请求体中的 `id` 不参与选择；成功返回更新后的静音对象并写审计 `notification.mute.update`。不存在返回 `404 NOT_FOUND`。

### DELETE /api/v1/notification/mutes/\{id\}

删除静音计划（认证 + CSRF）。成功响应 `{"ok":true,"message":"静音计划已删除。"}` 并写审计 `notification.mute.delete`；不存在返回 `404 NOT_FOUND`。

### POST /api/v1/notification/test

使用**已保存**配置发送测试消息（认证 + CSRF），不接收或转发表单中的未保存凭据。

```json
{"channel":"bot"}
```

`channel` 为 `bot` 或 `webhook`。测试消息正文以系统设置中的系统名称开头（如 `Spore 通知测试成功`），在系统设置中修改名称后**即时生效**。成功响应 `{"ok":true,"message":"测试消息已发送。"}`，并写不含凭据的 `notification.test` 审计；配置不完整、凭据不可解密或远端拒绝时返回 `400` 受控中文错误。

### POST /api/v1/notification/bot/chat-id

通过已保存 Bot Token 调用 Telegram `getUpdates`，返回最近一条消息所属会话（认证 + CSRF）。请求体 `{}`；成功响应：

```json
{"ok":true,"message":"已获取最近会话 Chat ID。","chat_id":"-1001234567890"}
```

若无最近消息，先向 Bot 发送任意消息后重试。

---

## 10. GitHub OAuth 配置

敏感边界：Client Secret 任何情况下不出现在响应、日志或审计中（审计只记布尔状态）。
OAuth App 的申请步骤与部署配置见 [github-oauth.md](../guide/github-oauth.md)，本节只覆盖 API。

### GET /api/v1/oauth/settings

读取 GitHub 登录配置与绑定状态（认证）。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `github_configured` | bool | 当前生效的通道可用状态 |
| `configured` | bool | Client ID 与 Secret 已完整配置（数据库优先于环境变量） |
| `enabled` | bool | 通道开关 |
| `client_id` | string | Client ID |
| `secret_set` | bool | Secret 是否已设置（永不下发明文） |
| `bound` | bool | 是否已绑定 GitHub 账号 |
| `github_login` / `github_id` / `bound_at` | — | 绑定账号信息；未绑定时省略/为 0 |
| `read_error` | string | 配置/绑定读取失败的受控提示；正常为空 |

### POST /api/v1/oauth/settings

配置管理（认证 + CSRF）。

| 请求字段 | 类型 | 说明 |
| --- | --- | --- |
| `action` | string | `save`（保存 ID/Secret 与开关） \| `enable` \| `disable` \| `clear`（清除数据库配置） |
| `client_id` | string | save 时有效 |
| `client_secret` | string | save 时有效；空串表示保持已有 Secret |
| `enabled` | bool | save 时的开关 |

响应 `{"ok":true,"settings":{...同 GET 响应}}`。错误：`400`（未知 action / 参数非法）。

### POST /api/v1/oauth/bind

发起绑定模式 OAuth（认证 + CSRF），无请求体。服务端签发单次 state 并返回受控授权跳转 URL（回调沿用 `/auth/github/callback`）；**绑定成功会使全部会话失效**，前端需引导重新登录。

| 响应字段 | 类型 | 说明 |
| --- | --- | --- |
| `ok` | bool | `true` |
| `authorize_url` | string | GitHub 授权页跳转 URL（前端 `window.location` 跳转） |

错误：`400`（通道未配置）、`500`。

### POST /api/v1/oauth/unbind

解除 GitHub 绑定（认证 + CSRF），无请求体。删除绑定、**失效全部会话**并清 Cookie。响应 `{"ok":true,"relogin":true}`（前端应跳转 `/admin/login`）。错误：`400`（当前未绑定）。

---

## 11. 数据备份

本节仅处理 SQLite 业务数据库。数据库导出与导入都不包含 `data/cloud-drive.json`、网盘凭据或云盘配置候选/回滚文件；云盘配置必须通过 [§5c](#_5c-云盘配置备份与恢复) 的独立加密 ZIP 流程备份和恢复，不能依赖数据库备份一并迁移。

### GET /api/v1/backup

备份页状态（认证）。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `db_path` | string | 数据库文件路径 |
| `db_size_bytes` | int64 | 数据库文件大小（不可统计为 0） |
| `last_backup_at` | int64 | 最近备份时间（0 = 从未） |
| `pending` | bool | 是否有待导入的备份文件 |
| `pending_state` | string | `待确认 \| confirmed \| failed`；无待导入时省略 |
| `pending_sha256` | string | 待导入文件哈希；无待导入时省略 |

### POST /api/v1/backup/export

导出数据库快照（认证 + CSRF），无请求体。流式下载 `.db` 文件（`application/octet-stream`，文件名 `spore-backup-YYYYMMDD-HHMMSS.db`），内容仅业务数据库，**不含** session.json / peers.json。导出即更新 `last_backup_at`。错误时返回 JSON `500`。

### POST /api/v1/backup/import

上传待导入备份（认证 + CSRF）。**multipart/form-data**，文件字段名 `backup`，大小上限 **2GB+1MB**；扩展名校验。上传只进入**待确认**状态，不立即生效。

响应 `{"ok":true,"message":"<受控提示>","backup":{...同 GET 响应}}`。错误：`400`（文件非法/超限）。

### POST /api/v1/backup/import/confirm

确认导入（认证 + CSRF）。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `confirm` | string | 是 | 固定 `import` |

确认后写入 marker，**下次启动时应用**（安全设置保留、Web 会话清除、MTProto Session/peers 文件不受影响）。响应结构同上传。错误：`400`（确认值非法/无待导入）。

### GET /api/v1/backup/r2

定时备份整体状态（认证；备份页「定时备份与云端同步」卡片的读取口径）。间隔/份数沿用 settings 键（`backup_interval_hours` / `backup_keep_count`，编辑入口在本端点 POST；设置页不再展示这两个字段）。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `interval_hours` | int | 备份间隔小时（0 = 关闭；缺省 6） |
| `keep_count` | int | 保留份数（缺省 8；本地与 R2 同步轮转） |
| `last_backup_at` | int64 | 最近本地快照时间（0 = 从未；上传失败不影响该口径） |
| `r2.enabled` | bool | 是否启用 R2 上云 |
| `r2.complete` | bool | 连接四要素是否齐备（开启的前提） |
| `r2.account_id` | string | Cloudflare Account ID（32 位十六进制） |
| `r2.bucket` | string | 存储桶名 |
| `r2.endpoint` | string | 由 Account ID 拼出的 S3 端点；未配置为空 |
| `r2.access_key_id` | string | **掩码** `********`（已配置时；不下发明文） |
| `r2.secret_access_key` | string | **掩码** `********`（已配置时；不下发明文） |
| `r2.last_upload_at` | int64 | 最近一次上传时间（0 = 从未上传） |
| `r2.last_upload_error` | string | 最近上传失败的受控场景文案；无错误为空 |

凭据只存服务器 `data/r2-backup.json`（0600），不进数据库与备份件。

### POST /api/v1/backup/r2

合并保存定时备份配置（认证 + CSRF）。字段缺省不变更；间隔/份数与 `POST /api/v1/settings` 同键同审计（`settings.backup_interval` / `settings.backup_keep`）。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `interval_hours` | int | 否 | 0–168（0 = 关闭）；即时生效 |
| `keep_count` | int | 否 | 1–50；即时生效 |
| `r2.enabled` | bool | 否 | 开启前需四要素齐备，否则 `400` |
| `r2.account_id` | string | 否 | 空串 = 不变更 |
| `r2.access_key_id` | string | 否 | 空串或掩码 = 沿用已保存值 |
| `r2.secret_access_key` | string | 否 | 空串或掩码 = 沿用已保存值 |
| `r2.bucket` | string | 否 | 空串 = 不变更 |

响应 `{"ok":true,"backup_schedule":{...同 GET 响应}}`。连接配置变更会清除旧的上传错误记录并写 `backup.r2_config` 审计（不含密钥）。错误：`400`（参数非法 / 开启但配置不完整）。

### POST /api/v1/backup/r2/test

R2 连通性测试（认证 + CSRF）。用**已保存**配置做只读探测（ListObjects，不写对象）。配置不完整返回 `400`。响应：

```json
{ "ok": true, "connected": true, "message": "连接成功：R2 存储桶可访问。" }
```

`connected=false` 时 `message` 为受控失败场景（密钥无效 / 桶不存在 / 超时等）。写 `backup.r2_test` 审计。

---

## 12. 系统（重启 / MTProto）

### POST /api/v1/restart

受控重启（认证 + CSRF）。

| 请求字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `confirm` | string | 是 | 固定 `restart` |

成功返回 **`202`**（异步动作，仅代表已触发）：

```json
{ "ok": true, "message": "应用正在优雅退出；……", "restarting": true }
```

进程发送 SIGTERM 优雅退出；Docker Compose 等监管机制会自动拉起。错误：`400`（确认值不符）、`409 CONFLICT`（重启已在进行中）、`503 SERVICE_UNAVAILABLE`（运行环境未接入重启控制）。

### GET /api/v1/mtproto/status

MTProto 登录会话状态（认证）。**扫码 URL 是敏感值，不在本 API 下发**——前端经 `/mtproto/qr.png` 展示二维码。

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `state` | string | `ready \| login_pending \| offline`；MTProto 未接入时为 `unknown`（此时其余字段省略） |
| `updated_at` | int64 | 状态更新时间 |
| `qr_available` | bool | 是否有待扫描的登录二维码 |
| `last_error` | string | 最近错误；无则省略 |
| `bot_state` | string | Bot MTProto 会话状态（主 bot）：`ready \| offline`；未接入时省略 |
| `bot_dc_id` | int | 主 bot 当前主会话 DC ID；未知或离线时省略 |
| `bot_updated_at` | int64 | 主 bot 会话状态更新时间；未接入时省略 |
| `bots` | array \| 缺省 | 多机器人池逐 bot 的直传会话状态 `{bot_id, username, state, dc_id, updated_at}`（装配顺序，主 bot 在前）；单 bot 部署省略 |

### POST /api/v1/mtproto/relogin

触发重连（认证 + CSRF），无请求体。仅离线状态接受。响应 `{"ok":true,"message":"已触发重连，请等待新的扫码二维码。"}`。错误：`409 CONFLICT`（当前不是离线状态）、`503 TELEGRAM_UNAVAILABLE`（未接入）。

MTProto 状态响应（`GET /api/v1/mtproto/status`）在 `state=offline` 时附带 `error_kind` 离线原因分类：`banned`（账号被封禁 USER_DEACTIVATED_BAN）/ `revoked`（会话被撤销或失效）/ `network`（网络异常）/ `unknown`。分类为 `banned` / `revoked` 时同步产生 `mtproto.banned` **critical** 事件（穿透静音计划）。

### POST /api/v1/mtproto/clear-session

清理用户号会话文件（认证 + CSRF，无请求体；v24 起）：删除 `data/session.json` 与 `data/peers.json`（Bot 直传会话文件 `bot-session*.json` 与用户号无关，不受影响），为新号扫码腾出干净状态。**仅离线状态接受**（在线返回 `409 CONFLICT`，防误删运行中会话）。响应 `{"ok":true,"message":"会话文件已清理，请点击重新登录并用新号扫码。"}`。错误：`409`（会话在线）；`503 TELEGRAM_UNAVAILABLE`（未接入）。审计：`mtproto.clear_session`。

---

## 12b. 缓存频道迁移（v24）

把旧缓存频道中仍可读的副本整批复制到当前缓存频道（免重新提取），供切换缓存频道或升级后重建秒级复用。业务核心在 `internal/dumpcache`：分批限速执行、后台运行、断点可续（重启后重新发起继续）；源频道不可读时中止（剩余条目由复用自愈重建）。

### GET /api/v1/dumpcache/migrate

查询迁移进度与建议源频道（认证）。响应：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `configured` | bool | 当前是否已配置缓存频道 |
| `channel_id` | int64 | 当前缓存频道 ID |
| `suggest_from` | int64 | 建议源频道：settings 键 `dump_channel_legacy_id`（首次查询时若存在 `dump_channel_id=0` 的升级前存量条目，按当时生效频道记录一次） |
| `progress` | object | `{running, from, to, total, done, failed, skipped, started_at, finished_at?, last_error?}` |

### POST /api/v1/dumpcache/migrate

发起迁移（认证 + CSRF）。请求体：`{"from_channel_id": -100…}`。拒绝条件（受控 400）：未配置缓存频道、源频道即当前频道、已有迁移进行中、ID 非法。响应 `{"ok":true,"progress":…}`。审计：`dumpcache.migrate`。

---

## 12a. 机器人池管理（多机器人池）

机器人池的列表与增删（认证；增删另需 CSRF）。列表合并环境变量来源（只读）与 `data/bots.json` 文件来源（可增删），并合并运行时身份（getMe 快照、长轮询在线状态、MTProto 直传会话）。修改后**重启进程生效**。数据范围红线：token 只进不出——任何响应、日志与审计不回显 token。

### GET /api/v1/bots

响应 `botsView`：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `bots` | array | 条目列表（env 在前、装配顺序；主 bot 在首），见下 |
| `max_bots` | int | 机器人数量上限（20） |
| `need_apply` | bool | 是否存在等待重启生效的条目 |

`bots` 行：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `bot_id` | int64 | Telegram bot 账号 ID（token 数字前缀）；未接入时仍可推导 |
| `username` / `name` | string | getMe 身份（未接入时省略） |
| `primary` | bool | 是否主 bot（env 首项） |
| `online` | bool | Bot API 长轮询是否在线 |
| `conflict` | bool | 消息拉取冲突：token 被 webhook 或另一个轮询实例占用，该 bot 收不到新消息（发送不受影响）。处理方式：让对方服务下线该 bot（webhook 型需由对方删除 webhook），或从本实例移除该 token 后重启；冲突进入/恢复会分别产生/解决 `bot.poll_conflict` 事件 |
| `paused` | bool | 已暂停：管理端手动暂停后停止接收该 bot 的新消息（在途任务由原 bot 正常完成）；即时生效、重启保持 |
| `disabled` | bool | 停用（v24）：长轮询 401——Token 被封禁或撤销。发送路由自动降级到其他可用 bot；名下排队任务出队即标记 `failed(BOT_DISABLED)` 并提示用户向其他机器人重新提交。进入停用产生 `bot.banned` **critical** 事件（穿透静音计划）；不自愈，替换 Token 并重启是唯一恢复路径（重启后仍失效会再次标记） |
| `mtproto_state` | string \| 缺省 | 该 bot 的 MTProto 直传会话状态；未接入时省略 |
| `source` | string | `env`（环境变量，只读）\| `file`（管理端可增删） |
| `restart_pending` | bool | 已配置但当前进程未接入（等待重启） |

错误：`503`（未接入机器人列表管理器）。

### POST /api/v1/bots/add

新增文件来源 bot（认证 + CSRF）。请求体：`{"token": "<Bot API token>"}`（服务端校验格式、去重、上限 20，写入 `data/bots.json` 后原子落盘，0600）。响应 `{"ok":true, "bots":[...], "need_apply":true, "restart_hint":"…重启后生效…"}`。

错误：`400`（token 格式非法、重复、数量达上限）；`503`（未接入管理器）。审计：`bots.add`（target `bot:<id>`，不含 token）。

### POST /api/v1/bots/{id}/delete

移除文件来源 bot（认证 + CSRF），无请求体；`{id}` 为 bot 数字 ID。响应同 add。错误：`400`（条目不存在或为 env 来源——env 来源请在部署环境修改后重启）；`503`（未接入管理器）。审计：`bots.remove`。

### POST /api/v1/bots/{id}/pause · /resume

暂停/恢复指定 bot（认证 + CSRF），无请求体。**即时生效**并持久化（settings 键 `bots_paused`，重启/重连后保持）。暂停 = 停止接收该 bot 的新消息（长轮询立即停止）；**已受理的任务仍由原 bot 正常完成**（发送通道保留）。响应：`{"ok":true, "bots":[...], "need_apply":…, "message":"…受控提示…"}`（`bots` 回显最新列表）。

错误：`400`（id 无效）；`503`（未接入机器人列表管理器）。审计：`bot.pause` / `bot.resume`。

---

## 13. 功能端点（非 JSON）

会话认证（无 CSRF，只读），SPA 直接引用，错误为纯文本（非 JSON 信封）。

### GET /users/export.csv

导出全部用户用量（无筛选参数）。UTF-8 BOM（兼容 Excel）。

### GET /requests/export.csv

导出消息记录。筛选参数与 `GET /api/v1/requests` 完全同源（`user_id`/`status`/`channel`/`media_type`/`delivery_mode`/`error_code`/`since`/`until`），不含分页。单次导出上限 10 万行，超限截断并在响应头置 `X-Export-Truncated: true`。

### GET /channels/export.csv

导出频道统计。筛选参数：`since`/`until`（与 `GET /api/v1/channels` 同源）。同样具备 10 万行截断保护。

### GET /mtproto/qr.png

当前扫码登录 URL 渲染为 PNG（认证）。`image/png`、`no-store`；`404` 当前没有待扫描的登录码；`503` MTProto 未接入。图内容即登录令牌，不走 JSON、不落日志。

---

## 附录：错误码总表

### API 传输层错误码（`internal/web/api.go`）

| 错误码 | HTTP | 说明 |
| --- | --- | --- |
| `UNAUTHORIZED` | 401 | 未登录或登录已过期 |
| `CSRF_FAILED` | 403 | CSRF token 缺失或不匹配 |
| `FORBIDDEN` | 403 | 已认证但无权限执行该操作 |
| `BAD_REQUEST` | 400 | 请求参数非法 |
| `NOT_FOUND` | 404 | 资源不存在或已被删除 |
| `CONFLICT` | 409 | 操作与当前状态冲突 |
| `METHOD_NOT_ALLOWED` | 405 | 请求方法与该端点不匹配 |
| `SERVICE_UNAVAILABLE` | 503 | 可选依赖未接入，操作暂不可用 |

### 业务错误码（`internal/apperr`，按上下文映射 HTTP 状态）

| 错误码 | 典型 HTTP | 说明 |
| --- | --- | --- |
| `INTERNAL_ERROR` | 500 | 未分类内部错误 |
| `STORE_UNAVAILABLE` | 503 | 存储服务暂时不可用 |
| `STORE_CONSTRAINT` | 409 | 与现有数据冲突（已存在、状态不允许等） |
| `QUEUE_FULL` | 503 | 内存队列饱和 |
| `RETRY_EXHAUSTED` | 409 | 请求已达最大尝试次数 |
| `USER_DISABLED` | 409 | 用户已禁用或归档 |
| `TELEGRAM_UNAVAILABLE` | 503 | Telegram 通道未就绪（MTProto 相关操作） |
| `WEB_AUTH_FAILED` | 401 | 访问密钥错误或 OAuth 账号未绑定 |
| `WEB_LOGIN_LOCKED` | 429 | 登录失败次数过多被限流锁定 |
| `WEB_CSRF_INVALID` | 403 | 登录级 CSRF 校验失败 |
| `OAUTH_STATE_INVALID` | — | OAuth state 缺失、过期或已使用 |
| `OAUTH_EXCHANGE_FAILED` | — | 与 GitHub 的 token/账号交换失败 |
| `CHANNEL_TARGET_INVALID` | 400 | 频道标识无法识别（频道绑定） |
| `CHANNEL_INVITE_INVALID` | 400 | 绑定邀请无效、过期或指向普通群组 |
| `CHANNEL_INVITE_UNRESOLVED` | 503 | 读取账号当前无法通过邀请取得频道 ID |
| `CHANNEL_NOT_POSTABLE` | 409 | 机器人不是该频道管理员或无发言权限 |
| `CHANNEL_NOT_PINNABLE` | 409 | 机器人缺少超级群组置顶权限 |
| `CHANNEL_ALREADY_BOUND` | 409 | 该频道已被其他用户绑定 |
| `CHANNEL_BIND_LIMIT` | 409 | 已达到可绑定频道的数量上限 |

请求记录可能出现的失败码（`error_code` 字段）：`INVALID_URL`、`MESSAGE_NOT_FOUND`、`CHANNEL_NOT_ACCESSIBLE`、`SERVICE_MESSAGE`、`MEDIA_UNSUPPORTED`、`FILE_TOO_LARGE`、`TEMP_DIR_FULL`、`MEDIA_DOWNLOAD_FAILED`（下载兜底）、`NETWORK_ERROR`（网络连接失败或超时）、`TELEGRAM_SERVER_ERROR`（Telegram RPC 5xx）、`FILE_REFERENCE_INVALID`（媒体引用失效，刷新后仍不可得）、`TELEGRAM_RATE_LIMIT`、`SEND_TARGET_INVALID`（发送目标不可用，重试无效）、`BOT_SEND_FAILED`（发送兜底）、`LARGE_CHANNEL_UNAVAILABLE`、`SPLIT_UNAVAILABLE`（超大视频可播放切段不可用：下载前前置校验失败，不降级）、`INTERNAL_ERROR`（未分类兜底）、`INTERRUPTED`、`REQUEST_CANCELLED`、`CLOUD_AUTH_FAILED`（云盘账号验证失败）、`CLOUD_QUOTA`（网盘空间不足）、`CLOUD_NETWORK`（网盘网络异常）、`CLOUD_UPLOAD_FAILED`（云盘上传兜底失败）、`CLOUD_UPLOAD_TIMEOUT`（云盘上传超过任务时限）、`CLOUD_VERIFY_FAILED`（同链接已上传核验/远端存在性检查暂时不可用）、`CLOUD_DOWNLOAD_DENIED`（用户级云盘下载权限被拒绝，见用户详情 `effective_cloud_download`）、`CLOUD_TEXT_ONLY`（纯文本消息不支持网盘下载） 等，中文文案由 `apperr.UserText` 统一提供。`NETWORK_ERROR`/`TELEGRAM_SERVER_ERROR`/`FILE_REFERENCE_INVALID`/`SEND_TARGET_INVALID` 为细化码，仅对新增版本后的新失败产生；历史行保留归类时的原始码。云盘任务的逐文件结果另见 `GET /api/v1/requests/{id}` 的 `cloud_uploads`。
