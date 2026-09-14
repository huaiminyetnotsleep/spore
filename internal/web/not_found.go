package web

import (
	"net/http"
)

const neutralNotFoundMessage = "请求的资源不存在或当前不可用。"

// handleNotFound 返回普通未匹配路径的受控 404，不回显请求路径或底层错误。
// /api/v1、/admin 和 /assets/ 等更具体的路由不会进入此处理器。
func (s *Server) handleNotFound(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(neutralNotFoundMessage + "\n"))
}
