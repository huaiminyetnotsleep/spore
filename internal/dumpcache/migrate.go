package dumpcache

// migrate.go — 缓存频道迁移工具：把旧缓存频道（含升级前 dump_channel_id=0
// 的存量，由调用方折算源频道）中仍可读的副本整批复制到当前全部启用的
// 缓存频道，免去逐链接重新提取。管理端在新增/切换缓存频道后触发
//（POST /api/v1/dumpcache/migrate），后台执行、进度可查；重启即停但幂等
// 可续——已复制的目标频道已有现行条目，重跑自动跳过、从断点继续。
//
// v29 起运行与逐条明细持久化（dump_migrate_runs/items，store 层状态机）：
// 暂停/继续/取消由 Web Handler 即写 DB 状态，执行循环逐条/批间轮询感知，
// 不经任何内存信号——跨 MTProto 生命周期（Service 每轮重建）天然安全；
// DB 唯一部分索引硬保证同时至多一个活跃运行。服务重建时由装配层调用
// ReconcileDumpMigrate 把遗留 running/paused 收敛为 interrupted（含上一
// 进程崩溃遗留），管理员可重新发起（幂等续跑）。

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// migrateBatchSize 是每页扫描与每批复制的条目上限；批间 pacing 避免打满
// Bot API 配额（429 重试由 delivery 层兜底）。migratePausePoll 是暂停等待
// 期间的状态轮询间隔。变量仅为测试可调小，运行期不变。
var (
	migrateBatchSize = 50
	migratePacing    = 800 * time.Millisecond
	migratePausePoll = time.Second
)

// migrateItemErrMax 是明细内单目标错误文本的截断上限（同 error_logs 的
// 错误串截断思路：只留排障所需，防异常超长错误刷库）。
const migrateItemErrMax = 200

// StartMigrate 启动一次迁移（后台 goroutine，一次仅一个）：把 from 频道的
// 现行格式条目复制到全部启用的缓存频道（from 本身除外——它若仍启用，其
// 条目本就有效）。拒绝条件返回受控错误：无启用缓存频道、源频道 ID 无效、
// 源频道即全部启用频道、已有迁移在跑（running/paused，DB 唯一索引兜底）。
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
	// 友好快速路径：活跃运行先查一次给受控文案；并发窗口由 DB 唯一
	// 活跃索引硬兜底（CreateDumpMigrateRun 冲突映射同一文案）。
	if run, err := s.st.LatestDumpMigrateRun(ctx); err == nil &&
		(run.Status == store.DumpMigrateRunning || run.Status == store.DumpMigratePaused) {
		return errors.New("已有缓存迁移进行中，请等待完成或先取消")
	}
	total, err := s.st.CountDumpEntriesByChannel(ctx, from)
	if err != nil {
		return fmt.Errorf("统计源频道条目失败: %w", err)
	}
	run, err := s.st.CreateDumpMigrateRun(ctx, from, targets, total)
	if err != nil {
		return err
	}
	// 后台执行：ctx 由调用方去取消化（WithoutCancel）；停止走 DB 状态
	//（暂停/取消/中断），服务重启即停、幂等可续。
	go s.runMigrate(context.WithoutCancel(ctx), run, sourceEnabled)
	return nil
}

