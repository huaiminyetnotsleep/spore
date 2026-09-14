/**
 * 只读管理页面的 /api/v1 查询契约（类型 + 请求函数）。
 * 类型独立于 SSR view struct：时间一律 Unix 毫秒（0 表示尚未发生），
 * 可空文本以空串表示；中文标签与格式化由 shared/format 处理。
 * 本模块只做查询（GET）；写操作 API 随后续迁移任务补充。
 */
import { apiRequest } from "./client";

/** 列表统一分页信封。 */
export interface ListEnvelope<T> {
  items: T[];
  page: number;
  page_size: number;
  total: number;
  total_pages: number;
}

/** 组装查询串；undefined/空串字段跳过（保持 URL 与筛选语义一致）。 */
function toQuery<T extends object>(params: T): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== "") {
      search.set(key, String(value));
    }
  }
  const encoded = search.toString();
  return encoded ? `?${encoded}` : "";
}

/**
 * 构造同源 CSV 导出地址（SSR 端点不变，沿用会话 Cookie 鉴权）。
 * 只拼筛选参数，不含分页（与 SSR exportQuery 行为一致）。
 */
export function buildExportURL<T extends object>(path: string, params: T = {} as T): string {
  return `${path}${toQuery(params)}`;
}

// ---- 总览（纯快照）----

export interface OverviewHealth {
  store_ok: boolean;
  /** raw 状态：ready | login_pending | offline | unknown */
  mtproto_state: string;
  mtproto_error?: string;
  /** Bot MTProto 会话 raw 状态：ready | offline；空串 = 未接入（旧后端）。 */
  bot_mtproto_state?: string;
  /** Bot MTProto 会话当前主 DC；正数才有效，0/缺省表示未知或离线。 */
  bot_mtproto_dc_id?: number;
  db_size_bytes: number;
  db_path: string;
  temp_dir_bytes: number;
  temp_dir: string;
  github_configured: boolean;
}

export interface OverviewQueue {
  len: number;
  cap: number;
}

export interface DistRow {
  /** 空串表示"未记录"（展示文案由 shared/format 统一）。 */
  key: string;
  count: number;
}

export interface OverviewRequests {
  queued_rows: number;
  processing_rows: number;
}

export interface OverviewUsers {
  total: number;
  enabled: number;
  pending: number;
  disabled: number;
  archived: number;
}

/** 频道加入全时段快照统计（不随时间范围变化；join 关闭时历史数据仍有意义）。 */
export interface OverviewJoin {
  pending: number;
  approved: number;
  rejected: number;
  failed: number;
  /** 当前加入中的频道总数（三来源合计）。 */
  active_joined: number;
  /** 外部拉入且当前仍加入的频道数。 */
  external_active: number;
  /** 已退出频道总数（三来源合计）。 */
  left_total: number;
  /** 加入数量上限（syscfg；0 = 不限）。 */
  max_channels: number;
  /** 来源分布固定三行（join_command/approved/external，含 0）。 */
  source_dist: DistRow[];
}

export interface OverviewResponse {
  version: string;
  started_at: number;
  addr: string;
  workers: number;
  health: OverviewHealth;
  queue?: OverviewQueue;
  requests: OverviewRequests;
  users: OverviewUsers;
  join: OverviewJoin;
}

/** 零值 join 快照：旧版本后端的 overview 响应没有 join 字段（如 /api/v1/stats
 *  拆分前启动的进程），回退为全零以兼容前后端短暂版本错位，避免总览页崩白。 */
const EMPTY_JOIN: OverviewJoin = {
  pending: 0,
  approved: 0,
  rejected: 0,
  failed: 0,
  active_joined: 0,
  external_active: 0,
  left_total: 0,
  max_channels: 0,
  source_dist: [],
};

export async function fetchOverview(): Promise<OverviewResponse> {
  const data = await apiRequest<OverviewResponse>("/api/v1/overview");
  return { ...data, join: data.join ?? EMPTY_JOIN };
}

