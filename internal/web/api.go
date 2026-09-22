package web

// SPA API（/api/v1）通用契约：统一 JSON 错误结构、API 专用认证与 CSRF 中间件。
// 具体页面资源 API 由各页面相关功能补充；本文件只固化跨页面的公共约定：
//   - API 响应一律 JSON：未认证返回 401 JSON、CSRF 失败返回 403 JSON，
//     绝不重定向到登录 HTML，避免 fetch 误收页面；
//   - 认证复用现有 spore_session 会话（Cookie 语义与滑动续期不变），
//     会话级 CSRF token 复用 web_sessions.csrf_token，不新增第二套安全状态；
//   - 错误码是稳定的大写下划线字符串（与 apperr 风格一致），message 面向
//     管理员展示，不含底层错误细节、Session、Secret 或 token；
//   - SSR view struct 不作为 API DTO；DTO 定义在 api_ 前缀文件内。

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// API 传输层错误码：/api/v1 响应体的稳定字符串。
// 业务存储类错误（STORE_UNAVAILABLE 等）直接沿用 apperr 错误码原文，保持单一来源。
const (
	apiCodeUnauthorized     = "UNAUTHORIZED"        // 未认证：无 Cookie 或会话已过期
	apiCodeCSRFFailed       = "CSRF_FAILED"         // CSRF token 缺失或不匹配
	apiCodeForbidden        = "FORBIDDEN"           // 已认证但无权限执行该操作
	apiCodeBadRequest       = "BAD_REQUEST"         // 请求参数非法
	apiCodeNotFound         = "NOT_FOUND"           // 资源或路由不存在
	apiCodeConflict         = "CONFLICT"            // 与当前状态冲突
	apiCodeMethodNotAllowed = "METHOD_NOT_ALLOWED"  // HTTP 方法与注册路由不匹配
	apiCodeUnavailable      = "SERVICE_UNAVAILABLE" // 可选依赖未接入，操作暂不可用
	// apiCodeMTProtoOffline MTProto 用户号离线：邀请/加入类操作暂不可执行
	//（瞬态，登录恢复后重试；文案与 channel-join 页面的受控提示一致）。
	apiCodeMTProtoOffline = "MTPROTO_OFFLINE"
)

// apiCSRFHeader 是 API 变更请求携带会话级 CSRF token 的请求头。
const apiCSRFHeader = "X-CSRF-Token"

// apiUserMessage 返回错误码对应的受控中文文案（面向管理员展示）。
// API 传输层自有码在本函数维护；业务存储类错误码（STORE_UNAVAILABLE 等）
// 委托 apperr.UserText，保证用户文案只有 apperr 一个来源；未识别的码
// 由 apperr 回落内部错误文案，不透出底层细节。
func apiUserMessage(code string) string {
	switch code {
	case apiCodeUnauthorized:
		return "未登录或登录已过期。"
	case apiCodeCSRFFailed:
		return "请求校验失败，请刷新页面后重试。"
	case apiCodeForbidden:
		return "没有权限执行该操作。"
	case apiCodeBadRequest:
		return "请求参数不正确。"
	case apiCodeNotFound:
		return "资源不存在或已被删除。"
	case apiCodeConflict:
		return "操作与当前状态冲突，请刷新后重试。"
	case apiCodeMethodNotAllowed:
		return "请求方法与该端点不匹配。"
	case apiCodeUnavailable:
		return "管理服务未接入本实例，无法执行该操作。"
	case apiCodeMTProtoOffline:
		return "Telegram 用户号当前离线，请先在「Telegram 连接」页完成登录再操作。"
	default:
		return apperr.UserText(apperr.Code(code))
	}
}

// apiErrorBody 是 API 统一错误结构中的 error 对象。
type apiErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// apiErrorEnvelope 是 API 统一错误响应信封：{"error":{"code","message"}}。
type apiErrorEnvelope struct {
	Error apiErrorBody `json:"error"`
}

// writeAPIJSON 输出 JSON 响应并统一内容类型。
func writeAPIJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAPIError 输出统一 JSON 错误。message 必须是受控文案
// （apiUserMessage 或 apperr.UserText），不得传入 err.Error()、路径或 token。
func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeAPIJSON(w, status, apiErrorEnvelope{Error: apiErrorBody{Code: code, Message: message}})
}

