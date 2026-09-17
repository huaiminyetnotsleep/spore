/**
 * 事件中心列表页（SSR /events 的 SPA 对应实现）。
 * 级别、事件（key + 受控中文描述）、次数、首次/最近、最近通知时间与状态；
 * 未解决事件可标记解决（经 /api/v1 写端点，与系统自动恢复共用语义：
 * 解决后同一事件再次发生会重开并重新通知）。
 */
import { useQuery } from "@tanstack/react-query";
import { Form, Select, Space, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useState } from "react";

import { fetchEvents, type EventRow } from "../../api/admin";
import { resolveEvent } from "../../api/mutations";
import {
  EVENT_STATUS_LABELS,
  fmtTime,
  labelOf,
} from "../../shared/format";
import { useAdminAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { FilterBar } from "../shared/FilterBar";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { LoadError } from "../shared/PageStates";
import { applyListFilters } from "../shared/listFilters";
import { RowActions, type RowActionItem } from "../shared/RowActions";
import { StatusTag, type StatusTone } from "../shared/StatusTag";

const { Text } = Typography;

/** 事件级别 → 语义色调（领域映射由本页定义）。 */
const SEVERITY_TONES: Record<string, StatusTone> = {
  error: "error",
  warn: "warning",
  warning: "warning",
  info: "processing",
};

/** 事件状态 → 语义色调（与 EVENT_STATUS_LABELS 同 key）。 */
const EVENT_STATUS_TONES: Record<string, StatusTone> = {
  open: "error",
  resolved: "success",
};

interface EventsQuery {
  status: string;
}

export function EventsPage() {
  const [form] = Form.useForm<EventsQuery>();
  const [filters, setFilters] = useState<EventsQuery>({ status: "" });
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  /** 行级目标 key：解决 pending 只让当前行按钮进入 loading。 */
  const [busyEventId, setBusyEventId] = useState<number | null>(null);

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["events", "list", { ...filters, page, pageSize }],
    queryFn: () =>
      fetchEvents({ status: filters.status || undefined, page, page_size: pageSize }),
  });

  const resolve = useAdminAction({
    action: (id: number) => {
      setBusyEventId(id);
      return resolveEvent(id);
    },
    invalidate: [["events"], ["overview"]],
    successText: "已标记为已解决。",
  });

  const columns: ColumnsType<EventRow> = [
    {
      title: "级别",
      dataIndex: "severity",
      key: "severity",
      render: (severity: string) => (
        <StatusTag tone={SEVERITY_TONES[severity] ?? "default"}>{severity}</StatusTag>
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
        <StatusTag tone={EVENT_STATUS_TONES[status] ?? "default"}>
          {labelOf(EVENT_STATUS_LABELS, status)}
        </StatusTag>
      ),
    },
    {
      title: "操作",
      key: "actions",
      // 统一行操作（RowActions）：解决为即时执行（无确认），仅未解决行提供
      width: 100,
      render: (_, row) =>
        row.status === "open" ? (
          <RowActions
            actions={[
              {
                key: "resolve",
                label: "标记解决",
                loading: busyEventId === row.id && resolve.pending,
                disabled: resolve.pending,
                onClick: () => void resolve.run(row.id),
              } satisfies RowActionItem,
            ]}
          />
        ) : null,
    },
  ];

  return (
    <PageScaffold
      title="事件中心"
      description="系统异常按 key 去重合并；同一事件在冷却窗口内只推送一次通知。"
    >
      <PageSection>
        <Space direction="vertical" size="middle" className="field-width-full">
          <FilterBar<EventsQuery>
            mode="submit"
            form={form}
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
            onReset={() => {
              applyListFilters({ status: "" }, filters, page, setPage, setFilters, refetch);
            }}
          >
            <Form.Item name="status">
              <Select
                className="field-width-120"
                virtual={false}
                options={[
                  { value: "", label: "全部" },
                  ...Object.entries(EVENT_STATUS_LABELS).map(([value, label]) => ({ value, label })),
                ]}
              />
            </Form.Item>
          </FilterBar>

          {isError ? (
            <LoadError onRetry={() => void refetch()} />
          ) : (
            <DataTable<EventRow>
              rowKey="id"
              loading={isPending}
              columns={columns}
              dataSource={data?.items}
              emptyText="暂无事件记录。"
              pagination={{
                current: data?.page ?? page,
                pageSize: data?.page_size ?? pageSize,
                total: data?.total ?? 0,
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
      </PageSection>
    </PageScaffold>
  );
}
