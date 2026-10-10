/**
 * 历史恢复列表页（/recovery）：恢复范围表单、只读预检与任务列表。
 * 详情在独立路由 /recovery/jobs/:id（RecoveryJobDetailPage）。
 * 空闲（无运行中任务）时停止自动刷新；创建成功后跳转新任务详情。
 */
import { useQuery } from "@tanstack/react-query";
import { BookOutlined } from "@ant-design/icons";
import { Alert, AutoComplete, Button, DatePicker, Descriptions, Form, Select, Space } from "antd";
import type { ColumnsType } from "antd/es/table";
import type { Dayjs } from "dayjs";
import { useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";

import { PROJECT_IDENTITY } from "../../shared/projectIdentity.generated";
import { fetchBots, fetchChannels, fetchChannelBindings, fetchUsers, fetchWatchSources, type ChannelBindingRow, type ChannelRow, type UserRow, type WatchSourceRow } from "../../api/admin";
import { createRecoveryJob, fetchRecoveryJobs, previewRecovery, type RecoveryInput, type RecoveryJob, type RecoveryPreview } from "../../api/recovery";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { PageScaffold, PageSection, ResponsiveActionBar } from "../shared/PageLayout";
import { InlineQueryState, SectionQueryState } from "../shared/QueryStates";
import { counts, recoveryJobsPollInterval, statusTag } from "./shared";

interface FormValues {
  channel_key?: string;
  user_id?: string | number | null;
  since?: Dayjs | null;
  until?: Dayjs | null;
  target: string;
  bot_id: number;
}
export function recoveryInput(values: FormValues): RecoveryInput {
  return {
    filter: { channel_key: values.channel_key?.trim() ?? "", user_id: Number(values.user_id) || 0, since: values.since?.valueOf() ?? 0, until: values.until?.valueOf() ?? 0 },
    target: values.target.trim(), bot_id: values.bot_id ?? 0,
  };
}

type RecoveryOption = { value: string; label: string };
// 使用指南「已处理历史恢复」小节：表单操作、边界与换机器人方案的唯一权威说明。
export const recoveryDocsUrl = `${PROJECT_IDENTITY.pagesUrl}guide/usage.html#_8-4-已处理历史恢复`;
// 下拉过滤同时匹配值与名称；@/-100 前缀不参与比较，输入 ID 片段也能命中。
export function recoveryOptionFilter(input: string, option?: { value?: string | number; label?: unknown }) {
  const needle = input.trim().toLowerCase().replace(/^@/, "").replace(/^-100/, "");
  if (!needle) return true;
  const value = String(option?.value ?? "");
  const label = typeof option?.label === "string" ? option.label : "";
  return `${value} ${label}`.toLowerCase().replace(/^-100/, "").includes(needle);
}
type RecoverySourceInput = {
  watchSources?: Pick<WatchSourceRow, "channel_id" | "kind" | "username" | "title" | "status">[];
  bindings?: Pick<ChannelBindingRow, "channel_id" | "username" | "title" | "status">[];
  channels?: Pick<ChannelRow, "key">[];
};
// 来源枚举 = 生效监听源（带类型）+ 有效绑定 + 提取历史键；按值去重。
export function buildRecoverySourceOptions({ watchSources = [], bindings = [], channels = [] }: RecoverySourceInput): RecoveryOption[] {
  const out: RecoveryOption[] = [];
  const seen = new Set<string>();
  const push = (value: string, label: string) => {
    const v = value.trim();
    if (!v || seen.has(v)) return;
    seen.add(v);
    out.push({ value: v, label });
  };
  for (const source of watchSources) {
    if (source.status !== "approved") continue;
    const kind = source.kind === "supergroup" ? "超级群组" : source.kind === "channel" ? "频道" : "来源";
    push(String(source.channel_id), `${source.title || source.username || source.channel_id} · ${kind} · 监听源`);
  }
  for (const binding of bindings) {
    if (binding.status !== "active") continue;
    push(String(binding.channel_id), `${binding.title || binding.username || binding.channel_id} · 绑定频道`);
  }
  for (const channel of channels) push(channel.key, `${channel.key} · 提取历史来源`);
  return out;
}
export function buildRecoveryTargetOptions(bindings: Pick<ChannelBindingRow, "channel_id" | "username" | "title" | "status">[] = []): RecoveryOption[] {
  return bindings.filter((binding) => binding.status === "active").map((binding) => ({
    value: String(binding.channel_id),
    label: `${binding.title || binding.username || binding.channel_id}${binding.username ? `（@${binding.username}）` : `（${binding.channel_id}）`}`,
  }));
}
export function buildRecoveryUserOptions(users: Pick<UserRow, "id" | "username" | "display_name">[] = []): RecoveryOption[] {
  return users.map((user) => ({
    value: String(user.id),
    label: `${user.display_name || user.username || user.id}${user.username ? `（@${user.username}）` : ""} · ${user.id}`,
  }));
}

export function RecoveryPage() {
  const [form] = Form.useForm<FormValues>();
  const [preview, setPreview] = useState<{ input: RecoveryInput; result: RecoveryPreview }>();
  const revision = useRef(0);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const navigate = useNavigate();
  const confirm = useConfirmAction();
  const bots = useQuery({ queryKey: ["bots"], queryFn: fetchBots });
  // 表单枚举：监听源/绑定频道（带名称与类型）、提取历史来源、用户管理名单。
  // 任一枚举查询失败只影响下拉建议，输入能力不受影响。
  const bindings = useQuery({ queryKey: ["channel-bindings"], queryFn: () => fetchChannelBindings() });
  const watchSources = useQuery({ queryKey: ["watch-sources"], queryFn: fetchWatchSources });
  const channelStats = useQuery({ queryKey: ["channels", "recovery-options"], queryFn: () => fetchChannels({ page_size: 200 }) });
  const users = useQuery({ queryKey: ["users", "recovery-options"], queryFn: () => fetchUsers({ status: "enabled", page_size: 200 }) });
  const sourceOptions = useMemo(() => buildRecoverySourceOptions({ watchSources: watchSources.data?.items, bindings: bindings.data?.items, channels: channelStats.data?.items }), [bindings.data, channelStats.data, watchSources.data]);
  const targetOptions = useMemo(() => buildRecoveryTargetOptions(bindings.data?.items), [bindings.data]);
  const userOptions = useMemo(() => buildRecoveryUserOptions(users.data?.items), [users.data]);
  const jobs = useQuery({ queryKey: ["recovery", "jobs", page, pageSize], queryFn: () => fetchRecoveryJobs(page, pageSize), refetchInterval: recoveryJobsPollInterval });
  const preflight = useAdminAction({
    action: async (vars: { input: RecoveryInput; revision: number }) => ({ ...vars, result: await previewRecovery(vars.input) }),
    successText: "预检完成；权限检查不代表每条历史都可恢复。",
    onDone: (data) => { if (data.revision === revision.current) setPreview({ input: data.input, result: data.result }); },
  });
  const create = useAdminAction({
    action: (input: RecoveryInput) => createRecoveryJob(input), invalidate: [["recovery"]], successText: "已创建恢复任务。",
    onDone: (job) => { setPage(1); setPreview(undefined); void navigate(`/recovery/jobs/${job.id}`); },
  });
  const frozen = preflight.pending || create.pending;
  const jobColumns: ColumnsType<RecoveryJob> = [
    { title: "任务", dataIndex: "id", render: (id: number) => <Button type="link" onClick={() => void navigate(`/recovery/jobs/${id}`)}>任务 #{id}</Button> },
    { title: "状态", dataIndex: "status", render: statusTag },
    { title: "目标", render: (_, job) => `${job.target_title} (${job.target_chat_id})` },
    { title: "机器人", dataIndex: "bot_id" },
    { title: "进度", render: (_, job) => counts(job) },
    { title: "创建时间", dataIndex: "created_at", render: fmtTime },
    { title: "更新时间", dataIndex: "updated_at", render: fmtTime },
  ];
  return (
    <PageScaffold
      title="历史恢复"
      description="仅恢复系统已处理的历史，不扫描原频道全部历史，不改变原记录或频道绑定。"
      actions={<Button href={recoveryDocsUrl} target="_blank" rel="noreferrer" icon={<BookOutlined />}>操作文档</Button>}
    >
      <PageSection title="恢复范围与目标">
        <Space direction="vertical" size="middle" className="field-width-full">
          <Alert type="info" showIcon message="请提前创建目标频道或非论坛超级群组，加入执行机器人并授予发帖权限。" description="公开频道可填 @username；私有频道可填 -100 开头的 ID。空来源、用户和时间表示全部已处理历史。预检只检查元数据与权限，不发送测试消息，也不保证所有历史可恢复。" />
          <SectionQueryState initialLoading={bots.isPending} error={bots.error} hasData={!!bots.data} onRetry={() => void bots.refetch()}>
            {bots.data ? <Form form={form} layout="vertical" initialValues={{ bot_id: 0 }} disabled={frozen} onValuesChange={() => { revision.current += 1; setPreview(undefined); }} onFinish={(values: FormValues) => {
              const input = recoveryInput(values);
              setPreview(undefined);
              void preflight.run({ input, revision: revision.current });
            }}>
              <Form.Item name="channel_key" label="来源频道" extra="可下拉选择监听源 / 绑定频道 / 提取历史来源，或直接输入 @用户名、-100 ID；留空为全部来源。"><AutoComplete className="field-width-full" options={sourceOptions} filterOption={recoveryOptionFilter} placeholder="@source 或 -1001234567890" /></Form.Item>
              <Form.Item name="user_id" label="用户 ID" rules={[{ validator: (_, value: FormValues["user_id"]) => value === undefined || value === null || String(value).trim() === "" || /^\d+$/.test(String(value).trim()) ? Promise.resolve() : Promise.reject(new Error("请输入正整数用户 ID，或留空。")) }]}><AutoComplete className="field-width-full" options={userOptions} filterOption={recoveryOptionFilter} placeholder="下拉选择用户或直接输入 ID；留空为全部用户" /></Form.Item>
              <Form.Item name="since" label="开始时间（包含）"><DatePicker showTime className="field-width-full" /></Form.Item>
              <Form.Item name="until" label="结束时间（不包含）" dependencies={["since"]} rules={[({ getFieldValue }) => ({ validator: (_, value: Dayjs | null) => !value || !getFieldValue("since") || value.valueOf() > (getFieldValue("since") as Dayjs).valueOf() ? Promise.resolve() : Promise.reject(new Error("结束时间必须晚于开始时间。")) })]}><DatePicker showTime className="field-width-full" /></Form.Item>
              <Form.Item name="target" label="目标频道 / 超级群组" rules={[{ required: true, whitespace: true, message: "请输入提前创建的目标。" }]}><AutoComplete className="field-width-full" options={targetOptions} filterOption={recoveryOptionFilter} placeholder="下拉选择已绑定频道，或输入新目标 @target / -1001234567890" /></Form.Item>
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
            {preview.result.warnings.map((warning) => <Alert key={warning} type="warning" showIcon message={warning} />)}
            <DataTable<RecoveryPreview["cache_channels"][number]> rowKey="channel_id" columns={[
              { title: "缓存频道", dataIndex: "channel_id" },
              { title: "权限", render: (_, channel) => channel.readable ? "可读（仅权限）" : "不可读" },
              { title: "说明", dataIndex: "message" },
            ]} dataSource={preview.result.cache_channels} emptyText="预检未发现已知缓存频道。" />
            <ResponsiveActionBar><Button type="primary" disabled={frozen} onClick={() => {
              if (!preview) return;
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
    </PageScaffold>
  );
}
