/**
 * 机器人池管理页（多机器人池）：列表展示 env（只读）与 bots.json（可增删）
 * 来源的机器人及其运行时状态；添加（token 写入 0600 文件）与删除为写操作，
 * 变更后重启进程生效（提示经系统运维页受控重启）。token 只进不出：页面
 * 任何位置不回显 token，添加后输入框即清空。
 */
import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Form, Input, Space, Tag, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import { useState } from "react";

import { fetchBots, type BotRow } from "../../api/admin";
import { addBot, deleteBot, pauseBot, resumeBot } from "../../api/mutations";
import { MTPROTO_STATE_LABELS, botLabel, labelOf } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { FormModal } from "../shared/FormModal";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { LoadError } from "../shared/PageStates";
import { RowActions, type RowActionItem } from "../shared/RowActions";
import { StatusTag, type StatusTone } from "../shared/StatusTag";

const { Text } = Typography;

interface AddBotFormValues {
  token: string;
}

/** MTProto 会话状态 → 语义色调（ready 为正常，其余中性展示）。 */
function mtprotoTone(state: string): StatusTone {
  return state === "ready" ? "success" : "inactive";
}

export function BotsPage() {
  const [form] = Form.useForm<AddBotFormValues>();
  const [addOpen, setAddOpen] = useState(false);
  const confirm = useConfirmAction();
  /** 行级目标 key：暂停/恢复/移除 pending 只让当前行按钮进入 loading。 */
  const [busyBotId, setBusyBotId] = useState<number | null>(null);

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["bots"],
    queryFn: fetchBots,
  });

  const add = useAdminAction({
    action: (values: AddBotFormValues) => addBot(values.token.trim()),
    invalidate: [["bots"], ["overview"]],
    successText: (result) => result.restart_hint,
    // 弹窗关闭与表单重置由 FormModal 负责
  });

  const remove = useAdminAction({
    action: (botID: number) => {
      setBusyBotId(botID);
      return deleteBot(botID);
    },
    invalidate: [["bots"], ["overview"]],
    successText: (result) => result.restart_hint,
  });

  // 暂停/恢复：即时生效（暂停停止接收新消息，在途任务由原 bot 正常完成）
  const runtime = useAdminAction({
    action: (vars: { botID: number; paused: boolean }) => {
      setBusyBotId(vars.botID);
      return (vars.paused ? pauseBot : resumeBot)(vars.botID);
    },
    invalidate: [["bots"], ["overview"]],
    successText: (result) => result.message ?? result.restart_hint,
  });

  const columns: ColumnsType<BotRow> = [
    {
      title: "机器人",
      key: "bot",
      render: (_, row) => (
        <Space size={6} wrap>
          {row.primary ? <Tag color="blue">主</Tag> : null}
          <Text>{botLabel(row.bot_id, row.username)}</Text>
          {row.name ? <Text type="secondary">{row.name}</Text> : null}
        </Space>
      ),
    },
    {
      title: "长轮询",
      key: "online",
      render: (_, row) => (
        <Space size={4} wrap>
          {row.restart_pending ? (
            <StatusTag tone="warning">待重启生效</StatusTag>
          ) : (
            <StatusTag tone={row.online ? "success" : "inactive"}>
              {row.online ? "在线" : "离线"}
            </StatusTag>
          )}
          {row.paused ? (
            <StatusTag tone="warning" data-testid={`bot-paused-${row.bot_id}`}>
              已暂停
            </StatusTag>
          ) : null}
          {row.conflict ? (
            <StatusTag tone="error" data-testid={`bot-conflict-${row.bot_id}`}>
              收不到消息
            </StatusTag>
          ) : null}
          {row.disabled ? (
            <StatusTag tone="error" data-testid={`bot-disabled-${row.bot_id}`}>
              已停用（Token 失效）
            </StatusTag>
          ) : null}
        </Space>
      ),
    },
    {
      title: "MTProto 直传",
      key: "mtproto",
      render: (_, row) =>
        row.mtproto_state ? (
          <StatusTag tone={mtprotoTone(row.mtproto_state)}>
            {labelOf(MTPROTO_STATE_LABELS, row.mtproto_state)}
          </StatusTag>
        ) : (
          "—"
        ),
    },
    {
      title: "来源",
      key: "source",
      render: (_, row) =>
        row.source === "env" ? <Tag>环境变量</Tag> : <Tag color="geekblue">管理端配置</Tag>,
    },
    {
      title: "操作",
      key: "actions",
      // 统一行操作（RowActions）：暂停经 warning 确认，恢复即时执行；
      // 移除为 danger 且重启后生效；env 来源不提供移除入口（文字说明）
      width: 130,
      render: (_, row) => {
        const actions: RowActionItem[] = [];
        if (!row.restart_pending) {
          actions.push(
            row.paused
              ? {
                  key: "resume",
                  label: "恢复",
                  loading: busyBotId === row.bot_id && runtime.pending,
                  disabled: runtime.pending,
                  onClick: () => void runtime.run({ botID: row.bot_id, paused: false }),
                }
              : {
                  key: "pause",
                  label: "暂停",
                  loading: busyBotId === row.bot_id && runtime.pending,
                  disabled: runtime.pending,
                  onClick: () =>
                    confirm({
                      intent: "warning",
                      title: "确认暂停机器人",
                      content: `确定暂停机器人 ${botLabel(row.bot_id, row.username)}？暂停后停止接收该机器人的新消息（已在处理的任务会正常完成），可随时恢复。`,
                      action: () => runtime.run({ botID: row.bot_id, paused: true }),
                    }),
                },
          );
        }
        if (row.source === "file") {
          actions.push({
            key: "remove",
            label: "移除",
            danger: true,
            loading: busyBotId === row.bot_id && remove.pending,
            disabled: remove.pending,
            onClick: () =>
              confirm({
                intent: "danger",
                title: "确认移除机器人",
                content: `确定移除机器人 ${botLabel(row.bot_id, row.username)}？重启进程后生效；生效前该 bot 仍会继续收发消息。`,
                action: () => remove.run(row.bot_id),
              }),
          });
        }
        return (
          <Space size={4} wrap>
            <RowActions actions={actions} />
            {row.source === "env" ? <Text type="secondary">环境变量配置</Text> : null}
          </Space>
        );
      },
    },
  ];

  return (
    <PageScaffold
      title="机器人池管理"
      description="多机器人池平等服务同一套频道绑定；token 只写入服务端，页面不回显。"
      actions={
        <Button type="primary" onClick={() => setAddOpen(true)}>
          添加机器人
        </Button>
      }
    >
      <PageSection>
        <Space direction="vertical" size="middle" className="field-width-full">
          {data?.need_apply ? (
            <Alert
              type="warning"
              showIcon
              message="有机器人配置变更尚未生效，重启进程后应用（可在「系统运维」页受控重启）。"
              data-testid="bots-restart-hint"
            />
          ) : null}
          {isError ? (
            <LoadError onRetry={() => void refetch()} />
          ) : (
            <DataTable<BotRow>
              rowKey="bot_id"
              loading={isPending}
              columns={columns}
              dataSource={data?.bots}
              emptyText="尚未接入任何机器人。"
              pagination={false}
            />
          )}
          <Text type="secondary">
            多机器人池：所有机器人平等服务同一套频道绑定，用户从任意机器人提交，回复从受理机器人返回。
            新机器人需先在 BotFather 创建并持有 token；绑定频道与缓存频道要求所有机器人都是管理员。
          </Text>
        </Space>
      </PageSection>

      <FormModal<AddBotFormValues>
        title="添加机器人"
        open={addOpen}
        form={form}
        onOpenChange={setAddOpen}
        submitText="添加"
        onSubmit={async (values) => (await add.run(values)) !== undefined}
      >
        <Form.Item
          name="token"
          label="Bot Token"
          rules={[
            { required: true, message: "请输入 BotFather 下发的 token" },
            { pattern: /^\d+:[A-Za-z0-9_-]{20,}$/, message: "token 格式无效（形如 123456:ABC…）" },
          ]}
          extra="token 仅写入服务端数据目录的 0600 配置文件，不会出现在页面、日志或备份中；保存后需重启生效。"
        >
          <Input.Password placeholder="123456789:AA…" autoComplete="off" />
        </Form.Item>
      </FormModal>
    </PageScaffold>
  );
}
