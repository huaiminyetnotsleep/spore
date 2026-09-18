// Package apperr 定义统一错误模型：机器可读错误码 + 用户中文提示。
// 设计见 docs/reference/architecture.md 第 5 节；提示语义参考旧 src/util/errors.ts。
//
// 本包保持零第三方依赖：gotd / Bot API 特定错误的分类由调用层完成后，
// 通过 Wrap/New 构造 AppError，再经 From 统一兜底。
package apperr

import (
	"errors"
	"fmt"
)

// Code 是机器可读的业务错误码。
type Code string

const (
	CodeInvalidURL          Code = "INVALID_URL"
	CodeInvalidInviteURL    Code = "INVALID_INVITE_URL"
	CodeMessageNotFound     Code = "MESSAGE_NOT_FOUND"
	CodeChannelInaccessible Code = "CHANNEL_NOT_ACCESSIBLE"
	CodeServiceMessage      Code = "SERVICE_MESSAGE"
	CodeMediaUnsupported    Code = "MEDIA_UNSUPPORTED"
	CodeFileTooLarge        Code = "FILE_TOO_LARGE"
	CodeTempDirFull         Code = "TEMP_DIR_FULL"
	CodeMediaDownloadFailed Code = "MEDIA_DOWNLOAD_FAILED"
	CodeRateLimited         Code = "TELEGRAM_RATE_LIMIT"
	CodeSendFailed          Code = "BOT_SEND_FAILED"
	// 传输与目标细分码：把大筐码（INTERNAL_ERROR / MEDIA_DOWNLOAD_FAILED /
	// BOT_SEND_FAILED）中可定位的失败原因拆出来，请求记录里能直接看出
	// 问题出在本机网络、Telegram 服务端、源内容还是发送目标；未命中的
	// 场景仍由各自的大筐码兜底。
	CodeNetworkError         Code = "NETWORK_ERROR"         // 网络连接失败或超时（重试通常可恢复）
	CodeTelegramServer       Code = "TELEGRAM_SERVER_ERROR" // Telegram 服务端故障（RPC 5xx）
	CodeFileReferenceInvalid Code = "FILE_REFERENCE_INVALID"
	// CodeFileReferenceInvalid 源媒体 file reference 失效（过期或彻底失效）且
	// 刷新后仍不可得：通常意味着源内容已被删除或更换，重试无意义。
	CodeSendTargetInvalid Code = "SEND_TARGET_INVALID" // 发送目标不可用（聊天不存在/机器人被拒/权限不足）
	// CodeLargeChannelUnavailable 大文件直传通道（Bot 号 MTProto 会话）未就绪：
	// 超过 Bot API 上限的媒体无法发送，小文件不受影响。
	CodeLargeChannelUnavailable Code = "LARGE_CHANNEL_UNAVAILABLE"
	CodeStoreUnavailable        Code = "STORE_UNAVAILABLE"
	CodeStoreMigration          Code = "STORE_MIGRATION_FAILED"
	CodeStoreConstraint         Code = "STORE_CONSTRAINT"
	CodeInternal                Code = "INTERNAL_ERROR"

	// 访问控制拒绝码（internal/access 准入链的拒绝原因，供 UserText 转用户文案）。
	// 注意与 CodeRateLimited（TELEGRAM 侧 429）区分：本组是用户提交频率超限。
	CodeUserNotAuthorized Code = "USER_NOT_AUTHORIZED" // 用户不存在于白名单
	CodeUserPending       Code = "USER_PENDING"        // 申请待审批
	CodeUserDisabled      Code = "USER_DISABLED"       // 禁用或归档
	CodeDuplicateLink     Code = "DUPLICATE_LINK"      // 去重窗口内重复链接
	CodeSubmitRateLimited Code = "RATE_LIMITED"        // 提交间隔未到
	CodeQuotaExceeded     Code = "QUOTA_EXCEEDED"      // 当日额度耗尽
	CodeConcurrentLimited Code = "CONCURRENT_LIMIT"    // 未完成任务数超限
	CodeQueueFull         Code = "QUEUE_FULL"          // 内存队列饱和

	// 任务生命周期码（internal/queue 阶段埋点与受控重试使用）。
	CodeInterrupted      Code = "INTERRUPTED"       // 进程退出/重启中断的未完成任务
	CodeRequestCancelled Code = "REQUEST_CANCELLED" // 管理员主动取消的请求
	CodeRetryExhausted   Code = "RETRY_EXHAUSTED"   // 重试超上限（累计尝试含首次最多 3 次）

	// Web 管理端认证码（internal/web 登录、会话与 OAuth 边界使用；
	// 文案面向管理员展示在登录/结果页面，不向 Telegram 用户发送）。
	CodeWebAuthFailed       Code = "WEB_AUTH_FAILED"       // 访问密钥错误或 OAuth 账号未绑定
	CodeWebLoginLocked      Code = "WEB_LOGIN_LOCKED"      // 登录失败次数过多被限流锁定
	CodeWebCSRFInvalid      Code = "WEB_CSRF_INVALID"      // CSRF token 缺失或不匹配
	CodeOAuthStateInvalid   Code = "OAUTH_STATE_INVALID"   // OAuth state 缺失、过期或已使用
	CodeOAuthExchangeFailed Code = "OAUTH_EXCHANGE_FAILED" // 与 GitHub 的 token/账号交换失败

	// 云盘下载码（internal/cloudarchive rclone 子进程错误分类 + queue 云盘任务边界）。
	// 用户文案不携带目的地凭据与远端路径细节；原始 stderr 只进结构化日志。
	CodeCloudAuthFailed    Code = "CLOUD_AUTH_FAILED"    // 网盘账号验证失败（登录/凭据错误）
	CodeCloudQuota         Code = "CLOUD_QUOTA"          // 网盘空间不足
	CodeCloudNetwork       Code = "CLOUD_NETWORK"        // 网盘网络异常
	CodeCloudUploadFailed  Code = "CLOUD_UPLOAD_FAILED"  // 其余上传失败（含 rclone 不可用）的兜底
	CodeCloudUploadTimeout Code = "CLOUD_UPLOAD_TIMEOUT" // 云盘上传超过任务时限
	CodeCloudTextOnly      Code = "CLOUD_TEXT_ONLY"      // 纯文本消息无媒体可上传
	CodeCloudVerifyFailed  Code = "CLOUD_VERIFY_FAILED"  // 同链接已上传核验（远端存在性检查）暂时不可用
	// CodeCloudDownloadDenied 用户级云盘下载权限拒绝（users.cloud_download
	// 判定不允许 /download；与全局开关关闭的 CLOUD_DISABLED 语义不同）。
	CodeCloudDownloadDenied Code = "CLOUD_DOWNLOAD_DENIED"

	// 频道绑定码（internal/binding 绑定/解绑流程使用）。
	CodeChannelTargetInvalid Code = "CHANNEL_TARGET_INVALID" // 频道标识无法解析
	CodeChannelNotPostable   Code = "CHANNEL_NOT_POSTABLE"   // 机器人不是该频道管理员或无发言权限
	CodeChannelAlreadyBound  Code = "CHANNEL_ALREADY_BOUND"  // 该频道已被其他用户绑定
	CodeChannelBindLimit     Code = "CHANNEL_BIND_LIMIT"     // 达到频道绑定数量上限
)

