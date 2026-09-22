// 用户号频道成员管理：/join 的 MTProto 能力层。
// 加入（importChatInvite）、预检（checkChatInvite）、退出（leaveChannel）、
// 列出已加入频道、加入后静音（updateNotifySettings）与归档（editPeerFolders）。
// 全部方法须在 client.Run 的 ready 作用域内调用（与 Fetcher 同约束）。
package mtproto

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
)

// ErrMembershipUnavailable 表示当前没有可用的用户号上下文（MTProto 离线）。
// Web/botapi 层据此返回受控的"暂不可用"提示。
var ErrMembershipUnavailable = errors.New("mtproto: 没有可用的用户号上下文")

// ErrChannelCreator 表示当前账号是频道创建者，不能退出（Telegram 限制）。
var ErrChannelCreator = errors.New("mtproto: 账号是频道创建者，无法退出")

// ErrJoinRequestSent 表示该邀请链接开启了"加入需管理员审核"：
// importChatInvite 不直接加入，而是向频道管理员发送加入请求，
// 须待频道侧批准后账号才真正成为成员。
var ErrJoinRequestSent = errors.New("mtproto: 已向频道管理员发送加入请求")

// JoinOptions 控制加入后的附加动作；零值表示不做任何附加动作。
type JoinOptions struct {
	Mute    bool // 加入后静音该频道
	Archive bool // 加入后把对话移入归档夹（folder 1）
}

// InviteInfo 是 checkChatInvite 的预检结果：链接指向的频道概要。
type InviteInfo struct {
	Title           string // 频道标题（预览用）
	Participants    int    // 参与人数
	IsChannel       bool   // 是频道/超级组（false 为普通群组）
	AlreadyJoined   bool   // 当前账号已是成员
	RequestedToJoin bool   // 需管理员审批才能加入的频道（加入请求制）
	// AlreadyJoined 为 true 时携带频道定位信息（供事后静音/归档补执行）；
	// 其余场景为零值。
	ChannelID  int64
	AccessHash int64
}

// JoinedChannel 是 ListJoined 返回的已加入频道条目。
type JoinedChannel struct {
	ChannelID  int64
	AccessHash int64 // peers 缓存收割所得，供退出复用
	Title      string
	Username   string
	Kind       string // "channel"（广播频道）/ "supergroup"
	Creator    bool   // 当前账号是否为创建者（不可退出）
	Archived   bool   // 对话当前是否已在归档夹（folder 1）
}

// muteUntilFar 静音截止时间取足够远的将来（Telegram 客户端表现为"永久静音"）。
func muteUntilFar(now time.Time) int {
	return int(now.AddDate(100, 0, 0).Unix())
}

// checkInvite 预检邀请链接：不加入，仅取频道概要与当前成员状态。
func (f *Fetcher) checkInvite(ctx context.Context, hash string) (InviteInfo, error) {
	invite, err := f.api.MessagesCheckChatInvite(ctx, hash)
	if err != nil {
		return InviteInfo{}, classifyMembershipError(err)
	}
	switch v := invite.(type) {
	case *tg.ChatInvite:
		return InviteInfo{
			Title:           v.Title,
			Participants:    v.ParticipantsCount,
			IsChannel:       v.Channel,
			RequestedToJoin: v.RequestNeeded,
		}, nil
	case *tg.ChatInviteAlready:
		// 已是成员：从实际 ChatClass 判断频道/普通群，避免把普通群组误标为频道。
		info := inviteInfoFromChat(v.Chat, true)
		if ch, ok := v.Chat.(*tg.Channel); ok {
			_ = f.cache.putBatch([]tg.ChatClass{ch})
			_ = f.cache.save()
		}
		return info, nil
	case *tg.ChatInvitePeek:
		// 仅可窥视的频道：按实际 ChatClass 返回定位（无法读历史）。
		return inviteInfoFromChat(v.Chat, false), nil
	default:
		return InviteInfo{}, apperr.New(apperr.CodeInvalidURL, "邀请链接指向未知的聊天形态")
	}
}

