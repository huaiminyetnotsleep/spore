package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/delivery"
	"github.com/huaiminyetnotsleep/spore/internal/errlog"
	"github.com/huaiminyetnotsleep/spore/internal/queue"
	"github.com/huaiminyetnotsleep/spore/internal/store"
	"github.com/huaiminyetnotsleep/spore/internal/tmeurl"
)

// settings 表中由本包持有的运营设置键（value_json 为 JSON 编码值）。
const (
	settingKeyTimezone       = "timezone"         // 运营时区：IANA 名称（JSON 字符串），日额度按该时区分桶
	settingKeyDedupWindowMin = "dedup_window_min" // 重复链接检测窗口：分钟（JSON 数字）
)

// 运营设置默认值（解析失败/键缺失时回退，不阻断提交）。
const (
	defaultTimezoneName = "Asia/Shanghai"
	defaultDedupWindow  = 10 * time.Minute
	dayFormat           = "2006-01-02" // usage_daily.day 键格式（运营时区日期）
)

// Enqueuer 是服务对内存队列的最小依赖（饱和预检 + 入队）。
// 以接口注入便于为"事务提交后入队失败（队列满竞态）"分支写确定性测试。
type Enqueuer interface {
	Full() bool
	Enqueue(job queue.Job) error
}

// RequestCanceller 是访问层向活动 worker 发送请求级取消信号的最小依赖。
type RequestCanceller interface {
	CancelRequest(requestID int64) bool
}

// EventSink 是访问控制服务对事件中心的最小依赖；避免 access 反向依赖 notify。
type EventSink interface {
	StoreWriteFailed(ctx context.Context, scene string)
}

// ActivityNotifier 是访问控制服务对活动通知的最小依赖（由装配层适配到
// 事件中心，保持 access 不依赖 notify）；nil 表示不通知。
type ActivityNotifier interface {
	// UserApplied 在新用户提交申请（/start 落 pending）时触发；
	// 已有 pending 的重复 /start 刷新不触发。
	UserApplied(ctx context.Context, app ApplicationInfo)
}

// ApplicationInfo 一次新用户申请的关键信息（活动通知 payload）。
type ApplicationInfo struct {
	UserID            int64
	Username          string // Telegram username，可空
	DisplayName       string // Telegram 显示名，可空
	SourceBotUsername string // 受理申请的 bot username，可空
}

// Options 聚合服务依赖。
type Options struct {
	Store            *store.Store     // 必填：业务数据库
	Queue            Enqueuer         // 必填：链接提取任务内存队列
	RequestCanceller RequestCanceller // 可选；缺省从 Queue 自动探测
	Events           EventSink        // 可选：数据库不可用时上报事件
	Activity         ActivityNotifier // 可选：新用户申请活动通知
	// ErrLog 错误日志写入门面（可选，nil 安全）：入队失败（队列满竞态）
	// 等提交环节错误逐条落 error_logs。
	ErrLog *errlog.Service
	Log    *slog.Logger
	Now    func() time.Time // 可注入时钟（测试用）；缺省 time.Now
}

// Service 是访问控制应用服务；零值不可用，经 New 构造。
type Service struct {
	store     *store.Store
	queue     Enqueuer
	canceller RequestCanceller
	events    EventSink
	activity  ActivityNotifier
	errLog    *errlog.Service
	senderMu  sync.RWMutex
	sender    delivery.Sender // 审批结果通知通道；Bot 就绪后经 SetSender 注入（见注释）
	dumpLive  dumpLiveFunc    // 缓存副本有效性校验；Bot 就绪后经 SetDumpLive 注入
	// dumpChannelID 解析当前缓存频道 ID（与 dumpcache 同源）；Bot 就绪后经
	// SetDumpChannelID 注入。闭包读 settings，严禁在事务视图内调用。
	dumpChannelID func() int64
	log           *slog.Logger
	now           func() time.Time
}

