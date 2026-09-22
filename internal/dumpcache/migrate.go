package dumpcache

// migrate.go — 缓存频道迁移工具：把旧缓存频道（含升级前 dump_channel_id=0
// 的存量，由调用方折算源频道）中仍可读的副本整批复制到当前缓存频道，
// 免去逐链接重新提取。管理端在切换缓存频道后触发（POST /api/v1/dumpcache/
// migrate），后台执行、进度可查；重启即停但幂等可续——已迁移行已回写
// 新频道归属与新消息 ID，重跑从断点继续。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// MigrateProgress 是迁移任务的可序列化进度快照（Web 轮询展示）。
type MigrateProgress struct {
	Running    bool   `json:"running"`
	From       int64  `json:"from"`
	To         int64  `json:"to"`
	Total      int64  `json:"total"`
	Done       int64  `json:"done"`    // 已复制并回写
	Failed     int64  `json:"failed"`  // 单条失败（消息已删/网络），行保持原值不命中、无害
	Skipped    int64  `json:"skipped"` // 目标频道已有现行条目（含重复提交），旧行已删
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	LastError  string `json:"last_error,omitempty"` // 整体中止原因（如源频道不可读）
}

// migrateBatchSize 是每页扫描与每批复制的条目上限；批间 pacing 避免打满
// Bot API 配额（429 重试由 delivery 层兜底）。
const (
	migrateBatchSize = 50
	migratePacing    = 800 * time.Millisecond
)

// StartMigrate 启动一次迁移（后台 goroutine，一次仅一个）：把 from 频道的
// 现行格式条目复制到当前缓存频道。拒绝条件返回受控错误：未配置缓存频道、
// 源频道即当前频道、已有迁移在跑。
func (s *Service) StartMigrate(ctx context.Context, from int64) error {
	to, ok := s.Channel()
	if !ok {
		return errors.New("未配置缓存频道，无法迁移")
	}
	if from == 0 {
		return errors.New("源频道 ID 无效")
	}
	if from == to {
		return fmt.Errorf("源频道（%d）即当前缓存频道，无需迁移", from)
	}
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()
	if s.migrate != nil && s.migrate.Running {
		return errors.New("已有缓存迁移进行中，请等待完成")
	}
	total, err := s.st.CountDumpEntriesByChannel(ctx, from)
	if err != nil {
		return fmt.Errorf("统计源频道条目失败: %w", err)
	}
	// 后台执行：ctx 由调用方去取消化（WithoutCancel），重启即停、幂等可续
	runCtx := context.WithoutCancel(ctx)
	s.migrate = &MigrateProgress{
		Running: true, From: from, To: to, Total: total, StartedAt: time.Now().UnixMilli(),
	}
	go s.runMigrate(runCtx, *s.migrate)
	return nil
}

// MigrateProgress 返回当前进度快照。
func (s *Service) MigrateProgress() MigrateProgress {
	s.migrateMu.Lock()
	defer s.migrateMu.Unlock()
	if s.migrate == nil {
		return MigrateProgress{}
	}
	return *s.migrate
}

// runMigrate 执行迁移主体：按 id 升序分页扫描现行格式条目，逐条判定
// （目标已有现行条目 → 跳过并删旧行）→ CopyMessages 复制 → 回写新频道
// 归属与新消息 ID。源频道整体不可读（chat not found/deactivated/banned）
// 时中止——剩余条目由 WriteClean 自愈或换源重试；单条失败不中断。
func (s *Service) runMigrate(ctx context.Context, start MigrateProgress) {
	progress := start
	finish := func(reason string) {
		s.migrateMu.Lock()
		progress.Running = false
		progress.FinishedAt = time.Now().UnixMilli()
		progress.LastError = reason
		s.migrate = &progress
		s.migrateMu.Unlock()
		if reason != "" {
			s.log.Warn("缓存迁移中止", "from", progress.From, "to", progress.To, "reason", reason)
		} else {
			s.log.Info("缓存迁移完成", "from", progress.From, "to", progress.To,
				"done", progress.Done, "failed", progress.Failed, "skipped", progress.Skipped)
		}
	}
	snap := func() {
		s.migrateMu.Lock()
		s.migrate = &progress
		s.migrateMu.Unlock()
	}

	var afterID int64
	for {
		if ctx.Err() != nil {
			finish("执行中断（服务重启），可重新发起继续")
			return
		}
		page, err := s.st.ListDumpEntriesByChannel(ctx, progress.From, afterID, migrateBatchSize)
		if err != nil {
			finish(fmt.Sprintf("读取源频道条目失败: %v", err))
			return
		}
		if len(page) == 0 {
			finish("")
			return
		}
		for _, e := range page {
			afterID = e.ID
			// 目标频道已有同链接现行条目（此前切换后自愈写入）：跳过并清理旧行
			if _, err := s.st.LatestDumpEntry(ctx, e.ChannelKey, e.MessageID, progress.To); err == nil {
				if err := s.st.DeleteDumpEntry(ctx, e.ID); err == nil {
					progress.Skipped++
				}
				continue
			} else if !errors.Is(err, store.ErrNotFound) {
				progress.Failed++
				continue
			}
			ids, err := s.senderFor(0).CopyMessages(ctx, progress.From, progress.To, e.DumpIDs)
			if err != nil {
				if delivery.IsChannelGoneError(err) {
					finish("源频道不可读（已删除或封禁），剩余条目将由复用自愈重建")
					return
				}
				// 单条失败（消息被删/瞬时故障）：行保持原值（不命中，无害）
				progress.Failed++
				continue
			}
			if err := s.st.UpdateDumpEntryCopy(ctx, e.ID, progress.To, ids); err != nil {
				progress.Failed++
				continue
			}
			progress.Done++
		}
		snap()
		time.Sleep(migratePacing)
	}
}