// inviteInfoFromChat 从邀请响应携带的实际聊天对象提取概要。超级群组同广播
// 频道一样由 tg.Channel 表示；普通群组是 tg.Chat，必须保持 IsChannel=false。
func inviteInfoFromChat(chat tg.ChatClass, alreadyJoined bool) InviteInfo {
	info := InviteInfo{AlreadyJoined: alreadyJoined}
	switch ch := chat.(type) {
	case *tg.Channel:
		info.Title = ch.Title
		info.IsChannel = true
		info.ChannelID = ch.ID
		info.AccessHash = ch.AccessHash
	case *tg.Chat:
		info.Title = ch.Title
	}
	return info
}

// joinInvite 通过邀请链接加入频道，按 opts 执行加入后动作。
// 返回加入结果：alreadyJoined 为 true 表示此前已是成员（未重复加入）。
func (f *Fetcher) joinInvite(ctx context.Context, hash string, opts JoinOptions) (ch tg.Channel, alreadyJoined bool, err error) {
	result, err := f.api.MessagesImportChatInvite(ctx, hash)
	if err != nil {
		if tgerr.Is(err, "INVITE_REQUEST_SENT") {
			// 该链接开启了"加入需管理员审核"：已发出加入请求，账号尚未成为成员
			return tg.Channel{}, false, ErrJoinRequestSent
		}
		if tgerr.Is(err, "USER_ALREADY_PARTICIPANT") {
			// 已是成员：从 checkChatInvite 拿频道信息补全标题
			info, cerr := f.checkInvite(ctx, hash)
			if cerr != nil || !info.AlreadyJoined {
				return tg.Channel{}, true, apperr.New(apperr.CodeChannelInaccessible,
					"账号已是该频道成员，但获取频道信息失败")
			}
			return tg.Channel{ID: info.ChannelID, Title: info.Title}, true, nil
		}
		return tg.Channel{}, false, classifyMembershipError(err)
	}

	var chats []tg.ChatClass
	switch r := result.(type) {
	case *tg.MessagesChatInviteJoinResultOk:
		chats = chatsOfUpdates(r.Updates)
	default:
		// WebView 形态（需要跳转确认）不支持，按无效链接处理
		return tg.Channel{}, false, apperr.New(apperr.CodeInvalidURL,
			"该邀请链接需要在客户端确认，暂不支持")
	}

	target, ok := firstChannel(chats)
	if !ok {
		return tg.Channel{}, false, apperr.New(apperr.CodeInternal,
			"加入成功但未能从结果中定位频道对象")
	}

	// 收割 peer 缓存：加入后第一条消息链接立即可用，无需再遍历对话列表
	_ = f.cache.putBatch([]tg.ChatClass{&target})
	_ = f.cache.save()

	f.ApplyPostJoin(ctx, target.ID, target.AccessHash, opts)
	return target, false, nil
}

// ApplyPostJoin 执行加入后动作（静音 + 归档），失败只记日志不外溢——
// 均不影响加入结果本身。请求制频道在频道侧批准后的懒执行路径也复用此方法。
func (f *Fetcher) ApplyPostJoin(ctx context.Context, channelID, accessHash int64, opts JoinOptions) {
	if opts.Mute {
		if err := f.muteChannel(ctx, channelID, accessHash); err != nil {
			f.log.Warn("加入后静音失败（不影响加入结果）", "channel_id", channelID, "error", err.Error())
		}
	}
	if opts.Archive {
		if err := f.archiveChannel(ctx, channelID, accessHash); err != nil {
			f.log.Warn("加入后归档失败（不影响加入结果）", "channel_id", channelID, "error", err.Error())
		}
	}
}

