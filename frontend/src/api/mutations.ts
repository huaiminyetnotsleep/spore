/**
 * 管理操作写 API（POST /api/v1）：与后端写端点一一对应的类型与请求函数。
 * 请求统一携带会话 Cookie 与 X-CSRF-Token 头（token 来自 session bootstrap
 * 缓存）；响应为 {"ok":true,...} 摘要，失败抛出携带服务端受控中文文案的
 * ApiError。本模块不包含业务规则，确认与提示由页面组件负责。
 * 高风险操作（OAuth/备份/重启/MTProto）同样不落任何敏感值到本地存储。
 */
import { apiErrorFrom, apiRequest } from "./client";
import { getCSRFToken } from "./session";
import { PROJECT_IDENTITY } from "../shared/projectIdentity.generated";
import type {
  BackupScheduleView,
  BackupView,
  WatchEventsDeleteResult,
  WatchInviteRequestRow,
  WatchSourceRow,
  CloudDriveBackupCandidate,
  CloudDriveView,
  NotificationMute,
  NotificationPolicy,
  NotificationWebhookFormat,
  OAuthSettingsView,
  SettingsView,
} from "./admin";

/** 写请求公共出口：JSON 体 + CSRF 请求头。 */
async function postJSON<T>(path: string, body?: unknown): Promise<T> {
  return apiRequest<T>(path, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": getCSRFToken(),
    },
    body: body === undefined ? "{}" : JSON.stringify(body),
  });
}

/** PUT 写请求出口：JSON 体 + CSRF 请求头（与 postJSON 仅方法不同）。 */
async function putJSON<T>(path: string, body: unknown): Promise<T> {
  return apiRequest<T>(path, {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": getCSRFToken(),
    },
    body: JSON.stringify(body),
  });
}

/** DELETE 写请求出口：会话 CSRF + 可选 JSON 体。 */
async function deleteJSON<T>(path: string, body?: unknown): Promise<T> {
  return apiRequest<T>(path, {
    method: "DELETE",
    headers: {
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      "X-CSRF-Token": getCSRFToken(),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}

/** 写操作成功响应的公共字段。 */
export interface WriteOK {
  ok: boolean;
}

// ---- 申请审批 ----

export interface ApplicationReviewResult extends WriteOK {
  /** false 表示审批已生效但 Bot 通知发送失败（需管理员手动告知）。 */
  notified: boolean;
  status: string;
}

export const approveApplication = (id: number): Promise<ApplicationReviewResult> =>
  postJSON<ApplicationReviewResult>(`/api/v1/applications/${id}/approve`);

export const rejectApplication = (id: number): Promise<ApplicationReviewResult> =>
  postJSON<ApplicationReviewResult>(`/api/v1/applications/${id}/reject`);

// ---- 机器人池管理（多机器人池） ----

export interface BotMutationResult extends WriteOK {
  /** 变更后的列表快照（合并运行时身份）。 */
  bots: import("./admin").BotRow[];
  max_bots: number;
  need_apply: boolean;
  /** 受控提示：变更已保存，重启进程后生效（接管端点为恢复提示）。 */
  restart_hint: string;
  /** 接管端点的受控提示；增删端点缺省。 */
  message?: string;
}

/** 新增文件来源 bot（token 写入 bots.json，0600；重启生效）。 */
export const addBot = (token: string): Promise<BotMutationResult> =>
  postJSON<BotMutationResult>("/api/v1/bots/add", { token });

/** 移除文件来源 bot（env 来源由服务端拒绝，需改环境变量）。 */
export const deleteBot = (botId: number): Promise<BotMutationResult> =>
  postJSON<BotMutationResult>(`/api/v1/bots/${botId}/delete`);

/** 暂停 bot：停止接收新消息（在途任务正常完成），即时生效、重启保持。 */
export const pauseBot = (botId: number): Promise<BotMutationResult> =>
  postJSON<BotMutationResult>(`/api/v1/bots/${botId}/pause`);

/** 恢复 bot：重新拉起该 bot 的长轮询，即时生效。 */
export const resumeBot = (botId: number): Promise<BotMutationResult> =>
  postJSON<BotMutationResult>(`/api/v1/bots/${botId}/resume`);

// ---- 用户管理 ----

export interface AddUserInput {
  user_id: number;
  note?: string;
}

export interface AddUserResult extends WriteOK {
  user_id: number;
}

export const addUser = (input: AddUserInput): Promise<AddUserResult> =>
  postJSON<AddUserResult>("/api/v1/users", input);

/** 状态变更动作：与 SSR POST /users/{id}/<action> 一一对应。 */
export type UserStatusAction = "enable" | "disable" | "archive" | "restore";

export interface UserStatusResult extends WriteOK {
  status: string;
}

export const setUserStatus = (
  id: number,
  action: UserStatusAction,
): Promise<UserStatusResult> =>
  postJSON<UserStatusResult>(`/api/v1/users/${id}/${action}`);

/** 限额调整：缺省字段保持原值（与 SSR 留空语义一致）。 */
export interface UserLimitsInput {
  submit_interval_sec?: number;
  daily_limit?: number;
  concurrent_limit?: number;
  /** 频道绑定数量上限；0 = 跟随角色默认，缺省保持不变。 */
  bind_limit?: number;
}

export const updateUserLimits = (id: number, input: UserLimitsInput): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/users/${id}/limits`, input);

export const resetUserQuota = (id: number): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/users/${id}/reset-quota`);

export interface UserOwnerResult extends WriteOK {
  is_owner: boolean;
}

export const setUserOwner = (id: number, owner: boolean): Promise<UserOwnerResult> =>
  postJSON<UserOwnerResult>(`/api/v1/users/${id}/set-owner`, { owner });

/** 云盘下载权限三态：0=跟随角色默认 1=显式允许 2=显式拒绝（owner 可被拒绝）。 */
export type CloudDownloadMode = 0 | 1 | 2;

export interface UserCloudDownloadResult extends WriteOK {
  cloud_download: number;
  effective_cloud_download: boolean;
}

export const setUserCloudDownload = (
  id: number,
  mode: CloudDownloadMode,
): Promise<UserCloudDownloadResult> =>
  postJSON<UserCloudDownloadResult>(`/api/v1/users/${id}/cloud-download`, {
    cloud_download: mode,
  });

export interface UserAutoPinResult extends WriteOK {
  auto_pin: boolean;
}

/** 自动置顶偏好开关：开启后该用户普通任务提交即默认置顶。 */
export const setUserAutoPin = (id: number, autoPin: boolean): Promise<UserAutoPinResult> =>
  postJSON<UserAutoPinResult>(`/api/v1/users/${id}/auto-pin`, { auto_pin: autoPin });

export const refreshUserProfile = (id: number): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/users/${id}/refresh-profile`);

// ---- 消息记录 ----

export const retryRequest = (id: number): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/requests/${id}/retry`);

/** 重置单条请求的尝试计数（仅 failed；attempt 清回 1，不入队）。 */
export const resetRequestAttempts = (id: number): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/requests/${id}/reset_attempts`);

export interface CancelResult {
  id: number;
  result: "cancelled" | "conflict" | "not_found" | "failed";
  error_code?: string;
}

export interface CancelManyResult extends WriteOK {
  results: CancelResult[];
}

export interface DeleteResult {
  id: number;
  result: "deleted" | "conflict" | "not_found" | "failed";
  error_code?: string;
}

export interface DeleteManyResult extends WriteOK {
  results: DeleteResult[];
}

export const cancelRequest = (id: number): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/requests/${id}/cancel`);

