// 核心链路装配：MTProto 用户账号每次就绪（含重连）回调 onMTProtoReady，
// 在该生命周期内重建 fetcher、Bot 长轮询客户端、发送路由与队列消费；离线
// 时随回调 ctx 结束而停止，重连后重建。其余长生命周期服务由 main 构造并
// 集中在本结构体跨生命周期复用。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/botapi"
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
// 在 main 构造一次，Bot 客户端等 ready 作用域资源在每次回调内重建。
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
	botClient   *mtproto.BotClient
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
// 频道成员管理能力、挂 update 入口与对账兜底，随后组装 Bot 客户端、发送
// 路由与队列消费并阻塞在长轮询上，直到 ctx 取消（进程退出或会话离线）。
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

	b, err := botapi.New(botapi.Options{
		Cfg:      a.cfg,
		Log:      a.log,
		Queue:    a.q,
		Access:   a.access,
		Channels: a.bindings,
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
			status := botapi.RuntimeStatus{
				MTProtoState:   a.userClient.Session().Status().State,
				LargeFileReady: a.botClient.Available(),
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
		return fmt.Errorf("初始化 Bot 失败: %w", err)
	}
	// 拉取机器人身份（总览页展示 Name 与 @username）：库的 New 已隐式
	// getMe 但丢弃了结果，这里再取一次存入持有器；token 固定，Bot 重建
	// 时幂等刷新。失败只记日志不影响主链路，总览页保持"未接入"。
	meCtx, meCancel := context.WithTimeout(ctx, 10*time.Second)
	if me, meErr := b.GetMe(meCtx); meErr != nil {
		a.log.Warn("获取机器人身份失败（总览页将显示未接入）", "error", meErr.Error())
	} else {
		name := me.FirstName
		if me.LastName != "" {
			name += " " + me.LastName
		}
		a.botIdentity.set(me.ID, name, me.Username)
		a.log.Info("已获取机器人身份", "bot_id", me.ID, "bot_username", me.Username)
	}
	meCancel()
	menuCtx, menuCancel := context.WithTimeout(ctx, 10*time.Second)
	if err := botapi.RegisterCommands(menuCtx, b); err != nil {
		a.log.Warn("注册 Bot 命令菜单失败，继续启动长轮询", "error", err.Error())
	} else {
		a.log.Info("已注册 Bot 命令菜单")
	}
	menuCancel()
	sender := delivery.New(b, delivery.Config{PhotoLimit: a.cfg.PhotoLimit})

	// 业务发送走路由：未超过 Bot API 上限的媒体走 Bot API 上传，
	// 超限媒体经 Bot 号 MTProto 会话直传（不经 Bot API 服务器，
	// 上限 2000MB）；相册含超限成员时同样分流转 MTProto 整组直传；
	// 文本/删除等其余操作走 Bot API。
	router := delivery.NewRouter(sender, a.botClient, a.cfg.BotAPIUploadCap(), a.cfg.MaxFileSize)
	// 事件通知通道用原始 sender：通知失败是"通知"这一动作的结果，
	// 不计入业务发送失败，避免"通知失败 → 产生事件 → 再通知"的自激回路
	a.hub.SetSender(sender)
	// 业务发送经计数包装：Bot API 连续发送失败达到阈值时产生事件
	counted := notify.NewCountSender(router, a.hub)
	a.access.SetSender(counted)
	// Bot 客户端就绪：频道绑定校验（GetChat/GetChatMember）与频道副本
	// 投递（CopyMessages）都经它走 Bot API
	a.bindings.SetBot(b)
	// 缓存频道（转存频道复用）：channelID 闭包实时读取——Web 端 settings
	// 优先，环境变量 DUMP_CHANNEL_ID 兜底（均为 0 时复用关闭；之后可在
	// Web 端随时配置启用，无需重启）
	dumpChannel := func() int64 {
		return web.LoadEffectiveDumpChannelID(ctx, a.st, a.cfg.DumpChannelID)
	}
	dumpSvc := dumpcache.New(counted, a.st, dumpChannel, a.log)
	// 副本有效性校验（dumpcache.EntryLive，试探复制判定）注入管理端补写
	// 预检：缓存频道里的副本消息被客户端删除后，条目坐标不感知删除，
	// 据此放行重新补写自愈
	a.access.SetDumpLive(dumpSvc.EntryLive)
	if dumpChannel() != 0 {
		a.log.Info("缓存频道复用已启用", "channel_id", dumpChannel())
	}
	// 频道加入审批结果通知通道（用原始 sender，通知失败不计业务失败）
	a.join.SetNotifier(joinmgr.SenderNotifier{Sender: sender})
	// 排队任务被取消（/cancel 与 Web 管理端共用）时立即把占位提示改为
	// 取消文案：worker 的 claim 拦截要等出队才触发，队列繁忙时滞留可达
	// 分钟级。与出队路径写入同一文案，重复编辑幂等。
	a.q.SetPendingCancelHandler(func(j queuepkg.Job) {
		if j.StatusMsgID == 0 {
			return
		}
		hctx, hcancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer hcancel()
		if err := counted.EditMessageText(hctx, j.ChatID, j.StatusMsgID, queuepkg.CancelledStatusHTML(j.Ref)); err != nil {
			a.log.Warn("排队任务取消提示编辑失败", "chat_id", j.ChatID, "msg_id", j.StatusMsgID, "error", err.Error())
		}
	})

	deps := queuepkg.Deps{
		Fetcher:  fetcher,
		Sender:   counted,
		Store:    a.st,          // requests 行阶段埋点
		Events:   a.hub,         // 事件回调：连续任务失败 / 数据库写失败 / 临时目录占用
		Progress: a.progressReg, // 请求记录页实时进度（Web 记录页展示）
		Copier:   a.bindings,    // 频道副本：任务成功后复制到该用户绑定的频道
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
		},
		Log: a.log,
	}
	// 队列可观测地结束：轮询停止后等 drain（退出通知+状态清理）完成再退出本轮
	queueDone := make(chan struct{})
	go func() {
		a.q.Run(ctx, a.cfg.WorkerCount, queuepkg.Process(deps), queuepkg.Discard(deps))
		close(queueDone)
	}()

	a.log.Info("核心链路就绪，启动长轮询")
	b.Start(ctx)
	<-queueDone
	return nil
}