// muteChannel 把频道通知静音到远期（等效客户端"永久静音"）。
func (f *Fetcher) muteChannel(ctx context.Context, channelID, accessHash int64) error {
	settings := tg.InputPeerNotifySettings{}
	settings.SetMuteUntil(muteUntilFar(time.Now()))
	_, err := f.api.AccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{
		Peer:     &tg.InputNotifyPeer{Peer: &tg.InputPeerChannel{ChannelID: channelID, AccessHash: accessHash}},
		Settings: settings,
	})
	return err
}

// archiveRetryDelays 归档重试退避：importChatInvite 返回后对话在服务端有
// 传播窗口，立即 editPeerFolders 会以 PEER_ID_INVALID/CHANNEL_INVALID 失败，
// 需等对话建立后重试。
var archiveRetryDelays = []time.Duration{time.Second, 3 * time.Second}

// archiveFolderID Telegram 归档夹的 folder ID（主列表为 0）。
const archiveFolderID = 1

// archiveChannel 把对话移入归档夹（folder 1）。已在归档夹时直接成功
// （幂等，实时/对账路径重复触发不产生额外写调用）。
// 归档要求对话已存在于账号的对话列表；加入后立即调用可能尚未就绪，
// 故先经 messages.getPeerDialogs 确认，未就绪按退避重试。
func (f *Fetcher) archiveChannel(ctx context.Context, channelID, accessHash int64) error {
	peer := &tg.InputPeerChannel{ChannelID: channelID, AccessHash: accessHash}
	var lastErr error
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(archiveRetryDelays[attempt-1]):
			}
		}
		folder, found, err := f.dialogFolder(ctx, peer, channelID)
		switch {
		case err != nil:
			lastErr = err // 查询失败（限流等）：按可重试处理
		case !found:
			lastErr = fmt.Errorf("频道 %d 的对话尚未建立", channelID)
		case folder == archiveFolderID:
			return nil
		default:
			if _, err := f.api.FoldersEditPeerFolders(ctx, []tg.InputFolderPeer{
				{Peer: peer, FolderID: archiveFolderID},
			}); err != nil {
				lastErr = classifyMembershipError(err)
			} else {
				return nil
			}
		}
		if attempt >= len(archiveRetryDelays) {
			return lastErr
		}
	}
}

// dialogFolder 返回频道对话所在 folder（0 主列表 / 1 归档夹）；found 为
// false 表示对话尚未出现在账号对话列表中（getPeerDialogs）。
func (f *Fetcher) dialogFolder(ctx context.Context, peer tg.InputPeerClass, channelID int64) (folder int, found bool, err error) {
	dialogs, err := f.api.MessagesGetPeerDialogs(ctx,
		[]tg.InputDialogPeerClass{&tg.InputDialogPeer{Peer: peer}})
	if err != nil {
		return 0, false, err
	}
	for _, d := range dialogs.Dialogs {
		if dlg, ok := d.(*tg.Dialog); ok {
			if pc, ok := dlg.Peer.(*tg.PeerChannel); ok && pc.ChannelID == channelID {
				if folderID, has := dlg.GetFolderID(); has {
					return folderID, true, nil
				}
				return 0, true, nil
			}
		}
	}
	return 0, false, nil
}

// leaveChannel 退出频道：优先用 peer 缓存的 accessHash，未命中时遍历对话列表收割。
func (f *Fetcher) leaveChannel(ctx context.Context, channelID int64) error {
	hash, ok := f.cache.get(channelID)
	if !ok {
		f.log.Info("peer 缓存未命中，遍历对话列表定位频道", "channel_id", channelID)
		if err := f.walkDialogs(ctx); err != nil {
			return err
		}
		_ = f.cache.save()
		hash, ok = f.cache.get(channelID)
		if !ok {
			return apperr.New(apperr.CodeChannelInaccessible,
				fmt.Sprintf("账号未加入频道 %d，无需退出", channelID))
		}
	}
	_, err := f.api.ChannelsLeaveChannel(ctx, &tg.InputChannel{ChannelID: channelID, AccessHash: hash})
	if err != nil {
		if tgerr.Is(err, "USER_CREATOR") {
			return ErrChannelCreator
		}
		return classifyMembershipError(err)
	}
	return nil
}

