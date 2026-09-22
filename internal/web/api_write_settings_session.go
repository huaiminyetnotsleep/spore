package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/huaiminyetnotsleep/spore/internal/syscfg"
)

// ---- 运营设置 ----

// apiSettingsView 是 GET /api/v1/settings 的响应 DTO：仅覆盖可编辑项与
// 生效状态；静态配置（密钥状态等）与重启操作仍属旧版/后续任务范围。
type apiSettingsView struct {
	Timezone                      string `json:"timezone"`
	DedupWindowMin                int    `json:"dedup_window_min"`
	MaxLinksPerMessage            int    `json:"max_links_per_message"`
	QueueCapacity                 int    `json:"queue_capacity"`
	QueueRuntime                  int    `json:"queue_runtime"` // 0 表示未接入队列指标
	QueueSame                     bool   `json:"queue_same"`
	WorkerCount                   int    `json:"worker_count"`         // 配置值（DB 覆盖或环境默认；重启生效）
	WorkerCountRuntime            int    `json:"worker_count_runtime"` // 当前进程值
	WorkerCountSame               bool   `json:"worker_count_same"`
	MaxFileSizeBytes              int64  `json:"max_file_size_bytes"` // 配置值（重启生效）
	StreamLimitBytes              int64  `json:"stream_limit_bytes"`
	MaxFileSizeRuntime            int64  `json:"max_file_size_runtime_bytes"` // 当前进程值
	StreamLimitRuntime            int64  `json:"stream_limit_runtime_bytes"`
	TempDirMaxSizeBytes           int64  `json:"temp_dir_max_size_bytes"`         // 配置值（重启生效）
	TempDirMaxSizeRuntime         int64  `json:"temp_dir_max_size_runtime_bytes"` // 当前进程值
	MemoryBudgetBytes             int64  `json:"memory_budget_bytes"`             // 内存管道进程级预算（即时生效）
	MediaSame                     bool   `json:"media_same"`
	ChannelCopyEnabled            bool   `json:"channel_copy_enabled"`     // 频道副本同步总开关（即时生效）
	TGReuseEnabled                bool   `json:"tg_reuse_enabled"`         // TG 链接复用总开关（即时生效；默认开）
	DumpChannelID                 int64  `json:"dump_channel_id"`          // 缓存频道数字 ID（0 = 未配置）
	DumpChannelTitle              string `json:"dump_channel_title"`       // 缓存频道标题（展示用）
	JoinEnabled                   bool   `json:"join_enabled"`             // 频道加入总开关（即时生效；默认关）
	JoinAutoLeaveExternal         bool   `json:"join_auto_leave_external"` // 自动退出外部拉入频道（惰性检测；默认关）
	JoinRequireApproval           bool   `json:"join_require_approval"`
	JoinMaxChannels               int    `json:"join_max_channels"` // 0 = 不限
	JoinMuteEnabled               bool   `json:"join_mute_enabled"`
	JoinArchiveEnabled            bool   `json:"join_archive_enabled"`
	WatchApplyEnabled             bool   `json:"watch_apply_enabled"`    // 监听源用户自助申请开关（即时生效；默认关）
	WatchRequireApproval          bool   `json:"watch_require_approval"` // 用户申请需审批（false=免审批直接生效）
	WatchMaxSources               int    `json:"watch_max_sources"`      // 监听源总数上限（0=不限；仅约束用户申请）
	WatchPerUserLimit             int    `json:"watch_per_user_limit"`   // 每用户申请上限（0=不限）
	MaxRequestAttempts            int    `json:"max_request_attempts"`   // 单个请求累计尝试上限（含首次；即时生效）
	BackupIntervalHours           int    `json:"backup_interval_hours"`  // 自动备份间隔小时（0=关闭；缺省 6；即时生效）
	BackupKeepCount               int    `json:"backup_keep_count"`      // 自动备份保留份数（缺省 8 ≈ 48 小时窗口）
	DownloadThreads               int    `json:"download_threads"`
	DownloadThreadsEnv            int    `json:"download_threads_env"`
	DownloadThreadsOverridden     bool   `json:"download_threads_overridden"`
	UploadThreads                 int    `json:"upload_threads"`
	UploadThreadsEnv              int    `json:"upload_threads_env"`
	UploadThreadsOverridden       bool   `json:"upload_threads_overridden"`
	DownloadConnections           int    `json:"download_connections"`
	DownloadConnectionsEnv        int    `json:"download_connections_env"`
	DownloadConnectionsOverridden bool   `json:"download_connections_overridden"`
	UploadConnections             int    `json:"upload_connections"`
	UploadConnectionsEnv          int    `json:"upload_connections_env"`
	UploadConnectionsOverridden   bool   `json:"upload_connections_overridden"`
	LastBackupAt                  int64  `json:"last_backup_at"` // 0 表示从未备份
}