// New 创建服务；Store 与 Queue 为必填。
func New(opt Options) (*Service, error) {
	if opt.Store == nil || opt.Queue == nil {
		return nil, errors.New("access: Store 与 Queue 为必填项")
	}
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	canceller := opt.RequestCanceller
	if canceller == nil {
		canceller, _ = opt.Queue.(RequestCanceller)
	}
	now := opt.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: opt.Store, queue: opt.Queue, canceller: canceller,
		events: opt.Events, activity: opt.Activity, errLog: opt.ErrLog, log: opt.Log, now: now}, nil
}

// SetSender 注入审批结果通知通道。通知依赖 Bot 实例，而 Bot 装配又需要本服务
// （审批/重试等管理入口），存在构造环，故拆为后置注入。
// 当前 Web 管理端先于首轮注入启动，且 MTProto 每轮重连都会以新 Bot 重新注入，
// 与审批路径的读取并发，因此以读写锁保护（读取方经 notifySender 取快照）。
func (s *Service) SetSender(snd delivery.Sender) {
	s.senderMu.Lock()
	defer s.senderMu.Unlock()
	s.sender = snd
}

// notifySender 返回当前通知通道快照（可能为 nil：Bot 尚未就绪）。
func (s *Service) notifySender() delivery.Sender {
	s.senderMu.RLock()
	defer s.senderMu.RUnlock()
	return s.sender
}

// Submission 一次链接提交的输入（botapi 已预发占位提示后调用 Submit）。
type Submission struct {
	UserID          int64
	ChatID          int64            // 结果回传的私聊
	Ref             tmeurl.SourceRef // 已解析的来源链接
	StatusMsgID     int              // botapi 预发的"正在获取消息..."消息 ID，任务完成后由 worker 删除
	Username        string           // Telegram 当前 username，可空
	DisplayName     string           // Telegram 当前显示名，可空
	ProfileProvided bool             // 是否明确携带本次 Bot update 的资料（允许清除空资料）
	// BatchContinuation 表示同一条 Bot 输入中的后续链接。提交间隔按用户输入
	// 消息检查一次，后续链接跳过该项；额度、并发、去重和队列仍逐条校验。
	BatchContinuation bool
	// CloudDest 非空表示云盘下载提交（/download 指令）：六步链语义与裸链接
	// 完全一致，通过后 requests 行落 cloud 占位与目的地名称，任务带同名
	// CloudDest 走网盘上传路径。空值 = 现有 TG 投递（零值兼容）。
	CloudDest string
	// BotID/BotUsername 是受理 bot 的身份（多机器人池归属）；0 = 存量调用方
	//（Web 补存等内部通道），落库为 0、worker 回退主 bot 处理。
	BotID       int64
	BotUsername string
	// Pin 表示本次提交标记自动置顶（/pin <链接>）：任务成功后把发送给用户
	// 的媒体复制到用户绑定的频道/群组并置顶组首。仅普通投递生效（云盘/
	// 缓存补写任务不适用）；用户级 auto_pin 偏好开启时普通提交同样默认置顶
	//（Submit 内合并，无须调用方重复传入）。
	Pin bool
}

// Decision 是提交裁定：Allowed 表示已建任务并入队；
// 拒绝时 Reason 为原因码（apperr 错误码，可直接经 apperr.UserText 转用户文案）。
// 拒绝不建 requests 行、不扣额度；唯一例外是"事务提交后入队失败"的竞态
// （见 Submit），此时 RequestID 非 0 且该行已标记为 QUEUE_FULL 失败。
type Decision struct {
	Allowed   bool
	Reason    apperr.Code
	RequestID int64  // 通过（或竞态落库）时新建的 requests 行 ID
	JobID     string // 通过时入队的内存任务 ID
}