// listJoined 遍历对话列表（主列表 + 归档夹），返回当前账号加入的全部
// 频道与超级群组（普通群组不在 /join 范围内，不返回）。
// 同时顺带收割全部 peer hash（复用 walkDialogs），失败不阻断列表。
func (f *Fetcher) listJoined(ctx context.Context) ([]JoinedChannel, error) {
	if err := f.walkDialogs(ctx); err != nil {
		return nil, err
	}
	_ = f.cache.save()

	seen := map[int64]JoinedChannel{}
	f.forEachCachedChannel(func(id, hash int64) {
		seen[id] = JoinedChannel{ChannelID: id, AccessHash: hash}
	})
	// walkDialogs 只收割 hash；标题/username/creator 需要从 chats 原始数据取，
	// 重新拉一遍 dialogs 拿原始 chats（无分页游标时一次拿全）。
	channels, err := f.collectJoinedChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]JoinedChannel, 0, len(channels))
	for _, c := range channels {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })
	return out, nil
}

// forEachCachedChannel 遍历 peer 缓存中的全部频道 hash。
func (f *Fetcher) forEachCachedChannel(fn func(id, hash int64)) {
	f.cache.mu.Lock()
	defer f.cache.mu.Unlock()
	for id, hash := range f.cache.hashes {
		fn(id, hash)
	}
}

// collectJoinedChannels 拉取对话列表（两个 folder）并提取频道/超级群信息。
func (f *Fetcher) collectJoinedChannels(ctx context.Context) ([]JoinedChannel, error) {
	var out []JoinedChannel
	seen := map[int64]bool{}
	for _, folder := range []int{0, 1} {
		offsetDate, offsetID := 0, 0
		var offsetPeer tg.InputPeerClass = &tg.InputPeerEmpty{}
		for page := 0; ; page++ {
			req := tg.MessagesGetDialogsRequest{
				Limit:      100,
				OffsetDate: offsetDate,
				OffsetID:   offsetID,
				OffsetPeer: offsetPeer,
			}
			if folder > 0 {
				req.SetFolderID(folder)
			}
			resp, err := f.api.MessagesGetDialogs(ctx, &req)
			if err != nil {
				return nil, classifyMembershipError(err)
			}
			var dialogList []tg.DialogClass
			var messages []tg.MessageClass
			var chats []tg.ChatClass
			switch r := resp.(type) {
			case *tg.MessagesDialogs:
				dialogList, messages, chats = r.Dialogs, r.Messages, r.Chats
			case *tg.MessagesDialogsSlice:
				dialogList, messages, chats = r.Dialogs, r.Messages, r.Chats
			default:
				return out, nil
			}

			// 只保留出现在对话列表中的频道（= 当前仍加入的），并记录其
			// 所在 folder（0 主列表 / 1 归档夹）
			dialogPeers := map[int64]bool{}
			archivedPeers := map[int64]bool{}
			for _, d := range dialogList {
				if dlg, ok := d.(*tg.Dialog); ok {
					if pc, ok := dlg.Peer.(*tg.PeerChannel); ok {
						dialogPeers[pc.ChannelID] = true
						if folderID, has := dlg.GetFolderID(); has && folderID == archiveFolderID {
							archivedPeers[pc.ChannelID] = true
						}
					}
				}
			}
			for _, c := range chats {
				ch, ok := c.(*tg.Channel)
				if !ok || !dialogPeers[ch.ID] {
					continue
				}
				if seen[ch.ID] {
					continue
				}
				seen[ch.ID] = true
				kind := "supergroup"
				if ch.Broadcast {
					kind = "channel"
				}
				out = append(out, JoinedChannel{
					ChannelID:  ch.ID,
					AccessHash: ch.AccessHash,
					Title:      ch.Title,
					Username:   ch.Username,
					Kind:       kind,
					Creator:    ch.Creator,
					Archived:   archivedPeers[ch.ID],
				})
			}

			// 翻页（与 walkFolder 相同的游标逻辑）
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
			if !moved || len(dialogList) < 100 {
				break
			}
			if page > 20 {
				break
			}
		}
	}
	return out, nil
}

