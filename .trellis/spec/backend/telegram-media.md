# 媒体传输：下载三分支与上传双通道

> 来源：`.trellis/tasks/archive/2026-08/09-01-media-reference-fallback` + `09-02-bot-mtproto-reference-send`（引用直发，已移除）+ `.trellis/tasks/09-03-media-pipeline-concurrency`（管道化内存流式与并发分片）。适用于 `internal/media`、`internal/mtproto`、`internal/delivery`、`internal/queue`、`internal/config` 中媒体传输相关变更。

## Scenario: 媒体统一"下载 → 上传"（三分支下载 + 双通道上传）

### 1. Scope / Trigger

- 触发：改动 `media.Open`/`media.Options`、`reorderBuffer`、`mtproto.BotClient.SendMedia`/`SendAlbum`、`delivery.routerSender` 路由语义、相册打开编排、`internal/queue/split.go` 分卷拆分编排或媒体传输相关 env 时，必须遵守本契约。
- 媒体发送统一走"下载 → 上传"（引用直发已移除）：worker 打开下载句柄后交 `delivery.routerSender` 按大小路由——≤ uploadCap（默认 50MB，本地 Bot API 服务器时为 MaxFileSize）走 Bot API multipart 流式上传；超过走 Bot 身份 MTProto 会话直传（上限 2000MB）。
- 相册整组双通道：全员 ≤ uploadCap 且 Bot API 可整组（photo ≤ photoLimit / video 任意）且**整组总量 ≤ albumCap**（`BotAPIAlbumCap()`：官方服务器 50MB——sendMediaGroup 把整组字节装进同一 multipart 请求体，请求体容量独立于逐成员上限起效，成员各自合规的相册总量约 70MB 即被纯文本 413 拒绝，真机 2026-10-08；本地服务器 MaxInt64 不做总量分流，拆分相册既行走 sendMediaGroup）→ `sendMediaGroup`；否则（含超限成员——必为 video/document 且 ≤ MaxFileSize，经 `AlbumGroupable` 预检，或总量超 albumCap）→ 同一 Bot 会话**两阶段**整组直传：逐成员 uploader 上传（**串行**——uploader 内部已并发分片，再按成员并发放大 invoke 会触发 FLOOD_WAIT）→ `messages.uploadMedia` 注册到目标 peer → 汇总坐标引用 → `messages.sendMultiMedia` 一次整组（MTProto 通道承载 photo/video/document 全形态，总量分流组内的小成员随组直传）；photo 超 photoLimit、document/audio 成员或超 MaxFileSize → 不可整组，逐条降级（Telegram 不允许 document 与 photo/video 混组；全 document 组合法，仅分卷拆分路径产生，见下条）。worker 预检（AlbumGroupable）只看单成员，总量超限的组仍属"可整组"，通道选择由 SendAlbum 按 albumCap 在发送时决策——同源不变量不变：预检通过的组，SendAlbum 必有通道。
- 相册 caption 与"恰好组首一条"不变量：`AlbumEntry.Caption` 逐成员构造语义 caption（worker 从 `Item.MediaCaption()` 填充；拆分成员的首段始终携带该成员自己的正文与切段说明，整组组首成员另带原链/脚注——归一化前每条语义 caption 都要在场，正文才不会丢），但**客户端对相册的首渲染在多个成员携带 caption 时抑制组级展示位（相册下方空白）**——真机五组实验（2026-09-20）：恰好组首一条（正常相册、单视频切段、修复前的缓存频道副本）正常展示；2+ 条（混合相册图片+分段各带 caption、把署名写到组末分段后的缓存频道副本——copyMessages 忠实继承源 caption，反向验证）一律空白；删除任一成员触发重渲染后展示恢复。因此 `routerSender.SendAlbum` **发送前统一归一化**（`normalizeAlbumCaptions` + `message.MergeCaptions`）：全部成员语义 caption 按源顺序合并进组首（`\n\n` 连接、实体按 UTF-16 前缀平移、频道脚注保留组首一份），其余成员清零，返回浅拷贝不修改调用方条目——Bot API（含本地服务器模式下的拆分相册）与 MTProto 两条通道从首次请求起就只收到规范形态。**红线：不做发送后 caption 修补**——`EditMessageCaption(..., "")` 受依赖库 `omitempty` 影响空串可能不进 multipart（清空不可靠），且补写完成前存在错误展示窗口；历史方案（ef754aa/63d641c 逐成员重写、7ad71d1 首末两步双写）均因未减少 caption 数量而失败，勿回退。合并不截断：`RenderHTML`/`Limited` 仍是唯一预算执行点（1024 UTF-16 units）。归一化发生时记 Info 观测（chat_id + 成员数 + 非空 caption 数，不含消息内容）。**混合相册拆分整组的 delivery_mode**：`sendAlbumGroup` 在任一成员经切段展开（plans[i].split）时补记 `track.split`（与单媒体拆分路径同语义归并 split；此前该路径漏标被归并为 upload，管理端"分段投递"口径失真）。
- 分卷拆分投递（split，`internal/queue/split.go`）：Size 超过 MaxFileSize 但 ≤ MaxSplitTotalSize（运行时为 10 × `Config.SplitSegmentSize`；默认 `SplitSegmentSize=1800MiB`，≈17.6GB）的媒体不再 `FILE_TOO_LARGE`——`MAX_FILE_SIZE` 与 `SPLIT_SEGMENT_SIZE` 均可通过环境变量下调，用于本地小媒体测试（例如 10MiB/5MiB），正式默认值不变；强制走临时文件路径（`media.Open` 预检放行 + 禁入流式/内存分支），完整落盘（`Handle.WaitDownloaded`）后按 `SplitSegmentSize`（1800MB：流复制关键帧偏移 + 4000 分片双 margin）切为 N 段，经相册整组直传为同一条消息。两种形态：**可播放视频分段**（视频+有时长+ffmpeg 可用）——`ffmpeg -ss <t> -t <dur> -c copy -avoid_negative_ts make_zero -f matroska` 流复制（不转码、无损；muxer 显式指定）切出真实视频文件（段名 .mkv）；镜像精简 ffmpeg 须含 matroska muxer（Dockerfile 已启用并构建期自检；启动期 `media.CheckMatroskaMuxer` 探测缺失即告警——精简/阉割构建会以 'Requested output format is not known' 失败，真机 2026-09-20），以 video 形态上传（挂 DocumentAttributeVideo 可内联播放），切点按字节占比换算时间、实际对齐关键帧（±GOP 偏差），段实际大小超 MaxMediaFileSize 早失败 FILE_TOO_LARGE；**字节分段 document（兜底）**——非视频/缺时长/ffmpeg 不可用/切段失败，纯字节切割普通 document（不挂 video 属性——假播放器点了必败），文件名 `<原名>.part<i>of<N>`，caption/脚注/合并提示只挂首段（`Caption.WithNote` 尾部追加纯文本不平移实体），第 2 段起按『段起始字节/总大小 × 总时长』换算时间戳对完整文件 `ffmpeg -ss` 抽帧（`media.ExtractFrameAtJPEG`）。混合相册经 `planAlbumSend` 三态预检：全员可整组 → 常规；不可整组成员全部可拆且展开后 ≤ AlbumMaxItems → 拆分整组（图片与分段同一条相册，任务级原子）；否则逐条降级（单媒体路径各自拆分）。切段阶段失败（`errors.Is(err, errSegmentCut)`，区别于下载失败）→ 整组回退逐条 + 单媒体回退字节分段——切段在任何字节发出之前，回退仍原子；回退前显式 cleanupAll 释放旧句柄避免磁盘双份占用。拆分放弃『边下边传』交叠（流复制与 `Handle.OpenSection` 区间读取依赖完整落盘），磁盘峰值 ≈ 2× 文件大小（切段前 `media.CheckTempDir` 再预检一份）；成功按整组记 1 次送达并标 `delivery_mode=split`（失败回落 upload，错误码记录原因）。缓存频道副本按 `sentSpans`（源条目 → 消息 ID 组，`sentIDs` 记录、SendAlbum 返回 ID 与 entries 同序按成员计数切分）整批复制；相册只重写组首为 worker 传入的 canonical clean caption（`cleanAlbumCaptionHTML`：从实际展开 entries 以 `MergeCaptions` 同语义合并、剥离频道脚注，仅任务内存传递不落库）——不从源 items 按 span 重建多条 caption（会把多 caption 形态带回缓存并经复用扩散）；items 与消息数不一致的旧校验会跳过拆分副本，勿回退。
- 下载三分支（`media.Open`，按 Size）：≤ StreamLimit 纯 `io.Pipe` 流式（O(1) 内存）；StreamLimit–InMemoryLimit 走 `reorderBuffer` 内存管道（多线程并行下载乱序落位 + 顺序阻塞读，下载上传完全重叠，内存占用 = 文件大小）；> InMemoryLimit 走临时文件多线程并行落盘 + `fileGate` 就绪水位门控（下载落盘与上传读盘重叠，内存安全兜底）；可拆分媒体（> MaxFileSize 且 ≤ MaxSplitTotalSize）无视上述分派强制落盘分支。
- 下载分片统一 `downloadPartSize = 1MB`（upload.getFile 协议上限，4KB 对齐，`downloader.WithPartSize`；gotd 默认 512KB 调大后请求次数减半）；`reorderBuffer` 槽位与 `fileGate` 水位槽与分片同宽对齐。
- 并发分片：下载 `downloader.Builder.WithThreads(DOWNLOAD_THREADS)`（`Stream()` 无法并行——gotd 源码注释明确，流式小文件路径固定单流）；上传 `uploader.Uploader.WithThreads(UPLOAD_THREADS)`（>10MB bigLoop 单读 goroutine + N 并发发送 goroutine，消费任意顺序 `io.Reader`；≤10MB smallLoop 固定串行带 MD5，不受线程数影响）。
- **MTProto 连接池 + 粘性 DC 重定向（不可拆分）**：两个会话的 api 经物理上限16的 `client.Pool()` + 动态文件 RPC gate + `redirectInvoker` 装配；Web 设置的 `*_CONNECTIONS` 是有效并发上限，默认4，1–16，调高/调低即时影响新 RPC，不取消在途调用，底层 pool 惰性开连。N 个并发分片线程不再复用单条 TCP 连接（单连接吞吐典型15–30MB/s，并行 TCP 窗口 + 每连接独立 AES-IGE 加解密）。**跨 DC 红线**：裸 pool 不执行 gotd 主连接 `invokeDirect` 的 `*_MIGRATE`；`redirectInvoker` 按 Location/FileID 建立文件粘性路由，首次 `FILE_MIGRATE_X` 后后续同文件分片直接进入目标 DC 池，目标再次迁移时有限 hop 更新，普通 RPC 不受影响。`STATS_MIGRATE` 只做一次性重定向。**叠加顺序红线**：floodwait 必须在动态 gate/redirectInvoker 最外层（`tg.NewClient(waiter.Handle(gate))`），FLOOD_WAIT 睡眠不占 gate permit；主/子池在 `client.Run` 回调内创建并与 api 同生命周期关闭。Bot 会话 `CompressThreshold: -1` 关闭出站 gzip（gotd 对 >1KB 出站 payload 无条件压缩、压不小也照发；512KB 上传分片是不可压缩媒体，每 GB 白耗10–20s CPU）；用户会话保持默认（fetch RPC 小、下载响应 gzip 由服务端决定）。
- 相册成员下载句柄**并发打开**：`sendAlbumGroup` 内 errgroup + 信号量，并发上限 `albumOpenConcurrency = 2`（与下载线程解耦——按成员数×线程数放大 invoke 并发会显著提高 FLOOD_WAIT 概率）；任一失败整组失败，句柄按索引落位、defer 统一清理。

