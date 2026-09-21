package botapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/huaiminyetnotsleep/spore/internal/access"
	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/binding"
	"github.com/huaiminyetnotsleep/spore/internal/config"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/joinmgr"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
	"github.com/huaiminyetnotsleep/spore/internal/watch"
)

// helpText 渲染帮助文案：系统名称可配置（internal/syscfg，管理端修改即时生效），
// 其余为固定说明。🦞 是固定品牌符号，不随名称变化。
func helpText(name string) string {
	return `🦞 ` + name + ` — 受保护消息提取

把 Telegram 消息链接发给我，我会以全新消息的形式把内容发回给你（可以正常再次转发）；一条消息可同时发送多个链接。

支持的链接格式：
• https://t.me/username/message_id
• https://t.me/c/internal_id/message_id

可用命令：
/start — 申请使用或查看账号状态
/help — 查看帮助
/status — 查看服务运行状态
/health — 查看服务健康状态
/usage — 查看今日额度
/cancel 链接 — 取消该链接的下载/上传任务
/download 链接 — 把消息媒体下载到网盘（不重发到聊天）
/bind 频道 — 绑定我的频道（先把我拉进频道并设为管理员）
/unbind 频道 — 解绑我的频道
/channels — 查看我绑定的频道
/join 邀请链接 — 请系统账号加入私有频道（t.me/+… 链接）
/watch 频道 — 监听源频道/群组，新消息自动预热缓存（先把我加为该频道/群管理员）
/watch — 查看我的监听源
/unwatch 频道 — 移除我的监听源

绑定频道后，每次提取的内容会在发给你之后同步发送一份到你的频道。

监听源生效后，源内新消息会自动转存一份到缓存频道：之后任何人把该消息链接发给我，都能秒回（无需重新下载上传）。监听源申请是否需要审批由管理员配置。

/download 可指定目的地：/download 目的地 链接；不带目的地时使用默认目的地。可用目的地由管理员配置；纯文本消息不支持网盘下载。

要求：我的用户账号需要能访问来源频道。`
}

const (
	textOnlyMsg = "现阶段只支持发送文字消息链接。"
	// statusPrompt 与 queue.StatusPromptHTML 同源：worker 的进度编辑以它为
	// 前缀覆写占位消息（常量定义在 queue——botapi 已依赖 queue，不能反向）。
	statusPrompt = queue.StatusPromptHTML
	// cloudStatusPrompt 同源规则：云盘任务占位文案，worker 的云盘进度编辑
	// 以它为前缀（区别于普通任务的"正在获取消息..."）。
	cloudStatusPrompt = queue.StatusCloudPromptHTML
	multiCancelNotice = "一次只取消一条链接，已取第一条有效链接。"
)

// updateHandler 默认处理器：私聊文本 + 监听源消息。
// 私聊的白名单与额度校验经访问控制服务（access）完成，再分流命令与链接
// 入队；监听源消息（channel_post 与超级群组）交 OnSourceMessage 回调
// （listener 聚合转储，源白名单在回调内过滤）。
// 发送/删除一律经 delivery.Sender（统一错误分类与限流重试）。
func updateHandler(opt Options) tgbot.HandlerFunc {
	cleaner := commandCleaner{states: make(map[commandScopeKey]bool)}
	return func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
		// 监听源消息：频道帖（bot 为频道管理员）与超级群组消息（bot 为
		// 群管理员，privacy 旁路可见全部）。回调内自行做源白名单过滤，
		// 未配置监听时是廉价的缓存查询后丢弃。
		if opt.OnSourceMessage != nil {
			var srcMsg *models.Message
			if update.ChannelPost != nil {
				srcMsg = update.ChannelPost
			} else if update.Message != nil && update.Message.Chat.Type == models.ChatTypeSupergroup {
				srcMsg = update.Message
			}
			if srcMsg != nil {
				bi := opt.Bot.Get()
				opt.OnSourceMessage(srcMsg, senderFor(opt, b), bi.ID, bi.Username)
				return
			}
		}
		msg := update.Message
		if msg == nil || msg.Chat.Type != models.ChatTypePrivate {
			return
		}
		from := msg.From
		if from == nil {
			return
		}
		// 多机器人池：记录用户最近活跃的 bot（主动通知路由依据）；
		// bot 身份在 getMe 回填后才读得到（长轮询启动晚于回填，恒有值）。
		if opt.NoteActive != nil {
			opt.NoteActive(from.ID, opt.Bot.Get().ID)
		}
		if opt.ProfileObserver != nil {
			opt.ProfileObserver(*from)
		}
		localOpt := opt
		if localOpt.CleanChatCommands == nil {
			localOpt.CleanChatCommands = func(ctx context.Context, chatID int64, code string) {
				cleaner.clean(ctx, b, opt.Log, chatID, code)
			}
		}
		handleUpdate(ctx, localOpt, senderFor(localOpt, b), *from, msg.Chat.ID, strings.TrimSpace(msg.Text))
	}
}

