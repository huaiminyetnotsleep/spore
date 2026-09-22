package queue

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"golang.org/x/sync/errgroup"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/dumpcache"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/progress"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
	"github.com/huaiminyetnotsleep/spore/internal/transfercfg"
)

const processTimeout = 15 * time.Minute

// maxProcessTimeout 是单个任务的硬上限：发送阶段超时按媒体总量自适应
// （见 jobTimeout），但不超过该值。
const maxProcessTimeout = 2 * time.Hour

// jobTimeout 返回发送阶段的超时窗口：15 分钟基础 + 每 1MB 媒体加 1 秒
// （约 1MB/s 端到端吞吐下限）——2GB 级文件的下载+上传需要远超固定窗口。
func jobTimeout(totalBytes int64) time.Duration {
	t := processTimeout + time.Duration(totalBytes/(1<<20))*time.Second
	if t > maxProcessTimeout {
		return maxProcessTimeout
	}
	return t
}

// Fetcher 是 worker 对 MTProto 取数与媒体 file reference 刷新能力的最小依赖；
// *mtproto.Fetcher 天然满足。以接口注入使 requests 行的阶段埋点
// 可以用假实现做确定性单测。
type Fetcher interface {
	Fetch(ctx context.Context, ref tmeurl.SourceRef) ([]*tg.Message, error)
	RefreshMedia(ctx context.Context, ref tmeurl.SourceRef, messageID int) (message.Media, bool, error)
	API() *tg.Client
}

// EventSink 是 worker 对事件中心的最小回调集（internal/notify.Hub 天然满足）。
// 以接口注入保持 queue 不依赖 notify，装配层接线即可；nil 表示无事件回调。
type EventSink interface {
	// TaskResult 上报任务结果：连续失败达到阈值时由事件中心产生告警，成功清零。
	// detail 为最近一次失败任务的可读上下文（随告警展示；成功时忽略）。
	TaskResult(ctx context.Context, succeeded bool, detail string)
	// CloudResult 上报云盘任务（/download）结果：独立连续失败计数
	//（cloud.upload_failed 事件源），成功清零；仅云盘任务调用。detail 语义同 TaskResult。
	CloudResult(ctx context.Context, succeeded bool, detail string)
	// StoreWriteFailed 上报任务终态落库失败（数据库错误的代表性位置）。
	// scene 为写入场景的可读标签（随告警展示）。
	StoreWriteFailed(ctx context.Context, scene string)
	// CheckTempDir 在任务开始前提供低频抽样点：事件中心据此检查临时目录占用。
	CheckTempDir(ctx context.Context)
}

// PinTarget 描述单个置顶目标（绑定频道/群组）的置顶结果。
type PinTarget struct {
	Label  string // 目标显示名（标题优先，回退 @用户名 / 数字 ID）
	Pinned bool   // 置顶是否成功（副本发送失败与置顶失败均为 false）
}

// PinOutcome 汇总一轮置顶结果：OK 为 Targets 中 Pinned 的计数，Total 为
// 参与置顶的目标总数（含副本发送失败的）。Skipped 是因路由不匹配被跳过
// 的绑定显示名（绑定属于其他受理 bot，不计入 Total，仅提示用）。
// 非置顶调用返回零值。
type PinOutcome struct {
	OK      int
	Total   int
	Targets []PinTarget
	Skipped []string
}

// ChannelCopier 把已成功发送给用户的消息复制到该用户绑定的频道（频道副本）。
// 由装配层提供实现（internal/binding.Service）；nil 表示未配置频道绑定，
// worker 跳过全部副本投递。实现必须尽力而为：内部失败只记日志，
// 不向调用方传播错误，更不得影响任务结果。
// requestID 非 0 时实现须顺手把副本组首坐标落库 sent_messages（供 /pin
// 事后补置顶定位）。pin 为 true 时实现须对每个副本发送成功的目标执行
// 静音置顶（组首消息），并返回逐目标置顶结果（Label 供确认文案展示）；
// pin 为 false 时返回零值。
type ChannelCopier interface {
	CopyToChannels(ctx context.Context, requestID, botID, userID, userChatID int64, msgIDs []int, pin bool) PinOutcome
}

// ChannelLinksProvider 提供该用户绑定频道的脚注跳转链接（消息末尾的
// "📢 频道"样式化链接，随内容发送给用户并随副本进入频道）。
// 由装配层提供实现（internal/binding.Service）；nil 表示不织入脚注。
type ChannelLinksProvider interface {
	PublicChannelLinks(ctx context.Context, userID int64) ([]message.ChannelLink, error)
}

// CloudSink 是 worker 对网盘上传通道的最小依赖（方法签名与
// cloudarchive.Sink 一致，*cloudarchive.RcloneSink 天然满足）。以接口注入
// 保持 queue 可用假实现做确定性单测；nil 时云盘任务直接失败（防御装配错误）。
type CloudSink interface {
	// Upload 上传单个文件到目的地；ctx 取消时终止子进程并清理远端残件。
	Upload(ctx context.Context, dest cloudarchive.Destination, spec cloudarchive.UploadSpec) error
	// Ping 轻量连通性测试（worker 不使用，保留与 cloudarchive.Sink 同构）。
	Ping(ctx context.Context, dest cloudarchive.Destination) error
	// VerifyUploaded 核验历史远端路径是否仍全部存在（同链接重复提交的跳过
	// 判定）：(true,nil) 全部存在；(false,nil) 有路径确定不存在（走重传）；
	// err 非 nil 表示核验本身失败，存在性未知。
	VerifyUploaded(ctx context.Context, dest cloudarchive.Destination, paths []string) (bool, error)
}

// CloudCfg 提供云盘任务运行时的目的地解析（*cloudarchive.Manager 天然
// 满足）。只做目的地级查找，全局开关的准入把关在提交入口（botapi/access）。
type CloudCfg interface {
	// ResolveCloudDestination 按名称返回已启用的目的地；不存在或未启用 false。
	ResolveCloudDestination(name string) (cloudarchive.Destination, bool)
}

// Deps 聚合 worker 处理任务所需的依赖。
type Deps struct {
	Fetcher Fetcher
	Sender  delivery.Sender
	// SenderFor 按任务的受理 bot 解析发送通道（多机器人池）：状态提示编辑、
	// 媒体投递、频道副本都走受理 bot 对应的私聊。nil 或解析为 nil（bot 已
	// 下线/存量任务 BotID=0）时回退 Sender。
	SenderFor func(j Job) delivery.Sender
	Media     media.Options
	Transfer  *transfercfg.Runtime // 任务开始时读取一次，线程数保持任务内一致
	Store     *store.Store         // 业务数据库：requests 行阶段埋点；nil 或 Job.RequestID==0 时跳过
	Events    EventSink            // 事件中心回调；nil 时跳过全部事件上报
	// Progress 是处理中请求的实时传输进度注册表（internal/progress），
	// 与 Web 管理端共享同一实例；nil 时跳过全部进度上报。
	Progress *progress.Registry
	// Copier 是频道副本投递通道（该用户绑定的频道）；nil 时跳过。
	Copier ChannelCopier
	// ChannelCopyEnabled 返回频道副本同步总开关（运行设置，main 注入实时
	// 读库闭包）：返回 false 时跳过副本投递（绑定关系保留）；nil 视为开启。
	ChannelCopyEnabled func() bool
	// ReuseEnabled 返回 TG 链接复用总开关（运行设置，main 注入实时读库
	// 闭包）：返回 false 时跳过 tryReuseFromDump，行为与复用引入前一致；
	// nil 视为开启。
	ReuseEnabled func() bool
	// Dump 是缓存频道读写通道（转存频道复用）：nil（未配置 DUMP_CHANNEL_ID）
	// 时复用整体关闭，投递照常。
	Dump *dumpcache.Service
	// Channels 提供频道脚注链接；nil 时消息不带脚注。
	Channels ChannelLinksProvider
	// CloudSink 是云盘上传通道（/download 任务）；nil 时云盘任务直接失败。
	CloudSink CloudSink
	// CloudCfg 提供云盘任务运行时的目的地解析；nil 时云盘任务直接失败。
	CloudCfg CloudCfg
	Log      *slog.Logger
}

