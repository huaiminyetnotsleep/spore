/**
 * 频道绑定管理页：全部用户的频道绑定列表（含所属用户）、为指定用户绑定
 * 频道（服务端经 Bot API 校验机器人管理员身份）与解绑任意绑定。
 * 数据语义与 internal/binding 服务一致：同一频道只归属一个用户；任务成功
 * 后内容会复制到该用户绑定的频道。
 * 用户筛选为 URL 驱动（?user_id=）：FilterBar 只承载表单，URL 同步由本页负责。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, Form, Input, InputNumber, Space, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";

import { fetchChannelBindings, type ChannelBindingRow } from "../../api/admin";
import { bindChannel, unbindChannel } from "../../api/mutations";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { FilterBar } from "../shared/FilterBar";
import { FormModal } from "../shared/FormModal";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { LoadError } from "../shared/PageStates";
import { RowActions } from "../shared/RowActions";

const { Text } = Typography;

/** 绑定频道表单：归属用户 ID + 频道标识。 */
interface BindFormValues {
  user_id: number;
  target: string;
}

/** 用户筛选表单：提交后写入 URL（?user_id=）。字段名用 owner_id，
 * 避免与绑定表单的 user_id 字段同名（同页双表单同名会干扰 FormModal 重置）。 */
interface OwnerFilterValues {
  owner_id?: string;
}

/** 绑定来源中文标签；未知来源原样展示。 */
const BOUND_VIA_LABELS: Record<string, string> = {
  bot: "Bot 指令",
  web: "管理端",
};

function viaLabel(via: string): string {
  return BOUND_VIA_LABELS[via] ?? via;
}

/** 归属用户列展示：显示名（@用户名）优先，资料缺失退化为用户 ID。 */
function ownerText(row: ChannelBindingRow): string {
  const name = row.user_display_name || row.user_username;
  if (name) {
    return row.user_username && row.user_display_name
      ? `${row.user_display_name}（@${row.user_username}）`
      : name;
  }
  return "—";
}

