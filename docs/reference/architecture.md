# Spore 技术设计文档（Go 版）

> 本文是 Go 重写后的技术设计，替代旧的 TypeScript + grammY + copyMessage 方案。
> §11 为按当前代码整理的执行顺序与运行时流程。

## 1. 架构总览

Spore 是一个 Telegram 受保护消息提取机器人。用户把频道消息链接发给 Bot，系统通过 **MTProto 用户账号**读取源消息，重新构造为一条**全新消息**发回给用户。新消息不保留 Forward Header，可正常再次转发——这是与 Bot API `copyMessage`/`forwardMessage` 的本质区别。实现上不依赖 Telegram 的转发/复制通道，也没有针对源频道 noforward 标志的专门检查：能否读取取决于登录的读取账号对源频道的访问权限。

核心原则：

> **Bot API 负责"和用户聊天"，MTProto 负责"以用户身份访问 Telegram"，业务层负责"把源消息转换成新的消息"。**

### 1.1 双通道架构（两个 MTProto 会话）

```text
                          Telegram
                       ┌─────┴─────────┐
                       │               │
                   Bot API          MTProto
                       │          ┌────┴─────────┐
                       │      用户号会话      Bot 会话
                       │    （读源频道）  （BOT_TOKEN 登录）
              ┌────────▼───┐  ┌────▼─────┐ ┌────▼──────────┐
              │ go-telegram │  │ gotd/td  │ │ gotd/td       │
              │   /bot      │  │ 用户账号  │ │ Bot 身份       │
              └────────┬────┘  └────┬─────┘ └────┬──────────┘
                       │            │            │
                       ▼            ▼            ▼
        ┌─────────────────────────────────────────────┐
        │                  Go 进程                     │
        │                                              │
        │  botapi     长轮询接收消息、白名单、状态回复      │
        │  tmeurl    t.me 链接解析（links.ts 移植）        │
        │  queue     内存 Job 队列 + worker               │
        │  mtproto   用户号：登录、Peer 解析、取消息         │
        │            Bot 会话：大文件直传（MTProto 上传）   │
        │  message   源消息标准化 + 实体渲染（HTML/透传）    │
        │  media     流式下载 / 临时文件 fallback          │
        │  delivery  路由：Bot API 上传 / 大文件直传（新消息）│
        │  store     SQLite 持久化（用户/请求/用量等）      │
        │  config / apperr   配置与错误模型                │
        └─────────────────────────────────────────────┘
```

Bot 会话（`data/bot-session.json`，`internal/mtproto/bot.go`）复用 `BOT_TOKEN` 登录、
异常退出指数退避自动重启；大文件直传（`internal/mtproto/botsend.go`）用 gotd `uploader`
把下载好的媒体字节上传为 Bot 自己的新文件后 `messages.sendMedia` 发送——绕过官方
Bot API 服务器的 50MB 上传硬上限（MTProto 通道上限 2000MB）。未就绪时仅超过
Bot API 上限的媒体以 `LARGE_CHANNEL_UNAVAILABLE` 确定性失败，小文件不受影响。

### 1.2 一次请求的完整数据流

```text
用户发链接 "https://t.me/example/123"
   ↓ botapi：私聊限定 + access 访问控制校验（数据库白名单/额度）
   ↓ tmeurl.Parse → SourceRef{Username, MessageID}
   ↓ 回复"正在获取消息..."（记下 StatusMsgID）→ queue.Enqueue(Job)
   ↓ worker 出队（单 worker）
   ↓ mtproto.ResolveInputPeer → tg.InputPeerClass
   ↓ mtproto.Fetch → []*tg.Message（Album 时按 GroupedID 聚合相邻消息）
   ↓ message.Convert → []Item（内部标准模型）
   ↓ 文本：message.RenderHTML → delivery.SendMessage
   ↓ 媒体统一"下载 → 上传"：media.Open（流式/内存管道/临时文件）→ delivery.SendMedia / SendAlbum
     （routerSender 按大小路由：≤ Bot API 上限（官方服务器 50MB）走 Bot API 上传；
       超限媒体经 Bot 身份会话 MTProto 直传，上限 2000MB）
   ↓ 删除"正在获取消息..."，Job 完成
用户收到全新消息 → 可正常转发 ✅
```

（运行期的完整执行顺序、并发模型与退出路径见 §11。）

### 1.3 运行模型

- 单进程、长驻运行；`SIGINT`/`SIGTERM` 优雅退出。
- 所有 MTProto 调用必须发生在 gotd `client.Run` 的回调作用域内，因此 **Bot 轮询与 worker 均在 MTProto 就绪后启动**。
- 业务数据存内嵌 SQLite（`data/spore.db`，见 §3.7）；无消息结果缓存、无 Redis。MTProto 会话 `data/session.json` 与 AccessHash 缓存 `data/peers.json` 仍是文件，不入库。
- Worker 数默认 1，通过 `WORKER_COUNT` 可调。

## 2. 目录结构与模块职责

```text
spore/
├── cmd/bot/main.go              # 唯一入口：启动编排（依赖装配，无业务逻辑）
├── frontend/                    # React SPA 管理端（构建产物经 embed 进 Go 二进制）
├── internal/
│   ├── config/                  # 环境变量加载与校验
│   ├── apperr/                  # AppError 错误模型 + 错误码 + 中文用户提示
│   ├── botapi/                  # Bot 长轮询、私聊过滤、命令分流、链接提交
│   ├── access/                  # 用户准入六步链、申请/审批、额度、管理取消/删除/重试
│   ├── tmeurl/                  # t.me 消息链接与邀请链接解析（纯函数）
│   ├── mtproto/                 # 用户号/Bot 号两个 gotd 客户端：登录、Peer 解析、取消息、
│   │                            #   成员加入、大文件直传、DC 重定向
│   ├── message/                 # 源消息标准化、媒体类型判定、Entity→HTML、caption
│   ├── media/                   # 媒体下载句柄（流式 io.Pipe / 内存管道 / 临时文件）与清理
│   ├── delivery/                # 发送路由：Bot API 上传 / Bot 号 MTProto 大文件直传 / 相册
│   ├── queue/                   # 内存 Job 队列 + worker：普通投递、缓存复用、云盘任务
│   ├── dumpcache/               # 缓存频道干净副本写入与复制（转存频道复用）
│   ├── binding/                 # 用户频道绑定（/bind 与管理端共用校验）与脚注来源
│   ├── joinmgr/                 # 频道加入：/join、审批、静音/归档、外部拉入处理
│   ├── progress/                # 下载/上传进度的内存注册表（5 秒粒度占位编辑）
│   ├── store/                   # SQLite 持久化（连接、版本化迁移、各聚合 DAO）
│   ├── syscfg/                  # 系统身份与频道加入等运行设置的读取/校验
│   ├── transfercfg/             # 下载/上传线程与连接数的运行时覆盖（DB 覆盖 env）
│   ├── cloudarchive/            # 云盘下载：cloud-drive.json 配置、rclone 封装、远端布局
│   ├── monitor/                 # 进程资源与传输速率采样（RSS/临时目录/CPU，48h 历史）
│   ├── notify/                  # 系统事件：按 key 合并、冷却通知、owner 私聊
│   └── web/                     # 管理端：SPA 壳、/api/v1 JSON、登录会话、CSV/QR 端点
├── data/                        # spore.db / session.json / bot-session.json / peers.json /
│                                #   tmp/ / cloud-drive.json 等（gitignore）
├── go.mod / go.sum / Makefile / .env.example / README.md / docker-compose.yml
```

