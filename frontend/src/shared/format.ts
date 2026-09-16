/**
 * 共享格式化与中文标签 util。
 * 语义与 SSR 页面（internal/web pages.go）逐项对齐：状态中文标签、
 * 时间/耗时/字节/成功率格式；SPA 按浏览器本地时区展示时间。
 */
import dayjs from "dayjs";

/** 用户状态中文标签（SSR statusText 同源）。 */
export const USER_STATUS_LABELS: Record<string, string> = {
  pending: "待审批",
  enabled: "已启用",
  disabled: "已禁用",
  archived: "已归档",
};

/** 请求状态中文标签（SSR statusText 同源）。 */
export const REQUEST_STATUS_LABELS: Record<string, string> = {
  queued: "排队中",
  processing: "处理中",
  succeeded: "成功",
  failed: "失败",
  cancelled: "已取消",
};

/** 事件状态中文标签（SSR events 页面同源）。 */
export const EVENT_STATUS_LABELS: Record<string, string> = {
  open: "未解决",
  resolved: "已解决",
};

/**
 * 投递方式中文标签（requests.delivery_mode，与 CSV deliveryModeText 同源语义）：
 * reference = 全部媒体经引用送达；upload = 全部经下载上传；mixed = 两者兼有；
 * text = 纯文本请求，无媒体；reuse = 重复链接复用（copyMessages 直拷历史
 * 已投递消息）；cloud = 云盘下载（/download 指令或管理端补存创建）；
 * dump = 缓存补写（管理端转存缓存频道，不向用户投递）。
 */
export const DELIVERY_MODE_LABELS: Record<string, string> = {
  reference: "引用",
  upload: "上传",
  mixed: "混合",
  text: "文本",
  reuse: "复用",
  cloud: "网盘",
  dump: "缓存补写",
};

/**
 * MTProto 会话状态中文标签（SSR mtprotoStateText 同源）。
 * unknown 仅在服务未接入 MTProto 会话时由 API 返回，与 SSR 的"未接入"一致。
 */
export const MTPROTO_STATE_LABELS: Record<string, string> = {
  ready: "就绪",
  login_pending: "登录进行中",
  offline: "离线",
  unknown: "未接入",
};

/** 频道加入来源中文标签（joined_channels.joined_via，后端只下发 raw key）。 */
export const JOIN_SOURCE_LABELS: Record<string, string> = {
  join_command: "号主 /join",
  approved: "审批通过",
  external: "外部拉入",
};

/** 来源 raw key → 中文标签；未知来源回退原值。 */
export function joinSourceLabel(key: string): string {
  return JOIN_SOURCE_LABELS[key] ?? key;
}

/** 状态标签配色（Ant Tag color）。 */
export const USER_STATUS_TAG_COLORS: Record<string, string> = {
  pending: "gold",
  enabled: "green",
  disabled: "red",
  archived: "default",
};

export const REQUEST_STATUS_TAG_COLORS: Record<string, string> = {
  queued: "blue",
  processing: "gold",
  succeeded: "green",
  failed: "red",
  cancelled: "default",
};

export const EVENT_STATUS_TAG_COLORS: Record<string, string> = {
  open: "red",
  resolved: "green",
};

/** 投递方式标签配色（Ant Tag color）。 */
export const DELIVERY_MODE_TAG_COLORS: Record<string, string> = {
  reference: "green",
  upload: "blue",
  mixed: "gold",
  text: "default",
  reuse: "cyan",
  cloud: "purple",
  dump: "geekblue",
};

/** 云盘上传记录状态中文标签（cloud_uploads.status，后端只下发 raw key）。 */
export const CLOUD_UPLOAD_STATUS_LABELS: Record<string, string> = {
  uploading: "上传中",
  succeeded: "成功",
  failed: "失败",
};

/** 云盘上传记录状态标签配色（Ant Tag color）。 */
export const CLOUD_UPLOAD_STATUS_TAG_COLORS: Record<string, string> = {
  uploading: "gold",
  succeeded: "green",
  failed: "red",
};