// copyWindow 是任务成功后频道副本投递的独立时间窗：使用剥离取消信号的
// ctx，任务收尾（含进程退出）时副本投递不因 ctx 已死而失效。
const copyWindow = 2 * time.Minute

// senderFor 解析任务应使用的发送通道：受理 bot 优先，未命中回退 Sender。
func (d Deps) senderFor(j Job) delivery.Sender {
	if d.SenderFor != nil {
		if snd := d.SenderFor(j); snd != nil {
			return snd
		}
	}
	return d.Sender
}

// Process 组装 worker 的任务处理逻辑：取源消息 → 标准化 → 重发。
// 文本直接发送；媒体统一"下载 → 上传"（路由层按大小选择 Bot API 或
// Bot 号 MTProto 大文件直传）；相册整组发送，不可行时降级逐条。
// 任务取出与结束时同步更新 requests 行（的阶段埋点）。
func Process(d Deps) Processor {
	return func(ctx context.Context, j Job) {
		// 任务内超时由 runJob 分阶段管理：取数固定 15 分钟窗口，
		// 发送按媒体总量自适应（jobTimeout）——2GB 级文件不能按固定窗口砍断。
		d.Log.Info("任务开始", "job_id", j.ID, "user_id", j.UserID, "ref", j.Ref.String())
		if !markStarted(ctx, d, j) {
			// 排队任务可能已被取消（管理员或用户本人），claim 失败时不得调用
			// Fetch、下载或发送；占位消息改为取消文案而非删除。
			markStatusCancelled(d, ctx, j)
			return
		}
		if d.Events != nil {
			d.Events.CheckTempDir(ctx) // 内部按时间节流，不逐任务遍历目录
		}
		// 实时传输进度：claim 成功后注册，任务收尾（含取消与 panic 不得残留）
		// 由 defer 清除；无持久化记录的任务（RequestID==0）没有进度可展示。
		if j.RequestID != 0 {
			d.Progress.Register(j.RequestID)
			defer d.Progress.Done(j.RequestID)
		}
		// 重试入队的任务没有占位提示（原占位已随上次失败消失，或遗留为冻结
		// 的旧进度）：认领成功后按任务形态补发一条，让重试执行在 Bot 里恢复
		// 实时进度展示，终态删除/取消文案也随之落在新占位上。发送失败只记
		// 日志不阻断任务——本轮无进度展示，与补发逻辑引入前的行为一致。
		if j.NeedsStatusPrompt && j.StatusMsgID == 0 {
			if id, serr := d.senderFor(j).SendMessage(ctx, j.ChatID, statusPromptHTML(j)); serr != nil {
				d.Log.Warn("重试任务补发占位提示失败，本轮无进度展示",
					"job_id", j.ID, "request_id", j.RequestID, "error", serr.Error())
			} else {
				j.StatusMsgID = id
				d.recordAnchors(ctx, j, store.SentKindStatus, []int{id})
				d.Log.Info("重试任务已补发占位提示",
					"job_id", j.ID, "request_id", j.RequestID, "status_msg_id", id)
			}
		}
		// 占位消息实时进度编辑：与进度注册同步启停（无占位时为 no-op），
		// stop 等待编辑 goroutine 退出，避免收尾删除占位后仍有一次在途编辑
		stopProgressEditor := startProgressEditor(ctx, d, j)
		defer stopProgressEditor()

		jobDeps := d
		if d.Transfer != nil {
			snap := d.Transfer.Snapshot()
			jobDeps.Media.DownloadThreads = snap.DownloadThreads
		}
		// 任务分流：云盘任务把媒体上传到网盘而不重发回 Telegram；仅缓存
		// 补写任务直接向缓存频道写干净副本而不投递用户。多条路径共享上面
		// 的开始埋点、进度注册与编辑器、取消标记与超时治理。
		var cloudPaths []string
		var cloudSkipped bool
		var meta mediaMeta
		var err error
		switch {
		case j.DumpOnly:
			meta, err = runDumpJob(ctx, jobDeps, j)
		case j.CloudDest != "":
			meta, cloudPaths, cloudSkipped, err = runCloudJob(ctx, jobDeps, j)
		default:
			meta, err = runJob(ctx, jobDeps, j)
		}
		// 云盘请求无论成败都保持 cloud 投递标记（失败行不回落 upload 占位，
		// 列表筛选"网盘"语义完整；失败早于转换时元数据为零值）；仅缓存
		// 补写任务同款语义保持 dump 标记（列表筛选"缓存补写"口径完整）。
		deliveryMode := meta.deliveryMode()
		if j.CloudDest != "" {
			deliveryMode = store.DeliveryModeCloud
		}
		if j.DumpOnly {
			deliveryMode = store.DeliveryModeDump
		}
		if err != nil {
			if IsRequestCancelled(ctx) {
				d.Log.Info("任务已取消", "job_id", j.ID, "request_id", j.RequestID)
				// 先停进度编辑再写取消文案：stop 等待编辑 goroutine 退出，
				// 避免在途的进度编辑覆盖终态提示（stop 幂等，defer 中的
				// 二次调用为空操作）。
				stopProgressEditor()
				markStatusCancelled(d, ctx, j)
				return
			}
			ae := apperr.From(err)
			if ctx.Err() != nil {
				// 进程退出（父 ctx 取消）导致的失败统一归 INTERRUPTED，
				// 避免把关停信号记成 INTERNAL_ERROR；该类请求可在 Web 重试
				ae = apperr.New(apperr.CodeInterrupted, "进程退出中断任务")
			}
			d.Log.Error("任务失败", "job_id", j.ID, "ref", j.Ref.String(), "code", ae.Code, "error", err.Error())
			if finishErr := finishRequest(d, ctx, j, store.RequestResult{
				Status:           store.RequestFailed,
				ErrorCode:        string(ae.Code),
				MediaType:        meta.MediaType,
				MediaTypes:       meta.MediaTypes,
				FileSize:         meta.FileSize,
				FileName:         meta.FileName,
				SourceMediaDCIDs: meta.MediaDCIDs,
				DeliveryMode:     deliveryMode,
			}); isExpectedStateRace(finishErr) {
				// 管理员取消或其他终态抢先写入时，自然失败不得覆盖请求记录，
				// 也不得再向用户发送失败提示或累计失败事件。
				deleteStatusBestEffort(d, ctx, j)
				return
			}
			// 进程正在退出（ctx 已取消）：在途任务的收尾由 drain 路径的
			// WithoutCancel 窗口接管，这里不再用死 ctx 白打两次 Bot API
			if ctx.Err() == nil {
				if j.DumpOnly {
					// 仅缓存补写任务不打扰用户：失败只落库（Web 列表可见
					// 原因与错误码），不发错误提示
					delivery.TryDeleteStatus(ctx, d.senderFor(j), d.Log, j.ChatID, j.StatusMsgID)
				} else {
					if id, sendErr := d.senderFor(j).SendMessage(ctx, j.ChatID, failureNoticeHTML(ae.Code, j.Ref)); sendErr != nil {
						d.Log.Warn("错误提示发送失败", "job_id", j.ID, "error", sendErr.Error())
					} else {
						d.recordAnchors(ctx, j, store.SentKindFailure, []int{id})
					}
					delivery.TryDeleteStatus(ctx, d.senderFor(j), d.Log, j.ChatID, j.StatusMsgID)
				}
			}
		} else {
			if finishErr := finishRequest(d, ctx, j, store.RequestResult{
				Status:           store.RequestSucceeded,
				MediaType:        meta.MediaType,
				MediaTypes:       meta.MediaTypes,
				FileSize:         meta.FileSize,
				FileName:         meta.FileName,
				SourceMediaDCIDs: meta.MediaDCIDs,
				DeliveryMode:     deliveryMode,
			}); isExpectedStateRace(finishErr) {
				deleteStatusBestEffort(d, ctx, j)
				return
			}
			delivery.TryDeleteStatus(ctx, d.senderFor(j), d.Log, j.ChatID, j.StatusMsgID)
			switch {
			case j.DumpOnly:
				// 副本已由任务直接发送进缓存频道（发送即写入）：无用户消息
				// 产生，缓存重写、频道副本与脚注、确认文案均不适用
			case j.CloudDest != "":
				// 云盘任务：发确认文本（含目的地、远端路径、原链接与网盘官网；
				// 跳过重复上传时附说明）；无 TG 媒体消息产生，频道副本与脚注
				// 天然不适用
				sendCloudConfirm(ctx, d, j, cloudPaths, cloudSkipped)
			default:
				// 引用回复锚点：投递到用户私聊的消息坐标（相册/分卷逐条、
				// 任意一条被回复都能反查回本请求）
				d.recordAnchors(ctx, j, store.SentKindMedia, meta.SentIDs)
				// 缓存频道干净副本：同链接后续提交直接复制的来源（尽力而为）
				d.writeCleanDump(ctx, j, meta)
				// 频道副本：任务整体成功后，把刚发给用户的消息复制到该用户
				// 绑定的频道（尽力而为，失败只记日志不影响任务结果）。
				// pin 任务置顶副本组首并回写结果：标记读自请求行（/pin <链接>
				// 或用户 auto_pin 偏好），重试与进程重启后依然继承。
				pin := d.requestPin(ctx, j)
				d.copyToChannels(ctx, j, meta.SentIDs, pin)
			}
		}
		d.Log.Info("任务结束", "job_id", j.ID)
		// 事件上报放在收尾最后：成功清零连续失败计数，失败累计并按阈值告警；
		// 云盘任务另经 CloudResult 进入独立计数（cloud.upload_failed 事件源）。
		// 失败时附带来源链接人可读形式，随告警通知展示最近失败上下文。
		if d.Events != nil {
			detail := ""
			if err != nil {
				detail = j.Ref.String()
			}
			d.Events.TaskResult(ctx, err == nil, detail)
			if j.CloudDest != "" {
				d.Events.CloudResult(ctx, err == nil, detail)
			}
		}
	}
}