export const cancelRequests = (ids: number[]): Promise<CancelManyResult> =>
  postJSON<CancelManyResult>("/api/v1/requests/cancel", { ids });

/** 删除单条请求记录（服务端仅接受终态记录）。 */
export const deleteRequest = (id: number): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/requests/${id}/delete`);

export const deleteRequests = (ids: number[]): Promise<DeleteManyResult> =>
  postJSON<DeleteManyResult>("/api/v1/requests/delete", { ids });

// ---- 频道记录清理 ----

export interface ChannelDeleteResult extends WriteOK {
  /** 实际删除的请求记录行数（频道是请求记录的纯聚合）。 */
  deleted: number;
}

/** 删除该频道的全部请求记录（有未完成请求时服务端拒绝）。 */
export const deleteChannelRequests = (key: string): Promise<ChannelDeleteResult> =>
  postJSON<ChannelDeleteResult>(`/api/v1/channels/${encodeURIComponent(key)}/delete`);

// ---- 审计日志 ----

export interface DeletedCountResult extends WriteOK {
  deleted: number;
}

export const deleteAuditEntries = (ids: number[]): Promise<DeletedCountResult> =>
  postJSON<DeletedCountResult>("/api/v1/audit/delete", { ids });

export const clearAudit = (): Promise<DeletedCountResult> =>
  postJSON<DeletedCountResult>("/api/v1/audit/clear", { confirm: "clear_audit" });

// ---- 事件中心 ----

export const resolveEvent = (id: number): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/events/${id}/resolve`);

// ---- 系统设置 ----

