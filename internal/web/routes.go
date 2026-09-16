package web

import (
	"net/http"

	"github.com/huaiminyetnotsleep/spore/internal/store"
)

// registerRoutes 按公开面、管理 SPA、API 和保留功能端点四个边界注册管理端路由。
// 各组保持原 Handler 中的 pattern 与 middleware，不在拆分时改变服务契约。
func (s *Server) registerRoutes(mux *http.ServeMux) {
	s.registerPublicRoutes(mux)
	s.registerAdminRoutes(mux)
	s.registerAPIRoutes(mux)
	s.registerFunctionalRoutes(mux)
	// 普通路径的最低优先级兜底；API、管理端、资源和功能端点由更具体的路由优先匹配。
	mux.HandleFunc("/", s.handleNotFound)
}

func (s *Server) registerPublicRoutes(mux *http.ServeMux) {
	// 公开路由（无鉴权）：探针不含任何敏感信息
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	// 公开路由：登录 API、OAuth 回调与静态资源（前端资源不含业务数据）。
	mux.HandleFunc("GET /api/v1/login/csrf", s.handleAPILoginCSRF)
	mux.HandleFunc("POST /api/v1/login", s.handleAPILogin)
	mux.HandleFunc("GET /api/v1/login", http.HandlerFunc(s.writeAPIMethodNotAllowed))
	mux.HandleFunc("POST /api/v1/login/csrf", http.HandlerFunc(s.writeAPIMethodNotAllowed))
	mux.HandleFunc("GET /auth/github", s.handleOAuthStart)
	mux.HandleFunc("GET /auth/github/callback", s.handleOAuthCallback)
	mux.Handle("GET /assets/", handleFrontendAssets())
	mux.Handle("GET /favicon.svg", handleFrontendRootAsset("favicon.svg"))
	mux.Handle("GET /icon.svg", handleFrontendRootAsset("icon.svg"))
}

// registerAdminRoutes 注册需要会话认证的 SPA 管理端入口。
func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	// /admin/login 是免认证的公开登录壳（ServeMux 精确路径优先于通配）。
	// 已删除旧 SSR 认证页与全部表单路由：管理端唯一入口是 /admin，
	// 全部业务变更经 /api/v1（认证 + CSRF + JSON 契约）。
	mux.Handle("GET /admin", s.requireAuth(s.handleAdminApp))
	mux.HandleFunc("GET /admin/login", s.handleAdminLoginApp)
	mux.Handle("GET /admin/{path...}", s.requireAuth(s.handleAdminApp))
}

