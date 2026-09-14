package botapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"golang.org/x/text/language"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/joinmgr"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// Access 是访问控制服务（access.Service）在 Bot 侧所需的最小接口；
// 以接口注入便于 handler 测试用假实现替换。
type Access interface {
	// Submit 执行六步校验链，通过时写 requests 并入内存队列。
	Submit(ctx context.Context, in access.Submission) (access.Decision, error)
	// UserDownloadStatus 是 /download 提交前的轻量用户预检（Submit 第一步的
	// 只读形态 + 用户级云盘下载权限维度），返回状态/权限拒绝码（空串=允许
	// 该维度）；/download 借此在进入功能分支前给未授权用户回与裸链接相同
	// 的申请引导，无下载权限的授权用户回专门文案。
	UserDownloadStatus(ctx context.Context, userID int64) (apperr.Code, error)
	// HandleStart 处理 /start 的申请落库与状态分流。
	HandleStart(ctx context.Context, in access.StartInput) (access.StartOutcome, error)
	// Usage 查询当日额度使用（/usage 命令数据源；频道指令用它做状态校验）。
	Usage(ctx context.Context, userID int64) (access.Usage, error)
	// CancelOwnByLink 取消该用户名下与链接匹配的在途任务，返回实际取消数（/cancel 命令）。
	CancelOwnByLink(ctx context.Context, userID int64, ref tmeurl.SourceRef) (int, error)
}

// CloudStatus 是云盘下载功能（/download）在 Bot 侧所需的最小状态接口。
// 由装配层组合 cloudarchive.Manager 与 rclone 探测实现（轻量 adapter）；
// 以接口注入便于 handler 测试与功能开关判定。配置里的凭据不出现在任何
// 返回值中（只有名称与开关状态）。
type CloudStatus interface {
	// Enabled 报告云盘下载全局开关（cloud-drive.json 的 enabled）。
	Enabled() bool
	// Available 报告 rclone 二进制当前是否可用（缺失时 /download 回暂不可用）。
	Available() bool
	// DefaultDestination 返回默认目的地名称（未配置时为空串）。
	DefaultDestination() string
	// DestinationEnabled 报告名称对应的目的地是否存在且已启用。
	DestinationEnabled(name string) bool
	// EnabledDestinations 返回全部已启用目的地名称（目的地无效时回可用列表）。
	EnabledDestinations() []string
}

// Channels 是频道绑定服务（binding.Service）在 Bot 侧所需的最小接口。
// 绑定校验、归属限制（只能操作自己的绑定）与审计都在服务内完成。
type Channels interface {
	// BindBot 为用户绑定一个频道（校验机器人为该频道管理员且有发言权限）。
	BindBot(ctx context.Context, userID int64, target string) (store.ChannelBinding, error)
	// UnbindBot 解除该用户的频道绑定；目标不存在或不属于该用户返回 store.ErrNotFound。
	UnbindBot(ctx context.Context, userID int64, target string) (store.ChannelBinding, error)
	// ListByUser 返回该用户名下的全部绑定。
	ListByUser(ctx context.Context, userID int64) ([]store.ChannelBinding, error)
}

// ChannelJoin 是频道加入服务（joinmgr.Service）在 Bot 侧所需的最小接口。
// 提交分流（owner 即时 / 普通用户审核）、上限与开关校验都在服务内完成。
type ChannelJoin interface {
	Submit(ctx context.Context, userID int64, isOwner bool, text string) (joinmgr.SubmitOutcome, error)
}

// OwnerCheck 判定提交者是否号主（users 表全局唯一 owner）；未设 owner 时
// 由装配方决定回退策略。nil 时 /join 对所有人按非 owner 处理。
type OwnerCheck func(ctx context.Context, userID int64) (bool, error)

// RuntimeStatus 是 Bot 命令使用的脱敏运行状态快照。
// 不包含 token、session、二维码 URL、文件路径或底层错误文本。
type RuntimeStatus struct {
	MTProtoState   string
	LargeFileReady bool // 大文件直传通道（Bot 号 MTProto 会话）是否就绪
	StoreReady     bool
	QueueLen       int
	QueueCap       int
	Processing     int
	WorkerCount    int
}