// recordAnchors 把本任务 bot 已发出消息的坐标落库（sent_messages，引用
// 回复交互锚点）：进度占位 / 投递媒体 / 失败通知（频道副本坐标由 binding
// 在 CopyToChannels 内落库）。尽力而为，失败只记日志——锚点缺行最坏影响
// 是 /pin、/cancel 回复该消息时反查不到（回引导文案），不得反噬任务主流程。
// RequestID==0（无持久化记录的任务）或 Store 未接时跳过。
func (d Deps) recordAnchors(ctx context.Context, j Job, kind string, ids []int) {
	if d.Store == nil || j.RequestID == 0 || len(ids) == 0 {
		return
	}
	rows := make([]store.SentMessage, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		rows = append(rows, store.SentMessage{
			RequestID: j.RequestID,
			BotID:     j.BotID,
			ChatID:    j.ChatID,
			MessageID: int64(id),
			Kind:      kind,
		})
	}
	if err := d.Store.InsertSentMessages(ctx, rows); err != nil {
		d.Log.Warn("消息坐标落库失败",
			"job_id", j.ID, "request_id", j.RequestID, "kind", kind, "error", err.Error())
	}
}

// copyToChannels 在任务成功后把已发送消息复制到该用户绑定的频道。
// 使用剥离取消信号的 ctx 与独立时间窗：任务收尾（含进程退出）时副本投递
// 仍可完成；Copier 自身尽力而为，失败不向任务结果传播。
// pin 为 true 时绕过频道同步开关（显式置顶意图优先于全局设置）并回写置顶
// 结果、给用户发简短确认；无媒体消息（纯文本任务）时整体跳过。
func (d Deps) copyToChannels(ctx context.Context, j Job, msgIDs []int, pin bool) {
	if d.Copier == nil || len(msgIDs) == 0 {
		return
	}
	if !pin && d.ChannelCopyEnabled != nil && !d.ChannelCopyEnabled() {
		d.Log.Info("频道同步开关已关闭，跳过副本投递", "job_id", j.ID, "user_id", j.UserID)
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), copyWindow)
	defer cancel()
	outcome := d.Copier.CopyToChannels(cctx, j.RequestID, j.BotID, j.UserID, j.ChatID, msgIDs, pin)
	if pin {
		d.finishPin(cctx, j, outcome)
	}
}

// requestPin 读取任务的置顶标记（requests.pin，/pin <链接> 或用户 auto_pin
// 偏好写入）。无持久化记录或读取失败视为不置顶，与引入本标记前的行为一致。
func (d Deps) requestPin(ctx context.Context, j Job) bool {
	if d.Store == nil || j.RequestID == 0 {
		return false
	}
	r, err := d.Store.GetRequest(ctx, j.RequestID)
	if err != nil {
		d.Log.Warn("读取置顶标记失败，本任务不置顶", "request_id", j.RequestID, "error", err.Error())
		return false
	}
	return r.Pin
}

// finishPin 是置顶任务的收尾：回写置顶结果（管理端详情展示）并给用户发
// 确认文案——带原消息链接与逐目标置顶明细，让用户知道哪条消息被置顶、
// 置顶到了哪个频道/群组。均尽力而为：失败只记日志，不影响任务结果。
// total 为 0（完成时无绑定）时提示绑定前提；失败目标单独列出并引导检查
// 权限（原因可能是权限被撤、bot 被移出目标等，无法细分）。
func (d Deps) finishPin(ctx context.Context, j Job, outcome PinOutcome) {
	if d.Store != nil && j.RequestID != 0 {
		if err := d.Store.SetRequestPinResult(ctx, j.RequestID, outcome.OK, outcome.Total); err != nil {
			d.Log.Warn("置顶结果回写失败", "request_id", j.RequestID, "error", err.Error())
		}
	}
	sourceURL, _ := j.Ref.URL()
	if _, err := d.senderFor(j).SendMessage(ctx, j.ChatID, pinResultText(sourceURL, outcome)); err != nil {
		d.Log.Warn("置顶确认发送失败", "job_id", j.ID, "error", err.Error())
	}
}