// ---- 系统资源与传输监控 ----

export type SystemMetricsRange = "realtime" | "4h" | "1d";

export interface SystemMetricPoint {
  at: number;
  rss_bytes: number | null;
  temp_dir_bytes: number | null;
  download_bytes_per_second: number | null;
  upload_bytes_per_second: number | null;
  /** 进程 CPU 占用率（0–100，占全部核心）；null 表示探针不可用。 */
  cpu_percent: number | null;
}

export interface SystemMetricsResponse {
  range: SystemMetricsRange;
  since: number;
  until: number;
  sample_interval_ms: number;
  points: SystemMetricPoint[];
}

export function fetchSystemMetrics(range: SystemMetricsRange): Promise<SystemMetricsResponse> {
  return apiRequest<SystemMetricsResponse>(`/api/v1/system-metrics${toQuery({ range })}`);
}

// ---- 业务统计（时间范围）----

/** 运营时区日期范围筛选（YYYY-MM-DD，与 since/until 查询参数一致）。 */
export interface RangeParams {
  since?: string;
  until?: string;
  /** 全量统计（忽略时间范围）；后端约定值 "1"。 */
  all?: string;
}

export interface StatsChannel {
  key: string;
  total: number;
  succeeded: number;
  failed: number;
  success_rate: number;
  last_requested_at: number;
}

export interface StatsTrendPoint {
  day: string;
  total: number;
  succeeded: number;
  failed: number;
  /** 无终态请求的日期为 null。 */
  error_rate: number | null;
}

export interface StatsUser {
  id: number;
  username: string;
  display_name: string;
  total: number;
  succeeded: number;
  failed: number;
  success_rate: number;
  last_requested_at: number;
}

export interface StatsError {
  /** __other__ 表示 Top N 之外的错误汇总。 */
  key: string;
  count: number;
  ratio: number;
}

/** 按日源媒体 DC 分布数据点；dist 的 key 为 DC ID 十进制串，空串 = 未记录。 */
export interface DCTrendPoint {
  day: string;
  dist: DistRow[];
}

export interface StatsRequests {
  total: number;
  succeeded: number;
  failed: number;
  unfinished: number;
  active_users: number;
  /** [0,1]；无终态时为 0。 */
  success_rate: number;
  /** [0,1]；无终态时为 0，与 success_rate 使用相同终态分母。 */
  error_rate: number;
  trend: StatsTrendPoint[];
  top_channels: StatsChannel[];
  top_users: StatsUser[];
  media_dist: DistRow[];
  error_dist: StatsError[];
  /** 源媒体 DC 分布：一条请求跨多个 DC 时在每个 DC 各计一次。 */
  dc_dist: DistRow[];
  /** 按日源媒体 DC 分布（堆叠柱状图）。 */
  dc_trend: DCTrendPoint[];
}

export interface StatsResponse {
  /** 实际生效的时间范围回显（缺省近 7 天）。 */
  since_day: string;
  until_day: string;
  requests: StatsRequests;
}

export function fetchStats(params: RangeParams = {}): Promise<StatsResponse> {
  return apiRequest<StatsResponse>(`/api/v1/stats${toQuery(params)}`);
}

// ---- 用户 ----

export interface UserRow {
  id: number;
  username: string;
  display_name: string;
  status: string;
  is_owner: boolean;
  note: string;
  last_used_at: number;
  total_requests: number;
  has_total_requests: boolean;
  /** 用户级云盘下载权限 raw 三态（0=跟随角色默认 1=允许 2=拒绝）。 */
  cloud_download: number;
  /** 生效值（raw 0 时后端按角色解析：owner true / 普通用户 false）。 */
  effective_cloud_download: boolean;
}

export interface UserListParams {
  q?: string;
  status?: string;
  page?: number;
  page_size?: number;
}

