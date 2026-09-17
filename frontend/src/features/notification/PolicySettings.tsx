import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Button, Drawer, Form, Input, Select, Space, Switch, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";

import {
  fetchNotificationEventCatalog,
  fetchNotificationPolicy,
  type NotificationCategoryPolicy,
  type NotificationEventCatalogItem,
  type NotificationEventPolicy,
  type NotificationOverride,
  type NotificationPolicy,
  type NotificationSeverity,
} from "../../api/admin";
import { saveNotificationPolicy } from "../../api/mutations";
import { useAdminAction } from "../shared/actions";
import { DataTable } from "../shared/DataTable";
import { FilterBar } from "../shared/FilterBar";
import { FormActions } from "../shared/FormActions";
import { PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { RowActions } from "../shared/RowActions";
import { StatusTag, type StatusTone } from "../shared/StatusTag";

const { Text } = Typography;

const CHANNELS = [
  { key: "admin_badge", label: "管理端提醒" },
  { key: "bot", label: "Bot" },
  { key: "webhook", label: "Webhook" },
] as const;

const OVERRIDE_OPTIONS: Array<{ value: NotificationOverride; label: string }> = [
  { value: "inherit", label: "继承类别默认" },
  { value: "enabled", label: "明确开启" },
  { value: "disabled", label: "明确关闭" },
];

const DEFAULT_EVENT_POLICY: NotificationEventPolicy = {
  admin_badge: "inherit",
  bot: "inherit",
  webhook: "inherit",
  recovery: "inherit",
};

const SEVERITY_TONES: Record<NotificationSeverity, StatusTone> = {
  info: "processing",
  warn: "warning",
  error: "error",
};

interface PolicyFormValues {
  minimum_severity: NotificationSeverity;
  categories: Record<string, NotificationCategoryPolicy>;
}

type EventPolicyFormValues = NotificationEventPolicy;

interface CatalogFilters {
  keyword: string;
  category: string;
}

function categoryLabel(category: string): string {
  const labels: Record<string, string> = {
    system_alert: "系统告警",
    system_recovery: "系统恢复",
  };
  return labels[category] ?? category;
}

function normalizeCategoryPolicy(value?: NotificationCategoryPolicy): NotificationCategoryPolicy {
  return value ?? { admin_badge: true, bot: true, webhook: true };
}

function hasExplicitOverride(policy?: NotificationEventPolicy): boolean {
  return !!policy && Object.values(policy).some((value) => value !== "inherit");
}

export interface PolicySettingsProps {
  eventType?: string | null;
  onEventHandled?: () => void;
}

export function PolicySettings({ eventType, onEventHandled }: PolicySettingsProps) {
  const [form] = Form.useForm<PolicyFormValues>();
  const [eventForm] = Form.useForm<EventPolicyFormValues>();
  const [filterForm] = Form.useForm<CatalogFilters>();
  const [filters, setFilters] = useState<CatalogFilters>({ keyword: "", category: "" });
  const [selectedEvent, setSelectedEvent] = useState<NotificationEventCatalogItem | null>(null);
  const [eventPolicies, setEventPolicies] = useState<Record<string, NotificationEventPolicy>>({});
  const eventPoliciesRef = useRef<Record<string, NotificationEventPolicy>>({});

  const policyQuery = useQuery({
    queryKey: ["notification", "policy"],
    queryFn: fetchNotificationPolicy,
  });
  const catalogQuery = useQuery({
    queryKey: ["notification", "catalog"],
    queryFn: fetchNotificationEventCatalog,
  });

  useEffect(() => {
    if (!policyQuery.data) return;
    form.setFieldsValue({
      minimum_severity: policyQuery.data.minimum_severity,
      categories: policyQuery.data.categories,
    });
    const events = policyQuery.data.events ?? {};
    eventPoliciesRef.current = events;
    setEventPolicies(events);
  }, [form, policyQuery.data]);

  useEffect(() => {
    if (!eventType || !catalogQuery.data || !policyQuery.data) return;
    const event = catalogQuery.data.find((item) => item.type === eventType);
    if (event) {
      setFilters({ keyword: event.type, category: "" });
      filterForm.setFieldsValue({ keyword: event.type, category: "" });
      setSelectedEvent(event);
      eventForm.setFieldsValue(eventPoliciesRef.current[event.type] ?? DEFAULT_EVENT_POLICY);
    }
    onEventHandled?.();
  }, [catalogQuery.data, eventForm, eventType, filterForm, onEventHandled, policyQuery.data]);

  const categories = useMemo(
    () => Array.from(new Set((catalogQuery.data ?? []).map((item) => item.category))).sort(),
    [catalogQuery.data],
  );
  const filteredCatalog = useMemo(() => {
    const keyword = filters.keyword.trim().toLowerCase();
    return (catalogQuery.data ?? []).filter((item) => {
      if (filters.category && item.category !== filters.category) return false;
      if (!keyword) return true;
      return [item.type, item.type_label, item.title, item.description]
        .join(" ")
        .toLowerCase()
        .includes(keyword);
    });
  }, [catalogQuery.data, filters]);

  const save = useAdminAction({
    action: (input: NotificationPolicy) => saveNotificationPolicy(input),
    invalidate: [["notification", "policy"]],
    successText: (result) => result.message || "通知规则已保存。",
  });

  const submit = (values: PolicyFormValues) => {
    if (!policyQuery.data) return;
    const categoryPolicies = Object.fromEntries(
      categories.map((category) => [
        category,
        normalizeCategoryPolicy(values.categories?.[category] ?? policyQuery.data.categories[category]),
      ]),
    );
    void save.run({
      version: policyQuery.data.version,
      minimum_severity: values.minimum_severity,
      categories: categoryPolicies,
      events: eventPoliciesRef.current,
    });
  };

  const openEvent = (event: NotificationEventCatalogItem) => {
    setSelectedEvent(event);
    eventForm.setFieldsValue(eventPoliciesRef.current[event.type] ?? DEFAULT_EVENT_POLICY);
  };

  const columns: ColumnsType<NotificationEventCatalogItem> = [
    {
      title: "事件类型",
      dataIndex: "type",
      key: "type",
      render: (type: string, row) => (
        <Space direction="vertical" size={0}>
          <Text strong>{row.title}</Text>
          <Text type="secondary">{row.type_label}</Text>
          <Text code>{type}</Text>
        </Space>
      ),
    },
    {
      title: "类别",
      dataIndex: "category",
      key: "category",
      render: (value: string) => categoryLabel(value),
    },
    {
      title: "级别",
      dataIndex: "severity",
      key: "severity",
      render: (value: NotificationSeverity) => (
        <StatusTag tone={SEVERITY_TONES[value]}>{value}</StatusTag>
      ),
    },
    {
      title: "说明",
      dataIndex: "description",
      key: "description",
    },
    {
      title: "覆盖",
      key: "override",
      render: (_, row) =>
        hasExplicitOverride(eventPolicies[row.type]) ? (
          <StatusTag tone="warning">已覆盖</StatusTag>
        ) : (
          <StatusTag tone="default">继承</StatusTag>
        ),
    },
    {
      title: "操作",
      key: "actions",
      render: (_, row) => (
        <RowActions
          actions={[{ key: "configure", label: "配置", onClick: () => openEvent(row) }]}
        />
      ),
    },
  ];

  const hasPolicy = !!policyQuery.data;
  const hasCatalog = !!catalogQuery.data;
  const initialLoading =
    (policyQuery.isPending && !hasPolicy) || (catalogQuery.isPending && !hasCatalog);
  const initialError =
    (policyQuery.isError && !hasPolicy) || (catalogQuery.isError && !hasCatalog);

  return (
    <PageQueryState
      initialLoading={initialLoading}
      refreshing={policyQuery.isFetching || catalogQuery.isFetching}
      error={initialError}
      hasData={hasPolicy && hasCatalog}
      onRetry={() => {
        void policyQuery.refetch();
        void catalogQuery.refetch();
      }}
    >
      <Form<PolicyFormValues> form={form} layout="vertical" className="settings-form" onFinish={submit}>
        <PageSection
          title="默认通知规则"
          description="最低级别只约束外部 Bot/Webhook；管理端提醒与事件中心记录保持独立。"
        >
          <Form.Item name="minimum_severity" label="最低外部提醒级别">
            <Select
              virtual={false}
              options={[
                { value: "info", label: "信息（info）" },
                { value: "warn", label: "警告（warn）" },
                { value: "error", label: "错误（error）" },
              ]}
            />
          </Form.Item>
          <div className="notification-policy-matrix">
            <div className="notification-policy-matrix__header">类别</div>
            {CHANNELS.map((channel) => (
              <div key={channel.key} className="notification-policy-matrix__header">
                {channel.label}
              </div>
            ))}
            {categories.map((category) => (
              <div key={category} className="notification-policy-matrix__row">
                <div className="notification-policy-matrix__category">{categoryLabel(category)}</div>
                {CHANNELS.map((channel) => (
                  <Form.Item
                    key={channel.key}
                    name={["categories", category, channel.key]}
                    valuePropName="checked"
                    className="layout-margin-bottom-0"
                  >
                    <Switch aria-label={`${categoryLabel(category)} ${channel.label}`} />
                  </Form.Item>
                ))}
              </div>
            ))}
          </div>
        </PageSection>
        <FormActions>
          <Button type="primary" htmlType="submit" loading={save.pending}>
            保存通知规则
          </Button>
        </FormActions>
      </Form>

        <PageSection
          title="事件目录与覆盖"
          description="目录由系统提供，未发生过的事件也可提前配置。"
        >
          <FilterBar<CatalogFilters>
            mode="submit"
            form={filterForm}
            initialValues={filters}
            onFinish={(values) => setFilters({ keyword: values.keyword ?? "", category: values.category ?? "" })}
            onReset={() => setFilters({ keyword: "", category: "" })}
          >
            <Form.Item name="keyword">
              <Input placeholder="搜索事件类型、标题或说明" allowClear />
            </Form.Item>
            <Form.Item name="category">
              <Select
                className="field-width-180"
                virtual={false}
                options={[
                  { value: "", label: "全部类别" },
                  ...categories.map((category) => ({ value: category, label: categoryLabel(category) })),
                ]}
              />
            </Form.Item>
          </FilterBar>
          <DataTable<NotificationEventCatalogItem>
            rowKey="type"
            columns={columns}
            dataSource={filteredCatalog}
            emptyText="没有匹配的事件类型。"
            pagination={{ pageSize: 20 }}
          />
        </PageSection>

      <Drawer
        title={selectedEvent ? `配置事件：${selectedEvent.type_label || selectedEvent.type}` : "配置事件"}
        open={!!selectedEvent}
        width={480}
        className="notification-event-drawer"
        destroyOnHidden
        onClose={() => setSelectedEvent(null)}
      >
        {selectedEvent ? (
          <Form<EventPolicyFormValues>
            form={eventForm}
            layout="vertical"
            initialValues={DEFAULT_EVENT_POLICY}
            onFinish={(values) => {
              const next = { ...eventPoliciesRef.current, [selectedEvent.type]: values };
              eventPoliciesRef.current = next;
              setEventPolicies(next);
              setSelectedEvent(null);
            }}
          >
            <Text type="secondary">{selectedEvent.description}</Text>
            {CHANNELS.map((channel) => (
              <Form.Item key={channel.key} name={channel.key} label={channel.label}>
                <Select virtual={false} options={OVERRIDE_OPTIONS} />
              </Form.Item>
            ))}
            <Form.Item name="recovery" label="恢复通知">
              <Select
                virtual={false}
                disabled={!selectedEvent.supports_recovery}
                options={OVERRIDE_OPTIONS}
              />
            </Form.Item>
            <FormActions>
              <Button
                htmlType="button"
                onClick={() => {
                  const next = { ...eventPoliciesRef.current };
                  delete next[selectedEvent.type];
                  eventPoliciesRef.current = next;
                  setEventPolicies(next);
                  setSelectedEvent(null);
                }}
              >
                恢复默认
              </Button>
              <Button type="primary" htmlType="submit">
                应用覆盖
              </Button>
            </FormActions>
          </Form>
        ) : null}
      </Drawer>
    </PageQueryState>
  );
}