### 2. Signatures

```go
// internal/media/handle.go：三分支下载入口（Handle 契约不变）
type Handle struct {
    Reader  io.Reader // 单次消费：io.Pipe 读端 / reorderBuffer / fileGate
    Cleanup func()    // 消费完成或放弃都必须调用：关闭数据源（唤醒读者+
                      // 终止下载）、取消下载 ctx、关句柄删临时文件
}
type Options struct {
    TmpDir          string
    MaxFileSize     int64
    StreamLimit     int64
    MemoryLimit     int64 // 内存管道上限；<=0 不启用（Size 超过 StreamLimit 即落盘）
    DownloadThreads int   // <1 按 1 处理（gotd WithThreads 同语义）
    MaxDirSize      int64
    DirUsage        func(dir string) (int64, error) // 测试注入
    MaxSplitTotalSize int64 // 拆分单条消息总上限；<=0 禁用拆分（超 MaxFileSize 即拒绝）
    SplitSegmentSize  int64 // 拆分单段大小（拆分编排队列读取）
}
func Open(ctx context.Context, api *tg.Client, m message.Media, jobID string, opt Options, log *slog.Logger) (*Handle, error)

// internal/media/gate.go：临时文件路径"边下边发"核心组件（数据在磁盘）
// 同一结构体实现 io.WriterAt（gotd Parallel 乱序落盘）与 io.Reader（顺序
// 阻塞读就绪前缀，pread 与并发 pwrite 安全）；文件写入成功后才在锁内记账。
func newFileGate(f *os.File, size int64, on func(int64)) *fileGate
func (g *fileGate) WriteAt(p []byte, off int64) (int, error) // closed 后返回 errConsumerClosed
func (g *fileGate) Read(p []byte) (int, error)               // 未就绪 cond.Wait；成功关闭读尽 EOF
func (g *fileGate) CloseWithError(err error)                 // err=nil 成功；短读升级 errShortDownload

// internal/media/buffer.go：重排序流式缓冲（内存管道核心组件）
// 同时实现 io.WriterAt（gotd Parallel 乱序写）与 io.Reader（顺序阻塞读）。
func newReorderBuffer(size int64) *reorderBuffer
func (b *reorderBuffer) WriteAt(p []byte, off int64) (int, error) // closed 后返回 errConsumerClosed
func (b *reorderBuffer) Read(p []byte) (int, error)               // 未就绪 cond.Wait；成功关闭读尽 EOF
func (b *reorderBuffer) CloseWithError(err error)                 // err=nil 成功；短读升级 errShortDownload

// internal/delivery/router.go：大小路由（uploadCap = config.BotAPIUploadCap()，
// largeCap = config.MaxFileSize，albumCap = config.BotAPIAlbumCap()；相册含
// 超限成员或总量超 albumCap 时经 largeCap 分流整组直传）
func NewRouter(botAPI Sender, large LargeFileSender, uploadCap, largeCap, albumCap int64, log *slog.Logger) Sender

// internal/mtproto/botsend.go：大文件直传（实现 delivery.LargeFileSender）
func (c *BotClient) SendMedia(ctx, chatID, m message.Media, caption message.Caption, r io.Reader) error
    // uploader.NewUploader(api).WithThreads(c.cfg.UploadThreads).Upload(NewUpload(名, r, m.Size))
func (c *BotClient) SendAlbum(ctx, chatID, medias []message.Media, readers []io.Reader, captions []message.Caption) error
    // medias/readers/captions 按位对应（router 从 AlbumEntry 拆出，接口签名
    // 不引入 delivery 类型）；两阶段：每成员 uploader.Upload →
    // messages.uploadMedia（photo → InputMediaUploadedPhoto、video →
    // uploadedMediaOf）→ mediaReference 提取坐标（Photo/Document.AsInput →
    // InputMediaPhoto/InputMediaDocument）→ messages.sendMultiMedia 一次整组；
    // caption.Limited() 逐成员挂 InputSingleMedia.Message/Entities；每成员
    // 独立 RandomID；PhotoEmpty/DocumentEmpty/nil/未知类型在 mediaReference
    // 内防御报错，不 panic
```