| 包 | 职责 | 不负责 |
| --- | --- | --- |
| `botapi` | 接收 Update、私聊过滤、命令分流（`/start` `/help` `/status` `/usage` `/cancel` `/join` 等）、链接入队、状态消息 | 任何 MTProto 操作、业务处理 |
| `access` | 用户准入六步链（状态/去重/频率/额度/并发/队列满）、`/start` 申请、审批、管理端取消/删除/重试/补存入口 | Telegram 协议、投递执行 |
| `tmeurl` | 文本 → `SourceRef`（纯函数） | 访问网络、验证频道存在 |
| `queue` | Job 缓冲、worker 生命周期、普通/复用/云盘任务编排、进度与取消 | 具体发送与下载实现 |
| `mtproto` | 用户号/Bot 号登录、Peer 解析、取消息、成员加入、大文件直传 | 消息语义解释、准入规则 |
| `message` | 源消息标准化、媒体类型判定、Entity→HTML 渲染 | 网络调用 |
| `media` | 下载策略选择（流式/内存管道/临时文件/拒绝）与句柄清理 | 上传 |
| `delivery` | 通过 Bot API 或 Bot 号 MTProto 发送新消息（按大小路由） | 消息内容构造 |
| `dumpcache` / `binding` / `joinmgr` | 缓存频道副本、用户频道绑定、频道加入与审批 | — |
| `store` | SQLite 连接/迁移与各聚合 DAO（用户、请求、用量、审计、事件、设置、会话、加入留痕等） | 业务准入规则、Telegram 协议 |
| `web` | SPA 壳与 `/api/v1` JSON API、登录会话/CSRF、CSV/QR 功能端点 | 业务准入决策（经 access/joinmgr 等服务） |
| `config` / `apperr` / `syscfg` / `transfercfg` | 横切：环境配置、错误模型、运行设置与传输覆盖 | — |
| `cloudarchive` / `monitor` / `notify` / `progress` | 云盘 rclone 封装、资源监控、事件通知、进度注册表 | — |

## 3. 关键技术决策

### 3.1 Bot API 库：go-telegram/bot

| 候选 | 结论 |
| --- | --- |
| **go-telegram/bot**（采用） | 活跃维护，跟进 Bot API 10.3；上传路径用 `io.Pipe()` + `multipart.NewWriter` 构造请求体（chunked 传输），`models.InputFileUpload{Filename, Data io.Reader}` 接受任意 Reader，**MTProto 下载流可直接灌进 Bot API 上传**，全程不落盘不驻留内存 |
| telegram-bot-api v5 | API 形态陈旧，文件上传先读入 `bytes.Buffer` |
| telebot v3 | 版本跟进滞后 |

这是实现 "优先 Streaming"媒体策略的决定性条件：gotd downloader → `io.Pipe` → multipart body 形成真正的背压串联；临时文件路径（fileGate 同样是顺序 Reader）复用同一参数。

### 3.2 MTProto：gotd/td + gotd/contrib（仅 floodwait 中间件），两个客户端

进程内并存**两个 gotd 客户端**（`internal/mtproto/client.go` 用户号 + `bot.go` Bot 身份），分工：

| 客户端 | 会话文件 | 登录方式 | 职责 |
| --- | --- | --- | --- |
| 用户账号 | `data/session.json` | 扫码 / 手机号+验证码（人工一次） | 读源频道、取消息、下载、file reference 过期刷新 |
| Bot 身份 | `data/bot-session.json` | `Auth().Bot(ctx, BOT_TOKEN)`（非交互，自动） | 大文件直传（§3.8） |

- 两个客户端都以 `floodwait.NewSimpleWaiter().WithMaxRetries(5)` 包装 invoker：自动对 `*tg.ErrorFloodWait`（及 `FLOOD_PREMIUM_WAIT`）限幅重试（SimpleWaiter 透传零开销、遇限流才内联等待，选型见 client.go 注释）。
- 两个客户端都 `NoUpdates: true`：入站更新一律走 Bot API 长轮询，MTProto 侧只按需调用。
- 同一 `BOT_TOKEN` 同时维持 Bot API 长轮询与 MTProto 会话——等价于 Bot 的"多设备登录"，互不冲突。
- **传输连接池 + DC 重定向**：两个会话的 api 都经 `client.Pool(*_CONNECTIONS)`（`DOWNLOAD_CONNECTIONS` / `UPLOAD_CONNECTIONS`，默认 4、1–16）+ `redirectInvoker` 装配，替换直连主连接——N 个并发分片线程默认全部复用单条 TCP 连接（单连接吞吐典型 15–30MB/s，受单 TCP 窗口与单连接串行加解密限制），池让分片各走独立连接（并行 TCP 窗口 + 每连接独立 AES-IGE）。池惰性开连、空闲约 1 条、共享主会话 auth key；**裸 pool 不可直接使用**：它不会执行 gotd 主连接 `invokeDirect` 内建的 `*_MIGRATE` 重定向，文件位于非主 DC 时首片会以 `303 FILE_MIGRATE_X` 失败（真机 2026-09-07：1.4GB 视频在 DC5）。`redirectInvoker` 捕获 FILE/STATS_MIGRATE，经 `client.DC(ctx, X, connections)` 自动迁移授权并缓存目标 DC 池后重发；其他会话级 MIGRATE 回退主连接处理。floodwait 包在 redirectInvoker 最外层（`tg.NewClient(waiter.Handle(redir))`），FLOOD_WAIT 睡眠不占任何池连接；设 1 即回退单连接现状。
- Bot 会话 `CompressThreshold: -1` 关闭出站 gzip：gotd 对 >1KB 出站 payload 无条件压缩（压不小也照发），512KB 上传分片是不可压缩媒体，每 GB 白耗约 10–20s CPU；下载响应是否 gzip 由服务端决定，不受影响。用户会话保持默认。

### 3.3 AccessHash：自研轻量缓存（头号难点）

私有频道链接 `t.me/c/<internal_id>/<msg_id>` 只携带裸 ChannelID，而构造 `tg.InputChannel` 必须有 **AccessHash**——它只能从该用户账号"见过"的 peer 信息中获得。

| 方案 | 结论 |
| --- | --- |
| **自研 `data/peers.json` 缓存**（采用） | 只需 `bareChannelID → accessHash` 一种映射，约 80 行，完全可控、可单测 |
| gotd `telegram/peers` 包 | 官方标注 experimental / WIP，排除主路径 |
| gotd/contrib bbolt PeerStorage | 实现的是 contrib 自有 `storage.PeerStorage` 接口，与 `peers.Storage` 方法集不匹配，接线有坑 |

解析流程（`internal/mtproto/resolve.go`）：

```text
SourceRef
 ├─ Username 分支：api.ContactsResolveUsername(ctx, name)
 │    → InputPeer；顺带把返回 Chats 里的 channel hash 写入缓存
 └─ ChannelID 分支：缓存命中 → InputChannel{ChannelID, AccessHash}
      未命中 → WalkDialogs：手写 MessagesGetDialogs 翻页遍历全部对话
              （覆盖归档对话夹），从每批 Chats 提取 hash 写缓存并 Save()
      仍无 → AppError{CHANNEL_NOT_ACCESSIBLE}（用户文案指引 /join 邀请链接加入）
```

前提：**用户账号必须是目标频道成员**（或频道可公开访问）。缓存持久化避免每次重启全量遍历。

账号不是成员时的补救通道（`internal/joinmgr` + `internal/mtproto/membership.go`）：

- Bot 命令 `/join <t.me/+邀请链接>`：经 `messages.importChatInvite` 让**读取账号**（非 Bot）加入频道；号主提交即时生效，普通用户默认落 `join_requests` 表待号主在管理端审批（同意时延时执行加入并 Bot 私聊通知结果）。**准入边界与普通链接不同**：`/join` 不经过普通链接的 enabled 用户状态准入链（不调用 `access` 校验、不扣额度），是否可用由 join 总开关（`join_enabled`，默认关）与审核/数量上限配置控制；而普通链接、`/download`、`/bind` 都要求 enabled 用户。
- 加入后按配置执行 `account.updateNotifySettings`（静音）与 `folders.editPeerFolders`（归档到 folder 1）——归档不影响 WalkDialogs 收割（本来就遍历 folder 0/1）。
- **外部拉入（被邀请）的频道同样按配置归档**：用户号会话轻量消费 update（`ChannelUpdateBridge`，只提取批次携带的频道对象，不建 updates 状态管理），经 peer 缓存过滤出的"新见"频道秒级补静音/归档；已加入频道页刷新、/join 提交（已是成员分支）与 30 分钟周期对账兜底（覆盖离线窗口漏收的 update，重连不补差异）。归档调用幂等（已在归档夹直接成功）。「自动退出外部拉入」开启时退出优先，归档让位给 Enforce 的惰性退出。
- 管理端「BotUser受邀频道」菜单（加入审批 + 已加入频道 + 受邀设置三页）：审批记录表（服务端分页/筛选/单条与批量删除）+ 已加入频道实时列表（对话遍历、手动刷新加载）+ 单条/批量退出（`channels.leaveChannel`）；/join 配置（总开关 `join_enabled` 默认关、自动退出、审核、静音/归档、数量上限）在「受邀设置」页维护，总开关关闭时 /join 直接拒绝。「自动退出外部拉入」（`join_auto_leave_external`，默认关）开启后，列表刷新与 /join 提交会惰性退出"非本系统加入"的频道（留痕来源 external 或无留痕；Telegram 无"拒绝被拉入"的服务端开关，只能事后拦截）。
- 数据边界：`join_requests.invite_hash` 为审批延时执行所必需，展示层一律脱敏；`joined_channels` 只留痕 ID/标题/来源/时间，**不存 access_hash**（数据范围红线）。

