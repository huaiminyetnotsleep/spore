# Spore 后端开发规范

本层适用于 `cmd/bot/`、`internal/` 及与 Go 服务实现直接相关的测试和运行时文件。Spore 是单仓库 Go 1.25 服务，使用 Telegram Bot API 与 gotd/td MTProto 双通道；持久化为内嵌 SQLite（`internal/store`），不依赖外部数据库或 Web API 服务。

## 规范索引

| 规范 | 内容 | 状态 |
| --- | --- | --- |
| [目录结构](./directory-structure.md) | Go 目录、包职责和依赖边界 | 已填写 |
| [持久化与数据文件](./database-guidelines.md) | SQLite 规则（迁移、事务、备份）与 session/peers/tmp 文件边界 | 已填写 |
| [错误处理](./error-handling.md) | `error`、`apperr`、用户提示和清理 | 已填写 |
| [日志](./logging-guidelines.md) | `slog`、字段、级别和脱敏 | 已填写 |
| [质量规范](./quality-guidelines.md) | 格式化、构建、测试和竞态检查 | 已填写 |
| [Web 服务与 SPA 契约](./web-serving.md) | internal/web 服务形态（SPA 壳/302/公开面）、登录 API 安全语义、CSP | 已填写 |
| [媒体双路径（引用/上传）](./telegram-media.md) | gotd fileid 桥接、Sender 引用契约与错误三分类、delivery_mode 观测 | 已填写 |
| [已处理历史恢复](./history-recovery.md) | 恢复任务/API/SQLite/运行器/页面契约，部分发送与安全续跑边界 | 已填写 |
| [Bot 命令菜单与 botapi 契约](./telegram-botapi.md) | 命令注册、scope 覆盖语义、`/start` 兼容清理、`/join` 创建者保护与排障 | 已填写 |

## 开发前检查清单

开始修改代码前：

- [ ] 先定位修改属于哪个 `internal` 包，并阅读该包的现有接口、模型和测试。
- [ ] 检查 `cmd/bot/main.go` 的启动编排，避免把业务逻辑塞进入口。
- [ ] 搜索现有的错误码、媒体限制、临时文件命名和资源清理实现，再新增逻辑。
- [ ] 明确数据经过 `botapi → tmeurl → queue → mtproto → message/media → delivery` 的边界格式。
- [ ] 确认 `context.Context` 的取消、任务超时和 `defer` 清理路径。
- [ ] 确认变更不会记录 Token、Session、消息正文或媒体 URL。
- [ ] 修改并发、共享缓存、队列或文件时，计划运行 `go test ./... -race`。
- [ ] 对照[文档同步约束](../guides/docs-sync.md)映射表，列出本次改动需要同步更新的文档清单（随任务一并实施，不留尾）。

## 质量检查