// apiAppErrStatus 把业务错误映射为 API 状态与错误码：查无此行 404、
// 存储约束 409、存储不可用 503；写操作相关业务码按语义补充
// （RETRY_EXHAUSTED/USER_DISABLED 409、QUEUE_FULL 503、邀请链接类 400、
// MTProto 用户号离线 503），其余回落 500 与 INTERNAL_ERROR。
func apiAppErrStatus(err error) (status int, code string) {
	if errors.Is(err, store.ErrNotFound) {
		return http.StatusNotFound, apiCodeNotFound
	}
	if errors.Is(err, mtproto.ErrMembershipUnavailable) {
		// 用户号离线是瞬态资源条件：同"稍后重试"语义，受控文案提示先登录
		return http.StatusServiceUnavailable, apiCodeMTProtoOffline
	}
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		switch ae.Code {
		case apperr.CodeStoreUnavailable, apperr.CodeQueueFull, apperr.CodePeerFlood:
			// 队列饱和与 PEER_FLOOD 都是瞬态资源条件，与存储不可用同归"稍后重试"
			return http.StatusServiceUnavailable, string(ae.Code)
		case apperr.CodeStoreConstraint, apperr.CodeRetryExhausted, apperr.CodeUserDisabled,
			apperr.CodeChannelNotPostable, apperr.CodeChannelAlreadyBound:
			return http.StatusConflict, string(ae.Code)
		case apperr.CodeChannelTargetInvalid:
			return http.StatusBadRequest, string(ae.Code)
		case apperr.CodeInvalidURL, apperr.CodeInvalidInviteURL:
			return http.StatusBadRequest, string(ae.Code)
		}
	}
	return http.StatusInternalServerError, string(apperr.CodeInternal)
}

// writeAPIAppErr 记录结构化日志（含受控底层错误）并输出映射后的 JSON 错误；
// 响应 message 只用受控文案，不透出底层错误字符串。日志级别按映射结果区分：
// 4xx 是调用方可预期的错误记 Warn，5xx 才是服务端异常记 Error；
// 队列饱和（503）是可预期的运行状态，同样记 Warn 避免误报服务异常。
func (s *Server) writeAPIAppErr(w http.ResponseWriter, r *http.Request, op string, err error) {
	status, code := apiAppErrStatus(err)
	ae := apperr.From(err)
	fields := []any{"op", op, "path", r.URL.Path, "code", ae.Code, "error", err.Error()}
	if status >= http.StatusInternalServerError && ae.Code != apperr.CodeQueueFull {
		s.log.Error("API 请求失败", fields...)
	} else {
		s.log.Warn("API 请求被拒绝", fields...)
	}
	writeAPIError(w, status, code, apiUserMessage(code))
}

// apiAuth 是 API 专用认证中间件：复用现有会话验证与滑动续期；
// 未认证返回 JSON 401，不区分 Cookie 缺失与过期（与会话语义一致，避免探测）。
func (s *Server) apiAuth(next sessionHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.authenticate(r)
		if !ok {
			writeAPIError(w, http.StatusUnauthorized, apiCodeUnauthorized, apiUserMessage(apiCodeUnauthorized))
			return
		}
		s.renewSession(w, sess)
		next(w, r, sess)
	})
}

// verifyAPICSRF 校验 API 变更请求的会话级 CSRF token：JSON 请求没有表单
// 隐藏域，token 经 X-CSRF-Token 头传递，常数时间比对。
func verifyAPICSRF(r *http.Request, sess session) bool {
	tok := r.Header.Get(apiCSRFHeader)
	return tok != "" &&
		subtle.ConstantTimeCompare([]byte(tok), []byte(sess.csrf)) == 1
}

// apiCSRF 是 API 专用 CSRF 中间件：失败返回 JSON 403（而非 HTML）。
// 只应包在 apiAuth 内层使用；管理操作写路由经 server.go 的
// mountAPIWrite 统一挂载。
func (s *Server) apiCSRF(next sessionHandler) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, sess session) {
		if !verifyAPICSRF(r, sess) {
			s.log.Warn("API CSRF 校验失败", "path", r.URL.Path, "ip", s.clientIP(r))
			writeAPIError(w, http.StatusForbidden, apiCodeCSRFFailed, apiUserMessage(apiCodeCSRFFailed))
			return
		}
		next(w, r, sess)
	}
}

// handleAPINotFound 兜底 /api/v1 下未匹配的路由与方法（ServeMux 通配），
// 返回 JSON 404，避免 fetch 收到 net/http 纯文本 404 或 SPA HTML。
// 不需要鉴权：路由存在与否不构成敏感信息，也不做任何业务处理。
func (s *Server) handleAPINotFound(w http.ResponseWriter, _ *http.Request) {
	writeAPIError(w, http.StatusNotFound, apiCodeNotFound, apiUserMessage(apiCodeNotFound))
}
