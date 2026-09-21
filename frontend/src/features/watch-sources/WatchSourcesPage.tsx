/**
 * 监听源页（/watch）：配置源频道/超级群组，bot 接收新帖并自动转储缓存频道
 * 预热 dump_entries——之后任何人提交该源的消息链接都直接命中复用秒回。
 * 三个分区：
 *  1. 添加：管理员直接添加（服务端校验 bot 须为源管理员；天然生效，
 *     不走审批与上限）。
 *  2. 列表：全部源与状态（待审批排最前）；pending 可同意/拒绝，approved
 *     可暂停/恢复，任意行可删除（danger 二次确认）。
 *  3. 申请设置：用户经 Bot /watch 自助申请的开关、审批模式与两项上限
 *     （即时生效；号主与管理员添加不受限）。
 * 受保护内容（禁止转发）的源会自动改走系统账号下载重传管线，无需在此配置。
 */
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { Button, Input, InputNumber, Space, Switch, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";

import { fetchSettings, fetchWatchSources, type WatchSourceRow } from "../../api/admin";
import {
  addWatchSource,
  deleteWatchSource,
  leaveWatchSource,
  reviewWatchSource,
  saveSettings,
  toggleWatchSource,
} from "../../api/mutations";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { FormActions } from "../shared/FormActions";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { DataTable } from "../shared/DataTable";
import { RowActions, type RowActionItem } from "../shared/RowActions";
import { SettingItem } from "../shared/SettingItem";

const { Text } = Typography;

const STATUS_LABELS: Record<WatchSourceRow["status"], { label: string; color: string }> = {
  pending: { label: "待审批", color: "gold" },
  approved: { label: "生效中", color: "green" },
  rejected: { label: "已拒绝", color: "default" },
};

export function WatchSourcesPage() {
  const [target, setTarget] = useState("");
  const [maxSources, setMaxSources] = useState<number | null>(null);
  const [perUser, setPerUser] = useState<number | null>(null);
  const confirm = useConfirmAction();

  const settings = useQuery({ queryKey: ["settings"], queryFn: fetchSettings });
  const sources = useQuery({ queryKey: ["watch-sources"], queryFn: fetchWatchSources });

  const add = useAdminAction({
    action: (value: string) => addWatchSource(value),
    invalidate: [["watch-sources"]],
    successText: (result) =>
      `已添加监听源「${result.source?.title || result.source?.channel_id}」，新消息将自动预热缓存频道。`,
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

  const leave = useAdminAction({
    action: (id: number) => leaveWatchSource(id),
    invalidate: [["watch-sources"]],
    successText: (result) =>
      result.outcome.failed > 0
        ? {
            type: "warning",
            text: `监听源已删除；${result.outcome.left} 个 bot 已退出，${result.outcome.failed} 个退出失败（可能本就不在该源内）。`,
          }
        : `已把 ${result.outcome.left} 个 bot 移出该源并删除监听源。`,
  });

  const saveApply = useAdminAction({
    action: (enabled: boolean) => saveSettings({ watch_apply_enabled: enabled }),
    invalidate: [["settings"]],
    successText: (result) =>
      result.settings.watch_apply_enabled
        ? "用户自助申请已开放（按当前审批模式与上限执行）。"
        : "用户自助申请已关闭（管理员添加不受影响）。",
  });

  const saveApproval = useAdminAction({
    action: (enabled: boolean) => saveSettings({ watch_require_approval: enabled }),
    invalidate: [["settings"]],
    successText: (result) =>
      result.settings.watch_require_approval
        ? "用户申请需审批后生效。"
        : "用户申请免审批，直接生效。",
  });

  const saveLimits = useAdminAction({
    action: (input: { max: number; perUser: number }) =>
      saveSettings({ watch_max_sources: input.max, watch_per_user_limit: input.perUser }),
    invalidate: [["settings"]],
    successText: () => "监听源上限已更新，即时生效。",
  });

  const rows = sources.data?.items ?? [];
  const cfg = settings.data;
  const limitsDirty =
    maxSources != null && perUser != null && cfg != null &&
    (maxSources !== cfg.watch_max_sources || perUser !== cfg.watch_per_user_limit);

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
      render: (v: number, row) =>
        v === 0 ? (
          <Text type="secondary">—</Text>
        ) : (
          <Text code>{row.bot_username ? `@${row.bot_username}` : v}</Text>
        ),
    },
    {
      title: "预热",
      key: "prewarm",
      render: (_, row) => (
        <Space direction="vertical" size={0}>
          <Text>{row.prewarm_count > 0 ? `${row.prewarm_count} 条` : "—"}</Text>
          {row.prewarm_last_at > 0 ? (
            <Text type="secondary">{fmtTime(row.prewarm_last_at)}</Text>
          ) : null}
        </Space>
      ),
    },
    {
      title: "添加时间",
      dataIndex: "created_at",
      render: (v: number) => fmtTime(v),
    },
    {
      title: "操作",
      key: "actions",
      render: (_, row) => {
        const actions: RowActionItem[] = [];
        if (row.status === "pending") {
          actions.push({
            key: "approve",
            label: "同意",
            danger: false,
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
            danger: false,
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
              content: `确定删除「${row.title || row.channel_id}”？删除后新消息不再预热缓存（已缓存副本保留）。`,
              okText: "删除",
              action: () => remove.run(row.channel_id),
            }),
        });
        return <RowActions actions={actions} />;
      },
    },
  ];

  return (
    <PageScaffold
      title="监听源配置"
      description="配置源频道/超级群组：bot 接收新帖并自动转存到缓存频道预热，之后任何人提交该源的消息链接都直接复制副本秒回（不限大小、相册保组）。"
    >
      <Space direction="vertical" size="middle" className="field-width-full">
        <PageSection title="添加监听源">
          <Space direction="vertical" size="small" className="field-width-full">
            <Space.Compact className="field-width-full">
              <Input
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                placeholder="@用户名 / https://t.me/链接 / -100 数字 ID（频道或超级群组）"
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
              添加前请先把机器人加为该频道/群的管理员（管理员身份保证能收到全部消息；群组还需在
              BotFather 关闭 privacy mode 或保持管理员）。支持多个监听源；受保护内容（禁止转发）的源会自动改走系统账号下载重传管线。
            </Text>
          </Space>
        </PageSection>

        <PageSection title={`监听源列表（${rows.length}）`}>
          <PageQueryState
            initialLoading={sources.isPending && !sources.data}
            error={sources.isError && !sources.data}
            hasData={!!sources.data}
            onRetry={() => void sources.refetch()}
          >
            <DataTable
              rowKey="channel_id"
              columns={columns}
              dataSource={rows}
              loading={sources.isPending}
              emptyText="暂无监听源"
            />
          </PageQueryState>
        </PageSection>

        <PageSection title="用户自助申请（Bot /watch）" extra={<Tag color="green">切换即保存</Tag>}>
          <Space direction="vertical" size="small" className="field-width-full">
            <SettingItem
              control={
                <Switch
                  checked={cfg?.watch_apply_enabled ?? false}
                  loading={saveApply.pending}
                  onChange={(enabled) => void saveApply.run(enabled)}
                  aria-label="用户自助申请开关"
                />
              }
              label="允许用户经 /watch 自助申请监听源"
              description="仅已通过 /start 审核的用户可申请；管理员在上方直接添加不受此开关限制。默认关闭。"
            />
            <SettingItem
              control={
                <Switch
                  checked={cfg?.watch_require_approval ?? true}
                  loading={saveApproval.pending}
                  onChange={(enabled) => void saveApproval.run(enabled)}
                  aria-label="申请审批开关"
                />
              }
              label="用户申请需审批后生效"
              description="开启后申请进入上方列表「待审批」；关闭则免审批直接生效。号主经 Bot 提交始终直接生效。"
            />
            <Space size="large">
              <SettingItem
                control={
                  <InputNumber
                    value={maxSources ?? cfg?.watch_max_sources ?? 20}
                    min={0}
                    max={200}
                    precision={0}
                    onChange={(v) => setMaxSources(v)}
                    aria-label="监听源总数上限"
                  />
                }
                label="总数上限（0 不限）"
              />
              <SettingItem
                control={
                  <InputNumber
                    value={perUser ?? cfg?.watch_per_user_limit ?? 3}
                    min={0}
                    max={20}
                    precision={0}
                    onChange={(v) => setPerUser(v)}
                    aria-label="每用户申请上限"
                  />
                }
                label="每用户上限（0 不限）"
              />
            </Space>
            <FormActions>
              <Button
                loading={saveLimits.pending}
                disabled={!limitsDirty}
                onClick={() => {
                  const max = maxSources ?? cfg?.watch_max_sources ?? 20;
                  const per = perUser ?? cfg?.watch_per_user_limit ?? 3;
                  void saveLimits.run({ max, perUser: per });
                }}
              >
                保存上限
              </Button>
            </FormActions>
            <Text type="secondary">
              上限只约束用户申请（含待审批），管理员添加与号主提交不受限；0 表示不限制。即时生效。
            </Text>
          </Space>
        </PageSection>
      </Space>
    </PageScaffold>
  );
}