export interface UserDetail {
  id: number;
  username: string;
  display_name: string;
  status: string;
  is_owner: boolean;
  note: string;
  created_at: number;
  first_used_at: number;
  last_used_at: number;
  archived_at: number;
  last_denied_at: number;
  last_denied_reason: string;
  /** 受控中文（服务端 deniedText 生成）。 */
  last_denied_text: string;
  used_today: number;
  daily_limit: number;
  remaining_today: number;
  submit_interval_sec: number;
  concurrent_limit: number;
  /** 用户级频道绑定数量上限 raw 值（0 = 跟随角色默认）。 */
  bind_limit: number;
  /** 生效值（bind_limit 为 0 时后端按角色解析：普通 1 / owner 3）。 */
  effective_bind_limit: number;
  /** 用户级云盘下载权限 raw 三态（0=跟随角色默认 1=允许 2=拒绝）。 */
  cloud_download: number;
  /** 生效值（raw 0 时后端按角色解析：owner true / 普通用户 false）。 */
  effective_cloud_download: boolean;
  total_requests: number;
}

export function fetchUsers(params: UserListParams = {}): Promise<ListEnvelope<UserRow>> {
  return apiRequest<ListEnvelope<UserRow>>(`/api/v1/users${toQuery(params)}`);
}

export function fetchUserDetail(id: number | string): Promise<UserDetail> {
  return apiRequest<UserDetail>(`/api/v1/users/${id}`);
}

// ---- 消息记录 ----

/** 实时传输进度快照（字节；仅 processing 且 worker 进行中的记录下发）。 */
export interface RequestProgressData {
  /** 任务全部媒体大小之和（相册为成员累加）。 */
  total_bytes: number;
  downloaded_bytes: number;
  uploaded_bytes: number;
}

export interface RequestRow {
  id: number;
  user_id: number;
  /** public | private */
  source_kind: string;
  channel_key: string;
  message_id: number;
  /** 原始消息完整链接（与 SSR/CSV 同源；私有频道链接需成员权限才能打开）。 */
  message_url: string;
  status: string;
  attempt: number;
  error_code: string;
  media_type: string;
  /** 请求包含的去重媒体类型；相册用于展示成员构成，旧记录为空数组。 */
  media_types?: string[];
  /** 源媒体所在的 Telegram DC ID 去重列表；纯文本/旧记录为空数组。 */
  source_media_dc_ids?: number[];
	/** 投递方式：reference（引用）| upload（上传）| mixed（混合）| text（文本）。 */
  delivery_mode: string;
  requested_at: number;
  duration_ms: number;
  /**
   * 实时下载/上传进度（下载与上传重叠进行，独立计数）；内存态不落库，
   * 仅 processing 且 worker 进行中下发，其余状态字段缺省。
   */
  progress?: RequestProgressData | null;
}

export interface RequestListParams {
  user_id?: string;
  status?: string;
  channel?: string;
  media_type?: string;
  error_code?: string;
  /** 投递方式（reference | upload | mixed | text | cloud）。 */
  delivery_mode?: string;
  since?: string;
  until?: string;
  page?: number;
  page_size?: number;
}

/** 云盘上传记录行（GET /api/v1/requests/{id} 的 cloud_uploads）。 */
export interface CloudUploadRow {
  destination: string;
  /** 远端完整路径（不含目的地前缀）。 */
  remote_path: string;
  /** 文件名；空串 = 未记录。 */
  file_name: string;
  /** uploading | succeeded | failed */
  status: string;
  /** 失败原因 raw 码；成功时为空串。 */
  error_code: string;
  bytes: number;
  created_at: number;
  finished_at: number;
}

