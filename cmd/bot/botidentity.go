// 接入机器人身份（Web 总览页展示）：Bot 客户端在 MTProto 就绪后才创建
// （重连会重建），晚于 Web 管理端启动，经原子持有器后置注入，由
// onMTProtoReady 内的 getMe 回填。
package main

import (
	"sync/atomic"

	"github.com/huaiminyetnotsleep/spore/internal/web"
)

// botIdentityStore 缓存接入机器人的 getMe 身份（web.BotIdentityProvider）：
// 只存 id/name/username 脱敏字段，绝不落 token。token 固定，Bot 重建时
// 幂等刷新；未回填前总览页显示"未接入"。
type botIdentityStore struct {
	v atomic.Pointer[web.BotIdentity]
}

func (s *botIdentityStore) BotIdentity() (web.BotIdentity, bool) {
	if id := s.v.Load(); id != nil {
		return *id, true
	}
	return web.BotIdentity{}, false
}

func (s *botIdentityStore) set(id int64, name, username string) {
	s.v.Store(&web.BotIdentity{ID: id, Name: name, Username: username})
}