/** 设置保存载荷：缺省字段不变更；媒体大小沿用"数值 + 单位"原文。 */
export interface SettingsSaveInput {
  timezone?: string;
  dedup_window_min?: number;
  /** 一条 Bot 输入允许的有效链接数（1–50，即时生效）。 */
  max_links_per_message?: number;
  queue_capacity?: number;
  /** 任务并发 worker 数（1–16，重启生效）；缺省不变更。 */
  worker_count?: number;
  max_file_size?: string;
  max_file_unit?: string;
  stream_limit?: string;
  stream_limit_unit?: string;
  temp_dir_max_size?: string;
  temp_dir_max_size_unit?: string;
  /** 内存管道进程级预算（64MB–8GB，即时生效）；缺省不变更。 */
  memory_budget?: string;
  memory_budget_unit?: string;
  /** 频道副本同步总开关；缺省不变更。 */
  channel_copy_enabled?: boolean;
  /** TG 链接复用总开关（copyMessages 直拷跳过重复下载上传）；缺省不变更。 */
  tg_reuse_enabled?: boolean;
  /** 缓存频道目标（@用户名 / t.me 链接 / -100 数字 ID）；空串清除；缺省不变更。 */
  dump_channel?: string;
  /** 频道加入（/join）配置；缺省不变更（join_max_channels 0 = 不限）。 */
  join_enabled?: boolean;
  join_auto_leave_external?: boolean;
  join_require_approval?: boolean;
  join_max_channels?: number;
  join_mute_enabled?: boolean;
  join_archive_enabled?: boolean;
  /** 监听源（/watch）配置；缺省不变更（0 = 不限）。 */
  watch_apply_enabled?: boolean;
  watch_require_approval?: boolean;
  watch_max_sources?: number;
  watch_per_user_limit?: number;
  /** 单个请求累计尝试上限（1–10，含首次，即时生效）；缺省不变更。 */
  max_request_attempts?: number;
  backup_interval_hours?: number;
  backup_keep_count?: number;
  /** 错误日志保留天数（1–365）；缺省不变更。 */
  error_log_retention_days?: number;
  /** 文件分片传输配置；缺省不变更。 */
  download_threads?: number;
  upload_threads?: number;
  download_connections?: number;
  upload_connections?: number;
  /** 要删除数据库覆盖并回落 .env 的字段名。 */
  clear_transfer_overrides?: TransferConfigKey[];
}

export type TransferConfigKey =
  | "download_threads"
  | "upload_threads"
  | "download_connections"
  | "upload_connections";

export interface SettingsSaveResult extends WriteOK {
  settings: SettingsView;
}

export const saveSettings = (input: SettingsSaveInput): Promise<SettingsSaveResult> =>
  postJSON<SettingsSaveResult>("/api/v1/settings", input);

// ---- 定时备份 + R2 上云配置 ----

/** R2 连接配置载荷：密钥字段传掩码或空串 = 沿用已保存值。 */
export interface BackupR2ConfigInput {
  enabled: boolean;
  account_id?: string;
  access_key_id?: string;
  secret_access_key?: string;
  bucket?: string;
}

/** 定时备份保存载荷：缺省字段不变更（间隔/份数与设置页同键同审计）。 */
export interface BackupScheduleSaveInput {
  interval_hours?: number;
  keep_count?: number;
  r2?: BackupR2ConfigInput;
}

export interface BackupScheduleSaveResult extends WriteOK {
  backup_schedule: BackupScheduleView;
}

export const saveBackupSchedule = (
  input: BackupScheduleSaveInput,
): Promise<BackupScheduleSaveResult> =>
  postJSON<BackupScheduleSaveResult>("/api/v1/backup/r2", input);

/** R2 连通性测试结果：connected=false 时 message 为受控失败场景。 */
export interface BackupR2TestResult extends WriteOK {
  connected: boolean;
  message: string;
}

export const testBackupR2Connection = (): Promise<BackupR2TestResult> =>
  postJSON<BackupR2TestResult>("/api/v1/backup/r2/test", {});

// ---- 监听源（/watch，预热缓存频道） ----

/** 监听源写操作公共响应；邀请链接可能返回待处理申请而非直接 source。 */
export interface WatchSourceOpResult extends WriteOK {
  source?: WatchSourceRow;
  invite_request?: WatchInviteRequestRow;
}

/** 管理员添加监听源；target 也可为邀请链接，由服务端返回 source 或 invite_request。 */
export const addWatchSource = (target: string, enabled = true): Promise<WatchSourceOpResult> =>
  postJSON<WatchSourceOpResult>("/api/v1/watch-sources/add", { target, enabled });

/** 审批待审批申请（approve=false 即拒绝）。 */
export const reviewWatchSource = (
  channelId: number,
  approve: boolean,
): Promise<WatchSourceOpResult> =>
  postJSON<WatchSourceOpResult>(`/api/v1/watch-sources/${channelId}/${approve ? "approve" : "reject"}`);

/** 切换 approved 行的暂停开关。 */
export const toggleWatchSource = (
  channelId: number,
  enabled: boolean,
): Promise<WatchSourceOpResult> =>
  postJSON<WatchSourceOpResult>(`/api/v1/watch-sources/${channelId}/toggle`, { enabled });