// Submit 执行六步校验链并在全部通过时入队：
//  1. 用户存在且 status == enabled（不存在引导 /start，pending 等待，禁用/归档已停用）；
//     云盘提交（CloudDest 非空）追加用户级下载权限复核（EffectiveCloudDownload，
//     显式允许/拒绝优先，默认 owner 允许、普通用户拒绝）；
//  2. 去重窗口内无同用户同链接的成功记录（owner 同样适用）；
//  3. 距上次通过提交 >= submit_interval_sec（owner 跳过；同一 Bot 输入的批量续项跳过）；
//  4. 运营时区当日用量 < daily_limit（owner 跳过）；
//  5. 未完成请求数 < concurrent_limit（owner 跳过）；
//  6. 内存队列未满（owner 同样受限的系统性保护）。
//
// 第 1–5 步检查与"扣减 usage_daily + 记录 users 使用时间 + 写 requests(queued)"
// 在同一个数据库事务内完成；拒绝只更新 users.last_denied_*。
// 返回的 error 仅表示存储故障（调用方回复 STORE_UNAVAILABLE 文案），业务拒绝走 Decision。
func (s *Service) Submit(ctx context.Context, in Submission) (Decision, error) {
	rt := s.loadRuntime(ctx)
	now := s.now()
	day := now.In(rt.loc).Format(dayFormat)

	var d Decision
	err := s.store.Tx(ctx, func(tx *store.Store) error {
		u, err := tx.GetUser(ctx, in.UserID)
		if errors.Is(err, store.ErrNotFound) {
			d = Decision{Reason: apperr.CodeUserNotAuthorized}
			return nil // 提交（无行可写），裁定为拒绝
		}
		if err != nil {
			return err
		}
		// Bot 提交携带资料时刷新用户资料，但不改变权限字段；旧的内部调用
		// 未携带资料时保持数据库中的最后一次成功资料。
		if in.ProfileProvided || in.Username != "" || in.DisplayName != "" {
			if err := tx.UpdateUserProfile(ctx, u.ID, in.Username, in.DisplayName); err != nil {
				return err
			}
		}
		if code := StatusDenyCode(u.Status); code != "" {
			return s.deny(ctx, tx, u, now, code, &d)
		}

		// 云盘提交（CloudDest 非空）追加用户级下载权限权威复核：预检后权限
		// 可能已被管理员修改，事务内以数据库当前值为准。拒绝与去重/限额
		// 同构（不建任务、不扣额度、只记 last_denied_*）。裸链接不受影响。
		if in.CloudDest != "" && !u.EffectiveCloudDownload() {
			return s.deny(ctx, tx, u, now, apperr.CodeCloudDownloadDenied, &d)
		}

		// 2. 同用户同链接重复（不建任务、不扣额度）：
		//   - 普通提交：时间窗内已有成功记录即拒绝；
		//   - 云盘提交：成功去重交给 worker 的"已上传跳过"路径（命中会直接返回成功
		//     而非拒绝），这里只拦同链接同目的地的在途任务——同路径并发上传会在
		//     允许同名对象的网盘（如 MEGA）产生重复文件。
		var dup bool
		if in.CloudDest != "" {
			dup, err = tx.HasUnfinishedCloudRequest(ctx, in.UserID, ChannelKey(in.Ref),
				in.Ref.MessageID, in.CloudDest)
		} else {
			dup, err = tx.HasRecentSucceeded(ctx, in.UserID, ChannelKey(in.Ref), in.Ref.MessageID,
				now.Add(-rt.dedupWindow).UnixMilli())
		}
		if err != nil {
			return err
		}
		if dup {
			return s.deny(ctx, tx, u, now, apperr.CodeDuplicateLink, &d)
		}

		// 3–5 仅约束普通用户；owner 跳过但仍走状态、重复与队列满检查
		if !u.IsOwner {
			if intervalMs := int64(u.SubmitIntervalSec) * 1000; !in.BatchContinuation && now.UnixMilli()-u.LastUsedAt < intervalMs {
				return s.deny(ctx, tx, u, now, apperr.CodeSubmitRateLimited, &d)
			}
			usage, err := tx.GetUsage(ctx, in.UserID, day)
			if err != nil {
				return err
			}
			if usage.Used >= u.DailyLimit {
				return s.deny(ctx, tx, u, now, apperr.CodeQuotaExceeded, &d)
			}
			unfinished, err := tx.CountUnfinishedByUser(ctx, in.UserID)
			if err != nil {
				return err
			}
			if unfinished >= u.ConcurrentLimit {
				return s.deny(ctx, tx, u, now, apperr.CodeConcurrentLimited, &d)
			}
		}

		// 6. 队列满：在事务内预检（非阻塞内存读），拒绝同样只记 last_denied_*
		if s.queue.Full() {
			return s.deny(ctx, tx, u, now, apperr.CodeQueueFull, &d)
		}

		// 全部通过：同一事务内完成扣减与落库（owner 不扣额度但照常留痕）
		if !u.IsOwner {
			if err := tx.IncrementUsage(ctx, in.UserID, day, 1); err != nil {
				return err
			}
		}
		if err := tx.TouchUserUsage(ctx, in.UserID, now.UnixMilli()); err != nil {
			return err
		}
		// 云盘提交（CloudDest 非空）与裸链接共用同一事务与扣减语义，仅落列
		// 差异：delivery_mode 提前占位 cloud 并保存目的地名称（重试重新入队
		// 时据此恢复任务路由），终态由 worker 按 cloud 路径覆盖。
		reqIn := store.Request{
			UserID:      in.UserID,
			SourceKind:  SourceKind(in.Ref),
			ChannelKey:  ChannelKey(in.Ref),
			MessageID:   in.Ref.MessageID,
			RequestedAt: now.UnixMilli(),
			QueuedAt:    now.UnixMilli(),
			BotID:       in.BotID,
			BotUsername: in.BotUsername,
		}
		if in.CloudDest != "" {
			reqIn.DeliveryMode = store.DeliveryModeCloud
			reqIn.CloudDestination = in.CloudDest
		} else if in.Pin || u.AutoPin {
			// 自动置顶仅普通投递生效：云盘任务无 TG 媒体消息可复制，
			// 缓存补写/重试入队不走本方法（重试经请求行继承 pin）。
			reqIn.Pin = true
		}
		req, err := tx.CreateRequest(ctx, reqIn)
		if err != nil {
			return err
		}
		d = Decision{Allowed: true, RequestID: req.ID}
		return nil
	})
	if err != nil {
		if s.events != nil && apperr.From(err).Code == apperr.CodeStoreUnavailable {
			s.events.StoreWriteFailed(ctx, "提交请求落库")
		}
		return Decision{}, err
	}
	if !d.Allowed {
		return d, nil
	}

	// 事务提交后入内存队列。与上面的 Full() 预检之间存在竞态窗口：
	// 落库后入队失败按"处理失败"收尾（额度不返还），用户侧收到队列满提示
	job := queue.NewJob(in.UserID, in.ChatID, in.Ref, in.StatusMsgID, d.RequestID)
	job.CloudDest = in.CloudDest
	job.BotID = in.BotID
	if err := s.queue.Enqueue(job); err != nil {
		s.log.Warn("事务提交后入队失败（队列已满）", "user_id", in.UserID, "request_id", d.RequestID)
		s.errLog.Record(ctx, errlog.Record{
			Source:    store.ErrorSourceRequest,
			Code:      string(apperr.CodeQueueFull),
			Stage:     "enqueue",
			Severity:  store.ErrorSeverityError,
			Message:   "提交后入队失败（队列已满），请求标记失败",
			Context:   map[string]any{"user_id": in.UserID, "bot_id": in.BotID, "channel_key": ChannelKey(in.Ref), "message_id": in.Ref.MessageID},
			RequestID: d.RequestID,
		})
		finish := store.RequestResult{
			Status:    store.RequestFailed,
			ErrorCode: string(apperr.CodeQueueFull),
		}
		if in.CloudDest != "" {
			// 云盘请求无论成败保持 cloud 投递标记（与 worker 收尾语义一致，
			// 列表筛选"网盘"口径完整）
			finish.DeliveryMode = store.DeliveryModeCloud
		}
		if ferr := s.store.FinishRequest(ctx, d.RequestID, finish); ferr != nil {
			s.log.Error("标记队列满失败状态未落库", "request_id", d.RequestID, "error", ferr.Error())
			if s.events != nil && apperr.From(ferr).Code == apperr.CodeStoreUnavailable {
				s.events.StoreWriteFailed(ctx, "队列满终态落库")
			}
		}
		return Decision{Reason: apperr.CodeQueueFull, RequestID: d.RequestID}, nil
	}
	d.JobID = job.ID
	return d, nil
}

