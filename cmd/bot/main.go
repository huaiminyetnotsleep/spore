// spore 入口：加载 .env → 校验配置 → 初始化日志 → 启动 MTProto 用户账号
// 客户端（首次扫码/验证码登录），就绪后执行 app.go 的 onMTProtoReady 回调
// 组装 fetcher / bot / sender / queue 并启动长轮询。本文件只负责组装根
// （配置、数据目录、长生命周期服务与 Web 管理端）；核心链路装配见 app.go。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	// 内嵌 IANA 时区数据库：运营时区（默认 Asia/Shanghai）在无系统 tzdata 的
	// 容器（scratch/distroless）里也能解析（按运营时区切换额度周期）
	_ "time/tzdata"

	"github.com/joho/godotenv"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/botlist"
	"github.com/huaiminyetnotsleep/spore/internal/botpool"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/cloudarchive"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/joinmgr"
	"github.com/huaiminyetnotsleep/spore/internal/media"
	"github.com/huaiminyetnotsleep/spore/internal/monitor"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/notify"
	"github.com/huaiminyetnotsleep/spore/internal/notifycfg"
	"github.com/huaiminyetnotsleep/spore/internal/progress"
	queuepkg "github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/transfercfg"
	"github.com/huaiminyetnotsleep/spore/internal/watch"
	"github.com/huaiminyetnotsleep/spore/internal/web"
)

