// Package web 提供管理端 HTTP 服务：访问密钥 + GitHub OAuth 双通道登录、
// 会话与 CSRF 防护、登录限流、审计与 healthz/readyz 探针；SPA 管理端
// （默认入口）经 /admin 提供，页面数据与全部业务变更走 /api/v1
// JSON API。CSV 导出与 MTProto 二维码仍由本包直接服务（SPA 同源引用）。
//
// 边界约定（§6.1）：
//   - 服务在独立 goroutine 运行，panic 经恢复中间件兜底，故障不影响 Bot 主链路；
//   - 访问密钥明文只在生成/重置时输出一次（stderr），库内只存 SHA-256 哈希；
//   - 会话 ID 只存哈希（web_sessions.id_hash），Cookie 带
//     Secure/HttpOnly/SameSite=Lax，12 小时滑动过期，多设备并行；
//   - CSRF 约定：/api/v1 变更请求校验会话级 token（X-CSRF-Token 头），
//     登录请求校验双提交 Cookie（spore_login_csrf 与 X-CSRF-Token 头
//     常数时间比对，当前传输通道为 JSON），OAuth 流程以随机单次 state 防 CSRF；
//   - 旧 SSR 认证页、登录页与 OAuth 结果页已全部删除：Go 侧不再
//     渲染任何 HTML，唯一 HTML 输出是 SPA 应用壳（/admin 受会话保护，
//     /admin/login 公开登录壳，共用每请求 CSP nonce）；旧页面入口不再注册；OAuth
//     回调结果改为 302 到 /admin/settings/oauth?oauth=<bound|error> 受控横幅；
//   - MTProto 状态/扫码/重连接口只依赖登录会话快照（mtproto.Session），
//     本包不接触任何 gotd 类型；扫码 URL 属敏感值，不入日志与审计，
//     二维码由服务端渲染（rsc.io/qr）经登录态接口交付；
//   - 备份导出用 SQLite VACUUM INTO 生成一致快照，仅业务数据库，
//     不含 session.json / peers.json；
//   - 审计来源 IP 仅在 WEB_TRUSTED_PROXY 开启时取 X-Forwarded-For 末段
//     （可信代理追加的真实对端，防客户端 prepend 伪造），否则直接使用 RemoteAddr；
//   - /api/v1 JSON API（api.go 与 api_ 前缀文件）：认证与错误一律 JSON——
//     未认证 401、CSRF 失败 403，绝不重定向到 HTML；复用同一
//     spore_session 会话与会话级 CSRF token（变更请求经 X-CSRF-Token 头），
//     错误信封固定为 {"error":{"code","message"}}，错误码为大写下划线；
//     SSR view struct 不作为 API DTO；只读列表查询 API（api_users/
//     api_requests/api_channels/api_events/api_overview）的列表响应统一为
//     分页信封 {"items","page","page_size","total","total_pages"}。
package web
