/**
 * 加入审批页：/join 命令的审批记录表。
 * 服务端分页与筛选（状态/申请用户/频道关键词/申请日期范围）；
 * 待审行可同意/拒绝（真实加入要经 Telegram RPC，期间锁住全部审批按钮，
 * 防止重复提交的"操作冲突"）；终态记录支持单条与批量删除。
 * 业务规则在 internal/joinmgr，页面只做展示与触发。
 */
import type { ReactNode } from "react";
import { useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import dayjs, { type Dayjs } from "dayjs";
import {
  Button,
  DatePicker,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Select,
  Space,
  Spin,
  Table,
  Tag,
  Typography,
} from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import type { ColumnsType } from "antd/es/table";
import type { TableRowSelection } from "antd/es/table/interface";

import { fetchJoinRequests, type JoinRequestListParams, type JoinRequestRow } from "../../api/admin";
import {
  approveJoinRequest,
  deleteJoinRequests,
  rejectJoinRequest,
} from "../../api/mutations";
import { fmtTime } from "../../shared/format";
import { applyListFilters } from "../shared/listFilters";
import { useAdminAction } from "../shared/actions";
import { useTitleMask } from "../shared/titleMask";
import { LoadError, PageCard } from "../shared/PageStates";

const { Text } = Typography;

/** 申请状态中文标签与颜色。 */
const STATUS_META: Record<JoinRequestRow["status"], { label: string; color: string }> = {
  pending: { label: "待审批", color: "gold" },
  approved: { label: "已通过", color: "green" },
  rejected: { label: "已拒绝", color: "default" },
  failed: { label: "加入失败", color: "red" },
};

const STATUS_OPTIONS = Object.entries(STATUS_META).map(([value, meta]) => ({
  value,
  label: meta.label,
}));

interface ApprovalFormValues {
  status?: string;
  user_id?: number;
  keyword?: string;
  since?: Dayjs | null;
  until?: Dayjs | null;
}

/** 表单值 → 查询参数（键形状稳定，配合 applyListFilters 的等值比较）。 */
function toQueryValues(values: ApprovalFormValues): JoinRequestListParams {
  const out: JoinRequestListParams = {};
  if (values.status) out.status = values.status;
  if (values.user_id != null) out.user_id = values.user_id;
  const kw = values.keyword?.trim();
  if (kw) out.keyword = kw;
  if (values.since) out.since = values.since.format("YYYY-MM-DD");
  if (values.until) out.until = values.until.format("YYYY-MM-DD");
  return out;
}

function statusTag(status: JoinRequestRow["status"]): ReactNode {
  const meta = STATUS_META[status] ?? { label: status, color: "default" };
  return <Tag color={meta.color}>{meta.label}</Tag>;
}

export function JoinApprovalsPage() {
  const [form] = Form.useForm<ApprovalFormValues>();
  const [filters, setFilters] = useState<JoinRequestListParams>({ status: "pending" });
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [selectedIDs, setSelectedIDs] = useState<number[]>([]);
  // 敏感频道名默认暗文，顶部按钮一键显隐
  const { toggle: titleToggle, text: titleText } = useTitleMask();

  // 审批动作（同意/拒绝共用）：真实加入要经 Telegram RPC（可能数秒），
  // 期间必须锁住全部审批按钮——否则列表刷新完成前行仍显示"待审批"，
  // 再次点击会被服务端 STORE_CONSTRAINT 拒绝（即"操作冲突"假象）。
  const review = useAdminAction({
    action: ({ kind, id }: { kind: "approve" | "reject"; id: number }) =>
      kind === "approve" ? approveJoinRequest(id) : rejectJoinRequest(id),
    invalidate: [["channel-join"]],
    successText: (result, vars) =>
      vars.kind === "approve"
        ? result.request.status === "failed"
          ? "执行加入失败，申请已标记为失败（链接可能已失效）。"
          : "已同意并执行加入，申请人会收到通知。"
        : "已拒绝该申请，申请人会收到通知。",
  });
  const [acting, setActing] = useState<{ id: number; kind: "approve" | "reject" } | null>(null);
  // 同步防重：双击在 React 重渲染前到达时 state 守卫不生效，用 ref 拦截
  const actingRef = useRef(false);
  const handleReview = (kind: "approve" | "reject", id: number) => {
    if (actingRef.current) return;
    actingRef.current = true;
    setActing({ id, kind });
    void review.run({ kind, id }).finally(() => {
      actingRef.current = false;
      setActing(null);
    });
  };

  const remove = useAdminAction({
    action: (ids: number[]) => deleteJoinRequests(ids),
    invalidate: [["channel-join"]],
    successText: (result) => {
      const ok = result.outcomes.filter((o) => o.ok).length;
      const failed = result.outcomes.length - ok;
      return failed === 0
        ? `已删除 ${ok} 条记录。`
        : `已删除 ${ok} 条，${failed} 条无法删除（待审批记录须先同意或拒绝）。`;
    },
    onDone: () => setSelectedIDs([]),
  });

  const requests = useQuery({
    queryKey: ["channel-join", "requests", { ...filters, page, pageSize }],
    queryFn: () => fetchJoinRequests({ ...filters, page, page_size: pageSize }),
  });
  // 任一审批在途或列表刷新中（含同意成功后的重拉窗口）都锁审批按钮；
  // 失败时不触发刷新，isFetching 回落 false 后按钮恢复，可重试。
  const reviewBusy = acting !== null || requests.isFetching;

  const columns: ColumnsType<JoinRequestRow> = [
    {
      title: "频道",
      dataIndex: "channel_title",
      render: (v: string) => titleText(v) || "（标题未知）",
    },
    { title: "邀请链接", dataIndex: "masked_hash", render: (v: string) => <Text code>{`+${v}`}</Text> },
    { title: "申请用户", dataIndex: "user_id", render: (id: number) => `ID ${id}` },
    { title: "状态", dataIndex: "status", render: statusTag },
    { title: "申请时间", dataIndex: "requested_at", render: (v: number) => fmtTime(v) },
    {
      title: "审批",
      key: "review",
      fixed: "right",
      render: (_, row) =>
        row.status === "pending" ? (
          <Space>
            <Button
              size="small"
              type="primary"
              loading={acting?.id === row.id && acting?.kind === "approve"}
              disabled={reviewBusy}
              onClick={() => handleReview("approve", row.id)}
            >
              同意
            </Button>
            <Button
              size="small"
              danger
              loading={acting?.id === row.id && acting?.kind === "reject"}
              disabled={reviewBusy}
              onClick={() => handleReview("reject", row.id)}
            >
              拒绝
            </Button>
          </Space>
        ) : (
          <Space>
            <Text type="secondary">
              {fmtTime(row.reviewed_at)}
              {row.note ? `（${row.note}）` : ""}
            </Text>
            <Popconfirm
              title="确认删除该记录？"
              description="删除后不可恢复。"
              okText="删除"
              cancelText="取消"
              onConfirm={() => void remove.run([row.id])}
            >
              <Button size="small" danger loading={remove.pending}>
                删除
              </Button>
            </Popconfirm>
          </Space>
        ),
    },
  ];

  const rowSelection: TableRowSelection<JoinRequestRow> = {
    selectedRowKeys: selectedIDs,
    onChange: (keys) => setSelectedIDs(keys.map(Number)),
    getCheckboxProps: (row) => ({ disabled: row.status === "pending" }),
  };
  const deletableSelected = selectedIDs.filter((id) =>
    (requests.data?.items ?? []).some((row) => row.id === id && row.status !== "pending"),
  );

  return (
    <PageCard
      title="加入审批"
      extra={
        <Space>
          {titleToggle}
          <Button
            icon={<ReloadOutlined />}
            loading={requests.isFetching}
            onClick={() => void requests.refetch()}
          >
            刷新
          </Button>
          <Popconfirm
            title={`确认删除选中的 ${deletableSelected.length} 条记录？`}
            description="仅终态记录会被删除；待审批记录须先同意或拒绝。删除后不可恢复。"
            okText="确认删除"
            cancelText="取消"
            onConfirm={() => void remove.run(deletableSelected)}
          >
            <Button danger disabled={deletableSelected.length === 0} loading={remove.pending}>
              批量删除（{deletableSelected.length}）
            </Button>
          </Popconfirm>
        </Space>
      }
    >
      <Space direction="vertical" size="small" className="field-width-full">
        <Form<ApprovalFormValues>
          form={form}
          layout="inline"
          initialValues={{ status: "pending" }}
          onFinish={(values) => {
            applyListFilters(toQueryValues(values), filters, page, setPage, setFilters, requests.refetch);
          }}
        >
          <Form.Item name="status">
            <Select
              placeholder="状态"
              allowClear
              className="field-width-110"
              options={[{ value: "", label: "全部" }, ...STATUS_OPTIONS]}
            />
          </Form.Item>
          <Form.Item name="user_id">
            <InputNumber placeholder="申请用户 ID" min={1} className="field-width-140" />
          </Form.Item>
          <Form.Item name="keyword">
            <Input placeholder="频道标题关键词" allowClear className="field-width-160" />
          </Form.Item>
          <Form.Item name="since">
            <DatePicker placeholder="申请开始日期" maxDate={dayjs()} />
          </Form.Item>
          <Form.Item name="until">
            <DatePicker placeholder="申请结束日期" maxDate={dayjs()} />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit">
              筛选
            </Button>
          </Form.Item>
          <Form.Item>
            <Button
              onClick={() => {
                form.resetFields();
                applyListFilters({ status: "pending" }, filters, page, setPage, setFilters, requests.refetch);
              }}
            >
              重置
            </Button>
          </Form.Item>
        </Form>

        {requests.isError ? (
          <LoadError onRetry={() => void requests.refetch()} />
        ) : requests.isPending ? (
          <Spin className="page-loading" tip="加载中…" />
        ) : (
          <Table<JoinRequestRow>
            rowKey="id"
            size="small"
            columns={columns}
            dataSource={requests.data?.items}
            rowSelection={rowSelection}
            locale={{ emptyText: "没有符合条件的申请记录。" }}
            scroll={{ x: "max-content" }}
            pagination={{
              current: requests.data?.page ?? page,
              pageSize: requests.data?.page_size ?? pageSize,
              total: requests.data?.total ?? 0,
              showSizeChanger: true,
              onChange: (nextPage, nextSize) => {
                setSelectedIDs([]);
                setPage(nextPage);
                setPageSize(nextSize);
              },
            }}
          />
        )}
      </Space>
    </PageCard>
  );
}