// handleUpdate 是剥离 tgbot 依赖后的业务分流（sender 为接口，便于测试）：
// 命令 → 相应处理；其余按链接提交处理。
func handleUpdate(ctx context.Context, opt Options, snd delivery.Sender, from models.User, chatID int64, text string) {
	if text == "" {
		sendText(ctx, opt, snd, chatID, textOnlyMsg)
		return
	}
	// 命令匹配取首词并剥掉 @botname 后缀，兼容 "/start hello"、"/help@bot" 等写法
	switch commandOf(text) {
	case "/start":
		handleStart(ctx, opt, snd, from, chatID)
		if opt.CleanChatCommands != nil {
			opt.CleanChatCommands(ctx, chatID, from.LanguageCode)
		}
	case "/help":
		sendText(ctx, opt, snd, chatID, helpText(systemName(ctx, opt)))
	case "/whoami":
		handleWhoami(ctx, opt, snd, chatID)
	case "/usage":
		handleUsage(ctx, opt, snd, from.ID, chatID)
	case "/cancel":
		handleCancel(ctx, opt, snd, from.ID, chatID, text)
	case "/download":
		handleDownload(ctx, opt, snd, from, chatID, text)
	case "/status":
		handleStatus(ctx, opt, snd, chatID)
	case "/health":
		handleHealth(ctx, opt, snd, chatID)
	case "/bind":
		handleBind(ctx, opt, snd, from.ID, chatID, text)
	case "/unbind":
		handleUnbind(ctx, opt, snd, from.ID, chatID, text)
	case "/channels":
		handleMyChannels(ctx, opt, snd, from.ID, chatID)
	case "/join":
		handleJoin(ctx, opt, snd, from, chatID, text)
	case "/watch":
		handleWatch(ctx, opt, snd, from, chatID, text)
	case "/unwatch":
		handleUnwatch(ctx, opt, snd, from, chatID, text)
	default:
		handleLinkWithProfile(ctx, opt, snd, from, chatID, text)
	}
}

// systemName 读取可配置的系统名称；Options 未注入来源时回退缺省值
// （测试环境多为 nil 注入）。
func systemName(ctx context.Context, opt Options) string {
	if opt.SystemName == nil {
		return syscfg.DefaultName
	}
	if name := opt.SystemName(ctx); name != "" {
		return name
	}
	return syscfg.DefaultName
}

// commandOf 从消息文本中提取规范化的命令词（首词，去 @bot 后缀，小写）。
func commandOf(text string) string {
	cmd := text
	if fields := strings.Fields(text); len(fields) > 0 {
		cmd = fields[0]
	}
	if i := strings.IndexByte(cmd, '@'); i > 0 {
		cmd = cmd[:i]
	}
	return strings.ToLower(cmd)
}

