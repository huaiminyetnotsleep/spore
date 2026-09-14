package web

// 系统设置（系统身份）API：GET/POST /api/v1/system/config。
// 与 /api/v1/settings（运营设置）分离：本端点只承载系统身份配置（本期仅
// system_name），读写经 internal/syscfg 单一来源，变更写审计，即时生效。

import (
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// handleAPISystemConfigGet 返回系统身份配置当前值
// （受 apiRequireAccess 门禁：未注入 access 服务返回受控 503）。
func (s *Server) handleAPISystemConfigGet(w http.ResponseWriter, r *http.Request, _ session) {
	if !s.apiRequireAccess(w, r, "api.system.config.get") {
		return
	}
	writeAPISingle(w, sysConfigView{SystemName: syscfg.Name(r.Context(), s.st)})
}

// sysConfigView 是系统身份配置的响应 DTO。
type sysConfigView struct {
	SystemName string `json:"system_name"`
}

// handleAPISystemConfigPost 保存系统身份配置：名称经 syscfg.ValidateName
// 校验（非法回 400 受控中文文案），值未变时跳过（不写审计），变更写审计
// settings.system_name（即时生效）。
func (s *Server) handleAPISystemConfigPost(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.system.config.update"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	var in struct {
		SystemName string `json:"system_name"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	name, err := syscfg.ValidateName(in.SystemName)
	if err != nil {
		s.apiBadRequest(w, r, op, err.Error())
		return
	}
	before := syscfg.Name(r.Context(), s.st)
	if name != before {
		if err := syscfg.SetName(r.Context(), s.st, name); err != nil {
			s.writeAPIAppErr(w, r, op, err)
			return
		}
		s.audit(r.Context(), "settings.system_name", "settings", map[string]any{
			"before": before, "after": name, "effect": "即时生效"})
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		sysConfigView
	}{apiWriteOK{OK: true}, sysConfigView{SystemName: syscfg.Name(r.Context(), s.st)}})
}