/** 删除监听源行（任意状态）。 */
export const deleteWatchSource = (channelId: number): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/watch-sources/${channelId}/delete`);

/** 移出 Bot：池内全部 bot 退出源聊天（频道即放弃管理员）并删除监听源行。 */
export interface WatchLeaveResult extends WriteOK {
  outcome: { left: number; failed: number };
}
export const leaveWatchSource = (channelId: number): Promise<WatchLeaveResult> =>
  postJSON<WatchLeaveResult>(`/api/v1/watch-sources/${channelId}/leave`);

/** 邀请链接申请操作响应；审批成功时可能同时创建监听源。 */
export interface WatchInviteRequestOpResult extends WriteOK {
  invite_request?: WatchInviteRequestRow;
  source?: WatchSourceRow;
}

export const approveWatchInviteRequest = (id: number): Promise<WatchInviteRequestOpResult> =>
  postJSON<WatchInviteRequestOpResult>(`/api/v1/watch-invite-requests/${id}/approve`);

export const rejectWatchInviteRequest = (id: number): Promise<WatchInviteRequestOpResult> =>
  postJSON<WatchInviteRequestOpResult>(`/api/v1/watch-invite-requests/${id}/reject`);

export const retryWatchInviteRequest = (id: number): Promise<WatchInviteRequestOpResult> =>
  postJSON<WatchInviteRequestOpResult>(`/api/v1/watch-invite-requests/${id}/retry`);

export const deleteWatchInviteRequest = (id: number): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/watch-invite-requests/${id}/delete`);

/** 删除预热事件（单条/批量共用，单条传单元素数组）。 */
export const deleteWatchEvents = (ids: number[]): Promise<WatchEventsDeleteResult> =>
  postJSON<WatchEventsDeleteResult>("/api/v1/watch-events/delete", { ids });

// ---- 错误日志中心 ----

/** 错误日志删除响应（按 ID 与按时间段共用）。 */
export interface ErrorLogsDeleteResult {
  ok: boolean;
  /** 实际删除行数。 */
  deleted: number;
}

/** 按 ID 批量删除错误日志（1–100 个）。 */
export const deleteErrorLogs = (ids: number[]): Promise<ErrorLogsDeleteResult> =>
  postJSON<ErrorLogsDeleteResult>("/api/v1/error-logs/delete", { ids });

/** 按时间段删除错误日志（至少一个时间界；可叠加来源/错误码条件）。 */
export const deleteErrorLogsRange = (body: {
  after?: number;
  before?: number;
  source?: string;
  code?: string;
}): Promise<ErrorLogsDeleteResult> =>
  postJSON<ErrorLogsDeleteResult>("/api/v1/error-logs/delete", body);

// ---- 系统设置（系统身份） ----

export interface SystemConfigSaveResult extends WriteOK {
  /** 规范化（去首尾空白）后的系统名称。 */
  system_name: string;
}

export const saveSystemConfig = (systemName: string): Promise<SystemConfigSaveResult> =>
  postJSON<SystemConfigSaveResult>("/api/v1/system/config", { system_name: systemName });

// ---- 通知设置 ----

export interface NotificationWebhookInput {
  enabled: boolean;
  format: NotificationWebhookFormat;
  /** 敏感字段：空字符串表示沿用已保存值。 */
  url: string;
  /** generic/feishu/dingtalk 可用；空字符串表示沿用已保存值。 */
  secret: string;
  generic_signature_header?: string;
  feishu_open_ids?: string[];
  feishu_at_all?: boolean;
  dingtalk_mobiles?: string[];
  dingtalk_at_all?: boolean;
  discord_user_ids?: string[];
  discord_role_ids?: string[];
  discord_everyone?: boolean;
}

export interface NotificationConfigSaveInput {
  automatic_events: boolean;
  bot: {
    enabled: boolean;
    /** 空字符串表示沿用已保存 Token。 */
    token: string;
    chat_id: string;
  };
  webhook: NotificationWebhookInput;
}

export interface NotificationOperationResult extends WriteOK {
  message: string;
}

export interface NotificationChatIDResult extends NotificationOperationResult {
  chat_id: string;
}

export const saveNotificationConfig = (
  input: NotificationConfigSaveInput,
): Promise<NotificationOperationResult> =>
  putJSON<NotificationOperationResult>("/api/v1/notification/config", input);

/** 仅使用服务端已保存配置发送测试，不携带表单中的未保存凭据。 */
export const testNotification = (
  channel: "bot" | "webhook",
): Promise<NotificationOperationResult> =>
  postJSON<NotificationOperationResult>("/api/v1/notification/test", { channel });