// deny 统一落拒绝痕迹并填充裁定；拒绝不建 requests 行、不扣额度。
func (s *Service) deny(ctx context.Context, tx *store.Store, u store.User, now time.Time, reason apperr.Code, d *Decision) error {
	if err := tx.MarkUserDenied(ctx, u.ID, string(reason), now.UnixMilli()); err != nil {
		return err
	}
	*d = Decision{Reason: reason}
	return nil
}

// StatusDenyCode 把用户状态映射为对应提示的错误码；enabled 返回空串。
// 未知状态（含用户不存在时的空状态）按未授权处理。
func StatusDenyCode(status string) apperr.Code {
	switch status {
	case store.UserEnabled:
		return ""
	case store.UserPending:
		return apperr.CodeUserPending
	case store.UserDisabled, store.UserArchived:
		return apperr.CodeUserDisabled
	default:
		return apperr.CodeUserNotAuthorized
	}
}

// UserDownloadStatus 是 /download 提交前的轻量用户预检（Submit 六步链第一步
// 的只读形态，外加用户级云盘下载权限维度）：返回状态/权限拒绝码（空串 =
// 该维度允许），用户不存在同样按未授权处理。不扣额度、不落拒绝痕迹——
// 完整裁定（去重/频率/配额/并发/队列）仍以 Submit 为准，语义零变化。
func (s *Service) UserDownloadStatus(ctx context.Context, userID int64) (apperr.Code, error) {
	u, err := s.store.GetUser(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return apperr.CodeUserNotAuthorized, nil
	}
	if err != nil {
		return "", err
	}
	if code := StatusDenyCode(u.Status); code != "" {
		return code, nil
	}
	if !u.EffectiveCloudDownload() {
		return apperr.CodeCloudDownloadDenied, nil
	}
	return "", nil
}

