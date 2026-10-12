# 目录结构与包边界

## 适用范围

Spore 使用 Go 单仓库布局。生产代码放在 `cmd/` 和 `internal/`；运行时生成的会话、Peer 缓存、业务数据库和临时媒体放在 `data/`，不作为源码提交。当前没有 `pkg/`；管理端 Web 路由层由 `internal/web` 承担（SPA 壳与 `/api/v1` JSON），前端页面位于 `frontend/`，持久化由 `internal/store`（内嵌 SQLite）承担，不引入外部数据库服务。

## 实际目录

```text
spore/
├── cmd/bot/main.go                  # 唯一入口和依赖组装
├── internal/
│   ├── access/                      # 访问控制应用服务：六步校验链、额度、审批与 owner
│   ├── apperr/                      # 机器错误码、AppError、用户中文提示
│   ├── botapi/                      # Bot 长轮询、私聊过滤、命令和入队（准入经 access）
│   ├── config/                      # 环境变量加载、默认值和跨字段校验
│   ├── delivery/                    # Bot API Sender、媒体发送和轻量重试
│   ├── media/                       # MTProto 下载句柄、流式/内存管道/临时文件策略
│   ├── message/                     # tg.Message 标准化和 Entity→HTML
│   ├── mtproto/                     # gotd 生命周期、登录、Peer、消息和资料读取
│   ├── notify/                      # 事件目录、去重、通知冷却、模板化正文（templates/）与活动通知
│   ├── notifycfg/                   # 通知通道/策略配置、凭据加密与 Bot/Webhook 适配
│   ├── queue/                       # 内存 Job 队列、worker、退出 drain
│   ├── store/                       # SQLite 持久化：连接、迁移、DAO、备份校验/导入
│   ├── tmeurl/                      # t.me 消息链接纯解析
│   └── web/                         # 管理端 HTTP：认证、OAuth、配置、备份、重启、审计与探针
├── data/                            # spore.db、pending-import.*、rollback、session/peers/tmp（运行时）
├── docs/                            # 架构、部署、任务和排障文档
├── go.mod / go.sum                  # Go 依赖
├── Makefile                         # dev/build/vet/test/linux 门面命令
└── .env.example                     # 配置模板，不存放真实凭据
```

## 包职责与依赖边界

| 包 | 负责 | 不负责 |
| --- | --- | --- |
| `botapi` | Update 过滤、命令、链接解析入口和状态回复，准入经 access | 直接执行 MTProto、下载媒体、自带权限数据 |
| `access` | 提交六步校验链（状态/重复/频率/额度/并发/队列满）、检查与扣减同事务、`/start` 申请与审批、owner 豁免 | 任务执行、MTProto、HTTP |
| `tmeurl` | 文本中提取并校验 `SourceRef` | 网络访问、频道存在性验证 |
| `queue` | Job 缓冲、worker 生命周期、处理编排 | 自己定义 Telegram API 协议 |
| `mtproto` | 用户号会话（登录、Peer/AccessHash、取消息、刷新媒体引用）与 Bot 会话（大文件直传 `messages.sendMedia`，见 bot.go/botsend.go） | 用户提示文本、Bot API 上传、import delivery |
| `notify` | 事件目录、按 key 去重、通知冷却、模板化通知正文（templates/）、活动通知、运行时投递接口与管理员告警/恢复 | 具体事件源业务逻辑、凭据加密、HTTP 通道实现 |
| `notifycfg` | 通知渠道与策略 settings、凭据加密、Bot/Webhook 适配、静音计划和运行时投递 | 事件生命周期与业务事件源 |
| `message` | `tg.Message` 到内部 `Item/Media`、实体渲染 | 网络调用和发送 |
| `media` | 下载三分支策略（流式/内存管道 reorderBuffer/临时文件）、`io.Reader` 和 Cleanup | Bot API 上传 |
| `delivery` | 通过 Bot API 发送全新消息、媒体和相册；定义 `ReferenceSender` 接口并经 `routerSender` 分派引用直发（mtproto.BotClient 实现，装配在 cmd/bot） | MTProto 取消息、业务队列、import mtproto |
| `store` | SQLite 连接与 PRAGMA、版本化迁移、各聚合 DAO 与 NULL/时间归一 | 业务准入规则、HTTP、Telegram 协议 |
| `config` | 环境变量解析、默认值和安全校验 | 启动服务、发送回复 |
| `web` | 管理端 HTTP：访问密钥/GitHub OAuth 登录、会话与 CSRF、登录限流、SPA 壳、JSON API、审计、healthz/readyz | 业务管理决策、直接依赖 gotd 类型、复制队列逻辑 |
| `apperr` | 统一错误码和用户中文文案 | 依赖具体 Telegram 客户端实现 |

典型数据流为：

```text
botapi.Update
  → tmeurl.SourceRef
  → queue.Job
  → mtproto.Fetcher（用户号会话）
  → message.Item
  → media.Handle / delivery.Sender（routerSender：超 Bot API 上限走 mtproto.BotClient 直传，
                                    上传/文本/删除走 Bot API telegramSender）
```

`internal/access` 位于提交入口与队列之间：botapi 解析链接后经其完成准入校验与扣减，通过的提交才进入 `queue`。`internal/store` 不在上述执行热路径上：它记录准入与结果事实（用户状态、请求阶段、用量、审计），执行仍由内存队列负责，记录以数据库为准。

依赖方向约束：`internal/web` 依赖 `internal/queue`（总览页队列指标），因此 `queue` 及其下游**不得反向 import `web`**。跨包共享的设置键常量与读取函数放两者共同依赖的 `internal/store`（如 `SettingKeyPreferMediaReference` / `LoadPreferMediaReference`），禁止在 web 与 queue 双写字面量。生效语义分两类：worker 类"按任务生效"的设置在任务开始时读快照（即时生效）；"容量/媒体参数"类经启动装配（`web.ApplyRuntimeMediaConfig`）重启生效。

`cmd/bot/main.go` 负责按此顺序组装依赖；新业务逻辑应放到拥有该数据和边界的 `internal` 包，而不是继续堆到入口或 handler。

## Go 文件和命名

- 文件使用小写短名，例如 `parse.go`、`worker.go`、`errors.go`；测试放在同包的 `*_test.go`。
- 包名使用短小的小写单词；导出的类型、函数和方法使用 PascalCase，包内实现使用 camelCase。
- 每个包的 `doc.go` 或入口文件保留简短中文包注释；导出 API 用中文 Go doc 说明职责、生命周期和限制。
- 构造函数采用 `NewXxx`；可选依赖或跨包参数优先使用 `Options`/`Deps` 结构体，而不是增加长参数列表。
- `context.Context` 放在需要取消或超时的函数第一个参数；不得把 context 存进长期结构体。
- 不为一次性逻辑新建公共包；优先扩展所属 `internal` 包并保持依赖方向单一。

## 真实示例

- `cmd/bot/main.go` 只负责配置、日志、目录、MTProto ready 回调和依赖组装。
- `internal/botapi/handler.go` 先做私聊判断，命令分流后链接提交经 `internal/access` 准入校验入队。
- `internal/queue/worker.go` 负责取消息、标准化、打开媒体、发送和统一清理。
- `internal/store/migrate.go` 只放版本化迁移脚本与执行逻辑；聚合 DAO 按表分文件（users.go、requests.go 等）。
- `internal/message/convert.go` 只把 Telegram 原始消息转换为内部模型，不发起网络调用。
- `internal/delivery/sender.go` 定义发送边界，业务层不直接拼 Bot API 请求。