// StatusProvider 提供 Bot 命令所需的只读运行状态。
type StatusProvider interface {
	Status(context.Context) (RuntimeStatus, error)
}

// StatusFunc 将函数适配为 StatusProvider，便于启动装配和单元测试。
type StatusFunc func(context.Context) (RuntimeStatus, error)

// Status implements StatusProvider.
func (f StatusFunc) Status(ctx context.Context) (RuntimeStatus, error) { return f(ctx) }

// Options 聚合 Bot 所需依赖。
type Options struct {
	Cfg    config.Config
	Log    *slog.Logger
	Queue  *queue.Queue // 必填：链接提取任务队列
	Access Access       // 必填：访问控制服务（白名单与额度由数据库接管）
	// Channels 提供频道绑定指令能力（binding.Service）；nil 时相关指令回复不可用。
	Channels Channels
	// ChannelJoin 提供频道加入指令能力（joinmgr.Service）；nil 时 /join 回复不可用。
	ChannelJoin ChannelJoin
	// IsOwner 判定 /join 提交者是否号主（owner 即时加入，普通用户走审核）。
	IsOwner OwnerCheck
	// CloudStatus 提供云盘下载功能状态与目的地信息（/download 准入预检）；
	// nil 时 /download 回复不可用。
	CloudStatus CloudStatus
	// CleanChatCommands 可选：私聊 /start 回复后尽力清理当前 chat 的命令覆盖。
	// 参数为实际 chat ID 与用户原始语言标签；nil 时使用实例级去重、限时实现。
	CleanChatCommands func(ctx context.Context, chatID int64, languageCode string)
	// WrapSender 可选地包装每次 update 使用的发送器（如事件统计包装）。
	// 包装器不得改变 Sender 的错误与资源语义。
	WrapSender func(delivery.Sender) delivery.Sender
	// SystemName 提供可配置的系统名称（internal/syscfg，settings 即时生效）；
	// nil 或返回空串时回退 syscfg.DefaultName（帮助与欢迎文案用）。
	SystemName      func(ctx context.Context) string
	Whoami          func(ctx context.Context) (string, error) // MTProto 就绪后注入，供 /whoami 验证
	Status          StatusProvider                            // 可选：供 /status 与 /health 查询运行状态
	ProfileObserver func(models.User)                         // 可选：记录已与 Bot 交互的用户资料上下文
}

