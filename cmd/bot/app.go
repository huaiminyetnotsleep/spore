// 核心链路装配：MTProto 用户账号每次就绪（含重连）回调 onMTProtoReady，
// 在该生命周期内重建 fetcher、多机器人池（逐 token 构建长轮询客户端与发送
// 路由）与队列消费；离线时随回调 ctx 结束而停止，重连后重建。其余长生命
// 周期服务由 main 构造并集中在本结构体跨生命周期复用。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/botapi"
	"github.com/huaiminyetnotsleep/spore/internal/botlist"
	"github.com/huaiminyetnotsleep/spore/internal/botpool"
	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/dumpcache"
	"github.com/huaiminyetnotsleep/spore/internal/joinmgr"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/notify"
	"github.com/huaiminyetnotsleep/spore/internal/progress"
	queuepkg "github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
	"github.com/huaiminyetnotsleep/spore/internal/transfercfg"
	"github.com/huaiminyetnotsleep/spore/internal/web"
)

// app 聚合核心链路的装配依赖：长生命周期服务（数据库/队列/各业务服务）
// 在 main 构造一次，机器人池等 ready 作用域资源在每次回调内重建。
type app struct {
	cfg         config.Config
	log         *slog.Logger
	st          *store.Store
	q           *queuepkg.Queue
	access      *access.Service
	bindings    *binding.Service
	join        *joinmgr.Service
	hub         *notify.Hub
	userClient  *mtproto.Client
	pool        *botpool.Pool                // 多机器人池：ready 生命周期内 Reset 重建
	botClients  map[int64]*mtproto.BotClient // botID → 大文件直传会话（main 构建、跨生命周期复用）
	bots        []botlist.Bot                // 有效 bot 列表（env ∪ bots.json，主 bot 在前）
	runtime     *botRuntime                  // 暂停/恢复运行时控制（settings 持久化 + 即时生效）
	profile     *mtproto.ProfileLookup
	membership  *mtproto.MembershipBridge
	memoryGate  *media.BudgetGate
	transfer    *transfercfg.Runtime
	progressReg *progress.Registry
	cloud       *cloudarchive.Manager
	cloudSink   cloudarchive.Sink
	botIdentity *botIdentityStore
}

