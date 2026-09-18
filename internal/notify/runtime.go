package notify

import (
	"context"
	"time"
)

// EventNotification 是已经过事件目录约束的通知上下文。
// 动态内容只能来自受控事件定义与事件源上报的 payload（经模板渲染）；
// 通道实现不得把底层错误或敏感值加入消息。
type EventNotification struct {
	SourceName string
	TypeCode   string
	TypeLabel  string
	Category   string
	Severity   string
	EventType  string
	Title      string
	Body       string
	Count      int
	OccurredAt time.Time
	Recovery   bool
	// Activity 表示逐次活动通知：渲染时省略"已发生 N 次"脚注
	// （登录/申请等每次发生都独立投递，次数无意义）。
	Activity bool
}

// RuntimeDeliveryResult 描述一次配置通道投递尝试。
// Managed 表示配置通道已接管事件投递；即使没有任何通道成功，Hub
// 也不应再回退到旧的 owner 通知，避免用户明确关闭通知后仍收到消息。
type RuntimeDeliveryResult struct {
	Managed   bool
	Attempted int
	Delivered int
	Failed    int
}

// RuntimeNotifier 是 Hub 对已配置运行时通知通道的最小依赖。
// notify 不依赖 notifycfg，具体通道和策略由装配层注入的实现负责。
type RuntimeNotifier interface {
	NotifyEvent(context.Context, EventNotification) (RuntimeDeliveryResult, error)
}