### 3. Contracts

- **Handle 生命周期**：`Cleanup` 必须调用（无论成败）；三条路径的 Cleanup 都是"放弃信号"——唤醒阻塞读者、让仍在下载的 goroutine 尽快终止（临时文件路径同时取消下载 ctx），而不是等任务超时；成功收尾后重复调用为幂等 no-op。
- **就绪语义（reorderBuffer / fileGate 同构）**：按 slot（与 downloadPartSize 同宽 1MB）累计字节数判满（不假设写入对齐/不重叠——Telegram 重取会重复写，就绪判定用 ≥）；就绪前缀 watermark 推进时 Broadcast 唤醒读者，上传因此可"边下边发"。fileGate 的计数在文件写入成功之后才增加，读者读到的字节必然已落盘。
- **三条路径错误传播同构**：下载错误（含临时文件路径）一律经 Reader 在发送消费阶段以 `MEDIA_DOWNLOAD_FAILED` 传出——临时文件路径自异步化后不再在 `media.Open` 同步失败；`FILE_REFERENCE_EXPIRED` 经 Unwrap 链（外层 AppError.Cause 为原始 tgerr）由 `sendMediaItem`/`openWithRefresh` 判定并刷新重试一次。
- **短读红线**：下载"成功结束"但落位字节 < 声明 Size 时升级为错误——截断数据不得当完整文件上传。
- **Size 元数据被信任为精确值**：`NewUpload(name, r, Size)`（computeParts）与 reorderBuffer 预分配都依赖它；photo 的 Size 取最大缩略图。
- **依赖方向**：`delivery` 定义 `LargeFileSender` 接口（签名仅用 message 类型），`mtproto.BotClient` 隐式实现，`cmd/bot` 装配——两个包互不 import。
- **配置校验位置**：`IN_MEMORY_LIMIT` 的区间校验（stream ≤ memory ≤ max）在 `config.Load` 内——**不动 `ValidateMediaLimits` 签名**（它被 Web 运行时覆盖链 `web/media_runtime.go` 复用，新键暂不进运行时设置）。
- **进程级内存预算闸门**：`media.Options.Memory`（`MemoryBudget` 接口，nil = 不限制）在内存路径判定时按文件大小记账（`TryAcquire` 非阻塞），预算不足**自动降级临时文件路径**（不排队、不失败）；预算经 Handle.Cleanup 以 `sync.Once` 恰好归还一次。额度来自 env `MEMORY_BUDGET`（默认 1GB，64MB–8GB）+ settings 键 `memory_budget`（Web 可改，闸门闭包每次 TryAcquire 锁外实时读库，即时生效）；相册成员经同一 Open 天然被约束，`worker.go` 不感知。降级路径磁盘占用仍由 `TEMP_DIR_MAX_SIZE` 约束。
- **相册错误语义**（并发打开前后一致）：任一成员打开失败整组失败（不回退逐条）；`FILE_REFERENCE_EXPIRED` 刷新重试封装在 `openWithRefresh` 内，天然可并发。
- **相册下载 ctx 生命周期**：流式/内存管道/临时文件成员的下载在句柄打开后仍在后台进行，其 ctx **必须独立于 errgroup**——errgroup 的 ctx 在 `Wait` 返回（含全员成功）时即被取消；沿用 gctx 会让整组打开完成的一刻后台下载全部死亡，上传读源以 `MEDIA_DOWNLOAD_FAILED(context canceled)` 失败（真机 2026-09-03）。`sendAlbumGroup` 用 `openCtx` 覆盖整组发送全程；任一成员失败时显式 `cancelOpen()` 停止其余在途下载，函数返回时兜底取消。
- **相册判定同源**：`routerSender.AlbumGroupable`（Bot API 承载 ∪ video ≤ largeCap 的 MTProto 整组承载）与 `routerSender.SendAlbum` 内部分流必须同一套判定——worker 预检通过的组，SendAlbum 必有通道可走；`SendAlbum` 收到双通道都承载不了的成员按 `CodeInternal` 防御（调用方违约）。
- **运行时回退开关**：`DOWNLOAD_THREADS=1 UPLOAD_THREADS=1` + `IN_MEMORY_LIMIT=STREAM_LIMIT` 即回到单线程两阶段行为；`DOWNLOAD_CONNECTIONS=1 UPLOAD_CONNECTIONS=1` 即回到单连接行为，均无需回滚代码。

