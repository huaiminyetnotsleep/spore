package web

// 通知设置 API：
//   - GET/PUT /api/v1/notification/config：读取脱敏配置、原子保存 Bot 与 Webhook；
//   - POST /api/v1/notification/test：使用已保存配置发送测试消息；
//   - POST /api/v1/notification/bot/chat-id：通过已保存 Bot Token 获取最近会话。
//
// Token、Webhook URL 与签名密钥只在 internal/notifycfg 内存中解密；响应、日志与审计
// 不包含敏感值。

import (
	"errors"
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/notifycfg"
	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

func (s *Server) apiRequireNotification(w http.ResponseWriter, r *http.Request, op string) bool {
	if s.notification != nil {
		return true
	}
	s.log.Warn("API 通知配置未接入", "op", op, "path", r.URL.Path)
	writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable,
		apiUserMessage(apiCodeUnavailable))
	return false
}

func (s *Server) handleAPINotificationConfigGet(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.notification.config.get"
	if !s.apiRequireNotification(w, r, op) {
		return
	}
	view, err := s.notification.Snapshot(r.Context())
	if err != nil {
		s.log.Error("读取通知配置失败", "op", op, "error", err.Error())
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPISingle(w, view)
}

type notificationConfigInput struct {
	AutomaticEvents bool                   `json:"automatic_events"`
	Bot             notifycfg.BotInput     `json:"bot"`
	Webhook         notifycfg.WebhookInput `json:"webhook"`
}

func (s *Server) handleAPINotificationConfigPut(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.notification.config.update"
	if !s.apiRequireNotification(w, r, op) {
		return
	}
	var in notificationConfigInput
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if err := s.notification.Save(r.Context(), notifycfg.ConfigInput{
		AutomaticEvents: in.AutomaticEvents, Bot: in.Bot, Webhook: in.Webhook,
	}); err != nil {
		var appErr *apperr.AppError
		if errors.As(err, &appErr) {
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	view, err := s.notification.Snapshot(r.Context())
	if err != nil {
		s.log.Error("保存后读取通知配置失败", "op", op, "error", err.Error())
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	s.audit(r.Context(), "settings.notification", "settings", map[string]any{
		"fields": []string{"automatic_events", "bot", "webhook"},
		"effect": "通道配置与自动事件通知设置已保存",
	})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Message string         `json:"message"`
		Config  notifycfg.View `json:"config"`
	}{apiWriteOK: apiWriteOK{OK: true}, Message: "通知配置已保存。", Config: view})
}

func (s *Server) handleAPINotificationTest(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.notification.test"
	if !s.apiRequireNotification(w, r, op) {
		return
	}
	var in struct {
		Channel string `json:"channel"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	if err := s.notification.TestSaved(r.Context(), in.Channel,
		syscfg.Name(r.Context(), s.st)+" 通知测试成功"); err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	s.audit(r.Context(), "notification.test", "notification:"+in.Channel, map[string]any{
		"channel": in.Channel,
	})
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Message string `json:"message"`
	}{apiWriteOK: apiWriteOK{OK: true}, Message: "测试消息已发送。"})
}

func (s *Server) handleAPINotificationBotChatID(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.notification.bot.chat_id"
	if !s.apiRequireNotification(w, r, op) {
		return
	}
	var in struct{}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	chat, err := s.notification.RecentBotChat(r.Context())
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Message string `json:"message"`
		ChatID  string `json:"chat_id"`
	}{apiWriteOK: apiWriteOK{OK: true}, Message: "已获取最近会话 Chat ID。", ChatID: chat.ID})
}