### 3.4 媒体传输策略（统一"下载 → 上传"，大文件经 Bot 号 MTProto 直传）

媒体发送统一走"下载 → 上传"：worker 打开下载句柄后交 `delivery.routerSender` 按媒体大小路由（`internal/queue/worker.go` + `internal/delivery/router.go`，Bot 会话见 §3.8）：

```text
源消息 Media（含下载位置 Location 与 Size）
   │
   ├─ media.Open 下载（三条路径共用）：
   │     Document.Size / photo size 预检查
   │     ├─ Size > MaxFileSize  → FILE_TOO_LARGE，直接拒绝，不发起下载
   │     ├─ Size ≤ StreamLimit  → 流式：goroutine 内 downloader.Stream(ctx, pw)
   │     │                        错误经 pw.CloseWithError 传出；pr 交给
   │     │                        models.InputFileUpload{Data}（chunked 上传）
   │     ├─ StreamLimit < Size ≤ InMemoryLimit 且预算充足 → 内存管道：
   │     │     多线程 downloader.Parallel(WithThreads(DOWNLOAD_THREADS),
   │     │     reorderBuffer) 乱序落位 + 顺序阻塞读（reorderBuffer 同时实现
   │     │     WriterAt/Reader），下载与上传完全重叠（边下边发）、零磁盘写入；
   │     │     内存占用 = 文件大小，按进程级预算闸门记账（Cleanup 归还）
   │     └─ InMemoryLimit < Size ≤ MaxFileSize 或预算不足 → 临时文件：
   │           downloader.Parallel(WithThreads(DOWNLOAD_THREADS),
   │           TempDir/jobID-名字) 多线程并行落盘，经 fileGate 就绪水位
   │           门控顺序读——下载落盘与上传读盘重叠（边下边发），
   │           发送后 Cleanup（成功失败都清理）
   │     （下载遇到 FILE_REFERENCE_EXPIRED → RefreshMedia 刷新后重试一次）
   │
   └─ 发送路由（routerSender，uploadCap = BotAPIUploadCap()）：
         ├─ Size ≤ uploadCap → Bot API 上传（官方服务器 uploadCap=50MB 硬上限）
         │     photo 超 PhotoLimit（官方 10MB）自动降级 document；相册走 sendMediaGroup
         ├─ Size > uploadCap → Bot 身份会话 MTProto 直传（gotd uploader
         │     WithThreads(UPLOAD_THREADS) 并发分片上传（>10MB bigLoop 单读多发，
         │     ≤10MB smallLoop 固定串行）+ messages.sendMedia，上限 2000MB）；
         │     Bot 会话未就绪时 LARGE_CHANNEL_UNAVAILABLE 确定性失败（小文件不受影响）
         └─ 相册整组：全员 ≤uploadCap 且 Bot API 可整组 → sendMediaGroup；
               含超限成员（video 且 ≤2000MB）→ 同一 Bot 会话逐成员 uploader
               上传（串行，避免按成员放大 invoke 并发）+ messages.sendMultiMedia
               整组直传；通道未就绪同样 LARGE_CHANNEL_UNAVAILABLE 确定性失败

默认 MaxFileSize 为 2000MB（MTProto 上传硬上限：4000 part × 512KB）；
默认 InMemoryLimit 为 512MB（单文件常驻内存上限）；内存路径另受进程级
预算闸门（`MEMORY_BUDGET`，默认 1GB，管理端 `memory_budget` 即时生效）
约束——每个进入内存管道的文件按大小记账（相册成员同受约束），
预算不足的新文件自动降级临时文件路径（不排队、不失败），
常驻内存被额度封顶而不随并发任务数/相册成员数线性放大；
预算在句柄 Cleanup 时归还，热调小额度不影响在途占用（软上限）。
配置本地 Bot API 服务器后 uploadCap 放宽到 MaxFileSize（全部上传走该服务器，
相册整组恒走 Bot API）。相册成员经 AlbumGroupable 预检（Bot API 承载 ∪ video
≤MaxFileSize 的 MTProto 整组承载）；photo 超 photoLimit、document/audio 及超
MaxFileSize 的成员不可整组，相册逐条发送。
```

统一返回 `media.Handle{Reader, Cleanup}`；三条路径的 `Cleanup` 都是"放弃
信号"——关闭数据源唤醒阻塞读者、终止仍在下载的 goroutine（临时文件路径
同时取消下载 ctx），并删除临时文件。相册成员的下载句柄**并发打开**
（`sendAlbumGroup` 内 errgroup + 信号量，并发上限 2——与 DOWNLOAD_THREADS
解耦，避免 invoke 并发按成员数×线程数放大触发 FLOOD_WAIT）；任一成员失败
整组失败，已打开句柄统一清理。下载分片统一 1MB（upload.getFile 协议上限，
`downloader.WithPartSize`，gotd 默认 512KB 调大后请求次数减半）；下载与
上传的并发分片分别经用户/Bot 会话的连接池各走独立 TCP 连接（§3.2）。

**低内存机器（≤2.5GB）运维建议**：把 `MEMORY_BUDGET` 降到 256MB（管理端
即时生效）——超预算的内存管道媒体自动落 fileGate 临时文件路径，该路径已做
下载落盘与上传读盘重叠（SSD 上性能损失小），峰值 RAM 随额度直接下降；
也可以把 `IN_MEMORY_LIMIT` 一并降到 256MB 收紧单文件上限。`WORKER_COUNT`
不再放大常驻内存（预算封顶），但仍有连接与 FLOOD_WAIT 侧的约束。

**重复链接复用（转存频道直拷，`internal/queue/reuse.go` + `internal/dumpcache`）**：
任务成功投递后，同步向 bot 自有的**缓存频道**（`DUMP_CHANNEL_ID` 指定的私有
频道，bot 为管理员）写一份**无脚注干净副本**——给用户的投递 caption 织有
该用户绑定频道的脚注，副本必须剥离：单媒体 `copyMessage` 带 caption 覆盖
（干净 caption = 引用正文 + 原消息链接，不含频道脚注）；单文本 `SendMessage`
干净渲染；多条 `copyMessages` 整批复制（相册保组）后逐条
`editMessageCaption`/`editMessageText` 清洗。副本坐标落 `dump_entries`
（迁移 v13）。同链接再次提交时，`runJob` 在取数之前先 `tryReuseFromDump`：
查最新副本条目 → `copyMessages(缓存频道 → 目标聊天)` 整条复制——服务端
复制媒体与 caption，无损、无转发头、**不受媒体大小限制（2GB 与 10KB 同
路径）**、相册保组、caption 从构造上无脚注泄露、副本不因原用户删消息失效，
跳过整个 fetch/下载/上传。绑定频道的用户收到复制品后经 fetch 一次重建
caption（1 次 RPC）编辑补上自己的脚注；复制失败（副本被删等）回落完整
链路，成功后重写副本自愈。额度照扣、终态记 `delivery_mode=reuse`；总开关
`tg_reuse_enabled`（settings，默认开，即时生效）与 `DUMP_CHANNEL_ID` 未
配置均回到完整"下载+上传"链路。历史上的用户聊天坐标复用（v12
`sent_chat_id`/`sent_message_ids_json` 列 + 脚注候选规则）已被本方案取代：
列保留在 schema 仅供历史行读取。与历史上移除的"源频道引用直发"不同：
复用的是自己重传成功后的消息，不依赖源频道坐标。

### 3.5 Entity → HTML 转换（正确性风险最高）

MTProto 的 `MessageEntity` 偏移以 **UTF-16 code unit** 计（emoji 占 2 unit），Bot API HTML 同样按 UTF-16 语义解析。转换算法（`internal/message/html.go`）：

