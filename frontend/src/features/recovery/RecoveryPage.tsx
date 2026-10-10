import { useQuery } from "@tanstack/react-query";
import { Alert, Button, DatePicker, Descriptions, Form, Input, InputNumber, Select, Space, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import type { Dayjs } from "dayjs";
import { useRef, useState } from "react";

import { fetchBots } from "../../api/admin";
import { controlRecoveryJob, createRecoveryJob, fetchRecoveryItems, fetchRecoveryJob, fetchRecoveryJobs, previewRecovery, type RecoveryControl, type RecoveryInput, type RecoveryItem, type RecoveryItemStatus, type RecoveryJob, type RecoveryPreview } from "../../api/recovery";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { PageScaffold, PageSection, ResponsiveActionBar } from "../shared/PageLayout";
import { InlineQueryState, SectionQueryState } from "../shared/QueryStates";
import { StatusTag } from "../shared/StatusTag";

const { Text } = Typography;
const labels = {
  running: "运行中", paused: "已暂停", completed: "已完成", cancelled: "已取消",
  pending: "待处理", processing: "处理中", succeeded: "成功", failed: "失败",
  unrecoverable: "不可恢复", uncertain: "结果不确定", skipped: "已跳过",
} as const;
const itemStatuses: RecoveryItemStatus[] = ["pending", "processing", "succeeded", "failed", "unrecoverable", "uncertain", "skipped"];
function statusTag(status: keyof typeof labels) {
  return <StatusTag tone={status === "succeeded" || status === "completed" ? "success" : status === "failed" || status === "unrecoverable" ? "error" : status === "uncertain" || status === "paused" ? "warning" : "default"}>{labels[status]}</StatusTag>;
}
interface FormValues {
  channel_key?: string;
  user_id?: number | null;
  since?: Dayjs | null;
  until?: Dayjs | null;
  target: string;
  bot_id: number;
}
export function recoveryInput(values: FormValues): RecoveryInput {
  return {
    filter: { channel_key: values.channel_key?.trim() ?? "", user_id: values.user_id ?? 0, since: values.since?.valueOf() ?? 0, until: values.until?.valueOf() ?? 0 },
    target: values.target.trim(), bot_id: values.bot_id ?? 0,
  };
}
const controlLabels: Record<RecoveryControl, string> = { pause: "暂停任务", resume: "继续任务", cancel: "取消任务", retry: "重试失败或不可恢复项" };
function counts(job: RecoveryJob) {
  return `总计 ${job.total} · 待处理 ${job.pending} · 处理中 ${job.processing} · 成功 ${job.succeeded} · 失败 ${job.failed} · 不可恢复 ${job.unrecoverable} · 不确定 ${job.uncertain} · 跳过 ${job.skipped}`;
}

export function RecoveryPage() {
  const [form] = Form.useForm<FormValues>();
  const [preview, setPreview] = useState<{ input: RecoveryInput; result: RecoveryPreview }>();
  const revision = useRef(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [selectedId, setSelectedId] = useState<number>();
  const [itemPage, setItemPage] = useState(1);
  const [itemPageSize, setItemPageSize] = useState(20);
  const [itemStatus, setItemStatus] = useState<RecoveryItemStatus>();
  const confirm = useConfirmAction();
  const bots = useQuery({ queryKey: ["bots"], queryFn: fetchBots });
  const jobs = useQuery({ queryKey: ["recovery", "jobs", page, pageSize], queryFn: () => fetchRecoveryJobs(page, pageSize), refetchInterval: 5000 });
  const detail = useQuery({ queryKey: ["recovery", "job", selectedId], queryFn: () => fetchRecoveryJob(selectedId!), enabled: selectedId !== undefined, refetchInterval: 5000 });
  const items = useQuery({ queryKey: ["recovery", "items", selectedId, itemPage, itemPageSize, itemStatus], queryFn: () => fetchRecoveryItems(selectedId!, itemPage, itemPageSize, itemStatus), enabled: selectedId !== undefined, refetchInterval: 5000 });
  const preflight = useAdminAction({
    action: async (vars: { input: RecoveryInput; revision: number }) => ({ ...vars, result: await previewRecovery(vars.input) }),
    successText: "预检完成；权限检查不代表每条历史都可恢复。",
    onDone: (data) => { if (data.revision === revision.current) setPreview({ input: data.input, result: data.result }); },
  });
  const create = useAdminAction({
    action: (input: RecoveryInput) => createRecoveryJob(input), invalidate: [["recovery"]], successText: "已创建恢复任务。",
    onDone: (job) => { setSelectedId(job.id); setItemPage(1); setItemStatus(undefined); setPage(1); setPreview(undefined); },
  });
  const control = useAdminAction({
    action: (vars: { id: number; action: RecoveryControl }) => controlRecoveryJob(vars.id, vars.action),
    invalidate: [["recovery"]], successText: "任务状态已更新。",
  });
  const frozen = preflight.pending || create.pending;
  function selectJob(id: number) { setSelectedId(id); setItemPage(1); setItemStatus(undefined); }
  function runControl(job: RecoveryJob, action: RecoveryControl) {
    confirm({
      intent: action === "retry" ? "default" : "warning", title: `确认${controlLabels[action]}`,
      content: `任务 #${job.id}，目标 ${job.target_title}（${job.target_chat_id}），机器人 ${job.bot_id}。${action === "resume" ? "继续前将重新检查权限；重启后不会自动恢复发送。" : action === "retry" ? "仅重试确定失败或不可恢复的项目（可先补齐来源权限）；不确定结果永不重试，请先人工核对目标和已知发送坐标。" : action === "cancel" ? "停止未发送的项目；已经发送的消息不会删除。" : "暂停后保留进度，在途发送可能仍会完成。"}`,
      action: () => control.run({ id: job.id, action }),
    });
  }
  const jobColumns: ColumnsType<RecoveryJob> = [
    { title: "任务", dataIndex: "id", render: (id: number) => <Button type="link" onClick={() => selectJob(id)}>任务 #{id}</Button> },
    { title: "状态", dataIndex: "status", render: statusTag },
    { title: "目标", render: (_, job) => `${job.target_title} (${job.target_chat_id})` },
    { title: "机器人", dataIndex: "bot_id" },
    { title: "进度", render: (_, job) => counts(job) },
    { title: "创建时间", dataIndex: "created_at", render: fmtTime },
    { title: "更新时间", dataIndex: "updated_at", render: fmtTime },
  ];
  const itemColumns: ColumnsType<RecoveryItem> = [
    { title: "项目 / 任务", render: (_, item) => `${item.id} / ${item.job_id}` },
    { title: "来源", dataIndex: "channel_key" },
    { title: "消息 ID", dataIndex: "message_id" },
    { title: "状态", dataIndex: "status", render: statusTag },
    { title: "方式", dataIndex: "method", render: (method: string) => method === "cache" ? "缓存复制" : method === "source" ? "来源提取" : "—" },
    { title: "相册成员 ID", dataIndex: "member_ids", render: (ids: number[]) => ids?.join(", ") || "—" },
    { title: "缓存坐标", dataIndex: "cache_copies", render: (copies: RecoveryItem["cache_copies"]) => copies?.map((copy) => `${copy.chat_id}: ${copy.message_ids.join(", ")}`).join("; ") || "—" },
    { title: "已知发送 ID（所选任务目标）", dataIndex: "sent_ids", render: (ids: number[]) => ids?.join(", ") || "—" },
    { title: "错误码", dataIndex: "error_code", render: (text: string) => text || "—" },
    { title: "错误说明", dataIndex: "error_message", render: (text: string) => text || "—" },
    { title: "创建时间", dataIndex: "created_at", render: fmtTime },
    { title: "更新时间", dataIndex: "updated_at", render: fmtTime },
  ];
  const job = detail.data;
  return (
    <PageScaffold title="历史恢复" description="仅恢复系统已处理的历史，不扫描原频道全部历史，不改变原记录或频道绑定。">
      <PageSection title="恢复范围与目标">
        <Space direction="vertical" size="middle" className="field-width-full">
          <Alert type="info" showIcon message="请提前创建目标频道或非论坛超级群组，加入执行机器人并授予发帖权限。" description="公开频道可填 @username；私有频道可填 -100 开头的 ID。空来源、用户和时间表示全部已处理历史。预检只检查元数据与权限，不发送测试消息，也不保证所有历史可恢复。" />
          <SectionQueryState initialLoading={bots.isPending} error={bots.error} hasData={!!bots.data} onRetry={() => void bots.refetch()}>
            {bots.data ? <Form form={form} layout="vertical" initialValues={{ bot_id: 0 }} disabled={frozen} onValuesChange={() => { revision.current += 1; setPreview(undefined); }} onFinish={(values: FormValues) => {
              const input = recoveryInput(values);
              setPreview(undefined);
              void preflight.run({ input, revision: revision.current });
            }}>
              <Form.Item name="channel_key" label="来源频道" extra="公开用户名、私有频道内部数字 ID 或 -100 ID；留空为全部来源。"><Input placeholder="@source 或 -1001234567890" /></Form.Item>
              <Form.Item name="user_id" label="用户 ID" rules={[{ type: "integer", min: 1, message: "请输入正整数用户 ID，或留空。" }]}><InputNumber className="field-width-full" placeholder="留空为全部用户" /></Form.Item>
              <Form.Item name="since" label="开始时间（包含）"><DatePicker showTime className="field-width-full" /></Form.Item>
              <Form.Item name="until" label="结束时间（不包含）" dependencies={["since"]} rules={[({ getFieldValue }) => ({ validator: (_, value: Dayjs | null) => !value || !getFieldValue("since") || value.valueOf() > (getFieldValue("since") as Dayjs).valueOf() ? Promise.resolve() : Promise.reject(new Error("结束时间必须晚于开始时间。")) })]}><DatePicker showTime className="field-width-full" /></Form.Item>
              <Form.Item name="target" label="目标频道 / 超级群组" rules={[{ required: true, whitespace: true, message: "请输入提前创建的目标。" }]}><Input placeholder="@target 或 -1001234567890" /></Form.Item>
              <Form.Item name="bot_id" label="执行机器人"><Select virtual={false} options={[{ value: 0, label: "默认主机器人（创建时固定实际 ID）" }, ...bots.data.bots.map((bot) => ({ value: bot.bot_id, label: `${bot.name || bot.username || "机器人"} (${bot.bot_id})${bot.disabled ? " · 已停用" : bot.restart_pending ? " · 等待重启" : !bot.online ? " · 离线" : ""}`, disabled: bot.disabled || bot.restart_pending || !bot.online || bot.bot_id <= 0 }))]} /></Form.Item>
              <ResponsiveActionBar><Button htmlType="submit" loading={preflight.pending} disabled={frozen}>权限预检</Button></ResponsiveActionBar>
            </Form> : null}
          </SectionQueryState>
          {preview ? <>
            <Descriptions column={1} title="预检结果" items={[
              { key: "target", label: "目标", children: `${preview.result.target_title} (${preview.result.target_chat_id})` },
              { key: "bot", label: "机器人", children: preview.result.bot_id },
              { key: "total", label: "候选总数", children: preview.result.total },
              { key: "cache", label: "有缓存 / 无缓存", children: `${preview.result.with_cache} / ${preview.result.without_cache}` },
            ]} />
            <Alert type="warning" showIcon message="缓存可读仅表示权限检查通过，不是逐消息可恢复保证。历史相册元数据可能不完整。" />
            {preview.result.warnings?.map((warning, index) => <Alert key={index} type="warning" showIcon message={warning} />)}
            <DataTable rowKey="channel_id" density="compact" dataSource={preview.result.cache_channels ?? []} pagination={false} emptyText="没有已知缓存频道。" columns={[
              { title: "缓存频道", dataIndex: "channel_id" }, { title: "权限", dataIndex: "readable", render: (readable: boolean) => readable ? "可读（仅权限）" : "不可读" }, { title: "说明", dataIndex: "message" },
            ]} />
            <ResponsiveActionBar><Button type="primary" disabled={frozen || preview.result.total === 0} loading={create.pending} onClick={() => {
              // Both requests share the exact immutable scope. Later form edits cannot alter the confirmed input.
              const snapshot: RecoveryInput = { ...preview.input, filter: { ...preview.input.filter } };
              const result = preview.result;
              confirm({ intent: "default", title: "确认开始历史恢复", content: `将 ${result.total} 个候选恢复到 ${result.target_title}（${result.target_chat_id}），预检机器人 ${result.bot_id}。范围：来源 ${snapshot.filter.channel_key || "全部"}，用户 ${snapshot.filter.user_id || "全部"}，开始 ${snapshot.filter.since ? fmtTime(snapshot.filter.since) : "不限"}，结束（不含）${snapshot.filter.until ? fmtTime(snapshot.filter.until) : "不限"}。创建时服务端会复核权限并固定实际机器人，不保证全部成功。`, action: () => create.run(snapshot) });
            }}>开始恢复</Button></ResponsiveActionBar>
          </> : null}
        </Space>
      </PageSection>
      <PageSection title="恢复任务">
        <InlineQueryState pending={jobs.isFetching && !!jobs.data} text={jobs.isFetching && jobs.data ? "正在刷新任务列表…" : undefined} />
        <SectionQueryState initialLoading={jobs.isPending} error={jobs.error} hasData={!!jobs.data} onRetry={() => void jobs.refetch()}>
          <DataTable<RecoveryJob> rowKey="id" columns={jobColumns} dataSource={jobs.data?.items} emptyText="暂无恢复任务。" pagination={{ current: page, pageSize, total: jobs.data?.total ?? 0, onChange: (next, size) => { setPage(next); setPageSize(size); } }} />
        </SectionQueryState>
      </PageSection>
      {selectedId !== undefined ? <PageSection title={`任务 #${selectedId} 详情`}>
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
          <Select aria-label="逐项状态筛选" className="field-width-full" virtual={false} value={itemStatus ?? "all"} options={[{ value: "all", label: "全部项目状态" }, ...itemStatuses.map((status) => ({ value: status, label: labels[status] }))]} onChange={(value: RecoveryItemStatus | "all") => { setItemStatus(value === "all" ? undefined : value); setItemPage(1); }} />
          <InlineQueryState pending={items.isFetching && !!items.data} text={items.isFetching && items.data ? "正在刷新项目…" : undefined} />
          <SectionQueryState initialLoading={items.isPending} error={items.error} hasData={!!items.data} onRetry={() => void items.refetch()}>
            <DataTable<RecoveryItem> density="compact" rowKey="id" columns={itemColumns} dataSource={items.data?.items} emptyText="暂无符合条件的恢复项目。" pagination={{ current: itemPage, pageSize: itemPageSize, total: items.data?.total ?? 0, onChange: (next, size) => { setItemPage(next); setItemPageSize(size); } }} />
          </SectionQueryState>
          <Text type="secondary">每 5 秒刷新任务和项目。跨来源按来源键稳定排序，不代表原始全局时间顺序。</Text>
        </Space>
      </PageSection> : null}
    </PageScaffold>
  );
}