// handleStart 处理 /start：未授权用户生成待审批申请，待审批用户幂等刷新，
// 已启用用户保持欢迎文案；禁用/归档提示账号已停用。
func handleStart(ctx context.Context, opt Options, snd delivery.Sender, from models.User, chatID int64) {
	botInfo := opt.Bot.Get()
	outcome, err := opt.Access.HandleStart(ctx, access.StartInput{
		UserID:      from.ID,
		Username:    from.Username,
		DisplayName: displayNameOf(from),
		BotID:       botInfo.ID,
		BotUsername: botInfo.Username,
	})
	if err != nil {
		ae := apperr.From(err)
		opt.Log.Warn("/start 处理失败", "user_id", from.ID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	switch outcome {
	case access.StartWelcome:
		sendText(ctx, opt, snd, chatID, helpText(systemName(ctx, opt)))
	case access.StartPending:
		sendText(ctx, opt, snd, chatID, apperr.UserText(apperr.CodeUserPending))
	default:
		sendText(ctx, opt, snd, chatID, apperr.UserText(apperr.CodeUserDisabled))
	}
}

// displayNameOf 取显示名（first + last），用于申请记录。
func displayNameOf(u models.User) string {
	return strings.TrimSpace(u.FirstName + " " + u.LastName)
}

// handleUsage 处理 /usage：回显当日额度使用情况（owner 不限额）。
func handleUsage(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID int64) {
	u, err := opt.Access.Usage(ctx, userID)
	if err != nil {
		ae := apperr.From(err)
		opt.Log.Warn("/usage 查询失败", "user_id", userID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	if code := access.StatusDenyCode(u.Status); code != "" {
		// 未授权 / 待审批 / 已停用：与链接提交同款状态提示
		sendText(ctx, opt, snd, chatID, apperr.UserText(code))
		return
	}
	if u.IsOwner {
		sendText(ctx, opt, snd, chatID,
			fmt.Sprintf("你是 owner，不受频率与额度限制。\n今日已提交 %d 次。", u.Used))
		return
	}
	sendText(ctx, opt, snd, chatID, fmt.Sprintf("今日额度：%d/%d，剩余 %d 次。\n额度将于 %s 重置。",
		u.Used, u.DailyLimit, u.Remaining, u.ResetAt.Format("2006-01-02 15:04")))
}

const statusUnavailableText = "服务状态暂时不可用，请稍后重试。"

func loadRuntimeStatus(ctx context.Context, opt Options) (RuntimeStatus, bool) {
	if opt.Status == nil {
		return RuntimeStatus{}, false
	}
	status, err := opt.Status.Status(ctx)
	if err != nil {
		opt.Log.Warn("运行状态查询失败", "error", err.Error())
		return RuntimeStatus{}, false
	}
	return status, true
}

func handleStatus(ctx context.Context, opt Options, snd delivery.Sender, chatID int64) {
	status, ok := loadRuntimeStatus(ctx, opt)
	if !ok {
		sendText(ctx, opt, snd, chatID, statusUnavailableText)
		return
	}
	large := "不可用（超过 50MB 的媒体将发送失败）"
	if status.LargeFileReady {
		large = "可用"
	}
	sendText(ctx, opt, snd, chatID, fmt.Sprintf("服务状态：运行中\n用户号 MTProto：%s\n大文件直传通道：%s\n数据库：%s\n队列：%d/%d\n处理中：%d\nWorker：%d",
		mtprotoStateText(status.MTProtoState), large, readyText(status.StoreReady),
		status.QueueLen, status.QueueCap, status.Processing, status.WorkerCount))
}

func handleHealth(ctx context.Context, opt Options, snd delivery.Sender, chatID int64) {
	status, ok := loadRuntimeStatus(ctx, opt)
	if !ok {
		sendText(ctx, opt, snd, chatID, "健康：UNAVAILABLE\n服务状态暂时不可用，请稍后重试。")
		return
	}
	state := "DEGRADED"
	if status.MTProtoState == mtproto.StateReady && status.StoreReady && status.WorkerCount > 0 {
		state = "OK"
	}
	sendText(ctx, opt, snd, chatID, "健康："+state)
}

func readyText(ready bool) string {
	if ready {
		return "正常"
	}
	return "不可用"
}

func mtprotoStateText(state string) string {
	switch state {
	case mtproto.StateReady:
		return "已就绪"
	case mtproto.StateLoginPending:
		return "登录中"
	case mtproto.StateOffline:
		return "离线"
	default:
		return "未知"
	}
}

// handleLink 解析链接并经 access 六步校验后入队提取任务；每个有效链接
// 独立创建请求与状态占位，任务完成后由对应 worker 删除。
func handleLink(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID int64, text string) {
	handleLinkWithProfile(ctx, opt, snd, models.User{ID: userID}, chatID, text)
}

type submitFailure struct {
	ref  tmeurl.SourceRef
	code apperr.Code
}

func effectiveMaxLinks(ctx context.Context, opt Options) int {
	limit := opt.Cfg.MaxLinksPerMessage
	if opt.MaxLinksPerMessage != nil {
		limit = opt.MaxLinksPerMessage(ctx)
	}
	if limit < config.MinLinksPerMessage || limit > config.MaxLinksPerMessage {
		return config.DefaultMaxLinksPerMessage
	}
	return limit
}

func rejectTooManyLinks(ctx context.Context, opt Options, snd delivery.Sender, chatID int64, refs []tmeurl.SourceRef) bool {
	limit := effectiveMaxLinks(ctx, opt)
	if len(refs) <= limit {
		return false
	}
	sendText(ctx, opt, snd, chatID, fmt.Sprintf("一次最多处理 %d 条有效链接，本次检测到 %d 条，未提交任何任务。请分批发送。", limit, len(refs)))
	return true
}

func batchSubmitSummary(succeeded int, failures []submitFailure) string {
	var b strings.Builder
	fmt.Fprintf(&b, "批量提交完成：成功 %d 条，失败 %d 条。", succeeded, len(failures))
	if len(failures) == 0 {
		return b.String()
	}
	b.WriteString("\n\n失败明细：")
	shown := 0
	for _, failure := range failures {
		line := fmt.Sprintf("\n• %s：%s", failure.ref.String(), apperr.UserText(failure.code))
		if b.Len()+len(line) > 3500 {
			break
		}
		b.WriteString(line)
		shown++
	}
	if shown < len(failures) {
		fmt.Fprintf(&b, "\n• 另有 %d 条失败，请减少单次链接数后重试。", len(failures)-shown)
	}
	return b.String()
}

func submitRefs(ctx context.Context, opt Options, snd delivery.Sender, from models.User, chatID int64,
	refs []tmeurl.SourceRef, prompt, cloudDest string) {
	multi := len(refs) > 1
	succeeded := 0
	failures := make([]submitFailure, 0)
	for i, ref := range refs {
		// access 内部仍会权威检查；此处快速路径避免为已饱和队列发送占位。
		if opt.Queue.Full() {
			failures = append(failures, submitFailure{ref: ref, code: apperr.CodeQueueFull})
			if !multi {
				sendText(ctx, opt, snd, chatID, apperr.UserText(apperr.CodeQueueFull))
			}
			continue
		}

		statusMsgID := 0
		sent, err := snd.SendMessage(ctx, chatID, prompt)
		if err != nil {
			opt.Log.Warn("状态提示发送失败", "user_id", from.ID, "ref", ref.String(), "error", err.Error())
		} else {
			statusMsgID = sent
		}

		botInfo := opt.Bot.Get()
		dec, err := opt.Access.Submit(ctx, access.Submission{
			UserID:            from.ID,
			ChatID:            chatID,
			Ref:               ref,
			StatusMsgID:       statusMsgID,
			Username:          from.Username,
			DisplayName:       displayNameOf(from),
			ProfileProvided:   true,
			BatchContinuation: i > 0,
			CloudDest:         cloudDest,
			BotID:             botInfo.ID,
			BotUsername:       botInfo.Username,
		})
		if err != nil {
			ae := apperr.From(err)
			opt.Log.Error("提交校验失败", "user_id", from.ID, "ref", ref.String(), "code", ae.Code, "error", err.Error())
			delivery.TryDeleteStatus(ctx, snd, opt.Log, chatID, statusMsgID)
			failures = append(failures, submitFailure{ref: ref, code: ae.Code})
			if !multi {
				sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
			}
			continue
		}
		if !dec.Allowed {
			opt.Log.Info("拒绝提交", "user_id", from.ID, "ref", ref.String(), "reason", dec.Reason)
			delivery.TryDeleteStatus(ctx, snd, opt.Log, chatID, statusMsgID)
			failures = append(failures, submitFailure{ref: ref, code: dec.Reason})
			if !multi {
				sendText(ctx, opt, snd, chatID, apperr.UserText(dec.Reason))
			}
			continue
		}
		succeeded++
		opt.Log.Info("任务已入队", "job_id", dec.JobID, "user_id", from.ID,
			"request_id", dec.RequestID, "ref", ref.String(), "cloud_dest", cloudDest)
	}
	if multi {
		sendText(ctx, opt, snd, chatID, batchSubmitSummary(succeeded, failures))
	}
}

// handleLinkWithProfile 处理一个或多个链接，并把 Bot update 中的资料传入访问控制服务。
func handleLinkWithProfile(ctx context.Context, opt Options, snd delivery.Sender, from models.User, chatID int64, text string) {
	refs := tmeurl.ParseAll(text)
	if len(refs) == 0 {
		sendText(ctx, opt, snd, chatID, apperr.UserText(apperr.CodeInvalidURL))
		return
	}
	if rejectTooManyLinks(ctx, opt, snd, chatID, refs) {
		return
	}
	submitRefs(ctx, opt, snd, from, chatID, refs, statusPrompt, "")
}

const cancelUsage = "用法：/cancel 消息链接（即当初提交的那条链接）"

// handleCancel 处理 /cancel <链接>：取消该用户名下与链接匹配的在途任务
// （queued/processing）。链接与提交入口同源解析（tmeurl），归属校验由 access
// 按 user_id 限定；取首条有效链接，其余忽略。
func handleCancel(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID int64, text string) {
	args := strings.Fields(text)
	if len(args) < 2 {
		sendText(ctx, opt, snd, chatID, cancelUsage)
		return
	}
	refs := tmeurl.ParseAll(strings.Join(args[1:], " "))
	if len(refs) == 0 {
		sendText(ctx, opt, snd, chatID, apperr.UserText(apperr.CodeInvalidURL))
		return
	}
	if len(refs) > 1 {
		sendText(ctx, opt, snd, chatID, multiCancelNotice)
	}
	n, err := opt.Access.CancelOwnByLink(ctx, userID, refs[0])
	if err != nil {
		ae := apperr.From(err)
		opt.Log.Error("/cancel 处理失败", "user_id", userID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	switch {
	case n == 0:
		sendText(ctx, opt, snd, chatID, "该链接当前没有进行中的任务。")
	case n == 1:
		sendText(ctx, opt, snd, chatID, "已取消该任务。")
	default:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf("已取消 %d 个任务。", n))
	}
}

// downloadUsage 是 /download 的参数提示：说明两种形式与目的地规则。
const downloadUsage = "用法：/download 消息链接 或 /download 目的地 消息链接\n\n" +
	"• 把消息中的媒体上传到网盘，不重发到聊天\n" +
	"• 不带目的地名称时使用默认目的地（由管理员配置）\n" +
	"• 纯文本消息不支持网盘下载"

// 云盘状态文案：与 apperr 文案同为受控中文常量。
const (
	cloudUnavailableText = "云盘下载功能暂不可用，请稍后重试。"
	cloudDisabledText    = "云盘下载功能未开启。"
)

// cloudDestNotFoundText 渲染目的地无效文案：可用列表为空时退化为未开启
// （防御 enabled=true 但无可用目的地的异常态，正常配置下不可达）。
func cloudDestNotFoundText(avail []string) string {
	if len(avail) == 0 {
		return cloudDisabledText
	}
	return "未找到该下载目的地，可用：" + strings.Join(avail, "、") + "。"
}

// parseDownloadArgs 解析 /download 参数，返回（目的地名称, 链接文本）。
// 第一段含 t.me 视为链接本身（默认目的地）；否则视为目的地名称、其余为
// 链接文本。链接文本为空（无参/只给了名称）由调用方回用法提示。
func parseDownloadArgs(text string) (dest, link string) {
	fields := strings.Fields(text)
	if len(fields) < 2 {
		return "", ""
	}
	if strings.Contains(fields[1], "t.me") {
		return "", strings.Join(fields[1:], " ")
	}
	if len(fields) < 3 {
		return fields[1], ""
	}
	return fields[1], strings.Join(fields[2:], " ")
}

// handleDownload 处理 /download [目的地] 链接：媒体上传到网盘而不重发回
// Telegram。顺序严格按 ：
//  1. 用户状态与下载权限预检（access 轻量查询）：未授权/待审/禁用回与
//     裸链接完全相同的申请引导文案（apperr.UserText），绝不出现云盘功能
//     字样（AC1）；状态通过但无用户级下载权限（默认普通用户无、owner 有，
//     显式设置优先）回专门拒绝文案；
//  2. 云盘状态：rclone 不可用/全局开关关闭/目的地无效各自回受控文案；
//  3. tmeurl.ParseAll 解析（多链接与裸链接同规则）；
//  4. Queue.Full 快速路径；
//  5. 占位提示（云盘文案）→ Access.Submit 透传 CloudDest（事务内还会
//     权威复核下载权限），拒绝/失败路径与 handleLinkWithProfile 同构
//     （清理占位+受控文案）。
func handleDownload(ctx context.Context, opt Options, snd delivery.Sender, from models.User, chatID int64, text string) {
	if opt.CloudStatus == nil {
		sendText(ctx, opt, snd, chatID, "此命令当前不可用。")
		return
	}
	dest, linkText := parseDownloadArgs(text)
	if linkText == "" {
		sendText(ctx, opt, snd, chatID, downloadUsage)
		return
	}

	// 1. 用户状态与下载权限预检：状态拒绝与裸链接同款申请引导（不暴露功能
	//    存在）；无下载权限的授权用户回专门文案。
	code, err := opt.Access.UserDownloadStatus(ctx, from.ID)
	if err != nil {
		ae := apperr.From(err)
		opt.Log.Warn("/download 状态预检失败", "user_id", from.ID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	if code != "" {
		sendText(ctx, opt, snd, chatID, apperr.UserText(code))
		return
	}

	// 2. 云盘状态：rclone 可用性 → 全局开关 → 目的地有效
	cs := opt.CloudStatus
	if !cs.Available() {
		sendText(ctx, opt, snd, chatID, cloudUnavailableText)
		return
	}
	if !cs.Enabled() {
		sendText(ctx, opt, snd, chatID, cloudDisabledText)
		return
	}
	if dest == "" {
		dest = cs.DefaultDestination()
	}
	if dest == "" || !cs.DestinationEnabled(dest) {
		sendText(ctx, opt, snd, chatID, cloudDestNotFoundText(cs.EnabledDestinations()))
		return
	}

	// 3. 链接解析与单次上限检查；超过上限整批拒绝，不创建占位或任务。
	refs := tmeurl.ParseAll(linkText)
	if len(refs) == 0 {
		sendText(ctx, opt, snd, chatID, apperr.UserText(apperr.CodeInvalidURL))
		return
	}
	if rejectTooManyLinks(ctx, opt, snd, chatID, refs) {
		return
	}

	// 4–5. 每条链接独立创建云盘占位并经同一六步链提交到相同目的地。
	submitRefs(ctx, opt, snd, from, chatID, refs, cloudStatusPrompt, dest)
}

// handleWhoami 经 MTProto 查询自身账号并回显，用于验证用户通道；调试命令，不在帮助文本列出。
func handleWhoami(ctx context.Context, opt Options, snd delivery.Sender, chatID int64) {
	if opt.Whoami == nil {
		sendText(ctx, opt, snd, chatID, "此命令当前不可用。")
		return
	}
	name, err := opt.Whoami(ctx)
	if err != nil {
		opt.Log.Warn("/whoami 查询失败", "chat_id", chatID, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(apperr.From(err).Code))
		return
	}
	sendText(ctx, opt, snd, chatID, "MTProto 账号："+name)
}

// joinUsage 是 /join 的参数提示：说明链接来源与审核规则。
const joinUsage = "用法：/join 频道邀请链接\n\n" +
	"• 链接形态：https://t.me/+xxxx 或 https://t.me/joinchat/xxxx\n" +
	"• 邀请链接可用你加入频道时拿到的那个，或向频道管理员索取\n" +
	"• 号主提交后立即加入；其他用户提交后需号主在管理端审核通过才会加入"

// commandArgument 提取命令后的参数，兼容命令大小写和 /join@bot 形式。
func commandArgument(text string) string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimSpace(text[len(fields[0]):])
}

// handleJoin 处理 /join <邀请链接>：让登录的读取账号加入私有频道。
// 开关/上限/审核分流在 joinmgr.Service 内完成，这里只做参数与依赖守卫
// 和结果文案。owner 判定失败按非 owner 处理（保守）。
func handleJoin(ctx context.Context, opt Options, snd delivery.Sender, from models.User, chatID int64, text string) {
	if opt.ChannelJoin == nil {
		sendText(ctx, opt, snd, chatID, "此命令当前不可用。")
		return
	}
	arg := commandArgument(text)
	if arg == "" {
		sendText(ctx, opt, snd, chatID, joinUsage)
		return
	}

	isOwner := false
	if opt.IsOwner != nil {
		ok, err := opt.IsOwner(ctx, from.ID)
		if err != nil {
			// owner 判定失败不影响提交流程，按普通用户处理（审核兜底）
			opt.Log.Warn("/join 号主判定失败，按普通用户处理",
				"user_id", from.ID, "error", err.Error())
		} else {
			isOwner = ok
		}
	}

	outcome, err := opt.ChannelJoin.Submit(ctx, from.ID, isOwner, arg)
	if err != nil {
		ae := apperr.From(err)
		opt.Log.Warn("/join 提交失败", "user_id", from.ID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	title := outcome.Title
	if title == "" {
		title = "该频道"
	}
	switch outcome.Kind {
	case joinmgr.SubmitDisabled:
		sendText(ctx, opt, snd, chatID, "频道加入功能当前已关闭。")
	case joinmgr.SubmitJoined:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf(
			"已加入频道「%s」，现在可以发送该频道的消息链接了。", title))
	case joinmgr.SubmitAlreadyJoined:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf(
			"账号已在频道「%s」中，直接发送该频道的消息链接即可。", title))
	case joinmgr.SubmitPending:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf(
			"已收到加入「%s」的申请，等待号主审核，通过后会通知你。", title))
	case joinmgr.SubmitPendingDuplicate:
		sendText(ctx, opt, snd, chatID,
			"这个频道的加入申请已在等待审核，请耐心等待。")
	case joinmgr.SubmitLimitReached:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf(
			"已达到加入频道数量上限（当前 %d 个），请联系管理员。", outcome.Requests))
	case joinmgr.SubmitJoinRequested:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf(
			"频道「%s」开启了加入审核，已向频道管理员发送加入请求，批准后即可发送该频道的消息链接。",
			title))
	default:
		sendText(ctx, opt, snd, chatID, apperr.UserText(apperr.CodeInternal))
	}
}

// watchUsage 是 /watch 与 /unwatch 共用的参数提示。
const watchUsage = "用法：/watch 频道（@mychannel、t.me/频道 链接或 -100 开头的 ID）\n\n" +
	"仅支持频道与超级群组，且需先把本机器人加为该频道/群的管理员" +
	"（管理员身份保证我能收到全部消息）。不带参数发送 /watch 可查看我的监听源。"

// handleWatch 处理 /watch：无参数列出本人监听源；带参数提交申请
// （准入/审批/上限校验在 watch.Service 内完成）。owner 判定失败按非
// owner 处理（保守，与 /join 一致）。
func handleWatch(ctx context.Context, opt Options, snd delivery.Sender, from models.User, chatID int64, text string) {
	if opt.Watch == nil {
		sendText(ctx, opt, snd, chatID, "此命令当前不可用。")
		return
	}
	arg := commandArgument(text)
	if arg == "" {
		rows, err := opt.Watch.ListByUser(ctx, from.ID)
		if err != nil {
			ae := apperr.From(err)
			opt.Log.Warn("/watch 列表查询失败", "user_id", from.ID, "code", ae.Code, "error", err.Error())
			sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
			return
		}
		if len(rows) == 0 {
			sendText(ctx, opt, snd, chatID, "你还没有监听源。发送 /watch 频道 添加。")
			return
		}
		var b strings.Builder
		b.WriteString("我的监听源：\n")
		for _, r := range rows {
			name := r.Title
			if name == "" {
				name = r.Username
			}
			if name == "" {
				name = fmt.Sprintf("频道 %d", r.ChannelID)
			}
			switch r.Status {
			case store.WatchPending:
				fmt.Fprintf(&b, "• %s（待审批）\n", name)
			case store.WatchApproved:
				state := "监听中"
				if !r.Enabled {
					state = "已暂停"
				}
				fmt.Fprintf(&b, "• %s（%s）\n", name, state)
			}
		}
		sendText(ctx, opt, snd, chatID, b.String())
		return
	}

	isOwner := false
	if opt.IsOwner != nil {
		ok, err := opt.IsOwner(ctx, from.ID)
		if err != nil {
			opt.Log.Warn("/watch 号主判定失败，按普通用户处理",
				"user_id", from.ID, "error", err.Error())
		} else {
			isOwner = ok
		}
	}
	botInfo := opt.Bot.Get()
	outcome, err := opt.Watch.Submit(ctx, from.ID, isOwner, arg, botInfo.ID, botInfo.Username)
	if err != nil {
		ae := apperr.From(err)
		opt.Log.Warn("/watch 提交失败", "user_id", from.ID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	title := outcome.Title
	if title == "" {
		title = "该源"
	}
	switch outcome.Kind {
	case watch.SubmitDisabled:
		sendText(ctx, opt, snd, chatID, "监听源自助申请当前未开放。")
	case watch.SubmitUserNotAllowed:
		sendText(ctx, opt, snd, chatID, "请先 /start 通过审核后再使用本功能。")
	case watch.SubmitInvalid:
		sendText(ctx, opt, snd, chatID, "无法识别目标：请发送频道/超级群组的 @用户名、t.me 链接或 -100 开头的 ID。")
	case watch.SubmitNotAdmin:
		sendText(ctx, opt, snd, chatID, "我还不在这个频道/群里，或不是管理员：请先把我加为该频道/群的管理员再试。")
	case watch.SubmitAlreadyMine:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf("「%s」已在你的监听列表中（资料已刷新）。", title))
	case watch.SubmitAlreadyOthers:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf("「%s」已由其他人添加。", title))
	case watch.SubmitUserLimit:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf(
			"已达每用户监听上限（%d/%d），可先 /unwatch 移除部分源。", outcome.Count, outcome.Limit))
	case watch.SubmitSourceLimit:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf(
			"监听源总数已达上限（%d/%d），请联系管理员。", outcome.Count, outcome.Limit))
	case watch.SubmitPending:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf(
			"已提交监听「%s」的申请，等待管理员审批，通过后会通知你。", title))
	case watch.SubmitActive:
		sendText(ctx, opt, snd, chatID, fmt.Sprintf(
			"「%s」已开始监听：源内新消息会自动预热缓存，之后把该源的消息链接发给我可秒回。", title))
	default:
		sendText(ctx, opt, snd, chatID, apperr.UserText(apperr.CodeInternal))
	}
}

