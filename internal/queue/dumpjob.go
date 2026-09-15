package queue

// dumpjob.go — 仅缓存补写任务（管理端"转存缓存频道"）：fetch 源消息后经
// sendConverted 直接向缓存频道发送干净副本（links=nil，无用户绑定频道脚注，
// 语义与 WriteClean 一致），随后落 dump_entries 坐标供同链接复用。不经
// WriteClean 的"从用户聊天复制"路径——该路径依赖任务执行时内存中的用户
// 消息坐标（不落库），历史记录不具备，只能重新获取源消息。与云盘任务
// （cloudjob.go）同构：不向用户重发任何消息；Process 层共享开始埋点、
// 进度注册、取消标记与超时治理，成功/失败均不产生用户侧消息。

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
//  3. fetch（15 分钟取数窗口）→ sendConverted 发送到缓存频道；任一条目
//     失败即整体失败，部分已发送消息留在缓存频道但不落条目（与 WriteClean
//     的中断语义一致，下次成功投递/补写自愈）；
//  4. 全部条目发送成功后落 dump_entries（RecordEntry 尽力而为）。
func runDumpJob(ctx context.Context, d Deps, j Job) (mediaMeta, error) {
	if d.Dump == nil {
		return mediaMeta{}, apperr.New(apperr.CodeInternal, "缓存频道依赖未装配")
	}
	channel, ok := d.Dump.Channel()
	if !ok {
		return mediaMeta{}, apperr.New(apperr.CodeInternal, "缓存频道未配置")
	}
	if d.Dump.EntryLive(ctx, refChannelKey(j.Ref), j.Ref.MessageID) {
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
	meta, err := sendConverted(ctx, d, j, channel, msgs, nil)
	if err != nil {
		return mediaMeta{}, err
	}
	d.Dump.RecordEntry(ctx, refChannelKey(j.Ref), j.Ref.MessageID, meta.SentIDs)
	d.Log.Info("缓存频道干净副本已写入", "job_id", j.ID, "request_id", j.RequestID,
		"ref", j.Ref.String(), "messages", len(meta.SentIDs))
	return meta, nil
}
