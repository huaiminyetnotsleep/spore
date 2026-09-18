package main

// 活动通知适配器：事件源（access/joinmgr）通过各自的本地数据结构上报，
// 由此转换为事件中心 payload，保持依赖方向（事件源不 import notify）。

import (
	"context"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/joinmgr"
	"github.com/huaiminyetnotsleep/spore/internal/notify"
)

// activityHub 把 access / joinmgr 的活动上报适配到事件中心活动通知。
type activityHub struct {
	hub *notify.Hub
}

// UserApplied 实现 access.ActivityNotifier。
func (a activityHub) UserApplied(ctx context.Context, app access.ApplicationInfo) {
	a.hub.UserApplied(ctx, notify.UserApplicationData{
		UserID:            app.UserID,
		Username:          app.Username,
		DisplayName:       app.DisplayName,
		SourceBotUsername: app.SourceBotUsername,
	})
}

// ChannelJoinRequested 实现 joinmgr.ActivityNotifier。
func (a activityHub) ChannelJoinRequested(ctx context.Context, info joinmgr.JoinRequestInfo) {
	a.hub.ChannelJoinRequested(ctx, notify.ChannelJoinRequestData{
		UserID:       info.UserID,
		Username:     info.Username,
		DisplayName:  info.DisplayName,
		ChannelTitle: info.ChannelTitle,
		Participants: info.Participants,
	})
}
