package access

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// sourcewarm.go — 监听源预热回退补写：监听器对无法服务端复制的源消息
//（受保护内容 has_protected_content，或 copyMessages 被拒），按源频道键与
// 消息 ID 重建 SourceRef 并特权入队 DumpOnly 任务（与 DumpBackfill 同一
// worker 管线：fetch → 下载 → 重传进缓存频道 → 落 dump_entries，>2GB 自动
// 分段）。放在本包与 DumpBackfill 同理：需要建 requests 行并入队，且必须
// 绕过用户配额/频率/去重/并发（自动化动作不占任何用户额度）。
//
// requests.user_id 外键要求行挂在真实用户上；自动化动作无发起用户，统一
// 归属号主（is_owner=1）——管理端请求列表天然可见回退执行情况。号主未
// 设置时跳过并记日志（不作为错误打断监听）。

// EnqueueSourceDump 为监听到的源消息建 dump 请求行并特权入队。sourceKind/
// channelKey 与 store.Request 同语义（public=用户名键、private=-100 数字键）。
// 返回关联的 requests 行 ID（未建行 = 0：号主未设置或队列满）。队列满时行
// 标记 failed(QUEUE_FULL)（可经现有重试入口重试）；error 仅表示存储故障，
// 调用方记日志即可、不重试（下一条消息自愈）。
func (s *Service) EnqueueSourceDump(ctx context.Context, sourceKind, channelKey string, messageID int) (int64, error) {
	ownerID, err := s.store.OwnerID(ctx)
	if errors.Is(err, store.ErrNotFound) {
		s.log.Warn("监听源回退补写跳过：未设置号主（requests 行无归属用户）",
			"channel_key", channelKey, "message_id", messageID)
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	ref, ok := refFromChannelKey(sourceKind, channelKey, messageID)
	if !ok {
		return 0, apperr.New(apperr.CodeInternal,
			"监听源频道键无法重建来源链接: "+channelKey)
	}
	now := s.now().UnixMilli()
	created, err := s.store.CreateRequest(ctx, store.Request{
		UserID:       ownerID,
		SourceKind:   sourceKind,
		ChannelKey:   channelKey,
		MessageID:    messageID,
		DeliveryMode: store.DeliveryModeDump,
		RequestedAt:  now,
		QueuedAt:     now,
	})
	if err != nil {
		return 0, err
	}
	job := queue.NewJob(ownerID, ownerID, ref, 0, created.ID)
	job.DumpOnly = true
	if err := s.queue.Enqueue(job); err != nil {
		s.log.Warn("监听源回退补写入队失败（队列已满，行已标记可重试）",
			"request_id", created.ID, "channel_key", channelKey, "message_id", messageID)
		if ferr := s.store.FinishRequest(ctx, created.ID, store.RequestResult{
			Status:       store.RequestFailed,
			ErrorCode:    string(apperr.CodeQueueFull),
			DeliveryMode: store.DeliveryModeDump,
		}); ferr != nil {
			s.log.Error("标记监听源回退补写队列满状态未落库",
				"request_id", created.ID, "error", ferr.Error())
		}
		return created.ID, nil
	}
	s.log.Info("监听源回退补写已入队",
		"request_id", created.ID, "channel_key", channelKey, "message_id", messageID)
	return created.ID, nil
}

// refFromChannelKey 从频道键重建 SourceRef（RefFromRequest 的入参形态：
// SourceKind + ChannelKey + MessageID；监听路径无 requests 行，单独取其
// 逆变换逻辑）。私有频道键格式损坏时返回 false（数据异常防御）。
func refFromChannelKey(sourceKind, channelKey string, messageID int) (tmeurl.SourceRef, bool) {
	if sourceKind == store.SourcePrivate {
		if !strings.HasPrefix(channelKey, "-100") || len(channelKey) <= len("-100") {
			return tmeurl.SourceRef{}, false
		}
		id, err := strconv.ParseInt(channelKey[len("-100"):], 10, 64)
		if err != nil || id <= 0 {
			return tmeurl.SourceRef{}, false
		}
		return tmeurl.SourceRef{
			Kind:      tmeurl.PeerChannelID,
			ChannelID: id,
			MessageID: messageID,
		}, true
	}
	if channelKey == "" {
		return tmeurl.SourceRef{}, false
	}
	return tmeurl.SourceRef{
		Kind:      tmeurl.PeerUsername,
		Username:  channelKey,
		MessageID: messageID,
	}, true
}