// New 创建 Bot 实例。Sender 由 handler 按 update 即时构造（telegramSender
// 无状态，仅持 bot 与配置），避免"Sender 需要 bot、handler 需要 Sender"的
// 构造顺序纠缠；worker 所需的 Sender 由 main 用同一 delivery.New 单独构造。
// 调用方随后自行 b.Start(ctx)；须保证在 MTProto 就绪后启动。
func New(opt Options) (*tgbot.Bot, error) {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.Queue == nil {
		return nil, errors.New("botapi: Queue 为必填项")
	}
	if opt.Access == nil {
		return nil, errors.New("botapi: Access 为必填项")
	}

	// 只订阅 message 更新（对齐旧 TS 版 allowed_updates），减少无关流量与解析开销
	allowed := tgbot.AllowedUpdates{"message"}
	opts := []tgbot.Option{
		tgbot.WithDefaultHandler(updateHandler(opt)),
		tgbot.WithAllowedUpdates(allowed),
		// 库默认 http.Client 总超时 60s，会掐断大文件上传（慢上行 30MB 即可超时）；
		// 改为无总超时（时长由调用方 ctx 控制，任务级 15 分钟），长轮询超时保持默认 60s
		tgbot.WithHTTPClient(time.Minute, &http.Client{}),
	}
	// 本地 Bot API 服务器（--local 模式，可选路线）：Bot API 上传上限 50MB → MaxFileSize；
	// 未配置时超过 50MB 的媒体由 worker 侧路由到 Bot 号 MTProto 大文件直传
	if opt.Cfg.BotAPIURL != "" {
		opts = append(opts, tgbot.WithServerURL(opt.Cfg.BotAPIURL))
	}
	b, err := tgbot.New(opt.Cfg.BotToken, opts...)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// RegisterCommands 将公开命令写入 Telegram 菜单。失败由调用方记录并降级，
// 不应阻断 Bot 长轮询，因为命令文本分流仍然可用。
func RegisterCommands(ctx context.Context, b *tgbot.Bot) error {
	if b == nil {
		return errors.New("botapi: Bot 为空")
	}
	_, err := b.SetMyCommands(ctx, &tgbot.SetMyCommandsParams{
		Commands: []models.BotCommand{
			{Command: "start", Description: "申请使用或查看账号状态"},
			{Command: "help", Description: "查看帮助"},
			{Command: "status", Description: "查看服务运行状态"},
			{Command: "health", Description: "查看服务健康状态"},
			{Command: "usage", Description: "查看今日额度"},
			{Command: "cancel", Description: "取消下载/上传任务"},
			{Command: "download", Description: "下载消息媒体到网盘"},
			{Command: "bind", Description: "绑定我的频道（需先把我设为频道管理员）"},
			{Command: "unbind", Description: "解绑我的频道"},
			{Command: "channels", Description: "查看我绑定的频道"},
			{Command: "join", Description: "请系统账号加入私有频道（t.me/+ 邀请链接）"},
		},
	})
	return err
}

const commandCleanupTimeout = 2 * time.Second

type commandScopeKey struct {
	chatID   int64
	language string
}

// states 中 false 表示请求进行中，true 表示已成功；失败删除，留待下次 /start。
// 每个 updateHandler（即 Bot 实例）拥有独立状态，网络调用不持锁。
type commandCleaner struct {
	mu     sync.Mutex
	states map[commandScopeKey]bool
}

func commandLanguage(code string) string {
	if code == "" {
		return ""
	}
	tag, err := language.Parse(code)
	if err != nil {
		return ""
	}
	// Raw 不推测 und、仅地区或私用标签的语言。
	base, _, _ := tag.Raw()
	result := base.String()
	if len(result) != 2 || result[0] < 'a' || result[0] > 'z' || result[1] < 'a' || result[1] > 'z' {
		return ""
	}
	return result
}

func (c *commandCleaner) clean(ctx context.Context, b *tgbot.Bot, log *slog.Logger, chatID int64, code string) {
	ctx, cancel := context.WithTimeout(ctx, commandCleanupTimeout)
	defer cancel()
	codes := []string{""}
	if normalized := commandLanguage(code); normalized != "" {
		codes = append(codes, normalized)
	}
	for _, code := range codes {
		if ctx.Err() != nil {
			return
		}
		key := commandScopeKey{chatID: chatID, language: code}
		c.mu.Lock()
		_, claimed := c.states[key]
		if !claimed {
			c.states[key] = false
		}
		c.mu.Unlock()
		if claimed {
			continue
		}
		err := deleteChatCommands(ctx, b, chatID, code)
		c.mu.Lock()
		if err == nil {
			c.states[key] = true
		} else {
			delete(c.states, key)
		}
		c.mu.Unlock()
		if err != nil {
			category := "chat"
			if code != "" {
				category = "language"
			}
			// SDK 错误可能包含带 token 的请求 URL，绝不记录原始错误或标签。
			log.Warn("清理命令覆盖失败", "category", category, "chat_id", chatID, "language_code", code)
		}
	}
}

func deleteChatCommands(ctx context.Context, b *tgbot.Bot, chatID int64, code string) error {
	ok, err := b.DeleteMyCommands(ctx, &tgbot.DeleteMyCommandsParams{
		Scope:        &models.BotCommandScopeChat{ChatID: chatID},
		LanguageCode: code,
	})
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("botapi: 删除命令未成功")
	}
	return nil
}

// senderFor 为单次 update 构造 Sender（含图片上限等配置）。
func senderFor(opt Options, b *tgbot.Bot) delivery.Sender {
	snd := delivery.New(b, delivery.Config{PhotoLimit: opt.Cfg.PhotoLimit})
	if opt.WrapSender != nil {
		return opt.WrapSender(snd)
	}
	return snd
}
