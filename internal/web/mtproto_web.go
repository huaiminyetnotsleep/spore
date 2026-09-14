package web

// MTProto 扫码登录的 Web 端点：服务端渲染二维码 PNG。
// 当前 SSR 登录页/状态页/重连表单已删除：SPA 经 /api/v1/mtproto/status
// 与 /api/v1/mtproto/relogin（api_mtproto.go）完成状态轮询与重连触发，
// 二维码图片仍由本端点直接提供（前端 img 引用）。
// 二维码内容（tg://login?token=...）是敏感值：不入日志、不入审计，
// 仅经登录态接口交付浏览器（本包不依赖任何 gotd 类型，状态经 mtproto.Session 快照）。

import (
	"net/http"
	"strconv"

	"rsc.io/qr"
)

// handleMTProtoQR 把当前扫码登录 URL 渲染为 PNG（登录态）。
// 服务端渲染避免了向页面引入第三方 JS 二维码库；图内容即登录令牌，
// 响应 no-store、错误只记分类不记 URL。
func (s *Server) handleMTProtoQR(w http.ResponseWriter, r *http.Request, sess session) {
	w.Header().Set("Cache-Control", "no-store")
	if s.mtp == nil {
		http.Error(w, "MTProto 状态未接入", http.StatusServiceUnavailable)
		return
	}
	snap := s.mtp.Status()
	if snap.QRURL == "" {
		http.Error(w, "当前没有待扫描的登录码", http.StatusNotFound)
		return
	}
	code, err := qr.Encode(snap.QRURL, qr.M)
	if err != nil {
		s.log.Error("生成登录二维码失败", "error", err.Error()) // 不记 URL 内容
		http.Error(w, "二维码生成失败", http.StatusInternalServerError)
		return
	}
	png := code.PNG()
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(png)))
	_, _ = w.Write(png)
}