// pinResultText 渲染置顶结果确认文案（HTML：原消息链接与目标名逐个列出，
// 频道/群组名可能含 HTML 特殊字符，一律转义；与 failureNoticeHTML 同风格）。
// Skipped（绑定属于其他受理 bot）单独成行提示，不计入失败。
func pinResultText(sourceURL string, o PinOutcome) string {
	link := "原消息链接不可用"
	if sourceURL != "" {
		link = fmt.Sprintf(`<a href="%s">%s</a>`, sourceURL, sourceURL)
	}
	if o.Total == 0 && len(o.Skipped) == 0 {
		return "任务已完成。您尚未绑定频道/群组，未执行置顶；先 /bind 绑定后对新任务生效。\n原消息：" + link
	}
	var pinned, failed []string
	for _, t := range o.Targets {
		label := html.EscapeString(t.Label)
		if t.Pinned {
			pinned = append(pinned, label)
		} else {
			failed = append(failed, label)
		}
	}
	skipped := make([]string, 0, len(o.Skipped))
	for _, s := range o.Skipped {
		skipped = append(skipped, html.EscapeString(s))
	}
	var b strings.Builder
	if len(pinned) > 0 {
		fmt.Fprintf(&b, "📌 已置顶原消息 %s 到：%s", link, strings.Join(pinned, "、"))
	}
	if len(failed) > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		if len(pinned) == 0 {
			fmt.Fprintf(&b, "📌 原消息 %s 置顶失败：%s（请检查机器人的发帖与置顶权限）", link, strings.Join(failed, "、"))
		} else {
			fmt.Fprintf(&b, "置顶失败：%s（请检查机器人的置顶权限）", strings.Join(failed, "、"))
		}
	}
	if len(skipped) > 0 {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		if o.Total == 0 {
			fmt.Fprintf(&b, "任务已完成，未执行置顶：本任务的受理机器人名下暂无绑定，%s 属于其他机器人（用对应机器人发链接即可投递）",
				strings.Join(skipped, "、"))
		} else {
			fmt.Fprintf(&b, "另有 %d 个绑定属于其他机器人，本次未投递：%s", len(skipped), strings.Join(skipped, "、"))
		}
	}
	return b.String()
}

// writeCleanDump 在任务成功后向缓存频道写无脚注干净副本（dumpcache）。
// 使用剥离取消信号的 ctx 与独立时间窗：任务收尾（含进程退出）时副本写入
// 仍可完成。复用命中的任务不重写（副本即复制来源）；复用开关关闭时同样
// 跳过（副本只服务复用）。尽力而为，失败不影响任务结果。
func (d Deps) writeCleanDump(ctx context.Context, j Job, meta mediaMeta) {
	if d.Dump == nil || meta.Reused || len(meta.SentIDs) == 0 || len(meta.Items) == 0 {
		return
	}
	if d.ReuseEnabled != nil && !d.ReuseEnabled() {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), copyWindow)
	defer cancel()
	sourceURL, _ := j.Ref.URL()
	d.Dump.WriteClean(cctx, j.BotID, j.ChatID, refChannelKey(j.Ref), j.Ref.MessageID,
		meta.Items, meta.SentSpans, sourceURL, meta.CleanAlbumCaptionHTML)
}

// channelLinks 读取该用户绑定频道的脚注链接（尽力而为：失败降级为空，
// 不阻塞任务）；Provider 为 nil 或任务无归属用户时返回 nil。
func (d Deps) channelLinks(ctx context.Context, j Job) []message.ChannelLink {
	if d.Channels == nil || j.UserID == 0 {
		return nil
	}
	links, err := d.Channels.PublicChannelLinks(ctx, j.UserID)
	if err != nil {
		d.Log.Warn("读取频道脚注链接失败，本任务不带脚注", "user_id", j.UserID, "error", err.Error())
		return nil
	}
	return links
}

// sentIDs 收集已成功发送给用户的消息 ID（按发送顺序），任务成功后由频道
// 副本经 Bot API copyMessages 定位复制；发送中途失败时已收集的部分 ID
// 随任务失败自然作废（副本只对整体成功的任务投递）。
// sentIDs 累计任务已发送的消息 ID：items 是按发送顺序的摊平列表（频道
// 副本整批复制用）；spans 按源条目分组（一条源条目对应的消息 ID 连续成组
// ——分卷拆分会把一个条目展开为多条），供缓存频道干净副本按条目清洗 caption。
type sentIDs struct {
	items []int
	spans [][]int
}

func (s *sentIDs) addSpan(ids []int) {
	span := make([]int, 0, len(ids))
	for _, id := range ids {
		if id != 0 {
			s.items = append(s.items, id)
			span = append(span, id)
		}
	}
	if len(span) > 0 {
		s.spans = append(s.spans, span)
	}
}

func (s *sentIDs) add(id int) {
	s.addSpan([]int{id})
}

// runJob 执行提取与回传，返回转换阶段得到的媒体诊断元数据与错误。
// 失败发生在消息转换之前（取数失败/无内容）时元数据为零值，
// 转换之后的失败（发送/下载）仍能带上媒体类型、大小与文件名。
// 取数之前先尝试从缓存频道复用同链接的干净副本（copyMessages 直拷，见
// reuse.go）：命中则跳过整个 fetch/下载/上传，未命中继续完整链路。
func runJob(ctx context.Context, d Deps, j Job) (mediaMeta, error) {
	if m, sent, ok := tryReuseFromDump(ctx, d, j); ok {
		m.SentIDs = sent
		return m, nil
	}
	// 取数阶段独立基础窗口：链接解析与邻域取数不因后续大文件传输的
	// 自适应超时而失去上限。
	fetchCtx, cancelFetch := context.WithTimeout(ctx, processTimeout)
	msgs, err := d.Fetcher.Fetch(fetchCtx, j.Ref)
	cancelFetch()
	if err != nil {
		return mediaMeta{}, err
	}
	// 频道脚注：与原消息链接同位织入
	return sendConverted(ctx, d, j, j.ChatID, msgs, d.channelLinks(ctx, j))
}