// SourceKind 把链接形态映射为 requests.source_kind。
func SourceKind(ref tmeurl.SourceRef) string {
	if ref.Kind == tmeurl.PeerChannelID {
		return store.SourcePrivate
	}
	return store.SourcePublic
}

// ChannelKey 生成 requests.channel_key：公开频道记 username，
// 私有频道记 -100 前缀数字 ID（与 MTProto 客户端频道标识一致的字符串形式）。
func ChannelKey(ref tmeurl.SourceRef) string {
	if ref.Kind == tmeurl.PeerChannelID {
		return fmt.Sprintf("-100%d", ref.ChannelID)
	}
	return ref.Username
}

// RefFromRequest 从请求行重建来源链接定位符（SourceKind/ChannelKey 的逆变换），
// 供受控重试复用同一行重新入队。私有频道键格式损坏时返回 false（数据异常防御）。
func RefFromRequest(r store.Request) (tmeurl.SourceRef, bool) {
	return refFromChannelKey(r.SourceKind, r.ChannelKey, r.MessageID)
}

// ---- 运营设置（时区 / 去重窗口）----

// runtime 是一次提交所需的运营设置快照。
type runtime struct {
	loc         *time.Location
	tzName      string // 当前生效的时区名（设置页回显用）
	dedupWindow time.Duration
}

