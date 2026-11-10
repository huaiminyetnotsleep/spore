/**
 * 缓存迁移列表页与运行详情页共享的标签、进度与轮询助手。
 * 轮询只在存在运行中的迁移时以 2 秒执行（与频道设置页进度提示同节奏），
 * 空闲时完全停止。
 */
import type { DumpMigrateItem, DumpMigrateItemStatus, DumpMigrateRun, DumpMigrateRunStatus } from "../../api/admin";
import { StatusTag } from "../shared/StatusTag";

export const runLabels = {
  running: "运行中", paused: "已暂停", completed: "已完成", cancelled: "已取消", interrupted: "已中断",
} as const;
export const itemLabels = { migrated: "已迁移", skipped: "已跳过", failed: "失败" } as const;
export const itemStatuses: DumpMigrateItemStatus[] = ["migrated", "skipped", "failed"];
export const controlLabels = { pause: "暂停迁移", resume: "继续迁移", cancel: "取消迁移" } as const;

export function runStatusTag(status: DumpMigrateRunStatus) {
  return <StatusTag tone={status === "completed" ? "success" : status === "paused" || status === "interrupted" ? "warning" : "default"}>{runLabels[status]}</StatusTag>;
}

export function itemStatusTag(status: DumpMigrateItemStatus) {
  return <StatusTag tone={status === "migrated" ? "success" : status === "failed" ? "error" : "default"}>{itemLabels[status]}</StatusTag>;
}

/** processed/total 百分比（0–100，total 为 0 时 0）。 */
export function runPercent(run: Pick<DumpMigrateRun, "total" | "done" | "skipped" | "failed">) {
  if (run.total <= 0) return 0;
  return Math.min(100, Math.round(((run.done + run.skipped + run.failed) / run.total) * 100));
}

/** 运行的一行计数摘要。 */
export function runCounts(run: Pick<DumpMigrateRun, "total" | "done" | "skipped" | "failed">) {
  return `已迁移 ${run.done} · 跳过 ${run.skipped} · 失败 ${run.failed} · 共 ${run.total}`;
}

/** 明细行「目标频道落地」渲染：频道 ID + 新消息 ID，失败目标显示错误。 */
export function itemResults(item: Pick<DumpMigrateItem, "results">) {
  if (!item.results?.length) return "—";
  return item.results.map((r) =>
    r.error ? `${r.channel_id}：${r.error}` : `${r.channel_id}: ${r.message_ids?.join(", ") ?? ""}`,
  ).join("；");
}

/** 运行列表轮询：只要有 running 项就 2s 轮询。 */
export function migrateRunsPollInterval(query: { state: { data?: { items?: Pick<DumpMigrateRun, "status">[] } } }) {
  return query.state.data?.items?.some((run) => run.status === "running") ? 2000 : false;
}

/** 详情轮询：运行中 2s。 */
export function migrateRunPollInterval(query: { state: { data?: Pick<DumpMigrateRun, "status"> } }) {
  return query.state.data?.status === "running" ? 2000 : false;
}