// runMigrate 执行迁移主体：按 id 升序分页扫描源频道现行格式条目，逐条对
// 每个尚无现行条目的目标频道复制并落新行；全部目标就绪（或本就无需复制）
// 且源频道不在启用集合时删除源行（源频道仍启用则其条目继续有效，保留）。
// 每条处理后写明细行（聚合计数由此得出）。控制流经 DB 状态逐条感知：
// paused 轮询等待、cancelled/interrupted 静默退出（Handler 已写终态）。
// 源频道整体不可读（chat not found/deactivated/banned）时结束并记录原因
// ——剩余条目由 WriteClean 自愈或换源重试；单条/单目标失败不中断。
func (s *Service) runMigrate(ctx context.Context, run store.DumpMigrateRun, sourceEnabled bool) {
	from, targets := run.FromChannelID, run.To
	finish := func(reason string) {
		// 结束写回仅命中 running/paused（已被取消则 no-op）；DB 写失败
		// 只能记日志——遗留 running 行由下次启动 Reconcile 收敛。
		if err := s.st.SetDumpMigrateFinished(ctx, run.ID, reason); err != nil {
			s.log.Error("写迁移运行终态失败", "run_id", run.ID, "error", err.Error())
		}
		if reason != "" {
			s.log.Warn("缓存迁移中止", "run_id", run.ID, "from", from, "to", targets, "reason", reason)
		} else {
			s.log.Info("缓存迁移完成", "run_id", run.ID, "from", from, "to", targets)
		}
	}

	var afterID int64
	for {
		switch s.migrateGate(ctx, run.ID) {
		case migrateProceed:
		case migrateHalt: // 已取消/中断：Handler 已写终态，静默退出
			return
		default: // 暂停等待或 DB 故障
			finish("执行中断（暂停等待或读取运行状态失败），可重新发起继续")
			return
		}
		page, err := s.st.ListDumpEntriesByChannel(ctx, from, afterID, migrateBatchSize)
		if err != nil {
			finish(fmt.Sprintf("读取源频道条目失败: %v", err))
			return
		}
		if len(page) == 0 {
			finish("")
			return
		}
		halted := false // 批内感知到暂停/取消：跳过心跳与限速，直接回到闸门
		for _, e := range page {
			afterID = e.ID
			if s.migrateGate(ctx, run.ID) != migrateProceed {
				halted = true
				break
			}
			if reason := s.migrateEntry(ctx, run.ID, from, targets, sourceEnabled, e); reason != "" {
				finish(reason)
				return
			}
		}
		if halted {
			continue
		}
		_ = s.st.TouchDumpMigrateRun(ctx, run.ID)
		time.Sleep(migratePacing)
	}
}

// migrateGateVerdict 是 migrateGate 的裁决。
type migrateGateVerdict int

const (
	migrateProceed migrateGateVerdict = iota // 继续执行
	migrateHalt                              // 终态（cancelled/interrupted）：静默退出
	migrateStop                              // 暂停等待失败/DB 故障：结束并记录
)

// migrateGate 读取运行状态并裁决：running 继续；paused 轮询等待至恢复
// （恢复 → 继续；转终态 → halt；等待中 DB 故障 → stop）；其余（cancelled/
// interrupted/completed）→ halt。
func (s *Service) migrateGate(ctx context.Context, runID int64) migrateGateVerdict {
	status, err := s.st.DumpMigrateRunStatusOf(ctx, runID)
	if err != nil {
		return migrateStop
	}
	if status != store.DumpMigratePaused {
		if status == store.DumpMigrateRunning {
			return migrateProceed
		}
		return migrateHalt
	}
	ticker := time.NewTicker(migratePausePoll)
	defer ticker.Stop()
	for {
		<-ticker.C
		status, err = s.st.DumpMigrateRunStatusOf(ctx, runID)
		if err != nil {
			return migrateStop
		}
		switch status {
		case store.DumpMigrateRunning:
			return migrateProceed
		case store.DumpMigratePaused: // 继续等待
		default:
			return migrateHalt
		}
	}
}