// sendConverted 把取到的源消息转换并发送到 target 聊天，返回媒体诊断元数据
// （SentIDs 是按发送顺序的已发送消息 ID）。普通投递 target 为用户私聊，
// links 织频道脚注；仅缓存补写（dumpjob.go）target 为缓存频道，links 传
// nil——caption 构造对 nil links 天然无脚注，副本与 writeCleanDump 的干净
// 语义一致。转换后无可提取内容/含不支持类型返回明确错误。
// meta 必须是命名返回值：defer 在 return 后回填 SentIDs（失败时保留部分
// 已发送坐标，成功后供频道副本/缓存条目落库续用）。
func sendConverted(ctx context.Context, d Deps, j Job, target int64, msgs []*tg.Message, links []message.ChannelLink) (meta mediaMeta, err error) {
	items := message.Convert(msgs)
	if len(items) == 0 {
		return mediaMeta{}, apperr.New(apperr.CodeServiceMessage, "源消息无可提取内容")
	}
	meta = mediaMetaOf(items)
	meta.Items = items
	sent := &sentIDs{}
	defer func() {
		meta.SentIDs = sent.items
		meta.SentSpans = sent.spans
	}()
	// 进度总量 = 全部媒体大小之和（相册为成员累加）；未注册 ID（无持久化
	// 记录）经 Registry 的 no-op 语义自动跳过
	d.Progress.AddTotal(j.RequestID, meta.FileSize)
	sourceURL, _ := j.Ref.URL()

	sendCtx, cancelSend := context.WithTimeout(ctx, jobTimeout(meta.FileSize))
	defer cancelSend()

	if isAlbum(items) {
		return meta, sendAlbumGroup(sendCtx, d, j, target, items, sourceURL, links, &meta, sent)
	}

	for _, it := range items {
		switch {
		case it.Media == nil:
			// 文本消息（网页预览视为纯文本）
			id, serr := d.senderFor(j).SendMessage(sendCtx, target, it.RenderHTMLWithSource(sourceURL, links))
			if serr != nil {
				return meta, serr
			}
			sent.add(id)
		case it.Media.Kind == message.KindUnsupported:
			return meta, apperr.New(apperr.CodeMediaUnsupported,
				"该消息包含暂不支持的内容类型")
		default:
			if serr := sendMediaItem(sendCtx, d, j, target, it, sourceURL, links, &meta.Track, sent); serr != nil {
				return meta, serr
			}
		}
	}
	return meta, nil
}

// mediaMeta 是任务结束时落库的媒体诊断元数据（requests 表列），
// Track 累计投递路径观测，终态经 deliveryMode 归并为 delivery_mode。
type mediaMeta struct {
	MediaType  string
	MediaTypes []string
	FileSize   int64
	FileName   string
	MediaDCIDs []int
	Track      deliveryTrack
	// Reused 标记本次投递命中链接复用（缓存频道直拷，见 reuse.go），
	// deliveryMode 据此返回 reuse，且收尾不再重写缓存频道副本；Track
	// 观测不参与该路径。
	Reused bool
	// SentIDs 是已成功发送给用户的消息 ID（按发送顺序，摊平），任务成功后经
	// 频道副本复制到该用户绑定的频道；失败任务收集的部分 ID 自然作废。
	// SentSpans 是按源条目分组的同一批 ID（与 Items 按序对应，一条源条目
	// 展开的拆分段连续成组），供缓存频道按条目清洗 caption。
	SentIDs   []int
	SentSpans [][]int
	// Items 是转换后的标准化条目（内存传递，不落库）：成功收尾时供
	// dumpcache.WriteClean 构造无脚注干净副本。复用与云盘路径为零值。
	Items []message.Item
	// CleanAlbumCaptionHTML 是相册投递的 canonical 组首干净 caption（渲染后
	// HTML、无频道脚注；内存传递不落库）：从实际展开的相册条目按源顺序合并
	// 全部成员正文与切段说明（与路由归一化同一份合并语义），writeCleanDump
	// 据此让缓存频道副本与投递保持同一"恰好组首一条"布局。非相册任务为零值
	// （副本仍按条目清洗）。
	CleanAlbumCaptionHTML string
}

// cleanAlbumCaptionHTML 把实际投递的相册条目 caption 合并为缓存频道副本的
// canonical 组首 clean caption：全部成员正文按源顺序折叠（含拆分成员正文与
// 切段说明），剥离用户频道脚注（缓存副本天然无脚注）。条目为空或全部
// caption 为空时返回 ""（调用方回退按条目清洗）。
func cleanAlbumCaptionHTML(entries []delivery.AlbumEntry) string {
	if len(entries) == 0 {
		return ""
	}
	captions := make([]message.Caption, len(entries))
	for i, e := range entries {
		captions[i] = e.Caption
		captions[i].Channels = nil
	}
	return message.MergeCaptions(captions).RenderHTML()
}

// mediaMetaOf 从标准化条目提取落库元数据：
//   - 多成员 Telegram 相册的主类型记 "album"，并保存成员类型去重集合；
//   - 非相册沿用单条媒体类型，caption 不参与类型统计；
//   - 文件大小取组内全部媒体之和，文件名取首个带文件名的媒体条目；
//   - 投递观测标记"已转换"与"含媒体"，路径计数由各发送函数累计。
func mediaMetaOf(items []message.Item) mediaMeta {
	m := mediaMeta{MediaType: mediaTypeLabel(items[0])}
	if isAlbum(items) {
		m.MediaType = "album"
	}
	m.Track.converted = true
	seenTypes := make(map[string]struct{})
	seenDC := make(map[int]struct{})
	for _, it := range items {
		mediaType := mediaTypeLabel(it)
		if _, ok := seenTypes[mediaType]; !ok {
			seenTypes[mediaType] = struct{}{}
			m.MediaTypes = append(m.MediaTypes, mediaType)
		}
		if it.Media != nil {
			m.Track.hasMedia = true
			m.FileSize += it.Media.Size
			if m.FileName == "" {
				m.FileName = it.Media.FileName
			}
			if it.Media.DCID > 0 {
				seenDC[it.Media.DCID] = struct{}{}
			}
		}
	}
	for dc := range seenDC {
		m.MediaDCIDs = append(m.MediaDCIDs, dc)
	}
	slices.Sort(m.MediaDCIDs)
	return m
}

// mediaTypeLabel 把标准化条目映射为 requests.media_type 的稳定取值，
// 供筛选与统计分组使用。
func mediaTypeLabel(it message.Item) string {
	if it.Media == nil {
		return "text"
	}
	switch it.Media.Kind {
	case message.KindPhoto:
		return "photo"
	case message.KindVideo:
		return "video"
	case message.KindDocument:
		return "document"
	case message.KindAudio:
		return "audio"
	case message.KindVoice:
		return "voice"
	default:
		return "unsupported"
	}
}

// markStarted 落库"worker 取出任务"埋点（processing + started_at）。
// 写失败只记日志不阻断执行：终态仍会经 finishRequest 落库，行不会失真。
func markStarted(ctx context.Context, d Deps, j Job) bool {
	if IsRequestCancelled(ctx) {
		return false
	}
	if d.Store == nil || j.RequestID == 0 {
		return true
	}
	if err := d.Store.MarkRequestStarted(ctx, j.RequestID, 0); err != nil {
		if IsRequestCancelled(ctx) || isExpectedStateRace(err) {
			// 取消可能恰好发生在初始 cause 检查之后；不要把取消触发的
			// context 错误误报为存储故障事件，也不要开始执行任务。
			return false
		}
		d.Log.Warn("记录任务开始失败", "job_id", j.ID, "request_id", j.RequestID, "error", err.Error())
		if d.Events != nil {
			d.Events.StoreWriteFailed(ctx, "任务开始标记落库")
		}
	}
	return true
}

// finishRequest 落库任务终态。任务可能因进程退出（ctx 取消）而结束，
// 故统一用剥离取消信号的 ctx 执行本地写，保证记录不因关停丢失；
// 写入限时 10 秒（与退出 drain 窗口同宽），避免磁盘级故障挂死 worker。
func finishRequest(d Deps, ctx context.Context, j Job, r store.RequestResult) error {
	if d.Store == nil || j.RequestID == 0 {
		return nil
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWindow)
	defer cancel()
	err := d.Store.FinishRequest(wctx, j.RequestID, r)
	if err != nil {
		if isExpectedStateRace(err) {
			return err
		}
		d.Log.Error("落库任务终态失败", "job_id", j.ID, "request_id", j.RequestID,
			"status", r.Status, "error", err.Error())
		// 数据库写入失败的代表性事件源：终态丢失意味着记录失真，需要管理员关注
		if d.Events != nil {
			d.Events.StoreWriteFailed(wctx, "任务终态落库")
		}
	}
	return err
}