### 4. Validation & Error Matrix

| 条件 | 行为 |
| --- | --- |
| `Size > MaxFileSize` 且不可拆分（未配置拆分上限或 > MaxSplitTotalSize） | `FILE_TOO_LARGE`，不发起下载；可拆分 → 强制临时文件路径，不再拒绝 |
| `Location == nil` | `CodeMediaUnsupported` 防御错误（nil 接口编码进请求会 panic） |
| 流式/内存管道/临时文件下载失败 | 错误经 `CloseWithError` 传出，读端以 `MEDIA_DOWNLOAD_FAILED` 返回 |
| 下载"成功"但短读（任一非流式路径） | `errShortDownload`（防御：Size 元数据不准） |
| 消费方放弃（发送失败/整组失败） | Cleanup 关闭数据源，下载 goroutine 立即终止 |
| 临时目录超限（`used + size > MaxDirSize`） | `TEMP_DIR_FULL` 拒绝（仅落盘分支预检；查询失败 fail-open） |
| 上传 FLOOD_WAIT | 上传侧 bigLoop 自带 FloodWait 循环；仍限流经 SimpleWaiter 后按 `TELEGRAM_RATE_LIMIT` 失败 |
| Bot 会话未就绪 + Size > uploadCap | `LARGE_CHANNEL_UNAVAILABLE` 确定性失败（零网络，小文件不受影响） |
| 相册含超限成员或总量超 albumCap + Bot 会话未就绪 | `LARGE_CHANNEL_UNAVAILABLE` 确定性失败（任务失败，用户重试；不降级逐条——大成员单发同样依赖该通道） |
| 相册成员超 largeCap / 非 photo-video 类型 | 预检即不可整组 → 逐条降级（worker 路径）；绕过预检直达 SendAlbum → `CodeInternal` 防御 |

