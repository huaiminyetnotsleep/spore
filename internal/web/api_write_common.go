package web

// POST /api/v1 管理操作写端点是管理操作的唯一写入口：
// 申请审批、用户管理、请求受控重试、事件解决、运营设置与会话登出。
//
// 契约约定：
//   - 全部写端点挂 apiAuth(apiCSRF(handler))（会话 Cookie + X-CSRF-Token 头）；
//   - 业务规则不复制：写端点调用与原 SSR 表单同一套核心操作
//     （internal/access 方法或本包 *Core 提取函数），校验、事务、审计与错误
//     分类保持一致；
//   - 请求体为 JSON（仅带载荷的端点解析）；响应一律 JSON，成功为
//     {"ok":true,...} 摘要，失败经 writeAPIAppErr / 受控文案输出统一错误信封。

import "net/http"

// apiWriteOK 是写操作成功响应的公共字段。
type apiWriteOK struct {
	OK bool `json:"ok"`
}

type apiIDsRequest struct {
	IDs []int64 `json:"ids"`
}

type apiDeletedResult struct {
	apiWriteOK
	Deleted int `json:"deleted"`
}

// apiRequireAccess 校验 access 服务已注入；
// 未注入时输出 503 JSON 并返回 false。
func (s *Server) apiRequireAccess(w http.ResponseWriter, r *http.Request, op string) bool {
	if s.access != nil {
		return true
	}
	s.log.Warn("API 管理服务未接入", "op", op, "path", r.URL.Path)
	writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
		apiUserMessage(apiCodeUnavailable))
	return false
}