// HarvestChats 把一批 chats（如加入结果 Updates 里的）收割进 peer 缓存并落盘。
// 公开给 joinmgr 在审批通过等场景复用。
func (f *Fetcher) HarvestChats(chats []tg.ChatClass) int {
	n := f.cache.putBatch(chats)
	if n > 0 {
		_ = f.cache.save()
	}
	return n
}

// HarvestNovelChannels 过滤出 peer 缓存中未见的频道（"新见"），收割其 hash
// 进缓存并落盘，返回新见频道的定位信息。update 实时路径（被拉入频道归档）
// 用它跳过已知频道——否则每条频道消息都会触发一次留痕查询与归档调用。
// 注意：本系统刚加入的频道已在 joinInvite 内收割，同样会被过滤。
func (f *Fetcher) HarvestNovelChannels(channels []tg.Channel) []JoinedChannel {
	var out []JoinedChannel
	var novel []tg.ChatClass
	for i := range channels {
		ch := &channels[i]
		if _, ok := f.cache.get(ch.ID); ok {
			continue
		}
		kind := "supergroup"
		if ch.Broadcast {
			kind = "channel"
		}
		out = append(out, JoinedChannel{
			ChannelID:  ch.ID,
			AccessHash: ch.AccessHash,
			Title:      ch.Title,
			Username:   ch.Username,
			Kind:       kind,
			Creator:    ch.Creator,
		})
		novel = append(novel, ch)
	}
	if len(novel) > 0 {
		_ = f.cache.putBatch(novel)
		_ = f.cache.save()
	}
	return out
}

// MembershipBridge 是生命周期感知的频道成员管理桥接器（仿 ProfileLookup）：
// SetAPI/Clear 由 Client.Run 的 ready 生命周期调用，Web/botapi 不接触 gotd 指针。
type MembershipBridge struct {
	mu      sync.RWMutex
	api     *tg.Client
	fetcher *Fetcher
}

// NewMembershipBridge 创建空桥接器。
func NewMembershipBridge() *MembershipBridge { return &MembershipBridge{} }

// SetAPI 绑定当前 ready 生命周期的 API 客户端与取数器。
func (b *MembershipBridge) SetAPI(api *tg.Client, fetcher *Fetcher) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.api = api
	b.fetcher = fetcher
}

// Clear 清除已结束生命周期的绑定。
func (b *MembershipBridge) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.api = nil
	b.fetcher = nil
}

// Available 报告当前是否有可用的用户号上下文。
func (b *MembershipBridge) Available() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.api != nil && b.fetcher != nil
}

// CheckInvite 预检邀请链接（不加入）。
func (b *MembershipBridge) CheckInvite(ctx context.Context, hash string) (InviteInfo, error) {
	f, release, err := b.acquire()
	if err != nil {
		return InviteInfo{}, err
	}
	defer release()
	return f.checkInvite(ctx, hash)
}

// JoinInvite 通过邀请链接加入频道并执行加入后动作。
// 返回频道 ID 与标题；alreadyJoined 为 true 表示此前已是成员（未重复加入），
// 此时通过 CheckInvite 回查并返回已有频道定位。
func (b *MembershipBridge) JoinInvite(ctx context.Context, hash string, opts JoinOptions) (channelID int64, title string, alreadyJoined bool, err error) {
	f, release, err := b.acquire()
	if err != nil {
		return 0, "", false, err
	}
	defer release()
	ch, already, err := f.joinInvite(ctx, hash, opts)
	if err != nil {
		return 0, "", false, err
	}
	return ch.ID, ch.Title, already, nil
}

