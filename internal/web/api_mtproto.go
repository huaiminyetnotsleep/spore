package web

// 高风险页面迁移：MTProto 扫码登录状态与
// 重连的 SPA API（/api/v1）。
//
// 敏感边界：扫码 URL（tg://login?token=...）是敏感值，本 API 不下发——
// 前端经既有同源 GET /mtproto/qr.png 图片端点展示二维码（img 引用），
// 状态响应只带 qr_available 布尔与 updated_at（用作图片缓存参数）；
// qr_url 不进入 API 响应、日志与审计，状态响应保持 no-store。

import (
	"errors"
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/apperr"
	"github.com/huaiminyetnotsleep/spore/internal/mtproto"
)

// handleAPIMTProtoStatus 返回登录会话状态（apiAuth JSON 401）。
// state / updated_at / last_error 与既有状态语义一致；
// 差异仅是不下发 qr_url 本身，改以 qr_available 布尔表达"有待扫码的二维码"。
// 响应 no-store，避免代理/浏览器缓存登录状态。
func (s *Server) handleAPIMTProtoStatus(w http.ResponseWriter, r *http.Request, _ session) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if s.mtp == nil {
		// 未接入：只给 state（unknown 即"未接入"）
		writeAPIJSON(w, http.StatusOK, map[string]any{"state": "unknown"})
		return
	}
	snap := s.mtp.Status()
	resp := map[string]any{
		"state":        snap.State,
		"updated_at":   snap.UpdatedAt,
		"qr_available": snap.QRURL != "",
	}
	if snap.LastError != "" {
		resp["last_error"] = snap.LastError
	}
	if s.botMTP != nil {
		bot := s.botMTP.Status()
		resp["bot_state"] = bot.State
		resp["bot_updated_at"] = bot.UpdatedAt
		if bot.DCID > 0 {
			resp["bot_dc_id"] = bot.DCID
		}
	}
	// 多机器人池：逐 bot 的大文件直传会话状态（装配顺序，主 bot 在前）；
	// 顶层 bot_* 字段保留为主 bot 状态（前端兼容）。
	if s.botMTPs != nil {
		if entries := s.botMTPs.BotMTProtoEntries(); len(entries) > 0 {
			rows := make([]apiBotMTProtoRow, 0, len(entries))
			for _, e := range entries {
				rows = append(rows, apiBotMTProtoRow{
					BotID:     e.BotID,
					Username:  e.Username,
					State:     e.Snapshot.State,
					DCID:      e.Snapshot.DCID,
					UpdatedAt: e.Snapshot.UpdatedAt,
				})
			}
			resp["bots"] = rows
		}
	}
	writeAPIJSON(w, http.StatusOK, resp)
}

// apiBotMTProtoRow 是 MTProto 状态响应 bots 数组的单 bot 条目。
type apiBotMTProtoRow struct {
	BotID     int64  `json:"bot_id"`
	Username  string `json:"username,omitempty"`
	State     string `json:"state"`
	DCID      int    `json:"dc_id,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

// handleAPIMTProtoRelogin 触发重连（仅离线状态接受，
// 成功写审计）。确认弹层由前端负责；服务端校验会话 CSRF（挂载层）与状态。
func (s *Server) handleAPIMTProtoRelogin(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.mtproto.relogin"
	if s.mtp == nil {
		s.log.Warn("MTProto 状态未接入，拒绝重连", "op", op)
		writeAPIError(w, http.StatusServiceUnavailable, "TELEGRAM_UNAVAILABLE",
			"MTProto 状态未接入本实例，无法触发重连。")
		return
	}
	if err := s.mtp.TriggerRelogin(); err != nil {
		if errors.Is(err, mtproto.ErrNotOffline) {
			writeAPIError(w, http.StatusConflict, "CONFLICT", "会话当前不是离线状态，无需重连")
			return
		}
		s.log.Error("触发 MTProto 重连失败", "op", op, "error", err.Error())
		writeAPIError(w, http.StatusInternalServerError, string(apperr.CodeInternal),
			"触发重连失败，请稍后重试")
		return
	}
	s.audit(r.Context(), "mtproto.relogin", "mtproto", nil)
	writeAPISingle(w, struct {
		apiWriteOK
		Message string `json:"message"`
	}{apiWriteOK{OK: true}, "已触发重连，请等待新的扫码二维码。"})
}
