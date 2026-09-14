package mtproto

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/mdp/qrterminal/v3"
	"golang.org/x/term"

	"github.com/huaiminyetnotsleep/spore/internal/config"
)

// terminalAuth 终端交互式登录：验证码 + 可选 2FA 密码（免回显）。
// 实现 gotd auth.UserAuthenticator 接口；用于 LOGIN_MODE=phone 的验证码流程。
type terminalAuth struct {
	phone string
}

var stdin = bufio.NewReader(os.Stdin)

func (a terminalAuth) Phone(_ context.Context) (string, error) {
	return a.phone, nil
}

// Code 收取验证码后在终端提示输入。
func (a terminalAuth) Code(_ context.Context, sentCode *tg.AuthSentCode) (string, error) {
	fmt.Println("验证码已发送（请在 Telegram App 或短信中查收）。")
	fmt.Print("请输入验证码: ")
	line, err := stdin.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("读取验证码失败: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// Password 两步验证密码；账号未设置 2FA 时不会被调用。
func (a terminalAuth) Password(_ context.Context) (string, error) {
	fmt.Print("请输入两步验证密码（输入不回显）: ")
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("读取密码失败: %w", err)
	}
	return string(pw), nil
}

// SignUp 本项目只登录既有账号，不支持注册新账号。
func (a terminalAuth) SignUp(_ context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, fmt.Errorf("spore 不支持注册新账号，请使用已有 Telegram 用户账号登录")
}

// AcceptTermsOfService 无条件接受服务条款推送（gotd 登录流程要求实现；
// 仅新账号首次登录会触发，本项目既有账号场景不适用）。
func (a terminalAuth) AcceptTermsOfService(_ context.Context, _ tg.HelpTermsOfService) error {
	return nil
}

// login 按 LOGIN_MODE 分发登录流程：
// qr   —— 仅扫码；
// phone—— 仅手机号验证码（要求 TG_PHONE，config 已校验）;
// auto —— 优先扫码；扫码失败且配置了 TG_PHONE 时回退验证码流程。
//
// viaWeb 为 true 时（Web 触发的重连）只走扫码，且二维码经登录会话推送给
// 浏览器渲染——验证码/2FA 需要终端交互，Web 通道不可用；失败后回落
// "重启进程走终端登录"的降级路径。
func (c *Client) login(ctx context.Context, client *telegram.Client, viaWeb bool) error {
	if viaWeb {
		c.log.Info("进入 Web 扫码登录流程")
		if err := c.loginByQR(ctx, client, true); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// 只记分类信息，不记扫码 URL 等敏感值
			return fmt.Errorf("Web 扫码登录失败: %w（可重试，或重启进程走终端登录）", err)
		}
		return nil
	}

	// 回退策略在进入流程前一次性决议，错误分支只消费该决策
	fallbackToPhone := c.cfg.LoginMode == config.LoginModeAuto && c.cfg.TGPhone != ""

	tryQR := c.cfg.LoginMode == config.LoginModeQR || c.cfg.LoginMode == config.LoginModeAuto
	if tryQR {
		c.log.Info("未检测到有效会话，进入扫码登录流程")
		if err := c.loginByQR(ctx, client, false); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			c.log.Warn("扫码登录失败", "error", err.Error())
			if !fallbackToPhone {
				return fmt.Errorf("扫码登录失败: %w（可改用 LOGIN_MODE=phone 走验证码流程）", err)
			}
			fmt.Println("⚠ 扫码登录失败，回退到手机号验证码登录（原因见上方日志）")
		} else {
			return nil
		}
	}

	flow := auth.NewFlow(terminalAuth{phone: c.cfg.TGPhone}, auth.SendCodeOptions{})
	if err := flow.Run(ctx, client.Auth()); err != nil {
		return fmt.Errorf("验证码登录失败: %w", err)
	}
	return nil
}

// qrTokenRefreshInterval 扫码 token 有效期约 30 秒，到期前刷新重绘二维码。
const qrTokenRefreshInterval = 30 * time.Second

// loginByQR 扫码登录：导出 login token → 渲染二维码（终端或 Web）→
// 用户用已登录的 Telegram App「设置 → 设备 → 关联桌面设备」扫描确认 → 导入授权。
//
// viaWeb 选择二维码呈现方式：false 在终端渲染（原路径保持不变）；
// true 把登录 URL 推送到登录会话（c.sess），由 Web 管理端轮询展示。
// 两种模式都必须传 Options{Migrate: client.MigrateTo} 并处理 Export 侧的
// MigrationNeededError（docs/ops/troubleshooting.md 记录的坑：账号不在默认 DC 时
// 须迁移数据中心后重试；Import 侧迁移经 Migrate 回调由 qrlogin 内部处理）。
//
// 采用独立轮询循环而非 qrlogin.QR.Auth：后者的立即唤醒依赖 updates 信号，
// 而 spore 以 NoUpdates 模式运行；token 过期后重新 Export 同样能拿到结果——
// 已授权时 Export 返回空 token，随后 Import 完成导入。扫码全程无需手机号与验证码。
func (c *Client) loginByQR(ctx context.Context, client *telegram.Client, viaWeb bool) error {
	qr := qrlogin.NewQR(client.API(), c.cfg.TGAPIID, c.cfg.TGAPIHash, qrlogin.Options{
		Migrate: client.MigrateTo,
	})
	shown := false // 首个二维码渲染前输出操作指引（DC 迁移重试不应重置该状态）
	for {
		token, err := qr.Export(ctx)
		if err != nil {
			var mig *qrlogin.MigrationNeededError
			if errors.As(err, &mig) {
				if mErr := client.MigrateTo(ctx, mig.MigrateTo.DCID); mErr != nil {
					return fmt.Errorf("迁移数据中心 %d 失败: %w", mig.MigrateTo.DCID, mErr)
				}
				c.log.Info("已迁移至账号所在数据中心", "dc", mig.MigrateTo.DCID)
				continue // 迁移后重新导出 token
			}
			return fmt.Errorf("导出扫码登录令牌失败: %w", err)
		}
		// 空 token 表示另一端已完成确认
		if token.Empty() {
			if _, err := qr.Import(ctx); err != nil {
				return fmt.Errorf("导入扫码授权失败: %w", err)
			}
			return nil
		}

		if viaWeb {
			// 登录 URL 推送登录会话（敏感值：不落日志），浏览器端渲染二维码
			c.sess.setQR(token.URL())
		} else {
			if !shown {
				fmt.Println("═══════════════ 请扫码登录 ═══════════════")
				fmt.Println("打开手机 Telegram → 设置 → 设备 → 关联桌面设备，扫描下方二维码：")
				shown = true
			}
			fmt.Fprintln(os.Stdout)
			qrterminal.GenerateHalfBlock(token.URL(), qrterminal.L, os.Stdout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(qrTokenRefreshInterval):
			// token 到期前刷新；若期间已确认，下一轮 Export 返回空 token
		}
	}
}