// buildAPISettingsView 组装设置读取响应。
func (s *Server) buildAPISettingsView(ctx context.Context) apiSettingsView {
	var view apiSettingsView
	if s.queue != nil {
		view.QueueRuntime = s.queue.Cap()
	}
	if s.access != nil {
		view.Timezone = s.access.TimezoneName(ctx)
		view.DedupWindowMin = s.access.DedupWindowMinutes(ctx)
	}
	view.MaxLinksPerMessage = LoadMaxLinksPerMessage(ctx, s.st, s.cfg.MaxLinksPerMessage)
	view.QueueCapacity = s.loadQueueCapacity(ctx)
	view.QueueSame = view.QueueRuntime == view.QueueCapacity
	// worker 数：配置值 = DB 覆盖（缺失/非法回退进程值）；进程值来自启动时
	// main 用 LoadWorkerCount 覆盖后的 cfg，两者不一致即有待重启变更。
	view.WorkerCount = LoadWorkerCount(ctx, s.st, s.cfg.WorkerCount)
	view.WorkerCountRuntime = s.cfg.WorkerCount
	view.WorkerCountSame = view.WorkerCount == view.WorkerCountRuntime
	view.MaxFileSizeBytes, view.StreamLimitBytes, view.TempDirMaxSizeBytes = s.loadMediaSettings(ctx)
	view.MaxFileSizeRuntime = s.cfg.MaxFileSize
	view.StreamLimitRuntime = s.cfg.StreamLimit
	view.TempDirMaxSizeRuntime = s.cfg.TempDirMaxSize
	if view.MaxFileSizeRuntime == 0 {
		view.MaxFileSizeRuntime = int64(50) << 20
	}
	if view.StreamLimitRuntime == 0 {
		view.StreamLimitRuntime = int64(20) << 20
	}
	if view.TempDirMaxSizeRuntime == 0 {
		view.TempDirMaxSizeRuntime = int64(5) << 30
	}
	view.MediaSame = view.MaxFileSizeBytes == s.cfg.MaxFileSize &&
		view.StreamLimitBytes == s.cfg.StreamLimit &&
		view.TempDirMaxSizeBytes == s.cfg.TempDirMaxSize
	view.MemoryBudgetBytes = LoadMemoryBudget(ctx, s.st, s.cfg.MemoryBudget)
	view.ChannelCopyEnabled = LoadChannelCopyEnabled(ctx, s.st)
	view.TGReuseEnabled = LoadTGReuseEnabled(ctx, s.st)
	view.DumpChannelID = LoadEffectiveDumpChannelID(ctx, s.st, s.cfg.DumpChannelID)
	view.DumpChannelTitle = LoadDumpChannelTitle(ctx, s.st)
	joinCfg := syscfg.LoadJoinConfig(ctx, s.st)
	view.JoinEnabled = joinCfg.Enabled
	view.JoinAutoLeaveExternal = joinCfg.AutoLeaveExternal
	view.JoinRequireApproval = joinCfg.RequireApproval
	view.JoinMaxChannels = joinCfg.MaxChannels
	view.JoinMuteEnabled = joinCfg.MuteEnabled
	view.JoinArchiveEnabled = joinCfg.ArchiveEnabled
	watchCfg := syscfg.LoadWatchConfig(ctx, s.st)
	view.WatchApplyEnabled = watchCfg.ApplyEnabled
	view.WatchRequireApproval = watchCfg.RequireApproval
	view.WatchMaxSources = watchCfg.MaxSources
	view.WatchPerUserLimit = watchCfg.PerUserLimit
	view.MaxRequestAttempts = syscfg.LoadMaxRequestAttempts(ctx, s.st)
	view.BackupIntervalHours = syscfg.LoadBackupIntervalHours(ctx, s.st)
	view.BackupKeepCount = syscfg.LoadBackupKeepCount(ctx, s.st)
	if s.transfer != nil {
		transfer := s.transfer.View()
		view.DownloadThreads = transfer.DownloadThreads.Effective
		view.DownloadThreadsEnv = transfer.DownloadThreads.EnvDefault
		view.DownloadThreadsOverridden = transfer.DownloadThreads.Overridden
		view.UploadThreads = transfer.UploadThreads.Effective
		view.UploadThreadsEnv = transfer.UploadThreads.EnvDefault
		view.UploadThreadsOverridden = transfer.UploadThreads.Overridden
		view.DownloadConnections = transfer.DownloadConnections.Effective
		view.DownloadConnectionsEnv = transfer.DownloadConnections.EnvDefault
		view.DownloadConnectionsOverridden = transfer.DownloadConnections.Overridden
		view.UploadConnections = transfer.UploadConnections.Effective
		view.UploadConnectionsEnv = transfer.UploadConnections.EnvDefault
		view.UploadConnectionsOverridden = transfer.UploadConnections.Overridden
	}
	view.LastBackupAt = s.lastBackupAt(ctx)
	return view
}