export interface RequestDetail extends RequestRow {
  /** 频道标识#消息ID（与 SSR 链接文本同源）。 */
  channel_link_text: string;
  /** 规范化来源 URL；结构化字段异常时为空串（展示为"链接不可用"）。 */
  message_url: string;
  username: string;
  error_text: string;
  attempt_max: number;
  file_name: string;
  file_size: number;
  queued_at: number;
  started_at: number;
  finished_at: number;
  /** 补存来源请求 ID；普通请求为 0。旧版本后端缺省该字段。 */
  parent_request_id?: number;
  /** 云盘上传记录；无记录为 []。旧版本后端缺省该字段。 */
  cloud_uploads?: CloudUploadRow[];
}

export function fetchRequests(params: RequestListParams = {}): Promise<ListEnvelope<RequestRow>> {
  return apiRequest<ListEnvelope<RequestRow>>(`/api/v1/requests${toQuery(params)}`);
}

export function fetchRequestDetail(id: number | string): Promise<RequestDetail> {
  return apiRequest<RequestDetail>(`/api/v1/requests/${id}`);
}

// ---- 频道统计 ----

export interface ChannelRow {
  key: string;
  total: number;
  succeeded: number;
  failed: number;
  /** [0,1]；无终态时为 0。 */
  success_rate: number;
  last_requested_at: number;
}

export interface ChannelListParams {
  since?: string;
  until?: string;
  page?: number;
  page_size?: number;
}

export interface TrendPoint {
  day: string;
  total: number;
  succeeded: number;
  failed: number;
}

export interface ChannelDetail {
  key: string;
  stats: ChannelRow;
  trend: TrendPoint[];
  media_dist: DistRow[];
  error_dist: DistRow[];
  since_day: string;
  until_day: string;
}

export function fetchChannels(params: ChannelListParams = {}): Promise<ListEnvelope<ChannelRow>> {
  return apiRequest<ListEnvelope<ChannelRow>>(`/api/v1/channels${toQuery(params)}`);
}

export function fetchChannelDetail(
  key: string,
  params: RangeParams = {},
): Promise<ChannelDetail> {
  return apiRequest<ChannelDetail>(`/api/v1/channels/${encodeURIComponent(key)}${toQuery(params)}`);
}

// ---- 事件 ----

export interface EventRow {
  id: number;
  key: string;
  severity: string;
  message: string;
  count: number;
  first_at: number;
  last_at: number;
  /** 0 表示从未通知。 */
  last_notified_at: number;
  /** open | resolved */
  status: string;
}

export interface EventListParams {
  status?: string;
  page?: number;
  page_size?: number;
}

export function fetchEvents(params: EventListParams = {}): Promise<ListEnvelope<EventRow>> {
  return apiRequest<ListEnvelope<EventRow>>(`/api/v1/events${toQuery(params)}`);
}

// ---- 审计 ----

/**
 * 审计列表行（GET /api/v1/audit）。before/after 保持服务端返回的 JSON
 * 原始值，不做字段解释；null 表示该次变更没有对应快照。
 */
export interface AuditRow {
  id: number;
  actor: string;
  action: string;
  /** 操作对象；空串表示未记录。 */
  target: string;
  /** Unix 毫秒。 */
  created_at: number;
  before: unknown;
  after: unknown;
}

export interface AuditListParams {
  since?: string;
  until?: string;
  page?: number;
  page_size?: number;
}

export function fetchAudit(params: AuditListParams = {}): Promise<ListEnvelope<AuditRow>> {
  return apiRequest<ListEnvelope<AuditRow>>(`/api/v1/audit${toQuery(params)}`);
}

// ---- 申请审批（管理操作迁移） ----

/** 待审批申请行：pending 用户（按申请先后即 ID 升序，不分页）。 */
export interface ApplicationRow {
  id: number;
  username: string;
  display_name: string;
  /** Unix 毫秒（users.created_at）。 */
  applied_at: number;
}

export function fetchApplications(): Promise<{ items: ApplicationRow[] }> {
  return apiRequest<{ items: ApplicationRow[] }>("/api/v1/applications");
}

// ---- 系统设置（管理操作迁移） ----

