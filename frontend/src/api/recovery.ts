import type { ListEnvelope } from "./admin";
import { apiRequest } from "./client";
import { postJSON } from "./mutations";

export interface RecoveryFilter {
  channel_key: string;
  user_id: number;
  /** Unix milliseconds; zero means no boundary. until is exclusive. */
  since: number;
  until: number;
}
export interface RecoveryInput {
  filter: RecoveryFilter;
  target: string;
  bot_id: number;
}
export interface RecoveryPreview {
  target_chat_id: number;
  target_title: string;
  bot_id: number;
  total: number;
  with_cache: number;
  without_cache: number;
  cache_channels: { channel_id: number; readable: boolean; message: string }[];
  warnings: string[];
}
export type RecoveryJobStatus = "running" | "paused" | "completed" | "cancelled";
export type RecoveryItemStatus = "pending" | "processing" | "succeeded" | "failed" | "unrecoverable" | "uncertain" | "skipped";
export interface RecoveryJob {
  id: number;
  status: RecoveryJobStatus;
  target_chat_id: number;
  target_title: string;
  bot_id: number;
  filter: RecoveryFilter;
  created_at: number;
  updated_at: number;
  total: number;
  pending: number;
  processing: number;
  succeeded: number;
  failed: number;
  unrecoverable: number;
  uncertain: number;
  skipped: number;
  last_error: string;
}
export interface RecoveryItem {
  id: number;
  job_id: number;
  channel_key: string;
  message_id: number;
  member_ids: number[];
  cache_copies: { chat_id: number; message_ids: number[] }[];
  sent_ids: number[];
  status: RecoveryItemStatus;
  method: "cache" | "source" | "";
  error_code: string;
  error_message: string;
  created_at: number;
  updated_at: number;
}
export type RecoveryControl = "pause" | "resume" | "cancel" | "retry";
export const previewRecovery = (input: RecoveryInput) =>
  postJSON<RecoveryPreview>("/api/v1/recovery/preview", input);
export const createRecoveryJob = (input: RecoveryInput) =>
  postJSON<RecoveryJob>("/api/v1/recovery/jobs", input);
export const fetchRecoveryJobs = (page = 1, pageSize = 20) =>
  apiRequest<ListEnvelope<RecoveryJob>>(`/api/v1/recovery/jobs?page=${page}&page_size=${pageSize}`);
export const fetchRecoveryJob = (id: number) =>
  apiRequest<RecoveryJob>(`/api/v1/recovery/jobs/${id}`);
export const fetchRecoveryItems = (id: number, page = 1, pageSize = 20, status?: RecoveryItemStatus) =>
  apiRequest<ListEnvelope<RecoveryItem>>(`/api/v1/recovery/jobs/${id}/items?page=${page}&page_size=${pageSize}${status ? `&status=${status}` : ""}`);
export const controlRecoveryJob = (id: number, action: RecoveryControl) =>
  postJSON<RecoveryJob>(`/api/v1/recovery/jobs/${id}/${action}`);
