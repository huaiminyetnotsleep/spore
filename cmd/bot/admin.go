// 管理子命令：不启动 Bot 的自证与恢复入口，执行后直接退出。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/huaiminyetnotsleep/spore/internal/backup"
	"github.com/huaiminyetnotsleep/spore/internal/branding"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
	"github.com/huaiminyetnotsleep/spore/internal/web"
	"time"
)

// runAdminCommand 执行管理子命令后退出；目前支持 version（输出版本号）、
// admin reset-key（重新生成访问密钥，旧密钥与全部会话立即失效，动作留审计）
// 与 admin backup [--output PATH]（生成数据库一致性备份并轮转）。
func runAdminCommand(args []string) {
	if len(args) == 1 && args[0] == "version" {
		fmt.Println("spore", version)
		return
	}
	if len(args) == 2 && args[0] == "admin" && args[1] == "reset-key" {
		adminResetKey()
		return
	}
	if len(args) >= 2 && args[0] == "admin" && args[1] == "backup" {
		adminBackup(args[2:])
		return
	}
	fmt.Fprintln(os.Stderr, "未知子命令。用法：spore version | spore admin reset-key | spore admin backup [--output PATH]")
	os.Exit(2)
}

// adminBackup 生成数据库一致性备份：默认落 data/backups 并按 syscfg 的
// backup_keep_count 轮转（缺省保留 8 份）；--output 指定精确路径时不轮转。
// 与 reset-key 同款：只依赖数据目录与数据库，不要求 Telegram 凭据，
// 容器内可经 docker compose exec bot spore admin backup 使用。
func adminBackup(rest []string) {
	output := ""
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--output" && i+1 < len(rest) {
			output = rest[i+1]
			i++
			continue
		}
		fmt.Fprintf(os.Stderr, "无法识别的参数 %q\n", rest[i])
		os.Exit(2)
	}
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

	keep := syscfg.LoadBackupKeepCount(ctx, st)
	res, err := backup.Run(ctx, st, cfg.DataDir, output, keep, "cli", time.Now(), logger)
	if err != nil {
		logger.Error("备份失败", "error", err.Error())
		os.Exit(1)
	}
	fmt.Printf("%s (%d bytes)\n", res.Path, res.SizeBytes)
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
