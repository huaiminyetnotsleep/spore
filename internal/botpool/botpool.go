// Package botpool 持有多机器人池的运行时成员表：每个 bot 的身份、Bot API
// 客户端、发送路由与大文件直传会话。装配层（cmd/bot）在 MTProto ready 生命
// 周期内重建成员（Reset）；业务侧按 bot id 解析发送通道（worker 按 Job.BotID，
// 通知/审批按用户最近活跃的 bot），未命中（bot 已下线/存量请求）回退主 bot。
// 池内只持 token 与脱敏身份；任何日志与接口响应不得包含 token。
package botpool

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	tgbot "github.com/go-telegram/bot"

	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/message"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
)

// Member 是池内单个 bot 的运行时成员。Sender 是该 bot 的完整发送路由
// （Bot API 与 MTProto 大文件直传分流，已包事件计数），RawSender 是未经
// 计数的原始通道（通知类注入用，避免"通知失败→产生事件→再通知"自激）。
type Member struct {
	ID        int64
	Username  string
	Name      string
	Token     string // 绝不入日志
	BotAPI    *tgbot.Bot
	Sender    delivery.Sender
	RawSender delivery.Sender
	BotClient *mtproto.BotClient

	// conflict 标记消息拉取冲突（token 被 webhook 或其他轮询实例占用）：
	// 该 bot 收不到新消息，但发送通道不受影响。由装配层在轮询错误/启动
	// 探测时置位、收到该 bot 的 update 时清除。
	conflict atomic.Bool
}

// SetConflict 更新冲突标记，返回是否发生了状态变化（true = 进入或退出
// 冲突态，调用方据此触发一次事件/恢复，避免逐错误刷库）。
func (m *Member) SetConflict(conflicted bool) bool {
	if conflicted {
		return m.conflict.CompareAndSwap(false, true)
	}
	return m.conflict.CompareAndSwap(true, false)
}

// Conflict 返回当前冲突态。
func (m *Member) Conflict() bool { return m.conflict.Load() }

// Snapshot 是成员的脱敏快照（Web 总览/身份展示用，不含 token 与客户端）。
type Snapshot struct {
	ID       int64
	Name     string
	Username string
	Online   bool // Bot API 长轮询是否在线（ready 生命周期内置 true）
	Conflict bool // 消息拉取冲突（token 被其他服务占用；收不到新消息）
}

// Pool 是线程安全的 bot 成员表 + 用户最近活跃路由表。
type Pool struct {
	mu      sync.RWMutex
	members []*Member
	online  bool

	activeMu sync.RWMutex
	active   map[int64]int64 // userID → 最近活跃 botID（通知路由；未命中回退主 bot）
}

// New 创建空池。
func New() *Pool {
	return &Pool{active: map[int64]int64{}}
}

// Reset 整体替换成员表并标记全部在线（ready 生命周期重建时调用；
// 旧成员随生命周期结束已被停止，此处只做指针替换）。
func (p *Pool) Reset(members []*Member) {
	p.mu.Lock()
	p.members = members
	p.online = true
	p.mu.Unlock()
}

// MarkAllOffline 把在线标记置 false（ready 生命周期结束、长轮询随 ctx 停止；
// 身份快照保留，总览页显示离线态而非"未接入"）。
func (p *Pool) MarkAllOffline() {
	p.mu.Lock()
	p.online = false
	p.mu.Unlock()
}

// Snapshots 返回全部成员的脱敏快照（装配顺序，主 bot 在前）。
func (p *Pool) Snapshots() []Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]Snapshot, 0, len(p.members))
	for _, m := range p.members {
		out = append(out, Snapshot{
			ID: m.ID, Name: m.Name, Username: m.Username,
			Online: p.online, Conflict: m.Conflict(),
		})
	}
	return out
}

// MemberByID 返回指定 bot 的成员（精确匹配，不回退主 bot）；未命中返回 nil。
func (p *Pool) MemberByID(botID int64) *Member {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if botID != 0 {
		for _, m := range p.members {
			if m.ID == botID {
				return m
			}
		}
	}
	return nil
}

// Empty 报告池内是否没有成员。
func (p *Pool) Empty() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.members) == 0
}

// SenderFor 返回指定 bot 的发送路由；未命中（已下线/存量请求/网络重建窗口）
// 回退主 bot，池为空返回 nil（调用方按通道不可用处理）。
func (p *Pool) SenderFor(botID int64) delivery.Sender {
	m := p.memberFor(botID)
	if m == nil {
		return nil
	}
	return m.Sender
}

// BotAPIFor 返回指定 bot 的 Bot API 客户端；未命中回退主 bot。
func (p *Pool) BotAPIFor(botID int64) (*tgbot.Bot, bool) {
	m := p.memberFor(botID)
	if m == nil {
		return nil, false
	}
	return m.BotAPI, true
}

// BotAPIs 返回全部 Bot API 客户端（装配顺序，主 bot 在前）。
func (p *Pool) BotAPIs() []*tgbot.Bot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*tgbot.Bot, 0, len(p.members))
	for _, m := range p.members {
		out = append(out, m.BotAPI)
	}
	return out
}

// NoteActive 记录用户最近活跃的 bot（botapi handler 收到私聊消息时调用），
// 后续对该用户的主动通知（审批结果/事件/加入审批）路由到同一 bot。
func (p *Pool) NoteActive(userID, botID int64) {
	if userID == 0 || botID == 0 {
		return
	}
	p.activeMu.Lock()
	p.active[userID] = botID
	p.activeMu.Unlock()
}