/** 批量补存跳过原因中文标签（与服务端 skip_reason 枚举一一对应）。 */
export const CLOUD_ARCHIVE_SKIP_LABELS: Record<string, string> = {
  not_found: "请求不存在",
  not_finished: "请求未结束（排队或处理中）",
  text_only: "纯文本请求，无可存媒体",
  cloud_disabled: "云盘下载功能未开启",
  rclone_unavailable: "rclone 不可用",
  already_archiving: "已有在途补存任务",
};

/** 缓存补写跳过原因中文标签（与服务端 skip_reason 枚举一一对应）。 */
export const DUMP_BACKFILL_SKIP_LABELS: Record<string, string> = {
  not_found: "请求不存在",
  not_finished: "请求未结束（排队或处理中）",
  already_dumped: "缓存频道已有该链接副本",
  dump_disabled: "缓存频道未配置",
};

/** 从标签表取中文标签；未知原样返回（与 SSR statusText 回退一致）。 */
export function labelOf(labels: Record<string, string>, raw: string): string {
  return labels[raw] ?? raw;
}

/** Bot API 长轮询状态随 MTProto 会话派生（与 SSR handleOverview 一致）。 */
export function botAPIStateText(mtprotoState: string): string {
  switch (mtprotoState) {
    case "ready":
      return "运行中";
    case "login_pending":
      return "已停止（等待登录）";
    case "unknown":
      return "未知";
    default:
      return "已停止";
  }
}

/** 分布键空串（未记录）转展示文案（SSR distKey 同源）。 */
export function distKeyText(key: string): string {
  return key === "" ? "（未记录）" : key;
}

/** 受理/来源 bot 展示：@username 优先，空用户名回退 bot id；0（存量行/
 * 非 Bot 通道）显示"—"。多机器人池的行级归属展示共用本单一来源。 */
export function botLabel(botID: number | null | undefined, username?: string): string {
  if (!botID) {
    return "—";
  }
  return username ? `@${username}` : `bot ${botID}`;
}

/** 请求媒体类型展示；相册附带去重后的成员类型。 */
export function requestMediaTypeText(mediaType: string, mediaTypes?: string[]): string {
  const primary = distKeyText(mediaType);
  if (mediaType !== "album" || !mediaTypes?.length) {
    return primary;
  }
  return `${primary}（${mediaTypes.join(" + ")}）`;
}

/** Unix 毫秒 → "YYYY-MM-DD HH:mm:ss"；0/空表示尚未发生 → "—"。 */
export function fmtTime(ms: number | null | undefined): string {
  if (!ms) {
    return "—";
  }
  return dayjs(ms).format("YYYY-MM-DD HH:mm:ss");
}

/** 毫秒耗时 → 人类可读形式（与 SSR fmtDuration 同一分支：850ms / 1.5s / 1m30s）。 */
export function fmtDuration(ms: number): string {
  if (ms <= 0) {
    return "—";
  }
  if (ms < 1000) {
    return `${ms}ms`;
  }
  const seconds = ms / 1000;
  if (seconds < 60) {
    return `${trimTrailingZeros(seconds.toFixed(2))}s`;
  }
  const minutes = Math.floor(seconds / 60);
  return `${minutes}m${trimTrailingZeros((seconds % 60).toFixed(2))}s`;
}

function trimTrailingZeros(text: string): string {
  return text.replace(/\.?0+$/, "");
}

/** 字节数 → KiB/MiB/GiB 人类可读形式（SSR fmtBytes 同源）。 */
export function fmtBytes(n: number): string {
  if (n >= 2 ** 30) {
    return `${(n / 2 ** 30).toFixed(2)} GiB`;
  }
  if (n >= 2 ** 20) {
    return `${(n / 2 ** 20).toFixed(2)} MiB`;
  }
  if (n >= 2 ** 10) {
    return `${(n / 2 ** 10).toFixed(2)} KiB`;
  }
  return `${n} B`;
}

/** 文件大小展示；0/负值表示未落 → "—"（SSR fileSizeText 同源）。 */
export function fmtFileSize(n: number): string {
  return n <= 0 ? "—" : fmtBytes(n);
}

/** [0,1] 比率 → 百分比；无终态（done<=0）→ "—"（SSR fmtRate 同源）。 */
export function fmtRate(rate: number, done: number): string {
  if (done <= 0) {
    return "—";
  }
  return `${(rate * 100).toFixed(1)}%`;
}
