/**
 * 历史恢复列表页与详情页共享的标签与轮询助手。
 * 轮询只在存在运行中任务时以 5 秒间隔执行，空闲时完全停止。
 */
import type { RecoveryControl, RecoveryItemStatus, RecoveryJob } from "../../api/recovery";
import { StatusTag } from "../shared/StatusTag";

export const labels = {
  running: "运行中", paused: "已暂停", completed: "已完成", cancelled: "已取消",
  pending: "待处理", processing: "处理中", succeeded: "成功", failed: "失败",
  unrecoverable: "不可恢复", uncertain: "结果不确定", skipped: "已跳过",
} as const;
export const itemStatuses: RecoveryItemStatus[] = ["pending", "processing", "succeeded", "failed", "unrecoverable", "uncertain", "skipped"];
export const controlLabels: Record<RecoveryControl, string> = { pause: "暂停任务", resume: "继续任务", cancel: "取消任务", retry: "重试失败或不可恢复项" };

export function statusTag(status: keyof typeof labels) {
  return <StatusTag tone={status === "succeeded" || status === "completed" ? "success" : status === "failed" || status === "unrecoverable" ? "error" : status === "uncertain" || status === "paused" ? "warning" : "default"}>{labels[status]}</StatusTag>;
}
export function counts(job: RecoveryJob) {
  return `总计 ${job.total} · 待处理 ${job.pending} · 处理中 ${job.processing} · 成功 ${job.succeeded} · 失败 ${job.failed} · 不可恢复 ${job.unrecoverable} · 不确定 ${job.uncertain} · 跳过 ${job.skipped}`;
}
export function recoveryJobsPollInterval(query: { state: { data?: { items?: Pick<RecoveryJob, "status">[] } } }) {
  return query.state.data?.items?.some((job) => job.status === "running") ? 5000 : false;
}
export function recoveryJobPollInterval(query: { state: { data?: Pick<RecoveryJob, "status"> } }) {
  return query.state.data?.status === "running" ? 5000 : false;
}