1. 逐 rune 扫描文本，构建（字节偏移, UTF-16 累计 unit）映射表；
2. 实体 `[Offset, Offset+Length)` 换算为字节区间，越界实体丢弃并记 debug 日志；
3. 生成事件序列（同位置先闭后开，保证嵌套正确），文本段过 `html.EscapeString`，href 属性转义引号；
4. 标签映射：`Bold→<b>`、`Italic→<i>`、`Underline→<u>`、`Strike→<s>`、`Code→<code>`、`Pre→<pre>`（带 Language 时 `<pre><code class="language-X">`）、`URL/TextUrl→<a href>`、`Blockquote→<blockquote>`、`Spoiler→<tg-spoiler>`；`Mention` 保持原文（Bot 自动识别）；不支持类型退化为纯文本；
5. 长度限制：文本 >4096 / caption >1024 截断并追加"（原文过长已截断）"（MVP 已知限制）。

**独立纯函数 + golden 表驱动测试**（CJK、emoji、嵌套实体、TextUrl 引号）。

### 3.6 Album 聚合

同一 `GroupedID` 的多条消息是一个 Album，不能按独立消息逐条发送。`Fetch` 发现 `GroupedID != 0` 时**一次批量取** `[ID-9, ID+9]` 共 19 个 ID（`channels.getMessages` 支持批量），过滤相同 GroupedID 按升序返回。发送时 **caption 逐成员绑定**（保持源相册中文字与媒体的对应关系；客户端对组内 caption 的展示策略不影响数据保真）。整组只接受 photo/video，且 `AlbumMaxItems=10`（超过 10 项暂不拆分，直接返回错误）；路由按成员大小分流——全员在 Bot API 上限内走 `sendMediaGroup`，含超限成员（video 且 ≤2000MB）走 Bot 号 MTProto 两阶段整组直传：每成员 `uploader.Upload` → `messages.uploadMedia`（注册到目标 peer，换取带新鲜 file_reference 的坐标）→ 汇总为 `InputMediaPhoto/InputMediaDocument` 引用 → `messages.sendMultiMedia` 一次整组发送（sendMultiMedia 只接受已注册引用，raw `inputMediaUploaded*` 会被 400 MEDIA_INVALID 拒绝——真机结论 2026-09-03）；photo 超 photoLimit、document/audio 成员或超 2000MB 时整组降级逐条发送。

### 3.7 持久化：内嵌 SQLite（internal/store）

产品化阶段引入 SQLite 作为唯一业务数据库，支撑访问控制、请求记录、用量额度、审计、事件与管理端会话。

**选型**：`modernc.org/sqlite`（纯 Go 驱动，无 CGO，保住 `make linux` 交叉编译）+ 标准库 `database/sql`。连接级 PRAGMA 挂在 DSN 上（WAL、`busy_timeout=5000`、`foreign_keys=ON`、`synchronous=NORMAL`），连接数固定为 1——SQLite 单写者，容量目标（≤100 用户、约 5,000 请求/日，约 180 万行/年）远低于其上限，不需要外部数据库服务。

**表清单**（数据库文件 `DATA_DIR/spore.db`，迁移内嵌于 `internal/store/migrate.go`，以 `PRAGMA user_version` 版本化、只增不改）：

| 表 | 内容 |
| --- | --- |
| `users` | Telegram 用户（主键即 User ID）、状态（pending/enabled/disabled/archived）、owner 标记、频率/额度/并发限额与最近拒绝记录 |
| `requests` | 一次提取请求的全生命周期：queued → processing → succeeded/failed，含 attempt、error_code 与媒体诊断元数据 |
| `usage_daily` | (user_id, 运营时区日 YYYY-MM-DD) 的当日用量与重置次数 |
| `audit_log` | 管理员/系统变更审计（actor/action/target/before/after JSON） |
| `events` | 系统异常事件，按 key 去重合并（count/last_at），open/resolved；通知成功时间用于 30 分钟冷却 |
| `settings` | 运行时键值（时区、去重窗口、队列容量、访问密钥哈希等） |
| `web_sessions` | 管理端会话（只存 ID 哈希）、CSRF token 与过期时间 |

**数据红线**：消息正文、caption、媒体本体与任何凭据（Token/Session/手机号）不入库；`data/session.json`、`data/peers.json`、`data/tmp/` 维持原有文件管理方式，不随数据库备份导出。时间字段统一为 Unix 毫秒时间戳。

**与执行链路的关系**：任务执行仍是内存队列（channel + worker + 退出 drain）；`requests` 行是状态事实来源，worker 在阶段边界更新。数据库不在媒体传输热路径上。

### 3.8 Bot 身份 MTProto 会话：大文件直传

官方 Bot API 服务器的上传硬上限是 50MB；超过该上限的媒体经 Bot 身份 MTProto
会话直接上传发送（`internal/mtproto/bot.go` + `botsend.go`）。历史上该会话曾
用于"媒体坐标引用直发 + 私聊接力"（09-02 任务），因坐标按账号签发、链路复杂
且与"下载 + 上传"双轨维护成本高而移除，改为承载大文件上传：

```text
worker（媒体 Size > BotAPIUploadCap）
 → delivery.routerSender（internal/delivery/router.go）
     ├─ BotClient.Available() 为假 → LARGE_CHANNEL_UNAVAILABLE 确定性失败（零网络）
     └─ mtproto.BotClient.SendMedia
          ├─ resolveUserPeer：bot 特权 UsersGetUsers(InputUser{UserID, AccessHash:0})
          │   反查目标用户 → InputPeerUser（内存缓存，跨重连保留）
          ├─ uploader.NewUploader(api).Upload(NewUpload(文件名, reader, Size))
          │   （>10MB 自动走 saveBigFilePart 大文件分片，上限 2000MB）
          ├─ uploadedMediaOf：按 Kind 构造 InputMediaUploadedDocument
          │   （MIME + video/audio 属性 + 文件名；photo 超限按 document 发送）
          └─ MessagesSendMediaRequest
              （caption 经 Caption.Limited 截断后透传原始实体；RandomID 防重放）
```

关键设计：

- **生命周期**：`BotClient.Run` 指数退避自动重启（5s 起、上限 5min、稳定运行 1min 后重置）——Bot 登录非交互，无需像用户号那样"离线等 Web 扫码"。就绪态在锁内换 `*tg.Client` 指针，所有发送先经 `Available()` 守门（gotd 连接随 `client.Run` 回调存活，回调内 `<-ctx.Done()` 维持连接）；会话只发不收（`NoUpdates`），入站更新仍走 Bot API 长轮询。
- **路由契约**：`delivery.LargeFileSender` 接口定义在 delivery（签名仅用 message 类型），`mtproto.BotClient` 隐式实现，`cmd/bot` 装配——两个包互不 import；"reader 必须非 nil"防御在 router 层执行。
- **数据源**：>50MB 的媒体来自内存重排序缓冲（≤ InMemoryLimit，边下边发）
  或经就绪水位门控的临时文件（超过 InMemoryLimit，边下边发）——两者都支持
  顺序阻塞读，bigLoop 单读多发不要求 reader 可重放。
- **caption 双语义**（`message.Caption`）：Bot API 上传路径 `RenderHTML()` 渲染 HTML；MTProto 直传路径 `Limited()` 按 UTF-16 预算截断后**透传源消息原始实体**——无"实体→HTML→实体"二次转换失真。两条路径同源于 `Item.Text/Entities`。
- **错误处理**：FLOOD_WAIT 由 SimpleWaiter 先内联等待，仍限流则按 `TELEGRAM_RATE_LIMIT` 失败；其余 tgerr 归 `BOT_SEND_FAILED`，原始 tgerr 经 AppError.Unwrap 链可判定。

## 4. 内部模型

