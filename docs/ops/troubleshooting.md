# 问题与解决记录

> 开发过程中实际踩过的坑及其修复，按时间倒序排列。新条目加在最上面。
> 排查线上问题时先查这里，再查各库版本的适配注意事项（见文末）。

## 2026-09-20 切段失败 "Unable to choose an output format" / "'matroska' is not known"（exit 234）→ 镜像精简 ffmpeg 缺封装器

**现象（真机，两轮）**：2.1GB 视频切段时 ffmpeg 先报
`Unable to choose an output format for '...part1of2.mp4'`，显式 `-f matroska`
后又报 `Requested output format 'matroska' is not known`（exit 234 = EINVAL）。
混合相册整组失败（INTERNAL_ERROR），白下了 6 分钟。

**根因（两轮后才定位准）**：项目 Dockerfile 为控制镜像体积，自行编译的
**精简 ffmpeg 只启用了 `--enable-muxer=mjpeg`**（抽帧往管道输出单帧用的）
——没有 matroska/mp4 等任何视频封装器。第一轮按扩展名猜输出格式自然失败；
第二轮显式 `-f matroska` 直接点名要一个不存在的 muxer，同样失败。此前
首轮诊断猜的"源扩展名大写/空白导致猜测失败"是错的——任何扩展名都猜不中，
因为 muxer 根本没编译进去。历史包袱：该构建是为"抽帧封面"定制（只需要
mjpeg raw 封装），可播放切段引入后需求变了，Dockerfile 没跟上。

**方案**：
- Dockerfile 精简构建追加 `--enable-muxer=matroska`（切段输出容器），并在
  构建期自检 `ffmpeg -muxers | grep matroska`（缺了构建即失败，不再静默
  带病出镜像）；muxer 显式 `-f matroska` 保留（不依赖扩展名猜测）；
- 启动期能力探测 `media.CheckMatroskaMuxer`：自定义 FFMPEG_PATH 指向阉割
  构建时启动即 Warn，不再等到第一个 2GB 任务失败才发现；
- **切段能力前置校验、不降级**（第三轮,产品决策）：超限视频在下载开始前
  校验切段条件（单媒体 `ensurePlayableSplit` / 相册 `planAlbumSend` 报
  `SPLIT_UNAVAILABLE` 整组原子失败），不满足不发起下载、不降级字节分段；
  运行期切段失败原样报错。字节分段只保留给非视频媒体（唯一路径而非降级）。

**要点（防复发）**：
- 给 ffmpeg 的能力做任何假设前先探测（启动期 `-muxers` 查询，秒级）；
- 修改 Dockerfile 精简编译开关时，必须对照全部 ffmpeg 调用方所需能力
  （当前清单：抽帧 = mov/matroska 等 demuxer + 解码器 + scale filter +
  mjpeg 编码/封装；切段 = 同源 demuxer/parser + matroska muxer）；
- 切段不可用 = `SPLIT_UNAVAILABLE` 下载前报错，不降级——字节分段仅限
  非视频媒体，别给视频加回退。

## 2026-09-20 拆分投递的两处修正 → 可播放视频分段 + 缓存频道副本按组修复

**现象（真机）**：① 拆分以字节分段 document 投递，用户必须下载全部分段手工
合并，不能直接播放；② 混合相册（图片 + 大视频）图片先发出、任务成功后
缓存频道却始终没有副本；③ 拆分任务下载完成后才开始上传（无交叠）。

**根因与方案**：
- ①字节分段不可解码是切割方式的必然——改为 **ffmpeg 流复制切段**
  （`-c copy` 不转码、无损、秒级）：切出的是真实视频文件，以 video 形态
  上传（挂 DocumentAttributeVideo），每段点开即播；切段点对齐关键帧，段
  大小 1900MB→1800MB（关键帧偏移 margin）。非视频媒体走字节分段；视频
  切段能力不足时直接报错（SPLIT_UNAVAILABLE，不降级）——
  或切段失败仍回退字节分段。
