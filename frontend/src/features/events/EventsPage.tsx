/**
 * 事件中心列表页（SSR /events 的 SPA 对应实现）。
 * 级别、事件（key + 受控中文描述）、次数、首次/最近、最近通知时间与状态；
 * 未解决事件可标记解决（经 /api/v1 写端点，与系统自动恢复共用语义：
 * 解决后同一事件再次发生会重开并重新通知）。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, Form, Select, Space, Table, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useState } from "react";

import { fetchEvents, type EventRow } from "../../api/admin";
import { resolveEvent } from "../../api/mutations";
import {
  EVENT_STATUS_LABELS,
  EVENT_STATUS_TAG_COLORS,
  fmtTime,
  labelOf,
} from "../../shared/format";
import { useAdminAction } from "../shared/actions";
import { applyListFilters } from "../shared/listFilters";
import { LoadError, PageCard } from "../shared/PageStates";

const { Text } = Typography;

const SEVERITY_COLORS: Record<string, string> = {
  error: "red",
  warn: "gold",
  warning: "gold",
  info: "blue",
};

interface EventsQuery {
  status: string;
}

export function EventsPage() {
  const [form] = Form.useForm<EventsQuery>();
  const [filters, setFilters] = useState<EventsQuery>({ status: "" });
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["events", "list", { ...filters, page, pageSize }],
    queryFn: () =>
      fetchEvents({ status: filters.status || undefined, page, page_size: pageSize }),
  });

  const resolve = useAdminAction({
    action: (id: number) => resolveEvent(id),
    invalidate: [["events"], ["overview"]],
    successText: "已标记为已解决。",
  });

  const columns: ColumnsType<EventRow> = [
    {
      title: "级别",
      dataIndex: "severity",
      key: "severity",
      render: (severity: string) => (
        <Tag color={SEVERITY_COLORS[severity] ?? "default"}>{severity}</Tag>
      ),
    },
    {
      title: "事件",
      dataIndex: "key",
      key: "key",
      render: (key: string, row) => (
        <>
          <Text code>{key}</Text>
          <br />
          <Text type="secondary">{row.message}</Text>
        </>
      ),
    },
    { title: "次数", dataIndex: "count", key: "count", align: "right" },
    {
      title: "首次 / 最近",
      key: "times",
      render: (_, row) => (
        <>
          {fmtTime(row.first_at)}
          <br />
          {fmtTime(row.last_at)}
        </>
      ),
    },
    { title: "最近通知", dataIndex: "last_notified_at", key: "notified", render: fmtTime },
    {
      title: "状态",
      dataIndex: "status",
      key: "status",
      render: (status: string) => (
        <Tag color={EVENT_STATUS_TAG_COLORS[status]}>{labelOf(EVENT_STATUS_LABELS, status)}</Tag>
      ),
    },
    {
      title: "操作",
      key: "actions",
      render: (_, row) =>
        row.status === "open" ? (
          <Button
            size="small"
            loading={resolve.pending}
            disabled={resolve.pending}
            onClick={() => void resolve.run(row.id)}
          >
            标记解决
          </Button>
        ) : null,
    },
  ];

  return (
    <PageCard title="事件中心">
      <Space direction="vertical" size="middle" className="field-width-full">
        <Form
          form={form}
          layout="inline"
          initialValues={filters}
          onFinish={(values) => {
            applyListFilters(
              { status: values.status ?? "" },
              filters,
              page,
              setPage,
              setFilters,
              refetch,
            );
          }}
        >
          <Form.Item name="status">
            <Select
              className="field-width-120"
              options={[
                { value: "", label: "全部" },
                ...Object.entries(EVENT_STATUS_LABELS).map(([value, label]) => ({ value, label })),
              ]}
            />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit">
              筛选
            </Button>
          </Form.Item>
        </Form>

        {isError ? (
          <LoadError onRetry={() => void refetch()} />
        ) : (
          <Table<EventRow>
            rowKey="id"
            size="middle"
            loading={isPending}
            columns={columns}
            dataSource={data?.items}
            locale={{ emptyText: "暂无事件记录。" }}
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
        <Text type="secondary">
          事件由系统异常自动生成并按 key 去重合并；Bot API 可用时，同一事件在冷却窗口内只向管理员
          Telegram 私聊推送一次。标记解决后，若同一事件再次发生会重新打开并重新通知。
        </Text>
      </Space>
    </PageCard>
  );
}