// handleAPISettingsGet 返回可编辑运营设置的当前值与生效状态
// （受 apiRequireAccess 门禁：未注入 access 服务返回受控 503）。
func (s *Server) handleAPISettingsGet(w http.ResponseWriter, r *http.Request, _ session) {
	if !s.apiRequireAccess(w, r, "api.settings.get") {
		return
	}
	writeAPISingle(w, s.buildAPISettingsView(r.Context()))
}

// handleAPISettingsPost 保存运营设置：载荷映射到 settingsUpdateInput 后经
// applySettingsUpdate 校验、写库并留审计
// （时区/去重窗口即时生效，队列容量、worker 数与媒体参数重启生效）。
func (s *Server) handleAPISettingsPost(w http.ResponseWriter, r *http.Request, _ session) {
	const op = "api.settings.update"
	if !s.apiRequireAccess(w, r, op) {
		return
	}
	var in struct {
		Timezone               string   `json:"timezone"`
		DedupWindowMin         *int     `json:"dedup_window_min"`
		MaxLinksPerMessage     *int     `json:"max_links_per_message"`
		QueueCapacity          *int     `json:"queue_capacity"`
		WorkerCount            *int     `json:"worker_count"`
		MaxFileSize            string   `json:"max_file_size"`
		MaxFileSizeUnit        string   `json:"max_file_unit"`
		StreamLimit            string   `json:"stream_limit"`
		StreamLimitUnit        string   `json:"stream_limit_unit"`
		TempDirMaxSize         string   `json:"temp_dir_max_size"`
		TempDirMaxSizeUnit     string   `json:"temp_dir_max_size_unit"`
		MemoryBudget           string   `json:"memory_budget"`
		MemoryBudgetUnit       string   `json:"memory_budget_unit"`
		ChannelCopyEnabled     *bool    `json:"channel_copy_enabled"`
		TGReuseEnabled         *bool    `json:"tg_reuse_enabled"`
		DumpChannel            *string  `json:"dump_channel"`
		JoinEnabled            *bool    `json:"join_enabled"`
		JoinAutoLeaveExternal  *bool    `json:"join_auto_leave_external"`
		JoinRequireApproval    *bool    `json:"join_require_approval"`
		JoinMaxChannels        *int     `json:"join_max_channels"`
		JoinMuteEnabled        *bool    `json:"join_mute_enabled"`
		JoinArchiveEnabled     *bool    `json:"join_archive_enabled"`
		WatchApplyEnabled      *bool    `json:"watch_apply_enabled"`
		WatchRequireApproval   *bool    `json:"watch_require_approval"`
		WatchMaxSources        *int     `json:"watch_max_sources"`
		WatchPerUserLimit      *int     `json:"watch_per_user_limit"`
		MaxRequestAttempts     *int     `json:"max_request_attempts"`
		BackupIntervalHours    *int     `json:"backup_interval_hours"`
		BackupKeepCount        *int     `json:"backup_keep_count"`
		DownloadThreads        *int     `json:"download_threads"`
		UploadThreads          *int     `json:"upload_threads"`
		DownloadConnections    *int     `json:"download_connections"`
		UploadConnections      *int     `json:"upload_connections"`
		ClearTransferOverrides []string `json:"clear_transfer_overrides"`
	}
	if !s.apiReadJSON(w, r, op, &in) {
		return
	}
	payload := settingsUpdateInput{
		Timezone:               in.Timezone,
		MaxLinksPerMessage:     in.MaxLinksPerMessage,
		WorkerCount:            in.WorkerCount,
		MaxFileSizeRaw:         in.MaxFileSize,
		MaxFileSizeUnit:        in.MaxFileSizeUnit,
		StreamLimitRaw:         in.StreamLimit,
		StreamLimitUnit:        in.StreamLimitUnit,
		TempDirMaxSizeRaw:      in.TempDirMaxSize,
		TempDirMaxSizeUnit:     in.TempDirMaxSizeUnit,
		MemoryBudgetRaw:        in.MemoryBudget,
		MemoryBudgetUnit:       in.MemoryBudgetUnit,
		ChannelCopyEnabled:     in.ChannelCopyEnabled,
		TGReuseEnabled:         in.TGReuseEnabled,
		DumpChannel:            in.DumpChannel,
		JoinEnabled:            in.JoinEnabled,
		JoinAutoLeaveExternal:  in.JoinAutoLeaveExternal,
		JoinRequireApproval:    in.JoinRequireApproval,
		JoinMaxChannels:        in.JoinMaxChannels,
		JoinMuteEnabled:        in.JoinMuteEnabled,
		JoinArchiveEnabled:     in.JoinArchiveEnabled,
		WatchApplyEnabled:      in.WatchApplyEnabled,
		WatchRequireApproval:   in.WatchRequireApproval,
		WatchMaxSources:        in.WatchMaxSources,
		WatchPerUserLimit:      in.WatchPerUserLimit,
		MaxRequestAttempts:     in.MaxRequestAttempts,
		BackupIntervalHours:    in.BackupIntervalHours,
		BackupKeepCount:        in.BackupKeepCount,
		DownloadThreads:        in.DownloadThreads,
		UploadThreads:          in.UploadThreads,
		DownloadConnections:    in.DownloadConnections,
		UploadConnections:      in.UploadConnections,
		ClearTransferOverrides: in.ClearTransferOverrides,
	}
	if in.DedupWindowMin != nil {
		payload.DedupWindowRaw = strconv.Itoa(*in.DedupWindowMin)
	}
	if in.QueueCapacity != nil {
		payload.QueueCapRaw = strconv.Itoa(*in.QueueCapacity)
	}
	if _, err := s.applySettingsUpdate(r.Context(), payload); err != nil {
		var paramErr *settingsParamError
		if errors.As(err, &paramErr) {
			s.apiBadRequest(w, r, op, paramErr.msg)
			return
		}
		s.writeAPIAppErr(w, r, op, err)
		return
	}
	writeAPIJSON(w, http.StatusOK, struct {
		apiWriteOK
		Settings apiSettingsView `json:"settings"`
	}{apiWriteOK{OK: true}, s.buildAPISettingsView(r.Context())})
}

// ---- 会话 ----

// handleAPISessionLogout 登出（核心经 logoutSession：删除当前会话、清除
// Cookie 并写审计），成功后前端跳转 SPA 登录页（/admin/login）。
func (s *Server) handleAPISessionLogout(w http.ResponseWriter, r *http.Request, sess session) {
	s.logoutSession(w, r, sess)
	writeAPIJSON(w, http.StatusOK, apiWriteOK{OK: true})
}