- ②`dumpcache.WriteClean` 要求 items 与已发送消息**数量一致**，拆分把一个
  条目展开为多条，校验必然失败 → 副本整体跳过。改为 **sentSpans 按源条目
  分组**（worker 的 sentIDs 记录每个条目展开的消息 ID 组），整批复制保组后
  仅清洗每个 span 首条 caption。
- 混合相册同时原子化：`planAlbumSend` 预检（图片 + 可拆视频 → 同一条相册
  `[图片, 段1, 段2]` 整组直传，任务级原子），图片不再先发。
- ③拆分场景的串行（完整落盘 → ffmpeg 切段 → 上传）是可播放切段的前提
  （流复制要读到完整文件；moov 在尾部必须读文件尾），≤2000MB 媒体的
  边下边传不受影响。

**要点（防复发）**：
- `sentIDs.spans` 与 `mediaMeta.SentSpans` 是"源条目 → 消息 ID 组"的唯一
  映射，SendAlbum 的返回 ID 与 entries 同序，按成员 plan 计数切分；
- 拆分段以 video 形态发送的前提是 ffmpeg 流复制成功（真实可解码）；任何
  字节分段路径只承载非视频媒体，必须保持 document 形态（不挂 video 属性）；
- 拆分磁盘峰值 ≈ 2× 文件大小（完整落盘文件 + 分段文件），切段前经
  `media.CheckTempDir` 对"再落一份"做余量预检。

## 2026-09-19 超过 2000MB 的文件 FILE_TOO_LARGE 失败 → 分卷拆分投递（split）

**现象**：受保护频道的 2GB+ 视频走到下载完成才以 `FILE_TOO_LARGE` 失败
（`media.Open` 大小预检），白耗流量与磁盘；Bot 身份的 2000MB 是服务器端
硬限制（4000 part × 512KB，Bot 无 Premium、本地 Bot API 服务器同样 2000MB），
任何代码层面都无法突破。

**方案**：**分卷拆分投递**（`internal/queue/split.go`）——超过单文件上限的
媒体按 `SplitSegmentSize`（1900MB，距 4000 分片边界留 margin）切段，完整
落盘后以全 document 相册整组直传为**同一条消息**（相册 10 成员上限 →
单条消息约 19GB，超出仍 `FILE_TOO_LARGE`）。`media.Open` 对可拆分媒体不再
拒绝，但强制临时文件路径；`Handle` 新增 `Path/WaitDownloaded/OpenSection`
（区间读取与 ffmpeg 定位抽帧的前提是完整落盘）。分段是纯字节切割、不可
独立播放，以普通 document 发送（**不要挂 video 属性**——假播放器点了必败）；
逐段封面第 2 段起不能对分段字节直接抽帧（无容器头），按
"段起始字节 / 总大小 × 总时长"换算时间戳对完整文件 `ffmpeg -ss` 抽帧
（`media.ExtractFrameAtJPEG`）。`delivery_mode` 新增 `split`（仅成功时；
失败回落 upload 由错误码记录原因）。

**要点（防复发）**：
- 相册规则：Telegram 不允许 document 与 photo/video 混组，但全 document 组
  合法——router 的 `SendAlbum` 只对拆分路径放开 document 成员，
  worker 相册预检（`AlbumGroupable`）不感知，普通相册 document 成员仍逐条；
- 拆分放弃了"边下边传"交叠（`WaitDownloaded` 先行）——抽帧与区间读取都
  依赖完整落盘；2h 任务硬超时对慢链路超大文件仍是风险边界；
- caption 与来源脚注只挂首段，`message.Caption.WithNote` 尾部追加纯文本
  不平移既有实体偏移。

## 2026-09-11 并发传输内存打满 → 进程级内存预算闸门（降级临时文件路径）

**现象**：同时传多个记录（多任务并发、或相册多成员）时 CPU/内存接近打满，
整体效率下降。