// version 由构建期注入（-ldflags "-X main.version=..."，见 Makefile 与
// docker-publish.yml）：CI 镜像构建传 git tag 或 commit SHA，本地
// go run/build 保持 dev——运行中的容器经 spore version 自证构建来源，
// 注入值同时下发 Web 管理端（总览页服务信息与云盘备份元数据）。
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

	// ffmpeg 能力探测（尽力而为，不阻断启动）：大视频可播放切段依赖
	// matroska 封装器——镜像自带的精简 ffmpeg 已包含；FFMPEG_PATH 指向
	// 阉割构建时启动即告警（运行期切段自动回退字节分段，不影响抽帧）。
	if cfg.FFmpegPath != "" {
		if _, err := exec.LookPath(cfg.FFmpegPath); err == nil {
			pctx, pcancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := media.CheckMatroskaMuxer(pctx, cfg.FFmpegPath); err != nil {
				logger.Warn("ffmpeg 缺少 matroska 封装器，超大视频可播放切段不可用（运行期自动回退字节分段投递）",
					"ffmpeg", cfg.FFmpegPath, "error", err.Error())
			}
			pcancel()
		} else {
			logger.Warn("ffmpeg 不可用，视频封面兜底与可播放切段停用（大视频回退字节分段投递）",
				"ffmpeg", cfg.FFmpegPath, "error", err.Error())
		}
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
			notify.InterruptedData{Count: n})
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
	accessSvc, err := access.New(access.Options{
		Store: st, Queue: q, Events: hub,
		Activity: activityHub{hub: hub}, // 新用户申请活动通知
		Log:      logger,
	})
	if err != nil {
		logger.Error("初始化访问控制服务失败", "error", err.Error())
		os.Exit(1)
	}
	// 通知时间渲染接入运营时区（设置变更即时生效；未设置回退 GMT+8）。
	hub.SetTimezone(func() *time.Location { return accessSvc.Location(context.Background()) })
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
	// 多机器人池：env（BOT_TOKEN 主 bot + BOT_TOKENS 追加）提供基础列表，
	// Web 管理端可向 data/bots.json（0600，凭据不入库）增删，合并去重后
	// 生效（重启应用）。文件加载失败不阻断启动：回退 env 列表，事件中心
	// 就绪后上报（管理员可在管理端修复）。大文件直传会话按 bot 隔离，
	// 主 bot 沿用历史 bot-session.json 免重登。
	botMgr := botlist.NewManager(cfg.DataDir, logger)
	bots := botlist.Resolve(cfg.BotTokens, botMgr, logger)
	if len(bots) > 1 {
		logger.Info("多机器人池已启用", "bots", len(bots), "primary_source", string(bots[0].Source))
	}
	if err := botMgr.LoadError(); err != nil {
		// hub 尚未构建（云盘同款时序差异）：延迟到 hub 就绪后上报
		defer func() {
			hub.Raise(context.Background(), notify.KeyBotListInvalid, notify.SeverityWarn, nil)
		}()
	}
	// 每 bot 独立的大文件直传会话（botID 即 token 数字前缀）：跨 MTProto
	// 重连复用，长轮询重建不影响会话。
	pool := botpool.New()
	runtime := newBotRuntime(pool, st, logger)
	botClients := make(map[int64]*mtproto.BotClient, len(bots))
	for i, bt := range bots {
		client := mtproto.NewBotClientFor(cfg, logger, bt.Token,
			botlist.SessionPath(cfg.DataDir, bt.Token, i == 0))
		client.SetTransferRuntime(transferRuntime)
		botClients[botlist.BotID(bt.Token)] = client
	}
	// Web 顶层 Bot 会话状态必须挂在真实运行的主 bot 客户端上：
	// 单独 new 一个不 Run 的客户端会永远返回初始 offline（975db56 回归）。
	// 频道成员管理桥接器（/join、Web 已加入频道页共用）：ready 生命周期内
	// 绑定 API 与 Fetcher，离线时相关操作返回受控不可用。
	membership := mtproto.NewMembershipBridge()
	// MTProto 会话状态 → 事件中心（经观察回调，mtproto 不感知 notify）：
	// 转入 offline 产生/合并告警；重连成功（ready）自动解决对应事件。
	// 回调在 mtproto 状态锁外执行，Raise/Recover 内部自带限时，不会拖慢状态机。
	m.Session().SetStateHook(func(state string) {
		switch state {
		case mtproto.StateOffline:
			hub.Raise(ctx, notify.KeySessionOffline, notify.SeverityError, nil)
		case mtproto.StateReady:
			hub.Recover(ctx, notify.KeySessionOffline)
		}
	})
	// 频道加入服务（Bot /join 与 Web 管理端共用）：审批通知经 Bot 私聊
	// 送达申请人（sender 在 MTProto ready 内经 SetNotifier 注入）；
	// Bridge 的实际能力同样在 ready 作用域内绑定。
	joinSvc, err := joinmgr.New(joinmgr.Options{
		Store:    st,
		Bridge:   membership,
		Activity: activityHub{hub: hub}, // 新频道加入申请活动通知
		Log:      logger,
	})
	if err != nil {
		logger.Error("初始化频道加入服务失败", "error", err.Error())
		os.Exit(1)
	}
	// 监听源服务（Bot /watch 与 Web 管理端共用）：源校验用的 Bot 客户端、
	// 审批结果通知与私有邀请能力（读取账号 Check/JoinInvite）在 MTProto
	// ready 内注入；邀请申请的周期对账随 ready 会话启动（app.go）。
	watchSvc, err := watch.New(watch.Options{Store: st, Membership: membership, Log: logger})
	if err != nil {
		logger.Error("初始化监听源服务失败", "error", err.Error())
		os.Exit(1)
	}
	// 接入机器人身份（总览页展示）：直接读多机器人池成员快照，Bot 客户端
	// 在 MTProto 就绪后才入池（重连会重建），未就绪时总览页显示"未接入"。
	botIdentity := newBotIdentityStore(pool)
	// 检查更新（总览页服务版本旁刷新按钮）：查询上游 GitHub 最新 Release，
	// 结果缓存 1 小时；查询失败时端点受控降级，不影响其他功能。
	releaseCheck := web.NewGitHubReleaseChecker()
	// 通知通道配置存 settings；敏感字段使用 OAuth 主密钥经 HKDF 域分离后
	// 加密。主密钥缺失不阻断启动，但管理端不能保存新凭据。
	notificationCfg := notifycfg.NewManager(st, cfg.OAuthEncryptionKey, notifycfg.Options{})
	// 事件 Hub 通过最小运行时接口使用通知设置中的自动事件开关、策略和通道。
	// 注入后会补发启动前已经落库但尚未成功通知的事件；旧 owner 私聊在
	// automatic_events 关闭或配置异常时继续作为兼容回退。
	hub.SetRuntimeNotifier(notificationCfg)
	webSrv, err := web.New(web.Options{
		Store:             st,
		Cfg:               cfg,
		Log:               logger,
		Access:            accessSvc,                                        // 管理操作入口（审批/重试/限额/设置）
		Queue:             q,                                                // 总览页队列指标
		MTProto:           m.Session(),                                      // 扫码登录状态与重连接口（§6.4）
		BotMTProto:        botClients[botlist.BotID(bots[0].Token)],         // Bot 会话状态与当前 DC（主 bot；多 bot 见 robots 管理页）
		BotIdentity:       botIdentity,                                      // 总览页展示机器人池身份（Bot 就绪后经 getMe 回填）
		BotList:           botMgr,                                           // 机器人管理页（env ∪ bots.json 列表/增删）
		BotMTProtoList:    botMTProtoStore{pool: pool, clients: botClients}, // 逐 bot 直传会话状态
		BotRuntimeControl: runtime,                                          // 暂停/恢复（即时生效，settings 持久化）
		Profile:           profileLookup,                                    // 已就绪且有上下文时刷新用户资料
		RestartFunc:       func() error { return syscall.Kill(os.Getpid(), syscall.SIGTERM) },
		Hub:               hub,              // 事件中心（resolve 统一经它执行并留审计）
		Notification:      notificationCfg,  // 通知通道配置与测试发送
		Progress:          progressRegistry, // 请求记录页实时进度（与 worker 共享）
		Monitor:           metrics,          // 系统资源与传输监控
		Bindings:          bindingSvc,       // 频道绑定管理页（列表/绑定/解绑）

		ChannelJoin:    joinSvc,                // 频道加入管理页（审批/已加入/退出）
		Watch:          watchSvc,               // 监听源管理页（列表/审批/添加/删除）
		Transfer:       transferRuntime,        // 四项传输并发的原子运行时配置
		CloudCfg:       cloudMgr,               // 云盘下载页（配置视图/保存）与补存端点
		CloudSink:      rcloneSink,             // 云盘目的地连通性测试通道
		CloudBackupKey: cfg.OAuthEncryptionKey, // 云盘备份候选服务端加密根密钥
		Version:        version,                // 构建期版本（总览页服务信息/备份元数据）
		ReleaseCheck:   releaseCheck,           // 检查更新（上游最新 Release 查询）
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

	// 每个机器人的 MTProto 大文件直传会话：与用户号会话、各 bot 的 Bot API
	// 长轮询并存；异常退出自动退避重启，未就绪时超过 Bot API 上限的媒体
	// 以确定性错误结束（小文件不受影响）。
	for id, client := range botClients {
		go func(id int64, client *mtproto.BotClient) {
			if err := client.Run(ctx); err != nil {
				logger.Error("Bot MTProto 会话循环退出", "bot_id", id, "error", err.Error())
			}
		}(id, client)
	}

	// 核心链路装配依赖集中到 app：MTProto 每次就绪（含重连）回调
	// onMTProtoReady 重建机器人池与队列消费，其余服务跨生命周期复用。
	a := &app{
		cfg:         cfg,
		log:         logger,
		st:          st,
		q:           q,
		access:      accessSvc,
		bindings:    bindingSvc,
		join:        joinSvc,
		watch:       watchSvc,
		hub:         hub,
		userClient:  m,
		pool:        pool,
		botClients:  botClients,
		bots:        bots,
		runtime:     runtime,
		profile:     profileLookup,
		membership:  membership,
		memoryGate:  memoryGate,
		transfer:    transferRuntime,
		progressReg: progressRegistry,
		cloud:       cloudMgr,
		cloudSink:   rcloneSink,
		botIdentity: botIdentity,
	}

	err = m.Run(ctx, a.onMTProtoReady)

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
