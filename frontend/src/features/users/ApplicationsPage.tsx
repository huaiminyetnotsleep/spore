/**
 * 申请审批页（SSR /applications 的 SPA 对应实现）。
 * 待审批申请（pending 用户）列表 + 批准/拒绝操作：拒绝为不可逆操作，
 * 保留与 SSR data-confirm 一致的二次确认；审批经 /api/v1 写端点执行，
 * 通知失败（notified=false）时提示管理员手动告知用户，不误报完全成功。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, Space, Table, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useState } from "react";
import { Link } from "react-router-dom";

import { fetchApplications, type ApplicationRow } from "../../api/admin";
import { approveApplication, rejectApplication } from "../../api/mutations";
import { botLabel, fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { LoadError, PageCard } from "../shared/PageStates";

const { Text } = Typography;

/** 与 SSR ?notify=failed 提示同源的受控文案。 */
const NOTIFY_FAILED_TEXT = "审批已生效，但结果通知发送失败（Bot 可能未就绪），请手动告知用户。";

export function ApplicationsPage() {
  const [busyId, setBusyId] = useState<number | null>(null);
  const confirm = useConfirmAction();
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["applications"],
    queryFn: fetchApplications,
  });

  const approve = useAdminAction({
    action: (id: number) => {
      setBusyId(id);
      return approveApplication(id);
    },
    invalidate: [["applications"], ["users"], ["overview"]],
    successText: (result) =>
      result.notified ? "已批准，用户已启用并收到通知。" : NOTIFY_FAILED_TEXT,
  });
  const reject = useAdminAction({
    action: (id: number) => {
      setBusyId(id);
      return rejectApplication(id);
    },
    invalidate: [["applications"], ["users"], ["overview"]],
    successText: "已拒绝该申请。",
  });

  const columns: ColumnsType<ApplicationRow> = [
    {
      title: "用户 ID",
      dataIndex: "id",
      key: "id",
      render: (id: number) => <Link to={`/users/${id}`}>{id}</Link>,
    },
    {
      title: "用户名",
      dataIndex: "username",
      key: "username",
      render: (username: string) => (username ? `@${username}` : "—"),
    },
    {
      title: "显示名",
      dataIndex: "display_name",
      key: "display_name",
      render: (name: string) => name || "—",
    },
    { title: "申请时间", dataIndex: "applied_at", key: "applied_at", render: fmtTime },
    {
      title: "来源机器人",
      key: "source_bot",
      render: (_, row) => <Text>{botLabel(row.source_bot_id, row.source_bot_username)}</Text>,
    },
    {
      title: "操作",
      key: "actions",
      render: (_, row) => (
        <Space>
          <Button
            size="small"
            type="primary"
            loading={busyId === row.id && approve.pending}
            disabled={approve.pending || reject.pending}
            onClick={() => void approve.run(row.id)}
          >
            批准
          </Button>
          <Button
            size="small"
            danger
            loading={busyId === row.id && reject.pending}
            disabled={approve.pending || reject.pending}
            onClick={() =>
              confirm("确定拒绝该申请？用户将收到未通过通知。", () => {
                void reject.run(row.id);
              })
            }
          >
            拒绝
          </Button>
        </Space>
      ),
    },
  ];

  return (
    <PageCard title="申请审批">
      <Space direction="vertical" size="middle" className="field-width-full">
        {isError ? (
          <LoadError onRetry={() => void refetch()} />
        ) : (
          <Table<ApplicationRow>
            rowKey="id"
            size="middle"
            loading={isPending}
            columns={columns}
            dataSource={data?.items}
            locale={{
              emptyText: "当前没有待审批的申请。用户经 Bot 发送 /start 即可产生申请。",
            }}
            pagination={false}
          />
        )}
        <Text type="secondary">
          <Tag color="gold">待审批</Tag>
          批准后用户即可发送消息链接；拒绝后用户保持停用状态，历史记录保留。
        </Text>
      </Space>
    </PageCard>
  );
}