**根因**：内存重排管道按**整个文件大小**预分配内存（`newReorderBuffer` 的
`make([]byte, size)`），单文件最多驻留 512MB（`IN_MEMORY_LIMIT`）；相册把全部
成员句柄同时打开再整组发送，一个相册 Job 最坏 10×512MB；总内存 =
`WORKER_COUNT × 单Job内存` 线性放大，此前没有任何总量闸门。

**方案**：新增**进程级内存预算闸门**（`media.Options.Memory`，
`internal/media/memorybudget.go`）：媒体进入内存管道前按文件大小记账
（`TryAcquire` 非阻塞），预算不足的新文件**自动降级临时文件路径**
（边下边传语义不变，不排队、不失败）；预算在 `Handle.Cleanup` 时归还
（`sync.Once` 保证与获取配对）。额度 = env `MEMORY_BUDGET`（默认 1GB，
64MB–8GB）+ 管理端 `memory_budget` 键（即时生效）。效果：常驻内存被额度
封顶，不再随并发任务数/相册成员数放大，超额流量转移到磁盘管道
（磁盘占用仍由 `TEMP_DIR_MAX_SIZE` 兜底）。

**同批 CPU 侧小修**：
- `transferGate`（MTProto 传输 RPC 限流）从 50ms 忙等轮询改为 `sync.Cond`
  睡眠通知——饱和等待必然伴随其他传输在持续完成（每次完成 Broadcast），
  ctx 取消感知延迟以单次在途 RPC 为上界，消除高并发等待的自旋空转；
- `checkTempDir` 的目录占用统计加 30s TTL 缓存（相册逐成员触发预检，
  之前每次都全树遍历临时目录）；fail-open 语义与测试注入点不变；
- `monitor` 新增进程 CPU 占用采样（`cpu_percent`，getrusage 差分归一
  0–100），管理端"资源与传输监控"新增 CPU 图——验证本类优化效果依赖该指标。

**要点（防复发）**：
- 排查内存问题时先看管理端"运行设置"里的内存预算与 `IN_MEMORY_LIMIT`
  的区别：前者是**进程总量**封顶（即时生效），后者是**单文件**上限（重启生效）；
- 验证方式：批量提交大文件任务，监控页 RSS 应稳定在预算值附近封顶；
- 预算调小只影响新打开的媒体（软上限），在途传输自然回收，不会中断任务；
- 不要把降级当成故障：预算不足走临时文件路径是设计行为（日志为
  Debug 级"内存预算不足"，落盘路径另有 Info 日志）。

## 2026-09-03 移除引用直发/私聊接力 → 统一"下载 + 上传"，大文件经 Bot 号 MTProto 直传

**动机**：引用直发 + 私聊接力链路复杂（中转消息、按文件全局 ID 订阅、revoke 清理、
频道短路缓存、两个管理端开关），与"下载 + 上传"双轨并行维护成本高；且接力依赖
源频道坐标的可用性。改为统一"下载 → 上传"，超过 Bot API 50MB 上限的媒体复用
Bot 身份会话直接上传（gotd `uploader` + `InputMediaUploadedDocument`，上限 2000MB），
大文件能力保留且无需部署本地 Bot API 服务器。

**方案**：`delivery.routerSender` 由"reader==nil 且 Referable → 引用"改为
"Size > BotAPIUploadCap → 大文件直传"；`Media.Referable`、`relay.go`、`botupdates.go`、
`refreject.go`、`prefer_media_reference`/`allow_reference_fallback` 开关全部移除；
`MAX_FILE_SIZE` 默认放宽到 2000MB 且不再强制 `BOT_API_URL`。历史行的
`delivery_mode=reference/mixed` 仅作展示保留；当前新请求产生 `upload`（媒体）或
`text`（纯文本），云盘任务为 `cloud`，缓存频道复用命中为 `reuse`。

