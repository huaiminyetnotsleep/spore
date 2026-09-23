/**
 * 错误日志页（/error-logs）：请求管线与 bot 相关环节错误的逐条明细——
 * 来源、环节、错误码、受控描述、原始根因串与参数快照。服务端分页；
 * 按来源/错误码/级别/请求/时间范围筛选（草稿态 + 「查询」生效）；
 * 支持勾选批量删除与「按时间段清理」（先查条数再确认，防盲删）。
 * 数据由 internal/errlog 写入 error_logs（v25）；与事件中心互补
 * （事件按 key 聚合管通知，本页管逐条根因）。
 */
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router-dom";
import {
  App,
  Button,
  DatePicker,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Tag,
  Typography,
} from "antd";
import type { Dayjs } from "dayjs";
import type { ColumnsType } from "antd/es/table";

import {
  fetchErrorLogs,
  type ErrorLogRow,
  type ErrorLogSeverity,
  type ErrorLogSource,
} from "../../api/admin";
import { deleteErrorLogs, deleteErrorLogsRange } from "../../api/mutations";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { DataTable } from "../shared/DataTable";
import { FilterBar } from "../shared/FilterBar";
import { RowActions, type RowActionItem } from "../shared/RowActions";

const { Text } = Typography;

const SOURCE_OPTIONS: { value: ErrorLogSource; label: string }[] = [
  { value: "request", label: "请求管线" },
  { value: "botapi", label: "Bot 收发" },
  { value: "cloud", label: "网盘" },
  { value: "backup", label: "备份" },
  { value: "watch", label: "监听源" },
  { value: "mtproto", label: "用户号会话" },
];

const SOURCE_LABELS: Record<ErrorLogSource, string> = Object.fromEntries(
  SOURCE_OPTIONS.map((o) => [o.value, o.label]),
) as Record<ErrorLogSource, string>;

const SEVERITY_OPTIONS: { value: ErrorLogSeverity; label: string }[] = [
  { value: "error", label: "错误" },
  { value: "warn", label: "警告" },
];

interface FilterValues {
  source?: ErrorLogSource;
  code?: string;
  severity?: ErrorLogSeverity;
  request_id?: number | null;
  range?: [Dayjs | null, Dayjs | null] | null;
}

/** 已应用的筛选（草稿 → 查询按钮生效）。 */
interface AppliedFilters {
  source?: ErrorLogSource;
  code?: string;
  severity?: ErrorLogSeverity;
  requestId?: number;
  after?: number;
  before?: number;
}

