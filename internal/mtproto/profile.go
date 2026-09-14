package mtproto

import (
	"context"
	"errors"
	"sync"

	"github.com/go-telegram/bot/models"
	"github.com/gotd/td/tg"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// ErrProfileUnavailable 表示当前没有可安全使用的用户上下文（客户端离线或缺少 access hash）。
var ErrProfileUnavailable = errors.New("mtproto: 没有可用的用户资料上下文")

// ProfileLookup 是生命周期感知的用户资料查询桥接器。
// SetAPI/Clear 由 Client.Run 的 ready 生命周期调用，Web 不接触 gotd 客户端指针。
type ProfileLookup struct {
	mu        sync.RWMutex
	api       *tg.Client
	hashes    map[int64]int64
	usernames map[int64]string
}

// NewProfileLookup 创建空的用户资料查询桥接器。
func NewProfileLookup() *ProfileLookup {
	return &ProfileLookup{hashes: make(map[int64]int64), usernames: make(map[int64]string)}
}

// SetAPI 设置当前 ready 生命周期的 API 客户端。
func (p *ProfileLookup) SetAPI(api *tg.Client) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.api = api
}

// Clear 清除已结束生命周期的 API 客户端。
func (p *ProfileLookup) Clear() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.api = nil
}

// ObserveUser 记录 Bot 已见过的用户资料；后续刷新只能使用这个已知 username。
func (p *ProfileLookup) ObserveUser(u models.User) {
	if u.ID <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if u.Username == "" {
		delete(p.usernames, u.ID)
		return
	}
	p.usernames[u.ID] = u.Username
}

// SetUserAccessHash 记录已有上下文提供的 Telegram 用户 access hash。
func (p *ProfileLookup) SetUserAccessHash(id, accessHash int64) {
	if id <= 0 || accessHash == 0 {
		return
	}
	p.mu.Lock()
	p.hashes[id] = accessHash
	p.mu.Unlock()
}

// LookupUserProfile 只查询已有 access hash 或 Bot 已观察 username 的用户，
// 不承诺通过裸 User ID 枚举 Telegram 用户。
func (p *ProfileLookup) LookupUserProfile(ctx context.Context, id int64) (store.UserProfile, error) {
	// 读锁覆盖整个 Telegram 调用，Clear 会等待请求结束后再让生命周期退出。
	p.mu.RLock()
	defer p.mu.RUnlock()
	api := p.api
	hash, hasHash := p.hashes[id]
	username := p.usernames[id]
	if api == nil {
		return store.UserProfile{}, ErrProfileUnavailable
	}
	if hasHash {
		users, err := api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: id, AccessHash: hash}})
		if err != nil {
			return store.UserProfile{}, err
		}
		if profile := profileFromUsers(users, id); profile.ID != 0 {
			return profile, nil
		}
	}
	if username == "" {
		return store.UserProfile{}, ErrProfileUnavailable
	}
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: username})
	if err != nil {
		return store.UserProfile{}, err
	}
	if profile := profileFromUsers(resolved.Users, id); profile.ID != 0 {
		return profile, nil
	}
	return store.UserProfile{}, ErrProfileUnavailable
}

func profileFromUsers(users []tg.UserClass, id int64) store.UserProfile {
	for _, item := range users {
		if u, ok := item.(*tg.User); ok && u.ID == id {
			return store.UserProfile{ID: u.ID, Username: u.Username, DisplayName: userDisplayName(u)}
		}
	}
	return store.UserProfile{}
}

func userDisplayName(u *tg.User) string {
	if u == nil {
		return ""
	}
	if u.LastName == "" {
		return u.FirstName
	}
	if u.FirstName == "" {
		return u.LastName
	}
	return u.FirstName + " " + u.LastName
}
