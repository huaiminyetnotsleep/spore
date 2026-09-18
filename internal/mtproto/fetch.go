package mtproto

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// classifyTgError 把 gotd 的 Telegram RPC 错误统一归类为 AppError；
// 优先匹配可定位的业务错误（源不可达/引用失效/限流/服务端故障/网络故障），
// 未识别的错误才兜底 INTERNAL_ERROR。
func classifyTgError(err error) *apperr.AppError {
	switch {
	case tgerr.Is(err, "CHANNEL_PRIVATE", "CHANNEL_PUBLIC_GROUP_NA", "CHAT_ADMIN_REQUIRED", "CHAT_NOT_FOUND"):
		return apperr.Wrap(apperr.CodeChannelInaccessible, err)
	case tgerr.Is(err, "USERNAME_NOT_OCCUPIED", "USERNAME_INVALID"):
		return apperr.Wrap(apperr.CodeInvalidURL, err)
	case tgerr.Is(err, "MESSAGE_ID_INVALID", "MESSAGE_EMPTY"):
		return apperr.Wrap(apperr.CodeMessageNotFound, err)
	case tgerr.Is(err, "FILE_REFERENCE_EXPIRED", "PERSISTENT_FILE_REFERENCE_INVALID"):
		return apperr.Wrap(apperr.CodeFileReferenceInvalid, err)
	case tgerr.Is(err, "FLOOD_WAIT_X", "FLOOD_PREMIUM_WAIT_X"):
		return apperr.Wrap(apperr.CodeRateLimited, err)
	case tgerr.IsCode(err, 500):
		return apperr.Wrap(apperr.CodeTelegramServer, err)
	case apperr.IsTransportFailure(err):
		return apperr.Wrap(apperr.CodeNetworkError, err)
	default:
		return apperr.Wrap(apperr.CodeInternal, err)
	}
}

// IsFileReferenceExpired 判断错误链中是否含 file reference 失效，
// 该场景可通过 RefreshMedia 拿到新引用后重试。
func IsFileReferenceExpired(err error) bool {
	var ae *apperr.AppError
	if !errors.As(err, &ae) {
		return false
	}
	return tgerr.Is(ae.Cause, "FILE_REFERENCE_EXPIRED")
}

// API 暴露底层客户端供下载器使用；须在 client.Run 回调作用域内使用。
func (f *Fetcher) API() *tg.Client {
	return f.api
}

// Fetch 取源消息；相册时返回组内全部消息（按 ID 升序）。
//
// 取数策略：一次批量拉取以目标为中心的邻域窗口（单条与 19 条是同一次 RPC 成本），
// 在结果中定位目标消息，若属于相册则就地按 GroupedID 过滤出整组。
func (f *Fetcher) Fetch(ctx context.Context, ref tmeurl.SourceRef) ([]*tg.Message, error) {
	peer, err := f.ResolveInputPeer(ctx, ref)
	if err != nil {
		return nil, err
	}

	all, err := f.getMessages(ctx, peer, neighborIDs(ref.MessageID))
	if err != nil {
		return nil, err
	}

	var target *tg.Message
	for _, mc := range all {
		switch m := mc.(type) {
		case *tg.MessageService:
			if m.ID == ref.MessageID {
				return nil, apperr.New(apperr.CodeServiceMessage,
					fmt.Sprintf("消息 %d 为服务消息", ref.MessageID))
			}
		case *tg.MessageEmpty:
			if m.ID == ref.MessageID {
				return nil, apperr.New(apperr.CodeMessageNotFound,
					fmt.Sprintf("消息 %d 不存在或已删除", ref.MessageID))
			}
		case *tg.Message:
			if m.ID == ref.MessageID {
				target = m
			}
		}
	}
	if target == nil {
		return nil, apperr.New(apperr.CodeMessageNotFound, "该消息不在返回结果中")
	}

	if gid, ok := target.GetGroupedID(); ok && gid != 0 {
		group := make([]*tg.Message, 0, message.AlbumMaxItems)
		for _, mc := range all {
			m, ok := mc.(*tg.Message)
			if !ok {
				continue
			}
			if mgid, ok := m.GetGroupedID(); ok && mgid == gid {
				group = append(group, m)
			}
		}
		sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
		if len(group) > 1 {
			return group, nil
		}
	}
	return []*tg.Message{target}, nil
}

// neighborIDs 返回目标的邻域取数窗口：±(AlbumMaxItems-1) 足以覆盖
// 目标落在组内任意位置时的整组；Telegram 单次批量取数上限 100，远高于此。
func neighborIDs(target int) []int {
	w := message.AlbumMaxItems - 1
	start := target - w
	if start < 1 {
		start = 1
	}
	out := make([]int, 0, 2*w+1)
	for id := start; id <= target+w; id++ {
		out = append(out, id)
	}
	return out
}

// RefreshMedia 重新拉取源消息以刷新过期的 file reference。
// 返回该消息条目的最新媒体描述；消息已不可得时 ok 为 false。
// 供 worker 在下载失败（引用过期）时透明重试，单条与相册路径共用。
// 只转换目标那一条（ConvertOne），不为找一条而转换整组。
func (f *Fetcher) RefreshMedia(ctx context.Context, ref tmeurl.SourceRef, messageID int) (message.Media, bool, error) {
	msgs, err := f.Fetch(ctx, ref)
	if err != nil {
		return message.Media{}, false, err
	}
	for _, m := range msgs {
		if m.ID != messageID {
			continue
		}
		it := message.ConvertOne(m)
		if it.Media != nil && it.Media.Kind != message.KindUnsupported {
			return *it.Media, true, nil
		}
		return message.Media{}, false, nil
	}
	return message.Media{}, false, nil
}

// getMessages 按目标 peer 类型分发取数并归一结果。
func (f *Fetcher) getMessages(ctx context.Context, peer tg.InputPeerClass, ids []int) ([]tg.MessageClass, error) {
	inputMsgs := make([]tg.InputMessageClass, len(ids))
	for i, id := range ids {
		inputMsgs[i] = &tg.InputMessageID{ID: id}
	}

	var cls tg.MessagesMessagesClass
	var err error
	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		ch := &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash}
		cls, err = f.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: ch,
			ID:      inputMsgs,
		})
	case *tg.InputPeerUser, *tg.InputPeerChat:
		cls, err = f.api.MessagesGetMessages(ctx, inputMsgs)
	default:
		return nil, apperr.New(apperr.CodeInternal,
			fmt.Sprintf("暂不支持从该聊天形态获取消息：%T", peer))
	}
	if err != nil {
		return nil, classifyTgError(err)
	}
	return messagesOf(cls), nil
}

// messagesOf 从不同响应形态中抽取消息列表；AsModified 是 v0.16x 的官方归一化入口。
func messagesOf(cls tg.MessagesMessagesClass) []tg.MessageClass {
	if m, ok := cls.AsModified(); ok {
		return m.GetMessages()
	}
	return nil // MessagesNotModified 等：视为空
}
