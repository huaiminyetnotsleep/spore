import type { RecoveryItem, RecoveryJob, RecoveryPreview } from "../src/api/recovery";

export const recoveryJobFixture: RecoveryJob = {
  id: 71, status: "paused", target_chat_id: -1001234567890, target_title: "恢复目标频道",
  bot_id: 42, filter: { channel_key: "fixture-channel", user_id: 0, since: 0, until: 0 },
  created_at: 1700000000000, updated_at: 1700000001000, total: 3, pending: 1,
  processing: 0, succeeded: 0, failed: 1, unrecoverable: 0, uncertain: 1, skipped: 0, last_error: "",
};
export const recoveryItemFixture: RecoveryItem = {
  id: 72, job_id: 71, channel_key: "fixture-channel", message_id: 8101, member_ids: [8101, 8102],
  cache_copies: [{ chat_id: -1009876543210, message_ids: [11, 12] }], sent_ids: [501],
  status: "uncertain", method: "cache", error_code: "SEND_UNCERTAIN", error_message: "部分发送，请核对目标。",
  created_at: 1700000000000, updated_at: 1700000001000,
};
export const recoveryPreviewFixture: RecoveryPreview = {
  target_chat_id: recoveryJobFixture.target_chat_id, target_title: recoveryJobFixture.target_title,
  bot_id: 42, total: 3, with_cache: 2, without_cache: 1,
  cache_channels: [{ channel_id: -1009876543210, readable: true, message: "仅检查读取权限" }],
  warnings: ["历史相册元数据可能不完整。"],
};
export function recoveryEnvelope<T>(items: T[]) {
  return { items, page: 1, page_size: 20, total: items.length, total_pages: items.length ? 1 : 0 };
}