```go
// tmeurl：链接解析结果（links.ts 移植）
type SourceRef struct {
    Kind      PeerKind // PeerUsername | PeerChannelID
    Username  string   // Kind=PeerUsername，不带 @ 前缀
    ChannelID int64    // Kind=PeerChannelID，存裸正 ID
    MessageID int
}

// message：标准化后的消息条目
// Telegram 中文本正文与媒体 caption 为同一字段（tg.Message.Message），Item 只存一份 Text
type Item struct {
    ID        int
    GroupedID int64                   // 0 表示非 Album
    Text      string                  // 正文或 caption
    Entities  []tg.MessageEntityClass
    Media     *Media                  // nil 表示纯文本
}
const AlbumMaxItems = 10 // Telegram 相册单组上限；取数窗口/整组发送/降级判断共用

// message：媒体描述（下载与上传所需的全部信息）
type Media struct {
    Kind      ItemKind
    Location  tg.InputFileLocationClass // 媒体坐标唯一来源：photo/document 的 ID/AccessHash/FileReference
    FileName  string                    // DocumentAttributeFilename / 缺省 "photo.jpg" 等
    Size      int64                     // FILE_TOO_LARGE 预检查与上传通道路由（Bot API / 大文件直传）
    Video     *VideoMeta                // Width/Height/Duration/SupportsStreaming
    Audio     *AudioMeta                // Title/Performer/Duration/Voice
}

// message：媒体 caption 的结构化描述（文本 + 原始 MTProto 实体，UTF-16 偏移）
// Bot API 上传路径经 RenderHTML 渲染 HTML；MTProto 大文件直传路径经 Limited 截断后透传实体
type Caption struct {
    Text     string
    Entities []tg.MessageEntityClass
}

// media：下载句柄（文件名由发送侧取 message.Media.FileName，不在此重复）
type Handle struct {
    Reader  io.Reader
    Cleanup func() // 关闭数据源唤醒读者、终止在途下载，并删除临时文件
}

// queue：任务
type Job struct {
    ID          string        // 时间戳+自增字符串
    ChatID      int64         // 目标私聊
    UserID      int64
    Ref         tmeurl.SourceRef
    StatusMsgID int           // "正在获取消息..." 消息 ID，完成后删除
}
```

worker 主流程（`internal/queue/worker.go`）：

```text
process(job)（取数固定 15min 窗口；发送窗口按媒体总量自适应，封顶 2h）:
  msgs, err := fetcher.Fetch(job.Ref)        // 失败 → SendMessage(UserText) 并返回
  items := message.Convert(msgs)
  单条文本 → delivery.SendMessage(item.RenderHTML())
  单条媒体 → sendMediaItem → openAndSend → media.Open
             → SendMedia(reader=句柄, caption=MediaCaption)
             （routerSender 按 Size 路由：≤uploadCap 走 Bot API，超限走大文件直传；
               下载 FILE_REFERENCE_EXPIRED → RefreshMedia 刷新后重试一次）
  Album(>1) → AlbumGroupable 预检（Bot API 承载 ∪ video≤MaxFileSize 的
            MTProto 整组承载；不可整组成员导致降级逐条）
            → 并发打开全部句柄（上限 2，任一失败整体失败并清理）
              → SendAlbum(成员逐个携带自己的 caption；路由分流 Bot API
                sendMediaGroup 或 MTProto 两阶段整组直传)
  最后 delivery.DeleteMessage(StatusMsgID)
```

## 5. 错误模型

```go
type AppError struct {
    Code    Code   // 机器可读错误码
    Message string // 内部描述
    Cause   error  // 原始错误（Unwrap）
}
func UserText(code Code) string // 面向用户的中文提示
func From(err error) *AppError  // 把 gotd/Bot API 错误分类为 AppError
```

| 错误码 | 触发条件 | 用户提示（中文） |
| --- | --- | --- |
| `INVALID_URL` | `tmeurl.Parse` 失败 | 无法识别有效的 t.me 消息链接，请检查后重试。 |
| `MESSAGE_NOT_FOUND` | `tg.ErrMessageIdInvalid`、消息不存在 | 找不到这条消息，可能已删除或链接无效。 |
| `CHANNEL_NOT_ACCESSIBLE` | `tg.ErrChannelPrivate`、`ErrChatAdminRequired`、AccessHash 拿不到 | 无法访问该频道，请确认用户账号已加入该频道。 |
| `SERVICE_MESSAGE` | `*tg.MessageService` 或转换后无可提取内容 | 这是一条服务消息，没有可提取的内容。 |
| `MEDIA_UNSUPPORTED` | 贴纸、webpage 等暂不支持类型 | 暂不支持这种消息类型。 |
| `FILE_TOO_LARGE` | Size > MaxFileSize | 文件超过大小上限，暂无法发送。 |
| `MEDIA_DOWNLOAD_FAILED` | 下载流/临时文件失败 | 媒体下载失败，请稍后重试。 |
| `TELEGRAM_RATE_LIMIT` | `*tg.ErrorFloodWait` 兜底 | 请求过于频繁，请稍后重试。 |
| `BOT_SEND_FAILED` | Bot API / MTProto 其他发送错误 | 发送失败，请稍后重试。 |
| `LARGE_CHANNEL_UNAVAILABLE` | 超过 Bot API 上限的媒体遇到大文件直传通道（Bot 会话）未就绪 | 大文件发送通道暂不可用，请稍后重试。 |
| `INTERNAL_ERROR` | 未分类异常 | 处理失败，请稍后重试。 |
| `USER_NOT_AUTHORIZED` | 用户不在白名单（access 六步链第 1 步） | 此机器人仅限白名单用户使用，请先发送 /start 申请。 |
| `USER_PENDING` | 申请待审批 | 你的申请正在等待管理员审批，通过后即可使用。 |
| `USER_DISABLED` | 用户被禁用/归档，或重试其请求 | 账号已停用，如有疑问请联系管理员。 |
| `DUPLICATE_LINK` | 去重窗口内重复提交同链接 | 该链接刚刚已处理，无需重复提交。 |
| `RATE_LIMITED` | 提交间隔未到 | 提交过于频繁，请稍后再试。 |
| `QUOTA_EXCEEDED` | 当日额度耗尽 | 今日额度已用完，额度每天自动重置。 |
| `CONCURRENT_LIMIT` | 未完成任务数超限 | 你还有未完成的任务，请等待完成后再提交。 |
| `QUEUE_FULL` | 内存队列饱和 | 当前任务较多，请稍后再试。 |
| `INTERRUPTED` | 进程退出/重启中断的未完成任务 | 任务因服务重启被中断，请稍后重新发送链接。 |
| `RETRY_EXHAUSTED` | 受控重试超上限（累计含首次最多 3 次） | 该请求已达到最大尝试次数，无法再次重试。 |
| `STORE_UNAVAILABLE` / `STORE_MIGRATION_FAILED` / `STORE_CONSTRAINT` | SQLite 不可用 / 迁移失败 / 约束冲突（含停用 owner、重复添加等管理操作拒绝） | 存储类中文提示（见 `apperr.UserText`）。 |
| `WEB_AUTH_FAILED` / `WEB_LOGIN_LOCKED` / `WEB_CSRF_INVALID` / `OAUTH_STATE_INVALID` / `OAUTH_EXCHANGE_FAILED` | 管理端登录与 CSRF/OAuth 边界（`internal/web`） | 管理端页面中文提示（见 `apperr.UserText`）。 |

用户永远看到 `UserText`，原始异常只进日志（对齐旧 `errors.ts` 的边界设计）。

## 6. 配置参考

配置分两层：**环境变量**（`.env`，进程启动时读取，校验失败直接退出）与**数据库运行设置**（管理端"运行设置/频道设置/受邀设置"页写入 `settings` 表，部分项覆盖环境默认值，即时或重启生效）。

加载顺序：`godotenv.Load()`（.env，可选）→ `config.Load(os.Getenv)` → 校验失败直接退出。

环境变量按用途分组概览（默认值、校验规则、优先级与生效方式的**逐项完整说明以[配置参考](./configuration.md)为唯一权威来源**，此处不重复维护）：

| 分组 | 变量 |
| --- | --- |
| Telegram 凭据（必填） | `BOT_TOKEN`、`TG_API_ID`、`TG_API_HASH`；`TG_PHONE`/`LOGIN_MODE` 控制登录方式 |
| 处理与媒体 | `WORKER_COUNT`、`MAX_LINKS_PER_MESSAGE`、`MAX_FILE_SIZE`、`STREAM_LIMIT`、`IN_MEMORY_LIMIT`、`MEMORY_BUDGET`、`DOWNLOAD_THREADS`、`UPLOAD_THREADS`、`DOWNLOAD_CONNECTIONS`、`UPLOAD_CONNECTIONS`、`TEMP_DIR_MAX_SIZE` |
| 目录与运行 | `DATA_DIR`、`TEMP_DIR`、`LOG_LEVEL`、`WEB_ADDR`、`BOT_API_URL`、`ALLOWED_USER_IDS`（仅空库首启导入）、`DUMP_CHANNEL_ID`（缓存频道环境兜底） |
| Web 与安全 | `GITHUB_CLIENT_ID`、`GITHUB_CLIENT_SECRET`、`WEB_OAUTH_ENCRYPTION_KEY`、`WEB_TRUSTED_PROXY` |
| 事件与云盘 | `NOTIFY_COOLDOWN_MIN`、`EVENT_BOT_FAIL_THRESHOLD`、`EVENT_TASK_FAIL_THRESHOLD`、`EVENT_DISK_LIMIT_GB`、`RCLONE_BIN` |

