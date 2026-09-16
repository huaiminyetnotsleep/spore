/**
 * 频道统计列表页（SSR /channels 的 SPA 对应实现）。
 * 时间范围筛选 + 服务端分页 + CSV 导出；数据全部来自请求记录聚合，
 * 不触发任何频道访问。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, DatePicker, Form, Select, Space, Table, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import dayjs, { type Dayjs } from "dayjs";
import { useState } from "react";
import { Link } from "react-router-dom";

import { buildExportURL, fetchBots, fetchChannels, type ChannelRow } from "../../api/admin";
import { deleteChannelRequests } from "../../api/mutations";
import { botLabel, fmtRate, fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { applyListFilters } from "../shared/listFilters";
import { LoadError, PageCard } from "../shared/PageStates";

const { Text } = Typography;

interface ChannelFormValues {
  since?: Dayjs | null;
  until?: Dayjs | null;
  bot_id?: string;
}

interface ChannelQuery {
  since?: string;
  until?: string;
  bot_id?: string;
}

export function ChannelsListPage() {
  const [form] = Form.useForm<ChannelFormValues>();
  const [filters, setFilters] = useState<ChannelQuery>({});
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const confirm = useConfirmAction();

  // 机器人池列表：机器人筛选下拉选项；查询失败不阻塞列表。
  const bots = useQuery({ queryKey: ["bots"], queryFn: fetchBots });
  const botOptions = (bots.data?.bots ?? []).map((bot) => ({
    value: String(bot.bot_id),
    label: botLabel(bot.bot_id, bot.username),
  }));

  // 频道是请求记录的纯聚合：删除即清空该频道全部记录行，用户累计与
  // 总览统计随之变化，相关 query 一并失效
  const remove = useAdminAction({
    action: (key: string) => deleteChannelRequests(key),
    invalidate: [["requests"], ["channels"], ["users"], ["overview"]],
    successText: (result) => `已删除该频道的 ${result.deleted} 条请求记录。`,
  });

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["channels", "list", { ...filters, page, pageSize }],
    queryFn: () => fetchChannels({ ...filters, page, page_size: pageSize }),
  });

  const columns: ColumnsType<ChannelRow> = [
    {
      title: "频道",
      dataIndex: "key",
      key: "key",
      render: (key: string) => <Link to={`/channels/${encodeURIComponent(key)}`}>{key}</Link>,
    },
    { title: "请求量", dataIndex: "total", key: "total", align: "right" },
    { title: "成功", dataIndex: "succeeded", key: "succeeded", align: "right" },
    { title: "失败", dataIndex: "failed", key: "failed", align: "right" },
    {
      title: "成功率",
      key: "rate",
      align: "right",
      render: (_, row) => fmtRate(row.success_rate, row.succeeded + row.failed),
    },
    { title: "最近请求", dataIndex: "last_requested_at", key: "last", render: fmtTime },
    {
      title: "操作",
      key: "actions",
      fixed: "right",
      width: 90,
      render: (_, row) => (
        <Button
          size="small"
          danger
          loading={remove.pending}
          disabled={remove.pending}
          onClick={() =>
            confirm(
              `确定删除频道 ${row.key}？将删除该频道的全部 ${row.total} 条请求记录（含成功与失败），删除后不可恢复且不再参与统计。仍有未完成请求时将被拒绝。`,
              () => {
                void remove.run(row.key);
              },
            )
          }
        >
          删除
        </Button>
      ),
    },
  ];

  return (
    <PageCard
      title="频道统计"
      extra={<Button href={buildExportURL("/channels/export.csv", { ...filters })}>导出 CSV</Button>}
    >
      <Space direction="vertical" size="middle" className="field-width-full">
        <Form
          form={form}
          layout="inline"
          onFinish={(values) => {
            applyListFilters(
              {
                since: values.since ? values.since.format("YYYY-MM-DD") : undefined,
                until: values.until ? values.until.format("YYYY-MM-DD") : undefined,
                bot_id: values.bot_id?.trim() || undefined,
              },
              filters,
              page,
              setPage,
              setFilters,
              refetch,
            );
          }}
        >
          <Form.Item name="since">
            <DatePicker placeholder="开始日期" maxDate={dayjs()} />
          </Form.Item>
          <Form.Item name="until">
            <DatePicker placeholder="结束日期" maxDate={dayjs()} />
          </Form.Item>
          <Form.Item name="bot_id">
            <Select
              placeholder="机器人"
              allowClear
              className="field-width-140"
              virtual={false}
              loading={bots.isPending}
              options={[{ value: "", label: "全部" }, ...botOptions]}
            />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit">
              应用
            </Button>
          </Form.Item>
        </Form>

        {isError ? (
          <LoadError onRetry={() => void refetch()} />
        ) : (
          <Table<ChannelRow>
            rowKey="key"
            size="middle"
            loading={isPending}
            columns={columns}
            dataSource={data?.items}
            locale={{ emptyText: "当前条件下没有频道数据。" }}
            scroll={{ x: "max-content" }}
            pagination={{
              current: data?.page ?? page,
              pageSize: data?.page_size ?? pageSize,
              total: data?.total ?? 0,
              showSizeChanger: true,
              onChange: (nextPage, nextSize) => {
                setPage(nextPage);
                setPageSize(nextSize);
              },
            }}
          />
        )}
        <Text type="secondary">仅由请求记录聚合，不触发主动抓取。</Text>
      </Space>
    </PageCard>
  );
}
