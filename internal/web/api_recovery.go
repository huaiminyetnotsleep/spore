package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/recovery"
	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// RecoveryManager 是管理端对持久化历史恢复服务的最小依赖。
// 创建与控制的成功审计由服务和状态变更在同一事务内保存。
type RecoveryManager interface {
	Preview(context.Context, recovery.Input) (recovery.Preview, error)
	Create(context.Context, recovery.Input) (store.RecoveryJob, error)
	Control(context.Context, int64, string) (store.RecoveryJob, error)
	List(context.Context, int, int) ([]store.RecoveryJob, int, error)
	Get(context.Context, int64) (store.RecoveryJob, error)
	Items(context.Context, int64, string, int, int) ([]store.RecoveryItem, int, error)
}

func (s *Server) registerAPIRecoveryRoutes(mux *http.ServeMux) {
	s.mountAPIWrite(mux, "/api/v1/recovery/preview", s.handleAPIRecoveryPreview)
	mux.Handle("GET /api/v1/recovery/jobs", s.apiAuth(s.handleAPIRecoveryJobs))
	mux.Handle("POST /api/v1/recovery/jobs", s.apiAuth(s.apiCSRF(s.handleAPIRecoveryCreate)))
	mux.Handle("GET /api/v1/recovery/jobs/{id}", s.apiAuth(s.handleAPIRecoveryJob))
	mux.Handle("GET /api/v1/recovery/jobs/{id}/items", s.apiAuth(s.handleAPIRecoveryItems))
	for _, action := range []string{"pause", "resume", "cancel", "retry"} {
		s.mountAPIWrite(mux, "/api/v1/recovery/jobs/{id}/"+action, s.handleAPIRecoveryControl(action))
	}
}

func (s *Server) apiRequireRecovery(w http.ResponseWriter, r *http.Request, op string) bool {
	if !s.apiRequireAccess(w, r, op) {
		return false
	}
	if s.recovery == nil {
		writeAPIError(w, http.StatusServiceUnavailable, apiCodeUnavailable, "历史恢复服务未接入本实例。")
		return false
	}
	return true
}

func (s *Server) readRecoveryInput(w http.ResponseWriter, r *http.Request, op string) (recovery.Input, bool) {
	var in recovery.Input
	if !s.apiReadJSON(w, r, op, &in) {
		return in, false
	}
	in.Target = strings.TrimSpace(in.Target)
	in.Filter.ChannelKey = strings.TrimSpace(in.Filter.ChannelKey)
	if in.Target == "" || len(in.Target) > 256 {
		s.apiBadRequest(w, r, op, "请填写新目标的频道或超级群组 ID，或公开用户名。")
		return in, false
	}
	if in.BotID < 0 || in.Filter.UserID < 0 || len(in.Filter.ChannelKey) > 128 {
		s.apiBadRequest(w, r, op, "机器人、用户或来源筛选参数无效。")
		return in, false
	}
	if in.Filter.Since < 0 || in.Filter.Until < 0 || (in.Filter.Until != 0 && in.Filter.Until <= in.Filter.Since) {
		s.apiBadRequest(w, r, op, "历史时间范围无效，结束时间必须晚于开始时间。")
		return in, false
	}
	return in, true
}

func (s *Server) writeAPIRecoveryErr(w http.ResponseWriter, r *http.Request, op string, err error) {
	var ae *apperr.AppError
	if errors.As(err, &ae) {
		switch ae.Code {
		case apperr.CodeBotDisabled:
			writeAPIError(w, http.StatusServiceUnavailable, string(ae.Code), "所选机器人不可用，请检查机器人状态或选择其他机器人。")
			return
		case apperr.CodeSendTargetInvalid:
			writeAPIError(w, http.StatusConflict, string(ae.Code), "机器人无法向该目标发送，请先加入目标并授予发送权限。")
			return
		}
	}
	s.writeAPIAppErr(w, r, op, err)
}

func (s *Server) handleAPIRecoveryPreview(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.recovery.preview"
	if !s.apiRequireRecovery(w, r, op) {
		return
	}
	in, ok := s.readRecoveryInput(w, r, op)
	if !ok {
		return
	}
	out, err := s.recovery.Preview(r.Context(), in)
	if err != nil {
		s.writeAPIRecoveryErr(w, r, op, err)
		return
	}
	writeAPISingle(w, out)
}

func (s *Server) handleAPIRecoveryCreate(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.recovery.create"
	if !s.apiRequireRecovery(w, r, op) {
		return
	}
	in, ok := s.readRecoveryInput(w, r, op)
	if !ok {
		return
	}
	out, err := s.recovery.Create(r.Context(), in)
	if err != nil {
		s.writeAPIRecoveryErr(w, r, op, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeAPIJSON(w, http.StatusCreated, out)
}

func (s *Server) handleAPIRecoveryJobs(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.recovery.list"
	if !s.apiRequireRecovery(w, r, op) {
		return
	}
	p, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, "分页参数无效。")
		return
	}
	items, total, err := s.recovery.List(r.Context(), p.Page, p.PageSize)
	if err != nil {
		s.writeAPIRecoveryErr(w, r, op, err)
		return
	}
	writeAPIList(w, newAPIListEnvelope(items, p, total))
}

func (s *Server) handleAPIRecoveryJob(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.recovery.get"
	if !s.apiRequireRecovery(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	out, err := s.recovery.Get(r.Context(), id)
	if err != nil {
		s.writeAPIRecoveryErr(w, r, op, err)
		return
	}
	writeAPISingle(w, out)
}

func (s *Server) handleAPIRecoveryItems(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.recovery.items"
	if !s.apiRequireRecovery(w, r, op) {
		return
	}
	id, ok := s.apiPathID(w, r, op)
	if !ok {
		return
	}
	p, err := parseAPIPageParams(r)
	if err != nil {
		s.apiBadRequest(w, r, op, "分页参数无效。")
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "pending", "processing", "succeeded", "failed", "unrecoverable", "uncertain", "skipped":
	default:
		s.apiBadRequest(w, r, op, "恢复项目状态无效。")
		return
	}
	items, total, err := s.recovery.Items(r.Context(), id, status, p.Page, p.PageSize)
	if err != nil {
		s.writeAPIRecoveryErr(w, r, op, err)
		return
	}
	writeAPIList(w, newAPIListEnvelope(items, p, total))
}

func (s *Server) handleAPIRecoveryControl(action string) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, _ session) {
		op := "api.recovery." + action
		if !s.apiRequireRecovery(w, r, op) {
			return
		}
		id, ok := s.apiPathID(w, r, op)
		if !ok {
			return
		}
		out, err := s.recovery.Control(r.Context(), id, action)
		if err != nil {
			s.writeAPIRecoveryErr(w, r, op, err)
			return
		}
		writeAPISingle(w, out)
	}
}
