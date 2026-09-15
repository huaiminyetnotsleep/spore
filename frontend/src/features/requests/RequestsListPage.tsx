/**
 * 消息记录列表页（SSR /requests 的 SPA 对应实现）。
 * 用户/频道/状态/媒体类型/投递方式/错误码/时间范围筛选 + 服务端分页 + CSV 导出，
 * 筛选字段与 SSR 页面一一对应；来源链接跳详情页（message_url 在详情展示）。
 * 云盘补存：终态非纯文本行可单条「存到网盘」，批量模式多选后批量补存，
 * 目的地弹层默认选中云盘配置的默认目的地。
 * 缓存补写（转存缓存频道）：终态行（含纯文本）批量写入缓存频道干净副本，
 * 全程不向用户发送消息；需在系统设置中配置缓存频道。
 */
import { useQuery } from "@tanstack/react-query";
import {
  Button,
  DatePicker,
  Form,
  Input,
  Modal,
  Segmented,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import type { ColumnsType } from "antd/es/table";
import dayjs, { type Dayjs } from "dayjs";
import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";

import {
  buildExportURL,
  fetchCloudDrive,
  fetchRequests,
  fetchSettings,
  type RequestRow,
} from "../../api/admin";
import {
  cancelRequest,
  cancelRequests,
  cloudArchiveBatch,
  cloudArchiveRequest,
  deleteRequest,
  deleteRequests,
  dumpBackfillRequest,
  dumpBackfillRequests,
  type CancelManyResult,
  type CloudArchiveBatchResult,
  type DeleteManyResult,
  type DumpBackfillBatchResult,
} from "../../api/mutations";
import {
  CLOUD_ARCHIVE_SKIP_LABELS,
  DUMP_BACKFILL_SKIP_LABELS,
  DELIVERY_MODE_LABELS,
  DELIVERY_MODE_TAG_COLORS,
  REQUEST_STATUS_LABELS,
  REQUEST_STATUS_TAG_COLORS,
  fmtDuration,
  fmtTime,
  labelOf,
  requestMediaTypeText,
} from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { applyListFilters } from "../shared/listFilters";
import { LoadError, PageCard } from "../shared/PageStates";
import { RequestProgress, RequestProgressEmpty } from "./RequestProgress";

const { Text } = Typography;

const MEDIA_TYPE_OPTIONS = ["text", "photo", "video", "document", "audio", "voice", "album"].map(
  (value) => ({ value, label: value }),
);

const DELIVERY_MODE_OPTIONS = Object.entries(DELIVERY_MODE_LABELS).map(([value, label]) => ({
  value,
  label,
}));

/** 批量操作模式：取消（活动行）/ 删除（终态行）/ 存到网盘（终态非纯文本行）/
 * 转存缓存频道（终态行，含纯文本）。 */
type BatchMode = "cancel" | "delete" | "archive" | "dump";

/** 批量操作逐条结果摘要的展示状态：两类批量动作（网盘补存/缓存补写）共用
 * 同一结果弹层，标题、逐条结果与跳过原因标签表随动作携带。 */
interface BatchSummaryState {
  title: string;
  createdNoun: string;
  skipLabels: Record<string, string>;
  results: {
    request_id: number;
    created_request_id?: number;
    skip_reason?: string;
    queue_full?: boolean;
  }[];
}

const TERMINAL_STATUSES = ["succeeded", "failed", "cancelled"];

/** 处理中记录存在时的进度轮询间隔（与 MTProto 状态页一致，2 秒）。 */
const PROGRESS_POLL_INTERVAL_MS = 2000;

const STATUS_OPTIONS = Object.entries(REQUEST_STATUS_LABELS).map(([value, label]) => ({
  value,
  label,
}));

function sourceMediaDCs(ids: number[] | undefined) {
  return ids?.length ? ids.map((id) => `DC ${id}`).join("、") : "—";
}

interface RequestFormValues {
  user_id?: string;
  channel?: string;
  status?: string;
  media_type?: string;
  delivery_mode?: string;
  error_code?: string;
  since?: Dayjs | null;
  until?: Dayjs | null;
}

interface RequestQuery {
  user_id?: string;
  channel?: string;
  status?: string;
  media_type?: string;
  delivery_mode?: string;
  error_code?: string;
  since?: string;
  until?: string;
}

/** 表单值 → 查询参数（日期转 YYYY-MM-DD，空值剔除）。 */
function toQueryValues(values: RequestFormValues): RequestQuery {
  const day = (d: Dayjs | null | undefined) => (d ? d.format("YYYY-MM-DD") : undefined);
  return {
    user_id: values.user_id?.trim() || undefined,
    channel: values.channel?.trim() || undefined,
    status: values.status || undefined,
    media_type: values.media_type || undefined,
    delivery_mode: values.delivery_mode || undefined,
    error_code: values.error_code?.trim() || undefined,
    since: day(values.since),
    until: day(values.until),
  };
}

/** 从跨页入口带入请求筛选；未知参数不参与查询。 */
function filtersFromSearchParams(params: URLSearchParams): RequestQuery {
  const value = (name: keyof RequestQuery) => params.get(name)?.trim() || undefined;
  return {
    user_id: value("user_id"),
    channel: value("channel"),
    status: value("status"),
    media_type: value("media_type"),
    delivery_mode: value("delivery_mode"),
    error_code: value("error_code"),
    since: value("since"),
    until: value("until"),
  };
}

/** 把 URL 日期筛选转换为 DatePicker 可接受的值；服务端仍负责日期语义校验。 */
function dateValue(value?: string): Dayjs | null {
  if (!value) return null;
  const parsed = dayjs(value);
  return parsed.isValid() ? parsed : null;
}

export function RequestsListPage() {
  const [form] = Form.useForm<RequestFormValues>();
  const [searchParams] = useSearchParams();
  const searchFilters = useMemo(() => filtersFromSearchParams(searchParams), [searchParams]);
  const [filters, setFilters] = useState<RequestQuery>(searchFilters);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [batchMode, setBatchMode] = useState<BatchMode>("cancel");
  const [selectedIDs, setSelectedIDs] = useState<number[]>([]);
  const confirm = useConfirmAction();

  // 云盘配置只用于补存入口的可用性判断与目的地选择；查询失败不阻塞列表。
  const cloudDrive = useQuery({ queryKey: ["cloud-drive"], queryFn: fetchCloudDrive });
  // 缓存频道配置只用于缓存补写入口的可用性判断；查询失败不阻塞列表。
  const settings = useQuery({ queryKey: ["settings"], queryFn: fetchSettings });
  const dumpChannelReady = (settings.data?.dump_channel_id ?? 0) !== 0;

  // 补存目的地弹层（单条/批量共用）：目标 ID 集合 + 当前选中的目的地。
  const [archiveTarget, setArchiveTarget] = useState<number[] | null>(null);
  const [archiveDestination, setArchiveDestination] = useState("");
  const [batchSummary, setBatchSummary] = useState<BatchSummaryState | null>(null);

  const destinationOptions = (cloudDrive.data?.destinations ?? [])
    .filter((dest) => dest.enabled)
    .map((dest) => ({ value: dest.name, label: dest.name }));

  /** 打开目的地弹层：默认选中 default_destination（未启用时回落第一个启用目的地）。 */
  const openArchiveModal = (ids: number[]) => {
    const enabledNames = destinationOptions.map((option) => option.value);
    const preferred = cloudDrive.data?.default_destination ?? "";
    setArchiveDestination(
      enabledNames.includes(preferred) ? preferred : (enabledNames[0] ?? ""),
    );
    setArchiveTarget(ids);
  };

  /** 行级「存到网盘」可用性：返回 undefined 表示可用，否则为禁用原因（Tooltip）。 */
  const archiveDisabledReason = (row: RequestRow): string | undefined => {
    if (!TERMINAL_STATUSES.includes(row.status)) {
      return "仅已结束（成功/失败/取消）的请求可存到网盘";
    }
    if (row.delivery_mode === "text") {
      return "纯文本请求没有可存媒体";
    }
    if (!cloudDrive.data) {
      return "云盘配置加载中或不可用";
    }
    if (!cloudDrive.data.enabled) {
      return "云盘下载功能未开启，可在「云盘下载」页开启";
    }
    return undefined;
  };

  /** 行级「转存缓存频道」可用性：终态即可（纯文本同样可写副本），另要求
   * 缓存频道已配置。返回 undefined 表示可用，否则为禁用原因（Tooltip）。 */
  const dumpDisabledReason = (row: RequestRow): string | undefined => {
    if (!TERMINAL_STATUSES.includes(row.status)) {
      return "仅已结束（成功/失败/取消）的请求可转存缓存频道";
    }
    if (settings.data && !dumpChannelReady) {
      return "缓存频道未配置，可在「运行设置」页配置";
    }
    return undefined;
  };

  const invalidateRequestQueries = [["requests"], ["channels"], ["users"], ["overview"]];
  const remove = useAdminAction({
    action: (id: number) => deleteRequest(id),
    invalidate: invalidateRequestQueries,
    successText: "记录已删除。",
  });
  const cancel = useAdminAction({
    action: (id: number) => cancelRequest(id),
    invalidate: invalidateRequestQueries,
    successText: "请求已取消。",
    onDone: () => setSelectedIDs([]),
  });
  // 云盘补存：管理端动作绕过用户配额与去重窗口；在途补存等服务端校验
  // （409 受控文案），列表行数据不含补存在途信息，前端不禁用该情形。
  const archive = useAdminAction({
    action: (input: { id: number; destination?: string }) =>
      cloudArchiveRequest(input.id, input.destination),
    invalidate: invalidateRequestQueries,
    successText: "已创建云盘补存任务，新请求行将以「网盘」投递方式出现在列表中。",
  });
  const archiveMany = useAdminAction<CloudArchiveBatchResult, { ids: number[]; destination?: string }>(
    {
      action: (input) => cloudArchiveBatch(input.ids, input.destination),
      invalidate: invalidateRequestQueries,
      successText: "批量存到网盘已提交。",
      onDone: (data) => {
        setSelectedIDs([]);
        if (data) {
          setBatchSummary({
            title: "批量存到网盘结果",
            createdNoun: "云盘补存任务",
            skipLabels: CLOUD_ARCHIVE_SKIP_LABELS,
            results: data.results,
          });
        }
      },
    },
  );
  // 缓存补写：管理端动作绕过用户配额与去重窗口；已有副本/未配置等服务端
  // 校验（409/503 受控文案），列表行数据不含副本信息，前端不禁用该情形。
  const dumpBackfill = useAdminAction({
    action: (id: number) => dumpBackfillRequest(id),
    invalidate: invalidateRequestQueries,
    successText: "已创建缓存补写任务，新请求行将以「缓存补写」投递方式出现在列表中。",
  });
  const dumpBackfillMany = useAdminAction<DumpBackfillBatchResult, number[]>({
    action: (ids) => dumpBackfillRequests(ids),
    invalidate: invalidateRequestQueries,
    successText: "批量转存缓存频道已提交。",
    onDone: (data) => {
      setSelectedIDs([]);
      if (data) {
        setBatchSummary({
          title: "批量转存缓存频道结果",
          createdNoun: "缓存补写任务",
          skipLabels: DUMP_BACKFILL_SKIP_LABELS,
          results: data.results,
        });
      }
    },
  });
  const cancelMany = useAdminAction<CancelManyResult, number[]>({
    action: (ids) => cancelRequests(ids),
    invalidate: invalidateRequestQueries,
    successText: (data) => {
      const resultText: Record<string, string> = {
        cancelled: "已取消",
        conflict: "状态冲突，未取消",
        not_found: "不存在",
        failed: "处理失败",
      };
      return `批量取消结果：${data.results
        .map((item) => `#${item.id} ${resultText[item.result] ?? "未处理"}`)
        .join("；")}`;
    },
    onDone: () => setSelectedIDs([]),
  });
  const removeMany = useAdminAction<DeleteManyResult, number[]>({
    action: (ids) => deleteRequests(ids),
    invalidate: invalidateRequestQueries,
    successText: (data) => {
      const resultText: Record<string, string> = {
        deleted: "已删除",
        conflict: "状态冲突，未删除",
        not_found: "不存在",
        failed: "处理失败",
      };
      return `批量删除结果：${data.results
        .map((item) => `#${item.id} ${resultText[item.result] ?? "未处理"}`)
        .join("；")}`;
    },
    onDone: () => setSelectedIDs([]),
  });

  useEffect(() => {
    setPage(1);
    setFilters(searchFilters);
    form.setFieldsValue({
      ...searchFilters,
      since: dateValue(searchFilters.since),
      until: dateValue(searchFilters.until),
    });
  }, [form, searchFilters]);

  useEffect(() => {
    setSelectedIDs([]);
  }, [filters, page, pageSize]);

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["requests", "list", { ...filters, page, pageSize }],
    queryFn: () => fetchRequests({ ...filters, page, page_size: pageSize }),
    // 仅当前页存在处理中记录时轮询实时进度（内存态字段，2 秒刷新足够）
    refetchInterval: (q) =>
      q.state.data?.items.some((row) => row.status === "processing")
        ? PROGRESS_POLL_INTERVAL_MS
        : false,
    refetchIntervalInBackground: false,
  });

  const columns: ColumnsType<RequestRow> = [
    {
      title: "ID",
      dataIndex: "id",
      key: "id",
      render: (id: number) => <Link to={`/requests/${id}`}>{id}</Link>,
    },
    {
      title: "来源",
      key: "source",
      render: (_, row) => (
        <>
          {row.message_url ? (
            <a
              href={row.message_url}
              target="_blank"
              rel="noopener noreferrer"
              className="source-link"
            >
              {row.message_url}
            </a>
          ) : (
            <Text code>链接不可用</Text>
          )}
          {row.source_kind === "private" ? <Tag color="gold">私有，需权限</Tag> : null}
        </>
      ),
    },
    {
      title: "用户",
      key: "user",
      render: (_, row) => (
        <Space direction="vertical" size={0}>
          <Link to={`/users/${row.user_id}`}>{row.user_id}</Link>
          <Text type="secondary">
            {row.username ? `@${row.username}` : "—"}
            {row.display_name ? `（${row.display_name}）` : ""}
          </Text>
        </Space>
      ),
    },
    {
      title: "频道",
      dataIndex: "channel_key",
      key: "channel_key",
      render: (channel: string) => (
        <Link to={`/channels/${encodeURIComponent(channel)}`}>{channel}</Link>
      ),
    },
    {
      title: "状态",
      dataIndex: "status",
      key: "status",
      render: (status: string) => (
        <Tag color={REQUEST_STATUS_TAG_COLORS[status]}>{labelOf(REQUEST_STATUS_LABELS, status)}</Tag>
      ),
    },
    {
      title: "进度",
      key: "progress",
      render: (_, row) =>
        row.progress ? <RequestProgress progress={row.progress} /> : <RequestProgressEmpty />,
    },
    { title: "尝试", dataIndex: "attempt", key: "attempt", align: "right" },
    { title: "错误码", dataIndex: "error_code", key: "error_code" },
    {
      title: "媒体",
      dataIndex: "media_type",
      key: "media_type",
      render: (mediaType: string, row) => (
        <Text>{requestMediaTypeText(mediaType, row.media_types)}</Text>
      ),
    },
    {
      title: "源媒体 DC",
      dataIndex: "source_media_dc_ids",
      key: "source_media_dc_ids",
      width: 130,
      className: "request-dc-cell",
      render: sourceMediaDCs,
    },
    {
      title: "投递方式",
      dataIndex: "delivery_mode",
      key: "delivery_mode",
      render: (mode: string) => (
        <Tag color={DELIVERY_MODE_TAG_COLORS[mode]}>{labelOf(DELIVERY_MODE_LABELS, mode)}</Tag>
      ),
    },
    { title: "请求时间", dataIndex: "requested_at", key: "requested_at", render: fmtTime },
    { title: "耗时", dataIndex: "duration_ms", key: "duration", render: fmtDuration },
    {
      title: "操作",
      key: "actions",
      fixed: "right",
      width: 240,
      render: (_, row) => {
        const canCancel = row.status === "queued" || row.status === "processing";
        const archiveReason = archiveDisabledReason(row);
        const dumpReason = dumpDisabledReason(row);
        const actionsPending =
          cancel.pending || remove.pending || cancelMany.pending || removeMany.pending;
        return (
          <Space size={4} wrap={false}>
            {/* 禁用按钮不触发鼠标事件，包一层 span 让 Tooltip 能展示禁用原因 */}
            <Tooltip title={archiveReason}>
              <span>
                <Button
                  size="small"
                  loading={archive.pending}
                  disabled={archiveReason !== undefined || actionsPending}
                  onClick={() => openArchiveModal([row.id])}
                >
                  存到网盘
                </Button>
              </span>
            </Tooltip>
            <Tooltip title={dumpReason}>
              <span>
                <Button
                  size="small"
                  loading={dumpBackfill.pending}
                  disabled={dumpReason !== undefined || actionsPending}
                  onClick={() =>
                    confirm(
                      `确定将记录 #${row.id} 转存缓存频道？将按原链接重新获取源消息并写入干净副本，全程不向用户发送任何消息。`,
                      () => {
                        void dumpBackfill.run(row.id);
                      },
                    )
                  }
                >
                  转存
                </Button>
              </span>
            </Tooltip>
            {canCancel ? (
              <Button
                size="small"
                danger
                loading={cancel.pending}
                disabled={cancel.pending || cancelMany.pending || removeMany.pending}
                onClick={() =>
                  confirm(
                    `确定取消请求 #${row.id}？取消后不可恢复，但不会删除记录。`,
                    () => {
                      void cancel.run(row.id);
                    },
                  )
                }
              >
                取消
              </Button>
            ) : (
              <Button
                size="small"
                danger
                loading={remove.pending}
                disabled={remove.pending || cancelMany.pending || removeMany.pending}
                onClick={() =>
                  confirm(
                    `确定删除记录 #${row.id}？删除后不可恢复，且该记录不再参与统计。未结束（排队/处理中）的记录不可删除。`,
                    () => {
                      void remove.run(row.id);
                    },
                  )
                }
              >
                删除
              </Button>
            )}
          </Space>
        );
      },
    },
  ];

  return (
    <PageCard
      title="请求记录"
      extra={
        <Button href={buildExportURL("/requests/export.csv", { ...filters })}>
          按当前条件导出 CSV
        </Button>
      }
    >
      <Space direction="vertical" size="middle" className="field-width-full">
        <Form
          form={form}
          layout="inline"
          onFinish={(values) => {
            applyListFilters(toQueryValues(values), filters, page, setPage, setFilters, refetch);
          }}
        >
          <Form.Item name="user_id">
            <Input placeholder="用户 ID" allowClear className="field-width-120" />
          </Form.Item>
          <Form.Item name="channel">
            <Input placeholder="频道" allowClear className="field-width-140" />
          </Form.Item>
          <Form.Item name="status">
            <Select
              placeholder="状态"
              allowClear
              className="field-width-110"
              options={[{ value: "", label: "全部" }, ...STATUS_OPTIONS]}
            />
          </Form.Item>
          <Form.Item name="media_type">
            <Select
              placeholder="媒体类型"
              allowClear
              className="field-width-120"
              options={[{ value: "", label: "全部" }, ...MEDIA_TYPE_OPTIONS]}
            />
          </Form.Item>
          <Form.Item name="delivery_mode">
            {/* virtual={false}：选项少（6 项），全量渲染便于键盘/无障碍访问 */}
            <Select
              placeholder="投递方式"
              allowClear
              className="field-width-110"
              virtual={false}
              options={[{ value: "", label: "全部" }, ...DELIVERY_MODE_OPTIONS]}
            />
          </Form.Item>
          <Form.Item name="error_code">
            <Input placeholder="错误码" allowClear className="field-width-160" />
          </Form.Item>
          <Form.Item name="since">
            <DatePicker placeholder="开始日期" maxDate={dayjs()} />
          </Form.Item>
          <Form.Item name="until">
            <DatePicker placeholder="结束日期" maxDate={dayjs()} />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit">
              筛选
            </Button>
          </Form.Item>
        </Form>

        <Space wrap>
          <Text>批量操作</Text>
          <Segmented
            value={batchMode}
            options={[
              { label: "取消请求", value: "cancel" },
              { label: "删除记录", value: "delete" },
              { label: "存到网盘", value: "archive" },
              { label: "转存缓存频道", value: "dump" },
            ]}
            onChange={(value) => {
              setBatchMode(value as BatchMode);
              setSelectedIDs([]);
            }}
          />
          {selectedIDs.length > 0 ? (
            batchMode === "cancel" ? (
              <Button
                danger
                loading={cancelMany.pending}
                disabled={cancel.pending || removeMany.pending}
                onClick={() =>
                  confirm(
                    `确定取消已选的 ${selectedIDs.length} 条请求？取消后不可恢复，但不会删除记录。`,
                    () => {
                      void cancelMany.run(selectedIDs);
                    },
                  )
                }
              >
                批量取消（{selectedIDs.length}）
              </Button>
            ) : batchMode === "delete" ? (
              <Button
                danger
                loading={removeMany.pending}
                disabled={remove.pending || cancelMany.pending}
                onClick={() =>
                  confirm(
                    `确定删除已选的 ${selectedIDs.length} 条记录？删除后不可恢复，且这些记录不再参与请求、频道、用户和总览统计。`,
                    () => {
                      void removeMany.run(selectedIDs);
                    },
                  )
                }
              >
                批量删除（{selectedIDs.length}）
              </Button>
            ) : batchMode === "dump" ? (
              <Button
                loading={dumpBackfillMany.pending}
                disabled={dumpBackfill.pending || cancel.pending || remove.pending}
                onClick={() =>
                  confirm(
                    `确定对已选的 ${selectedIDs.length} 条记录转存缓存频道？将按原链接重新获取源消息并写入干净副本，全程不向用户发送任何消息；已有副本的记录会自动跳过。`,
                    () => {
                      void dumpBackfillMany.run(selectedIDs);
                    },
                  )
                }
              >
                批量转存（{selectedIDs.length}）
              </Button>
            ) : (
              <Button
                loading={archiveMany.pending}
                disabled={archive.pending || cancel.pending || remove.pending}
                onClick={() => openArchiveModal(selectedIDs)}
              >
                批量存到网盘（{selectedIDs.length}）
              </Button>
            )
          ) : null}
          <Text type="secondary">
            {batchMode === "cancel"
              ? "仅可选择当前页仍在排队或处理中的记录。"
              : batchMode === "delete"
                ? "仅可选择当前页已成功、失败或取消的终态记录。"
                : batchMode === "dump"
                  ? dumpChannelReady
                    ? "仅可选择当前页已结束的记录；副本写入缓存频道，不向用户发送消息。"
                    : "缓存频道未配置，可在「运行设置」页配置后使用。"
                  : "仅可选择当前页已结束且非纯文本的记录；云盘下载需在「云盘下载」页开启。"}
          </Text>
        </Space>
        {isError ? (
          <LoadError onRetry={() => void refetch()} />
        ) : (
          <Table<RequestRow>
            rowKey="id"
            rowSelection={{
              selectedRowKeys: selectedIDs,
              onChange: (keys) => setSelectedIDs(keys.map((key) => Number(key))),
              getCheckboxProps: (row) => {
                const active = row.status === "queued" || row.status === "processing";
                const terminal = TERMINAL_STATUSES.includes(row.status);
                if (batchMode === "cancel") {
                  return { disabled: !active };
                }
                if (batchMode === "delete") {
                  return { disabled: !terminal };
                }
                if (batchMode === "dump") {
                  // 转存缓存频道：终态即可（纯文本同样可写副本）；缓存频道
                  // 未配置时禁用（在途补写由服务端复核）。
                  return { disabled: dumpDisabledReason(row) !== undefined };
                }
                // 存到网盘：终态 ∧ 非纯文本 ∧ 云盘已开启（在途补存由服务端复核）。
                return { disabled: archiveDisabledReason(row) !== undefined };
              },
            }}
            size="middle"
            loading={isPending}
            columns={columns}
            dataSource={data?.items}
            locale={{ emptyText: "没有符合条件的记录。" }}
            scroll={{ x: "max-content" }}
            pagination={{
              current: data?.page ?? page,
              pageSize: data?.page_size ?? pageSize,
              total: data?.total ?? 0,
              showSizeChanger: true,
              onChange: (nextPage, nextSize) => {
                setSelectedIDs([]);
                setPage(nextPage);
                setPageSize(nextSize);
              },
            }}
          />
        )}
        <Text type="secondary">仅记录提取请求的执行事实，不含消息正文与媒体本体。</Text>
      </Space>

      {/* 补存目的地弹层：单条与批量共用；缺省选中默认目的地。 */}
      <Modal
        open={archiveTarget !== null}
        title={
          archiveTarget && archiveTarget.length > 1
            ? `批量存到网盘（${archiveTarget.length} 条）`
            : "存到网盘"
        }
        okText={archiveTarget && archiveTarget.length > 1 ? "批量存入" : "存到网盘"}
        cancelText="取消"
        confirmLoading={archive.pending || archiveMany.pending}
        onCancel={() => setArchiveTarget(null)}
        onOk={() => {
          if (!archiveTarget) return;
          const destination = archiveDestination || undefined;
          setArchiveTarget(null);
          if (archiveTarget.length === 1) {
            void archive.run({ id: archiveTarget[0], destination });
          } else {
            void archiveMany.run({ ids: archiveTarget, destination });
          }
        }}
      >
        <Space direction="vertical" size="small" className="field-width-full">
          <Text type="secondary">
            将按原链接重新抓取媒体并上传到所选目的地，不重发回
            Telegram；管理端补存不占用用户配额，新建的请求行以「网盘」投递方式出现在列表中。
          </Text>
          <Select
            aria-label="补存目的地"
            value={archiveDestination || undefined}
            placeholder="选择目的地"
            options={destinationOptions}
            onChange={setArchiveDestination}
            className="field-width-240"
          />
        </Space>
      </Modal>

      {/* 批量操作逐条结果摘要（网盘补存/缓存补写共用）：成功创建数 + 跳过原因。 */}
      <Modal
        open={batchSummary !== null}
        title={batchSummary?.title ?? ""}
        footer={
          <Button onClick={() => setBatchSummary(null)}>关闭</Button>
        }
        onCancel={() => setBatchSummary(null)}
      >
        {batchSummary ? (
          <Space direction="vertical" size="small" className="field-width-full">
            <Text>
              {`成功创建 ${
                batchSummary.results.filter(
                  (row) => row.created_request_id && !row.queue_full,
                ).length
              } 条${batchSummary.createdNoun}；队列满 ${
                batchSummary.results.filter((row) => row.queue_full).length
              } 条；跳过 ${
                batchSummary.results.filter((row) => row.skip_reason).length
              } 条。`}
            </Text>
            {batchSummary.results.some((row) => row.skip_reason || row.queue_full) ? (
              <ul className="cloud-batch-summary-list">
                {batchSummary.results
                  .filter((row) => row.skip_reason || row.queue_full)
                  .map((row) => (
                    <li key={row.request_id}>
                      {row.queue_full
                        ? `#${row.request_id}：已创建任务但队列已满（QUEUE_FULL），可稍后重试`
                        : `#${row.request_id}：${labelOf(
                            batchSummary.skipLabels,
                            row.skip_reason ?? "",
                          )}`}
                    </li>
                  ))}
              </ul>
            ) : null}
            <Text type="secondary">
              队列已满的行会标记 QUEUE_FULL 失败，可经现有重试入口重试；源消息已被删除或缓存频道已改配置时，对应新行会以明确错误失败。
            </Text>
          </Space>
        ) : null}
      </Modal>
    </PageCard>
  );
}
