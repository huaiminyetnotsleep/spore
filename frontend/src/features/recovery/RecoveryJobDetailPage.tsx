/**
 * 历史恢复任务详情页（/recovery/jobs/:id）。
 * 展示固定目标/机器人/范围、实时聚合计数与最后错误；按状态提供暂停、
 * 继续、取消与受控重试（仅 failed/unrecoverable，不确定项永不重试）。
 * 逐项表支持状态筛选与分页，展示来源坐标、已知缓存副本与已知发送 ID。
 * 只有运行中任务保持 5 秒轮询，空闲时停止。
 */
import { useQuery } from "@tanstack/react-query";
import { BookOutlined } from "@ant-design/icons";
import { Alert, Button, Descriptions, Select, Space, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import { controlRecoveryJob, fetchRecoveryItems, fetchRecoveryJob, type RecoveryControl, type RecoveryItem, type RecoveryItemStatus } from "../../api/recovery";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { PageScaffold, PageSection, ResponsiveActionBar } from "../shared/PageLayout";
import { DetailGate, DetailNotFound } from "../shared/PageStates";
import { InlineQueryState, SectionQueryState } from "../shared/QueryStates";
import { counts, controlLabels, itemStatuses, labels, recoveryJobPollInterval, statusTag } from "./shared";
import { recoveryDocsUrl } from "./RecoveryPage";

const { Text } = Typography;

const itemColumns: ColumnsType<RecoveryItem> = [
  { title: "项目 / 任务", render: (_, item) => `${item.id} / ${item.job_id}` },
  { title: "来源", dataIndex: "channel_key" },
  { title: "消息 ID", dataIndex: "message_id" },
  { title: "状态", dataIndex: "status", render: statusTag },
  { title: "方式", dataIndex: "method", render: (method: string) => method === "cache" ? "缓存复制" : method === "source" ? "来源提取" : "—" },
  { title: "相册成员 ID", dataIndex: "member_ids", render: (ids: number[]) => ids?.join(", ") || "—" },
  { title: "缓存坐标", dataIndex: "cache_copies", render: (copies: RecoveryItem["cache_copies"]) => copies?.map((copy) => `${copy.chat_id}: ${copy.message_ids.join(", ")}`).join("; ") || "—" },
  { title: "已知发送 ID（任务目标）", dataIndex: "sent_ids", render: (ids: number[]) => ids?.join(", ") || "—" },
  { title: "错误码", dataIndex: "error_code", render: (text: string) => text || "—" },
  { title: "错误说明", dataIndex: "error_message", render: (text: string) => text || "—" },
  { title: "创建时间", dataIndex: "created_at", render: fmtTime },
  { title: "更新时间", dataIndex: "updated_at", render: fmtTime },
];

export function RecoveryJobDetailPage() {
  const params = useParams();
  const jobId = Number(params.id);
  const id = Number.isInteger(jobId) && jobId > 0 ? jobId : undefined;
  const navigate = useNavigate();
  const confirm = useConfirmAction();
  const [itemPage, setItemPage] = useState(1);
  const [itemPageSize, setItemPageSize] = useState(20);
  const [itemStatus, setItemStatus] = useState<RecoveryItemStatus>();
  const detail = useQuery({ queryKey: ["recovery", "job", id], queryFn: () => fetchRecoveryJob(id!), enabled: id !== undefined, refetchInterval: recoveryJobPollInterval });
  const items = useQuery({ queryKey: ["recovery", "items", id, itemPage, itemPageSize, itemStatus], queryFn: () => fetchRecoveryItems(id!, itemPage, itemPageSize, itemStatus), enabled: id !== undefined, refetchInterval: () => recoveryJobPollInterval({ state: { data: detail.data } }) });
  const control = useAdminAction({
    action: (vars: { id: number; action: RecoveryControl }) => controlRecoveryJob(vars.id, vars.action),
    invalidate: [["recovery"]], successText: "任务状态已更新。",
  });
  function runControl(job: NonNullable<typeof detail.data>, action: RecoveryControl) {
    confirm({
      intent: action === "retry" ? "default" : "warning", title: `确认${controlLabels[action]}`,
      content: `任务 #${job.id}，目标 ${job.target_title}（${job.target_chat_id}），机器人 ${job.bot_id}。${action === "resume" ? "继续前将重新检查权限；重启后不会自动恢复发送。" : action === "retry" ? "仅重试确定失败或不可恢复的项目（可先补齐来源权限）；不确定结果永不重试，请先人工核对目标和已知发送坐标。" : action === "cancel" ? "停止未发送的项目；已经发送的消息不会删除。" : "暂停后保留进度，在途发送可能仍会完成。"}`,
      action: () => control.run({ id: job.id, action }),
    });
  }
  const job = detail.data;
  if (id === undefined) {
    return (
      <PageScaffold
        title="恢复任务详情"
        description="展示固定目标、范围、聚合计数与逐项结果；不确定结果必须人工核对。"
        actions={<Button href={recoveryDocsUrl} target="_blank" rel="noreferrer" icon={<BookOutlined />}>操作文档</Button>}
      >
        <DetailNotFound title="恢复任务不存在" backTo="/recovery" backText="返回历史恢复" />
      </PageScaffold>
    );
  }
  return (
    <PageScaffold
      title="恢复任务详情"
      description="展示固定目标、范围、聚合计数与逐项结果；不确定结果必须人工核对。"
      actions={<><Button href={recoveryDocsUrl} target="_blank" rel="noreferrer" icon={<BookOutlined />}>操作文档</Button><Button onClick={() => void navigate("/recovery")}>返回历史恢复</Button></>}
    >
      <DetailGate
        loading={detail.isPending}
        error={detail.error}
        onRetry={() => void detail.refetch()}
        notFoundTitle="恢复任务不存在"
        backTo="/recovery"
        backText="返回历史恢复"
      >
        <PageSection title={`任务 #${id} 详情`}>
          <Space direction="vertical" size="middle" className="field-width-full">
            <InlineQueryState pending={detail.isFetching && !!job} text={detail.isFetching && job ? "正在刷新任务详情…" : undefined} />
            <SectionQueryState initialLoading={detail.isPending} error={detail.error} hasData={!!job} onRetry={() => void detail.refetch()}>
              {job ? <>
                <Descriptions column={1} items={[
                  { key: "id", label: "任务 ID", children: job.id }, { key: "status", label: "状态", children: statusTag(job.status) },
                  { key: "target", label: "固定目标", children: `${job.target_title} (${job.target_chat_id})` }, { key: "bot", label: "固定机器人 ID", children: job.bot_id },
                  { key: "filter", label: "固定范围", children: `来源 ${job.filter.channel_key || "全部"} · 用户 ${job.filter.user_id || "全部"} · 开始 ${job.filter.since ? fmtTime(job.filter.since) : "不限"} · 结束（不含）${job.filter.until ? fmtTime(job.filter.until) : "不限"}` },
                  { key: "counts", label: "完整计数", children: counts(job) }, { key: "created", label: "创建时间", children: fmtTime(job.created_at) }, { key: "updated", label: "更新时间", children: fmtTime(job.updated_at) },
                  { key: "error", label: "最后错误", children: job.last_error || "—" },
                ]} />
                <ResponsiveActionBar>
                  {job.status === "running" ? <Button disabled={control.pending} onClick={() => runControl(job, "pause")}>暂停任务</Button> : null}
                  {job.status === "paused" ? <Button disabled={control.pending} onClick={() => runControl(job, "resume")}>继续任务</Button> : null}
                  {job.status === "running" || job.status === "paused" ? <Button disabled={control.pending} onClick={() => runControl(job, "cancel")}>取消任务</Button> : null}
                  {(job.status === "paused" || job.status === "completed") && job.failed + job.unrecoverable > 0 ? <Button disabled={control.pending} onClick={() => runControl(job, "retry")}>重试失败或不可恢复项</Button> : null}
                </ResponsiveActionBar>
              </> : null}
            </SectionQueryState>
            <Alert type="warning" showIcon message="不确定结果可能已发送，必须人工核对目标和已知发送 ID；系统不会自动重试此类项目，也不保证恰好一次投递。" description="发送超时、部分输出或重启时在途项目可能标记为不确定；空的已知发送 ID 不代表确定未发送。" />
          </Space>
        </PageSection>
        <PageSection title="恢复项目">
          <Space direction="vertical" size="middle" className="field-width-full">
            <Select aria-label="逐项状态筛选" className="field-width-full" virtual={false} value={itemStatus ?? "all"} options={[{ value: "all", label: "全部项目状态" }, ...itemStatuses.map((status) => ({ value: status, label: labels[status] }))]} onChange={(value: RecoveryItem["status"] | "all") => { setItemStatus(value === "all" ? undefined : value); setItemPage(1); }} />
            <InlineQueryState pending={items.isFetching && !!items.data} text={items.isFetching && items.data ? "正在刷新项目…" : undefined} />
            <SectionQueryState initialLoading={items.isPending} error={items.error} hasData={!!items.data} onRetry={() => void items.refetch()}>
              <DataTable<RecoveryItem> density="compact" rowKey="id" columns={itemColumns} dataSource={items.data?.items} emptyText="暂无符合条件的恢复项目。" pagination={{ current: itemPage, pageSize: itemPageSize, total: items.data?.total ?? 0, onChange: (next, size) => { setItemPage(next); setItemPageSize(size); } }} />
            </SectionQueryState>
            <Text type="secondary">有运行中任务时每 5 秒自动刷新，空闲时停止轮询。跨来源按来源键稳定排序，不代表原始全局时间顺序。</Text>
          </Space>
        </PageSection>
      </DetailGate>
    </PageScaffold>
  );
}
