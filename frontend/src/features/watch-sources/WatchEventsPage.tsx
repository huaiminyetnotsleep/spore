/**
 * 监听记录页（/watch-events）：预热事件留痕——哪个 bot、在哪个源、转发
 * 了哪些消息（源消息直链）、走哪条路径（服务端复制 / 受保护重传）、缓存
 * 落点与关联请求。服务端分页；按源/按方式筛选（草稿态 + 「查询」按钮生
 * 效，「重置」恢复全部）。支持单条与批量删除留痕（不影响缓存副本）。
 * 事件在每次转储成功/回退入队时落 watch_events（internal/listener）。
 */
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { Button, Select, Space, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";

import { fetchWatchEvents, fetchWatchSources, type WatchEventRow } from "../../api/admin";
import { deleteWatchEvents } from "../../api/mutations";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { DataTable } from "../shared/DataTable";
import { RowActions, type RowActionItem } from "../shared/RowActions";

const { Text } = Typography;

const PATH_OPTIONS = [
  { value: "copy", label: "服务端复制" },
  { value: "fallback", label: "重传管线" },
];

const PATH_LABELS: Record<WatchEventRow["path"], string> = {
  copy: "服务端复制",
  fallback: "重传管线",
};

export function WatchEventsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const confirm = useConfirmAction();
  // 支持统计页「按源排行」点击带 ?channel_id=... 直达筛选结果。
  const initialChannel = (() => {
    const raw = searchParams.get("channel_id");
    if (!raw) return undefined;
    const parsed = Number(raw);
    return Number.isSafeInteger(parsed) && parsed !== 0 ? parsed : undefined;
  })();
  // 筛选为草稿态：调整下拉不立即生效，点「查询」才应用
  const [draftChannel, setDraftChannel] = useState<number | undefined>(initialChannel);
  const [draftPath, setDraftPath] = useState<WatchEventRow["path"] | undefined>();
  const [channelFilter, setChannelFilter] = useState<number | undefined>(initialChannel);
  const [pathFilter, setPathFilter] = useState<WatchEventRow["path"] | undefined>();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [selectedKeys, setSelectedKeys] = useState<React.Key[]>([]);

  // 筛选选项来自监听源列表（含已删除源的历史事件：筛选只列当前源）
  const sources = useQuery({ queryKey: ["watch-sources"], queryFn: fetchWatchSources });
  const events = useQuery({
    queryKey: ["watch-events", { channel_id: channelFilter ?? 0, path: pathFilter ?? "", page, pageSize }],
    queryFn: () =>
      fetchWatchEvents({
        channel_id: channelFilter,
        path: pathFilter,
        page,
        page_size: pageSize,
      }),
  });

  const removeEvents = useAdminAction({
    action: (ids: number[]) => deleteWatchEvents(ids),
    invalidate: [["watch-events"]],
    successText: (result) => `已删除 ${result.deleted} 条预热事件（缓存副本保留）。`,
    onDone: () => setSelectedKeys([]),
  });

  // 条件未变化时 queryKey 不变、不会自动重新请求；点「查询/重置」是明确的
  // 刷新意图，条件相同也强制 refetch（不在第一页时重置页码本身即触发新查询）。
  const applyFilters = () => {
    const unchanged = draftChannel === channelFilter && draftPath === pathFilter;
    setChannelFilter(draftChannel);
    setPathFilter(draftPath);
    setPage(1);
    if (unchanged && page === 1) void events.refetch();
    const next = new URLSearchParams(searchParams);
    if (draftChannel) next.set("channel_id", String(draftChannel));
    else next.delete("channel_id");
    setSearchParams(next, { replace: true });
  };

  const resetFilters = () => {
    const unchanged = channelFilter === undefined && pathFilter === undefined;
    setDraftChannel(undefined);
    setDraftPath(undefined);
    setChannelFilter(undefined);
    setPathFilter(undefined);
    setPage(1);
    if (unchanged && page === 1) void events.refetch();
    const next = new URLSearchParams(searchParams);
    next.delete("channel_id");
    setSearchParams(next, { replace: true });
  };

  const sourceOptions = (sources.data?.items ?? []).map((row) => ({
    value: row.channel_id,
    label: row.title || row.username || String(row.channel_id),
  }));

  const deleteSingle = (row: WatchEventRow) => {
    confirm({
      intent: "danger",
      title: "删除预热事件",
      content: `确定删除消息 ${row.message_id} 的预热留痕？已缓存副本保留，仅删除本条记录。`,
      okText: "删除",
      action: () => removeEvents.run([row.id]),
    });
  };

  const deleteBatch = () => {
    const ids = selectedKeys.map(Number);
    confirm({
      intent: "danger",
      title: "批量删除预热事件",
      content: `确定删除选中的 ${ids.length} 条预热留痕？已缓存副本保留，仅删除记录。`,
      okText: "删除",
      action: () => removeEvents.run(ids),
    });
  };

  const columns: ColumnsType<WatchEventRow> = [
    {
      title: "时间",
      dataIndex: "created_at",
      width: 170,
      render: (v: number) => fmtTime(v),
    },
    {
      title: "源",
      dataIndex: "title",
      render: (v: string, row) => (
        <Space direction="vertical" size={0}>
          <Text>{v || "（未知）"}</Text>
          <Text type="secondary" code>
            {row.username ? `@${row.username}` : row.channel_id}
          </Text>
        </Space>
      ),
    },
    {
      title: "消息",
      dataIndex: "message_id",
      render: (v: number, row) => (
        <Space direction="vertical" size={0}>
          {row.message_url ? (
            <a href={row.message_url} target="_blank" rel="noreferrer">
              消息 {v}
            </a>
          ) : (
            <Text>消息 {v}</Text>
          )}
          {row.member_ids.length > 1 ? (
            <Text type="secondary">相册 {row.member_ids.length} 条</Text>
          ) : null}
        </Space>
      ),
    },
    {
      title: "方式",
      dataIndex: "path",
      width: 130,
      render: (v: WatchEventRow["path"], row) =>
        v === "copy" ? (
          <Tag color="green">{PATH_LABELS[v]}</Tag>
        ) : (
          <Space direction="vertical" size={0}>
            <Tag color="orange">{PATH_LABELS[v]}</Tag>
            {row.request_id > 0 ? (
              <Link to={`/requests/${row.request_id}`}>请求 {row.request_id}</Link>
            ) : null}
          </Space>
        ),
    },
    {
      title: "Bot",
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
      title: "缓存落点",
      dataIndex: "dump_ids",
      width: 110,
      render: (ids: number[], row) =>
        row.path === "fallback" ? (
          <Text type="secondary">随请求写入</Text>
        ) : ids.length > 0 ? (
          <Text>{ids.length} 条</Text>
        ) : (
          <Text type="secondary">—</Text>
        ),
    },
    {
      title: "操作",
      key: "actions",
      width: 90,
      render: (_, row) => {
        const actions: RowActionItem[] = [
          {
            key: "delete",
            label: "删除",
            danger: true,
            onClick: () => deleteSingle(row),
          },
        ];
        return <RowActions actions={actions} />;
      },
    },
  ];

  return (
    <PageScaffold
      title="监听记录"
      description="监听源转储的逐次留痕：哪个 Bot 在哪个源转发了哪些消息（含源消息直链）、走哪条路径、缓存落点与关联请求；受保护内容走重传管线时链接到对应请求行。"
    >
      <PageSection
        title="预热事件"
        extra={
          <Space wrap>
            <Select
              allowClear
              placeholder="全部源"
              className="filter-select"
              value={draftChannel}
              options={sourceOptions}
              onChange={(v) => setDraftChannel(v)}
              aria-label="按源筛选"
              loading={sources.isPending}
            />
            <Select
              allowClear
              placeholder="全部方式"
              className="filter-select"
              value={draftPath}
              options={PATH_OPTIONS}
              onChange={(v) => setDraftPath(v)}
              aria-label="按方式筛选"
            />
            <Button type="primary" onClick={applyFilters}>
              查询
            </Button>
            <Button onClick={resetFilters}>重置</Button>
          </Space>
        }
      >
        {selectedKeys.length > 0 ? (
          <Space wrap className="batch-action-bar">
            <Text>已选 {selectedKeys.length} 条</Text>
            <Button danger onClick={deleteBatch}>
              批量删除
            </Button>
            <Button onClick={() => setSelectedKeys([])}>取消选择</Button>
          </Space>
        ) : null}
        <PageQueryState
          initialLoading={events.isPending && !events.data}
          error={events.isError && !events.data}
          hasData={!!events.data}
          onRetry={() => void events.refetch()}
        >
          <DataTable
            rowKey="id"
            columns={columns}
            dataSource={events.data?.items ?? []}
            loading={events.isPending}
            emptyText="暂无预热事件（源内新消息转储后在此留痕）"
            rowSelection={{
              selectedRowKeys: selectedKeys,
              onChange: (keys) => setSelectedKeys(keys),
            }}
            pagination={{
              current: events.data?.page ?? page,
              pageSize: events.data?.page_size ?? pageSize,
              total: events.data?.total ?? 0,
              showSizeChanger: true,
              onChange: (nextPage, nextSize) => {
                setPage(nextPage);
                setPageSize(nextSize);
              },
            }}
          />
        </PageQueryState>
      </PageSection>
    </PageScaffold>
  );
}
