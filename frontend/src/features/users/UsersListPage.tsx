/**
 * 用户列表页（SSR /users 的 SPA 对应实现）。
 * 搜索（ID/用户名/显示名/备注）、状态筛选、服务端分页、CSV 导出与
 * 手动添加用户（默认启用，规则与 SSR /users 表单一致）；
 * 状态中文标签与时间格式复用共享 util。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, Form, Input, InputNumber, Select, Space, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { fetchUsers, type UserRow } from "../../api/admin";
import { addUser, resetUserQuota, setUserStatus, type UserStatusAction } from "../../api/mutations";
import { USER_STATUS_LABELS, USER_STATUS_TAG_COLORS, botLabel, fmtTime, labelOf } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { FilterBar } from "../shared/FilterBar";
import { FormModal } from "../shared/FormModal";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { LoadError } from "../shared/PageStates";
import { applyListFilters } from "../shared/listFilters";
import { RowActions, type RowActionItem } from "../shared/RowActions";

const { Text } = Typography;

interface UsersQuery {
  q: string;
  status: string;
}

/** 手动添加用户表单（SSR：ID 必填正整数，备注 ≤500 字符）。 */
interface AddUserFormValues {
  user_id: number;
  note?: string;
}

const EMPTY_FILTERS: UsersQuery = { q: "", status: "" };

