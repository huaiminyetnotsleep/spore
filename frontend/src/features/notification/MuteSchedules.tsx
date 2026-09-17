import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, DatePicker, Form, Input, Radio, Select, Space, Switch, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import dayjs, { type Dayjs } from "dayjs";

import {
  fetchNotificationEventCatalog,
  fetchNotificationMutes,
  type NotificationChannel,
  type NotificationMute,
  type NotificationMuteMatchMode,
} from "../../api/admin";
import {
  createNotificationMute,
  deleteNotificationMute,
  updateNotificationMute,
  type NotificationMuteInput,
} from "../../api/mutations";
import { fmtTime } from "../../shared/format";
import { useAdminAction, useConfirmAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { FormModal } from "../shared/FormModal";
import { PageSection } from "../shared/PageLayout";
import { PageActions } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { RowActions } from "../shared/RowActions";
import { StatusTag } from "../shared/StatusTag";

const { Text } = Typography;

type StartMode = "now" | "scheduled";
type EndMode = "permanent" | "scheduled";

interface MuteFormValues {
  name: string;
  match_mode: NotificationMuteMatchMode;
  category?: string;
  event_types?: string[];
  channels: NotificationChannel[];
  start_mode: StartMode;
  starts_at?: Dayjs;
  end_mode: EndMode;
  ends_at?: Dayjs;
  enabled: boolean;
}

const CHANNEL_OPTIONS = [
  { value: "admin_badge", label: "管理端提醒" },
  { value: "bot", label: "Bot" },
  { value: "webhook", label: "Webhook" },
] satisfies Array<{ value: NotificationChannel; label: string }>;

function categoryLabel(category: string): string {
  const labels: Record<string, string> = {
    system_alert: "系统告警",
    system_recovery: "系统恢复",
  };
  return labels[category] ?? category;
}

function toFormValues(mute?: NotificationMute): MuteFormValues {
  if (!mute) {
    return {
      name: "",
      match_mode: "all",
      category: "",
      event_types: [],
      channels: ["admin_badge", "bot", "webhook"],
      start_mode: "now",
      end_mode: "scheduled",
      enabled: true,
    };
  }
  return {
    name: mute.name,
    match_mode: mute.match_mode,
    category: mute.category,
    event_types: mute.event_types,
    channels: mute.channels,
    start_mode: mute.starts_at > 0 ? "scheduled" : "now",
    starts_at: mute.starts_at > 0 ? dayjs(mute.starts_at) : undefined,
    end_mode: mute.permanent ? "permanent" : "scheduled",
    ends_at: mute.ends_at > 0 ? dayjs(mute.ends_at) : undefined,
    enabled: mute.enabled,
  };
}

function toInput(values: MuteFormValues): NotificationMuteInput {
  const permanent = values.end_mode === "permanent";
  return {
    name: values.name.trim(),
    match_mode: values.match_mode,
    category: values.match_mode === "category" ? (values.category ?? "") : "",
    event_types: values.match_mode === "events" ? (values.event_types ?? []) : [],
    channels: values.channels,
    starts_at: values.start_mode === "scheduled" ? (values.starts_at?.valueOf() ?? 0) : 0,
    ends_at: permanent ? 0 : (values.ends_at?.valueOf() ?? 0),
    permanent,
    enabled: values.enabled,
  };
}

export function MuteSchedules() {
  const [form] = Form.useForm<MuteFormValues>();
  const [editing, setEditing] = useState<NotificationMute | null>(null);
  const [modalOpen, setModalOpen] = useState(false);
  const confirm = useConfirmAction();

  const mutesQuery = useQuery({
    queryKey: ["notification", "mutes"],
    queryFn: fetchNotificationMutes,
  });
  const catalogQuery = useQuery({
    queryKey: ["notification", "catalog"],
    queryFn: fetchNotificationEventCatalog,
  });

  const categories = useMemo(
    () => Array.from(new Set((catalogQuery.data ?? []).map((item) => item.category))).sort(),
    [catalogQuery.data],
  );

  const createAction = useAdminAction({
    action: (input: NotificationMuteInput) => createNotificationMute(input),
    invalidate: [["notification", "mutes"]],
    successText: "静音计划已创建。",
  });
  const updateAction = useAdminAction({
    action: ({ id, input }: { id: string; input: NotificationMuteInput }) =>
      updateNotificationMute(id, input),
    invalidate: [["notification", "mutes"]],
    successText: "静音计划已更新。",
  });
  const deleteAction = useAdminAction({
    action: (id: string) => deleteNotificationMute(id),
    invalidate: [["notification", "mutes"]],
    successText: "静音计划已删除。",
  });

  const matchMode = Form.useWatch("match_mode", form);
  const startMode = Form.useWatch("start_mode", form);
  const endMode = Form.useWatch("end_mode", form);

  const openCreate = () => {
    setEditing(null);
    form.setFieldsValue(toFormValues());
    setModalOpen(true);
  };
  const openEdit = (mute: NotificationMute) => {
    setEditing(mute);
    form.setFieldsValue(toFormValues(mute));
    setModalOpen(true);
  };

  const columns: ColumnsType<NotificationMute> = [
    {
      title: "名称",
      dataIndex: "name",
      key: "name",
      render: (name: string, row) => (
        <Space direction="vertical" size={0}>
          <Text strong>{name}</Text>
          <Text type="secondary">
            {row.match_mode === "all"
              ? "全部事件"
              : row.match_mode === "category"
                ? categoryLabel(row.category)
                : `${row.event_types.length} 个指定事件`}
          </Text>
        </Space>
      ),
    },
    {
      title: "渠道",
      dataIndex: "channels",
      key: "channels",
      render: (channels: NotificationChannel[]) =>
        channels.map((channel) => CHANNEL_OPTIONS.find((item) => item.value === channel)?.label ?? channel).join("、"),
    },
    {
      title: "时间",
      key: "time",
      render: (_, row) => (
        <Space direction="vertical" size={0}>
          <Text>{row.starts_at > 0 ? fmtTime(row.starts_at) : "立即开始"}</Text>
          <Text type="secondary">{row.permanent ? "永久" : `至 ${fmtTime(row.ends_at)}`}</Text>
        </Space>
      ),
    },
    {
      title: "状态",
      dataIndex: "enabled",
      key: "enabled",
      render: (enabled: boolean) => (
        <StatusTag tone={enabled ? "success" : "default"}>{enabled ? "已启用" : "已停用"}</StatusTag>
      ),
    },
    {
      title: "操作",
      key: "actions",
      render: (_, row) => (
        <RowActions
          actions={[
            { key: "edit", label: "编辑", onClick: () => openEdit(row) },
            {
              key: "delete",
              label: "删除",
              danger: true,
              loading: deleteAction.pending,
              onClick: () =>
                confirm({
                  intent: "danger",
                  title: "删除静音计划",
                  content: `确定删除“${row.name}”吗？`,
                  okText: "删除",
                  action: () => deleteAction.run(row.id),
                }),
            },
          ]}
        />
      ),
    },
  ];

  const hasMutes = !!mutesQuery.data;
  const hasCatalog = !!catalogQuery.data;
  const initialLoading =
    (mutesQuery.isPending && !hasMutes) || (catalogQuery.isPending && !hasCatalog);
  const initialError =
    (mutesQuery.isError && !hasMutes) || (catalogQuery.isError && !hasCatalog);

  return (
    <PageQueryState
      initialLoading={initialLoading}
      refreshing={mutesQuery.isFetching || catalogQuery.isFetching}
      error={initialError}
      hasData={hasMutes && hasCatalog}
      onRetry={() => {
        void mutesQuery.refetch();
        void catalogQuery.refetch();
      }}
    >
      <PageSection
        title="静音计划"
        description="静音只抑制选定渠道的提醒，事件仍会写入事件中心并累计次数。"
        extra={
          <PageActions>
            <Button type="primary" onClick={openCreate}>
              新增静音计划
            </Button>
          </PageActions>
        }
      >
        <DataTable<NotificationMute>
          rowKey="id"
          columns={columns}
          dataSource={mutesQuery.data}
          emptyText="暂无静音计划。"
          pagination={false}
        />
      </PageSection>

      <FormModal<MuteFormValues>
        title={editing ? "编辑静音计划" : "新增静音计划"}
        open={modalOpen}
        form={form}
        onOpenChange={setModalOpen}
        submitText={editing ? "保存" : "创建"}
        formProps={{ layout: "vertical", initialValues: toFormValues() }}
        onSubmit={async (values) => {
          const input = toInput(values);
          const result = editing
            ? await updateAction.run({ id: editing.id, input })
            : await createAction.run(input);
          return !!result;
        }}
      >
        <Form.Item name="name" label="计划名称" rules={[{ required: true, message: "请输入计划名称" }]}>
          <Input maxLength={100} />
        </Form.Item>
        <Form.Item name="enabled" label="启用计划" valuePropName="checked">
          <Switch aria-label="启用计划" />
        </Form.Item>
        <Form.Item name="match_mode" label="静音范围" rules={[{ required: true }]}>
          <Radio.Group
            options={[
              { value: "all", label: "全部事件" },
              { value: "category", label: "指定类别" },
              { value: "events", label: "指定事件" },
            ]}
          />
        </Form.Item>
        {matchMode === "category" ? (
          <Form.Item name="category" label="事件类别" rules={[{ required: true, message: "请选择事件类别" }]}>
            <Select
              virtual={false}
              options={categories.map((category) => ({ value: category, label: categoryLabel(category) }))}
            />
          </Form.Item>
        ) : null}
        {matchMode === "events" ? (
          <Form.Item name="event_types" label="事件类型" rules={[{ required: true, message: "请选择事件类型" }]}>
            <Select
              mode="multiple"
              virtual={false}
              options={(catalogQuery.data ?? []).map((event) => ({
                value: event.type,
                label: `${event.title} · ${event.type_label} (${event.type})`,
              }))}
            />
          </Form.Item>
        ) : null}
        <Form.Item name="channels" label="静音渠道" rules={[{ required: true, message: "请选择至少一个渠道" }]}>
          <Select mode="multiple" virtual={false} options={CHANNEL_OPTIONS} />
        </Form.Item>
        <Form.Item name="start_mode" label="开始时间">
          <Radio.Group
            options={[
              { value: "now", label: "立即开始" },
              { value: "scheduled", label: "指定时间" },
            ]}
          />
        </Form.Item>
        {startMode === "scheduled" ? (
          <Form.Item name="starts_at" label="指定开始时间" rules={[{ required: true, message: "请选择开始时间" }]}>
            <DatePicker showTime className="field-width-full" />
          </Form.Item>
        ) : null}
        <Form.Item name="end_mode" label="结束方式">
          <Radio.Group
            options={[
              { value: "scheduled", label: "指定结束时间" },
              { value: "permanent", label: "永久" },
            ]}
          />
        </Form.Item>
        {endMode === "scheduled" ? (
          <Form.Item name="ends_at" label="结束时间" rules={[{ required: true, message: "请选择结束时间" }]}>
            <DatePicker showTime className="field-width-full" />
          </Form.Item>
        ) : null}
      </FormModal>
    </PageQueryState>
  );
}
