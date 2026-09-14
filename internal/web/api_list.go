package web

// SPA API（/api/v1）只读列表端点的公共契约：分页参数解析与校验、统一分页
// 信封和路径参数解析。具体资源端点见 api_users/api_requests/api_channels/
// api_events；错误链路与认证约定见 api.go。

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// API 列表分页边界（缺省 50、上限 200，防止一次拉全表）。页码上限远超
// 任何真实数据量，只用于封顶 offset 计算防整数溢出。
const (
	apiDefaultPageSize = 50
	apiMaxPageSize     = 200
	apiMaxPage         = 1 << 40
)

// apiPageParams 是 /api/v1 列表端点解析后的分页参数。
type apiPageParams struct {
	Page     int // 1 起
	PageSize int
	Offset   int
}

// parseAPIPageParams 解析并校验 page/page_size：page>=1、page_size 1–200，
// 缺省 page=1、page_size=50；非法值返回错误（调用方回 400 JSON）。
// 与 SSR 的静默收敛不同：API 调用方是自家前端代码，显式报错利于及早暴露
// 参数拼接 bug，也满足分页信封 total_pages 的可预期语义。
func parseAPIPageParams(r *http.Request) (apiPageParams, error) {
	q := r.URL.Query()
	page := 1
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return apiPageParams{}, errors.New("page 必须为正整数")
		}
		if n > apiMaxPage {
			return apiPageParams{}, errors.New("page 超出有效范围")
		}
		page = n
	}
	size := apiDefaultPageSize
	if v := q.Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > apiMaxPageSize {
			return apiPageParams{}, errors.New("page_size 必须为 1–200 的整数")
		}
		size = n
	}
	return apiPageParams{Page: page, PageSize: size, Offset: (page - 1) * size}, nil
}

// apiListEnvelope 是列表端点的统一分页信封：
// {"items":[...],"page":n,"page_size":n,"total":n,"total_pages":n}。
type apiListEnvelope[T any] struct {
	Items      []T `json:"items"`
	Page       int `json:"page"`
	PageSize   int `json:"page_size"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

// newAPIListEnvelope 组装分页信封；items 为 nil 时归一为空数组
// （前端不必区分 null 与 []）。
func newAPIListEnvelope[T any](items []T, p apiPageParams, total int) apiListEnvelope[T] {
	if items == nil {
		items = []T{}
	}
	return apiListEnvelope[T]{
		Items:      items,
		Page:       p.Page,
		PageSize:   p.PageSize,
		Total:      total,
		TotalPages: (total + p.PageSize - 1) / p.PageSize,
	}
}

// writeAPIList 输出列表查询响应。管理端数据一律禁止缓存（与会话 bootstrap
// 同一策略），避免共享代理留存业务行。
func writeAPIList(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeAPIJSON(w, http.StatusOK, v)
}

// writeAPISingle 输出详情查询响应（缓存策略同列表）。
func writeAPISingle(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeAPIJSON(w, http.StatusOK, v)
}

// apiBadRequest 记录参数拒绝日志并输出 400 JSON。message 必须是本包
// 受控文案（静态文案或与 SSR 页面提示同源的参数解析错误，JSON 编码输出），
// 不得包含底层错误细节、路径或凭据。
func (s *Server) apiBadRequest(w http.ResponseWriter, r *http.Request, op, message string) {
	s.log.Warn("API 请求参数非法", "op", op, "path", r.URL.Path)
	writeAPIError(w, http.StatusBadRequest, apiCodeBadRequest, message)
}

// apiPathID 解析 API 路径中的正整数 ID；非法时输出 400 JSON 并返回 false。
func (s *Server) apiPathID(w http.ResponseWriter, r *http.Request, op string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.apiBadRequest(w, r, op, "路径中的 ID 无效。")
		return 0, false
	}
	return id, true
}

// apiNotFound 输出资源不存在的 404 JSON（读路径失败，不写审计）。
func (s *Server) apiNotFound(w http.ResponseWriter, r *http.Request, op string) {
	s.writeAPIAppErr(w, r, op, store.ErrNotFound)
}

// apiDistRow 是分布类列表条目（总览与频道详情共用）；Key 为空串表示
// "未记录"（展示文案由前端统一处理）。
type apiDistRow struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}
