package notify

// 事件 payload：各事件源在触发点可捕获的关键信息。payload 经模板渲染为
// 通知正文（templates/ 下每种事件一个文件），不写入 events 表——事件中心
// 文案仍使用目录受控描述，关键信息只出现在推送消息里。

// templateData 是所有事件模板的统一渲染上下文。
type templateData struct {
	// Time 是人可读的最近发生时间（已按运营时区格式化，含 GMT 偏移后缀）。
	// 告警取事件行 LastAt（合并后为最近一次发生）；活动通知为发生时刻。
	Time string
	// Payload 是事件专属关键信息；无专属信息的事件为 nil。
	Payload any
}

// ---- 告警事件 payload ----

// StreakData 连续失败型事件（botapi.send_failures / tasks.consecutive_failures /
// cloud.upload_failed）。
type StreakData struct {
	Count     int    // 连续失败次数（达到阈值时触发，通常等于阈值）
	Threshold int    // 告警阈值
	Detail    string // 最近一次失败的上下文（任务为来源链接人可读形式；可为空）
}

// TempUsageData 临时目录占用超限（disk.temp_usage）。
type TempUsageData struct {
	Used  int64 // 当前占用字节数
	Limit int64 // 告警阈值字节数
}

// InterruptedData 启动恢复中断任务（tasks.interrupted_recovered）。
type InterruptedData struct {
	Count int // 被标记为失败（可重试）的任务数
}

// BotIDData bot 维度事件（bot.init_failed / bot.poll_conflict / bot.banned）。
type BotIDData struct {
	BotID int64 // bot 的 token 数字前缀（即 bot 用户 ID）
}

// MTProtoBanData 用户号封禁/会话撤销事件（mtproto.banned）。
type MTProtoBanData struct {
	ErrorKind string // banned = 封号；revoked = 会话失效/撤销
}

// BackupFailData 备份失败事件（backup.failed）。Scene 为受控场景描述
// （定时备份 / CLI 备份 / 磁盘空间不足跳过），不携带错误原文与路径。
type BackupFailData struct {
	Scene string
}

// StoreWriteData 数据库写入失败（store.write_failed）。
type StoreWriteData struct {
	Scene string // 写入场景（提交落库 / 队列满终态 / 任务开始标记 / 任务终态）
}

// ---- 活动通知 payload ----

// AdminLoginData 管理后台登录成功（web.admin_login）。
type AdminLoginData struct {
	Method  string // 登录方式：访问密钥 / GitHub
	IP      string // 客户端 IP（信任反代时取 X-Forwarded-For 末段）
	OS      string // 解析 User-Agent 得到的操作系统；未知为"未知"
	Browser string // 解析 User-Agent 得到的浏览器；未知为"未知"
}

// UserApplicationData 新用户申请（user.application）。
type UserApplicationData struct {
	UserID            int64
	Username          string // Telegram username，可空
	DisplayName       string // Telegram 显示名，可空
	SourceBotUsername string // 受理申请的 bot username，可空
}

// ChannelJoinRequestData 频道加入申请（channel.join_request）。
type ChannelJoinRequestData struct {
	UserID       int64
	Username     string // Telegram username，可空
	DisplayName  string // Telegram 显示名，可空
	ChannelTitle string // 邀请链接指向的频道标题
	Participants int    // 链接携带的参与人数；0 表示未知
}
