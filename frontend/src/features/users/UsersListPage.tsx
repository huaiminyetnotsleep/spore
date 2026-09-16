/**
 * 用户列表页（SSR /users 的 SPA 对应实现）。
 * 搜索（ID/用户名/显示名/备注）、状态筛选、服务端分页、CSV 导出与
 * 手动添加用户（默认启用，规则与 SSR /users 表单一致）；
 * 状态中文标签与时间格式复用共享 util。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, Form, Input, InputNumber, Modal, Select, Space, Table, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useState } from "react";
import { Link, useNavigate } from "react-router-dom";

import { fetchUsers, type UserRow } from "../../api/admin";
import { addUser, resetUserQuota, setUserStatus, type UserStatusAction } from "../../api/mutations";
import { USER_STATUS_LABELS, USER_STATUS_TAG_COLORS, botLabel, fmtTime, labelOf } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { applyListFilters } from "../shared/listFilters";
import { LoadError, PageCard } from "../shared/PageStates";

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

export function UsersListPage() {
  const [form] = Form.useForm<UsersQuery>();
  const [addForm] = Form.useForm<AddUserFormValues>();
  const [filters, setFilters] = useState<UsersQuery>({ q: "", status: "" });
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [addOpen, setAddOpen] = useState(false);
  const navigate = useNavigate();
  const confirm = useConfirmAction();

  // 列表快捷操作：与详情页共用同一组写端点、确认文案与失效口径
  const status = useAdminAction({
    action: (vars: { userId: number; action: UserStatusAction }) =>
      setUserStatus(vars.userId, vars.action),
    invalidate: [["users"], ["overview"]],
    successText: (result) => `状态已更新为${labelOf(USER_STATUS_LABELS, result.status)}。`,
  });
  const resetQuota = useAdminAction({
    action: (targetId: number) => resetUserQuota(targetId),
    invalidate: [["users"]],
    successText: "当日已用额度已重置。",
  });

  const add = useAdminAction({
    action: (values: AddUserFormValues) => addUser(values),
    invalidate: [["users"], ["overview"]],
    successText: "用户已添加（默认启用）。",
    onDone: (result) => {
      setAddOpen(false);
      addForm.resetFields();
      // 与 SSR 一致：添加后进入新用户详情页
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
      // 快捷操作按钮组较宽：右固定 + 表格 max-content，窄屏时横向滚动而非溢出
      fixed: "right",
      width: 300,
      render: (_, row) => (
        <Space size="small" wrap>
          <Link to={`/requests?user_id=${row.id}`}>查看请求记录</Link>
          <Link to={`/channel-bindings?user_id=${row.id}`}>查看频道绑定</Link>
          {/* owner 身份不能停用/归档（与详情页、服务端规则一致），不提供入口 */}
          {row.status === "enabled" && !row.is_owner ? (
            <Button
              size="small"
              danger
              loading={status.pending}
              disabled={status.pending}
              onClick={() =>
                confirm("确定禁用该用户？其新请求将被立即拒绝。", () => {
                  void status.run({ userId: row.id, action: "disable" });
                })
              }
            >
              禁用
            </Button>
          ) : null}
          {row.status !== "archived" && !row.is_owner ? (
            <Button
              size="small"
              danger
              loading={status.pending}
              disabled={status.pending}
              onClick={() =>
                confirm("确定归档该用户？立即失去权限，历史记录与统计保留，可恢复。", () => {
                  void status.run({ userId: row.id, action: "archive" });
                })
              }
            >
              归档
            </Button>
          ) : null}
          <Button
            size="small"
            loading={resetQuota.pending}
            disabled={resetQuota.pending}
            onClick={() =>
              confirm("确定重置该用户今日已用额度为 0？", () => {
                void resetQuota.run(row.id);
              })
            }
          >
            重置今日用量
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <PageCard
      title="用户管理"
      extra={
        <Space>
          <Button type="primary" onClick={() => setAddOpen(true)}>
            新增用户
          </Button>
          {/* /users/export.csv 端点与 SSR 一致：始终导出全部用户（不受理筛选参数），
              因此这里不带筛选条件，避免误导为按当前条件导出。 */}
          <Button href="/users/export.csv">导出 CSV</Button>
        </Space>
      }
    >
      <Space direction="vertical" size="middle" className="field-width-full">
        <Form
          form={form}
          layout="inline"
          initialValues={filters}
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
          <Form.Item>
            <Button type="primary" htmlType="submit">
              筛选
            </Button>
          </Form.Item>
        </Form>

        {isError ? (
          <LoadError onRetry={() => void refetch()} />
        ) : (
          <Table<UserRow>
            rowKey="id"
            size="middle"
            loading={isPending}
            columns={columns}
            dataSource={data?.items}
            locale={{ emptyText: "没有符合条件的用户。" }}
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

        <Text type="secondary">
          共 {data?.total ?? 0} 个用户。列表支持快捷禁用/归档与重置今日用量；限额等完整操作在用户详情页执行。
        </Text>
      </Space>

      <Modal
        title="手动添加用户"
        open={addOpen}
        okText="添加（默认启用）"
        cancelText="取消"
        confirmLoading={add.pending}
        onCancel={() => setAddOpen(false)}
        onOk={() => addForm.submit()}
      >
        <Form<AddUserFormValues>
          form={addForm}
          layout="vertical"
          onFinish={(values) => void add.run(values)}
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
        </Form>
      </Modal>
    </PageCard>
  );
}
