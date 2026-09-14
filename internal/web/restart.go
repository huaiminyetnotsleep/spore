package web

import (
	"context"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// restartUnavailableText 是重启通道未接入时的受控提示。
const restartUnavailableText = "当前运行环境未接入受控重启，请由运行环境手动重启进程。"

// restartConfirmValue 是二次确认字段的固定取值（不执行 Shell，不接受任意参数）。
const restartConfirmValue = "restart"

// restartReason 返回本次重启的固定审计原因（含待导入备份时说明用途）。
func (s *Server) restartReason() string {
	if marker, err := readPendingMarker(s.cfg.DataDir); err == nil && marker == "confirmed" {
		return "应用待导入备份"
	}
	return "管理员手动触发"
}

// scheduleRestart 让响应有机会写出后触发受控重启（/api/v1/restart 的核心）：
// 只调用注入的 RestartFunc（生产实现只发送 SIGTERM），不执行 Shell。
// 同一 Server 生命周期内只接受一次调度，避免重复提交产生多个信号或审计。
func (s *Server) scheduleRestart(reason string) bool {
	s.restartMu.Lock()
	if s.restartScheduled {
		s.restartMu.Unlock()
		return false
	}
	s.restartScheduled = true
	fn := s.restartFunc
	s.restartMu.Unlock()

	go func() {
		// 让响应有机会写出，随后由进程信号处理器执行正常 drain。
		time.Sleep(50 * time.Millisecond)
		if err := fn(); err != nil {
			s.restartMu.Lock()
			s.restartScheduled = false
			s.restartMu.Unlock()
			s.log.Error("触发受控重启失败", "error", err.Error())
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			s.audit(ctx, "admin.restart.failed", "process", map[string]any{
				"reason": reason, "triggered": false})
			cancel()
		}
	}()
	return true
}

func readPendingMarker(dataDir string) (string, error) {
	m, err := store.ReadPendingImport(dataDir)
	if err != nil {
		return "", err
	}
	return m.Status, nil
}