export function BindingsPage() {
  const [bindForm] = Form.useForm<BindFormValues>();
  const [filterForm] = Form.useForm<OwnerFilterValues>();
  const [bindOpen, setBindOpen] = useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  const userID = useMemo(() => searchParams.get("user_id")?.trim() || "", [searchParams]);
  /** 行级目标 key：解绑 pending 只让当前行按钮进入 loading。 */
  const [busyChannelId, setBusyChannelId] = useState<number | null>(null);
  const confirm = useConfirmAction();

  // 深链 / 其他页面跳转带来的 user_id 变化需要回填筛选输入
  useEffect(() => {
    filterForm.setFieldsValue({ owner_id: userID || undefined });
  }, [filterForm, userID]);

  const bind = useAdminAction({
    action: (values: BindFormValues) =>
      bindChannel({ user_id: values.user_id, target: values.target.trim() }),
    invalidate: [["channel-bindings"], ["users"]],
    successText: "频道已绑定。",
    // 弹窗关闭与表单重置由 FormModal 负责
  });

  const unbind = useAdminAction({
    action: (row: ChannelBindingRow) => {
      setBusyChannelId(row.channel_id);
      return unbindChannel(row.channel_id);
    },
    invalidate: [["channel-bindings"], ["users"]],
    successText: (result) => `已解除绑定「${result.binding.title}」。`,
  });

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["channel-bindings", "list", userID],
    queryFn: () => fetchChannelBindings(userID ? { user_id: userID } : {}),
  });
  const items = data?.items ?? [];

  const columns: ColumnsType<ChannelBindingRow> = [
    {
      title: "所属用户",
      key: "owner",
      render: (_, row) => (
        <Space direction="vertical" size={0}>
          <Link to={`/users/${row.user_id}`}>{row.user_id}</Link>
          <Text type="secondary">{ownerText(row)}</Text>
        </Space>
      ),
    },
    {
      title: "频道",
      key: "channel",
      render: (_, row) => (
        <>
          {row.title || "（未取得标题）"}
          {row.username ? <Text type="secondary">（@{row.username}）</Text> : null}
        </>
      ),
    },
    {
      title: "频道 ID",
      dataIndex: "channel_id",
      key: "channel_id",
      render: (id: number) => <Text copyable>{id}</Text>,
    },
    {
      title: "绑定来源",
      dataIndex: "bound_via",
      key: "bound_via",
      width: 110,
      render: (via: string) =>
        via === "bot" ? <Tag color="blue">{viaLabel(via)}</Tag> : <Tag>{viaLabel(via)}</Tag>,
    },
    {
      title: "路由 bot",
      dataIndex: "bot_id",
      key: "bot_id",
      width: 120,
      render: (botId: number) =>
        botId !== 0 ? (
          <Text type="secondary">{botId}</Text>
        ) : (
          <Tag>任意机器人</Tag>
        ),
    },
    {
      title: "绑定时间",
      dataIndex: "created_at",
      key: "created_at",
      render: fmtTime,
    },
    {
      title: "操作",
      key: "actions",
      width: 72,
      render: (_, row) => (
        <RowActions
          actions={[
            {
              key: "unbind",
              label: "解绑",
              danger: true,
              loading: busyChannelId === row.channel_id && unbind.pending,
              disabled: unbind.pending,
              onClick: () =>
                confirm({
                  intent: "danger",
                  title: "确认解除绑定",
                  content: `确定解除「${row.title || row.channel_id}」的频道绑定？该用户之后收到的内容将不再同步到此频道。`,
                  action: () => unbind.run(row),
                }),
            },
          ]}
        />
      ),
    },
  ];

  return (
    <PageScaffold
      title="频道绑定"
      description="同一频道只归属一个用户；任务成功后内容会复制到该用户绑定的频道。"
      actions={
        <Button type="primary" onClick={() => setBindOpen(true)}>
          绑定频道
        </Button>
      }
    >
      <PageSection>
        <Space direction="vertical" size="middle" className="field-width-full">
          {/* 用户筛选为 URL 驱动（?user_id=）：提交写入 URL，URL 变化驱动查询 */}
          <FilterBar<OwnerFilterValues>
            mode="submit"
            form={filterForm}
            onFinish={(values) => {
              const next = values.owner_id?.trim() || "";
              setSearchParams(next ? { user_id: next } : {});
            }}
            onReset={() => {
              setSearchParams({});
            }}
          >
            <Form.Item name="owner_id">
              <Input placeholder="按用户 ID 筛选" allowClear className="field-width-200" />
            </Form.Item>
          </FilterBar>

          {isError ? (
            <LoadError onRetry={() => void refetch()} />
          ) : (
            <DataTable<ChannelBindingRow>
              rowKey="channel_id"
              loading={isPending}
              columns={columns}
              dataSource={items}
              emptyText="还没有任何频道绑定。"
              pagination={false}
            />
          )}
          <Text type="secondary">
            共 {items.length} 条绑定。用户可经 Bot /bind 自行绑定频道（需先把机器人设为频道管理员）；
            任务成功后，提取内容会同步发送一份到该用户绑定的频道。
          </Text>
        </Space>
      </PageSection>

      <FormModal<BindFormValues>
        title="绑定频道"
        open={bindOpen}
        form={bindForm}
        onOpenChange={setBindOpen}
        submitText="绑定"
        onSubmit={async (values) => (await bind.run(values)) !== undefined}
      >
        <Form.Item
          name="user_id"
          label="所属用户（Telegram 用户 ID）"
          rules={[
            { required: true, message: "用户 ID 必须为正整数。" },
            { type: "integer", min: 1, message: "用户 ID 必须为正整数。" },
          ]}
        >
          <InputNumber placeholder="如 123456789" className="field-width-full" />
        </Form.Item>
        <Form.Item
          name="target"
          label="频道标识"
          rules={[{ required: true, message: "请填写频道用户名、t.me 链接或频道 ID。" }]}
          extra="支持 @mychannel、https://t.me/mychannel 或 -100 开头的频道 ID。"
        >
          <Input placeholder="@mychannel" allowClear />
        </Form.Item>
      </FormModal>
    </PageScaffold>
  );
}