export function ErrorLogsPage() {
  const { message } = App.useApp();
  const confirm = useConfirmAction();
  const [searchParams, setSearchParams] = useSearchParams();
  // 支持请求详情页「查看相关日志」深链：?request_id=（白名单解析）
  const initialRequestId = useMemo(() => {
    const raw = searchParams.get("request_id");
    if (!raw) return undefined;
    const parsed = Number(raw);
    return Number.isSafeInteger(parsed) && parsed > 0 ? parsed : undefined;
  }, [searchParams]);

  const [applied, setApplied] = useState<AppliedFilters>({ requestId: initialRequestId });
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [selectedKeys, setSelectedKeys] = useState<React.Key[]>([]);
  // 「按时间段清理」弹层：选范围 → 查条数 → 二次确认删除
  const [cleanupOpen, setCleanupOpen] = useState(false);
  const [cleanupRange, setCleanupRange] = useState<[Dayjs, Dayjs] | null>(null);
  const [counting, setCounting] = useState(false);

  const logs = useQuery({
    queryKey: [
      "error-logs",
      {
        source: applied.source ?? "",
        code: applied.code ?? "",
        severity: applied.severity ?? "",
        request_id: applied.requestId ?? 0,
        after: applied.after ?? 0,
        before: applied.before ?? 0,
        page,
        pageSize,
      },
    ],
    queryFn: () =>
      fetchErrorLogs({
        source: applied.source,
        code: applied.code,
        severity: applied.severity,
        request_id: applied.requestId,
        created_after: applied.after,
        created_before: applied.before,
        page,
        page_size: pageSize,
      }),
  });

  const removeLogs = useAdminAction({
    action: (ids: number[]) => deleteErrorLogs(ids),
    invalidate: [["error-logs"]],
    successText: (result) => `已删除 ${result.deleted} 条错误日志。`,
    onDone: () => setSelectedKeys([]),
  });

  const removeRange = useAdminAction({
    action: (body: { after?: number; before?: number; source?: string; code?: string }) =>
      deleteErrorLogsRange(body),
    invalidate: [["error-logs"]],
    successText: (result) => `已按时间段清理 ${result.deleted} 条错误日志。`,
    onDone: () => {
      setCleanupOpen(false);
      setCleanupRange(null);
      setSelectedKeys([]);
      setPage(1);
    },
  });

  const applyFilters = (values: FilterValues) => {
    setPage(1);
    setSelectedKeys([]);
    setApplied({
      source: values.source,
      code: values.code?.trim() || undefined,
      severity: values.severity,
      requestId: values.request_id && values.request_id > 0 ? values.request_id : undefined,
      after: values.range?.[0] ? values.range[0].valueOf() : undefined,
      before: values.range?.[1] ? values.range[1].valueOf() + 1 : undefined,
    });
    const next = new URLSearchParams(searchParams);
    if (values.request_id && values.request_id > 0) {
      next.set("request_id", String(values.request_id));
    } else {
      next.delete("request_id");
    }
    setSearchParams(next, { replace: true });
  };

  const cleanupBody = () =>
    cleanupRange
      ? {
          after: cleanupRange[0].valueOf(),
          before: cleanupRange[1].valueOf() + 1,
          source: applied.source,
          code: applied.code,
        }
      : null;

  const cleanupConditionText = () => {
    const parts: string[] = [];
    if (applied.source) parts.push(`来源=${SOURCE_LABELS[applied.source]}`);
    if (applied.code) parts.push(`错误码=${applied.code}`);
    return parts.length > 0 ? `（叠加当前筛选：${parts.join("、")}）` : "";
  };

  // 先查条数再确认：条数为 0 直接提示；确认弹层显示将删除数量，防盲删
  const submitCleanup = async () => {
    const body = cleanupBody();
    if (!body) return;
    setCounting(true);
    try {
      const res = await fetchErrorLogs({
        source: applied.source,
        code: applied.code,
        created_after: body.after,
        created_before: body.before,
        page: 1,
        page_size: 1,
      });
      if (res.total === 0) {
        void message.info("所选时间范围内没有匹配的错误日志。");
        return;
      }
      confirm({
        intent: "danger",
        title: "按时间段清理错误日志",
        content: `将删除 ${res.total} 条错误日志${cleanupConditionText()}，删除后不可恢复。确定继续？`,
        okText: "清理",
        action: () => removeRange.run(body),
      });
    } finally {
      setCounting(false);
    }
  };

  const deleteSingle = (row: ErrorLogRow) => {
    confirm({
      intent: "danger",
      title: "删除错误日志",
      content: `确定删除这条 ${SOURCE_LABELS[row.source]} 日志？删除后不可恢复。`,
      okText: "删除",
      action: () => removeLogs.run([row.id]),
    });
  };

  const deleteBatch = () => {
    const ids = selectedKeys.map(Number);
    confirm({
      intent: "danger",
      title: "批量删除错误日志",
      content: `确定删除选中的 ${ids.length} 条错误日志？删除后不可恢复。`,
      okText: "删除",
      action: () => removeLogs.run(ids),
    });
  };

  const columns: ColumnsType<ErrorLogRow> = [
    {
      title: "时间",
      dataIndex: "created_at",
      width: 170,
      render: (v: number) => fmtTime(v),
    },
    {
      title: "来源",
      dataIndex: "source",
      width: 120,
      render: (v: ErrorLogSource) => <Tag>{SOURCE_LABELS[v]}</Tag>,
    },
    {
      title: "级别",
      dataIndex: "severity",
      width: 80,
      render: (v: ErrorLogSeverity) =>
        v === "error" ? <Tag color="red">错误</Tag> : <Tag color="orange">警告</Tag>,
    },
    {
      title: "错误码",
      dataIndex: "code",
      width: 200,
      render: (v: string, row) =>
        v ? (
          <Space direction="vertical" size={0}>
            <Text code>{v}</Text>
            {row.stage ? <Text type="secondary">环节：{row.stage}</Text> : null}
          </Space>
        ) : (
          <Text type="secondary">未分类{row.stage ? `（${row.stage}）` : ""}</Text>
        ),
    },
    {
      title: "描述",
      dataIndex: "message",
      render: (v: string, row) => (
        <Space direction="vertical" size={0}>
          <Text>{v}</Text>
          {row.request_id > 0 ? (
            <Link to={`/requests/${row.request_id}`}>请求 {row.request_id}</Link>
          ) : null}
        </Space>
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
      title="错误日志"
      description="请求管线与 Bot 相关环节错误的逐条明细：来源、环节、错误码与原始根因串（含参数快照）。自动按保留天数清理；也可在此批量删除或按时间段清理。"
    >
      <PageSection title="日志明细">
        <FilterBar<FilterValues>
          mode="submit"
          onFinish={(values) => applyFilters(values)}
          onReset={() => {
            setPage(1);
            setSelectedKeys([]);
            setApplied({});
            const next = new URLSearchParams(searchParams);
            next.delete("request_id");
            setSearchParams(next, { replace: true });
          }}
          initialValues={
            initialRequestId ? { request_id: initialRequestId } : undefined
          }
          actions={
            <Button danger onClick={() => setCleanupOpen(true)}>
              按时间段清理
            </Button>
          }
        >
          <Form.Item name="source">
            <Select
              allowClear
              placeholder="全部来源"
              className="field-width-140"
              options={SOURCE_OPTIONS}
              aria-label="按来源筛选"
            />
          </Form.Item>
          <Form.Item name="severity">
            <Select
              allowClear
              placeholder="全部级别"
              className="field-width-120"
              options={SEVERITY_OPTIONS}
              aria-label="按级别筛选"
            />
          </Form.Item>
          <Form.Item name="code">
            <Input
              allowClear
              placeholder="错误码（如 BOT_SEND_FAILED）"
              className="field-width-200"
              aria-label="按错误码筛选"
            />
          </Form.Item>
          <Form.Item name="request_id">
            <InputNumber
              placeholder="请求 ID"
              min={1}
              precision={0}
              className="field-width-120"
              aria-label="按请求 ID 筛选"
            />
          </Form.Item>
          <Form.Item name="range">
            <DatePicker.RangePicker
              showTime={{ format: "HH:mm" }}
              format="YYYY-MM-DD HH:mm"
              aria-label="按时间范围筛选"
            />
          </Form.Item>
        </FilterBar>
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
          initialLoading={logs.isPending && !logs.data}
          error={logs.isError && !logs.data}
          hasData={!!logs.data}
          onRetry={() => void logs.refetch()}
        >
          <DataTable
            rowKey="id"
            columns={columns}
            dataSource={logs.data?.items ?? []}
            loading={logs.isPending}
            emptyText="暂无错误日志（一切正常，或条件过滤后无匹配）"
            expandable={{
              rowExpandable: (row) => !!row.detail || Object.keys(row.context ?? {}).length > 0,
              expandedRowRender: (row) => (
                <Space direction="vertical" size={4} className="error-log-expand">
                  {row.detail ? (
                    <div>
                      <Text type="secondary">根因：</Text>
                      <Text code>{row.detail}</Text>
                    </div>
                  ) : null}
                  {Object.keys(row.context ?? {}).length > 0 ? (
                    <div>
                      <Text type="secondary">参数：</Text>
                      <pre className="audit-json">
                        {JSON.stringify(row.context, null, 2)}
                      </pre>
                    </div>
                  ) : null}
                </Space>
              ),
            }}
            rowSelection={{
              selectedRowKeys: selectedKeys,
              onChange: (keys) => setSelectedKeys(keys),
            }}
            pagination={{
              current: logs.data?.page ?? page,
              pageSize: logs.data?.page_size ?? pageSize,
              total: logs.data?.total ?? 0,
              showSizeChanger: true,
              onChange: (nextPage, nextSize) => {
                setPage(nextPage);
                setPageSize(nextSize);
              },
            }}
          />
        </PageQueryState>
      </PageSection>
      <Modal
        title="按时间段清理错误日志"
        open={cleanupOpen}
        onCancel={() => {
          setCleanupOpen(false);
          setCleanupRange(null);
        }}
        onOk={() => void submitCleanup()}
        okText="查条数并清理"
        okButtonProps={{ danger: true, disabled: !cleanupRange, loading: counting }}
        cancelText="取消"
      >
        <Space direction="vertical" size={8}>
          <DatePicker.RangePicker
            showTime={{ format: "HH:mm" }}
            format="YYYY-MM-DD HH:mm"
            value={cleanupRange}
            onChange={(value) =>
              setCleanupRange(value && value[0] && value[1] ? [value[0], value[1]] : null)
            }
            aria-label="选择清理时间范围"
          />
          <Text type="secondary">
            删除所选时间范围内的全部错误日志{cleanupConditionText()}；确认前会先显示将删除的条数。
            日常清理交给自动保留策略（运行设置 → 错误日志保留天数）即可，本入口用于手动释放。
          </Text>
        </Space>
      </Modal>
    </PageScaffold>
  );
}
