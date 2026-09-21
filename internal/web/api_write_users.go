package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// ---- 用户管理 ----

// handleAPIUserAdd 处理手动添加用户（默认 enabled），业务规则经 addUserCore。
func (s *Server) handleAPIUserAdd(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.users.add"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	var in struct {
		UserID int64  `json:"user_id"`
		Note   string `json:"note"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.UserID <= 0 {
		s.apiBadRequest(w, r, op, "用户 ID 必须为正整数。")
		return
	}
	if err := s.addUserCore(r.Context(), in.UserID, strings.TrimSpace(in.Note)); err != nil {
		if apperr.From(err).Code == apperr.CodeStoreConstraint {
			// 与 SSR 相同的受控提示；409 + apperr 码让前端可区分"已存在"
			writeAPIError(w, http.StatusConflict, string(apperr.CodeStoreConstraint), "该用户已存在。")
			return
		}
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		UserID int64 `json:"user_id"`
	}{apiWriteOK{OK: true}, in.UserID})
}

// handleAPIUserStatusAction 处理启用/禁用/归档/恢复（restore 即重新启用）。
func (s *Server) handleAPIUserStatusAction(status string) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ session) {
		const op = "api.users.status"
		if !s.apiRequireAccess(w, r, op) {
			return
		}
		id, ok := s.apiPathID(w, r, op)
		if !ok {
			return
		}
		if err := s.access.SetUserStatus(r.Context(), "admin", id, status); err != nil {
			s.writeAPIUserOpErr(w, r, op, err)
			return
		}
		writeAPIJSON(w, http.StatusOK, struct {
			apiWriteOK
			Status string `json:"status"`
		}{apiWriteOK{OK: true}, status})
	}
}

// handleAPIUserLimits 处理限额调整：三项均可缺省保持原值；提供值必须为正
// 且不超 userLimitRules 上限（与 SSR 表单共用同一边界来源）。
func (s *Server) handleAPIUserLimits(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.users.limits"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	var in struct {
		SubmitIntervalSec *int `json:"submit_interval_sec"`
		DailyLimit        *int `json:"daily_limit"`
		ConcurrentLimit   *int `json:"concurrent_limit"`
		BindLimit         *int `json:"bind_limit"` // 0 = 跟随角色默认；1–20 = 显式值
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	values := []*int{in.SubmitIntervalSec, in.DailyLimit, in.ConcurrentLimit}
	bad := make([]string, 0, len(userLimitRules))
	for i, v := range values {
		if v == nil {
			continue
		}
		if *v <= 0 || *v > userLimitRules[i].Max {
			bad = append(bad, userLimitRules[i].Name)
		}
	}
	// 频道绑定上限允许 0（恢复跟随角色默认），显式值 1–20
	if in.BindLimit != nil && (*in.BindLimit < 0 || *in.BindLimit > userBindLimitMax) {
		bad = append(bad, "频道绑定上限")
	}
	if len(bad) > 0 {
		s.apiBadRequest(w, r, op,
			"限额取值非法（须为正整数且不超上限，缺省保持不变）；字段："+strings.Join(bad, "、"))
		return
	}
	opt := func(v *int) int {
		if v == nil {
			return 0 // 与 SSR 留空保持原值同语义
		}
		return *v
	}
	if err := s.access.SetUserLimits(r.Context(), "admin", id,
		opt(in.SubmitIntervalSec), opt(in.DailyLimit), opt(in.ConcurrentLimit), in.BindLimit); err != nil {
		s.writeAPIUserOpErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}

// handleAPIUserSetCloudDownload 处理用户级云盘下载权限三态设置
// （0=跟随角色默认 1=显式允许 2=显式拒绝；owner 可被显式拒绝）。
func (s *Server) handleAPIUserSetCloudDownload(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.users.set_cloud_download"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	var in struct {
		CloudDownload *int `json:"cloud_download"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.CloudDownload == nil {
		s.apiBadRequest(w, r, op, "cloud_download 取值必须为整数（0=跟随默认 1=允许 2=拒绝）。")
		return
	}
	mode := *in.CloudDownload
	if mode < store.CloudDownloadDefault || mode > store.CloudDownloadDeny {
		s.apiBadRequest(w, r, op, "cloud_download 取值必须为 0（跟随默认）、1（允许）或 2（拒绝）。")
		return
	}
	if err := s.access.SetUserCloudDownload(r.Context(), "admin", id, mode); err != nil {
		s.writeAPIUserOpErr(w, r, op, err)
		return
	}
	u, err := s.st.GetUser(r.Context(), id)
	if err != nil {
		s.writeAPIUserOpErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		CloudDownload          int  `json:"cloud_download"`
		EffectiveCloudDownload bool `json:"effective_cloud_download"`
	}{apiWriteOK{OK: true}, u.CloudDownload, u.EffectiveCloudDownload()})
}