// migrateEntry 处理单条源条目：逐目标频道查现行条目（此前自愈写入或已
// 迁移）只补缺失的频道，全部就绪则清理源行（源频道停用时），并落一条
// 明细（migrated/skipped/failed + 各目标落地消息 ID 或错误）。返回非空
// reason 表示整次运行应结束（源频道不可读）。
func (s *Service) migrateEntry(ctx context.Context, runID, from int64, targets []int64, sourceEnabled bool, e store.DumpEntry) string {
	// 逐目标频道查现行条目（此前自愈写入或已迁移）：只补缺失的频道
	pending := make([]int64, 0, len(targets))
	for _, to := range targets {
		if _, err := s.st.LatestDumpEntry(ctx, e.ChannelKey, e.MessageID, to); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			s.recordMigrateItem(ctx, runID, e, "failed", nil, "查询目标频道现行条目失败")
			return ""
		}
		pending = append(pending, to)
	}
	if len(pending) == 0 {
		// 全部启用频道都已有现行条目：源行不在启用集合时清理（清理失败
		// 只记日志——残留源行无害，明细仍记 skipped）
		if !sourceEnabled {
			if err := s.st.DeleteDumpEntry(ctx, e.ID); err != nil {
				s.log.Warn("清理迁移源条目失败", "dump_entry_id", e.ID, "error", err.Error())
			}
		}
		s.recordMigrateItem(ctx, runID, e, "skipped", nil, "")
		return ""
	}
	results := make([]store.DumpMigrateItemResult, 0, len(pending))
	failed := 0
	for _, to := range pending {
		ids, err := s.senderFor(0).CopyMessages(ctx, from, to, e.DumpIDs)
		if err != nil {
			if delivery.IsChannelGoneError(err) {
				// 源频道整体不可读：本条记失败明细后结束整次运行（剩余由
				// 复用自愈或换源重试），由调用方统一写终态。
				results = append(results, store.DumpMigrateItemResult{ChannelID: to, Error: truncateErr(err)})
				s.recordMigrateItem(ctx, runID, e, "failed", results, "源频道不可读，本条未迁移")
				return "源频道不可读（已删除或封禁），剩余条目将由复用自愈重建"
			}
			// 单目标失败（目标频道失权/瞬时故障）：该频道不落行，源行
			// 保持原值（源频道已停用时该条目暂不命中，无害）
			results = append(results, store.DumpMigrateItemResult{ChannelID: to, Error: truncateErr(err)})
			failed++
			continue
		}
		if _, err := s.st.InsertDumpEntry(ctx, store.DumpEntry{
			ChannelKey: e.ChannelKey, MessageID: e.MessageID, DumpIDs: ids,
			DumpChannelID: to, CreatedAt: e.CreatedAt,
		}); err != nil {
			results = append(results, store.DumpMigrateItemResult{ChannelID: to, Error: truncateErr(err)})
			failed++
			continue
		}
		results = append(results, store.DumpMigrateItemResult{ChannelID: to, MessageIDs: ids})
	}
	if failed > 0 {
		s.recordMigrateItem(ctx, runID, e, "failed", results,
			fmt.Sprintf("%d/%d 个目标频道复制失败", failed, len(pending)))
		return ""
	}
	if !sourceEnabled {
		if err := s.st.DeleteDumpEntry(ctx, e.ID); err != nil {
			// 旧行残留只是多一条不命中行（源频道已停用），无害
			s.log.Warn("清理迁移源条目失败", "dump_entry_id", e.ID, "error", err.Error())
		}
	}
	s.recordMigrateItem(ctx, runID, e, "migrated", results, "")
	return ""
}

// recordMigrateItem 落一条明细；写失败只记日志（计数依赖明细，DB 故障
// 将在下一页读取时中止整次运行）。
func (s *Service) recordMigrateItem(ctx context.Context, runID int64, e store.DumpEntry,
	status string, results []store.DumpMigrateItemResult, itemErr string) {
	if err := s.st.InsertDumpMigrateItem(ctx, store.DumpMigrateItem{
		RunID: runID, ChannelKey: e.ChannelKey, MessageID: e.MessageID,
		Status: status, Results: results, Error: itemErr,
	}); err != nil {
		s.log.Warn("写迁移明细失败", "run_id", runID, "channel_key", e.ChannelKey,
			"message_id", e.MessageID, "error", err.Error())
	}
}

// truncateErr 截断明细错误文本，防异常超长错误串刷库。
func truncateErr(err error) string {
	msg := err.Error()
	runes := []rune(msg)
	if len(runes) > migrateItemErrMax {
		return string(runes[:migrateItemErrMax]) + "…"
	}
	return msg
}