### 5. Good/Base/Bad Cases

- Good：500MB 视频 → 内存管道路径：4 线程并行下载乱序落位，MTProto 上传 4 线程并发发送、顺序读缓冲——端到端 ≈ max(下载, 上传)/4，零磁盘写入。
- Base：2GB 视频（> InMemoryLimit）→ 临时文件多线程落盘 + fileGate 就绪水位门控，上传侧顺序读就绪前缀——下载落盘与上传读盘重叠，总耗时 ≈ max(下载, 上传)；相册多成员 → 2 路并发打开。
- Bad：跳过 `Cleanup`（下载 goroutine 挂到任务超时）；把 reorderBuffer 当可重放 reader 重试上传（单次消费契约）；并发相册打开不用固定索引写槽位；日志打印 file_reference/access hash 全文。

### 6. Tests Required

- `internal/media/buffer_test.go`：乱序写→有序读、部分就绪先读、CloseWithError 唤醒阻塞读者/终止写入、短读报错、并发压测（`-race`）。
- `internal/media/gate_test.go`：与 buffer_test.go 同构的乱序/部分就绪/关闭传播/短读/重复写幂等/进度回调/并发压测（`-race`）。
- `internal/media/handle_test.go`：三分支路由（`chunkInvoker` 假 invoker 返回真实字节，注意**越界偏移返回空切片**——并行 worker 会在 EOF 信号前乐观领到越界 offset，真实服务器语义如此）；落盘分支 Cleanup 删除文件；`TEMP_DIR_FULL` 预检。
- `internal/queue/worker_upload_test.go`：相册并发打开行为等价（整组/降级/成员下载失败整组失败）与逐成员 caption 传播；临时文件路径异步化后，下载失败经 reader 在发送阶段传播——需观察该语义的用例用 `consumeMediaReaders`/`consumeAlbumReaders` 让假 Sender 模拟真实上传侧消费。
- `internal/delivery/router_test.go`：相册双通道分流（全员 ≤uploadCap 且总量 ≤albumCap → Bot API；含超限成员或总量超 albumCap → MTProto 整组；通道不可用 → `LARGE_CHANNEL_UNAVAILABLE`；超 largeCap → 防御错误；本地服务器形态 albumCap=MaxInt64 不做总量分流）与 `AlbumGroupable` 新语义；**发送前**"恰好组首一条 caption"归一化（两条通道从首次请求即收到组首合并 caption、其余清零；调用方条目不被修改；实体 UTF-16 偏移平移；单 caption 原样透传；无任何发送后 caption 编辑）。
- `internal/queue/split_playable_test.go`：混合相册 [图片, 超限视频] 拆分整组端到端（需 ffmpeg）——成员形态 [photo, 段1, 段2]、图片组首携带正文+来源+脚注、后置视频成员正文与切段说明保留在其首段（归一化前语义 caption 在场，不静默丢弃）、delivery_mode=split。
- `internal/mtproto/botsend_test.go`：相册两阶段全路径（假 invoker 断言 uploadMedia 先于 sendMultiMedia、最终媒体只含 `InputMediaPhoto/InputMediaDocument` 坐标引用且 ID 来自注册返回值、caption 逐成员绑定、注册失败不发起整组）与 `mediaReference` 防御（PhotoEmpty/DocumentEmpty/nil/未知类型不 panic）。
- `internal/config/config_test.go`：线程与内存上限的默认值、区间校验。
- 质量门（quality-guidelines）：media/queue/mtproto/delivery 变更必须 `go test ./... -race`。

