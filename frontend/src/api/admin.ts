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

/** 接入的 Bot API 机器人身份（getMe 快照；缺省 = 未接入或查询未成功）。 */
export interface OverviewBot {
  id: number;
  /** getMe 的 first_name（如有 last_name 由后端拼接）。 */
  name: string;
  /** 不含 @；无公开用户名时为空串。 */
  username: string;
}

/** 多机器人池中单个 bot 的展示条目（装配顺序，主 bot 在前）。 */
export interface OverviewBotEntry extends OverviewBot {
  /** 是否主 bot（env 首项）。 */
  primary: boolean;
  /** Bot API 长轮询是否在线。 */
  online: boolean;
  /** 消息拉取冲突（token 被其他服务占用；收不到新消息）。 */
  conflict: boolean;
  /** 已暂停（停止接收新消息；在途任务正常完成）。 */
  paused: boolean;
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
  /** 来源分布固定四行（join_command/approved/external/watch_source，含 0）。 */
  source_dist: DistRow[];
}

export interface OverviewResponse {
  version: string;
  started_at: number;
  addr: string;
  workers: number;
  health: OverviewHealth;
  queue?: OverviewQueue;
  /** 接入机器人身份（主 bot）；旧后端或未就绪时缺省（展示"未接入"）。 */
  bot?: OverviewBot;
  /** 机器人池全部成员；旧后端或空池时缺省。 */
  bots?: OverviewBotEntry[];
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

// ---- 检查更新（总览页服务版本旁的刷新按钮）----

/** 版本比较结论 raw 值：unknown = 当前版本无法解析（如 dev 构建）。 */
export type VersionCheckStatus = "up_to_date" | "outdated" | "unknown";

export interface VersionCheckResponse {
  current_version: string;
  latest_version?: string;
  status: VersionCheckStatus;
  /** 上游发布页链接；查询结果未携带时缺省。 */
  release_url?: string;
  /** 查询时间（Unix 毫秒）；服务端有缓存窗口，可能早于本次点击。 */
  checked_at: number;
}

/** force=true：手动刷新，跳过服务端缓存直接查询上游。 */
export async function fetchVersionCheck(force = false): Promise<VersionCheckResponse> {
  return apiRequest<VersionCheckResponse>(`/api/v1/version/check${force ? "?force=1" : ""}`);
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
  /** 限定受理 bot（多机器人池）；缺省 = 全部。 */
  bot_id?: string;
  /** 全量统计（忽略时间范围）；后端约定值 "1"。 */
  all?: string;
}

/** 按受理 bot 的统计条目；bot_id 为 0 表示存量行/非 Bot 通道创建（展示"未知"）。 */
export interface StatsBot {
  bot_id: number;
  bot_username?: string;
  total: number;
  succeeded: number;
  failed: number;
  last_requested_at: number;
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
  /** 投递方式分布：全部状态请求按 delivery_mode 分组。 */
  delivery_dist: DistRow[];
  error_dist: StatsError[];
  /** 源媒体 DC 分布：一条请求跨多个 DC 时在每个 DC 各计一次。 */
  dc_dist: DistRow[];
  /** 按日源媒体 DC 分布（堆叠柱状图）。 */
  dc_trend: DCTrendPoint[];
  /** 按受理 bot 分布（不受 bot 筛选影响，展示全量分布）。 */
  bot_dist: StatsBot[];
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
  /** 自动置顶偏好：true = 该用户普通任务提交即默认置顶。 */
  auto_pin: boolean;
  /** 来源 bot（首次 /start 的受理 bot）；0 = 存量行/Web 手动添加。 */
  source_bot_id: number;
  source_bot_username?: string;
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
  /** 自动置顶偏好：true = 该用户普通任务提交即默认置顶。 */
  auto_pin: boolean;
  total_requests: number;
  /** 来源 bot（首次 /start 的受理 bot）；0 = 存量行/Web 手动添加。 */
  source_bot_id: number;
  source_bot_username?: string;
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
  username: string;
  display_name: string;
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
  /** 投递方式：reference | upload | mixed | text | reuse | cloud | dump。 */
  delivery_mode: string;
  /** 受理 bot 的 Telegram 账号 ID；0 = 存量行/非 Bot 通道创建（展示"—"）。 */
  bot_id: number;
  /** 受理时的 bot 用户名快照（不含 @）。 */
  bot_username?: string;
  /** 自动置顶：true = 任务成功后会在绑定频道/群组置顶副本组首。 */
  pin: boolean;
  /** 置顶成功的目标数（worker 收尾回写；未回写为 0）。 */
  pin_ok: number;
  /** 参与置顶的目标总数（含副本发送失败的；0 = 完成时无绑定）。 */
  pin_total: number;
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
  /** 限定受理 bot；缺省 = 全部。 */
  bot_id?: string;
  status?: string;
  channel?: string;
  media_type?: string;
  error_code?: string;
  /** 投递方式（reference | upload | mixed | text | reuse | cloud | dump）。 */
  delivery_mode?: string;
  /** 仅置顶任务（pin=1）；缺省 = 全部。 */
  pin?: string;
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
  /** 限定受理 bot（多机器人池）；缺省 = 全部。 */
  bot_id?: string;
  page?: number;
  page_size?: number;
}

export interface TrendPoint {
  day: string;
  total: number;
  succeeded: number;
  failed: number;
}

/** 频道内按受理 bot 的分布条目；bot_id 为 0 表示存量行（展示"未知"）。 */
export interface ChannelBotRow {
  bot_id: number;
  bot_username?: string;
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
  /** 按受理 bot 分布（多机器人池）。 */
  bot_dist: ChannelBotRow[];
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
  /** 来源 bot（首次 /start 的受理 bot）；0 = 存量行。 */
  source_bot_id: number;
  source_bot_username?: string;
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
  /** 一条 Bot 输入允许的有效链接数（1–50，保存后即时生效）。 */
  max_links_per_message: number;
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

