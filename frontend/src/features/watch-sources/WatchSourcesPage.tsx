/**
 * 监听源管理页（/watch-sources）：添加源频道/超级群组，并在同页处理邀请链接申请。
 * direct source 保持原有审批、暂停与删除流程；私有邀请链接先让读取账号加入，
 * Bot 管理员权限仍由管理员在 Telegram 中人工配置。
 * 两张表均支持「查询条件 + 查询按钮」（草稿态筛选，客户端过滤）；
 * 监听源列表支持批量操作（逐条复用单条端点，allSettled 汇总结果）。
 */
import { useState } from "react";
import type { Key } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { Button, Input, Select, Space, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";

import {
  fetchWatchSources,
  type WatchInviteRequestRow,
  type WatchSourceRow,
} from "../../api/admin";
import {
  addWatchSource,
  approveWatchInviteRequest,
  deleteWatchInviteRequest,
  deleteWatchSource,
  rejectWatchInviteRequest,
  retryWatchInviteRequest,
  reviewWatchSource,
  toggleWatchSource,
} from "../../api/mutations";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { DataTable } from "../shared/DataTable";
import { RowActions, type RowActionItem } from "../shared/RowActions";
import { StatusTag, type StatusTone } from "../shared/StatusTag";

const { Text } = Typography;

const STATUS_LABELS: Record<WatchSourceRow["status"], { label: string; color: string }> = {
  pending: { label: "待审批", color: "gold" },
  approved: { label: "生效中", color: "green" },
  rejected: { label: "已拒绝", color: "default" },
};

const SOURCE_STATUS_OPTIONS = [
  { value: "pending", label: "待审批" },
  { value: "approved", label: "生效中" },
  { value: "rejected", label: "已拒绝" },
];

const INVITE_STATUS_META: Record<
  WatchInviteRequestRow["status"],
  { label: string; tone: StatusTone; description: string }
> = {
  pending: { label: "待审批", tone: "warning", description: "等待管理员审批邀请链接" },
  waiting_telegram: {
    label: "等待读取账号加入",
    tone: "processing",
    description: "读取账号尚未完成加入，可稍后重试",
  },
  waiting_bot: {
    label: "等待 Bot 管理员权限",
    tone: "warning",
    description: "请先在 Telegram 中把 Bot 人工设为管理员，再重试",
  },
  approved: { label: "已通过", tone: "success", description: "邀请申请已转为监听源" },
  rejected: { label: "已拒绝", tone: "inactive", description: "管理员已拒绝该申请" },
  failed: { label: "处理失败", tone: "error", description: "上次处理失败，可修正后重试" },
};

const INVITE_STATUS_OPTIONS = Object.entries(INVITE_STATUS_META).map(([value, meta]) => ({
  value,
  label: meta.label,
}));

type InviteActionKind = "approve" | "reject" | "retry" | "delete";

/** 批量逐条执行的结果汇总。 */
interface BatchSummary {
  ok: number;
  fail: number;
}

function summarizeSettled(results: PromiseSettledResult<unknown>[]): BatchSummary {
  let ok = 0;
  let fail = 0;
  for (const r of results) {
    if (r.status === "fulfilled") ok += 1;
    else fail += 1;
  }
  return { ok, fail };
}

/** 关键词匹配：任一字段包含即命中（大小写不敏感；空关键词全通过）。 */
function matchesKeyword(keyword: string, fields: (string | number)[]): boolean {
  const k = keyword.trim().toLowerCase();
  if (!k) return true;
  return fields.some((f) => String(f ?? "").toLowerCase().includes(k));
}

export function WatchSourcesPage() {
  const [target, setTarget] = useState("");
  const [actingInvite, setActingInvite] = useState<{ id: number; kind: InviteActionKind } | null>(null);
  const confirm = useConfirmAction();

  // 监听源列表筛选（草稿态 + 「查询」生效）
  const [draftSourceStatus, setDraftSourceStatus] = useState<WatchSourceRow["status"] | undefined>();
  const [draftSourceKeyword, setDraftSourceKeyword] = useState("");
  const [sourceStatus, setSourceStatus] = useState<WatchSourceRow["status"] | undefined>();
  const [sourceKeyword, setSourceKeyword] = useState("");
  // 邀请申请筛选（同款交互）
  const [draftInviteStatus, setDraftInviteStatus] = useState<WatchInviteRequestRow["status"] | undefined>();
  const [draftInviteKeyword, setDraftInviteKeyword] = useState("");
  const [inviteStatus, setInviteStatus] = useState<WatchInviteRequestRow["status"] | undefined>();
  const [inviteKeyword, setInviteKeyword] = useState("");
  // 监听源批量选择
  const [selectedSourceKeys, setSelectedSourceKeys] = useState<Key[]>([]);

  const sources = useQuery({ queryKey: ["watch-sources"], queryFn: fetchWatchSources });

  const add = useAdminAction({
    action: (value: string) => addWatchSource(value),
    invalidate: [["watch-sources"]],
    successText: (result) => {
      if (result.source) {
        return `已添加监听源「${result.source.title || result.source.channel_id}」，新消息将自动预热缓存频道。`;
      }
      if (result.invite_request) {
        return "邀请链接申请已创建，请在下方完成审批和 Telegram 权限配置。";
      }
      return "监听源请求已提交。";
    },
    onDone: () => setTarget(""),
  });

  const review = useAdminAction({
    action: ({ id, approve }: { id: number; approve: boolean }) => reviewWatchSource(id, approve),
    invalidate: [["watch-sources"]],
    successText: (result, vars) =>
      vars.approve
        ? `已通过「${result.source?.title || vars.id}」的监听申请（申请人会收到通知）。`
        : `已拒绝「${result.source?.title || vars.id}」的监听申请（申请人会收到通知）。`,
  });

  const toggle = useAdminAction({
    action: ({ id, enabled }: { id: number; enabled: boolean }) => toggleWatchSource(id, enabled),
    invalidate: [["watch-sources"]],
    successText: (result, vars) =>
      vars.enabled
        ? `「${result.source?.title || vars.id}」已恢复监听。`
        : `「${result.source?.title || vars.id}」已暂停监听（配置保留）。`,
  });

  const remove = useAdminAction({
    action: (id: number) => deleteWatchSource(id),
    invalidate: [["watch-sources"]],
    successText: () => "监听源已删除（bot 仍在源内，如需退出用「移出 Bot」）。",
  });

  const inviteAction = useAdminAction({
    action: ({ id, kind }: { id: number; kind: InviteActionKind }) => {
      if (kind === "approve") return approveWatchInviteRequest(id);
      if (kind === "reject") return rejectWatchInviteRequest(id);
      if (kind === "retry") return retryWatchInviteRequest(id);
      return deleteWatchInviteRequest(id);
    },
    invalidate: [["watch-sources"]],
    successText: (_, vars) => {
      if (vars.kind === "approve") return "已开始处理邀请申请，请根据最新状态继续配置。";
      if (vars.kind === "reject") return "已拒绝邀请申请。";
      if (vars.kind === "retry") return "已重新处理邀请申请，请关注最新状态。";
      return "邀请申请记录已删除。";
    },
  });

  const runInviteAction = (id: number, kind: InviteActionKind) => {
    setActingInvite({ id, kind });
    return inviteAction.run({ id, kind }).finally(() => setActingInvite(null));
  };

  // ---- 监听源批量操作：逐条复用单条端点，allSettled 汇总 ----

  const batchReview = useAdminAction<BatchSummary, { ids: number[]; approve: boolean }>({
    action: ({ ids, approve }) =>
      Promise.allSettled(ids.map((id) => reviewWatchSource(id, approve))).then(summarizeSettled),
    invalidate: [["watch-sources"]],
    successText: (r, vars) =>
      r.fail === 0
        ? `批量${vars.approve ? "同意" : "拒绝"}完成（${r.ok} 条）。`
        : { type: "warning", text: `批量${vars.approve ? "同意" : "拒绝"}：成功 ${r.ok}，失败 ${r.fail}（仅待审批行可审批）。` },
    onDone: () => setSelectedSourceKeys([]),
  });

  const batchToggle = useAdminAction<BatchSummary, { ids: number[]; enabled: boolean }>({
    action: ({ ids, enabled }) =>
      Promise.allSettled(ids.map((id) => toggleWatchSource(id, enabled))).then(summarizeSettled),
    invalidate: [["watch-sources"]],
    successText: (r, vars) =>
      r.fail === 0
        ? `批量${vars.enabled ? "恢复" : "暂停"}完成（${r.ok} 条）。`
        : { type: "warning", text: `批量${vars.enabled ? "恢复" : "暂停"}：成功 ${r.ok}，失败 ${r.fail}（仅生效中行可切换）。` },
    onDone: () => setSelectedSourceKeys([]),
  });

  const batchDelete = useAdminAction<BatchSummary, { ids: number[] }>({
    action: ({ ids }) =>
      Promise.allSettled(ids.map((id) => deleteWatchSource(id))).then(summarizeSettled),
    invalidate: [["watch-sources"]],
    successText: (r) =>
      r.fail === 0
        ? `批量删除完成（${r.ok} 条）。`
        : { type: "warning", text: `批量删除：成功 ${r.ok}，失败 ${r.fail}。` },
    onDone: () => setSelectedSourceKeys([]),
  });

  const rows = sources.data?.items ?? [];
  const inviteRows = sources.data?.invite_requests ?? [];

  const filteredSources = rows.filter(
    (row) =>
      (!sourceStatus || row.status === sourceStatus) &&
      matchesKeyword(sourceKeyword, [
        row.title,
        row.username,
        row.channel_id,
        row.user_username,
        row.user_display_name,
        row.bot_username,
      ]),
  );
  const filteredInvites = inviteRows.filter(
    (row) =>
      (!inviteStatus || row.status === inviteStatus) &&
      matchesKeyword(inviteKeyword, [
        row.channel_title,
        row.masked_hash,
        row.user_username,
        row.user_display_name,
      ]),
  );

  const selectedSources = rows.filter((row) => selectedSourceKeys.includes(row.channel_id));
  const pendingIds = selectedSources.filter((r) => r.status === "pending").map((r) => r.channel_id);
  const pausableIds = selectedSources.filter((r) => r.status === "approved" && r.enabled).map((r) => r.channel_id);
  const resumableIds = selectedSources.filter((r) => r.status === "approved" && !r.enabled).map((r) => r.channel_id);

  const applySourceFilters = () => {
    setSourceStatus(draftSourceStatus);
    setSourceKeyword(draftSourceKeyword);
  };
  const resetSourceFilters = () => {
    setDraftSourceStatus(undefined);
    setDraftSourceKeyword("");
    setSourceStatus(undefined);
    setSourceKeyword("");
  };
  const applyInviteFilters = () => {
    setInviteStatus(draftInviteStatus);
    setInviteKeyword(draftInviteKeyword);
  };
  const resetInviteFilters = () => {
    setDraftInviteStatus(undefined);
    setDraftInviteKeyword("");
    setInviteStatus(undefined);
    setInviteKeyword("");
  };

  const columns: ColumnsType<WatchSourceRow> = [
    {
      title: "频道/群组",
      dataIndex: "title",
      render: (v: string, row) => (
        <Space direction="vertical" size={0}>
          <Space size={4}>
            <Text>{v || "（未知标题）"}</Text>
            {row.kind === "supergroup" ? (
              <Tag>超级群组</Tag>
            ) : row.kind === "channel" ? (
              <Tag color="cyan">频道</Tag>
            ) : null}
          </Space>
          <Text type="secondary" code>
            {row.username ? `@${row.username}` : row.channel_id}
          </Text>
        </Space>
      ),
    },
    {
      title: "状态",
      dataIndex: "status",
      render: (v: WatchSourceRow["status"], row) => {
        const meta = STATUS_LABELS[v];
        if (v !== "approved") return <Tag color={meta.color}>{meta.label}</Tag>;
        return row.enabled ? (
          <Tag color={meta.color}>生效中</Tag>
        ) : (
          <Tag color="orange">已暂停</Tag>
        );
      },
    },
    {
      title: "申请人",
      dataIndex: "added_by",
      render: (v: number, row) =>
        v === 0 ? (
          <Tag>管理员添加</Tag>
        ) : (
          <Space direction="vertical" size={0}>
            <Text>{row.user_display_name || row.user_username || "（已删除用户）"}</Text>
            {row.user_username ? <Text type="secondary">@{row.user_username}</Text> : null}
            <Link to={`/users/${v}`}>用户 {v}</Link>
          </Space>
        ),
    },
    {
      title: "受理 Bot",
      dataIndex: "bot_id",
      width: 180,
      render: (v: number, row) =>
        v === 0 ? (
          <Text type="secondary">—</Text>
        ) : (
          <span className="cell-nowrap">
            <Text code>{row.bot_username ? `@${row.bot_username}` : v}</Text>
          </span>
        ),
    },
    {
      title: "预热",
      key: "prewarm",
      render: (_, row) => (
        <Space direction="vertical" size={0}>
          <Text>{row.prewarm_count > 0 ? `${row.prewarm_count} 条` : "—"}</Text>
          {row.prewarm_last_at > 0 ? <Text type="secondary">{fmtTime(row.prewarm_last_at)}</Text> : null}
        </Space>
      ),
    },
    { title: "添加时间", dataIndex: "created_at", render: (v: number) => fmtTime(v) },
    {
      title: "操作",
      key: "actions",
      render: (_, row) => {
        const actions: RowActionItem[] = [];
        if (row.status === "pending") {
          actions.push({
            key: "approve",
            label: "同意",
            onClick: () => void review.run({ id: row.channel_id, approve: true }),
          });
          actions.push({
            key: "reject",
            label: "拒绝",
            danger: true,
            onClick: () => void review.run({ id: row.channel_id, approve: false }),
          });
        }
        if (row.status === "approved") {
          actions.push({
            key: "toggle",
            label: row.enabled ? "暂停" : "恢复",
            onClick: () => void toggle.run({ id: row.channel_id, enabled: !row.enabled }),
          });
        }
        actions.push({
          key: "delete",
          label: "删除",
          danger: true,
          onClick: () =>
            confirm({
              intent: "danger",
              title: "删除监听源",
              content: `确定删除「${row.title || row.channel_id}」？删除后新消息不再预热缓存（已缓存副本保留）。`,
              okText: "删除",
              action: () => remove.run(row.channel_id),
            }),
        });
        return <RowActions actions={actions} />;
      },
    },
  ];

  const inviteColumns: ColumnsType<WatchInviteRequestRow> = [
    {
      title: "邀请目标",
      key: "target",
      render: (_, row) => (
        <Space direction="vertical" size={0}>
          <Text>{row.channel_title || "（等待读取频道信息）"}</Text>
          <Text type="secondary" code>
            {row.channel_id ? row.channel_id : row.masked_hash}
          </Text>
          {row.participants > 0 ? <Text type="secondary">约 {row.participants} 名成员</Text> : null}
        </Space>
      ),
    },
    {
      title: "申请人",
      dataIndex: "user_id",
      render: (id: number, row) => (
        <Space direction="vertical" size={0}>
          <Text>{row.user_display_name || row.user_username || `用户 ${id}`}</Text>
          {row.user_username ? <Text type="secondary">@{row.user_username}</Text> : null}
          <Link to={`/users/${id}`}>用户 {id}</Link>
        </Space>
      ),
    },
    {
      title: "状态",
      dataIndex: "status",
      render: (status: WatchInviteRequestRow["status"], row) => {
        const meta = INVITE_STATUS_META[status];
        return (
          <Space direction="vertical" size={0}>
            <StatusTag tone={meta.tone}>{meta.label}</StatusTag>
            <Text type="secondary">{row.note || meta.description}</Text>
          </Space>
        );
      },
    },
    {
      title: "受理 Bot",
      dataIndex: "bot_id",
      render: (id: number, row) =>
        id ? <Text code>{row.bot_username ? `@${row.bot_username}` : id}</Text> : <Text type="secondary">待分配</Text>,
    },
    { title: "申请时间", dataIndex: "created_at", render: (v: number) => fmtTime(v) },
    {
      title: "操作",
      key: "actions",
      render: (_, row) => {
        const actions: RowActionItem[] = [];
        const busy = actingInvite?.id === row.id;
        if (row.status === "pending") {
          actions.push({
            key: "approve",
            label: "审批",
            primary: true,
            loading: busy && actingInvite?.kind === "approve",
            disabled: inviteAction.pending,
            onClick: () => void runInviteAction(row.id, "approve"),
          });
        }
        if (["waiting_telegram", "waiting_bot", "failed"].includes(row.status)) {
          actions.push({
            key: "retry",
            label: "重试",
            loading: busy && actingInvite?.kind === "retry",
            disabled: inviteAction.pending,
            onClick: () => void runInviteAction(row.id, "retry"),
          });
        }
        if (["pending", "waiting_telegram", "waiting_bot"].includes(row.status)) {
          actions.push({
            key: "reject",
            label: "拒绝",
            loading: busy && actingInvite?.kind === "reject",
            disabled: inviteAction.pending,
            onClick: () =>
              confirm({
                intent: "warning",
                title: "拒绝邀请申请",
                content: "确定拒绝该邀请链接申请？",
                action: () => runInviteAction(row.id, "reject"),
              }),
          });
        }
        actions.push({
          key: "delete",
          label: "删除",
          danger: true,
          loading: busy && actingInvite?.kind === "delete",
          disabled: inviteAction.pending,
          onClick: () =>
            confirm({
              intent: "danger",
              title: "删除邀请申请",
              content: "确定删除该邀请申请记录？删除后不可恢复。",
              okText: "删除",
              action: () => runInviteAction(row.id, "delete"),
            }),
        });
        return <RowActions actions={actions} />;
      },
    },
  ];

  return (
    <PageScaffold
      title="监听源管理"
      description="管理直接监听源与私有邀请链接申请；支持查询、审批、批量操作、权限配置、重试、暂停、恢复与删除。"
    >
      <Space direction="vertical" size="middle" className="field-width-full">
        <PageSection title="添加监听源">
          <Space direction="vertical" size="small" className="field-width-full">
            <Space.Compact className="field-width-full">
              <Input
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                placeholder="@用户名 / https://t.me/链接 / 邀请链接 / -100 数字 ID"
                aria-label="监听源目标"
              />
              <Button
                type="primary"
                loading={add.pending}
                disabled={target.trim() === ""}
                onClick={() => void add.run(target)}
              >
                验证并添加
              </Button>
            </Space.Compact>
            <Text type="secondary">
              公开源和数字 ID 添加前，请先把 Bot 加为该频道/群的管理员。私有邀请链接只会让读取账号加入目标，Bot
              仍须由管理员在 Telegram 中人工设为管理员，然后在邀请申请中重试。群组还需在 BotFather 关闭 privacy mode
              或保持管理员；受保护内容会自动改走系统账号下载重传管线。
            </Text>
          </Space>
        </PageSection>

        <PageQueryState
          initialLoading={sources.isPending && !sources.data}
          error={sources.isError && !sources.data}
          hasData={!!sources.data}
          onRetry={() => void sources.refetch()}
        >
          <Space direction="vertical" size="middle" className="field-width-full">
            <PageSection
              title={`邀请申请（${filteredInvites.length}）`}
              extra={
                <Space wrap>
                  <Select
                    allowClear
                    placeholder="全部状态"
                    className="filter-select"
                    value={draftInviteStatus}
                    options={INVITE_STATUS_OPTIONS}
                    onChange={(v) => setDraftInviteStatus(v)}
                    aria-label="邀请状态筛选"
                  />
                  <Input
                    allowClear
                    placeholder="标题 / 邀请码 / 申请人"
                    className="filter-input"
                    value={draftInviteKeyword}
                    onChange={(e) => setDraftInviteKeyword(e.target.value)}
                    onPressEnter={applyInviteFilters}
                    aria-label="邀请关键词筛选"
                  />
                  <Button type="primary" onClick={applyInviteFilters}>
                    查询
                  </Button>
                  <Button onClick={resetInviteFilters}>重置</Button>
                </Space>
              }
            >
              <DataTable
                rowKey="id"
                columns={inviteColumns}
                dataSource={filteredInvites}
                loading={sources.isFetching}
                emptyText="暂无邀请申请"
              />
            </PageSection>

            <PageSection
              title={`监听源列表（${filteredSources.length}）`}
              extra={
                <Space wrap>
                  <Select
                    allowClear
                    placeholder="全部状态"
                    className="filter-select"
                    value={draftSourceStatus}
                    options={SOURCE_STATUS_OPTIONS}
                    onChange={(v) => setDraftSourceStatus(v)}
                    aria-label="监听源状态筛选"
                  />
                  <Input
                    allowClear
                    placeholder="标题 / 用户名 / ID / 申请人 / Bot"
                    className="filter-input"
                    value={draftSourceKeyword}
                    onChange={(e) => setDraftSourceKeyword(e.target.value)}
                    onPressEnter={applySourceFilters}
                    aria-label="监听源关键词筛选"
                  />
                  <Button type="primary" onClick={applySourceFilters}>
                    查询
                  </Button>
                  <Button onClick={resetSourceFilters}>重置</Button>
                </Space>
              }
            >
              {selectedSourceKeys.length > 0 ? (
                <Space wrap className="batch-action-bar">
                  <Text>已选 {selectedSourceKeys.length} 项</Text>
                  {pendingIds.length > 0 ? (
                    <>
                      <Button
                        type="primary"
                        loading={batchReview.pending}
                        onClick={() =>
                          void batchReview.run({ ids: pendingIds, approve: true })
                        }
                      >
                        批量同意（{pendingIds.length}）
                      </Button>
                      <Button
                        danger
                        loading={batchReview.pending}
                        onClick={() =>
                          confirm({
                            intent: "warning",
                            title: "批量拒绝监听源",
                            content: `确定拒绝选中的 ${pendingIds.length} 条待审批申请？`,
                            action: () => batchReview.run({ ids: pendingIds, approve: false }),
                          })
                        }
                      >
                        批量拒绝（{pendingIds.length}）
                      </Button>
                    </>
                  ) : null}
                  {pausableIds.length > 0 ? (
                    <Button
                      loading={batchToggle.pending}
                      onClick={() =>
                        confirm({
                          intent: "warning",
                          title: "批量暂停监听",
                          content: `确定暂停选中的 ${pausableIds.length} 个生效中源？配置保留，可恢复。`,
                          action: () => batchToggle.run({ ids: pausableIds, enabled: false }),
                        })
                      }
                    >
                      批量暂停（{pausableIds.length}）
                    </Button>
                  ) : null}
                  {resumableIds.length > 0 ? (
                    <Button
                      loading={batchToggle.pending}
                      onClick={() => void batchToggle.run({ ids: resumableIds, enabled: true })}
                    >
                      批量恢复（{resumableIds.length}）
                    </Button>
                  ) : null}
                  <Button
                    danger
                    loading={batchDelete.pending}
                    onClick={() =>
                      confirm({
                        intent: "danger",
                        title: "批量删除监听源",
                        content: `确定删除选中的 ${selectedSources.length} 个监听源？删除后新消息不再预热缓存（已缓存副本保留）。`,
                        okText: "删除",
                        action: () =>
                          batchDelete.run({
                            ids: selectedSources.map((r) => r.channel_id),
                          }),
                      })
                    }
                  >
                    批量删除
                  </Button>
                  <Button onClick={() => setSelectedSourceKeys([])}>取消选择</Button>
                </Space>
              ) : null}
              <DataTable
                rowKey="channel_id"
                columns={columns}
                dataSource={filteredSources}
                loading={sources.isFetching}
                emptyText="暂无监听源"
                rowSelection={{
                  selectedRowKeys: selectedSourceKeys,
                  onChange: (keys) => setSelectedSourceKeys(keys),
                  getCheckboxProps: () => ({ disabled: sources.isFetching }),
                }}
              />
            </PageSection>
          </Space>
        </PageQueryState>
      </Space>
    </PageScaffold>
  );
}