### 7. Wrong vs Correct

#### Wrong

```go
// 内存管道路径忘记 Cleanup：下载 goroutine 阻塞在 WriteAt/读者挂死到任务超时
h, _ := media.Open(ctx, api, m, key, opt, log)
send(ctx, h.Reader) // 无 defer h.Cleanup()

// 假 invoker 对越界 offset 返回 error：并行下载会因 worker 乐观超发而误报失败
if req.Offset >= len(payload) { return errors.New("out of range") }
```

#### Correct

```go
h, err := media.Open(ctx, api, m, key, opt, log)
if err != nil { return err }
defer func() { if h.Cleanup != nil { h.Cleanup() } }()

// 相册并发打开：固定索引落位 + 信号量限流 + errgroup 首错取消
g, gctx := errgroup.WithContext(ctx)
sem := make(chan struct{}, albumOpenConcurrency)
for i, it := range items {
    g.Go(func() error {
        select {
        case sem <- struct{}{}:
        case <-gctx.Done():
            return gctx.Err()
        }
        defer func() { <-sem }()
        h, err := openWithRefresh(gctx, d, j, *it.Media, it.ID)
        if err != nil { return err }
        handles[i] = h
        return nil
    })
}
```

## Scenario: 视频封面（缩略图）随重发携带