/** 通过已保存 Bot Token 的 getUpdates 获取最近会话，不提交表单 Token。 */
export const fetchNotificationBotChatID = (): Promise<NotificationChatIDResult> =>
  postJSON<NotificationChatIDResult>("/api/v1/notification/bot/chat-id");

export const saveNotificationPolicy = (
  input: NotificationPolicy,
): Promise<NotificationOperationResult> =>
  putJSON<NotificationOperationResult>("/api/v1/notification/policy", input);

export type NotificationMuteInput = Omit<NotificationMute, "id">;

export const createNotificationMute = (
  input: NotificationMuteInput,
): Promise<NotificationMute> =>
  postJSON<NotificationMute>("/api/v1/notification/mutes", input);

export const updateNotificationMute = (
  id: string,
  input: NotificationMuteInput,
): Promise<NotificationMute> =>
  putJSON<NotificationMute>(`/api/v1/notification/mutes/${encodeURIComponent(id)}`, input);

export const deleteNotificationMute = (id: string): Promise<WriteOK> =>
  deleteJSON<WriteOK>(`/api/v1/notification/mutes/${encodeURIComponent(id)}`);

// ---- 会话 ----

export const logout = (): Promise<WriteOK> =>
  postJSON<WriteOK>("/api/v1/session/logout");

// ---- GitHub OAuth（高风险页面迁移） ----

/** OAuth 配置变更动作：与 SSR 表单 action 字段一一对应。 */
export type OAuthSettingsAction = "save" | "enable" | "disable" | "clear";

export interface OAuthSettingsSaveInput {
  action: OAuthSettingsAction;
  client_id?: string;
  /** 仅本次提交携带新 Secret 时非空；页面不回显已保存 Secret。 */
  client_secret?: string;
  enabled?: boolean;
}

export interface OAuthSettingsSaveResult extends WriteOK {
  settings: OAuthSettingsView;
}

export const saveOAuthSettings = (
  input: OAuthSettingsSaveInput,
): Promise<OAuthSettingsSaveResult> =>
  postJSON<OAuthSettingsSaveResult>("/api/v1/oauth/settings", input);

export interface OAuthBindResult extends WriteOK {
  /** 受控 GitHub 授权跳转地址（一次性 state），由前端 window.location 跳转。 */
  authorize_url: string;
}

export const bindOAuth = (): Promise<OAuthBindResult> =>
  postJSON<OAuthBindResult>("/api/v1/oauth/bind");

export interface OAuthUnbindResult extends WriteOK {
  /** 全部会话已失效，前端应立即跳转登录页。 */
  relogin: boolean;
}

export const unbindOAuth = (): Promise<OAuthUnbindResult> =>
  postJSON<OAuthUnbindResult>("/api/v1/oauth/unbind");

// ---- 备份（高风险页面迁移） ----

export interface BackupOpResult extends WriteOK {
  message: string;
  backup: BackupView;
}

/** 确认整库替换导入（下次启动应用；服务端与 SSR 表单同一 confirm 语义）。 */
export const confirmBackupImport = (): Promise<BackupOpResult> =>
  postJSON<BackupOpResult>("/api/v1/backup/import/confirm", { confirm: "import" });

/**
 * 上传并校验备份（multipart；不经 postJSON——上传大小上限由服务端按
 * SSR 同一来源控制）。成功后进入待确认状态，不改变当前数据库。
 */
export async function uploadBackup(file: File): Promise<BackupOpResult> {
  const form = new FormData();
  form.append("backup", file);
  return apiRequest<BackupOpResult>("/api/v1/backup/import", {
    method: "POST",
    headers: { "X-CSRF-Token": getCSRFToken() },
    body: form,
  });
}

/** 触发 Blob 文件下载 helper。 */
async function triggerBlobDownload(response: Response, defaultFilename: string): Promise<void> {
  if (!response.ok) {
    throw await apiErrorFrom(response);
  }
  const blob = await response.blob();
  const disposition = response.headers.get("Content-Disposition") ?? "";
  const named = /filename="([^"]+)"/.exec(disposition);
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = named?.[1] ?? defaultFilename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

export async function exportBackup(): Promise<void> {
  const response = await fetch("/api/v1/backup/export", {
    method: "POST",
    credentials: "same-origin",
    headers: { "X-CSRF-Token": getCSRFToken() },
  });
  await triggerBlobDownload(response, `${PROJECT_IDENTITY.slug}-backup.db`);
}

/** 导出单个指定的 JSON 配置文件。 */
export async function exportBackupJSON(name: string): Promise<void> {
  const response = await fetch("/api/v1/backup/export/json", {
    method: "POST",
    credentials: "same-origin",
    headers: {
      "Content-Type": "application/json",
      "X-CSRF-Token": getCSRFToken(),
    },
    body: JSON.stringify({ name }),
  });
  await triggerBlobDownload(response, name);
}