/**
 * 可编辑运营设置及其生效状态（GET /api/v1/settings）。
 * 字节值与重启语义由服务端维护；媒体大小输入沿用 SSR 的"数值 + MB/GB 单位"。
 */
export interface SettingsView {
  timezone: string;
  dedup_window_min: number;
  queue_capacity: number;
  /** 当前进程实际容量；0 表示未接入队列指标。 */
  queue_runtime: number;
  queue_same: boolean;
  /** 任务并发 worker 数：配置值（DB 覆盖或环境默认，重启生效）与当前进程值。 */
  worker_count: number;
  worker_count_runtime: number;
  /** 配置值与当前进程值一致；false 表示待重启生效。 */
  worker_count_same: boolean;
  max_file_size_bytes: number;
  stream_limit_bytes: number;
  max_file_size_runtime_bytes: number;
  stream_limit_runtime_bytes: number;
  /** 临时媒体目录总量上限（字节）与当前进程值。 */
  temp_dir_max_size_bytes: number;
  temp_dir_max_size_runtime_bytes: number;
  /** 配置值与当前进程值一致；false 表示待重启生效。 */
  media_same: boolean;
  /** 内存管道进程级预算（字节；保存后即时生效）。 */
  memory_budget_bytes: number;

  /** 频道副本同步总开关（即时生效；缺省 true）。 */
  channel_copy_enabled: boolean;

  /** TG 链接复用总开关（即时生效；缺省 true）。 */
  tg_reuse_enabled: boolean;
  /** 缓存频道（重复链接复用的干净副本来源）；0 = 未配置。 */
  dump_channel_id: number;
  /** 缓存频道标题（展示用）。 */
  dump_channel_title: string;

  /** 频道加入（/join）配置（即时生效；join_enabled 缺省 false）。 */
  join_enabled: boolean;
  /** 自动退出外部拉入的频道（惰性检测；缺省 false）。 */
  join_auto_leave_external: boolean;
  join_require_approval: boolean;
  /** 加入数量上限，0 表示不限。 */
  join_max_channels: number;
  join_mute_enabled: boolean;
  join_archive_enabled: boolean;

  /** 文件分片传输运行时配置：当前生效值（1–16）。 */
  download_threads: number;
  upload_threads: number;
  download_connections: number;
  upload_connections: number;
  /** 各项 .env 默认值（数据库未覆盖时即为当前值）。 */
  download_threads_env: number;
  upload_threads_env: number;
  download_connections_env: number;
  upload_connections_env: number;
  /** 各项是否存在数据库覆盖。 */
  download_threads_overridden: boolean;
  upload_threads_overridden: boolean;
  download_connections_overridden: boolean;
  upload_connections_overridden: boolean;

  /** 最近备份时间（Unix 毫秒）；0 表示从未备份。 */
  last_backup_at: number;
}

export function fetchSettings(): Promise<SettingsView> {
  return apiRequest<SettingsView>("/api/v1/settings");
}

// ---- 系统设置（系统身份，与运营设置分离） ----

/** 系统身份配置（GET /api/v1/system/config）。本期仅系统名称。 */
export interface SystemConfigView {
  /** 系统名称；未配置或非法时服务端返回缺省值 Spore。 */
  system_name: string;
}

export function fetchSystemConfig(): Promise<SystemConfigView> {
  return apiRequest<SystemConfigView>("/api/v1/system/config");
}

// ---- GitHub OAuth（高风险页面迁移） ----

/**
 * GitHub 登录配置与绑定状态（GET /api/v1/oauth/settings）。
 * 字段口径与 SSR /settings/oauth 页面一致；Secret 任何情况下不出现，
 * secret_set 只报布尔；bound_at 为 Unix 毫秒（0 表示未绑定）。
 */
export interface OAuthSettingsView {
  /** 当前生效的通道可用状态（数据库配置优先于环境变量）。 */
  github_configured: boolean;
  /** Client ID 与 Secret 已完整配置（数据库或环境变量）。 */
  configured: boolean;
  enabled: boolean;
  client_id: string;
  secret_set: boolean;
  bound: boolean;
  github_login?: string;
  github_id?: number;
  bound_at?: number;
  /** 配置/绑定读取失败的受控提示；正常为空。 */
  read_error?: string;
}