### 1. Scope / Trigger

- 触发：改动 `message.Media.Thumb/ThumbJPEG`、`queue.prepareVideoThumb`、`mtproto.uploadThumb`、`delivery` 视频发送参数或 `FFMPEG_PATH` 时，必须遵守本契约。
- 背景：MTProto 服务器**不**为上传的 document 自动生成缩略图（Bot API 服务器会，但对部分容器/编码失败）——直传路径不带 Thumb 的视频在客户端无封面。重发视频统一"源缩略图优先 → ffmpeg 抽帧兜底 → 无封面降级"三级解析（`internal/queue/thumb.go`）。
- 提取（`message.documentThumb`）：只认 `tg.Document.Thumbs` 中真实字节档（PhotoSize/PhotoSizeProgressive，取最大），跳过 PhotoStrippedSize（需专用解码）与 PhotoPathSize（Lottie 路径预览）；坐标为带 `ThumbSize` 的 `InputDocumentFileLocation`，仅 Kind==Video 提取。
- 解析时机：worker 打开主媒体句柄后就地解析（`openAndSend`/`sendAlbumGroup` 逐成员），结果写入 `Media.ThumbJPEG`（≤200KB 级内存字节）随 Media 值拷贝进 Sender。
- ffmpeg 兜底（`media.ExtractFrameJPEG`）：从主下载流头部顺序读 8MB（覆盖容器头+首帧），写临时文件喂 ffmpeg（MP4 demuxer 解析 moov 需要 seekable 输入，管道会失败），首帧缩放到 320×320 内输出 mjpeg。moov 在尾部的视频无法从头解析 → 失败降级。
- 上传（MTProto `uploadThumb`）：缩略图先于主文件上传（≤200KB、fail-fast）；上传失败**降级不失败**（外观增强不值得让大文件上传前功尽弃），仅 ctx 已取消时外传。
- Bot API 相册路径（sendMediaGroup）**无法带缩略图**：go-telegram/bot 的表单构造器只为主媒体生成附件，`InputMediaVideo.Thumbnail` 引用的 attach:// 名字没有对应 form part——依赖服务器自动生成（≤50MB 基本可用），带确定封面的整组走 MTProto SendAlbum。

### 2. Contracts

- **降级红线**：缩略图任何一步失败（源下载失败/ffmpeg 缺失/抽帧失败）都不得让任务失败——只记日志（下载失败 Warn、其余 Debug），照常无封面投递。
- **数据流完整性红线**：ffmpeg 兜底从主数据流消费的头部字节必须经 `io.MultiReader` 接回上传源（reader 单次消费契约）；主下载流自身的读取错误（含 FILE_REFERENCE_EXPIRED 经 reader 传播）必须原样外传——`sendMediaItem` 的刷新重试依赖它，刷新后带着新坐标重新解析缩略图。
- 源缩略图独立下载（`downloadThumb`）：独立 30s 超时窗口、不占进度计数（进度总量按主媒体声明）、复用 `media.Open`（天然获得 DC 粘性路由）；不自己做 file reference 刷新——主媒体的刷新重试会带着新坐标重新走到这里。
- `Media.Thumb` 是提取期坐标（转换时填充，可随 RefreshMedia 重新产生）；`Media.ThumbJPEG` 是发送期解析结果（worker 填充，不落库、不参与复用判断）。