**要点（防复发）**：
- Bot 会话上传用 `uploader.NewUpload(文件名, reader, Size)`：>10MB 自动走
  `saveBigFilePart`，总上限 4000 part × 512KB = 2000MB（`MaxMediaFileSize` 与之对齐）；
- >50MB 媒体必然超过 StreamLimit（默认 20MB）走临时文件，失败重试不受
  io.Pipe 单次消费限制——不要把大文件改成流式管道直传；
- `BotClient` 会话只发不收（`NoUpdates: true`），入站更新仍走 Bot API 长轮询；
- 任务超时改为分阶段：取数固定 15 分钟，发送窗口按媒体总量自适应（封顶 2h），
  2GB 级文件不能按固定窗口砍断；
- 保留的 `RefreshMedia`/`IsFileReferenceExpired` 属下载路径共用能力
  （下载同样会遇到 `FILE_REFERENCE_EXPIRED`），勿随引用逻辑删除。

## 2026-09-02 Bot API 拒收合成 file_id → Bot 身份 MTProto 引用直发

> 注：本条机制已被 2026-09-03 条目移除（Bot 会话改作大文件直传），下文为历史记录。

**现象**：09-01 任务把源媒体编码成 Bot API `file_id`（gotd `fileid` 包）直发，
官方 Bot API 一律以 `400 wrong file identifier/HTTP URL specified` 拒收——发生在
解析阶段，服务器不认识它没签发过的 file_id（平台限制，非编码缺陷）。

**方案**：引用发送改经 **Bot 身份的第二个 gotd 客户端**（`Auth().Bot(ctx, BOT_TOKEN)`
非交互登录）：`messages.sendMedia / sendMultiMedia` 直接消费媒体坐标
`id+access_hash+file_reference`（构造 `InputMediaPhoto/Document`），MTProto 层没有
"自签发"限制。目标 peer 用 bot 特权 `UsersGetUsers(InputUser{UserID, AccessHash:0})`
反查。WTelegramBot 即此机制，gotd 能力对等。

**真机后续（同日）**：直连引用被 400 `MEDIA_EMPTY` 拒——媒体坐标（file_reference 可达性）
**按账号签发**，Bot 不能消费用户号取到的坐标。WTelegramBot 源码复核确认其 file_id 全部
来自 bot 自己收到的消息（`Bot.Updates.cs` 接收侧自签 → `Bot.TL.cs:394` 发送侧解码），
不存在跨账号引用。修复 = **私聊接力**（`internal/mtproto/relay.go`）：用户号把源媒体引用
发进用户号↔Bot 私聊 → Bot 从自己的 UpdateNewMessage 拿自签坐标（私聊消息 ID 按账号独立
编号，按文件全局 ID 关联）→ 直发 → revoke 清理中转消息。

**要点（防复发）**：
- tgerr 错误文案含下划线（`FILE_REFERENCE_EXPIRED`），旧的空格关键词匹配
  （`"file reference"`）覆盖不到 MTProto 错误——引用错误分类必须走 `tgerr.Is`
  结构化判定（AppError.Unwrap 链可达），见 `internal/delivery/reference.go`；
- 私聊消息 **ID 按账号独立编号**：用户号 sendMedia 返回的 ID 与 Bot 侧消息 ID 不同，
  不能跨会话按 ID 取消息；中转关联必须按**文件全局 ID**（photo/document 的 ID）订阅，
  且先订阅后发送；
- 中转消息 revoke 清理必须在成败两路径都执行（defer），用剥离取消信号的限时 ctx；
- `MessagesSendMediaRequest.Entities` 是 flags 条件字段，必须经 `SetEntities`
  （直接赋值不置位会丢实体）；`RandomID` 每次必须非零（防重放）；
- gotd 连接随 `client.Run` 回调存活：Bot 会话的 `*tg.Client` 在锁内换指针，
  发送前必须 `Available()` 守门；回调内 `<-ctx.Done()` 维持连接；
