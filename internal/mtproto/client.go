// Package mtproto 封装两个 gotd/td MTProto 客户端：用户账号会话
// （登录、Peer 解析、取消息、刷新媒体引用）与 Bot 身份会话
// （媒体引用直发，见 bot.go / botsend.go）。
package mtproto

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/gotd/contrib/middleware/floodwait"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/transfercfg"
)

// Client 持有 gotd 客户端生命周期所需的配置与日志。
type Client struct {
	cfg      config.Config
	log      *slog.Logger
	sess     *Session // 登录会话状态与 Web 重连接口（New 时创建）
	transfer *transfercfg.Runtime
	updates  *ChannelUpdateBridge // update 分发桥（ready 作用域内 Bind，见 updates.go）
}

// New 创建客户端；登录会话对象随客户端创建，Web 层经 Session() 访问。
func New(cfg config.Config, log *slog.Logger) *Client {
	return &Client{cfg: cfg, log: log, sess: newSession(nil), updates: NewChannelUpdateBridge()}
}

// Session 返回登录会话对象（状态快照与 Web 触发重连）。
func (c *Client) Session() *Session { return c.sess }

// Updates 返回 update 分发桥（装配层在 ready 作用域内 Bind/Unbind）。
func (c *Client) Updates() *ChannelUpdateBridge { return c.updates }

// SetTransferRuntime 注入进程级传输并发快照；nil 时回退启动配置。
func (c *Client) SetTransferRuntime(r *transfercfg.Runtime) { c.transfer = r }

// Run 启动 MTProto 客户端循环：每轮运行一个完整的 client 生命周期
// （必要时登录 → ready 回调内启动 Bot/worker）。ready 返回或 ctx 结束时整体退出。
//
// 首轮登录保持终端扫码/验证码流程不变（降级路径）。
// 轮内异常退出（会话失效、网络错误等）不再终止进程：标记离线并等待
// Web 触发重连或进程退出——期间 Bot/worker 已随本轮 ctx 结束而停止，
// Web 管理端独立运行，可完成扫码重登。
//
// 重要：所有 MTProto 调用必须发生在 ready 回调作用域内（gotd 连接随 runOnce 存活），
// 因此 Bot 与 worker 都应在 ready 内启动；每轮重连都会重新执行一遍 ready。
func (c *Client) Run(ctx context.Context, ready func(ctx context.Context, api *tg.Client) error) error {
	for {
		viaWeb := c.sess.takeWebLogin() // Web 触发的重连使用 Web 扫码呈现；首轮恒为终端
		err := c.runOnce(ctx, ready, viaWeb)
		if ctx.Err() != nil {
			return nil
		}
		// 正常路径下 runOnce 只会在 ctx 结束后返回 nil；此处兜底把罕见的
		// "无错误提前返回"也纳入离线等待，避免状态机卡在无法重连的状态
		c.sess.setOffline(err)
		if err != nil {
			c.log.Error("MTProto 客户端异常退出，等待重连（可在 Web 管理端触发，或重启进程走终端登录）",
				"error", err.Error())
		} else {
			c.log.Warn("MTProto 客户端提前结束，进入离线等待重连")
		}
		if !c.sess.waitRelogin(ctx) {
			return nil
		}
	}
}

// runOnce 运行一个 client 生命周期：原 Run 的主体，附加登录会话状态埋点。
func (c *Client) runOnce(ctx context.Context, ready func(ctx context.Context, api *tg.Client) error, viaWeb bool) error {
	sessionPath := filepath.Join(c.cfg.DataDir, "session.json")

	// FLOOD_WAIT 自动等待重试（docs/reference/architecture.md §3.2）。
	// 用 SimpleWaiter 而非调度版 Waiter：后者每次 invoke 走调度链（~0.5ms 延迟 +
	// 互斥锁/堆开销）、把全部调用串行化（相册并发下载退化单路），且常驻 1kHz ticker；
	// SimpleWaiter 直接透传、遇 FLOOD_WAIT 才内联睡，正适合本项目低并发形态。
	waiter := floodwait.NewSimpleWaiter().WithMaxRetries(5)
	client := telegram.NewClient(c.cfg.TGAPIID, c.cfg.TGAPIHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
		// 轻量消费 update：只提取批次里携带的频道对象（被拉入频道的实时
		// 归档入口），不做 updates 状态管理；漏收由 joinmgr 惰性对账兜底。
		UpdateHandler: c.updates,
	})

	return client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return fmt.Errorf("查询登录状态失败: %w", err)
		}
		if !status.Authorized {
			c.sess.setLoginPending(viaWeb)
			if err := c.login(ctx, client, viaWeb); err != nil {
				return err
			}
			// 只记路径，不记任何会话内容
			c.log.Info("登录成功，会话已保存", "path", sessionPath)
		} else {
			c.log.Info("会话有效，静默登录")
		}
		c.sess.setReady()

		// 传输连接池：并发分片线程不再全部复用单条 TCP 连接（单连接吞吐
		// 典型 15–30MB/s，是带宽富余机器的传输瓶颈），下载与 fetch 按并发
		// 需求惰性开连至 DownloadConnections 条，空闲时仍只有约 1 条。
		// redirectInvoker 补回直连路径内建的 *_MIGRATE 处理（跨 DC 文件的
		// FILE_MIGRATE_X 重定向到目标 DC 池，见 redirect.go）；floodwait
		// 包装在最外层：FLOOD_WAIT 等待不占用任何池连接。
		// 物理池固定上限 16，动态 gate 负责运行时连接并发；gotd pool
		// 惰性开连，因此不会启动即建立 16 条连接。
		pool, err := client.Pool(maxTransferConnections)
		if err != nil {
			return fmt.Errorf("创建下载连接池失败: %w", err)
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
				return c.transfer.Snapshot().DownloadConnections
			}
			return c.cfg.DownloadConnections
		}
		gate := newTransferGate(redir, limit)
		// 经 floodwait 中间件包装后的 API：调用发生 FLOOD_WAIT 时自动等待重试。
		api := tg.NewClient(waiter.Handle(gate))
		return ready(ctx, api)
	})
}

// Me 以当前会话身份调 users.getUsers，返回账号显示名（@username），
// 供 /whoami 验证 MTProto 通道可用。
func Me(ctx context.Context, api *tg.Client) (string, error) {
	users, err := api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUserSelf{}})
	if err != nil {
		return "", err
	}
	for _, uc := range users {
		u, ok := uc.(*tg.User)
		if !ok {
			continue
		}
		name := u.FirstName
		if u.LastName != "" {
			name += " " + u.LastName
		}
		if u.Username != "" {
			name += " @" + u.Username
		}
		return name, nil
	}
	return "", errors.New("users.getUsers 未返回可用的用户对象")
}