func isExpectedStateRace(err error) bool {
	if err == nil || errors.Is(err, store.ErrNotFound) {
		return err != nil
	}
	return apperr.From(err).Code == apperr.CodeStoreConstraint
}

// deleteStatusBestEffort 在独立时间窗内尽力删除占位提示：使用剥离取消信号的
// ctx，任务收尾（含取消与进程退出）时 Bot API 调用不因 ctx 已死而失效。
func deleteStatusBestEffort(d Deps, ctx context.Context, j Job) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWindow)
	defer cancel()
	delivery.TryDeleteStatus(wctx, d.senderFor(j), d.Log, j.ChatID, j.StatusMsgID)
}

// CancelledStatusHTML 渲染取消终态的占位消息：取消文案 + 来源消息链接，
// 链接以删除线样式标记"该链接的任务已取消"状态。URL 由服务端受控生成
// （频道键为用户名或 -100 数字 ID），无 HTML 注入面；链接不可重建时
// （如私有频道键数据异常）退化为纯取消文案。
func CancelledStatusHTML(ref tmeurl.SourceRef) string {
	if url, ok := ref.URL(); ok {
		return fmt.Sprintf("%s\n<s><a href=\"%s\">%s</a></s>", StatusCancelledHTML, url, url)
	}
	return StatusCancelledHTML
}

// failureNoticeHTML 渲染任务失败的用户提示：错误码文案 + 来源消息链接，
// 多条链接并发处理时用户可据此区分是哪条任务失败。URL 由服务端受控生成
// （频道键为用户名或 -100 数字 ID），无 HTML 注入面；链接不可重建时
// （如私有频道键数据异常）退化为纯错误文案。
func failureNoticeHTML(code apperr.Code, ref tmeurl.SourceRef) string {
	text := apperr.UserText(code)
	if url, ok := ref.URL(); ok {
		return fmt.Sprintf("%s\n<a href=\"%s\">%s</a>", text, url, url)
	}
	return text
}

// markStatusCancelled 把占位消息编辑为取消终态文案（尽力而为，独立时间窗）。
// 取消不删除占位消息，让用户在聊天里看到明确结果。排队任务的即时编辑由装配
// 层的 PendingCancelHandler 触发，与本路径写入同一文案，重复编辑时 Telegram
// 侧的"message is not modified"按无操作处理，天然幂等。
func markStatusCancelled(d Deps, ctx context.Context, j Job) {
	if j.StatusMsgID == 0 {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownWindow)
	defer cancel()
	if err := d.senderFor(j).EditMessageText(wctx, j.ChatID, j.StatusMsgID, CancelledStatusHTML(j.Ref)); err != nil {
		d.Log.Debug("占位消息取消文案编辑失败", "job_id", j.ID, "request_id", j.RequestID, "error", err.Error())
	}
}

// isAlbum 判断该组消息是否为相册（同 GroupedID 的媒体组）。
func isAlbum(items []message.Item) bool {
	return len(items) > 1 && items[0].IsAlbumMember()
}

// Discard 返回进程退出时清理遗留任务的处理器：已取消的遗留任务把占位提示
// 改为取消文案；其余告知用户任务未执行并删除状态提示，同时把 requests 行
// 置为 failed(INTERRUPTED)，不静默丢失，
// 用户可经 Web 重试；启动恢复 FailInterruptedRequests 是硬退出场景的兜底，二者幂等）。
func Discard(d Deps) Processor {
	return func(ctx context.Context, j Job) {
		if d.Store != nil && j.RequestID != 0 {
			if r, err := d.Store.GetRequest(ctx, j.RequestID); err == nil && r.Status == store.RequestCancelled {
				markStatusCancelled(d, ctx, j)
				return
			}
		}
		d.Log.Warn("进程退出，丢弃排队任务", "job_id", j.ID, "user_id", j.UserID)
		if !j.DumpOnly { // 仅缓存补写任务全程不打扰用户（含退出丢弃）
			if _, err := d.senderFor(j).SendMessage(ctx, j.ChatID, "服务正在退出，任务未能完成，请稍后重新发送链接。"); err != nil {
				d.Log.Warn("丢弃通知发送失败", "job_id", j.ID, "error", err.Error())
			}
		}
		delivery.TryDeleteStatus(ctx, d.senderFor(j), d.Log, j.ChatID, j.StatusMsgID)
		discard := store.RequestResult{
			Status:    store.RequestFailed,
			ErrorCode: string(apperr.CodeInterrupted),
		}
		if j.CloudDest != "" {
			// 云盘请求无论成败保持 cloud 投递标记（与 Process 收尾语义一致；
			// 空串会把列回落 upload，丢失"网盘"筛选口径）
			discard.DeliveryMode = store.DeliveryModeCloud
		}
		finishRequest(d, ctx, j, discard)
		if d.Events != nil {
			// 排队任务在退出 drain 中同样是失败结果，应纳入连续失败统计；
			// finishRequest 已先完成请求终态收尾，避免事件看到未完成记录。
			d.Events.TaskResult(ctx, false, j.Ref.String())
			if j.CloudDest != "" {
				d.Events.CloudResult(ctx, false, j.Ref.String())
			}
		}
	}
}

// albumOpenConcurrency 限制相册成员并发打开（下载）数：与 DOWNLOAD_THREADS
// 解耦——若按成员数×下载线程放大 invoke 并发，会显著提高 FLOOD_WAIT 概率。
const albumOpenConcurrency = 2

// albumMemberPlan 是相册单成员的整组规划：普通成员占 1 个相册槽位；可拆
// 视频成员 split=true 并展开为 count 个分段槽位。
type albumMemberPlan struct {
	split bool
	count int
}

// planAlbumSend 纯元数据预检相册的整组可行性（不发起下载，与 SendAlbum
// 分流判定同源）：
//   - 全员可整组（Sender.AlbumGroupable，photo/video 且超限成员 ≤2000MB）
//     → 常规整组；
//   - 不可整组成员全部是"可拆视频"（超过 2000MB 但有时长、ffmpeg 可用）且
//     展开后总成员数 ≤ 相册上限 → 拆分整组：大视频成员切段为可播放分段，
//     与其余成员合成同一条相册（如 [图片, 段1, 段2]），任务级原子——任何
//     成员失败整组失败，一个字节都不发出；
//   - 存在"超限视频但无法切段"（缺时长/ffmpeg 不可用）的成员 → 报
//     SPLIT_UNAVAILABLE，整组原子失败——回退逐条会让图片先发出而视频
//     失败，破坏原子性（设计决策 2026-09-20）；
//   - 其余（存在既不可整组也不可拆的非视频成员，或展开超相册上限）→
//     nil，调用方逐条发送（超大成员在单媒体路径内各自拆分，非视频走
//     字节分段）。
func planAlbumSend(d Deps, j Job, items []message.Item) ([]albumMemberPlan, error) {
	plans := make([]albumMemberPlan, len(items))
	total := 0
	for i, it := range items {
		m := *it.Media
		switch {
		case d.senderFor(j).AlbumGroupable(m):
			plans[i] = albumMemberPlan{count: 1}
		case splittableVideo(d.Media, m):
			n, err := splitSegmentCount(m.Size, d.Media.SplitSegmentSize)
			if err != nil {
				return nil, err
			}
			plans[i] = albumMemberPlan{split: true, count: n}
		default:
			if needsSplit(d.Media, m) && m.Kind == message.KindVideo {
				reason := splitVideoSkipReason(d.Media, m)
				return nil, apperr.New(apperr.CodeSplitUnavailable,
					fmt.Sprintf("相册含超大视频且可播放切段不可用：%s", reason))
			}
			return nil, nil
		}
		total += plans[i].count
	}
	if total > message.AlbumMaxItems {
		return nil, nil
	}
	return plans, nil
}

