package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// apiMaxJSONBody 是写端点 JSON 请求体上限。设置/限额等载荷远小于该值，
// 只作防御性封顶（备份上传走 /api/v1/backup/import 的 multipart 专用上限）。
const apiMaxJSONBody = 64 << 10

// apiReadJSON 解析写端点的 JSON 请求体到 v；解析失败输出 400 JSON 并返回
// false。空请求体按零值放行（必填字段由各端点校验）。错误文案是本包受控
// 文案，不透出底层解析细节。
func (s *Server) apiReadJSON(w http.ResponseWriter, r *http.Request, op string, v any) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		s.apiBadRequest(w, r, op, "请求体必须为 application/json。")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, apiMaxJSONBody)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.Is(err, io.EOF):
			return true
		case errors.As(err, &maxErr):
			s.apiBadRequest(w, r, op, "请求体超过大小上限。")
		default:
			s.apiBadRequest(w, r, op, "请求体不是合法的 JSON。")
		}
		return false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		s.apiBadRequest(w, r, op, "请求体包含多余的 JSON 内容。")
		return false
	}
	return true
}

// writeAPIMethodNotAllowed 是 API 写端点的方法不匹配回退（GET 等非 POST 方法）：
// 输出 405 JSON，避免 fetch 收到 net/http 纯文本 405。不做鉴权，方法存在
// 与否不构成敏感信息。
func (s *Server) writeAPIMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Allow", http.MethodPost)
	writeAPIError(w, http.StatusMethodNotAllowed, apiCodeMethodNotAllowed,
		apiUserMessage(apiCodeMethodNotAllowed))
}