// handleAPIUserSetAutoPin 处理用户级自动置顶偏好开关（开启后该用户普通
// 任务提交即默认置顶；云盘/缓存补写任务不适用，/pin 单次指定不受影响）。
func (s *Server) handleAPIUserSetAutoPin(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.users.set_auto_pin"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	var in struct {
		AutoPin *bool `json:"auto_pin"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.AutoPin == nil {
		s.apiBadRequest(w, r, op, "auto_pin 取值必须为布尔。")
		return
	}
	if err := s.access.SetUserAutoPin(r.Context(), "admin", id, *in.AutoPin); err != nil {
		s.writeAPIUserOpErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		AutoPin bool `json:"auto_pin"`
	}{apiWriteOK{OK: true}, *in.AutoPin})
}

// handleAPIUserResetQuota 处理重置当日已用额度。
func (s *Server) handleAPIUserResetQuota(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.users.reset_quota"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	if err := s.access.ResetDailyUsage(r.Context(), "admin", id); err != nil {
		s.writeAPIUserOpErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}

// handleAPIUserSetOwner 处理 owner 设置/取消（业务核心经 setOwnerCore：
// 设为 owner 后补发暂存事件）。
func (s *Server) handleAPIUserSetOwner(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.users.set_owner"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	var in struct {
		Owner *bool `json:"owner"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if in.Owner == nil {
		s.apiBadRequest(w, r, op, "owner 取值必须为布尔。")
		return
	}
	if err := s.setOwnerCore(r.Context(), id, *in.Owner); err != nil {
		s.writeAPIUserOpErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		IsOwner bool `json:"is_owner"`
	}{apiWriteOK{OK: true}, *in.Owner})
}

// handleAPIUserRefreshProfile 从当前可用 Telegram 上下文刷新资料
// （核心经 refreshUserProfileCore；保留 SSR 的 Profile 依赖缺失不可用语义）。
func (s *Server) handleAPIUserRefreshProfile(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.users.refresh_profile"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	out := s.refreshUserProfileCore(r.Context(), id)
	switch {
	case out.OK:
		writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
	case out.Err != nil:
		s.writeAPIAppErr(w, r, op, out.Err)
	case out.Reason == "no_context":
		writeAPIError(w, http.StatusServiceUnavailable, "TELEGRAM_UNAVAILABLE",
			"Telegram 通道未就绪或没有可用的用户上下文。")
	default:
		writeAPIError(w, http.StatusServiceUnavailable, "TELEGRAM_UNAVAILABLE",
			"Telegram 通道未就绪、无权限或没有可用的用户上下文，已保留原资料。")
	}
}

// writeAPIUserOpErr 把用户管理写操作错误转为 JSON 错误：查无此行走统一
// 404 链路，其余复用 API 公共状态映射，message 使用用户管理受控文案。
func (s *Server) writeAPIUserOpErr(w http.ResponseWriter, r *http.Request, op string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	ae := apperr.From(err)
	s.log.Warn("用户管理操作失败", "op", op, "code", ae.Code, "error", err.Error())
	status, code := apiAppErrStatus(err)
	writeAPIError(w, status, code, userOpText(ae))
}