// SenderForUser 返回应向该用户发消息的发送路由：最近活跃 bot 优先，未命中
// 或已下线回退主 bot；池为空返回 nil。审批/事件/加入通知通道经 UserRouter
// 间接使用本方法（私聊 chatID 即用户 ID）。
func (p *Pool) SenderForUser(userID int64) delivery.Sender {
	p.activeMu.RLock()
	botID := p.active[userID]
	p.activeMu.RUnlock()
	if m := p.memberFor(botID); m != nil {
		return m.Sender
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.members) == 0 {
		return nil
	}
	return p.members[0].Sender
}

// RawSenderForUser 同 SenderForUser，但返回未经事件计数的原始通道
// （事件中心/加入审批通知注入用，避免自激回路）。
func (p *Pool) RawSenderForUser(userID int64) delivery.Sender {
	p.activeMu.RLock()
	botID := p.active[userID]
	p.activeMu.RUnlock()
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.members) == 0 {
		return nil
	}
	if m := p.memberFor(botID); m != nil {
		return m.RawSender
	}
	return p.members[0].RawSender
}

// memberFor 按 ID 查找成员；botID 非法或未命中时返回主 bot（首项）。
func (p *Pool) memberFor(botID int64) *Member {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if len(p.members) == 0 {
		return nil
	}
	if botID != 0 {
		for _, m := range p.members {
			if m.ID == botID {
				return m
			}
		}
	}
	return p.members[0]
}

// UserRouter 实现 delivery.Sender：每次调用按目标私聊（chatID 即用户 ID）
// 实时解析该用户归属 bot 的通道，供持有单一 Sender 槽位的服务（access 审批
// 通知、hub 事件通知、joinmgr 加入审批）在多机器人池下正确路由。
// counted 为 true 时路由到已计数通道（业务计数语义），false 时原始通道。
type UserRouter struct {
	Pool    *Pool
	Counted bool
}

func (r UserRouter) senderFor(chatID int64) delivery.Sender {
	if r.Counted {
		return r.Pool.SenderForUser(chatID)
	}
	return r.Pool.RawSenderForUser(chatID)
}

// SendMessage 以 HTML parse_mode 发送文本。
func (r UserRouter) SendMessage(ctx context.Context, chatID int64, html string) (int, error) {
	snd := r.senderFor(chatID)
	if snd == nil {
		return 0, delivery.ErrPoolUnavailable
	}
	return snd.SendMessage(ctx, chatID, html)
}

// EditMessageText 以 HTML parse_mode 编辑既有文本消息。
func (r UserRouter) EditMessageText(ctx context.Context, chatID int64, messageID int, html string) error {
	snd := r.senderFor(chatID)
	if snd == nil {
		return delivery.ErrPoolUnavailable
	}
	return snd.EditMessageText(ctx, chatID, messageID, html)
}

// SendMedia 按媒体类型分发发送（路由通道只用于私聊通知，极少走到媒体路径）。
func (r UserRouter) SendMedia(ctx context.Context, chatID int64, m message.Media, caption message.Caption, reader io.Reader) (int, error) {
	snd := r.senderFor(chatID)
	if snd == nil {
		return 0, delivery.ErrPoolUnavailable
	}
	return snd.SendMedia(ctx, chatID, m, caption, reader)
}

// SendAlbum 整组发送相册。
func (r UserRouter) SendAlbum(ctx context.Context, chatID int64, entries []delivery.AlbumEntry) ([]int, error) {
	snd := r.senderFor(chatID)
	if snd == nil {
		return nil, delivery.ErrPoolUnavailable
	}
	return snd.SendAlbum(ctx, chatID, entries)
}

// AlbumGroupable 判断媒体能否整组发送（纯元数据判定，路由到主 bot 即可）。
func (r UserRouter) AlbumGroupable(m message.Media) bool {
	snd := r.senderFor(0)
	if snd == nil {
		return false
	}
	return snd.AlbumGroupable(m)
}

// CopyMessages 从 fromChatID 整组复制消息到 chatID。
func (r UserRouter) CopyMessages(ctx context.Context, fromChatID, chatID int64, messageIDs []int) ([]int, error) {
	snd := r.senderFor(chatID)
	if snd == nil {
		return nil, delivery.ErrPoolUnavailable
	}
	return snd.CopyMessages(ctx, fromChatID, chatID, messageIDs)
}

// CopyMessage 单条复制并覆盖 caption。
func (r UserRouter) CopyMessage(ctx context.Context, fromChatID, chatID int64, messageID int, captionHTML string) (int, error) {
	snd := r.senderFor(chatID)
	if snd == nil {
		return 0, delivery.ErrPoolUnavailable
	}
	return snd.CopyMessage(ctx, fromChatID, chatID, messageID, captionHTML)
}

// EditMessageCaption 编辑既有媒体消息的 caption。
func (r UserRouter) EditMessageCaption(ctx context.Context, chatID int64, messageID int, captionHTML string) error {
	snd := r.senderFor(chatID)
	if snd == nil {
		return delivery.ErrPoolUnavailable
	}
	return snd.EditMessageCaption(ctx, chatID, messageID, captionHTML)
}

// DeleteMessage 删除一条 bot 自己发出的消息。
func (r UserRouter) DeleteMessage(ctx context.Context, chatID int64, messageID int) error {
	snd := r.senderFor(chatID)
	if snd == nil {
		return delivery.ErrPoolUnavailable
	}
	return snd.DeleteMessage(ctx, chatID, messageID)
}