export function fetchOAuthSettings(): Promise<OAuthSettingsView> {
  return apiRequest<OAuthSettingsView>("/api/v1/oauth/settings");
}

// ---- 备份（高风险页面迁移） ----

/** 备份页状态（GET /api/v1/backup），口径与 SSR /backup 页面一致。 */
export interface BackupView {
  db_path: string;
  /** 数据库文件字节数；文件不可统计为 0。 */
  db_size_bytes: number;
  /** 最近备份时间（Unix 毫秒）；0 表示从未备份。 */
  last_backup_at: number;
  pending: boolean;
  /** 待确认 | confirmed | failed。 */
  pending_state?: string;
  pending_sha256?: string;
}

export function fetchBackupStatus(): Promise<BackupView> {
  return apiRequest<BackupView>("/api/v1/backup");
}

// ---- MTProto 状态（高风险页面迁移） ----

/**
 * MTProto 登录会话状态（GET /api/v1/mtproto/status）。
 * 与 SSR /mtproto/status 字段一致（state/updated_at/last_error）；
 * 差异仅是不下发扫码 URL 本身：二维码经同源 /mtproto/qr.png 图片展示，
 * qr_available 只表达"当前有待扫码的二维码"。
 */
export interface MTProtoStatus {
  state: string;
  updated_at: number;
	qr_available: boolean;
	/** Bot MTProto 会话状态；未接入时字段缺省。 */
	bot_state?: string;
	bot_dc_id?: number;
	bot_updated_at?: number;
	last_error?: string;
}

export function fetchMTProtoStatus(): Promise<MTProtoStatus> {
  return apiRequest<MTProtoStatus>("/api/v1/mtproto/status");
}

// ---- 频道绑定（频道绑定管理页） ----

/** 频道绑定列表行（GET /api/v1/channel-bindings），与 internal/web/api_bindings.go 对齐。 */
export interface ChannelBindingRow {
  /** Bot API 频道数字 ID（-100…），主键。 */
  channel_id: number;
  /** 绑定归属用户的 Telegram ID。 */
  user_id: number;
  /** 频道公开用户名（无 @），私有频道为空串。 */
  username: string;
  /** 绑定时取得的频道标题。 */
  title: string;
  /** bot（用户 /bind 指令）| web（管理端）。 */
  bound_via: string;
  /** 绑定时间（Unix 毫秒）。 */
  created_at: number;
  /** 所属用户的用户名（用户被硬删除后为空串）。 */
  user_username: string;
  /** 所属用户的显示名。 */
  user_display_name: string;
}

export function fetchChannelBindings(): Promise<{ items: ChannelBindingRow[] }> {
  return apiRequest<{ items: ChannelBindingRow[] }>("/api/v1/channel-bindings");
}

// ---- 频道加入（频道加入管理页） ----

/** 加入申请行（GET /api/v1/channel-join/requests），invite_hash 仅脱敏下发。 */
export interface JoinRequestRow {
  id: number;
  user_id: number;
  channel_title: string;
  status: "pending" | "approved" | "rejected" | "failed";
  requested_at: number;
  reviewed_at: number;
  reviewed_by: string;
  note: string;
  masked_hash: string;
}

/** 审批记录列表的筛选与分页参数；零值/缺省字段表示该条件不限。 */
export interface JoinRequestListParams {
  status?: string;
  user_id?: number;
  /** 频道标题模糊匹配。 */
  keyword?: string;
  /** YYYY-MM-DD（运营时区，含当天）。 */
  since?: string;
  until?: string;
  page?: number;
  page_size?: number;
}