// defaultLoc 是时区设置的回退值；程序启动即解析一次。
var defaultLoc = func() *time.Location {
	loc, err := time.LoadLocation(defaultTimezoneName)
	if err != nil {
		return time.UTC // 容器缺 tzdata 时的兜底；main 已引入内嵌 tzdata，正常不会走到
	}
	return loc
}()

// loadRuntime 读取运营设置；键缺失、解析失败一律回退默认值并记日志（不阻断提交）。
func (s *Service) loadRuntime(ctx context.Context) runtime {
	rt := runtime{loc: defaultLoc, tzName: defaultTimezoneName, dedupWindow: defaultDedupWindow}
	if v, ok, err := s.store.GetSetting(ctx, settingKeyTimezone); err != nil {
		s.log.Warn("读取时区设置失败，回退默认", "error", err.Error())
	} else if ok {
		var name string
		if err := json.Unmarshal([]byte(v), &name); err != nil {
			s.log.Warn("时区设置不是合法 JSON 字符串，回退默认")
		} else if loc, err := time.LoadLocation(strings.TrimSpace(name)); err != nil {
			s.log.Warn("时区设置无法解析，回退默认", "timezone", name, "error", err.Error())
		} else {
			rt.loc = loc
			rt.tzName = strings.TrimSpace(name)
		}
	}
	if v, ok, err := s.store.GetSetting(ctx, settingKeyDedupWindowMin); err != nil {
		s.log.Warn("读取去重窗口设置失败，回退默认", "error", err.Error())
	} else if ok {
		var minutes int
		if err := json.Unmarshal([]byte(v), &minutes); err != nil || minutes <= 0 {
			s.log.Warn("去重窗口设置非法，回退默认")
		} else {
			rt.dedupWindow = time.Duration(minutes) * time.Minute
		}
	}
	return rt
}

// SetTimezone 更新运营时区；仅影响后续的日额度分桶与 /usage 展示（惰性计算，
// 无 00:00 定时器）。名称非法时返回错误，不入库。
func (s *Service) SetTimezone(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if _, err := time.LoadLocation(name); err != nil {
		return apperr.New(apperr.CodeInternal, fmt.Sprintf("无效时区 %q", name))
	}
	return s.store.SetSetting(ctx, settingKeyTimezone, mustJSON(name))
}

// SetDedupWindow 更新重复链接检测窗口（分钟），立即生效。
func (s *Service) SetDedupWindow(ctx context.Context, minutes int) error {
	if minutes <= 0 {
		return apperr.New(apperr.CodeInternal, "去重窗口必须为正整数分钟")
	}
	return s.store.SetSetting(ctx, settingKeyDedupWindowMin, mustJSON(minutes))
}

// Location 返回当前运营时区（Web 页面时间渲染使用；设置异常时回退默认值）。
func (s *Service) Location(ctx context.Context) *time.Location {
	return s.loadRuntime(ctx).loc
}

// TimezoneName 返回当前生效的运营时区名（Web 设置页回显；异常回退默认名）。
func (s *Service) TimezoneName(ctx context.Context) string {
	return s.loadRuntime(ctx).tzName
}

// DedupWindowMinutes 返回当前生效的重复链接检测窗口（分钟；异常回退默认值）。
func (s *Service) DedupWindowMinutes(ctx context.Context) int {
	return int(s.loadRuntime(ctx).dedupWindow / time.Minute)
}

// mustJSON 序列化为 JSON 字符串；string/int/map[string]any 不会失败，空串仅作兜底。
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---- /start 与 /usage ----

// StartInput /start 命令的输入（携带申请者可见身份信息，供管理员审批时参考）。
type StartInput struct {
	UserID      int64
	Username    string // Telegram @username，可空
	DisplayName string // 显示名，可空
	// BotID/BotUsername 是受理 bot（首次 /start 的来源 bot，落 users 来源列）；
	// 0 = Web 管理端等非 Bot 通道。
	BotID       int64
	BotUsername string
}

