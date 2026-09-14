package mtproto

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// peerCache 维护 bareChannelID → accessHash 映射并持久化为 JSON（data/peers.json）。
// AccessHash 只能来自用户账号"见过"的 peer 信息，策略：
// 先查缓存；未命中则全量遍历对话列表（含归档夹）收割 hash 后重试。
type peerCache struct {
	mu     sync.Mutex
	hashes map[int64]int64
	path   string
}

func loadPeerCache(path string) *peerCache {
	p := &peerCache{hashes: map[int64]int64{}, path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		return p // 首次运行或不可读：空表
	}
	_ = json.Unmarshal(data, &p.hashes)
	return p
}

func (p *peerCache) save() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	data, err := json.MarshalIndent(p.hashes, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p.path, data, 0o600)
}

func (p *peerCache) get(channelID int64) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	h, ok := p.hashes[channelID]
	return h, ok
}

// putBatch 收割一批 chats 中的频道 hash，返回新收录条数。
func (p *peerCache) putBatch(channels []tg.ChatClass) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range channels {
		ch, ok := c.(*tg.Channel)
		if !ok {
			continue
		}
		if _, exists := p.hashes[ch.ID]; !exists {
			n++
		}
		p.hashes[ch.ID] = ch.AccessHash
	}
	return n
}

// toInputPeer 把 PeerClass 映射为对应 InputPeer；未知形态返回 false。
// 对话翻页游标与 resolve 兜底共用此唯一映射。
func toInputPeer(peer tg.PeerClass) (tg.InputPeerClass, bool) {
	switch p := peer.(type) {
	case *tg.PeerChannel:
		return &tg.InputPeerChannel{ChannelID: p.ChannelID}, true
	case *tg.PeerUser:
		return &tg.InputPeerUser{UserID: p.UserID}, true
	case *tg.PeerChat:
		return &tg.InputPeerChat{ChatID: p.ChatID}, true
	default:
		return nil, false
	}
}

// dialogKey 生成 (peer, msgID) 的唯一键，供对话游标查表避开跨聊天 ID 碰撞。
func dialogKey(p tg.PeerClass, msgID int) string {
	switch v := p.(type) {
	case *tg.PeerChannel:
		return fmt.Sprintf("c%d:%d", v.ChannelID, msgID)
	case *tg.PeerUser:
		return fmt.Sprintf("u%d:%d", v.UserID, msgID)
	case *tg.PeerChat:
		return fmt.Sprintf("g%d:%d", v.ChatID, msgID)
	default:
		return fmt.Sprintf("?:%d", msgID)
	}
}

// Fetcher 组合 Peer 解析与消息获取；所有方法须在 client.Run 回调作用域内调用。
type Fetcher struct {
	api   *tg.Client
	log   *slog.Logger
	cache *peerCache
}

// NewFetcher 基于已登录的 API 客户端创建取数器。
// peers.json 的文件名由本包（格式的拥有者）决定，与 session.json 的做法一致。
func NewFetcher(api *tg.Client, dataDir string, log *slog.Logger) *Fetcher {
	peersPath := filepath.Join(dataDir, "peers.json")
	cache := loadPeerCache(peersPath)
	log.Info("已加载 peer 缓存", "path", peersPath, "entries", len(cache.hashes))
	return &Fetcher{api: api, log: log, cache: cache}
}

// ResolveInputPeer 把链接解析结果转换为可用于 API 调用的 InputPeer。
func (f *Fetcher) ResolveInputPeer(ctx context.Context, ref tmeurl.SourceRef) (tg.InputPeerClass, error) {
	switch ref.Kind {
	case tmeurl.PeerUsername:
		resolved, err := f.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: ref.Username})
		if err != nil {
			return nil, classifyTgError(err)
		}
		// 仅在缓存有新增时落盘，避免每次请求的无效写
		if n := f.cache.putBatch(resolved.Chats); n > 0 {
			_ = f.cache.save() // 持久化尽力而为
		}

		if peer := f.matchResolved(resolved); peer != nil {
			return peer, nil
		}
		return nil, apperr.New(apperr.CodeChannelInaccessible, "resolve 未返回可用的聊天对象")
	default: // 裸 ChannelID
		if h, ok := f.cache.get(ref.ChannelID); ok {
			return &tg.InputPeerChannel{ChannelID: ref.ChannelID, AccessHash: h}, nil
		}
		f.log.Info("peer 缓存未命中，开始遍历对话列表", "channel_id", ref.ChannelID)
		if err := f.walkDialogs(ctx); err != nil {
			return nil, err // 已是 AppError
		}
		_ = f.cache.save()
		if h, ok := f.cache.get(ref.ChannelID); ok {
			return &tg.InputPeerChannel{ChannelID: ref.ChannelID, AccessHash: h}, nil
		}
		return nil, apperr.New(apperr.CodeChannelInaccessible,
			fmt.Sprintf("遍历对话列表后仍无法定位频道 %d（用户账号可能未加入）", ref.ChannelID))
	}
}

