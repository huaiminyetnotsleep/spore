// Bot 身份的第二个 gotd 客户端：以现有 BOT_TOKEN 登录 MTProto，
// 专职大文件直传（见 botsend.go）；入站更新仍走 Bot API 长轮询。

package mtproto

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/transfercfg"
)

// BotClient 是以 Bot 身份登录的 MTProto 客户端。与用户账号会话
// （client.go 的 Client）并存：用户号负责读源频道，Bot 会话负责把
// 超过 Bot API 上限的大文件直接上传发给目标用户。
const (
	BotStateOffline = "offline"
	BotStateReady   = "ready"
)

// BotStatusSnapshot 是 Bot MTProto 会话的脱敏状态快照，供 Web 管理端展示。
// DCID 为当前主会话数据中心；0 表示尚未取得或当前离线。
type BotStatusSnapshot struct {
	State     string
	DCID      int
	UpdatedAt int64
}

type BotClient struct {
	cfg      config.Config
	log      *slog.Logger
	transfer *transfercfg.Runtime

	mu        sync.Mutex
	api       *tg.Client // 就绪后非 nil；掉线清空（peers 缓存保留）
	state     string
	dcID      int
	updatedAt int64
	peers     map[int64]int64 // userID → accessHash，bot 特权解析所得，跨重连稳定
}

// NewBotClient 创建 Bot 会话客户端；Run 启动后经 Available/Send* 使用。
func NewBotClient(cfg config.Config, log *slog.Logger) *BotClient {
	return &BotClient{cfg: cfg, log: log, state: BotStateOffline, updatedAt: time.Now().UnixMilli(), peers: map[int64]int64{}}
}

// SetTransferRuntime 注入进程级传输并发快照；nil 时回退启动配置。
func (c *BotClient) SetTransferRuntime(r *transfercfg.Runtime) { c.transfer = r }

// Run 启动 Bot 会话循环：登录 → 保持连接；异常退出按指数退避自动重启。
// 与用户会话"离线等待 Web 扫码"不同——Bot 登录非交互（token 换取会话），
// 无需人工介入。ctx 结束时返回 nil。
func (c *BotClient) Run(ctx context.Context) error {
	const initialBackoff, maxBackoff = 5 * time.Second, 5 * time.Minute
	backoff := initialBackoff
	for {
		start := time.Now()
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		// 会话曾稳定运行过一段时间：视为全新故障，退避重置
		if time.Since(start) > time.Minute {
			backoff = initialBackoff
		}
		c.log.Error("Bot MTProto 会话异常退出，退避后自动重启",
			"error", err.Error(), "backoff", backoff.String())
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runOnce 运行一个完整生命周期：必要时 Bot 登录，就绪后保持连接直至 ctx 结束。
func (c *BotClient) runOnce(ctx context.Context) error {
	sessionPath := filepath.Join(c.cfg.DataDir, "bot-session.json")

	// 与用户会话同款 FLOOD_WAIT 处理（client.go §runOnce 的选型说明）。
	waiter := floodwait.NewSimpleWaiter().WithMaxRetries(5)
	client := telegram.NewClient(c.cfg.TGAPIID, c.cfg.TGAPIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
		// 会话只发不收（大文件直传）：关闭更新处理，业务入站更新
		// 仍走 Bot API 长轮询，两个会话互不干扰。
		NoUpdates: true,
		// 关闭出站 gzip：gotd 对超过阈值（默认 1KB）的出站 payload 无条件
		// 压缩（即使压不小也照发），512KB saveBigFilePart 分片是不可压缩的
		// 媒体数据——每上传 1GB 纯浪费约 10–20s CPU。下载响应是否 gzip 由
		// 服务端决定，不受该开关影响；用户会话保持默认（fetch RPC 小、
		// 偶尔受益于压缩）。
		CompressThreshold: -1,
	})

	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("查询 Bot 登录状态失败: %w", err)
		}
		if !status.Authorized {
			if _, err := client.Auth().Bot(ctx, c.cfg.BotToken); err != nil {
				return fmt.Errorf("Bot 登录失败: %w", err)
			}
			c.log.Info("Bot MTProto 会话登录成功", "path", sessionPath)
		} else {
			c.log.Info("Bot MTProto 会话有效，静默登录")
		}

		// 上传连接池：并发分片线程各走独立 TCP 连接（用户会话同款说明见
		// client.go），惰性开连至 UploadConnections 条；redirectInvoker 补回
		// 直连路径内建的 *_MIGRATE 处理（跨 DC 上传目标同样会 FILE_MIGRATE）；
		// floodwait 包装在最外层，FLOOD_WAIT 等待不占用任何池连接。池与 api
		// 同生命周期：回调返回（掉线/ctx 结束）时先 setOffline 再收池。
		pool, err := client.Pool(maxTransferConnections)
		if err != nil {
			return fmt.Errorf("创建上传连接池失败: %w", err)
		}
		redir := &redirectInvoker{
			home: pool,
			createSub: func(ctx context.Context, dc int) (telegram.CloseInvoker, error) {
				return client.DC(ctx, dc, maxTransferConnections)
			},
			fallback: client.API().Invoker(),
			log:      c.log,
			subs:     map[int]telegram.CloseInvoker{},
		}
		defer redir.closeAll()
		limit := func() int {
			if c.transfer != nil {
				return c.transfer.Snapshot().UploadConnections
			}
			return c.cfg.UploadConnections
		}
		gate := newTransferGate(redir, limit)
		api := tg.NewClient(waiter.Handle(gate))
		c.setReady(api, client.Config().ThisDC)
		defer c.setOffline()
		<-ctx.Done() // 无需在回调内常驻业务：发送方经 Available 守门后调用
		return nil
	})
}

// Available 报告 Bot 会话当前是否可发起 RPC；delivery 路由据此决定大文件直传可用性。
func (c *BotClient) Available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.api != nil
}

// current 返回就绪的 API 客户端；未就绪时第二个返回值为 false。
func (c *BotClient) current() (*tg.Client, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.api, c.api != nil
}

func (c *BotClient) setReady(api *tg.Client, dcIDs ...int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.api = api
	c.state = BotStateReady
	c.dcID = 0
	if len(dcIDs) > 0 {
		c.dcID = dcIDs[0]
	}
	c.updatedAt = time.Now().UnixMilli()
}

// Status 返回 Bot 会话的脱敏状态，不暴露客户端或 Session 内容。
func (c *BotClient) Status() BotStatusSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return BotStatusSnapshot{State: c.state, DCID: c.dcID, UpdatedAt: c.updatedAt}
}

// setOffline 清空 api 使发送方立即观察到不可用；peers 缓存保留（hash 长期稳定）。
func (c *BotClient) setOffline() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.api = nil
	c.state = BotStateOffline
	c.dcID = 0
	c.updatedAt = time.Now().UnixMilli()
}