// sendAlbumGroup 相册整组发送到 target 聊天（普通投递为用户私聊，仅缓存
// 补写为缓存频道；links 为 nil 时成员 caption 无脚注）：
// 发送前先按元数据做整组规划（planAlbumSend：常规整组 / 含可拆视频的拆分
// 整组 / 逐条降级），不可整组也不可拆时直接走逐条路径，避免先整组下载再
// 作废的浪费。规划通过后并发打开全部句柄（上限 albumOpenConcurrency，成员
// 下载重叠；file reference 过期时经 RefreshMedia 刷新后重试一次），任一失败
// 即整组失败并取消其余打开，defer 统一清理；可拆视频成员在完整落盘后切段
// 展开为 N 个分段条目；最后整组原子发送——图片与大视频的分段出现在同一条
// 相册里，不存在"图片先发、视频后到"的部分投递。
//
// 下载 ctx（openCtx）独立于 errgroup：errgroup 的 ctx 在 Wait 返回——
// 含全员成功——时即被取消，而流式/内存管道成员的下载在句柄打开后仍在
// 后台进行、待 SendAlbum 消费；沿用 gctx 会让整组打开完成的一刻后台下载
// 全部被取消，上传读源时以 MEDIA_DOWNLOAD_FAILED(context canceled) 失败
// （真机结论 2026-09-03）。openCtx 覆盖整组发送全程；任一成员失败时
// 显式取消以停止其余在途下载（含分钟级 ToPath），函数返回时兜底取消。
// 可拆成员的切段等待（WaitDownloaded）同样挂在 openCtx 下——其余成员失败
// 时下载 ctx 被取消，切段等待随之返回错误，整组原子失败。
func sendAlbumGroup(ctx context.Context, d Deps, j Job, target int64, items []message.Item, sourceURL string, links []message.ChannelLink, meta *mediaMeta, sent *sentIDs) error {
	track := &meta.Track
	plans, planErr := planAlbumSend(d, j, items)
	if planErr != nil {
		// 超限视频无法切段：整组原子失败（零下载零投递，图片不会先发出）
		return planErr
	}
	if plans == nil {
		d.Log.Info("相册含不可整组项，直接逐条发送", "job_id", j.ID, "items", len(items))
		return sendItemsIndividually(ctx, d, j, target, items, sourceURL, links, track, sent)
	}
	// 拆分整组前置校验（下载开始前）：切段依赖 matroska 封装器，缺失直接
	// 报错终止整组——不白下载、不降级，保持任务级原子（零投递）。探测一次
	// 即可（同一 ffmpeg 二进制）。
	for i := range items {
		if plans[i].split {
			if err := media.CheckMatroskaMuxer(ctx, d.Media.FFmpegPath); err != nil {
				return apperr.New(apperr.CodeSplitUnavailable,
					fmt.Sprintf("超大视频可播放切段不可用：%v", err))
			}
			break
		}
	}

	handles := make([]*media.Handle, len(items))
	segCleanups := make([]func(), len(items)) // 分段 reader/文件清理（发送消费完后执行）
	defer func() {                            // 无论成败都清理已打开句柄与分段文件（未打开/未切段的槽位为 nil）
		for i, h := range handles {
			if segCleanups[i] != nil {
				segCleanups[i]()
			}
			if h != nil && h.Cleanup != nil {
				h.Cleanup()
			}
		}
	}()

	memberEntries := make([][]delivery.AlbumEntry, len(items))
	openCtx, cancelOpen := context.WithCancel(ctx)
	defer cancelOpen()
	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, albumOpenConcurrency)
	for i, it := range items {
		g.Go(func() error {
			select { // 信号量限并发；首个失败取消后续排队者
			case sem <- struct{}{}:
			case <-gctx.Done():
				return gctx.Err()
			}
			defer func() { <-sem }()
			h, err := openWithRefresh(openCtx, d, j, *it.Media, it.ID)
			if err != nil {
				cancelOpen() // 首个失败：立即取消其余成员的在途下载
				return err
			}
			handles[i] = h // 槽位固定，无需额外同步
			if plans[i].split {
				// 可拆视频成员：完整落盘 → ffmpeg 切段 → 展开为 N 个分段
				// 条目（不经过 prepareVideoThumb——其 reader 会被切段路径
				// 的区间读取取代，头部字节不能被消费）；该成员自己的正文与
				// 切段说明挂首段（withSource=是整组组首时另带原链/脚注），
				// 整组 caption 由路由层发送前统一归一化合并
				ents, cleanup, err := openVideoSegmentEntries(openCtx, d, j, it, *it.Media, h,
					sourceURL, links, i == 0)
				if err != nil {
					cancelOpen() // 切段/下载失败：整组失败（未发出任何字节，不降级）
					return err
				}
				memberEntries[i] = ents
				segCleanups[i] = cleanup
				return nil
			}
			m := *it.Media
			src, err := prepareVideoThumb(openCtx, d, &m, h.Reader, tempKey(j, it)+"-thumb")
			if err != nil {
				cancelOpen() // 成员数据流损坏：整组失败，停止其余在途下载
				return err
			}
			caption := it.MediaCaption().WithQuotedBody()
			if i == 0 {
				caption = caption.WithSourceLink(sourceURL).WithChannels(links)
			}
			memberEntries[i] = []delivery.AlbumEntry{{
				Media:   m,
				Reader:  uploadReader(d, j, src),
				Caption: caption, // 语义 caption 逐成员携带；发送前由路由层归一化合并到组首
			}}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		// 切段/下载失败：整组失败（未发出任何字节）。切段能力已在下载前
		// 前置校验，此处失败属源数据异常（冷门编码等），不降级不重试。
		return err
	}
	// 展开摊平（按成员顺序，拆分段跟在其源成员位置）
	entries := make([]delivery.AlbumEntry, 0, len(items))
	for _, me := range memberEntries {
		entries = append(entries, me...)
	}
	// canonical clean caption：与路由归一化同一份合并语义（从实际展开的
	// entries 计算，切段说明保留），剥离用户频道脚注后供缓存频道副本一次性
	// 重写组首，使副本与投递同为"恰好组首一条"布局。仅任务内存传递不落库。
	meta.CleanAlbumCaptionHTML = cleanAlbumCaptionHTML(entries)
	// 全员打开成功：openCtx 仍存活，SendAlbum 消费期间后台下载持续推进
	ids, err := d.senderFor(j).SendAlbum(ctx, target, entries)
	if err != nil {
		return err
	}
	// 按成员分组记录 spans：返回 ID 与 entries 同序（sentMessageIDs 按发送
	// 顺序提取），拆分段跟在其源成员位置连续成组；ID 数不符（发送器契约
	// 违约）时整体记一组兜底，不丢坐标也不越界
	if len(ids) == len(entries) {
		off := 0
		for _, me := range memberEntries {
			sent.addSpan(ids[off : off+len(me)])
			off += len(me)
		}
	} else {
		sent.addSpan(ids)
	}
	// 混合相册的拆分整组：任一成员经切段展开时按 split 记观测（与单媒体
	// 拆分路径同语义，delivery_mode 归并为 split；此前该路径漏标被归并为
	// upload，管理端"分段投递"口径失真）
	for i := range plans {
		if plans[i].split {
			track.split = true
			break
		}
	}
	track.delivered() // 整组成功按一次送达计
	return nil
}

// sendItemsIndividually 逐条发送到 target 聊天（各自打开句柄、过期刷新重试）。
// 与 sendConverted 主循环一致地拦截不支持类型，避免无 Location 的媒体进入下载。
func sendItemsIndividually(ctx context.Context, d Deps, j Job, target int64, items []message.Item, sourceURL string, links []message.ChannelLink, track *deliveryTrack, sent *sentIDs) error {
	for i, it := range items {
		if it.Media != nil && it.Media.Kind == message.KindUnsupported {
			return apperr.New(apperr.CodeMediaUnsupported,
				"该消息包含暂不支持的内容类型")
		}
		itemSourceURL := ""
		if i == 0 {
			itemSourceURL = sourceURL
		}
		if err := sendMediaItem(ctx, d, j, target, it, itemSourceURL, links, track, sent); err != nil {
			return err
		}
	}
	return nil
}

// sendMediaItem 发送单个媒体到 target 聊天：打开下载句柄 → 经 Sender 发送
// （路由层按大小选择 Bot API 上传或 Bot 号 MTProto 大文件直传）→ 清理；
// 下载或发送因 file reference 过期失败时，刷新源消息后重试一次（Location
// 等元数据同步更新）。大小上限预检在 media.Open 内执行。
// 投递观测的上传尝试与送达在 openAndSend 内计入。
func sendMediaItem(ctx context.Context, d Deps, j Job, target int64, it message.Item, sourceURL string, links []message.ChannelLink, track *deliveryTrack, sent *sentIDs) error {
	err := openAndSend(ctx, d, j, target, it, sourceURL, links, track, sent)
	if err == nil || !mtproto.IsFileReferenceExpired(err) {
		return err
	}
	fresh, ok := refreshMedia(ctx, d, j, it.ID, err)
	if !ok {
		return err // 以原始错误为准
	}
	it.Media = &fresh
	return openAndSend(ctx, d, j, target, it, sourceURL, links, track, sent)
}

// openAndSend 打开句柄 → 发送到 target → 清理（无论成败）。
// 视频媒体在发送前就地解析缩略图（源缩略图优先、ffmpeg 抽帧兜底，见
// thumb.go）；超过单文件上限的媒体走分卷拆分（见 split.go）；上传路径的
// 尝试与送达在此计入投递观测。
func openAndSend(ctx context.Context, d Deps, j Job, target int64, it message.Item, sourceURL string, links []message.ChannelLink, track *deliveryTrack, sent *sentIDs) error {
	m := *it.Media
	// 前置校验（下载开始前）：超限视频必须具备可播放切段能力，不满足直接
	// 报错——不白下载、不降级字节分段（设计决策 2026-09-20）
	if err := ensurePlayableSplit(ctx, d.Media, m); err != nil {
		return err
	}
	h, err := media.Open(ctx, d.Fetcher.API(), m, tempKey(j, it), d.Media, d.Log, downloadReporter(d, j))
	if err != nil {
		return err
	}
	defer func() {
		if h.Cleanup != nil {
			h.Cleanup()
		}
	}()
	if needsSplit(d.Media, m) {
		return sendSplitDocument(ctx, d, j, target, it, m, h, sourceURL, links, track, sent)
	}
	src, err := prepareVideoThumb(ctx, d, &m, h.Reader, tempKey(j, it)+"-thumb")
	if err != nil {
		return err
	}
	caption := it.MediaCaption().WithQuotedBody().WithSourceLink(sourceURL)
	if sourceURL != "" {
		// 脚注与原消息链接同位：只出现在组首/带来源的条目上
		caption = caption.WithChannels(links)
	}
	id, err := d.senderFor(j).SendMedia(ctx, target, m, caption, uploadReader(d, j, src))
	if err != nil {
		return err
	}
	sent.add(id)
	track.delivered()
	return nil
}

// openWithRefresh 打开媒体句柄，下载因 file reference 过期失败时刷新引用后重试一次。
func openWithRefresh(ctx context.Context, d Deps, j Job, m message.Media, itemID int) (*media.Handle, error) {
	key := fmt.Sprintf("%s-%d", j.ID, itemID)
	h, err := media.Open(ctx, d.Fetcher.API(), m, key, d.Media, d.Log, downloadReporter(d, j))
	if err == nil || !mtproto.IsFileReferenceExpired(err) {
		return h, err
	}
	fresh, ok := refreshMedia(ctx, d, j, itemID, err)
	if !ok {
		return h, err
	}
	return media.Open(ctx, d.Fetcher.API(), fresh, key, d.Media, d.Log, downloadReporter(d, j))
}

// downloadReporter 返回 media.Open 的下载进度回调：把落位字节增量计入
// 该任务的进度注册表（nil Registry 或未注册 ID 时为 no-op）。
func downloadReporter(d Deps, j Job) func(int64) {
	return func(n int64) { d.Progress.AddDownloaded(j.RequestID, n) }
}

// uploadReader 用计数 reader 包装上传数据源：被发送方读走的字节数
// （≈ 已上传字节）计入进度。单媒体与相册成员共用同一语义。
func uploadReader(d Deps, j Job, r io.Reader) io.Reader {
	return progress.CountingReader(r, func(n int64) { d.Progress.AddUploaded(j.RequestID, n) })
}

// refreshMedia 刷新过期的媒体引用，并统一记录刷新成功日志（trigger 为触发刷新
// 的底层错误，其文本进入日志供真机校准 file reference 类关键词；不含凭据）。
func refreshMedia(ctx context.Context, d Deps, j Job, itemID int, trigger error) (message.Media, bool) {
	fresh, ok, err := d.Fetcher.RefreshMedia(ctx, j.Ref, itemID)
	if err != nil || !ok {
		return message.Media{}, false
	}
	logArgs := []any{"job_id", j.ID, "item_id", itemID}
	if trigger != nil {
		logArgs = append(logArgs, "error", trigger.Error())
	}
	d.Log.Info("file reference 已刷新，重试媒体处理", logArgs...)
	return fresh, true
}

// tempKey 生成传入 media.Open 的任务键：单条与相册统一为 <jobID>-<itemID>，
// 使临时文件名形如 <纳秒时间戳>-<itemID>-<文件名>（media.IsTempName 依赖首段纯数字）。
func tempKey(j Job, it message.Item) string {
	return fmt.Sprintf("%s-%d", j.ID, it.ID)
}