export function UsersListPage() {
  const [form] = Form.useForm<UsersQuery>();
  const [addForm] = Form.useForm<AddUserFormValues>();
  const [filters, setFilters] = useState<UsersQuery>(EMPTY_FILTERS);
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [addOpen, setAddOpen] = useState(false);
  /** 行级目标 key：pending 只让当前行按钮进入 loading。 */
  const [busyUserId, setBusyUserId] = useState<number | null>(null);
  const navigate = useNavigate();
  const confirm = useConfirmAction();

  // 列表快捷操作：与详情页共用同一组写端点、确认文案与失效口径
  const status = useAdminAction({
    action: (vars: { userId: number; action: UserStatusAction }) => {
      setBusyUserId(vars.userId);
      return setUserStatus(vars.userId, vars.action);
    },
    invalidate: [["users"], ["overview"]],
    successText: (result) => `状态已更新为${labelOf(USER_STATUS_LABELS, result.status)}。`,
  });
  const resetQuota = useAdminAction({
    action: (targetId: number) => {
      setBusyUserId(targetId);
      return resetUserQuota(targetId);
    },
    invalidate: [["users"]],
    successText: "当日已用额度已重置。",
  });

  const add = useAdminAction({
    action: (values: AddUserFormValues) => addUser(values),
    invalidate: [["users"], ["overview"]],
    successText: "用户已添加（默认启用）。",
    // 与 SSR 一致：添加后进入新用户详情页；弹窗关闭与表单重置由 FormModal 负责
    onDone: (result) => {
      void navigate(`/users/${result.user_id}`);
    },
  });

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["users", "list", { ...filters, page, pageSize }],
    queryFn: () =>
      fetchUsers({
        q: filters.q || undefined,
        status: filters.status || undefined,
        page,
        page_size: pageSize,
      }),
  });

  const columns: ColumnsType<UserRow> = [
    {
      title: "ID",
      dataIndex: "id",
      key: "id",
      render: (id: number) => <Link to={`/users/${id}`}>{id}</Link>,
    },
    {
      title: "用户名 / 显示名",
      key: "name",
      render: (_, row) => (
        <>
          {row.username ? `@${row.username}` : "—"}
          {row.display_name ? `（${row.display_name}）` : ""}
        </>
      ),
    },
    {
      title: "状态",
      dataIndex: "status",
      key: "status",
      render: (status: string, row) => (
        <>
          <Tag color={USER_STATUS_TAG_COLORS[status]}>{labelOf(USER_STATUS_LABELS, status)}</Tag>
          {row.is_owner ? <Tag color="blue">owner</Tag> : null}
        </>
      ),
    },
    { title: "备注", dataIndex: "note", key: "note", ellipsis: true },
    {
      title: "来源机器人",
      key: "source_bot",
      render: (_, row) => <Text>{botLabel(row.source_bot_id, row.source_bot_username)}</Text>,
    },
    { title: "最近使用", dataIndex: "last_used_at", key: "last_used", render: fmtTime },
    {
      title: "累计请求",
      dataIndex: "total_requests",
      key: "total_requests",
      align: "right",
      render: (n: number) => n,
    },
    {
      title: "操作",
      key: "actions",
      // 统一行操作（RowActions）：导航链接内联，管理动作折叠进「更多」菜单，
      // 右固定 + 表格 max-content，窄屏时横向滚动而非溢出
      fixed: "right",
      width: 150,
      render: (_, row) => {
        // 列表快捷操作：与详情页共用同一组写端点、确认文案与失效口径
        const actions: RowActionItem[] = [
          {
            key: "requests",
            label: "查看请求记录",
            onClick: () => navigate(`/requests?user_id=${row.id}`),
          },
          {
            key: "bindings",
            label: "查看频道绑定",
            onClick: () => navigate(`/channel-bindings?user_id=${row.id}`),
          },
        ];
        // owner 身份不能停用/归档（与详情页、服务端规则一致），不提供入口
        if (row.status === "enabled" && !row.is_owner) {
          actions.push({
            key: "disable",
            label: "禁用",
            danger: true,
            loading: busyUserId === row.id && status.pending,
            disabled: status.pending,
            onClick: () =>
              confirm({
                intent: "warning",
                title: "确认禁用用户",
                content: "确定禁用该用户？其新请求将被立即拒绝。",
                action: () => status.run({ userId: row.id, action: "disable" }),
              }),
          });
        }
        if (row.status !== "archived" && !row.is_owner) {
          actions.push({
            key: "archive",
            label: "归档",
            danger: true,
            loading: busyUserId === row.id && status.pending,
            disabled: status.pending,
            onClick: () =>
              confirm({
                intent: "warning",
                title: "确认归档用户",
                content: "确定归档该用户？立即失去权限，历史记录与统计保留，可恢复。",
                action: () => status.run({ userId: row.id, action: "archive" }),
              }),
          });
        }
        actions.push({
          key: "reset-quota",
          label: "重置今日用量",
          loading: busyUserId === row.id && resetQuota.pending,
          disabled: resetQuota.pending,
          onClick: () =>
            confirm({
              intent: "default",
              title: "确认重置今日用量",
              content: "确定重置该用户今日已用额度为 0？",
              action: () => resetQuota.run(row.id),
            }),
        });
        return <RowActions actions={actions} />;
      },
    },
  ];

  return (
    <PageScaffold
      title="用户管理"
      description="管理访问用户、状态与配额。"
      actions={
        <>
          <Button type="primary" onClick={() => setAddOpen(true)}>
            新增用户
          </Button>
          {/* /users/export.csv 端点与 SSR 一致：始终导出全部用户（不受理筛选参数），
              因此这里不带筛选条件，避免误导为按当前条件导出。 */}
          <Button href="/users/export.csv">导出 CSV</Button>
        </>
      }
    >
      <PageSection>
        <Space direction="vertical" size="middle" className="field-width-full">
          <FilterBar<UsersQuery>
            mode="submit"
            form={form}
            initialValues={EMPTY_FILTERS}
            onFinish={(values) => {
              applyListFilters(
                { q: (values.q ?? "").trim(), status: values.status ?? "" },
                filters,
                page,
                setPage,
                setFilters,
                refetch,
              );
            }}
            onReset={() => {
              applyListFilters(EMPTY_FILTERS, filters, page, setPage, setFilters, refetch);
            }}
          >
            <Form.Item name="q">
              <Input placeholder="ID / 用户名 / 显示名 / 备注" allowClear className="field-width-240" />
            </Form.Item>
            <Form.Item name="status">
              <Select
                className="field-width-120"
                options={[
                  { value: "", label: "全部" },
                  ...Object.entries(USER_STATUS_LABELS).map(([value, label]) => ({ value, label })),
                ]}
              />
            </Form.Item>
          </FilterBar>

          {isError ? (
            <LoadError onRetry={() => void refetch()} />
          ) : (
            <DataTable<UserRow>
              rowKey="id"
              loading={isPending}
              columns={columns}
              dataSource={data?.items}
              emptyText="没有符合条件的用户。"
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
            共 {data?.total ?? 0} 个用户。列表支持快捷禁用/归档与重置今日用量；限额等完整操作在用户详情页执行。
          </Text>
        </Space>
      </PageSection>

      <FormModal<AddUserFormValues>
        title="手动添加用户"
        open={addOpen}
        form={addForm}
        onOpenChange={setAddOpen}
        submitText="添加（默认启用）"
        onSubmit={async (values) => (await add.run(values)) !== undefined}
      >
        <Form.Item
          name="user_id"
          label="Telegram 用户 ID"
          rules={[
            { required: true, message: "用户 ID 必须为正整数。" },
            { type: "integer", min: 1, message: "用户 ID 必须为正整数。" },
          ]}
        >
          <InputNumber placeholder="如 123456789" className="field-width-full" />
        </Form.Item>
        <Form.Item
          name="note"
          label="备注（可选）"
          rules={[{ max: 500, message: "备注最多 500 个字符。" }]}
        >
          <Input maxLength={500} placeholder="选填" />
        </Form.Item>
      </FormModal>
    </PageScaffold>
  );
}