`PhotoLimit` 不是环境变量：官方 Bot API 模式下为 10MB，本地 Bot API 模式下随 `MAX_FILE_SIZE` 放宽；图片超过该限制时降级为 document。

`config.Load(getenv func(string) string)` 注入 getenv 而非直接读全局，测试可传入独立环境（移植旧 `config.ts` 的设计）。

## 7. 登录与安全

### 7.1 MTProto 登录流程

```text
LOGIN_MODE=auto（默认）/ qr：
    导出 login token → 终端渲染二维码 → 手机 Telegram「关联桌面设备」扫码确认 → 导入授权
    （无需手机号、验证码与 2FA 密码；token 约 30s 过期自动刷新重绘）
LOGIN_MODE=phone：
    TG_PHONE → 发送验证码 → 终端输入验证码 → 2FA 密码（如设置）
两种模式产物一致：写入 data/session.json；后续运行 session 有效 → 静默自动登录。
auto 且已配置 TG_PHONE 时，扫码失败自动回退到验证码流程。
```

- 扫码基于 gotd `auth/qrlogin` 包：独立轮询循环（不依赖 updates 加速信号；用户号会话虽已轻量消费 update 用于频道事件，扫码轮询仍自成循环）；二维码由 `mdp/qrterminal/v3` 渲染。

**Bot 身份会话登录**（`internal/mtproto/bot.go`，09-02）：与用户号会话完全不同——
`Auth().Status` 未授权即调 `Auth().Bot(ctx, BOT_TOKEN)` 换取会话（非交互、秒级），
写入 `data/bot-session.json`；异常退出由 `Run` 循环指数退避自动重启，**没有任何人工环节**。
会话文件丢失/换机后下次启动自动重登（凭 Token 即可），敏感度低于用户号 Session。
- 验证码流程中 `terminalAuth` 实现 gotd `auth.UserAuthenticator` 接口（`Phone/Password/Code/AcceptTermsOfService/SignUp`）。
- 仅当 `client.Auth().Status()` 为未授权时进入登录流程。
- 首次启动顺序：**先完成 MTProto 登录，再启动 Bot 轮询**（Bot 不在线期间用户发的消息由 Telegram 排队补拉）。

### 7.2 安全边界

- `data/`（session.json、peers.json、tmp/）整体 gitignore；**Session 等效于账号控制权，绝不提交、不进日志**。
- 日志脱敏：不记录 Bot Token、API Hash、手机号、Session 内容、消息正文或完整媒体 URL；只记 job_id/user_id/message_id/媒体类型/大小/错误码，以及受控的消息定位字段。
- 权限默认 Deny All：白名单由数据库 users 表驱动（`internal/access` 六步校验链：状态/重复/频率/额度/并发/队列满），陌生用户经 `/start` 申请待管理员审批。
- 临时文件：每个 Job 的下载产物在发送后（无论成败）`Cleanup` 删除。

## 8. 与旧 TS 实现的关系

| 旧资产 | 处置 |
| --- | --- |
| `src/telegram/links.ts` 解析规则 | **逐条移植**为 `internal/tmeurl` |
| `tests/links.test.ts` | **全量移植**为 `parse_test.go`，另补 RE2 回归用例 |
| `src/config.ts` 校验语义 | 移植（BOT_TOKEN 正则、正整数校验、getenv 注入） |
| `src/util/errors.ts` 提示语义 | 移植进 `apperr.UserText` |
| `src/middleware/authorize.ts` | 演进为 `internal/access` 数据库白名单与额度控制（`ALLOWED_USER_IDS` 仅首启导入） |
| `src/bot.ts` / `main.ts` / grammY / copyMessage | 移除（无法实现受保护内容提取） |

## 9. 测试与验证

- 单元测试：`tmeurl`（全量移植+回归）、`apperr`（分类）、`config`（校验）、`message/html`（golden 表驱动，重点）。
- 每次变更的基础门禁：`go build ./... && go vet ./... && go test ./...`。
- 手动验收应覆盖公开、私有和受保护频道，以及文本、媒体、Album、错误提示和可转发性。当前仓库没有真实 Telegram、媒体、相册或部署验收记录；通过编译和单元测试不等于真机验收完成。

## 10. 明确不做（MVP 边界）

自动监控指定频道并同步新消息、批量迁移、多账号池、付费、OCR、AI 识别、去水印、转码、压缩、Redis 队列。Dockerfile、Docker Compose 和可选本地 Bot API 服务器属于当前已提供的官方部署方式；公网 HTTPS 反向代理由部署环境提供，不等于 Redis 等外部服务依赖已实现。产品化阶段已引入内嵌 SQLite 业务存储（§3.7）、进程内资源监控 `internal/monitor`（RSS/临时目录/CPU/传输速率采样与 `/api/v1/system-metrics`，非 Telegram 频道监控）与管理端 Web 服务 `internal/web`（认证底座 + 管理页面：审批、用户、记录、频道、频道加入、事件、设置、审计、备份/CSV 导出与 MTProto 扫码重连，§11），仍不引入任何外部数据库服务；后续改进方向以实际需求为准。

## 11. 代码执行顺序与运行时流程

> 本节按当前实现整理：入口 `cmd/bot/main.go`，各环节落在 `internal/` 对应包。
> §1.2 是"数据怎么流"的简化视图，本节是"代码按什么顺序跑"的完整视图。

### 11.1 进程启动顺序（cmd/bot/main.go）

```text
main()
 ├─ 1. godotenv.Load()              # 加载 .env（可选，失败忽略）
 ├─ 2. admin 子命令分流             # spore admin reset-key：只重置密钥后退出，不启动 Bot
 ├─ 3. config.Load(os.Getenv)       # 环境变量校验；失败 → stderr + exit(1)
 ├─ 4. newLogger(cfg.LogLevel)      # slog 文本日志 → stderr
 ├─ 5. os.MkdirAll(DataDir, 0700)   # data/：session.json 与 peers.json 落盘目录
 ├─ 6. 清理 TempDir 后重建          # 仅删除纯数字 job ID 命名的孤儿临时文件，避免误清目录
 ├─ 7. signal.NotifyContext(SIGINT, SIGTERM)
 ├─ 8. ApplyPendingImport           # 应用已确认的数据库导入候选（pending-import.json：
 │        #   保留当前 settings、清空 Web 会话、保存带时间戳 rollback 副本）；
 │        #   无 confirmed marker 的普通重启跳过
 ├─ 9. store.Open(DataDir/spore.db)  # 打开 SQLite 并自动执行版本化迁移（§3.7）
 ├─ 10. 媒体设置数据库覆盖          # max_file_size/stream_limit/temp_dir_max_size 覆盖 env；
 │        #   覆盖值非法时回退整套环境配置并产生 media.config_invalid 事件
 ├─ 11. transfercfg.Runtime         # 下载/上传线程与连接数：环境默认值 + 合法 DB 覆盖
 ├─ 12. cloud Manager               # 创建 cloud-drive.json 管理器（文件缺失 = 关闭态；
 │        #   损坏/校验失败保持关闭并产生 cloud.config_invalid，不阻断启动）
 ├─ 13. access.ImportLegacyWhitelist  # 首次启动（users 表为空）导入 ALLOWED_USER_IDS
 ├─ 14. notify Hub + rclone 探测    # 事件通知 Hub（冷却/阈值/owner 私聊）；rclone 可用性
 │        #   探测与每 10 分钟复查（cloud.disabled 事件的产生与自动恢复）
 ├─ 15. FailInterruptedRequests     # 上次遗留的 queued/processing 批量置 failed(INTERRUPTED)
 ├─ 16. queue.New(LoadQueueCapacity) # 内存队列与 access 服务在 MTProto 就绪前创建：
 │        # 容量经设置项 queue_capacity 配置（缺省 64，重启生效），
 │        # Bot 提交与 Web 审批/重试共用同一队列与 access 服务；
 │        # 资源监控服务（internal/monitor）同时启动采样 goroutine
 ├─ 17. Web 管理端（internal/web）   # EnsureAccessKey（首启打印密钥一次）→ 独立 goroutine，
 │        # **先于用户号 MTProto ready 启动**；监听 WEB_ADDR（默认 127.0.0.1:8080）；
 │        # 启动失败/panic 只记日志，不影响 Bot；注入 access/queue/join/云盘/
 │        # MTProto 登录会话等（管理页面与扫码重连入口）
 ├─ 18. mtproto.NewBotClient(cfg, log).Run(ctx)  # 独立 goroutine：Bot 身份 MTProto 会话
 │        # （data/bot-session.json，Auth().Bot 非交互登录；异常退出指数退避自动重启。
 │        #  仅服务大文件直传；未就绪时仅超限媒体失败，小文件主链路不受影响）
 └─ 19. mtproto.New(cfg, log).Run(ctx, ready)   # 交出控制权，见 11.2
```