// matchResolved 从 resolve 结果中取出与 Peer 字段匹配的输入 peer（携带 access hash）。
func (f *Fetcher) matchResolved(resolved *tg.ContactsResolvedPeer) tg.InputPeerClass {
	switch p := resolved.Peer.(type) {
	case *tg.PeerChannel:
		for _, c := range resolved.Chats {
			if ch, ok := c.(*tg.Channel); ok && ch.ID == p.ChannelID {
				return &tg.InputPeerChannel{ChannelID: ch.ID, AccessHash: ch.AccessHash}
			}
		}
	case *tg.PeerUser:
		for _, u := range resolved.Users {
			if usr, ok := u.(*tg.User); ok && usr.ID == p.UserID {
				return &tg.InputPeerUser{UserID: usr.ID, AccessHash: usr.AccessHash}
			}
		}
	}
	// 未能匹配到 hash：按类型给出最低限度 peer，未知形态放弃
	peer, _ := toInputPeer(resolved.Peer)
	return peer
}

// walkDialogs 全量遍历对话列表收割 access hash；主列表与归档夹分别拉取。
func (f *Fetcher) walkDialogs(ctx context.Context) error {
	for _, folder := range []int{0, 1} {
		if err := f.walkFolder(ctx, folder); err != nil {
			// 已分类的 AppError 原样上抛，避免覆盖错误码（如 FLOOD_WAIT）
			var ae *apperr.AppError
			if errors.As(err, &ae) {
				return ae
			}
			return apperr.Wrap(apperr.CodeInternal, fmt.Errorf("遍历对话夹 %d 失败: %w", folder, err))
		}
	}
	return nil
}

func (f *Fetcher) walkFolder(ctx context.Context, folderID int) error {
	limit := 100
	offsetDate, offsetID := 0, 0
	var offsetPeer tg.InputPeerClass = &tg.InputPeerEmpty{}

	for page := 0; ; page++ {
		req := tg.MessagesGetDialogsRequest{
			Limit:      limit,
			OffsetDate: offsetDate,
			OffsetID:   offsetID,
			OffsetPeer: offsetPeer,
		}
		if folderID > 0 {
			req.SetFolderID(folderID)
		}
		resp, err := f.api.MessagesGetDialogs(ctx, &req)
		if err != nil {
			return classifyTgError(err)
		}
		// 对话数超过 limit 时 Telegram 返回 dialogsSlice；其余形态（NotModified 等）视为遍历结束
		var dialogList []tg.DialogClass
		var messages []tg.MessageClass
		var chats []tg.ChatClass
		switch r := resp.(type) {
		case *tg.MessagesDialogs:
			dialogList, messages, chats = r.Dialogs, r.Messages, r.Chats
		case *tg.MessagesDialogsSlice:
			dialogList, messages, chats = r.Dialogs, r.Messages, r.Chats
		default:
			return nil
		}

		harvested := f.cache.putBatch(chats)

		// 翻页游标：最后一条 dialog 的 top message 日期与 ID。
		// 以 (peer, msgID) 为键——不同聊天的消息 ID 命名空间独立，仅按 ID 会碰撞取错日期；
		// 置顶消息可能是服务消息（MessageService），同样携带可用的 Date。
		topDate := map[string]int{}
		for _, mc := range messages {
			var peer tg.PeerClass
			var id, date int
			switch m := mc.(type) {
			case *tg.Message:
				peer, id, date = m.PeerID, m.ID, m.Date
			case *tg.MessageService:
				peer, id, date = m.PeerID, m.ID, m.Date
			default:
				continue
			}
			topDate[dialogKey(peer, id)] = date
		}
		moved := false
		last := len(dialogList) - 1
		for i := last; i >= 0; i-- {
			d, ok := dialogList[i].(*tg.Dialog)
			if !ok || d == nil {
				continue
			}
			if date, has := topDate[dialogKey(d.Peer, d.TopMessage)]; has {
				offsetDate = date
				offsetID = d.TopMessage
				if p, ok := toInputPeer(d.Peer); ok {
					offsetPeer = p
				}
				moved = true
				break
			}
		}
		f.log.Debug("对话翻页",
			"folder", folderID, "page", page,
			"dialogs", len(dialogList), "new_hashes", harvested,
		)

		if !moved || len(dialogList) < limit {
			return nil
		}
		if harvested == 0 && page > 20 { // 防御：极端大号限制页数
			return nil
		}
	}
}
