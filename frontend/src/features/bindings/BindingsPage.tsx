/**
 * 频道绑定管理页：全部用户的频道绑定列表（含所属用户）、为指定用户绑定
 * 频道（服务端经 Bot API 校验机器人管理员身份）与解绑任意绑定。
 * 数据语义与 internal/binding 服务一致：同一频道只归属一个用户；任务成功
 * 后内容会复制到该用户绑定的频道。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, Form, Input, InputNumber, Modal, Space, Table, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";

import { fetchChannelBindings, type ChannelBindingRow } from "../../api/admin";
import { bindChannel, unbindChannel } from "../../api/mutations";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { LoadError, PageCard } from "../shared/PageStates";

const { Text } = Typography;

/** 绑定频道表单：归属用户 ID + 频道标识。 */
interface BindFormValues {
  user_id: number;
  target: string;
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
  const [bindOpen, setBindOpen] = useState(false);
  const [searchParams, setSearchParams] = useSearchParams();
  const userID = useMemo(() => searchParams.get("user_id")?.trim() || "", [searchParams]);
  const confirm = useConfirmAction();

  const bind = useAdminAction({
    action: (values: BindFormValues) =>
      bindChannel({ user_id: values.user_id, target: values.target.trim() }),
    invalidate: [["channel-bindings"], ["users"]],
    successText: "频道已绑定。",
    onDone: () => {
      setBindOpen(false);
      bindForm.resetFields();
    },
  });

  const unbind = useAdminAction({
    action: (row: ChannelBindingRow) => unbindChannel(row.channel_id),
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
      title: "绑定时间",
      dataIndex: "created_at",
      key: "created_at",
      render: fmtTime,
    },
    {
      title: "操作",
      key: "actions",
      width: 100,
      render: (_, row) => (
        <Button
          size="small"
          danger
          loading={unbind.pending}
          disabled={unbind.pending}
          onClick={() =>
            confirm(
              `确定解除「${row.title || row.channel_id}」的频道绑定？该用户之后收到的内容将不再同步到此频道。`,
              () => {
                void unbind.run(row);
              },
            )
          }
        >
          解绑
        </Button>
      ),
    },
  ];

  return (
    <PageCard
      title="频道绑定"
      extra={
        <Button type="primary" onClick={() => setBindOpen(true)}>
          绑定频道
        </Button>
      }
    >
      <Space direction="vertical" size="middle" className="field-width-full">
        {userID ? (
          <Space>
            <Text type="secondary">当前用户 ID：{userID}</Text>
            <Button size="small" onClick={() => setSearchParams({})}>清除筛选</Button>
          </Space>
        ) : null}
        {isError ? (
          <LoadError onRetry={() => void refetch()} />
        ) : (
          <Table<ChannelBindingRow>
            rowKey="channel_id"
            size="middle"
            loading={isPending}
            columns={columns}
            dataSource={items}
            locale={{ emptyText: "还没有任何频道绑定。" }}
            scroll={{ x: "max-content" }}
            pagination={false}
          />
        )}
        <Text type="secondary">
          共 {items.length} 条绑定。用户可经 Bot /bind 自行绑定频道（需先把机器人设为频道管理员）；
          任务成功后，提取内容会同步发送一份到该用户绑定的频道。
        </Text>
      </Space>

      <Modal
        title="绑定频道"
        open={bindOpen}
        okText="绑定"
        cancelText="取消"
        confirmLoading={bind.pending}
        onCancel={() => setBindOpen(false)}
        onOk={() => bindForm.submit()}
      >
        <Form<BindFormValues>
          form={bindForm}
          layout="vertical"
          onFinish={(values) => void bind.run(values)}
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
        </Form>
      </Modal>
    </PageCard>
  );
}
