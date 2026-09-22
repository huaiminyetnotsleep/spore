// 接入机器人身份（Web 总览页展示）：Bot 客户端在 MTProto 就绪后才创建
// （重连会重建），晚于 Web 管理端启动。身份直接读自多机器人池的成员快照
// （pool.Reset 时整体刷新），不再单独缓存；池为空时总览页显示"未接入"。
package main

import (
	"github.com/huaiminyetnotsleep/spore/internal/botpool"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/web"
)

// botIdentityStore 适配 botpool.Pool → web.BotIdentityProvider：只暴露
// id/name/username 脱敏字段，绝不落 token。
type botIdentityStore struct {
	pool *botpool.Pool
}

// botMTProtoStore 适配逐 bot 的 MTProto 会话 → web.BotMTProtoStatuses：
// 按 pool 装配顺序输出（主 bot 在前），只含脱敏状态快照。
type botMTProtoStore struct {
	pool    *botpool.Pool
	clients map[int64]*mtproto.BotClient
}

// BotMTProtoEntries 返回池内全部 bot 的直传会话状态快照。
func (s botMTProtoStore) BotMTProtoEntries() []web.BotMTProtoEntry {
	snaps := s.pool.Snapshots()
	out := make([]web.BotMTProtoEntry, 0, len(snaps))
	for _, sn := range snaps {
		c := s.clients[sn.ID]
		if c == nil {
			continue
		}
		out = append(out, web.BotMTProtoEntry{BotID: sn.ID, Username: sn.Username, Snapshot: c.Status()})
	}
	return out
}

func newBotIdentityStore(pool *botpool.Pool) *botIdentityStore {
	return &botIdentityStore{pool: pool}
}

// BotIdentity 返回主 bot（池首项）身份；池为空返回 false（"未接入"）。
func (s *botIdentityStore) BotIdentity() (web.BotIdentity, bool) {
	snaps := s.pool.Snapshots()
	if len(snaps) == 0 {
		return web.BotIdentity{}, false
	}
	first := snaps[0]
	return web.BotIdentity{ID: first.ID, Name: first.Name, Username: first.Username}, true
}

// BotIdentities 返回池内全部成员（装配顺序，主 bot 在前）。
func (s *botIdentityStore) BotIdentities() []web.BotIdentityEntry {
	snaps := s.pool.Snapshots()
	out := make([]web.BotIdentityEntry, 0, len(snaps))
	for i, sn := range snaps {
		out = append(out, web.BotIdentityEntry{
			BotIdentity: web.BotIdentity{ID: sn.ID, Name: sn.Name, Username: sn.Username},
			Primary:     i == 0,
			Online:      sn.Online,
			Conflict:    sn.Conflict,
			Disabled:    sn.Disabled,
		})
	}
	return out
}