/** 一键打包导出所有 JSON 配置文件。 */
export async function exportBackupAllJSON(): Promise<void> {
  const response = await fetch("/api/v1/backup/export/all-json", {
    method: "POST",
    credentials: "same-origin",
    headers: { "X-CSRF-Token": getCSRFToken() },
  });
  await triggerBlobDownload(response, `${PROJECT_IDENTITY.slug}-json-backup.zip`);
}

/** 一键打包导出业务数据库与所有 JSON 配置文件（全量备份）。 */
export async function exportBackupFull(): Promise<void> {
  const response = await fetch("/api/v1/backup/export/full", {
    method: "POST",
    credentials: "same-origin",
    headers: { "X-CSRF-Token": getCSRFToken() },
  });
  await triggerBlobDownload(response, `${PROJECT_IDENTITY.slug}-full-backup.zip`);
}

export interface JSONImportResult extends WriteOK {
  message: string;
  filename?: string;
  backup: BackupView;
}

export interface AllJSONImportResult extends WriteOK {
  message: string;
  count: number;
  backup: BackupView;
}

/** 上传并覆盖单个 JSON 配置文件。 */
export async function uploadBackupJSON(file: File, targetName?: string): Promise<JSONImportResult> {
  const form = new FormData();
  form.append("file", file);
  if (targetName) {
    form.append("target_name", targetName);
  }
  return apiRequest<JSONImportResult>("/api/v1/backup/import/json", {
    method: "POST",
    headers: { "X-CSRF-Token": getCSRFToken() },
    body: form,
  });
}

/** 上传 ZIP 压缩包批量导入所有 JSON 配置文件。 */
export async function uploadBackupAllJSON(file: File): Promise<AllJSONImportResult> {
  const form = new FormData();
  form.append("file", file);
  return apiRequest<AllJSONImportResult>("/api/v1/backup/import/all-json", {
    method: "POST",
    headers: { "X-CSRF-Token": getCSRFToken() },
    body: form,
  });
}

// ---- 受控重启（高风险页面迁移） ----

export interface RestartResult extends WriteOK {
  message: string;
  restarting: boolean;
}

/** 触发受控优雅重启（服务端要求固定 confirm 值，不执行 Shell）。 */
export const restartServer = (): Promise<RestartResult> =>
  postJSON<RestartResult>("/api/v1/restart", { confirm: "restart" });

// ---- MTProto（高风险页面迁移） ----

/** 触发重连（仅离线状态被服务端接受；成功后等待新的扫码二维码）。 */
export const reloginMTProto = (): Promise<WriteOK> =>
  postJSON<WriteOK>("/api/v1/mtproto/relogin");

/** 清理用户号会话文件（session.json + peers.json；仅离线状态被接受）。 */
export const clearMTProtoSession = (): Promise<WriteOK> =>
  postJSON<WriteOK>("/api/v1/mtproto/clear-session");

/** 发起缓存频道迁移（旧频道副本整批复制到当前缓存频道；后台执行）。 */
export const startDumpMigrate = (fromChannelID: number): Promise<WriteOK> =>
  postJSON<WriteOK>("/api/v1/dumpcache/migrate", { from_channel_id: fromChannelID });

// ---- 频道绑定（频道绑定管理页） ----

/** 绑定频道请求体：归属用户 + 频道标识（@username / t.me 链接 / -100… ID）。 */
export interface BindChannelInput {
  user_id: number;
  target: string;
}

export interface BindChannelResult extends WriteOK {
  binding: {
    channel_id: number;
    user_id: number;
    username: string;
    title: string;
    bound_via: string;
    created_at: number;
  };
}

/** 为指定用户绑定频道（服务端校验机器人为该频道管理员且有发言权限）。 */
export const bindChannel = (input: BindChannelInput): Promise<BindChannelResult> =>
  postJSON<BindChannelResult>("/api/v1/channel-bindings", input);

export interface UnbindChannelResult extends WriteOK {
  binding: {
    channel_id: number;
    user_id: number;
    title: string;
  };
}

/** 解除指定频道 ID 的绑定（管理端可解绑任意用户的绑定）。 */
export const unbindChannel = (channelId: number): Promise<UnbindChannelResult> =>
  postJSON<UnbindChannelResult>(`/api/v1/channel-bindings/${encodeURIComponent(String(channelId))}/delete`);

/** 批量删除绑定记录的结果行。 */
export interface DeleteBindingResult {
  channel_id: number;
  ok: boolean;
  error?: string;
}

/** 批量删除绑定响应。 */
export interface DeleteBindingsResult extends WriteOK {
  deleted: number;
  results: DeleteBindingResult[];
}

/** 物理删除绑定记录（单条/批量共用；解绑是软解绑留痕，删除即清行）。 */
export const deleteBindings = (channelIds: number[]): Promise<DeleteBindingsResult> =>
  postJSON<DeleteBindingsResult>("/api/v1/channel-bindings/delete", { channel_ids: channelIds });