// StartOutcome 是 /start 的处理结果。
type StartOutcome int

const (
	StartWelcome  StartOutcome = iota // 已启用：欢迎文案（保持 /help 同款）
	StartPending                      // 不存在或待审批：落/刷 pending 行，回复等待审批
	StartDisabled                     // 禁用/归档：账号已停用
)

// HandleStart 处理 /start：不存在的用户创建 pending 申请，已有 pending 刷新申请时间
// （幂等），enabled 保持欢迎，disabled/archived 提示已停用。
func (s *Service) HandleStart(ctx context.Context, in StartInput) (StartOutcome, error) {
	u, err := s.store.GetUser(ctx, in.UserID)
	if errors.Is(err, store.ErrNotFound) {
		if _, err := s.store.CreateUser(ctx, store.User{
			ID:          in.UserID,
			Status:      store.UserPending,
			Username:    in.Username,
			DisplayName: in.DisplayName,
			CreatedAt:   s.now().UnixMilli(), // 与服务时钟一致，避免注入时钟下时间倒挂

			// 来源 bot（首次 /start 的受理 bot；多机器人池归属展示用）
			SourceBotID:       in.BotID,
			SourceBotUsername: in.BotUsername,
		}); err != nil {
			return 0, err
		}
		// 新申请活动通知（异步，不阻塞 Bot 回复；已有 pending 的刷新不通知）
		if s.activity != nil {
			go s.activity.UserApplied(ctx, ApplicationInfo{
				UserID:            in.UserID,
				Username:          in.Username,
				DisplayName:       in.DisplayName,
				SourceBotUsername: in.BotUsername,
			})
		}
		return StartPending, nil
	}
	if err != nil {
		return 0, err
	}
	if err := s.store.UpdateUserProfile(ctx, in.UserID, in.Username, in.DisplayName); err != nil {
		return 0, err
	}
	switch u.Status {
	case store.UserEnabled:
		return StartWelcome, nil
	case store.UserPending:
		if err := s.store.TouchPendingApplication(ctx, in.UserID, s.now().UnixMilli()); err != nil {
			return 0, err
		}
		return StartPending, nil
	default:
		return StartDisabled, nil
	}
}

// Usage 是 /usage 命令的查询结果。
type Usage struct {
	Status     string    // 用户当前状态；用户不存在时为空串
	IsOwner    bool      // owner 不受频率与额度限制
	Used       int       // 运营时区当日已用
	DailyLimit int       // 当日额度上限（owner 语义上不限）
	Remaining  int       // 剩余次数；owner 为 -1 表示不限
	ResetAt    time.Time // 下一运营日 00:00（携带运营时区，直接 Format 即可）
}

// Usage 查询用户当日用量与额度（/usage 命令数据源）；用户不存在时 Status 为空串。
func (s *Service) Usage(ctx context.Context, userID int64) (Usage, error) {
	rt := s.loadRuntime(ctx)
	u, err := s.store.GetUser(ctx, userID)
	if errors.Is(err, store.ErrNotFound) {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, err
	}
	now := s.now()
	usage, err := s.store.GetUsage(ctx, userID, now.In(rt.loc).Format(dayFormat))
	if err != nil {
		return Usage{}, err
	}
	out := Usage{
		Status:     u.Status,
		IsOwner:    u.IsOwner,
		Used:       usage.Used,
		DailyLimit: u.DailyLimit,
		ResetAt:    nextMidnight(now, rt.loc),
	}
	if u.IsOwner {
		out.Remaining = -1
		return out, nil
	}
	out.Remaining = u.DailyLimit - usage.Used
	if out.Remaining < 0 {
		out.Remaining = 0
	}
	return out, nil
}

// nextMidnight 计算运营时区的下一个 00:00（额度重置时间点）。
func nextMidnight(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, loc)
}