完成修改后至少运行：

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./...
```

涉及并发、缓存、队列、下载或退出路径时，追加：

```bash
go test ./... -race
```

还要检查：

- 错误是否经过 `internal/apperr` 分类，用户是否只看到中文友好提示；
- 临时媒体在成功、失败和取消路径是否都会清理；
- MTProto 调用是否仍位于 gotd client 生命周期内；
- 结构化日志是否带有必要任务字段且没有敏感信息；
- 跨包改动是否保持接口、消息模型和 `io.Reader` 传输契约一致；
- **文档同步**（[docs-sync.md](../guides/docs-sync.md)）：功能与文档一致——按映射表更新了涉及的 `docs/`、`README.md`、`.env.example` 与 spec；对重命名/删除的标识符 grep 反查文档无旧名残留；同一事实（默认值、边界、生效方式）的所有文档出处一致。

## 当前实现事实

- 入口：`cmd/bot/main.go`。
- Web 管理端：`internal/web/` 只输出 SPA 应用壳与 `/api/v1` JSON（`html/template` 已移除）；全部页面在 `frontend/`（React SPA，规范见 `.trellis/spec/frontend/`），服务形态与登录契约见 [web-serving.md](./web-serving.md)。
- Bot 接收和权限：`internal/botapi/`。
- 链接解析：`internal/tmeurl/`。
- 用户账号登录、Peer 解析和取消息：`internal/mtproto/`。
- 消息标准化和 Entity→HTML：`internal/message/`。
- 媒体下载句柄：`internal/media/`。
- Bot API 重发：`internal/delivery/`。
- 内存 Job 队列和 worker：`internal/queue/`。
- 业务持久化（SQLite）：`internal/store/`。
- 配置和业务错误：`internal/config/`、`internal/apperr/`。

## 跨层实现契约速查

本节把本项目化任务涉及的可执行契约集中列出；各主题的详细规则仍以前述五份规范为准。

### 1. 范围与触发条件

适用于修改 `botapi`、`tmeurl`、`queue`、`mtproto`、`message`、`media`、`delivery`、配置或运行时数据边界的任务，尤其是会改变输入输出、错误码、环境变量、队列或资源生命周期的改动。

### 2. 关键签名

```go
func Parse(text string) (SourceRef, bool)
func Load(getenv func(string) string) (Config, error)
func Open(ctx context.Context, api *tg.Client, m message.Media, jobID string, opt Options, log *slog.Logger) (*Handle, error)
func Process(d Deps) Processor
func UserText(code Code) string
```

新增接口应保持最小方法集；网络、下载和任务函数的 `context.Context` 放在第一个参数。

### 3. 输入、输出与环境契约

- Bot 输入是私聊文本中的 Telegram 消息链接；`tmeurl.Parse` 输出统一 `SourceRef`。
- `queue.Job` 保存用户、目标 chat、SourceRef、状态消息 ID 和关联的 requests 行 ID（`RequestID`，0 表示无持久化记录）；worker 在取出与结束时回写该行的阶段与终态。
- `message.Item/Media` 承载标准化文本、entity、媒体类型、下载位置、文件名和大小；`media.Handle.Reader` 是一次性消费的 `io.Reader`，`Cleanup` 必须可调用。
- 必填环境变量为 `BOT_TOKEN`、`TG_API_ID`、`TG_API_HASH`；`ALLOWED_USER_IDS` 可空，仅首次启动（users 表为空）时导入为 enabled 用户，此后白名单以数据库为准（用户经 `/start` 申请，`internal/access` 校验）。`DATA_DIR`、`TEMP_DIR`、`MAX_FILE_SIZE`、`STREAM_LIMIT` 等默认值和跨字段校验见配置代码。

### 4. 校验与错误矩阵

| 条件 | 处理 |
| --- | --- |
| 链接格式、host 或消息 ID 无效 | `INVALID_URL`，不入队 |
| 白名单外或非私聊消息 | 经 `internal/access` 六步校验链拒绝（`USER_NOT_AUTHORIZED`/`USER_PENDING`/`USER_DISABLED`、重复、频率、额度、并发、队列满），不进入业务队列 |
| 频道/消息不可访问 | `CHANNEL_NOT_ACCESSIBLE` 或 `MESSAGE_NOT_FOUND` |
| 媒体超过 `MaxFileSize` | `FILE_TOO_LARGE`，下载前拒绝 |
| 下载/上传失败 | 分别归类为媒体下载或 Bot 发送错误 |
| 请求被取消或任务超时 | 传播 context，仍执行 Cleanup |
| 进程退出中断任务（父 ctx 取消、退出 drain 丢弃、硬退出遗留） | requests 行置 `failed(INTERRUPTED)`；启动时 `FailInterruptedRequests` 批量兜底，可经受控重试重发（`attempt` 累计上限 `max_request_attempts` 动态配置，默认 3、1–10，settings 键单一来源 internal/syscfg；管理端可另经请求详情页重置计数） |
| 失败请求重试被拒 | 按分支返回 `RETRY_EXHAUSTED`/`USER_DISABLED`/`QUEUE_FULL`/`STORE_CONSTRAINT`，不扣额度、复用同一行 |
| 未分类错误 | `INTERNAL_ERROR`，原始原因只进日志 |

### 5. Good / Base / Bad cases

- **Good**：先完成私聊限定与访问控制校验（`internal/access`）再解析链接入队；媒体打开后立即 `defer Cleanup`；任务失败提示统一通过 `UserText` 生成。
- **Base**：单 worker、内存 channel、文本或单媒体按现有 `Process` 路径完成，并通过基础 Go 门禁。
- **Bad**：把原始 Telegram 错误发给用户、超限后仍下载、复制 `peers.json` 缓存、或在发送失败时遗漏 Pipe/临时文件清理。

### 6. 必要测试与断言

- `tmeurl`：断言公开/私有 URL、参数和尾标点得到正确 `SourceRef`，非法 host 被拒绝。
- `config`：断言 `ALLOWED_USER_IDS` 可空、含非法 ID 报错、`TEMP_DIR == DATA_DIR` 和官方服务器下超 50MB 配置失败。
- `message`：断言 emoji/CJK 的 UTF-16 entity、HTML 转义、嵌套标签和长度限制正确。
- `queue/media/delivery`：断言队列满、取消、发送失败和下载失败都会清理资源；涉及共享状态时运行 `go test ./... -race`。
- `internal/notify/`：事件按 key 去重、resolved 重开、30 分钟通知冷却、发送失败降级与事件源阈值；活动通知（登录/申请）逐次即时推送不落事件中心；消息正文由 `templates/` 下按事件一文件渲染关键信息。

### 7. 错误与正确对照

错误：

```go
if err != nil {
    log.Error("失败", "error", err)
    return nil
}
```

正确：

```go
if err != nil {
    return nil, apperr.Wrap(apperr.CodeMediaDownloadFailed, err)
}
```

前者吞掉错误并可能跳过上层清理或用户提示；后者保留错误链，由任务边界统一分类、记录和反馈。