// handleUnwatch 处理 /unwatch 频道：移除本人的监听源（号主可移除任意源）。
func handleUnwatch(ctx context.Context, opt Options, snd delivery.Sender, from models.User, chatID int64, text string) {
	if opt.Watch == nil {
		sendText(ctx, opt, snd, chatID, "此命令当前不可用。")
		return
	}
	arg := commandArgument(text)
	if arg == "" {
		sendText(ctx, opt, snd, chatID, watchUsage)
		return
	}
	isOwner := false
	if opt.IsOwner != nil {
		ok, err := opt.IsOwner(ctx, from.ID)
		if err != nil {
			opt.Log.Warn("/unwatch 号主判定失败，按普通用户处理",
				"user_id", from.ID, "error", err.Error())
		} else {
			isOwner = ok
		}
	}
	removed, err := opt.Watch.Unwatch(ctx, from.ID, isOwner, arg)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			sendText(ctx, opt, snd, chatID, "没有找到你添加的这个监听源（/watch 查看列表）。")
			return
		}
		ae := apperr.From(err)
		opt.Log.Warn("/unwatch 失败", "user_id", from.ID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	title := removed.Title
	if title == "" {
		title = "该源"
	}
	sendText(ctx, opt, snd, chatID, fmt.Sprintf("已移除监听源「%s」。", title))
}