### 3. Tests Required

- `internal/message/convert_test.go`：最大档选择（ThumbSize/Size）、stripped/path 跳过、无缩略图 nil、非视频不提取。
- `internal/queue/thumb_test.go`：源缩略图下载（`thumbAwareInvoker` 按 ThumbSize 分流）且数据源原样透传、抽帧成功且头部接回（内容逐字节一致）、抽帧失败/ffmpeg 缺失/FFmpegPath 空降级、下载流损坏错误外传、非视频透传；端到端 `TestWorkerCarriesSourceThumb`（单媒体 + 相册成员）。
- `internal/mtproto/botsend_test.go`：`uploadedMediaOf` Thumb 挂载与缺省（标志位）、SendMedia 全路径携带缩略图、`uploadThumb` 降级（上传失败 (nil,nil)、ctx 取消外传）。
- `internal/delivery/telegram_params_test.go`：sendVideo multipart 携带 `thumbnail` 附件（含字节与文件名）、无缩略图不带该字段。

## Gotchas

> **Warning**: gotd `downloader.Stream()` 无法并行（源码注释明确）——并行必须走 `Parallel(ctx, io.WriterAt)`；`ToPath` 内部即 Parallel（threads 默认 1），加 `.WithThreads(n)` 才并行。

> **Warning**: gotd 上传器 bigLoop 的读侧是**单 goroutine 顺序读**（`syncio.NewReader`），并发的是发送侧——所以管道/缓冲这类顺序数据源可直接喂并行上传，不需要 ReaderAt。

> **Warning**: `reorderBuffer.Read` 在"读尽但未关闭"时会等待关闭（bigLoop 读到 EOF 才收尾）——下载 goroutine 在 `Parallel` 返回后必须调用 `CloseWithError`，否则读者永久阻塞。

> **Warning**: 相册并发打开的句柄切片用 `make([]*media.Handle, len(items))` 预分配 + nil 守卫清理——失败中断时未打开的槽位是 nil，直接 `h.Cleanup()` 会 panic。

> **Warning**: go-telegram/bot 的 multipart 是真流式（`io.Pipe` 直连 chunked body），`InputFileUpload.Data` 为 `io.Copy`——上传侧天然支持任意大小顺序 reader，不要为"算 Content-Length"先缓冲整个文件。

> **Warning**: `MessagesSendMediaRequest.Entities` 是 flags 条件字段——必须经 `SetEntities` 设置；`RandomID` 每次发送必须非零（防重放）。`InputSingleMedia.Entities` 同理。`InputMediaUploadedDocument.Thumb` 同为 flags 条件字段——用 `SetThumb` 设置，nil 时不得置位。

> **Warning**: MTProto 服务器不为上传的 document 生成缩略图（官方客户端自行抽帧上传）；Bot API 服务器对 MP4/H.264 通常可自动生成，MKV/WebM/HEVC 等常失败——"有的视频没封面"即这两条路径都不带缩略图所致，见上方视频封面契约。

> **Warning**: `messages.sendMultiMedia` 的 `MultiMedia` **只接受已注册的媒体引用**（`InputMediaPhoto`/`InputMediaDocument`），直接携带 raw `inputMediaUploaded*` 构造器会被 400 `MEDIA_INVALID` 拒绝（真机结论 2026-09-03，与 gotd 注释及官方 TL 文档一致）。正确流程是逐成员 `uploader.Upload` → `messages.uploadMedia`（目标 peer）→ `Photo/Document.AsInput()` 坐标 → `sendMultiMedia`。注意 `messages.sendMedia`（单媒体）不受此约束，raw 构造器可用（391MB 单视频真机验证）。

> **Warning**: bot 特权 `UsersGetUsers(InputUser{UserID, AccessHash:0})` 仅 Bot 身份可用；access hash 缓存跨重连保留，不要打进日志。

> **Warning**: `IN_MEMORY_LIMIT` 是**单文件**常驻 RAM 上限；进程总量由 `MEMORY_BUDGET`（settings 键 `memory_budget`，即时生效）封顶——预算不足的内存路径媒体自动降级临时文件路径，不再随 WORKER_COUNT / 相册成员数线性放大。磁盘代价由 `TEMP_DIR_MAX_SIZE` 兜底。
