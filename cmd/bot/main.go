// spore 入口：加载 .env → 校验配置 → 初始化日志 →
// 启动 MTProto 用户账号客户端（首次扫码/验证码登录），就绪后在回调内
// 组装 fetcher / bot / sender / queue 并启动长轮询。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	// 内嵌 IANA 时区数据库：运营时区（默认 Asia/Shanghai）在无系统 tzdata 的
	// 容器（scratch/distroless）里也能解析（按运营时区切换额度周期）
	_ "time/tzdata"

	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/botapi"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/dumpcache"
	"github.com/huaiminyetnotsleep/spore/internal/joinmgr"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/monitor"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/notify"
	"github.com/huaiminyetnotsleep/spore/internal/progress"
	queuepkg "github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
	"github.com/huaiminyetnotsleep/spore/internal/transfercfg"
	"github.com/huaiminyetnotsleep/spore/internal/web"
)

// version 由构建期注入（-ldflags "-X main.version=..."，见 Makefile 与
// docker-publish.yml）：CI 镜像构建传 git tag 或 commit SHA，本地
// go run/build 保持 dev——运行中的容器经 spore version 自证构建来源。
var version = "dev"

func main() {
	_ = godotenv.Load()

	// admin/version 子命令（如 spore admin reset-key）：不启动 Bot，执行后直接退出
	if len(os.Args) > 1 {
		runAdminCommand(os.Args[1:])
		return
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置错误:", err)
		os.Exit(1)
	}

	logger := newLogger(cfg.LogLevel)
	logger.Info("spore 启动",
		"worker_count", cfg.WorkerCount,
		"data_dir", cfg.DataDir,
	)

	// 数据目录必须存在（session.json / peers.json 的落盘目标）
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		logger.Error("创建数据目录失败", "data_dir", cfg.DataDir, "error", err.Error())
		os.Exit(1)
	}

	// 启动时清理上次异常退出遗留的孤儿临时文件（只删本程序命名的条目）
	media.CleanOrphans(cfg.TempDir, logger)
	if err := os.MkdirAll(cfg.TempDir, 0o755); err != nil {
		logger.Error("创建临时目录失败", "temp_dir", cfg.TempDir, "error", err.Error())
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// confirmed marker 只在启动早期应用；没有 marker 的普通重启完全跳过导入。
	dbPath := filepath.Join(cfg.DataDir, branding.DatabaseFile)
	if applied, err := store.ApplyPendingImport(ctx, dbPath, cfg.DataDir, logger); err != nil {
		logger.Error("应用待导入数据库失败", "error", err.Error())
		os.Exit(1)
	} else if applied {
		logger.Info("待导入数据库已应用", "path", dbPath)
	}

	// 业务数据库（访问控制 / 请求记录 / Web 管理端共享的持久层）：
	// 启动时自动执行版本化迁移；本入口只做连接编排，业务读写由各内部包进行。
	st, err := store.Open(ctx, dbPath, logger)
	if err != nil {
		logger.Error("打开业务数据库失败", "path", dbPath, "error", err.Error())
		os.Exit(1)
	}
	defer st.Close()

	// 数据库覆盖值异常时不采用不可发送的配置：保留环境配置启动 Web 管理端，
	// 同时记录受控事件，管理员可登录修复设置后再重启。
	var runtimeMediaErr error
	if runtimeCfg, err := web.ApplyRuntimeMediaConfig(ctx, st, cfg); err != nil {
		runtimeMediaErr = err
		logger.Error("读取媒体运行配置失败，将暂不应用数据库覆盖值", "error", err.Error())
	} else {
		cfg = runtimeCfg
	}
	// 任务并发 worker 数同样以数据库覆盖 env（Web 运行设置修改，重启生效）；
	// 覆盖值缺失或非法时回退 env 值，不阻断启动。
	cfg.WorkerCount = web.LoadWorkerCount(ctx, st, cfg.WorkerCount)
	logger.Info("任务并发 worker 数已确定", "worker_count", cfg.WorkerCount)

	transferRuntime, err := transfercfg.NewFromConfig(ctx, st, cfg)
	if err != nil {
		logger.Error("初始化传输运行时配置失败", "error", err.Error())
		os.Exit(1)
	}

	// 媒体内存预算闸门（进程级）：媒体进入内存管道前按文件大小记账，预算
	// 不足自动降级临时文件路径（边下边传），常驻 RAM 被额度封顶而不随并发
	// 任务数放大。额度闭包实时读 settings（memory_budget 键优先，env 兜底），
	// 管理端修改即时生效。
	memoryGate := media.NewBudgetGate(func() int64 {
		return web.LoadMemoryBudget(ctx, st, cfg.MemoryBudget)
	})

	// 云盘下载（/download 指令）：配置管理器 + rclone 上传通道。
	// data/cloud-drive.json 与 session.json 同级（0600，凭据只落该文件不进
	// 数据库）。加载失败（文件损坏/校验不通过）时 Manager 保持零值快照
	// （功能关闭态），不阻断启动；错误在事件中心就绪后上报 cloud.config_invalid。
	cloudMgr := cloudarchive.NewManager(filepath.Join(cfg.DataDir, cloudarchive.FileName), logger)
	// NewManager 已尝试加载并记 Warn；再显式加载一次取错误供事件上报
	// （幂等的小文件读，正常配置下无副作用）。
	cloudCfgLoadErr := cloudMgr.Load()
	rcloneSink := cloudarchive.NewRcloneSink(logger)

	// 兼容迁移：首次启动（users 表为空）且配置了 ALLOWED_USER_IDS 时导入为
	// enabled 用户；此后白名单以数据库为准，env 不再强制非空
	if err := access.ImportLegacyWhitelist(ctx, st, cfg.AllowedUserIDs, logger); err != nil {
		logger.Error("导入环境变量白名单失败", "error", err.Error())
		os.Exit(1)
	}

	// 事件中心：异常事件写库去重合并，并经 Bot API 向
	// 管理员（users 表 owner 用户）私聊推送通知（冷却窗口内不重复推送）。
	// 事件写入/通知失败只记日志，绝不阻塞主链路。
	hub, err := notify.New(notify.Options{
		Store:             st,
		Log:               logger,
		Cooldown:          time.Duration(cfg.NotifyCooldownMin) * time.Minute,
		BotFailThreshold:  cfg.EventBotFailThreshold,
		TaskFailThreshold: cfg.EventTaskFailThreshold,
		TempDir:           cfg.TempDir,
		DiskLimitBytes:    int64(cfg.EventDiskLimitGB * float64(1<<30)),
	})
	if err != nil {
		logger.Error("初始化事件中心失败", "error", err.Error())
		os.Exit(1)
	}
	if runtimeMediaErr != nil {
		hub.Raise(ctx, notify.KeyMediaConfigInvalid, notify.SeverityError, "")
	}
	if cloudCfgLoadErr != nil {
		hub.CloudConfigInvalid(ctx)
	}

	// rclone 启动探测 + 低频周期复查：开启云盘
	// 但二进制不可用时上报事件（/download 回"功能暂不可用"），恢复可用后
	// 自动解决；未开启云盘时不打扰。探测只是 stat/LookPath，无子进程开销。
	go func() {
		check := func() {
			if _, err := cloudarchive.BinPath(); err != nil {
				if cloudMgr.Enabled() {
					logger.Warn("rclone 不可用，云盘下载功能按禁用处理", "error", err.Error())
					hub.CloudDisabled(ctx)
				}
				return
			}
			hub.CloudDisabledRecovered(ctx)
		}
		check()
		ticker := time.NewTicker(cloudRecheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	}()

	// 启动恢复：把上次进程退出遗留的 queued/processing 请求批量置为
	// failed(INTERRUPTED)，不静默丢失，可在 Web 侧重试。
	// 必须在队列 worker 启动前执行；失败说明数据库本身异常，按启动失败退出。
	if n, err := st.FailInterruptedRequests(ctx, 0); err != nil {
		logger.Error("恢复中断请求失败", "error", err.Error())
		os.Exit(1)
	} else if n > 0 {
		logger.Warn("已将上次运行遗留的未完成任务标记为失败（可重试）",
			"count", n, "error_code", string(apperr.CodeInterrupted))
		// 启动恢复的批量中断也是事件：入 Web 事件中心，
		// Bot 通道就绪后由 SetSender 补发通知
		hub.Raise(ctx, notify.KeyStartupRecovered, notify.SeverityWarn,
			fmt.Sprintf("上次运行遗留的 %d 个未完成任务已标记为失败（INTERRUPTED），可在管理端重试。", n))
	}

	// 内存队列与访问控制服务在 MTProto 就绪前创建：Web 管理页面的
	// 审批/重试/限额等操作与 Bot 提交共用同一服务与队列。容量经设置项
	// queue_capacity 配置（缺省 64），进程生命周期内固定——修改需重启生效。
	q := queuepkg.New(web.LoadQueueCapacity(ctx, st))
	// 实时传输进度注册表：worker 写入、Web 管理端读取，进程内共享同一实例
	// （内存态不落库，任务收尾即清除）。
	progressRegistry := progress.NewRegistry()
	metrics := monitor.New(monitor.Options{
		Store: st, Transfers: progressRegistry, TempDir: cfg.TempDir, Logger: logger,
	})
	go metrics.Run(ctx)
	accessSvc, err := access.New(access.Options{Store: st, Queue: q, Events: hub, Log: logger})
	if err != nil {
		logger.Error("初始化访问控制服务失败", "error", err.Error())
		os.Exit(1)
	}
	// 频道绑定服务（Bot /bind 与 Web 管理端共用）：Bot 客户端在长轮询链路
	// 就绪后经 SetBot 注入；worker 的频道副本投递也由它实现（queue.Copier）。
	bindingSvc, err := binding.New(binding.Options{Store: st, Log: logger})
	if err != nil {
		logger.Error("初始化频道绑定服务失败", "error", err.Error())
		os.Exit(1)
	}

	// Web 管理端（认证底座与管理页面）：首次部署生成访问密钥
	// （明文仅打印一次到 stderr），随后以独立 goroutine 启动 HTTP 服务。
	// Web 与 MTProto 生命周期解耦：启动失败或运行异常只记日志，
	// 绝不影响 Bot 主链路。
	if _, err := web.EnsureAccessKey(ctx, st, os.Stderr, logger); err != nil {
		logger.Error("初始化管理端访问密钥失败", "error", err.Error())
		os.Exit(1)
	}
	m := mtproto.New(cfg, logger)
	m.SetTransferRuntime(transferRuntime)
	profileLookup := mtproto.NewProfileLookup()
	// Bot 身份 MTProto 会话（大文件直传通道）独立于用户号会话，
	// 同时把脱敏状态注入 Web 管理端展示。
	botClient := mtproto.NewBotClient(cfg, logger)
	botClient.SetTransferRuntime(transferRuntime)
	// 频道成员管理桥接器（/join、Web 已加入频道页共用）：ready 生命周期内
	// 绑定 API 与 Fetcher，离线时相关操作返回受控不可用。
	membership := mtproto.NewMembershipBridge()
	// MTProto 会话状态 → 事件中心（经观察回调，mtproto 不感知 notify）：
	// 转入 offline 产生/合并告警；重连成功（ready）自动解决对应事件。
	// 回调在 mtproto 状态锁外执行，Raise/Recover 内部自带限时，不会拖慢状态机。
	m.Session().SetStateHook(func(state string) {
		switch state {
		case mtproto.StateOffline:
			hub.Raise(ctx, notify.KeySessionOffline, notify.SeverityError,
				"MTProto 会话已离线，需要重新登录（可在管理端扫码重连，或重启进程走终端登录）。")
		case mtproto.StateReady:
			hub.Recover(ctx, notify.KeySessionOffline)
		}
	})
	// 频道加入服务（Bot /join 与 Web 管理端共用）：审批通知经 Bot 私聊
	// 送达申请人（sender 在 MTProto ready 内经 SetNotifier 注入）；
	// Bridge 的实际能力同样在 ready 作用域内绑定。
	joinSvc, err := joinmgr.New(joinmgr.Options{
		Store:  st,
		Bridge: membership,
		Log:    logger,
	})
	if err != nil {
		logger.Error("初始化频道加入服务失败", "error", err.Error())
		os.Exit(1)
	}
	webSrv, err := web.New(web.Options{
		Store:       st,
		Cfg:         cfg,
		Log:         logger,
		Access:      accessSvc,     // 管理操作入口（审批/重试/限额/设置）
		Queue:       q,             // 总览页队列指标
		MTProto:     m.Session(),   // 扫码登录状态与重连接口（§6.4）
		BotMTProto:  botClient,     // Bot 会话状态与当前 DC
		Profile:     profileLookup, // 已就绪且有上下文时刷新用户资料
		RestartFunc: func() error { return syscall.Kill(os.Getpid(), syscall.SIGTERM) },
		Hub:         hub,              // 事件中心（resolve 统一经它执行并留审计）
		Progress:    progressRegistry, // 请求记录页实时进度（与 worker 共享）
		Monitor:     metrics,          // 系统资源与传输监控
		Bindings:    bindingSvc,       // 频道绑定管理页（列表/绑定/解绑）

		ChannelJoin:    joinSvc,                // 频道加入管理页（审批/已加入/退出）
		Transfer:       transferRuntime,        // 四项传输并发的原子运行时配置
		CloudCfg:       cloudMgr,               // 云盘下载页（配置视图/保存）与补存端点
		CloudSink:      rcloneSink,             // 云盘目的地连通性测试通道
		CloudBackupKey: cfg.OAuthEncryptionKey, // 云盘备份候选服务端加密根密钥
	})
	if err != nil {
		logger.Error("初始化 Web 管理端失败（Bot 继续运行）", "error", err.Error())
	} else {
		go func() {
			if err := webSrv.Run(ctx); err != nil {
				logger.Error("Web 管理端退出（Bot 继续运行）", "error", err.Error())
			}
		}()
	}

	// Bot 身份 MTProto 会话（大文件直传通道）：与用户号会话、Bot API
	// 长轮询并存；异常退出自动退避重启，未就绪时超过 Bot API 上限的媒体
	// 以确定性错误结束（小文件不受影响）。
	go func() {
		if err := botClient.Run(ctx); err != nil {
			logger.Error("Bot MTProto 会话循环退出", "error", err.Error())
		}
	}()

	err = m.Run(ctx, func(ctx context.Context, api *tg.Client) error {
		profileLookup.SetAPI(api)
		defer profileLookup.Clear()
		fetcher := mtproto.NewFetcher(api, cfg.DataDir, logger)
		// 频道成员管理能力随本生命周期绑定/解绑（离线时受控不可用）
		membership.SetAPI(api, fetcher)
		defer membership.Clear()

		// update 实时入口：新见频道（peer 缓存未命中）异步交给 joinmgr 补
		// 静音/归档——被拉入频道秒级归档。绑定目标在 gotd 读循环内同步
		// 执行，只做缓存过滤与起 goroutine；离线窗口漏收的由下方对账兜底。
		m.Updates().Bind(func(uctx context.Context, channels []tg.Channel) {
			seen := fetcher.HarvestNovelChannels(channels)
			if len(seen) == 0 {
				return
			}
			go joinSvc.OnChannelsSeen(uctx, seen)
		})
		defer m.Updates().Unbind()

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
					if err := joinSvc.ReconcileExternal(ctx); err != nil {
						logger.Info("外部拉入频道周期对账失败（下轮重试）", "error", err.Error())
					}
				}
			}
		}()

		b, err := botapi.New(botapi.Options{
			Cfg:      cfg,
			Log:      logger,
			Queue:    q,
			Access:   accessSvc,
			Channels: bindingSvc,
			WrapSender: func(snd delivery.Sender) delivery.Sender {
				return notify.NewCountSender(snd, hub)
			},
			// 系统名称：每次命令实时读 settings（管理端修改即时生效）
			SystemName: func(ctx context.Context) string { return syscfg.Name(ctx, st) },
			Whoami:     func(ctx context.Context) (string, error) { return mtproto.Me(ctx, api) },
			// /join：joinmgr 统一处理开关/上限/审核分流；owner 判定经
			// users 表全局唯一 owner（未设置时按普通用户走审核）
			ChannelJoin: joinSvc,
			// /download：云盘状态与目的地解析（Manager 快照 + rclone 实时探测）
			CloudStatus: cloudDriveStatus{mgr: cloudMgr},
			IsOwner: func(ctx context.Context, userID int64) (bool, error) {
				ownerID, err := st.OwnerID(ctx)
				if err != nil {
					return false, err
				}
				return ownerID != 0 && ownerID == userID, nil
			},
			Status: botapi.StatusFunc(func(ctx context.Context) (botapi.RuntimeStatus, error) {
				probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				status := botapi.RuntimeStatus{
					MTProtoState:   m.Session().Status().State,
					LargeFileReady: botClient.Available(),
					QueueLen:       q.Len(),
					QueueCap:       q.Cap(),
					WorkerCount:    cfg.WorkerCount,
					StoreReady:     true,
				}
				if _, _, probeErr := st.GetSetting(probeCtx, "bot_status_probe"); probeErr != nil {
					status.StoreReady = false
				}
				_, processing, countErr := st.CountRequestsByStatus(probeCtx)
				status.Processing = processing
				if countErr != nil {
					status.StoreReady = false
				}
				return status, nil
			}),
			ProfileObserver: profileLookup.ObserveUser,
		})

		if err != nil {
			return fmt.Errorf("初始化 Bot 失败: %w", err)
		}
		menuCtx, menuCancel := context.WithTimeout(ctx, 10*time.Second)
		if err := botapi.RegisterCommands(menuCtx, b); err != nil {
			logger.Warn("注册 Bot 命令菜单失败，继续启动长轮询", "error", err.Error())
		} else {
			logger.Info("已注册 Bot 命令菜单")
		}
		menuCancel()
		sender := delivery.New(b, delivery.Config{PhotoLimit: cfg.PhotoLimit})

		// 业务发送走路由：未超过 Bot API 上限的媒体走 Bot API 上传，
		// 超限媒体经 Bot 号 MTProto 会话直传（不经 Bot API 服务器，
		// 上限 2000MB）；相册含超限成员时同样分流转 MTProto 整组直传；
		// 文本/删除等其余操作走 Bot API。
		router := delivery.NewRouter(sender, botClient, cfg.BotAPIUploadCap(), cfg.MaxFileSize)
		// 事件通知通道用原始 sender：通知失败是"通知"这一动作的结果，
		// 不计入业务发送失败，避免"通知失败 → 产生事件 → 再通知"的自激回路
		hub.SetSender(sender)
		// 业务发送经计数包装：Bot API 连续发送失败达到阈值时产生事件
		counted := notify.NewCountSender(router, hub)
		accessSvc.SetSender(counted)
		// Bot 客户端就绪：频道绑定校验（GetChat/GetChatMember）与频道副本
		// 投递（CopyMessages）都经它走 Bot API
		bindingSvc.SetBot(b)
		// 缓存频道（转存频道复用）：channelID 闭包实时读取——Web 端 settings
		// 优先，环境变量 DUMP_CHANNEL_ID 兜底（均为 0 时复用关闭；之后可在
		// Web 端随时配置启用，无需重启）
		dumpChannel := func() int64 {
			return web.LoadEffectiveDumpChannelID(ctx, st, cfg.DumpChannelID)
		}
		dumpSvc := dumpcache.New(counted, st, dumpChannel, logger)
		if dumpChannel() != 0 {
			logger.Info("缓存频道复用已启用", "channel_id", dumpChannel())
		}
		// 频道加入审批结果通知通道（用原始 sender，通知失败不计业务失败）
		joinSvc.SetNotifier(joinmgr.SenderNotifier{Sender: sender})
		// 排队任务被取消（/cancel 与 Web 管理端共用）时立即把占位提示改为
		// 取消文案：worker 的 claim 拦截要等出队才触发，队列繁忙时滞留可达
		// 分钟级。与出队路径写入同一文案，重复编辑幂等。
		q.SetPendingCancelHandler(func(j queuepkg.Job) {
			if j.StatusMsgID == 0 {
				return
			}
			hctx, hcancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer hcancel()
			if err := counted.EditMessageText(hctx, j.ChatID, j.StatusMsgID, queuepkg.CancelledStatusHTML(j.Ref)); err != nil {
				logger.Warn("排队任务取消提示编辑失败", "chat_id", j.ChatID, "msg_id", j.StatusMsgID, "error", err.Error())
			}
		})

		deps := queuepkg.Deps{
			Fetcher:  fetcher,
			Sender:   counted,
			Store:    st,               // requests 行阶段埋点
			Events:   hub,              // 事件回调：连续任务失败 / 数据库写失败 / 临时目录占用
			Progress: progressRegistry, // 实时下载/上传进度（Web 记录页展示）
			Copier:   bindingSvc,       // 频道副本：任务成功后复制到该用户绑定的频道
			// 频道同步开关：投递前实时读 settings（运行设置修改即时生效）
			ChannelCopyEnabled: func() bool { return web.LoadChannelCopyEnabled(ctx, st) },
			// TG 链接复用开关：任务取数前实时读 settings（关闭即回到完整下载上传）
			ReuseEnabled: func() bool { return web.LoadTGReuseEnabled(ctx, st) },
			Dump:         dumpSvc,    // 缓存频道（未配置时 nil：无复用）
			Channels:     bindingSvc, // 频道脚注：消息末尾织入该用户绑定频道的跳转链接
			// 云盘任务（/download）：上传通道与目的地解析；nil 防御在 worker 内
			CloudSink: rcloneSink,
			CloudCfg:  cloudMgr,
			Transfer:  transferRuntime,
			Media: media.Options{
				TmpDir:          cfg.TempDir,
				MaxFileSize:     cfg.MaxFileSize,
				StreamLimit:     cfg.StreamLimit,
				MemoryLimit:     cfg.InMemoryLimit,
				DownloadThreads: cfg.DownloadThreads,
				MaxDirSize:      cfg.TempDirMaxSize,
				Memory:          memoryGate, // 进程级内存预算（预算不足降级落盘）
				FFmpegPath:      cfg.FFmpegPath,
			},
			Log: logger,
		}
		// 队列可观测地结束：轮询停止后等 drain（退出通知+状态清理）完成再退出本轮
		queueDone := make(chan struct{})
		go func() {
			q.Run(ctx, cfg.WorkerCount, queuepkg.Process(deps), queuepkg.Discard(deps))
			close(queueDone)
		}()

		logger.Info("核心链路就绪，启动长轮询")
		b.Start(ctx)
		<-queueDone
		return nil
	})

	if err != nil && ctx.Err() == nil {
		logger.Error("运行异常退出", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("已退出")
}

func newLogger(level slog.Level) *slog.Logger {
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}

// cloudRecheckInterval 是 rclone 可用性周期复查间隔：
// 启动探测 + 低频复查，如 10 分钟）。
const cloudRecheckInterval = 10 * time.Minute

// cloudDriveStatus 组合 cloudarchive.Manager 配置快照与 rclone 探测，实现
// botapi.CloudStatus（/download 准入预检）。Available 每次实时探测（BinPath
// 只是 stat/LookPath，/download 提交频度下开销可忽略）；其余走 Manager 的
// 无锁内存快照。凭据与 options 值不出现在任何返回值中。
type cloudDriveStatus struct {
	mgr *cloudarchive.Manager
}

func (s cloudDriveStatus) Enabled() bool { return s.mgr.Enabled() }

func (s cloudDriveStatus) Available() bool {
	_, err := cloudarchive.BinPath()
	return err == nil
}

func (s cloudDriveStatus) DefaultDestination() string {
	return s.mgr.Snapshot().DefaultDestination
}

func (s cloudDriveStatus) DestinationEnabled(name string) bool {
	_, ok := s.mgr.ResolveCloudDestination(name)
	return ok
}

func (s cloudDriveStatus) EnabledDestinations() []string {
	var names []string
	for _, d := range s.mgr.Snapshot().Destinations {
		if d.Enabled {
			names = append(names, d.Name)
		}
	}
	return names
}

// runAdminCommand 执行管理子命令后退出；目前支持 version（输出版本号）与
// admin reset-key（重新生成访问密钥，旧密钥与全部会话立即失效，动作留审计）。
func runAdminCommand(args []string) {
	if len(args) == 1 && args[0] == "version" {
		fmt.Println("spore", version)
		return
	}
	if len(args) == 2 && args[0] == "admin" && args[1] == "reset-key" {
		adminResetKey()
		return
	}
	fmt.Fprintln(os.Stderr, "未知子命令。用法：spore version | spore admin reset-key")
	os.Exit(2)
}

// adminResetKey 只依赖数据目录与数据库，不要求 Bot Token 等 Telegram 凭据，
// 便于在凭据不全的恢复场景重置管理端入口。
func adminResetKey() {
	cfg, err := config.LoadAdminCLI(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "配置错误:", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, branding.DatabaseFile), logger)
	if err != nil {
		logger.Error("打开业务数据库失败", "error", err.Error())
		os.Exit(1)
	}
	defer st.Close()

	if err := web.ResetAccessKey(ctx, st, os.Stderr, logger); err != nil {
		logger.Error("重置访问密钥失败", "error", err.Error())
		os.Exit(1)
	}
}