// ---- 频道加入（频道加入管理页） ----

/** 加入申请审批结果（同意/拒绝共用形态；服务端 joinmgr.JoinRequestView）。 */
export interface JoinReviewResult extends WriteOK {
  request: {
    id: number;
    user_id: number;
    channel_title: string;
    status: string;
  };
}

/** 同意加入申请：服务端执行加入（链接失效置 failed）并通知申请人。 */
export const approveJoinRequest = (id: number): Promise<JoinReviewResult> =>
  postJSON<JoinReviewResult>(`/api/v1/channel-join/requests/${encodeURIComponent(String(id))}/approve`, {});

/** 拒绝加入申请并通知申请人。 */
export const rejectJoinRequest = (id: number): Promise<JoinReviewResult> =>
  postJSON<JoinReviewResult>(`/api/v1/channel-join/requests/${encodeURIComponent(String(id))}/reject`, {});

/** 删除审批记录的逐条结果（pending 记录不可删）。 */
export interface JoinRequestDeleteOutcomeRow {
  id: number;
  ok: boolean;
  error?: string;
}

export interface DeleteJoinRequestsResult extends WriteOK {
  outcomes: JoinRequestDeleteOutcomeRow[];
}

/** 批量删除审批记录：仅终态（已通过/已拒绝/失败）可删，pending 须先审批。 */
export const deleteJoinRequests = (ids: number[]): Promise<DeleteJoinRequestsResult> =>
  postJSON<DeleteJoinRequestsResult>("/api/v1/channel-join/requests/delete", { ids });

/** 批量退出的逐条结果。 */
export interface LeaveOutcomeRow {
  channel_id: number;
  ok: boolean;
  error?: string;
}

export interface LeaveChannelsResult extends WriteOK {
  outcomes: LeaveOutcomeRow[];
}

/** 批量退出已加入频道（创建者频道与未加入的 ID 返回失败条目）。 */
export const leaveJoinedChannels = (channelIds: number[]): Promise<LeaveChannelsResult> =>
  postJSON<LeaveChannelsResult>("/api/v1/channel-join/channels/leave", { channel_ids: channelIds });

// ---- 云盘下载（配置保存 / 目的地测试 / 补存） ----

/** 云盘配置保存载荷：与 GET 视图同构（rclone_available 由服务端探测，不接受提交）。 */
export interface CloudDriveSaveInput {
  enabled: boolean;
  default_destination: string;
  destinations: CloudDriveView["destinations"];
}

/** PUT /cloud-drive 响应：{"ok":true,"config":{...保存后的视图，字段同 GET 响应}}。 */
export type CloudDriveSaveResult = WriteOK & { config: CloudDriveView };

/** 全量保存云盘下载配置；校验失败抛出服务端受控中文文案的 ApiError。 */
export const saveCloudDrive = (input: CloudDriveSaveInput): Promise<CloudDriveSaveResult> =>
  putJSON<CloudDriveSaveResult>("/api/v1/cloud-drive", input);

/** 目的地连通性测试结果：message 为成功确认或失败的分类中文原因。 */
export interface CloudDriveTestResult {
  ok: boolean;
  message: string;
}

export interface CloudDriveTestInput {
  name?: string;
  destination?: {
    name: string;
    type: string;
    path_prefix?: string;
    enabled?: boolean;
    options?: Record<string, string>;
  };
}

/** 对指定目的地执行 rclone 只读探测（支持已保存目的地名称，或直接传入待测目的地对象）。 */
export const testCloudDrive = (input: string | CloudDriveTestInput): Promise<CloudDriveTestResult> => {
  const body = typeof input === "string" ? { name: input } : input;
  return postJSON<CloudDriveTestResult>("/api/v1/cloud-drive/test", body);
};

// ---- 云盘配置加密备份与恢复 ----

export interface CloudDriveBackupOpResult extends WriteOK {
  candidate?: CloudDriveBackupCandidate;
  rollback_available?: boolean;
}

/** 从 Content-Disposition 提取安全文件名，兼容 filename 与 UTF-8 filename*。 */
function backupFilename(disposition: string): string {
  const encoded = /filename\*=UTF-8''([^;]+)/i.exec(disposition)?.[1];
  if (encoded) {
    try {
      return decodeURIComponent(encoded.trim());
    } catch {
      // 头部编码非法时回退普通 filename 或默认时间戳，不向 UI 泄漏解析细节。
    }
  }
  const named = /filename="([^"]+)"/i.exec(disposition)?.[1];
  if (named) return named;
  const timestamp = new Date().toISOString().replace(/[:.]/g, "-");
  return `${PROJECT_IDENTITY.slug}-cloud-drive-backup-${timestamp}.zip`;
}