// bindUsage 是 /bind 与 /unbind 共用的参数提示。
const bindUsage = "用法：/bind 频道用户名（@mychannel）、t.me/频道 链接或 -100 开头的频道 ID\n\n" +
	"私有频道没有用户名，请发送 t.me/c/… 链接或 -100 开头的频道 ID。" +
	"请先把本机器人拉进频道并设置为管理员（需有发言权限），再发送绑定命令。"

// requireEnabled 复用 /usage 的状态查询做频道指令准入：
// 未授权/待审批/停用用户返回 false 并已回复对应文案。
func requireEnabled(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID int64) bool {
	u, err := opt.Access.Usage(ctx, userID)
	if err != nil {
		ae := apperr.From(err)
		opt.Log.Warn("频道指令状态查询失败", "user_id", userID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return false
	}
	if code := access.StatusDenyCode(u.Status); code != "" {
		sendText(ctx, opt, snd, chatID, apperr.UserText(code))
		return false
	}
	return true
}

// handleBind 处理 /bind <频道>：校验机器人为该频道管理员且有发言权限后，
// 把频道绑定到当前用户名下；任务成功后的内容会同步发送到该频道。
func handleBind(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID int64, text string) {
	if opt.Channels == nil {
		sendText(ctx, opt, snd, chatID, "此命令当前不可用。")
		return
	}
	args := strings.Fields(text)
	if len(args) < 2 {
		sendText(ctx, opt, snd, chatID, bindUsage)
		return
	}
	if !requireEnabled(ctx, opt, snd, userID, chatID) {
		return
	}
	bound, err := opt.Channels.BindBot(ctx, userID, args[1])
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			sendText(ctx, opt, snd, chatID, "该频道没有绑定在你的账号下。")
			return
		}
		ae := apperr.From(err)
		opt.Log.Warn("/bind 处理失败", "user_id", userID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	sendText(ctx, opt, snd, chatID, bindSuccessText(bound))
}

// bindSuccessText 渲染绑定成功文案：标题 + 用户名/ID + 后续行为说明。
func bindSuccessText(b store.ChannelBinding) string {
	name := "@" + b.Username
	if b.Username == "" {
		name = fmt.Sprintf("ID %d", b.ChannelID)
	}
	return fmt.Sprintf("已绑定频道「%s」（%s）。\n之后每次提取的内容会在发给你之后同步发送到该频道；用 /unbind 可解除绑定。", b.Title, name)
}

// handleUnbind 处理 /unbind <频道>：只能解除当前用户自己的绑定。
func handleUnbind(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID int64, text string) {
	if opt.Channels == nil {
		sendText(ctx, opt, snd, chatID, "此命令当前不可用。")
		return
	}
	args := strings.Fields(text)
	if len(args) < 2 {
		sendText(ctx, opt, snd, chatID, "用法：/unbind 频道用户名、t.me/频道 链接或 -100 开头的频道 ID")
		return
	}
	if !requireEnabled(ctx, opt, snd, userID, chatID) {
		return
	}
	removed, err := opt.Channels.UnbindBot(ctx, userID, args[1])
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			sendText(ctx, opt, snd, chatID, "该频道没有绑定在你的账号下。")
			return
		}
		ae := apperr.From(err)
		opt.Log.Warn("/unbind 处理失败", "user_id", userID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	sendText(ctx, opt, snd, chatID, fmt.Sprintf("已解除绑定「%s」。", removed.Title))
}

// handleMyChannels 处理 /channels：列出当前用户绑定的频道。
func handleMyChannels(ctx context.Context, opt Options, snd delivery.Sender, userID, chatID int64) {
	if opt.Channels == nil {
		sendText(ctx, opt, snd, chatID, "此命令当前不可用。")
		return
	}
	if !requireEnabled(ctx, opt, snd, userID, chatID) {
		return
	}
	rows, err := opt.Channels.ListByUser(ctx, userID)
	if err != nil {
		ae := apperr.From(err)
		opt.Log.Warn("/channels 查询失败", "user_id", userID, "code", ae.Code, "error", err.Error())
		sendText(ctx, opt, snd, chatID, apperr.UserText(ae.Code))
		return
	}
	if len(rows) == 0 {
		sendText(ctx, opt, snd, chatID, "你还没有绑定频道。先把本机器人拉进你的频道并设为管理员，然后用 /bind 绑定。")
		return
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("你绑定了 %d 个频道：\n", len(rows)))
	for _, r := range rows {
		sb.WriteString("\n📌 ")
		sb.WriteString(r.Title)
		if r.Username != "" {
			sb.WriteString("（@" + r.Username + "）")
		} else {
			sb.WriteString(fmt.Sprintf("（ID %d）", r.ChannelID))
		}
		// 纯文本 URL 由 Telegram 客户端自动渲染为可点击链接
		sb.WriteString("\n🔗 ")
		sb.WriteString(binding.ChannelURL(r))
	}
	sendText(ctx, opt, snd, chatID, sb.String())
}

// sendText 发送文本回复；失败仅记日志。
func sendText(ctx context.Context, opt Options, snd delivery.Sender, chatID int64, text string) {
	if _, err := snd.SendMessage(ctx, chatID, text); err != nil {
		opt.Log.Error("发送回复失败", "chat_id", chatID, "error", err.Error())
	}
}