/** 服务端分页信封（与 /api/v1 列表端点统一形态一致）。 */
export interface JoinRequestListPage {
  items: JoinRequestRow[];
  page: number;
  page_size: number;
  total: number;
  total_pages: number;
}

export function fetchJoinRequests(params: JoinRequestListParams = {}): Promise<JoinRequestListPage> {
  const q = new URLSearchParams();
  if (params.status) q.set("status", params.status);
  if (params.user_id) q.set("user_id", String(params.user_id));
  if (params.keyword) q.set("keyword", params.keyword);
  if (params.since) q.set("since", params.since);
  if (params.until) q.set("until", params.until);
  if (params.page) q.set("page", String(params.page));
  if (params.page_size) q.set("page_size", String(params.page_size));
  const qs = q.toString();
  return apiRequest<JoinRequestListPage>(`/api/v1/channel-join/requests${qs ? `?${qs}` : ""}`);
}

/** 已加入频道行（GET /api/v1/channel-join/channels）：MTProto 实时列表 + 留痕来源。 */
export interface JoinedChannelRow {
  /** 裸频道 ID（t.me/c/<id> 的 id 部分）。 */
  channel_id: number;
  title: string;
  username: string;
  /** channel（广播频道）| supergroup。 */
  kind: string;
  /** 留痕来源：join_command（号主 /join）| approved（审批通过）| external（外部拉入）。 */
  source: string;
  joined_by: number;
  joined_at: number;
  /** 创建者频道不可退出。 */
  creator: boolean;
}

export function fetchJoinedChannels(): Promise<{ items: JoinedChannelRow[] }> {
  return apiRequest<{ items: JoinedChannelRow[] }>("/api/v1/channel-join/channels");
}

// ---- 云盘下载（配置查询；写操作在 api/mutations.ts） ----

/** 云盘目的地配置行（与 data/cloud-drive.json 同构）。 */
export interface CloudDriveDestination {
  /** 唯一名称，^[a-z][a-z0-9-]{0,31}$（禁 `_`，避免 rclone 环境变量映射撞名）。 */
  name: string;
  /** rclone 后端类型（如 mega、s3；webdav 为未来预留）。 */
  type: string;
  /** 远端路径前缀。 */
  path_prefix: string;
  enabled: boolean;
  /** rclone 后端参数键值对；管理端提交的 MEGA pass 可为原始密码，服务端保存时自动混淆。 */
  options: Record<string, string>;
}

/** 云盘下载配置与运行状态（GET /api/v1/cloud-drive）。 */
export interface CloudDriveView {
  /** 全局开关；关闭时授权用户 /download 收"云盘下载功能未开启"。 */
  enabled: boolean;
  /** 默认目的地名；空串 = 未设置。 */
  default_destination: string;
  /** rclone 二进制是否可用（服务端探测值，不接受提交）。 */
  rclone_available: boolean;
  destinations: CloudDriveDestination[];
}

export function fetchCloudDrive(): Promise<CloudDriveView> {
  return apiRequest<CloudDriveView>("/api/v1/cloud-drive");
}

/** 已验证、尚未应用的云盘配置备份候选摘要；不包含 options 或任何凭据。 */
export interface CloudDriveBackupCandidate {
  pending: boolean;
  format_version?: number;
  /** manifest 内的 RFC 3339 创建时间。 */
  created_at?: string;
  /** 候选上传时间（Unix 毫秒）。 */
  uploaded_at?: number;
  app_version?: string;
  /** 加密 payload 的 SHA-256。 */
  sha256?: string;
  size?: number;
  destination_names?: string[];
}

/** 云盘配置备份状态（GET /api/v1/cloud-drive/backup/status）。 */
export interface CloudDriveBackupStatus extends CloudDriveBackupCandidate {
  rollback_available: boolean;
}

export function fetchCloudDriveBackupStatus(): Promise<CloudDriveBackupStatus> {
  return apiRequest<CloudDriveBackupStatus>("/api/v1/cloud-drive/backup/status");
}