- worker 回退上传的各点必须值拷贝 `Media` 并清空 `Referable`（"reader 与引用
  二选一"契约，`contractSender` 测试把关）；
- 引用直发不经手媒体字节，**不受 MAX_FILE_SIZE 约束**（官方模式 >50MB 可直发）；
  `BOT_API_URL` bigfile profile 仅剩"上传兜底路径超 50MB"一个用途。

## 2026-08-27 复盘 二轮：泄漏、调度器与命名契约（防复发）

- **流式 Handle.Cleanup 曾为 nil**：上传中途放弃时下载 goroutine 阻塞在 Pipe 写端直到 15 分钟任务超时（相册最多同时泄漏 10 个）。修复：流式路径也设 Cleanup（CloseWithError），消费方 defer 即可全覆盖。
- **floodwait 曾用调度版 Waiter**：每次 invoke 走调度链（~0.5ms + 锁/堆）、**把全部 MTProto 调用串行化**（相册并发下载退化单路）、常驻 1kHz ticker。低并发场景应使用 `NewSimpleWaiter()`（透传 + 遇 FLOOD_WAIT 才内联睡）。
- **临时文件命名契约曾散落四处**（queue 生成 ID、worker 拼 key、media 落盘、main 反向解析）：现收敛为 `media.TempName/IsTempName/CleanOrphans`，命名与识别同源；queue 的 Job.ID 必须保持纯数字。
- **QR 扫码横幅曾因 DC 迁移丢失**：attempt 计数器被迁移重试递增，横幅只在 attempt==1 打印——用独立的 `shown` 标志表达"是否已渲染"。
- **进程退出时 drain 曾无人等待**：`go q.Run(...)` fire-and-forget，退出通知大概率被进程终止截断。main 现在等 `queueDone` 再退出；worker 在已取消的 ctx 上不再白打 Bot API（由 drain 的 WithoutCancel 窗口接管）。
- **大文件约束沉到 config**：MAX_FILE_SIZE 超官方 50MB 上限而无 BOT_API_URL 时启动期即拒绝；BOT_API_URL 校验 URL 形态；PhotoLimit（sendPhoto 10MB 降级线）随服务器模式计算（本地服务器 = MAX_FILE_SIZE）。

## 2026-08-27 大文件（>50MB 视频）无法发送——本地 Bot API 服务器支持

**现象**：频道里的原片视频动辄几百 MB，标准 Bot API 服务器上传硬上限 50MB，`FILE_TOO_LARGE` 直接拒绝。

**方案**：官方 telegram-bot-api 以 `--local` 模式自建，上传上限升至 2GB（下载侧同理）。
代码侧通过 `BOT_API_URL` 环境变量 + `tgbot.WithServerURL` 切换，`.env.example` 有模板，
部署方式见 README「大文件支持」一节（Docker 一行命令）。

**要点**：
- 50MB 是服务器侧硬限制，**不要只调大 MAX_FILE_SIZE 而不接本地服务器**——会变成"下载完 2GB 后被 Telegram 拒收"，白耗流量与磁盘；
- 接本地服务器后大文件仍走 `TEMP_DIR` 临时文件路径（>20MB），磁盘余量需 ≥ 最大单文件；
  （2026-09 后媒体管道更新：`STREAM_LIMIT`–`IN_MEMORY_LIMIT` 区间默认走内存重排管道，
  超过内存上限的大文件才落临时文件，此条磁盘余量结论对后者仍然成立。）
- 本地服务器与 MTProto 用户账号无关，api-id/hash 只是服务器注册参数。

## 2026-08-27 扫码登录闪退回退到验证码（DC 迁移未接线）

**现象**：`make dev` 首次登录不显示二维码（或闪现即失败），直接进入验证码 + 2FA 密码提示。

