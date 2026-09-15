// 管理子命令：不启动 Bot 的自证与恢复入口，执行后直接退出。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/web"
)

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