var userTexts = map[Code]string{
	CodeInvalidURL:              "无法识别有效的 t.me 消息链接，请检查后重试。",
	CodeInvalidInviteURL:        "无法识别有效的频道邀请链接，请发送完整的 t.me/+… 邀请链接后重试。",
	CodeMessageNotFound:         "找不到这条消息，可能已删除或链接无效。",
	CodeChannelInaccessible:     "无法访问该频道：系统读取账号未加入该频道。可发送 /join 频道邀请链接（t.me/+…）让它加入；链接可用你入群时拿到的那个，或向频道管理员索取。",
	CodeServiceMessage:          "这是一条服务消息，没有可提取的内容。",
	CodeMediaUnsupported:        "暂不支持这种消息类型。",
	CodeFileTooLarge:            "文件超过大小上限，暂无法发送。",
	CodeTempDirFull:             "临时目录空间已满，请稍后重试。",
	CodeMediaDownloadFailed:     "媒体下载失败，请稍后重试。",
	CodeNetworkError:            "网络连接失败或超时，请稍后重试。",
	CodeTelegramServer:          "Telegram 服务暂时故障，请稍后重试。",
	CodeFileReferenceInvalid:    "源消息的媒体引用已失效且无法刷新，内容可能已被删除或更换，请确认后重试。",
	CodeSendTargetInvalid:       "消息发送目标不可用：机器人可能已离开你的绑定频道或缺少发言权限，请重新绑定频道或联系管理员。",
	CodeRateLimited:             "请求过于频繁，请稍后重试。",
	CodeSendFailed:              "发送失败，请稍后重试。",
	CodeLargeChannelUnavailable: "大文件发送通道暂不可用，请稍后重试。",
	CodeStoreUnavailable:        "存储服务暂时不可用，请稍后重试。",
	CodeStoreMigration:          "存储初始化失败，请联系管理员。",
	CodeStoreConstraint:         "操作与现有数据冲突，请检查后重试。",
	CodeInternal:                "处理失败，请稍后重试。",

	CodeUserNotAuthorized: "此机器人仅限白名单用户使用，请先发送 /start 申请。",
	CodeUserPending:       "你的申请正在等待管理员审批，通过后即可使用。",
	CodeUserDisabled:      "账号已停用，如有疑问请联系管理员。",
	CodeDuplicateLink:     "该链接刚刚已处理，无需重复提交。",
	CodeSubmitRateLimited: "提交过于频繁，请稍后再试。",
	CodeQuotaExceeded:     "今日额度已用完，额度每天自动重置。",
	CodeConcurrentLimited: "你还有未完成的任务，请等待完成后再提交。",
	CodeQueueFull:         "当前任务较多，请稍后再试。",
	CodeInterrupted:       "任务因服务重启被中断，请稍后重新发送链接。",
	CodeRequestCancelled:  "请求已取消。",
	CodeRetryExhausted:    "该请求已达到最大尝试次数，无法再次重试。",

	CodeWebAuthFailed:       "登录失败：访问密钥或账号不正确。",
	CodeWebLoginLocked:      "尝试次数过多，请稍后再试。",
	CodeWebCSRFInvalid:      "请求校验失败，请刷新页面后重试。",
	CodeOAuthStateInvalid:   "登录状态校验失败，请重新发起登录。",
	CodeOAuthExchangeFailed: "GitHub 登录暂时不可用，请稍后重试。",

	CodeChannelTargetInvalid: "无法识别该频道标识，请发送频道用户名（如 @mychannel）、t.me/频道 链接或 -100 开头的频道 ID。",
	CodeChannelNotPostable:   "我还不是这个频道的管理员（或没有发送消息权限）。请先把我拉进频道并设置为管理员，再重试绑定。",
	CodeChannelAlreadyBound:  "该频道已被其他用户绑定。",
	CodeChannelBindLimit:     "已达到可绑定频道的数量上限，可先解绑不需要的频道再绑定。",

	CodeCloudAuthFailed:     "网盘账号验证失败，请联系管理员。",
	CodeCloudQuota:          "网盘空间不足。",
	CodeCloudNetwork:        "网盘网络异常，请稍后重试。",
	CodeCloudUploadFailed:   "网盘上传失败，请稍后重试。",
	CodeCloudUploadTimeout:  "网盘上传超时，请稍后重试。",
	CodeCloudTextOnly:       "纯文本消息不支持网盘下载。",
	CodeCloudVerifyFailed:   "云盘文件核验暂时不可用，请稍后重试。",
	CodeCloudDownloadDenied: "管理员未开放你的云盘下载权限，如有疑问请联系管理员。",
}

// AppError 是统一业务错误：Code 供分支判断，Message 仅进日志，Cause 保留原始错误。
type AppError struct {
	Code    Code
	Message string // 内部描述，可为空（回落到用户文案）
	Cause   error
}

// New 创建无底层原因的错误。
func New(code Code, message string) *AppError {
	return &AppError{Code: code, Message: message}
}

// Wrap 把底层错误归类为 AppError，描述回落到该错误码的标准用户文案。
func Wrap(code Code, cause error) *AppError {
	return &AppError{Code: code, Cause: cause}
}

func (e *AppError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = UserText(e.Code)
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, msg, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, msg)
}

func (e *AppError) Unwrap() error { return e.Cause }

// UserText 返回可直接展示给用户的中文提示；未知码回落 INTERNAL_ERROR 文案。
func UserText(code Code) string {
	if t, ok := userTexts[code]; ok {
		return t
	}
	return userTexts[CodeInternal]
}

// From 把任意错误归一为 *AppError：
// 已经是 AppError 的原样返回；未知错误包装为 INTERNAL_ERROR。
func From(err error) *AppError {
	var ae *AppError
	if errors.As(err, &ae) {
		return ae
	}
	return &AppError{Code: CodeInternal, Message: "未分类错误", Cause: err}
}