/** 导出加密 ZIP；密码只在本次请求体中存在，下载完成立即释放 object URL。 */
export async function exportCloudDriveBackup(password: string): Promise<void> {
  const response = await fetch("/api/v1/cloud-drive/backup/export", {
    method: "POST",
    credentials: "same-origin",
    headers: {
      Accept: "application/zip",
      "Content-Type": "application/json",
      "X-CSRF-Token": getCSRFToken(),
    },
    body: JSON.stringify({ password, password_confirmation: password }),
  });
  if (!response.ok) {
    throw await apiErrorFrom(response);
  }

  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  try {
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = backupFilename(response.headers.get("Content-Disposition") ?? "");
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}

/** 上传加密 ZIP 与本次验证密码；仅生成候选，不应用配置。 */
export async function importCloudDriveBackup(
  backup: File,
  password: string,
): Promise<CloudDriveBackupOpResult> {
  const form = new FormData();
  form.append("backup", backup);
  form.append("password", password);
  return apiRequest<CloudDriveBackupOpResult>("/api/v1/cloud-drive/backup/import", {
    method: "POST",
    headers: { "X-CSRF-Token": getCSRFToken() },
    body: form,
  });
}

export const confirmCloudDriveBackupImport = (): Promise<CloudDriveBackupOpResult> =>
  postJSON<CloudDriveBackupOpResult>("/api/v1/cloud-drive/backup/import/confirm", {
    confirm: "import_cloud_drive",
  });

export const cancelCloudDriveBackupPending = (): Promise<CloudDriveBackupOpResult> =>
  deleteJSON<CloudDriveBackupOpResult>("/api/v1/cloud-drive/backup/pending");

export const rollbackCloudDriveBackup = (): Promise<CloudDriveBackupOpResult> =>
  postJSON<CloudDriveBackupOpResult>("/api/v1/cloud-drive/backup/rollback", {
    confirm: "rollback_cloud_drive",
  });

/** 批量补存资格不满足的跳过原因（与服务端 skip_reason 枚举一一对应）。 */
export type CloudArchiveSkipReason =
  | "not_found"
  | "not_finished"
  | "text_only"
  | "cloud_disabled"
  | "rclone_unavailable"
  | "already_archiving";

/** 批量补存逐条摘要：创建成功带 created_request_id；资格不满足带 skip_reason；
 * 队列饱和时请求行已建但未入队，带 queue_full=true（可稍后经重试入口入队）。 */
export interface CloudArchiveBatchResultRow {
  request_id: number;
  created_request_id?: number;
  skip_reason?: CloudArchiveSkipReason;
  queue_full?: boolean;
}

export interface CloudArchiveBatchResult extends WriteOK {
  results: CloudArchiveBatchResultRow[];
}

/** 单条补存：对终态请求按原链接重抓取并上传网盘（destination 缺省用默认目的地）。 */
export const cloudArchiveRequest = (id: number, destination?: string): Promise<WriteOK> =>
  postJSON<WriteOK>(
    `/api/v1/requests/${id}/cloud-archive`,
    destination ? { destination } : {},
  );

/** 批量补存（1–100 条）：逐条独立执行，返回逐条摘要。 */
export const cloudArchiveBatch = (
  ids: number[],
  destination?: string,
): Promise<CloudArchiveBatchResult> =>
  postJSON<CloudArchiveBatchResult>("/api/v1/requests/cloud-archive-batch", {
    request_ids: ids,
    ...(destination ? { destination } : {}),
  });

/** 缓存补写资格不满足的跳过原因（与服务端 skip_reason 枚举一一对应）。 */
export type DumpBackfillSkipReason =
  | "not_found"
  | "not_finished"
  | "already_dumped"
  | "dump_disabled";

/** 缓存补写逐条摘要：字段语义与云盘补存一致（行已建但未入队时 queue_full=true）。 */
export interface DumpBackfillBatchResultRow {
  request_id: number;
  created_request_id?: number;
  skip_reason?: DumpBackfillSkipReason;
  queue_full?: boolean;
}

export interface DumpBackfillBatchResult extends WriteOK {
  results: DumpBackfillBatchResultRow[];
}

/** 单条缓存补写：对终态请求按原链接重取源消息，把干净副本直接写入缓存频道
 * （不向用户发送任何消息）。 */
export const dumpBackfillRequest = (id: number): Promise<WriteOK> =>
  postJSON<WriteOK>(`/api/v1/requests/${id}/dump-backfill`, {});

/** 批量缓存补写（1–100 条）：逐条独立执行，返回逐条摘要。 */
export const dumpBackfillRequests = (ids: number[]): Promise<DumpBackfillBatchResult> =>
  postJSON<DumpBackfillBatchResult>("/api/v1/requests/dump-backfill-batch", {
    request_ids: ids,
  });
