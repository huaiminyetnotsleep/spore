package dumpcache

// migrate.go — 缓存频道迁移工具：把旧缓存频道（含升级前 dump_channel_id=0
// 的存量，由调用方折算源频道）中仍可读的副本整批复制到当前全部启用的
// 缓存频道，免去逐链接重新提取。管理端在新增/切换缓存频道后触发
//（POST /api/v1/dumpcache/migrate），后台执行、进度可查；重启即停但幂等
// 可续——已复制的目标频道已有现行条目，重跑自动跳过、从断点继续。

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
	Running    bool    `json:"running"`
	From       int64   `json:"from"`
	To         []int64 `json:"to"`      // 目标启用频道（发起时快照，源频道除外）
	Total      int64   `json:"total"`   // 源频道条目总数
	Done       int64   `json:"done"`    // 已复制齐全部待补目标并清理源行的条目数
	Failed     int64   `json:"failed"`  // 存在复制失败的条目数（消息已删/网络），行保持原值不命中、无害
	Skipped    int64   `json:"skipped"` // 目标频道均已有现行条目（含重复提交）的条目数
	StartedAt  int64   `json:"started_at"`
	FinishedAt int64   `json:"finished_at,omitempty"`
	LastError  string  `json:"last_error,omitempty"` // 整体中止原因（如源频道不可读）
}

// migrateBatchSize 是每页扫描与每批复制的条目上限；批间 pacing 避免打满
// Bot API 配额（429 重试由 delivery 层兜底）。
const (
	migrateBatchSize = 50
	migratePacing    = 800 * time.Millisecond
)

// StartMigrate 启动一次迁移（后台 goroutine，一次仅一个）：把 from 频道的
// 现行格式条目复制到全部启用的缓存频道（from 本身除外——它若仍启用，其
// 条目本就有效）。拒绝条件返回受控错误：无启用缓存频道、源频道 ID 无效、
// 源频道即全部启用频道、已有迁移在跑。
func (s *Service) StartMigrate(ctx context.Context, from int64) error {
	channels, ok := s.Channels()
	if !ok {
		return errors.New("未配置启用的缓存频道，无法迁移")
	}
	if from == 0 {
		return errors.New("源频道 ID 无效")
	}
	targets := make([]int64, 0, len(channels))
	sourceEnabled := false
	for _, c := range channels {
		if c == from {
			sourceEnabled = true
			continue
		}
		targets = append(targets, c)
	}
	if len(targets) == 0 {
		return fmt.Errorf("源频道（%d）即全部启用的缓存频道，无需迁移", from)
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
		Running: true, From: from, To: targets, Total: total, StartedAt: time.Now().UnixMilli(),
	}
	go s.runMigrate(runCtx, *s.migrate, sourceEnabled)
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

// runMigrate 执行迁移主体：按 id 升序分页扫描源频道现行格式条目，逐条对
// 每个尚无现行条目的目标频道复制并落新行；全部目标就绪（或本就无需复制）
// 且源频道不在启用集合时删除源行（源频道仍启用则其条目继续有效，保留）。
// 源频道整体不可读（chat not found/deactivated/banned）时中止——剩余条目
// 由 WriteClean 自愈或换源重试；单条/单目标失败不中断。
func (s *Service) runMigrate(ctx context.Context, start MigrateProgress, sourceEnabled bool) {
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
			// 逐目标频道查现行条目（此前自愈写入或已迁移）：只补缺失的频道
			pending := make([]int64, 0, len(progress.To))
			for _, to := range progress.To {
				if _, err := s.st.LatestDumpEntry(ctx, e.ChannelKey, e.MessageID, to); err == nil {
					continue
				} else if !errors.Is(err, store.ErrNotFound) {
					progress.Failed++
					continue
				}
				pending = append(pending, to)
			}
			if len(pending) == 0 {
				// 全部启用频道都已有现行条目：源行不在启用集合时清理
				if !sourceEnabled {
					if err := s.st.DeleteDumpEntry(ctx, e.ID); err == nil {
						progress.Skipped++
					}
					continue
				}
				progress.Skipped++
				continue
			}
			allOK := true
			for _, to := range pending {
				ids, err := s.senderFor(0).CopyMessages(ctx, progress.From, to, e.DumpIDs)
				if err != nil {
					if delivery.IsChannelGoneError(err) {
						finish("源频道不可读（已删除或封禁），剩余条目将由复用自愈重建")
						return
					}
					// 单目标失败（目标频道失权/瞬时故障）：该频道不落行，
					// 源行保持原值（源频道已停用时该条目暂不命中，无害）
					progress.Failed++
					allOK = false
					continue
				}
				if _, err := s.st.InsertDumpEntry(ctx, store.DumpEntry{
					ChannelKey: e.ChannelKey, MessageID: e.MessageID, DumpIDs: ids,
					DumpChannelID: to, CreatedAt: e.CreatedAt,
				}); err != nil {
					progress.Failed++
					allOK = false
				}
			}
			if !allOK {
				continue
			}
			if !sourceEnabled {
				if err := s.st.DeleteDumpEntry(ctx, e.ID); err != nil {
					// 旧行残留只是多一条不命中行（源频道已停用），无害
					s.log.Warn("清理迁移源条目失败", "dump_entry_id", e.ID, "error", err.Error())
				}
			}
			progress.Done++
		}
		snap()
		time.Sleep(migratePacing)
	}
}
