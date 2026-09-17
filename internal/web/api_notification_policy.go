package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/notify"
	"github.com/huaiminyetnotsleep/spore/internal/notifycfg"
)

func (s *Server) handleAPINotificationEventCatalog(w http.ResponseWriter, _ *http.Request, _ session) {
	// 事件目录是小型静态集合，不分页；仍使用统一的 no-store 单值响应，
	// 避免把数组误包装成前端列表分页信封。
	writeAPISingle(w, notify.Catalog())
}

func (s *Server) handleAPINotificationPolicyGet(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.notification.policy.get"
	if !s.apiRequireNotification(w, r, op) {
		return
	}
	policy, err := s.notification.PolicySnapshot(r.Context())
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPISingle(w, policy)
}

func (s *Server) handleAPINotificationPolicyPut(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.notification.policy.update"
	if !s.apiRequireNotification(w, r, op) {
		return
	}
	var policy notifycfg.Policy
	if !s.apiReadJSON(w, r, op, &policy) {
		return
	}
	if err := s.notification.SavePolicy(r.Context(), policy); err != nil {
		s.writeNotificationPolicyError(w, r, op, err)
		return
	}
	saved, err := s.notification.PolicySnapshot(r.Context())
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.audit(r.Context(), "settings.notification_policy", "settings", map[string]any{
		"fields": []string{"version", "minimum_severity", "categories", "events"},
	})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Message string           `json:"message"`
		Policy  notifycfg.Policy `json:"policy"`
	}{apiWriteOK: apiWriteOK{OK: true}, Message: "通知策略已保存。", Policy: saved})
}

func (s *Server) handleAPINotificationMutesGet(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.notification.mutes.get"
	if !s.apiRequireNotification(w, r, op) {
		return
	}
	mutes, err := s.notification.ListMutes(r.Context())
	if err != nil {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	// 静音计划数量受配置上限约束，不分页；返回原始数组便于页面逐条管理。
	writeAPISingle(w, mutes)
}

func (s *Server) handleAPINotificationMuteCreate(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.notification.mute.create"
	if !s.apiRequireNotification(w, r, op) {
		return
	}
	var mute notifycfg.MuteSchedule
	if !s.apiReadJSON(w, r, op, &mute) {
		return
	}
	created, err := s.notification.CreateMute(r.Context(), mute)
	if err != nil {
		s.writeNotificationPolicyError(w, r, op, err)
		return
	}
	s.audit(r.Context(), "notification.mute.create", "notification_mute:"+created.ID, map[string]any{
		"id": created.ID, "match_mode": created.MatchMode, "enabled": created.Enabled,
	})
	writeAPIJSON(w, http.StatusCreated, created)
}

func (s *Server) handleAPINotificationMuteUpdate(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.notification.mute.update"
	if !s.apiRequireNotification(w, r, op) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.apiBadRequest(w, r, op, "静音计划 ID 不能为空。")
		return
	}
	var mute notifycfg.MuteSchedule
	if !s.apiReadJSON(w, r, op, &mute) {
		return
	}
	updated, err := s.notification.UpdateMute(r.Context(), id, mute)
	if err != nil {
		s.writeNotificationPolicyError(w, r, op, err)
		return
	}
	s.audit(r.Context(), "notification.mute.update", "notification_mute:"+updated.ID, map[string]any{
		"id": updated.ID, "match_mode": updated.MatchMode, "enabled": updated.Enabled,
	})
	writeAPIJSON(w, http.StatusOK, updated)
}

func (s *Server) handleAPINotificationMuteDelete(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.notification.mute.delete"
	if !s.apiRequireNotification(w, r, op) {
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		s.apiBadRequest(w, r, op, "静音计划 ID 不能为空。")
		return
	}
	if err := s.notification.DeleteMute(r.Context(), id); err != nil {
		s.writeNotificationPolicyError(w, r, op, err)
		return
	}
	s.audit(r.Context(), "notification.mute.delete", "notification_mute:"+id, map[string]any{"id": id})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Message string `json:"message"`
	}{apiWriteOK: apiWriteOK{OK: true}, Message: "静音计划已删除。"})
}

func (s *Server) writeNotificationPolicyError(w http.ResponseWriter, r *http.Request, op string, err error) {
	if errors.Is(err, notifycfg.ErrMuteNotFound) {
		writeAPIError(w, http.StatusNotFound, apiCodeNotFound, apiUserMessage(apiCodeNotFound))
		return
	}
	var validationErr *notifycfg.ValidationError
	if errors.As(err, &validationErr) {
		s.apiBadRequest(w, r, op, validationErr.Error())
		return
	}
	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	// 配置损坏、读取失败和写入失败不是请求参数错误，统一走受控 5xx
	// 响应；不得把任意 error 字符串直接返回给 API 调用方。
	s.log.Error("通知策略操作失败", "op", op, "path", r.URL.Path, "error", err.Error())
	writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal),
		apperr.UserText(apperr.CodeInternal))
}