// registerAPIRoutes 注册 SPA API 路由。
func (s *Server) registerAPIRoutes(mux *http.ServeMux) {
	// ---- SPA API（/api/v1；认证与错误均为 JSON，约定见 api.go）----
	// 通配兜底未匹配的 API 路由/方法（精确路由按 ServeMux 规则优先），
	// 避免 fetch 收到纯文本 404；页面资源 API 随页面相关功能补充。
	mux.HandleFunc("/api/v1", s.handleAPINotFound)
	mux.HandleFunc("/api/v1/{path...}", s.handleAPINotFound)
	mux.Handle("GET /api/v1/session", s.apiAuth(s.handleAPISession))
	s.mountAPIWrite(mux, "/api/v1/session/logout", s.handleAPISessionLogout)
	mux.Handle("GET /api/v1/overview", s.apiAuth(s.handleAPIOverview))
	// 检查更新：当前版本与上游最新发布比较（总览页服务版本旁的刷新按钮）
	mux.Handle("GET /api/v1/version/check", s.apiAuth(s.handleAPIVersionCheck))
	// 业务统计：总览拆页后承接原
	// /api/v1/overview 的时间范围请求指标与图表数据。
	mux.Handle("GET /api/v1/stats", s.apiAuth(s.handleAPIStats))
	mux.Handle("GET /api/v1/system-metrics", s.apiAuth(s.handleAPISystemMetrics))

	// 只读页面迁移：列表/详情查询端点，
	// 筛选与分页语义对齐对应 SSR 页面。
	mux.Handle("GET /api/v1/users", s.apiAuth(s.handleAPIUsersList))
	mux.Handle("GET /api/v1/users/{id}", s.apiAuth(s.handleAPIUserDetail))
	mux.Handle("GET /api/v1/requests", s.apiAuth(s.handleAPIRequestsList))
	mux.Handle("GET /api/v1/requests/{id}", s.apiAuth(s.handleAPIRequestDetail))
	mux.Handle("GET /api/v1/channels", s.apiAuth(s.handleAPIChannelsList))
	mux.Handle("GET /api/v1/channels/{key}", s.apiAuth(s.handleAPIChannelDetail))
	mux.Handle("GET /api/v1/events", s.apiAuth(s.handleAPIEventsList))
	// 频道绑定管理（internal/binding；Bot /bind 与 Web 共用同一服务）：
	// 列表为只读查询；绑定写端点与列表同路径不同方法（不能经 mountAPIWrite
	// 挂载——它会注册同路径 GET 405 兜底，与列表端点冲突），POST 直接
	// 组合认证 + CSRF 中间件。
	mux.Handle("GET /api/v1/channel-bindings", s.apiAuth(s.handleAPIChannelBindingsList))
	mux.Handle("POST /api/v1/channel-bindings", s.apiAuth(s.apiCSRF(s.handleAPIChannelBindingAdd)))
	s.mountAPIWrite(mux, "/api/v1/channel-bindings/{id}/delete", s.handleAPIChannelBindingDelete)
	// 频道加入管理（internal/joinmgr；Bot /join 与 Web 共用同一服务）：
	// 申请列表只读；审批与批量退出为写端点；已加入频道列表读取前执行
	// 总开关熔断（Enforce）。
	mux.Handle("GET /api/v1/channel-join/requests", s.apiAuth(s.handleAPIJoinRequestsList))
	s.mountAPIWrite(mux, "/api/v1/channel-join/requests/delete", s.handleAPIJoinRequestsDelete)
	s.mountAPIWrite(mux, "/api/v1/channel-join/requests/{id}/approve", s.handleAPIJoinRequestReview(true))
	s.mountAPIWrite(mux, "/api/v1/channel-join/requests/{id}/reject", s.handleAPIJoinRequestReview(false))
	mux.Handle("GET /api/v1/channel-join/channels", s.apiAuth(s.handleAPIJoinedChannelsList))
	s.mountAPIWrite(mux, "/api/v1/channel-join/channels/leave", s.handleAPIJoinedChannelsLeave)
	// 审计页迁移：只读查询端点，分页与时间
	// 倒序语义对齐 SSR /audit；before/after 原始 JSON 原样下发。
	mux.Handle("GET /api/v1/audit", s.apiAuth(s.handleAPIAuditList))
	s.mountAPIWrite(mux, "/api/v1/audit/delete", s.handleAPIAuditDelete)
	s.mountAPIWrite(mux, "/api/v1/audit/clear", s.handleAPIAuditClear)
	// 管理操作页面迁移：写端点统一走
	// 认证 + CSRF + JSON 契约，业务规则与 SSR 表单共用同一核心；
	// 申请列表与设置读取为配套查询端点。
	mux.Handle("GET /api/v1/applications", s.apiAuth(s.handleAPIApplicationsList))
	s.mountAPIWrite(mux, "/api/v1/applications/{id}/approve", s.handleAPIApplicationReview(true))
	s.mountAPIWrite(mux, "/api/v1/applications/{id}/reject", s.handleAPIApplicationReview(false))
	mux.Handle("POST /api/v1/users", s.apiAuth(s.apiCSRF(s.handleAPIUserAdd)))
	s.mountAPIWrite(mux, "/api/v1/users/{id}/refresh-profile", s.handleAPIUserRefreshProfile)
	s.mountAPIWrite(mux, "/api/v1/users/{id}/limits", s.handleAPIUserLimits)
	s.mountAPIWrite(mux, "/api/v1/users/{id}/enable", s.handleAPIUserStatusAction(store.UserEnabled))
	s.mountAPIWrite(mux, "/api/v1/users/{id}/disable", s.handleAPIUserStatusAction(store.UserDisabled))
	s.mountAPIWrite(mux, "/api/v1/users/{id}/archive", s.handleAPIUserStatusAction(store.UserArchived))
	s.mountAPIWrite(mux, "/api/v1/users/{id}/restore", s.handleAPIUserStatusAction(store.UserEnabled))
	s.mountAPIWrite(mux, "/api/v1/users/{id}/reset-quota", s.handleAPIUserResetQuota)
	s.mountAPIWrite(mux, "/api/v1/users/{id}/set-owner", s.handleAPIUserSetOwner)
	s.mountAPIWrite(mux, "/api/v1/users/{id}/cloud-download", s.handleAPIUserSetCloudDownload)
	s.mountAPIWrite(mux, "/api/v1/requests/{id}/retry", s.handleAPIRequestRetry)
	s.mountAPIWrite(mux, "/api/v1/requests/{id}/cancel", s.handleAPIRequestCancel)
	s.mountAPIWrite(mux, "/api/v1/requests/cancel", s.handleAPIRequestsCancel)
	s.mountAPIWrite(mux, "/api/v1/requests/delete", s.handleAPIRequestsDelete)
	// 缓存补写（转存缓存频道）：单条 + 批量；请求级资格与特权入队在
	// access.DumpBackfill，全局配置判定在 handler（见 api_write_dump_backfill.go）
	s.mountAPIWrite(mux, "/api/v1/requests/{id}/dump-backfill", s.handleAPIRequestDumpBackfill)
	s.mountAPIWrite(mux, "/api/v1/requests/dump-backfill-batch", s.handleAPIRequestsDumpBackfillBatch)
	// 记录清理（09-02 admin-delete-actions）：单条删除仅终态；频道删除即
	// 删除该频道全部请求记录（频道为 requests 纯聚合，无独立频道表）。
	s.mountAPIWrite(mux, "/api/v1/requests/{id}/delete", s.handleAPIRequestDelete)
	s.mountAPIWrite(mux, "/api/v1/channels/{key}/delete", s.handleAPIChannelDelete)
	s.mountAPIWrite(mux, "/api/v1/events/{id}/resolve", s.handleAPIEventResolve)
	mux.Handle("GET /api/v1/settings", s.apiAuth(s.handleAPISettingsGet))
	mux.Handle("POST /api/v1/settings", s.apiAuth(s.apiCSRF(s.handleAPISettingsPost)))
	// 系统设置（系统身份，与运营设置分离）：同路径 POST 不能经 mountAPIWrite
	// 挂载（会注册同路径 GET 405 兜底，与 GET 端点冲突），直接组合中间件。
	mux.Handle("GET /api/v1/system/config", s.apiAuth(s.handleAPISystemConfigGet))
	mux.Handle("POST /api/v1/system/config", s.apiAuth(s.apiCSRF(s.handleAPISystemConfigPost)))
	// 高风险页面迁移：OAuth、备份、受控
	// 重启与 MTProto 状态/重连；业务规则与 SSR 表单共用同一核心，
	// 敏感值（Secret、扫码 URL）不下发、不落日志。
	mux.Handle("GET /api/v1/oauth/settings", s.apiAuth(s.handleAPIOAuthSettingsGet))
	mux.Handle("POST /api/v1/oauth/settings", s.apiAuth(s.apiCSRF(s.handleAPIOAuthSettingsPost)))
	s.mountAPIWrite(mux, "/api/v1/oauth/bind", s.handleAPIOAuthBind)
	s.mountAPIWrite(mux, "/api/v1/oauth/unbind", s.handleAPIOAuthUnbind)
	mux.Handle("GET /api/v1/backup", s.apiAuth(s.handleAPIBackupGet))
	s.mountAPIWrite(mux, "/api/v1/backup/export", s.handleAPIBackupExport)
	s.mountAPIWrite(mux, "/api/v1/backup/import", s.handleAPIBackupImportUpload)
	s.mountAPIWrite(mux, "/api/v1/backup/import/confirm", s.handleAPIBackupImportConfirm)
	s.mountAPIWrite(mux, "/api/v1/restart", s.handleAPIRestart)
	mux.Handle("GET /api/v1/mtproto/status", s.apiAuth(s.handleAPIMTProtoStatus))
	s.mountAPIWrite(mux, "/api/v1/mtproto/relogin", s.handleAPIMTProtoRelogin)
	// 机器人管理（多机器人池）：列表合并 env ∪ bots.json；增删走文件配置
	//（重启生效），token 只进不出。
	mux.Handle("GET /api/v1/bots", s.apiAuth(s.handleAPIBotsGet))
	s.mountAPIWrite(mux, "/api/v1/bots/add", s.handleAPIBotsAdd)
	s.mountAPIWrite(mux, "/api/v1/bots/{id}/delete", s.handleAPIBotsDelete)
	// 暂停/恢复：settings 持久化（重启保持）+ 运行时控制即时生效
	s.mountAPIWrite(mux, "/api/v1/bots/{id}/pause", s.handleAPIBotRuntime(true))
	s.mountAPIWrite(mux, "/api/v1/bots/{id}/resume", s.handleAPIBotRuntime(false))
	// 云盘下载：配置视图/保存/连通性测试
	// 与请求补存（单条/批量）。保存是 PUT（全量替换语义，无 POST 变体），不能
	// 经 mountAPIWrite 挂载，直接组合认证 + CSRF 中间件（与 channel-bindings
	// 写端点同款）；其余 POST 端点按现有写端点模式挂载。
	mux.Handle("GET /api/v1/cloud-drive", s.apiAuth(s.handleAPICloudDriveGet))
	mux.Handle("PUT /api/v1/cloud-drive", s.apiAuth(s.apiCSRF(s.handleAPICloudDrivePut)))
	s.mountAPIWrite(mux, "/api/v1/cloud-drive/test", s.handleAPICloudDriveTest)
	mux.Handle("GET /api/v1/cloud-drive/backup/status", s.apiAuth(s.handleAPICloudBackupStatus))
	s.mountAPIWrite(mux, "/api/v1/cloud-drive/backup/export", s.handleAPICloudBackupExport)
	s.mountAPIWrite(mux, "/api/v1/cloud-drive/backup/import", s.handleAPICloudBackupImport)
	s.mountAPIWrite(mux, "/api/v1/cloud-drive/backup/import/confirm", s.handleAPICloudBackupConfirm)
	mux.Handle("DELETE /api/v1/cloud-drive/backup/pending", s.apiAuth(s.apiCSRF(s.handleAPICloudBackupPendingDelete)))
	s.mountAPIWrite(mux, "/api/v1/cloud-drive/backup/rollback", s.handleAPICloudBackupRollback)
	s.mountAPIWrite(mux, "/api/v1/requests/{id}/cloud-archive", s.handleAPIRequestCloudArchive)
	s.mountAPIWrite(mux, "/api/v1/requests/cloud-archive-batch", s.handleAPIRequestsCloudArchiveBatch)
}

// registerFunctionalRoutes 注册 SPA 仍直接引用的非页面端点。
func (s *Server) registerFunctionalRoutes(mux *http.ServeMux) {
	mux.Handle("GET /users/export.csv", s.requireAuth(s.handleUsersCSV))
	mux.Handle("GET /requests/export.csv", s.requireAuth(s.handleRequestsCSV))
	mux.Handle("GET /channels/export.csv", s.requireAuth(s.handleChannelsCSV))
	mux.Handle("GET /mtproto/qr.png", s.requireAuth(s.handleMTProtoQR))
}