// ApplyPostJoin 对已确定的频道执行加入后动作（静音/归档）。
func (b *MembershipBridge) ApplyPostJoin(ctx context.Context, channelID, accessHash int64, opts JoinOptions) error {
	f, release, err := b.acquire()
	if err != nil {
		return err
	}
	defer release()
	f.ApplyPostJoin(ctx, channelID, accessHash, opts)
	return nil
}

// LeaveChannel 退出频道。
func (b *MembershipBridge) LeaveChannel(ctx context.Context, channelID int64) error {
	f, release, err := b.acquire()
	if err != nil {
		return err
	}
	defer release()
	return f.leaveChannel(ctx, channelID)
}

// ListJoined 列出已加入的频道与超级群组。
func (b *MembershipBridge) ListJoined(ctx context.Context) ([]JoinedChannel, error) {
	f, release, err := b.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	return f.listJoined(ctx)
}

// acquire 取当前绑定的 fetcher；返回的 release 为 nil 安全占位（当前实现
// 读锁覆盖调用全程，Clear 会等待进行中的调用结束后再让生命周期退出）。
func (b *MembershipBridge) acquire() (*Fetcher, func(), error) {
	b.mu.RLock()
	if b.api == nil || b.fetcher == nil {
		b.mu.RUnlock()
		return nil, func() {}, ErrMembershipUnavailable
	}
	return b.fetcher, func() { b.mu.RUnlock() }, nil
}

// classifyMembershipError 把成员管理路径的 Telegram 错误归类为受控 AppError。
func classifyMembershipError(err error) *apperr.AppError {
	switch {
	case tgerr.Is(err, "INVITE_HASH_EMPTY", "INVITE_HASH_EXPIRED", "INVITE_HASH_INVALID"):
		return apperr.Wrap(apperr.CodeInvalidURL, err)
	case tgerr.Is(err, "INVITE_REQUEST_SENT"):
		return apperr.Wrap(apperr.CodeChannelInaccessible, err)
	case tgerr.Is(err, "CHANNEL_PRIVATE", "CHANNEL_PUBLIC_GROUP_NA", "CHAT_ADMIN_REQUIRED", "CHAT_NOT_FOUND", "USER_ALREADY_PARTICIPANT", "USER_BANNED_IN_CHANNEL"):
		return apperr.Wrap(apperr.CodeChannelInaccessible, err)
	case tgerr.Is(err, "FLOOD_WAIT_X", "FLOOD_PREMIUM_WAIT_X", "SLOWMODE_WAIT_X"):
		return apperr.Wrap(apperr.CodeRateLimited, err)
	case tgerr.Is(err, "PEER_FLOOD", "INVITE_PEER_FLOOD"):
		// 加入路径的账号级限制：与普通限流不同，通常持续数小时
		return apperr.Wrap(apperr.CodePeerFlood, err)
	case tgerr.IsCode(err, 500):
		return apperr.Wrap(apperr.CodeTelegramServer, err)
	case apperr.IsTransportFailure(err):
		return apperr.Wrap(apperr.CodeNetworkError, err)
	default:
		return apperr.Wrap(apperr.CodeInternal, err)
	}
}

// chatsOfUpdates 从 Updates 族响应中提取 chats 列表。
func chatsOfUpdates(cls tg.UpdatesClass) []tg.ChatClass {
	switch u := cls.(type) {
	case *tg.Updates:
		return u.Chats
	case *tg.UpdatesCombined:
		return u.Chats
	case *tg.UpdatesTooLong:
		return nil
	default:
		return nil
	}
}

// firstChannel 取列表中第一个 Channel（加入结果只含目标频道，取首个即可）。
func firstChannel(chats []tg.ChatClass) (tg.Channel, bool) {
	for _, c := range chats {
		if ch, ok := c.(*tg.Channel); ok && ch != nil {
			return *ch, true
		}
	}
	return tg.Channel{}, false
}