// onMTProtoReady 在用户号 MTProto 就绪（含重连）时执行：绑定资料刷新与
// 频道成员管理能力、挂 update 入口与对账兜底，随后逐 token 组装机器人池、
// 发送路由与队列消费并阻塞在长轮询上，直到 ctx 取消（进程退出或会话离线）。
func (a *app) onMTProtoReady(ctx context.Context, api *tg.Client) error {
	a.profile.SetAPI(api)
	defer a.profile.Clear()
	fetcher := mtproto.NewFetcher(api, a.cfg.DataDir, a.log)
	// 频道成员管理能力随本生命周期绑定/解绑（离线时受控不可用）
	a.membership.SetAPI(api, fetcher)
	defer a.membership.Clear()

	// update 实时入口：新见频道（peer 缓存未命中）异步交给 joinmgr 补
	// 静音/归档——被拉入频道秒级归档。绑定目标在 gotd 读循环内同步
	// 执行，只做缓存过滤与起 goroutine；离线窗口漏收的由下方对账兜底。
	a.userClient.Updates().Bind(func(uctx context.Context, channels []tg.Channel) {
		seen := fetcher.HarvestNovelChannels(channels)
		if len(seen) == 0 {
			return
		}
		go a.join.OnChannelsSeen(uctx, seen)
	})
	defer a.userClient.Updates().Unbind()

	// 周期兜底对账：离线窗口被拉入/未收到 update 的外部频道，最迟一个
	// 周期归档（重连不补 update 差异，页面刷新对账只在管理员访问时触发）
	go func() {
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := a.join.ReconcileExternal(ctx); err != nil {
					a.log.Info("外部拉入频道周期对账失败（下轮重试）", "error", err.Error())
				}
			}
		}
	}()

	// 多机器人池：逐 token 构建 Bot 客户端与发送路由。单个 token 失效只产生
	// 事件并跳过，不阻断其余 bot；全部失败时按原单 bot 语义结束本轮
	//（触发 MTProto 状态机的重连/退避路径）。
	members := make([]*botpool.Member, 0, len(a.bots))
	for i, bt := range a.bots {
		m, err := a.buildBot(ctx, api, bt, i == 0)
		if err != nil {
			a.log.Warn("机器人接入失败，跳过该 token", "index", i, "error", err.Error())
			a.hub.Raise(ctx, notify.KeyBotInitFailed, notify.SeverityError,
				notify.BotIDData{BotID: botlist.BotID(bt.Token)})
			continue
		}
		members = append(members, m)
	}
	if len(members) == 0 {
		return fmt.Errorf("初始化 Bot 失败: 没有任何机器人成功接入")
	}
	a.pool.Reset(members)
	defer a.pool.MarkAllOffline()

	// 通知与审批通道按用户路由：UserRouter 内部按"用户最近活跃 bot"解析
	//（botapi handler 每条私聊消息刷新），未命中回退主 bot。事件/加入通知
	// 走未计数原始通道（避免"通知失败→产生事件→再通知"自激）；审批通知
	// 走已计数通道（保留 Bot API 连续失败告警语义）。
	a.hub.SetSender(botpool.UserRouter{Pool: a.pool})
	a.access.SetSender(botpool.UserRouter{Pool: a.pool, Counted: true})
	a.join.SetNotifier(joinmgr.SenderNotifier{Sender: botpool.UserRouter{Pool: a.pool}})
	// 频道绑定校验（/bind 与 Web 绑定对全部 bot 校验发帖权限）与副本投递
	//（按受理 bot 复制）都经池取 Bot API 客户端
	a.bindings.SetBots(a.pool.BotAPIs())

	// 业务发送走路由：未超过 Bot API 上限的媒体走 Bot API 上传，超限媒体经
	// 受理 bot 的 MTProto 会话直传（不经 Bot API 服务器，上限 2000MB）；
	// 相册含超限成员时同样分流转 MTProto 整组直传；文本/删除等其余操作走
	// Bot API。worker 按 Job.BotID 经池解析路由（Deps.SenderFor）。
	// 队列可观测地结束：轮询停止后等 drain（退出通知+状态清理）完成再退出本轮
	queueDone := make(chan struct{})
	deps := a.queueDeps(ctx, fetcher)
	go func() {
		a.q.Run(ctx, a.cfg.WorkerCount, queuepkg.Process(deps), queuepkg.Discard(deps))
		close(queueDone)
	}()

	// 排队任务被取消（/cancel 与 Web 管理端共用）时立即把占位提示改为
	// 取消文案：worker 的 claim 拦截要等出队才触发，队列繁忙时滞留可达
	// 分钟级。与出队路径写入同一文案，重复编辑幂等。
	a.q.SetPendingCancelHandler(a.notifyPendingCancel)

	a.log.Info("核心链路就绪，启动长轮询", "bots", len(members))
	// 每 bot 一条轮询监督循环：支持按 settings 暂停/恢复（即时生效，无需
	// 重启）；全部随生命周期 ctx 结束而返回，收齐后再 drain。
	var wg sync.WaitGroup
	for _, m := range members {
		wg.Add(1)
		go func(m *botpool.Member) {
			defer wg.Done()
			a.runtime.RunPoller(ctx, m)
		}(m)
	}
	wg.Wait()
	<-queueDone
	return nil
}