### 11.2 MTProto 登录与就绪（internal/mtproto/client.go）

```text
Run(ctx, ready)                            # 重连循环：每轮一个完整 client 生命周期
 ├─ 每轮开始：takeWebLogin()               # Web 触发的重连走 Web 扫码呈现；首轮恒终端
 ├─ runOnce(ctx, ready, viaWeb)：
 │    ├─ floodwait.NewSimpleWaiter().WithMaxRetries(5)   # FLOOD_WAIT 自动等待重试
 │    ├─ telegram.NewClient(API_ID, API_HASH,
 │    │      { SessionStorage: data/session.json, NoUpdates: true })
 │    └─ client.Run(ctx, 回调)：
 │          回调：
 │          ├─ Auth().Status(ctx)
 │          ├─ 未授权 → login()：
 │          │    viaWeb → loginByQR(web)：扫码 URL 推送登录会话（Session.setQR），
 │          │      浏览器经 /mtproto/qr.png 渲染；失败回落"重启走终端登录"
 │          │    LOGIN_MODE=qr / auto → loginByQR()：终端渲染二维码，扫码确认
 │          │    auto 且配置了 TG_PHONE：扫码失败自动回退验证码流程
 │          │    LOGIN_MODE=phone（或上述回退）→ authFlow(terminalAuth)：
 │          │      手机号 → 终端输验证码 → 2FA 密码（如有）
 │          │    产物统一写入 data/session.json
 │          ├─ 已授权 → 静默登录（直接复用会话）
 │          └─ api := tg.NewClient(waiter.Handle(client.API().Invoker()))
 │                # 经 floodwait 包装的 API 客户端，随后传给 ready
 │             ready(ctx, api)             # 见 11.3；ready 返回本轮结束
 ├─ ctx 结束 → Run 返回 nil（正常退出路径）
 └─ 轮内异常退出（会话失效等）→ Session.setOffline(err) → waitRelogin(ctx)
      # 不再终止进程：Bot/worker 已随本轮回调 ctx 结束而停止，Web 管理端
      # 独立运行，管理员经 /mtproto/relogin 触发下一轮
```

**关键约束**：gotd 的连接只活在 `client.Run` 的回调作用域内，因此 Bot 轮询与 worker
都必须在 `ready` 回调里启动，`ready` 返回即本轮收工；每轮重连都会重新执行一遍 ready
（上一轮的 Bot/worker 已随上一轮 ctx 结束，无双跑重叠）。登录会话状态机（离线/登录中/
就绪、扫码 URL、重连信号）在 `internal/mtproto/session.go`，并发安全。


**Bot 会话生命周期**（`internal/mtproto/bot.go`，与用户号 Run 平行的独立 goroutine）：

```text
BotClient.Run(ctx)                       # main.go 第 18 步启动，指数退避循环
 ├─ runOnce：
 │    ├─ telegram.NewClient(API_ID, API_HASH,
 │    │      { SessionStorage: data/bot-session.json, NoUpdates: true })
 │    ├─ 回调内：Auth().Status → 未授权则 Auth().Bot(ctx, BOT_TOKEN)
 │    ├─ api := tg.NewClient(floodwait SimpleWaiter 包装)
 │    ├─ setReady(api) / defer setOffline()   # 锁内换指针；发送方经 Available() 守门
 │    └─ <-ctx.Done() 维持连接（无业务常驻回调，发送由 worker 按需发起）
 └─ 异常退出：5s 起指数退避（上限 5min，稳定运行 1min 后重置）自动重启；
      期间超过 Bot API 上限的媒体确定性失败（LARGE_CHANNEL_UNAVAILABLE），
      小文件主链路不受影响
```

### 11.3 ready 回调：组装核心链路（回到 main.go）

```text
ready(ctx, api)
 ├─ 1. fetcher := mtproto.NewFetcher(api, data/peers.json, log)
 │        # 构造时即加载 peer 缓存（ChannelID → AccessHash）
 ├─ 2. b, sender := botapi.New(Options{Cfg, Log, Queue, Access, Whoami})
 │        # 创建 go-telegram/bot（默认 handler）；队列与 access 服务在启动期
 │        # 已创建（§11.1 第 16 步），每轮复用；sender 经 SetSender 回填 access
 ├─ 3. sender := delivery.New(b, …) → router := delivery.NewRouter(sender, botClient, BotAPIUploadCap, MaxFileSize)
 │        # 业务发送走路由：Size ≤ Bot API 上限（官方服务器 50MB）→ Bot API 上传；
 │        # 超限 → Bot 号 MTProto 大文件直传；相册含超限成员 → 同通道整组直传；
 │        # 文本/删除 → Bot API。
 │        # hub.SetSender 用 Bot API 原始实现（通知只发文本，避免自激回路）
 ├─ 4. deps := queue.Deps{Fetcher, Sender: counted(router), Store, Media 下载参数, Log}
 ├─ 5. go q.Run(ctx, cfg.WorkerCount, queue.Process(deps))   # worker 协程组
 └─ 6. b.Start(ctx)                      # 阻塞长轮询；ctx 结束后等队列 drain 完再返回
```

`Whoami` 闭包经 MTProto 的 `users.getUsers` 查询自身账号，供 `/whoami` 命令验证通道。

### 11.4 运行期并发模型

| 协程 | 生命周期 | 职责 |
| --- | --- | --- |
| 主协程 | 启动后阻塞在 `b.Start` | Bot API `getUpdates` 长轮询 |
| worker × `WORKER_COUNT`（默认 1；Web 运行设置 `worker_count` 可按数据库覆盖 env，1–16，重启生效） | `q.Run` 内 WaitGroup 管理 | 从队列 channel 出队执行 `Process` |
| Web 管理端协程 | `web.Server.Run(ctx)`，MTProto 就绪前即启动，随 ctx 优雅关闭 | 管理端 HTTP：登录/会话/管理页面（审批/用户/记录/频道/事件/设置/审计/备份与 CSV 导出）、MTProto 扫码重连、探针；故障只记日志 |
| 流式下载协程 | 每个流式媒体一个，下载结束即退出 | 向 `io.Pipe` 写数据，与 Bot API 上传背压串联 |
| 内存管道下载协程 | 每个内存管道媒体一个（StreamLimit–InMemoryLimit 区间），下载结束即退出 | `downloader.Parallel` 多线程乱序写入 `reorderBuffer`，上传侧顺序阻塞读（边下边发）；消费方放弃经 Cleanup 终止 |
| 相册并发打开协程 | 每个相册成员一个（errgroup，并发上限 2），打开完成或整组失败即退出 | 并发执行 `openWithRefresh`（含 file reference 刷新重试），句柄按索引落位 |

- 队列是 `chan Job`（容量经设置项 `queue_capacity` 配置，缺省 64，重启生效）：`Enqueue` 非阻塞，满即拒（access 六步校验链末步检查，直接回繁忙）。
- botapi 的全部发送/删除都经 `opt.Sender`，统一享受 delivery 的错误分类与 429 重试。

### 11.5 一次用户请求的执行顺序

#### 阶段 A：接收与入队（internal/botapi/handler.go，轮询协程内）

```text
update 到达
 ├─ 1. 过滤：仅 Message、私聊、From 非空
 ├─ 2. 白名单 isAllowed（空表 Deny All）→ 失败回固定拒绝文案
 ├─ 3. 空文本 → textOnlyMsg
 ├─ 4. /start → HandleStart()；/help → helpText；/status、/health → 脱敏运行状态快照；/usage → 用量查询；/whoami → Whoami() 查询 MTProto 账号
 └─ 5. 其余文本 → handleLink：
       ├─ tmeurl.Parse 失败 → INVALID_URL 文案
       ├─ Queue.Full() → busy 文案（占位提示都省了）
       ├─ SendMessage("正在获取消息...") → 记 StatusMsgID（发送失败仅告警）
       ├─ NewJob + Enqueue；入队失败 → busy 文案 + 删除占位提示
       └─ 记日志：任务已入队
```

