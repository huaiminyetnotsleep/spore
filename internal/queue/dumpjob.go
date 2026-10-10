package queue

// dumpjob.go — 仅缓存补写任务（管理端"转存缓存频道"）：fetch 源消息后经
// sendConverted 直接向缓存频道发送干净副本（links=nil，无用户绑定频道脚注，
// 语义与 WriteClean 一致），随后落 dump_entries 坐标供同链接复用。不经
// WriteClean 的"从用户聊天复制"路径——该路径依赖任务执行时内存中的用户
// 消息坐标（不落库），历史记录不具备，只能重新获取源消息。与云盘任务
//（cloudjob.go）同构：不向用户重发任何消息；Process 层共享开始埋点、
// 进度注册、取消标记与超时治理，成功/失败均不产生用户侧消息。
//
// 多缓存频道扇出：源消息只 fetch 一次，按启用频道顺序对首个发送成功的
// 频道做完整重传（基准频道，决定任务成败），其余启用频道从该副本服务端
// 复制（bot 自有频道中转，不受源频道限制且零下载）；逐频道尽力而为，单
// 频道失败只告警（事件携带频道 ID）并继续，全部频道失败才判任务失败。

import (
	"context"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// runDumpJob 执行仅缓存补写任务，返回媒体诊断元数据与错误。流程：
//  1. 依赖与目标解析（nil 防御：装配错误/配置在排队后被清除都直接失败，
//     不静默降级——列表与重试入口可见明确原因）；
//  2. 执行时二次预检 dump_entries：提交入口已预检"跳过有效副本"，此处
//     覆盖入队到执行之间的并发窗口，并按消息存在性复核（管理员在缓存
//     频道删除副本后条目仍在，须放行重写自愈）；命中有效副本直接成功；
//  3. fetch（15 分钟取数窗口）→ 按启用频道顺序重传首个成功者（基准频道，
//     发送失败即整体失败：部分已发送消息留在该频道但不落条目，与
//     WriteClean 的中断语义一致，下次成功投递/补写自愈）；其余启用频道
//     从基准副本服务端复制，逐频道尽力而为、失败只告警不阻断；
//  4. 逐频道落 dump_entries（RecordEntryFor 尽力而为）。
func runDumpJob(ctx context.Context, d Deps, j Job) (mediaMeta, error) {
	if d.Dump == nil {
		return mediaMeta{}, apperr.New(apperr.CodeInternal, "缓存频道依赖未装配")
	}
	channels, ok := d.Dump.Channels()
	if !ok {
		return mediaMeta{}, apperr.New(apperr.CodeInternal, "缓存频道未配置")
	}
	channelKey := refChannelKey(j.Ref)
	if d.Dump.EntryLive(ctx, channelKey, j.Ref.MessageID) {
		d.Log.Info("同链接缓存频道副本仍有效（执行时复核命中），跳过补写",
			"job_id", j.ID, "request_id", j.RequestID, "ref", j.Ref.String())
		return metaMetaFromHistory(ctx, d, j), nil
	}
	// 无条目或副本已失效（试探复制失败）：继续补写重写副本自愈

	fetchCtx, cancelFetch := context.WithTimeout(ctx, processTimeout)
	msgs, err := d.Fetcher.Fetch(fetchCtx, j.Ref)
	cancelFetch()
	if err != nil {
		return mediaMeta{}, err
	}
	// 基准频道：按启用频道顺序首个发送成功者（决定任务成败与镜像源）
	var baseMeta mediaMeta
	var baseChannel int64
	var lastErr error
	for _, channel := range channels {
		meta, serr := sendConverted(ctx, d, j, channel, msgs, nil)
		if serr == nil {
			baseMeta, baseChannel = meta, channel
			break
		}
		lastErr = serr
		d.Log.Warn("缓存补写基准频道发送失败，尝试下一个启用频道",
			"job_id", j.ID, "request_id", j.RequestID,
			"dump_channel", channel, "error", serr.Error())
	}
	if baseChannel == 0 {
		return mediaMeta{}, lastErr
	}
	d.Dump.RecordEntryFor(ctx, baseChannel, channelKey, j.Ref.MessageID, baseMeta.SentIDs)
	// 其余启用频道从基准副本服务端复制（零下载；各频道消息坐标独立，逐
	// 频道落条目）：单频道失败告警并继续，不回滚基准频道结果
	allOK := true
	for _, channel := range channels {
		if channel == baseChannel {
			continue
		}
		ids, cerr := d.Dump.CopyOutFrom(ctx, j.BotID, channel, baseChannel, baseMeta.SentIDs)
		if cerr != nil {
			allOK = false
			d.Dump.ReportWriteFailure(ctx, cerr, channel)
			continue
		}
		d.Dump.RecordEntryFor(ctx, channel, channelKey, j.Ref.MessageID, ids)
	}
	if allOK {
		d.Dump.WriteRecovered(ctx)
	}
	// 监听源回退任务的缓存中转镜像（受保护内容 → 转发频道）：实现内按
	// requestID 反查监听事件行，非监听来源的补写任务直接跳过。镜像以基准
	// 频道为源（受保护源重传后从 bot 自有频道复制，不再受源频道限制）。
	d.mirrorSourceDump(ctx, j, baseChannel, baseMeta.SentIDs)
	d.Log.Info("缓存频道干净副本已写入", "job_id", j.ID, "request_id", j.RequestID,
		"ref", j.Ref.String(), "dump_channels", len(channels), "messages", len(baseMeta.SentIDs))
	return baseMeta, nil
}