**根因**：Telegram 账号注册在非默认数据中心（国内用户常见 DC4/DC5）。QR 登录的
`auth.exportLoginToken` 在默认 DC2 上会返回 `loginTokenMigrateTo`，要求客户端先迁移到
账号所在 DC。当时 `qrlogin.NewQR` 未传 `Options.Migrate` 回调，迁移需求被当作致命错误；
又因 auto 模式且 `TG_PHONE` 有值，静默回退到手机号验证码流程（仅一行易漏看的 WARN）。

完整链路（迁移发生在第二步，缺失即卡死在此处）：

```text
你的客户端 ──连──▶ DC2（默认入口）
                      │
                      │ auth.exportLoginToken（请求二维码）
                      ▼
              DC2：这账号不归我管，它在 DC4
                  返回 loginTokenMigrateTo(dc=4)     ← 缺 Migrate 回调时在这里失败
                      │
                      ▼
客户端迁移：断开 DC2，改连 DC4（MigrateTo 做的事，只换连接不动数据）
                      │
                      │ 再次 auth.exportLoginToken
                      ▼
              DC4：这是你的二维码 ✅ → 手机扫码 → 授权成功
```

**解决**（internal/mtproto/auth.go）：
- `loginByQR` 持有 `*telegram.Client`，`Options{Migrate: client.MigrateTo}`（Import 阶段迁移由 qrlogin 内部处理）；
- `Export` 返回 `*qrlogin.MigrationNeededError` 时显式 `client.MigrateTo(ctx, dcID)` 后重试导出；
- 回退发生时终端显式打印 `⚠ 扫码登录失败，回退到手机号验证码登录`。

**预防**：登录类新流程上线前，用一个非默认 DC 的账号实测；回退等关键分支必须有用户可见的提示，不能只靠日志。

## 2026-08-27 代码审查 修复的 9 项