#### 阶段 B：worker 处理（internal/queue/worker.go，worker 协程内）

每个 Job 的超时分阶段管理（`runJob`）：取数固定 15 分钟窗口；转换后按媒体总量
自适应发送窗口（15min + 每 1MB 加 1s，封顶 2h——2GB 级文件需要更长传输时间）：

```text
 ├─ 1. Fetcher.Fetch(ref)                          # internal/mtproto/fetch.go
 │     ├─ ResolveInputPeer：
 │     │    username → ContactsResolveUsername（顺带收割 hash 入缓存）
 │     │    channelID → 缓存命中直接用；未命中 → walkDialogs
 │     │      遍历主列表+归档夹（各自翻页）收割 hash → 落盘 → 重查；
 │     │      仍无 → CHANNEL_NOT_ACCESSIBLE
 │     ├─ getMessages：一次批量拉 [ID-9, ID+9] 共 19 个 ID
 │     │    （频道走 ChannelsGetMessages，用户/群走 MessagesGetMessages）
 │     ├─ 在结果中定位目标：服务消息 → SERVICE_MESSAGE；空 → MESSAGE_NOT_FOUND
 │     └─ 目标有 GroupedID → 就地过滤同组消息，按 ID 升序返回（Album）
 ├─ 2. message.Convert(msgs) → []Item              # internal/message/convert.go
 │     photo 取最大尺寸；document 按 attributes/MIME 分 voice/audio/video/document
 │     （GIF 以 animated 属性 + video/mp4 按 video 处理）；
 │     贴纸、投票、网页预览等非 photo/document 类型统一标 Unsupported——
 │     网页预览在源码注释中称按文本处理，但无对应实现分支与测试，
 │     按未确认能力对待，不作为已支持承诺
 ├─ 3. 分支发送（统一"下载 → 上传"，路由见 §3.4/§3.8）：
 │     ├─ 相册（多条且首项 IsAlbumMember）→ sendAlbumGroup：
 │     │    AlbumGroupable 预检（Bot API 判定 ∪ video≤MaxFileSize 的 MTProto
 │     │    整组承载；不支持类型/超限图片/超大成员不进组）→ 不可整组时逐条发送；
 │     │    并发 openWithRefresh 打开全部句柄（上限 2，FILE_REFERENCE_EXPIRED 时
 │     │    RefreshMedia 刷新后重试一次）→ SendAlbum 整组发送（路由分流
 │     │    Bot API sendMediaGroup / MTProto sendMultiMedia）；
 │     │    defer 统一清理全部句柄
 │     └─ 单条循环：
 │          Media==nil → SendMessage(RenderHTML())
 │          Unsupported → MEDIA_UNSUPPORTED
 │          其他 → sendMediaItem → openAndSend：media.Open 下载 →
 │            SendMedia(reader=句柄)（routerSender 按 Size 路由 Bot API /
 │            大文件直传；下载过期错误刷新后重试一次）
 ├─ 4. 失败路径：apperr.From(err) → SendMessage(UserText(code))
 │     用外层 ctx 发送（超时窗口不影响错误提示送达）
 └─ 5. cleanupStatusMsg：删除"正在获取消息..."（尽力而为，失败仅 debug 日志）
```

#### 阶段 C：媒体下载（internal/media/handle.go）

```text
media.Open(ctx, api, media, jobID, opt, log)
 ├─ Size > MaxFileSize → FILE_TOO_LARGE（不发起下载）
 ├─ Size ≤ StreamLimit → 流式：io.Pipe + 协程 downloader.Stream
 │      上传端直接消费读端，全程不落盘（Cleanup 关闭 Pipe 读端）
 ├─ StreamLimit < Size ≤ InMemoryLimit → 内存管道：协程 downloader.Parallel
 │      (WithThreads(DOWNLOAD_THREADS)) 多线程乱序写 reorderBuffer，
 │      上传端顺序阻塞读——边下边发、全程不落盘；内存占用 = 文件大小
 │      （Cleanup 关闭缓冲：唤醒阻塞读者、终止下载）
 └─ 其余 → ToPath(WithThreads(DOWNLOAD_THREADS), TmpDir/<jobID>-<文件名>)
        多线程并行落盘 → *os.File；Cleanup = 关闭句柄 + 删除文件（成败都执行）
```

#### 阶段 D：投递（internal/delivery）

```text
 ├─ SendMessage / DeleteMessage：429 → 等 RetryAfter+1 秒后重试一次
 ├─ SendMedia：photo 超 sendPhoto 上限(10MB) → 归一为 document 发送；
 │      按 Kind 分发 sendPhoto / Video / Voice / Audio / Document
 ├─ SendAlbum（router 分流，caption 逐成员绑定在 AlbumEntry 上）：
 │      全员 ≤uploadCap 且 Bot API 可整组 → sendMediaGroup 整组原子发送
 │      （attach://<名字> 挂附件，每项各自渲染 caption HTML）；组内混入
 │      不支持类型或超限图片 → ErrAlbumNotSupported；
 │      含超限成员（video ≤2000MB）→ mtproto.BotClient.SendAlbum 两阶段：
 │      逐成员 uploader.Upload → messages.uploadMedia 注册（photo 走
 │      InputMediaUploadedPhoto，video 复用单发大文件的 document+video
 │      属性构造）→ AsInput 坐标引用 → messages.sendMultiMedia 一次整组
```

注意：媒体上传不做 429 重试——上传体是单次消费的流，中途失败无法安全重放，
以最终错误返回，由用户重发链接兜底。

### 11.6 错误与清理的统一路径

- gotd 与 Bot API 的错误分别在 `classifyTgError` / `classifyBotError` 处归类为 `AppError`。
- worker 只消费 `apperr.From(err).Code`：用户看到 `UserText` 中文提示，原始错误只进日志。
- 清理三处兜底：worker defer 清理临时文件句柄、handler 入队失败删占位提示、
  main 启动时清空 TempDir。

### 11.7 退出顺序

```text
SIGINT / SIGTERM
 → ctx 取消
   → b.Start 返回（停止轮询）
   → 队列 drain（exit 通知 + 状态清理）完成后 ready 返回
     → gotd 关闭连接 → Run 循环见 ctx 已结束而返回 nil（不再进入离线等待）
   → worker 协程经 ctx.Done() 退出
 → main 记录"已退出"，进程结束
```

### 11.8 全景流程图

```text
启动：
  .env → config 校验 → 日志 → data/ tmp/ 准备 → 信号 ctx
    → 数据库导入候选应用（如有）→ store.Open（迁移）
    → 媒体设置 DB 覆盖 → transfercfg 运行时 → cloud Manager → 白名单导入
    → notify Hub + rclone 探测 → 中断请求恢复
    → queue（容量读 settings）+ access 服务 + 资源监控
    → Web 管理端（独立 goroutine，先于用户号 ready 启动）
    → Bot 号 MTProto（独立 goroutine，大文件直传通道）
    → 用户号 MTProto：会话有效？─否→ 扫码 / 验证码登录（写 session.json）
    → ready：NewFetcher(peers.json) → botapi.New → go workers → b.Start 长轮询（阻塞）
    （会话失效等异常退出后进入离线等待，可经 Web 触发重连进入下一轮）

请求（用户私聊发 t.me 链接）：
  轮询协程：私聊过滤 → tmeurl.Parse → access 六步校验链
    （状态/重复/频率/额度/并发/队列满，通过即落 requests 行并扣额度）
    → 占位提示 → Enqueue
  worker：缓存频道复用命中 → 直接复制副本给用户（可跳过下列完整链路）；
    否则 Fetch（Resolve → 批量取 → Album 聚合）→ Convert →
    文本 → RenderHTML → SendMessage
    媒体 → media.Open（流式 / 内存 / 临时文件 / 拒绝）→ SendMedia / SendAlbum
    成功 → 删除占位、写缓存频道干净副本、复制到用户绑定频道
    失败 → UserText 中文提示；最后删除占位提示
  → 用户收到一条全新消息，可正常转发 ✅
```