  /** 监听源（/watch）配置（即时生效；watch_apply_enabled 缺省 false）。 */
  watch_apply_enabled: boolean;
  /** 用户申请需审批（false = 免审批直接生效）。 */
  watch_require_approval: boolean;
  /** 监听源总数上限（0 = 不限；仅约束用户申请，管理员添加不受限）。 */
  watch_max_sources: number;
  /** 每用户申请上限（0 = 不限；号主经 Bot 提交不受限）。 */
  watch_per_user_limit: number;

  /** 单个请求累计尝试上限（含首次；即时生效；缺省 3，可配 1–10）。 */
  max_request_attempts: number;

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

// ---- 通知设置 ----

export type NotificationWebhookFormat = "generic" | "feishu" | "dingtalk" | "discord";
export type NotificationMentionMode = "none" | "all" | "users" | "roles";

export interface NotificationCredentialState {
  available: boolean;
  /** 凭据不可用时的服务端受控中文说明；正常时缺省。 */
  message?: string;
}

/** 与 notifycfg.View 对齐的脱敏配置；敏感 URL、Token、Secret 永不出现。 */
export interface NotificationConfigView {
  version: number;
  /** 是否将真实系统事件自动投递到已启用的外部通知通道。旧服务端缺省为 false。 */
  automatic_events?: boolean;
  bot: {
    enabled: boolean;
    chat_id: string;
    has_token: boolean;
    credential: NotificationCredentialState;
  };
  webhook: {
    enabled: boolean;
    format: NotificationWebhookFormat;
    has_url: boolean;
    has_secret: boolean;
    credential: NotificationCredentialState;
    generic_signature_header?: string;
    feishu_open_ids?: string[];
    feishu_at_all?: boolean;
    dingtalk_mobiles?: string[];
    dingtalk_at_all?: boolean;
    discord_user_ids?: string[];
    discord_role_ids?: string[];
    discord_everyone?: boolean;
  };
}

export function fetchNotificationConfig(): Promise<NotificationConfigView> {
  return apiRequest<NotificationConfigView>("/api/v1/notification/config");
}

export type NotificationSeverity = "info" | "warn" | "error";
export type NotificationOverride = "inherit" | "enabled" | "disabled";
export type NotificationChannel = "admin_badge" | "bot" | "webhook";

export interface NotificationEventCatalogItem {
  type: string;
  category: string;
  type_label: string;
  severity: NotificationSeverity;
  title: string;
  description: string;
  supports_recovery: boolean;
}

export interface NotificationCategoryPolicy {
  admin_badge: boolean;
  bot: boolean;
  webhook: boolean;
}

export interface NotificationEventPolicy {
  admin_badge: NotificationOverride;
  bot: NotificationOverride;
  webhook: NotificationOverride;
  recovery: NotificationOverride;
}

export interface NotificationPolicy {
  version: number;
  minimum_severity: NotificationSeverity;
  categories: Record<string, NotificationCategoryPolicy>;
  events: Record<string, NotificationEventPolicy>;
}

export type NotificationMuteMatchMode = "all" | "category" | "events";

export interface NotificationMute {
  id: string;
  name: string;
  match_mode: NotificationMuteMatchMode;
  category: string;
  event_types: string[];
  channels: NotificationChannel[];
  starts_at: number;
  ends_at: number;
  permanent: boolean;
  enabled: boolean;
}

export function fetchNotificationEventCatalog(): Promise<NotificationEventCatalogItem[]> {
  return apiRequest<NotificationEventCatalogItem[]>("/api/v1/notification/event-catalog");
}

export function fetchNotificationPolicy(): Promise<NotificationPolicy> {
  return apiRequest<NotificationPolicy>("/api/v1/notification/policy");
}

export function fetchNotificationMutes(): Promise<NotificationMute[]> {
  return apiRequest<NotificationMute[]>("/api/v1/notification/mutes");
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

/** data/ 目录下单个 JSON 配置文件的元数据。 */
export interface JSONFileInfo {
  name: string;
  description: string;
  size_bytes: number;
  mod_time: number;
}

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
  json_files?: JSONFileInfo[];
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
	/** 多机器人池：逐 bot 的直传会话状态（装配顺序，主 bot 在前）。 */
	bots?: MTProtoBotRow[];
}

/** MTProto 状态响应 bots 数组的单 bot 条目。 */
export interface MTProtoBotRow {
  bot_id: number;
  username?: string;
  state: string;
  dc_id?: number;
  updated_at: number;
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

export function fetchChannelBindings(params: { user_id?: string } = {}): Promise<{ items: ChannelBindingRow[] }> {
  return apiRequest<{ items: ChannelBindingRow[] }>(`/api/v1/channel-bindings${toQuery(params)}`);
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
  /** 留痕来源：join_command（号主 /join）| approved（审批通过）| external（外部拉入）| watch_source（监听源邀请）。 */
  source: string;
  joined_by: number;
  joined_at: number;
  /** 创建者频道不可退出。 */
  creator: boolean;
}

export function fetchJoinedChannels(): Promise<{ items: JoinedChannelRow[] }> {
  return apiRequest<{ items: JoinedChannelRow[] }>("/api/v1/channel-join/channels");
}

// ---- 监听源（/watch，预热缓存频道） ----

/**
 * 监听源列表行（GET /api/v1/watch-sources）：含各状态（pending/approved/
 * rejected，待审批排最前）与申请人展示资料（管理员添加 added_by=0 时为空）。
 */
export interface WatchSourceRow {
  /** Bot API -100 形态频道/群组 ID。 */
  channel_id: number;
  /** channel=频道 / supergroup=超级群组（空 = 旧数据）。 */
  kind: string;
  /** 公开源用户名（无 @）；私有源为空。 */
  username: string;
  title: string;
  /** pending=待审批 / approved=生效 / rejected=已拒绝。 */
  status: "pending" | "approved" | "rejected";
  /** approved 行的独立暂停开关。 */
  enabled: boolean;
  /** 0 = 管理员 Web 添加；>0 = 申请人用户 ID。 */
  added_by: number;
  /** 用户 /watch 的受理 bot ID；0 = Web 管理端添加。 */
  bot_id: number;
  /** 受理 bot 用户名快照（bot 移出池后历史仍可读）。 */
  bot_username: string;
  reviewed_by: string;
  created_at: number;
  updated_at: number;
  /** 申请人展示资料（LEFT JOIN；管理员添加或用户已删除时为空）。 */
  user_username: string;
  user_display_name: string;
  /** 该源累计写入缓存频道的条目数（双键合并；健康度指标）。 */
  prewarm_count: number;
  /** 最近一次预热时间（Unix 毫秒；0 = 从未预热）。 */
  prewarm_last_at: number;
}

/** 邀请链接监听申请的处理状态。 */
export type WatchInviteRequestStatus =
  | "pending"
  | "waiting_telegram"
  | "waiting_bot"
  | "approved"
  | "rejected"
  | "failed";

/**
 * 私有邀请链接监听申请。邀请只用于让读取账号加入目标；Bot 管理员权限仍需人工配置。
 */
export interface WatchInviteRequestRow {
  id: number;
  user_id: number;
  masked_hash: string;
  channel_id: number;
  channel_title: string;
  participants: number;
  status: WatchInviteRequestStatus;
  enabled: boolean;
  bot_id: number;
  bot_username: string;
  reviewed_by: string;
  note: string;
  created_at: number;
  updated_at: number;
  user_username: string;
  user_display_name: string;
}

export interface WatchSourcesResult {
  items: WatchSourceRow[];
  /** 兼容尚未返回该字段的服务端版本。 */
  invite_requests?: WatchInviteRequestRow[];
}

export function fetchWatchSources(): Promise<WatchSourcesResult> {
  return apiRequest<WatchSourcesResult>("/api/v1/watch-sources");
}

/**
 * 监听记录行（GET /api/v1/watch-events）：一次预热转储的留痕——哪个 bot、
 * 在哪个源、转发了哪些消息、走哪条路径、缓存落点与关联请求。
 */
export interface WatchEventRow {
  id: number;
  channel_id: number;
  /** 源快照（源删除后仍可读）。 */
  username: string;
  title: string;
  /** 定位消息 ID（相册取首条成员）。 */
  message_id: number;
  /** 转发的源消息 ID（相册为全部成员）。 */
  member_ids: number[];
  /** 缓存频道落点消息 ID（回退入队时为空）。 */
  dump_ids: number[];
  /** 关联 requests 行（仅 fallback；0 = 无）。 */
  request_id: number;
  bot_id: number;
  bot_username: string;
  /** copy=服务端复制 / fallback=受保护内容重传管线。 */
  path: "copy" | "fallback";
  created_at: number;
  /** 规范化源消息链接（与 requests 的 message_url 同一归一规则）。 */
  message_url: string;
}

/** 监听记录分页列表参数。 */
export interface WatchEventsParams {
  channel_id?: number;
  /** copy=服务端复制 / fallback=重传管线；缺省为全部。 */
  path?: "copy" | "fallback";
  page?: number;
  page_size?: number;
}

export interface WatchEventsResult {
  items: WatchEventRow[];
  page: number;
  page_size: number;
  total: number;
}

export function fetchWatchEvents(params: WatchEventsParams): Promise<WatchEventsResult> {
  const qs = new URLSearchParams();
  if (params.channel_id) qs.set("channel_id", String(params.channel_id));
  if (params.path) qs.set("path", params.path);
  if (params.page) qs.set("page", String(params.page));
  if (params.page_size) qs.set("page_size", String(params.page_size));
  const suffix = qs.size > 0 ? `?${qs.toString()}` : "";
  return apiRequest<WatchEventsResult>(`/api/v1/watch-events${suffix}`);
}

/** 预热事件删除响应（单条/批量共用）。 */
export interface WatchEventsDeleteResult {
  ok: boolean;
  /** 实际删除行数（不存在的不计入）。 */
  deleted: number;
}

/** 监听模块统计视图（GET /api/v1/watch-stats）。 */
export interface WatchStatsView {
  sources: { approved: number; pending: number; rejected: number };
  by_source: {
    channel_id: number;
    title: string;
    username: string;
    events: number;
    messages: number;
    last_at: number;
  }[];
  by_bot: { bot_id: number; bot_username: string; events: number }[];
  by_user: {
    added_by: number;
    user_username: string;
    user_display_name: string;
    events: number;
  }[];
  /** 最近 N 天趋势（缺日补零，升序）。 */
  trend: { day: string; events: number }[];
}

/** 监听统计响应：视图 + 实际生效时间范围回显（all 时为空串）。 */
export interface WatchStatsResult extends WatchStatsView {
  since_day: string;
  until_day: string;
}

/** 监听模块统计（时间段/bot 筛选与 fetchStats 同款参数）。 */
export function fetchWatchStats(params: RangeParams = {}): Promise<WatchStatsResult> {
  // 与 fetchStats / parseTimeRange 使用同一参数名：since / until / all / bot_id。
  return apiRequest<WatchStatsResult>(`/api/v1/watch-stats${toQuery(params)}`);
}

// ---- 机器人池管理（多机器人池） ----

/**
 * 机器人列表条目（GET /api/v1/bots）。合并 env（只读）与 bots.json（可增删）
 * 来源，并合并运行时身份；token 只进不出，任何字段不回显 token。
 */
export interface BotRow {
  /** Telegram bot 账号 ID（token 数字前缀）；0 = 尚未接入。 */
  bot_id: number;
  username?: string;
  name?: string;
  /** 是否主 bot（env 首项）。 */
  primary: boolean;
  /** Bot API 长轮询在线。 */
  online: boolean;
  /** 消息拉取冲突（token 被其他服务占用；收不到新消息）。 */
  conflict: boolean;
  /** 已暂停（停止接收新消息；在途任务正常完成）。 */
  paused: boolean;
  /** Bot MTProto 直传会话 raw 状态；空串 = 未接入。 */
  mtproto_state?: string;
  /** env（环境变量，只读）| file（bots.json，可增删）。 */
  source: string;
  /** 已配置但当前进程未接入（等待重启生效）。 */
  restart_pending: boolean;
}

export interface BotsView {
  bots: BotRow[];
  max_bots: number;
  /** 有条目等待重启生效。 */
  need_apply: boolean;
}

export function fetchBots(): Promise<BotsView> {
  return apiRequest<BotsView>("/api/v1/bots");
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