| 现象/风险 | 根因 | 解决 |
| --- | --- | --- |
| >100 对话的账号拿不到私有频道 AccessHash，误报"账号未加入"且缓存不完整持久化 | `walkFolder` 只认 `*tg.MessagesDialogs`，对话超 limit 时 Telegram 返回 `MessagesDialogsSlice`，被当遍历结束 | type switch 同时接收两种形态，统一抽 Dialogs/Messages/Chats 字段 |
| 带 ``` 代码块的消息发送必失败（can't parse entities） | Pre 带语言渲染成 `<pre class="language-x">`，Bot API 只认 class 挂在内层 `<code>` 上 | `classify` 直接返回完整开/闭标签串：`<pre><code class="language-x">…</code></pre>` |
| `TEMP_DIR` 误配到已有目录（如 `data`、`/tmp`）会被启动清理递归清空 | 启动时无护栏 `os.RemoveAll(TempDir)` | 只删纯数字任务 ID 命名的临时条目（当前实现为 `media.IsTempName` / `media.CleanOrphans`）+ config 拒绝 `TEMP_DIR == DATA_DIR` |
| Ctrl-C 时排队任务被静默丢弃，"正在获取消息..."提示永久滞留 | `Run` 的 worker 循环在 `ctx.Done` 直接退出 | 退出后用 `context.WithoutCancel`+10s 窗口 drain，`Discard` 处理器通知用户并删状态消息 |
| 相册降级发生在整组下载开始之后，白下载一轮 | 类型/大小判定纯看元数据，却放在句柄打开后 | `delivery.AlbumGroupable` 谓词前置预检（与 SendAlbum 同源） |
| `/start hello`、`/help@bot` 落进链接解析器 | 命令精确字符串匹配 | `commandOf()`：取首词 + 剥 `@botname` + 小写 |
| `reply()` 绕过 LOG_LEVEL 配置 | 用了包级 `slog` 而非 `opt.Log` | 统一走 `opt.Log` |
| 轮询拉取全部 update 类型 | 丢了旧版 `allowed_updates: ["message"]` | `tgbot.WithAllowedUpdates` |
| `/status`、`/health` 被当作链接解析 | Bot 命令未分流 | handler 显式处理命令，并由启动阶段注册 Telegram 菜单 |
| `git add .` 会把 `./bot` 二进制提交进库 | .gitignore 未含 | 加 `/bot` |

## 2026-08-27 复盘 批次要点（防复发）

- `peerCache` 只在有新增条目时落盘（此前每条公开链接任务都全量写 JSON 且持锁序列化）；
- `Item` 只存一份 `Text+Entities`——Telegram 的正文与 caption 本就是同一字段（`tg.Message.Message`），存两份必然漂移；
- `asRateLimit` 是 429 的唯一解包点；`classifyTgError` 未识别错误一律 `INTERNAL_ERROR`，调用点一行收口（避免兜底时机分散导致错误码被覆盖）；
- `Fetcher.RefreshMedia` 承载 file_reference 失效刷新，单条与相册路径共用（机制沉到拥有引用的层，别在编排层按路径打补丁）；
- `message.AlbumMaxItems = 10` 单一来源：mtproto 取数窗口、delivery 整组上限、queue 判断共同引用；
- `Fetch` 首查即取邻域窗口（1 条与 19 条同一次 RPC 成本，相册省一次往返）。

## 2026-08-27 早期实现 bug

- **MarkedChatID 公式错误**：`-100` 前缀是**字符串拼接**语义（`c/456 → -100456`），不存在固定加性偏移 `-(id + 1e13)`。展示用 marked ID 必须按 `"-100"+id` 拼接构造。
- **HTML 截断**：只追加"已截断"附注没裁剪正文，且附注本身会超出 4096 上限——截断预算必须为附注预留空间。
- **同位置多闭包嵌套**：`<b><i></b></i>` 非法嵌套——事件排序在"同位置先闭后开"之外，还需"同位置多闭包按 span 起点降序"（后开的先关）。

## 库版本适配备忘（gotd/td v0.161.0 / go-telegram/bot v1.24.0）

| 坑 | 正确用法 |
| --- | --- |
| 消息 ID 入参类型 | `[]tg.InputMessageClass`（`&tg.InputMessageID{ID: n}`），不是 `[]int` |
| 消息响应归一化 | `cls.AsModified()` 返回 `ModifiedMessagesMessages` 接口，再 `GetMessages()`；`MessagesSlice` 类型不存在于导出面 |
| tgerr 包路径 | `github.com/gotd/td/tgerr`（顶层，非 `telegram/tgerr`） |
| floodwait 中间件 | `floodwait.NewWaiter().WithMaxRetries(n)` 包 `client.Run`；API 包装用 `waiter.Handle(client.API().Invoker())` 再 `tg.NewClient` |
| 请求结构体化 | `ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{...})`（struct 指针，非散参） |
| QR 登录 | `qrlogin.NewQR(api, id, hash, qrlogin.Options{Migrate: client.MigrateTo})`；Export 的 `MigrationNeededError` 需自行迁移重试（见首条记录） |
| HandlerFunc 签名 | `func(ctx, *tgbot.Bot, *models.Update)`——**无 error 返回**，错误须局部记录 |
| InputFileUpload | 接口方法为指针接收者，必须传 `&models.InputFileUpload{...}` |
| SendMediaGroup 附件 | `Media: "attach://<名>"` + `MediaAttachment io.Reader`；media 组用 `[]models.InputMedia`（无 AnyInputMedia） |
| Bot API 错误形态 | 429 → `*tgbot.TooManyRequestsError{RetryAfter}`；其余用哨兵 `errors.Is(err, tgbot.ErrorBadRequest/ErrorForbidden/...)`；无 `ErrorResponse` 导出类型 |
| dialog 遍历响应 | 超过 limit 返回 `*tg.MessagesDialogsSlice`，与 `*tg.MessagesDialogs` 字段一致需分别处理 |

## 2026-09-03 相册整组直传：协议约束与下载 ctx 生命周期（防复发）

- **`messages.sendMultiMedia` 只接受已注册媒体引用**：直接携带 raw `inputMediaUploaded*` 构造器被 400 `MEDIA_INVALID` 拒绝（真机稳定复现）。正确流程：逐成员 `uploader.Upload` → `messages.uploadMedia`（目标 peer）→ `Photo/Document.AsInput()` 坐标 → `sendMultiMedia`。注意单媒体 `messages.sendMedia` 不受此约束（raw 构造器可用）。
- **errgroup 的 ctx 在 `Wait` 返回（含全员成功）时即被取消**：流式/内存管道成员的下载在句柄打开后仍在后台进行，把 gctx 传进 `media.Open` 会让整组打开完成的一刻后台下载全部死亡，上传读源以 `MEDIA_DOWNLOAD_FAILED(context canceled)` 失败——相册含临时文件成员（同步 ToPath 阻塞数分钟）时延迟到其完成后触发，纯流式/内存成员时立即触发（0.5 秒级失败）。修复：`sendAlbumGroup` 的下载用独立 `openCtx` 覆盖整组发送全程，成员失败时显式取消；回归测试用真实字节假 invoker + 读尽 Reader 的假 Sender 锁定（`TestWorkerUploadAlbumStreamsSurviveGroupOpen`）。
- **假 Sender 不消费 reader 的测试盲区**：上传路径的读源错误只在消费 Reader 时才暴露，"整组成功"类测试必须真正读尽数据源才能覆盖下载生命周期（`consumeAlbumReaders`）。

## 2026-09-07 频道加入（/join）的机制边界

- **私有频道邀请链接只有管理员能生成**：普通成员在客户端看不到链接，只能用自己入群时拿到的链接或向管理员索取——错误文案已包含该指引，不是缺陷。
- **Bot 无法通过邀请链接入群**：`messages.importChatInvite` 只对用户账号有意义；/join 加入的是扫码登录的**读取账号**（session.json 对应会话），不是 Bot。
- **"拒绝被拉入"不存在服务端开关**：Telegram 隐私设置只覆盖群组不覆盖频道，任何频道管理员可不经同意拉任意账号入频道。「自动退出外部拉入」（`join_auto_leave_external`，默认关）开启后的拦截是**惰性事后执行**：已加入频道页刷新与 /join 提交时 diff 实时列表与留痕，外部拉入的未记录频道自动退出；默认关闭时不做任何自动退出。
- **加入后立即归档会失败**：`folders.editPeerFolders` 要求对话已存在于对话列表，`importChatInvite` 返回后对话有服务端传播窗口，立即调用以 PEER_ID_INVALID/CHANNEL_INVALID 失败。必须先 `messages.getPeerDialogs` 确认对话建立，未就绪按 1s/3s 退避重试（membership.go archiveChannel）。静音不受此影响（updateNotifySettings 只需 access hash）。
- **importChatInvite 的 `USER_ALREADY_PARTICIPANT`**：视为"已加入"成功而非错误（文案提示直接发消息链接）。
- **加入后归档不影响 accessHash 收割**：`walkDialogs` 本来就遍历 folder 0（主列表）与 folder 1（归档夹），`folders.editPeerFolders` 移入归档不会让频道"消失"。
- **静音用 `account.updateNotifySettings` + MuteUntil 远期时间戳**：等效客户端"永久静音"；`InputPeerNotifySettings` 的 MuteUntil 是条件字段，用 `SetMuteUntil` 助手设置（直接结构体赋值不带 Flags 无效）。
- **creator 频道无法退出**：`channels.leaveChannel` 对创建者返回 `USER_CREATOR`，列表行须禁用退出操作。
- **join_requests.invite_hash 是审批延时执行的必需数据**：审批时才能执行加入；但它是敏感值——Web 下发与审计一律 `tmeurl.MaskInviteHash` 脱敏，不入日志。
