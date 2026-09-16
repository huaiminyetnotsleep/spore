package queue

// reuse.go — TG 链接复用（转存频道直拷）：任务成功投递后，缓存频道里保有
// 一份无脚注的干净副本（dumpcache.WriteClean，坐标落 dump_entries）；同链接
// 再次提交时，worker 在取数之前查最新副本并经 Bot API copyMessages 整条
// 复制到目标聊天——服务端复制不受媒体大小限制（2GB 同路径）、相册保组、
// caption 无脚注泄露、不因原用户删消息失效。与云盘 trySkipCloudUpload 同构，
// 区别在于"核验"就是复制本身：失败（副本被删等）自动回落完整下载上传，
// 成功后重写副本自愈。
//
// 历史上的 layer 1（用户聊天坐标 copyMessages 复用 + 脚注候选规则）已移除：
// 投递 caption 织有用户绑定频道脚注，跨用户整条复制会泄露脚注，而缓存频道
// 副本从构造上就是干净的，无需候选筛选。
//
// 绑定频道的用户收到复制品后需要自己的脚注：fetch 源消息一次（1 次 RPC，
// 仅此场景）重建首条 caption（含脚注）后 editMessageCaption 补上；fetch 失败
// 只跳过补脚注，不影响任务结果。

import (
	"context"
	"errors"

	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// tryReuseFromDump 尝试从缓存频道复用同链接的干净副本。命中返回媒体元数据
// （Reused=true）与复制得到的新消息 ID（频道副本与终态落库续用）；任何失败
// 都返回未命中，调用方继续正常流程。
func tryReuseFromDump(ctx context.Context, d Deps, j Job) (mediaMeta, []int, bool) {
	if d.ReuseEnabled != nil && !d.ReuseEnabled() {
		return mediaMeta{}, nil, false
	}
	if d.Dump == nil || d.Dump.Enabled() == false || d.Store == nil || j.RequestID == 0 {
		return mediaMeta{}, nil, false
	}
	entry, ok := d.Dump.Entry(ctx, refChannelKey(j.Ref), j.Ref.MessageID)
	if !ok {
		return mediaMeta{}, nil, false
	}
	ids, err := d.Dump.CopyOut(ctx, j.BotID, j.ChatID, entry.DumpIDs)
	if err != nil {
		// 副本被删、频道不可访问等预期内失败：回落完整下载上传（成功后重写副本）
		d.Log.Info("复制缓存频道副本失败，回落完整下载上传",
			"job_id", j.ID, "dump_entry_id", entry.ID, "error", err.Error())
		return mediaMeta{}, nil, false
	}

	// 绑定频道的用户需要自己的脚注：fetch 一次源消息重建首条 caption 后补上；
	// fetch 得到的条目同时用于还原更准确的媒体元数据
	items := fetchForFootnote(ctx, d, j)
	meta := metaMetaFromHistory(ctx, d, j)
	if len(items) > 0 {
		if ferr := applyFootnote(ctx, d, j, items, ids[0]); ferr != nil {
			d.Log.Info("复制品补脚注失败（脚注省略，不影响任务）",
				"job_id", j.ID, "error", ferr.Error())
		}
		meta = mediaMetaOf(items)
	}

	d.Log.Info("同链接命中缓存频道副本，直接复制已投递消息",
		"job_id", j.ID, "request_id", j.RequestID, "dump_entry_id", entry.ID,
		"ref", j.Ref.String(), "messages", len(ids), "footnote", len(items) > 0)
	meta.Reused = true
	return meta, ids, true
}

// fetchForFootnote 仅在当前用户绑定了公开频道时 fetch 源消息（重建带脚注的
// caption 与媒体元数据）；未绑定（多数用户）返回 nil——复用零取数。
// fetch 或转换失败返回 nil，调用方跳过补脚注。
func fetchForFootnote(ctx context.Context, d Deps, j Job) []message.Item {
	if d.Channels == nil {
		return nil
	}
	links, err := d.Channels.PublicChannelLinks(ctx, j.UserID)
	if err != nil || len(links) == 0 {
		return nil
	}
	fetchCtx, cancel := context.WithTimeout(ctx, processTimeout)
	defer cancel()
	msgs, err := d.Fetcher.Fetch(fetchCtx, j.Ref)
	if err != nil {
		d.Log.Info("补脚注取数失败，跳过脚注", "job_id", j.ID, "error", err.Error())
		return nil
	}
	items := message.Convert(msgs)
	if len(items) == 0 {
		return nil
	}
	return items
}

// applyFootnote 对复制的首条消息补上含用户频道脚注的 caption（媒体
// editMessageCaption，文本 editMessageText）。
func applyFootnote(ctx context.Context, d Deps, j Job, items []message.Item, firstID int) error {
	sourceURL, _ := j.Ref.URL()
	links, _ := d.Channels.PublicChannelLinks(ctx, j.UserID)
	first := items[0]
	if first.Media != nil {
		caption := first.MediaCaption().WithQuotedBody()
		if sourceURL != "" {
			caption = caption.WithSourceLink(sourceURL).WithChannels(links)
		}
		return d.senderFor(j).EditMessageCaption(ctx, j.ChatID, firstID, caption.RenderHTML())
	}
	return d.senderFor(j).EditMessageText(ctx, j.ChatID, firstID, first.RenderHTMLWithSource(sourceURL, links))
}

// metaMetaFromHistory 兜底还原：从同链接最近成功请求行取媒体诊断元数据
// （未 fetch 源消息时使用）；无历史行时返回零值 meta（reuse 标记仍有效）。
func metaMetaFromHistory(ctx context.Context, d Deps, j Job) mediaMeta {
	prior, err := d.Store.LatestSucceededTGRequest(ctx, refChannelKey(j.Ref), j.Ref.MessageID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.Log.Warn("查询历史成功请求失败", "job_id", j.ID, "error", err.Error())
		}
		return mediaMeta{}
	}
	return metaFromRequest(prior)
}