// buildBot 构建单个 bot 的长轮询客户端、发送路由与身份回填，返回池成员。
// 共享依赖（队列/访问控制/绑定/加入/云盘等）全部跨 bot 复用；Status 闭包
// 的大文件直传就绪位按本 bot 的 MTProto 会话判定。
func (a *app) buildBot(ctx context.Context, api *tg.Client, bt botlist.Bot, primary bool) (*botpool.Member, error) {
	botID := botlist.BotID(bt.Token)
	ref := botapi.NewBotRef(botID, "")
	// OnPollError：轮询（getUpdates）409 冲突识别——token 被 webhook 或另一个
	// 轮询实例占用时，本实例拿不到任何消息。错误会按库内退避反复出现，仅在
	// "非冲突 → 冲突"转换时记一次日志与事件（SetConflict 返回是否变化）。
	onPollError := func(err error) {
		if !botapi.IsConflictError(err) {
			return
		}
		if m := a.pool.MemberByID(botID); m != nil && m.SetConflict(true) {
			a.log.Warn("机器人消息拉取冲突（token 被其他服务占用，收不到新消息）", "bot_id", botID)
			a.hub.Raise(ctx, notify.KeyBotPollConflict, notify.SeverityError,
				notify.BotIDData{BotID: botID})
		}
	}
	// NoteActive：收到该 bot 的 update 即证明轮询已恢复，清除冲突态并解决事件。
	noteActive := func(userID, botID int64) {
		a.pool.NoteActive(userID, botID)
		if m := a.pool.MemberByID(botID); m != nil && m.SetConflict(false) {
			a.log.Info("机器人消息拉取已恢复", "bot_id", botID)
			a.hub.Recover(ctx, notify.KeyBotPollConflict)
		}
	}
	b, err := botapi.New(botapi.Options{
		Cfg:         a.cfg,
		Token:       bt.Token,
		Bot:         ref,
		NoteActive:  noteActive,
		OnPollError: onPollError,
		Log:         a.log,
		Queue:       a.q,
		Access:      a.access,
		Channels:    a.bindings,
		WrapSender: func(snd delivery.Sender) delivery.Sender {
			return notify.NewCountSender(snd, a.hub)
		},
		// 系统名称与单次链接上限均实时读 settings，管理端修改即时生效。
		SystemName: func(ctx context.Context) string { return syscfg.Name(ctx, a.st) },
		MaxLinksPerMessage: func(ctx context.Context) int {
			return web.LoadMaxLinksPerMessage(ctx, a.st, a.cfg.MaxLinksPerMessage)
		},
		Whoami: func(ctx context.Context) (string, error) { return mtproto.Me(ctx, api) },

		// /join：joinmgr 统一处理开关/上限/审核分流；owner 判定经
		// users 表全局唯一 owner（未设置时按普通用户走审核）
		ChannelJoin: a.join,
		// /download：云盘状态与目的地解析（Manager 快照 + rclone 实时探测）
		CloudStatus: cloudDriveStatus{mgr: a.cloud},
		IsOwner: func(ctx context.Context, userID int64) (bool, error) {
			ownerID, err := a.st.OwnerID(ctx)
			if err != nil {
				return false, err
			}
			return ownerID != 0 && ownerID == userID, nil
		},
		Status: botapi.StatusFunc(func(ctx context.Context) (botapi.RuntimeStatus, error) {
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			client := a.botClients[botID]
			status := botapi.RuntimeStatus{
				MTProtoState:   a.userClient.Session().Status().State,
				LargeFileReady: client != nil && client.Available(),
				QueueLen:       a.q.Len(),
				QueueCap:       a.q.Cap(),
				WorkerCount:    a.cfg.WorkerCount,
				StoreReady:     true,
			}
			if _, _, probeErr := a.st.GetSetting(probeCtx, "bot_status_probe"); probeErr != nil {
				status.StoreReady = false
			}
			_, processing, countErr := a.st.CountRequestsByStatus(probeCtx)
			status.Processing = processing
			if countErr != nil {
				status.StoreReady = false
			}
			return status, nil
		}),
		ProfileObserver: a.profile.ObserveUser,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化 Bot 失败: %w", err)
	}
	// 拉取机器人身份（总览/请求归属展示 Name 与 @username）：库的 New 已隐式
	// getMe 但丢弃了结果，这里再取一次回填；失败不致命——token 数字前缀即
	// bot id，身份照常入池（用户名留空），请求归属与路由不受影响。
	conflictAtStart := false
	meCtx, meCancel := context.WithTimeout(ctx, 10*time.Second)
	name, username := "", ""
	if me, meErr := b.GetMe(meCtx); meErr != nil {
		a.log.Warn("获取机器人身份失败（展示将只显示 bot id）", "bot_id", botID, "error", meErr.Error())
	} else {
		name = me.FirstName
		if me.LastName != "" {
			name += " " + me.LastName
		}
		username = me.Username
		ref.Set(me.ID, me.Username)
		botID = me.ID
		a.log.Info("已获取机器人身份", "bot_id", me.ID, "bot_username", me.Username, "primary", primary)
	}
	// webhook 冲突探测：token 已被其他服务以 webhook 占用时，本实例轮询注定
	// 409 拿不到消息，接入时即标记冲突并产生事件（冲突解除后随下一次
	// 轮询成功自动恢复）。
	if hookURL, hookErr := botapi.WebhookURL(meCtx, b); hookErr != nil {
		a.log.Warn("查询 webhook 状态失败（跳过冲突探测，运行期轮询错误仍会识别）",
			"bot_id", botID, "error", hookErr.Error())
	} else if hookURL != "" {
		conflictAtStart = true
		a.log.Warn("机器人 token 已被其他服务以 webhook 方式占用（收不到新消息）", "bot_id", botID)
		a.hub.Raise(ctx, notify.KeyBotPollConflict, notify.SeverityError,
			"有机器人收不到新消息：其 token 已被其他服务以 webhook 方式占用。请让对方服务删除 webhook 后下线该 bot，或在管理端移除该 token 后重启。")
	}
	meCancel()
	menuCtx, menuCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := botapi.RegisterCommands(menuCtx, b); err != nil {
		a.log.Warn("注册 Bot 命令菜单失败，继续启动长轮询", "bot_id", botID, "error", err.Error())
	}
	menuCancel()

	sender := delivery.New(b, delivery.Config{PhotoLimit: a.cfg.PhotoLimit})
	// 业务发送走路由：未超过 Bot API 上限的媒体走 Bot API 上传，超限媒体经
	// 本 bot 的 MTProto 会话直传（上限 2000MB）。已计数包装供 worker 与审批
	// 通知使用；原始 sender 供事件/加入通知使用（通知失败不计业务失败）。
	counted := notify.NewCountSender(
		delivery.NewRouter(sender, a.botClients[botID], a.cfg.BotAPIUploadCap(), a.cfg.MaxFileSize),
		a.hub)
	member := &botpool.Member{
		ID:        botID,
		Username:  username,
		Name:      name,
		Token:     bt.Token,
		BotAPI:    b,
		Sender:    counted,
		RawSender: sender,
		BotClient: a.botClients[botID],
	}
	if conflictAtStart {
		member.SetConflict(true)
	}
	return member, nil
}

// queueDeps 组装队列消费依赖：Sender 回退主 bot（BotID=0 的存量任务/Web
// 补存），SenderFor 按任务受理 bot 经池解析。
func (a *app) queueDeps(ctx context.Context, fetcher *mtproto.Fetcher) queuepkg.Deps {
	primary := a.pool.SenderFor(0)
	// 缓存频道（转存频道复用）：channelID 闭包实时读取——Web 端 settings
	// 优先，环境变量 DUMP_CHANNEL_ID 兜底（均为 0 时复用关闭；之后可在
	// Web 端随时配置启用，无需重启）
	dumpChannel := func() int64 {
		return web.LoadEffectiveDumpChannelID(ctx, a.st, a.cfg.DumpChannelID)
	}
	dumpSvc := dumpcache.New(primary, a.pool.SenderFor, a.st, dumpChannel, a.log)
	if dumpChannel() != 0 {
		a.log.Info("缓存频道复用已启用", "channel_id", dumpChannel())
	}
	return queuepkg.Deps{
		Fetcher: fetcher,
		Sender:  primary, // 回退通道：存量任务 / Web 补存（BotID=0）走主 bot
		// 受理 bot 路由（多机器人池）
		SenderFor: func(j queuepkg.Job) delivery.Sender { return a.pool.SenderFor(j.BotID) },
		Store:     a.st,          // requests 行阶段埋点
		Events:    a.hub,         // 事件回调：连续任务失败 / 数据库写失败 / 临时目录占用
		Progress:  a.progressReg, // 请求记录页实时进度（Web 记录页展示）
		Copier:    a.bindings,    // 频道副本：任务成功后复制到该用户绑定的频道
		// 频道同步开关：投递前实时读 settings（运行设置修改即时生效）
		ChannelCopyEnabled: func() bool { return web.LoadChannelCopyEnabled(ctx, a.st) },
		// TG 链接复用开关：任务取数前实时读 settings（关闭即回到完整下载上传）
		ReuseEnabled: func() bool { return web.LoadTGReuseEnabled(ctx, a.st) },
		Dump:         dumpSvc,    // 缓存频道（未配置时 nil：无复用）
		Channels:     a.bindings, // 频道脚注：消息末尾织入该用户绑定频道的跳转链接
		// 云盘任务（/download）：上传通道与目的地解析；nil 防御在 worker 内
		CloudSink: a.cloudSink,
		CloudCfg:  a.cloud,
		Transfer:  a.transfer,
		Media: media.Options{
			TmpDir:          a.cfg.TempDir,
			MaxFileSize:     a.cfg.MaxFileSize,
			StreamLimit:     a.cfg.StreamLimit,
			MemoryLimit:     a.cfg.InMemoryLimit,
			DownloadThreads: a.cfg.DownloadThreads,
			MaxDirSize:      a.cfg.TempDirMaxSize,
			Memory:          a.memoryGate, // 进程级内存预算（预算不足降级落盘）
			FFmpegPath:      a.cfg.FFmpegPath,
			// 分卷拆分投递（split）：超限媒体切段后经相册整组直传
			MaxSplitTotalSize: config.MaxSplitTotalSize,
			SplitSegmentSize:  config.SplitSegmentSize,
		},
		Log: a.log,
	}
}

// notifyPendingCancel 处理排队任务取消的即时占位清理：按受理 bot 路由编辑
// （池为空时静默跳过——出队路径的 claim 拦截仍会兜底）。
func (a *app) notifyPendingCancel(j queuepkg.Job) {
	if j.StatusMsgID == 0 {
		return
	}
	snd := a.pool.SenderFor(j.BotID)
	if snd == nil {
		return
	}
	hctx, hcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer hcancel()
	if err := snd.EditMessageText(hctx, j.ChatID, j.StatusMsgID, queuepkg.CancelledStatusHTML(j.Ref)); err != nil {
		a.log.Warn("排队任务取消提示编辑失败", "chat_id", j.ChatID, "msg_id", j.StatusMsgID, "error", err.Error())
	}
}
