/**
 * 监听记录页（/watch-events）：预热事件留痕——哪个 bot、在哪个源、转发
 * 了哪些消息（源消息直链）、走哪条路径（服务端复制 / 受保护重传）、缓存
 * 落点与关联请求。服务端分页；顶部按源筛选（选项来自监听源配置页数据）。
 * 事件在每次转储成功/回退入队时落 watch_events（internal/listener）。
 */
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import { Select, Space, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";

import { fetchWatchEvents, fetchWatchSources, type WatchEventRow } from "../../api/admin";
import { fmtTime } from "../../shared/format";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { DataTable } from "../shared/DataTable";

const { Text } = Typography;

export function WatchEventsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  // 支持统计页「按源排行」点击带 ?channel_id=... 直达筛选结果。
  const [channelFilter, setChannelFilter] = useState<number | undefined>(() => {
    const raw = searchParams.get("channel_id");
    if (!raw) return undefined;
    const parsed = Number(raw);
    return Number.isSafeInteger(parsed) && parsed !== 0 ? parsed : undefined;
  });
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);

  // 筛选选项来自监听源列表（含已删除源的历史事件：筛选只列当前源）
  const sources = useQuery({ queryKey: ["watch-sources"], queryFn: fetchWatchSources });
  const events = useQuery({
    queryKey: ["watch-events", { channel_id: channelFilter ?? 0, page, pageSize }],
    queryFn: () =>
      fetchWatchEvents({
        channel_id: channelFilter,
        page,
        page_size: pageSize,
      }),
  });

  const sourceOptions = (sources.data?.items ?? []).map((row) => ({
    value: row.channel_id,
    label: row.title || row.username || String(row.channel_id),
  }));

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
          <Tag color="green">服务端复制</Tag>
        ) : (
          <Space direction="vertical" size={0}>
            <Tag color="orange">重传管线</Tag>
            {row.request_id > 0 ? (
              <Link to={`/requests/${row.request_id}`}>请求 {row.request_id}</Link>
            ) : null}
          </Space>
        ),
    },
    {
      title: "Bot",
      dataIndex: "bot_id",
      width: 140,
      render: (v: number, row) =>
        v === 0 ? <Text type="secondary">—</Text> : (
          <Text code>{row.bot_username ? `@${row.bot_username}` : v}</Text>
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
  ];

  return (
    <PageScaffold
      title="监听记录"
      description="监听源转储的逐次留痕：哪个 Bot 在哪个源转发了哪些消息（含源消息直链）、走哪条路径、缓存落点与关联请求；受保护内容走重传管线时链接到对应请求行。"
    >
      <PageSection
        title="预热事件"
        extra={
          <Select
            allowClear
            placeholder="全部源"
            style={{ minWidth: 200 }}
            value={channelFilter}
            options={sourceOptions}
            onChange={(v) => {
              setChannelFilter(v);
              setPage(1);
              const next = new URLSearchParams(searchParams);
              if (v) next.set("channel_id", String(v));
              else next.delete("channel_id");
              setSearchParams(next, { replace: true });
            }}
            aria-label="按源筛选"
            loading={sources.isPending}
          />
        }
      >
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
