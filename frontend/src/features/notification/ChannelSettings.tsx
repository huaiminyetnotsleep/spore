import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Form,
  Input,
  Radio,
  Select,
  Space,
  Switch,
  Tabs,
  Tag,
  Typography,
} from "antd";

import {
  fetchNotificationConfig,
  type NotificationConfigView,
  type NotificationCredentialState,
  type NotificationMentionMode,
  type NotificationWebhookFormat,
} from "../../api/admin";
import {
  fetchNotificationBotChatID,
  saveNotificationConfig,
  testNotification,
  type NotificationConfigSaveInput,
  type NotificationWebhookInput,
} from "../../api/mutations";
import { useAdminAction } from "../shared/actions";
import { FormActions } from "../shared/FormActions";
import { PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import {
  WEBHOOK_FORMAT_OPTIONS,
  webhookFormatDescriptor,
} from "./webhookFormats";

const { Text } = Typography;

interface NotificationWebhookFormConfig {
  url?: string;
  secret?: string;
  mention_mode?: NotificationMentionMode;
  generic_signature_header?: string;
  open_ids?: string;
  mobiles?: string;
  user_ids?: string;
  role_ids?: string;
}

interface NotificationFormValues {
  automatic_events?: boolean;
  bot?: {
    enabled?: boolean;
    token?: string;
    chat_id?: string;
  };
  webhook?: {
    enabled?: boolean;
    format?: NotificationWebhookFormat;
    config?: NotificationWebhookFormConfig;
  };
}

function credentialStatus(hasCredential: boolean, state?: NotificationCredentialState) {
  if (state && !state.available) return "unavailable" as const;
  return hasCredential ? ("configured" as const) : ("missing" as const);
}

function credentialPlaceholder(
  status: "missing" | "configured" | "unavailable",
  emptyText: string,
): string {
  if (status === "unavailable") return "已保存但当前不可用，请重新填写";
  if (status === "configured") return "已保存，留空则沿用";
  return emptyText;
}

function credentialTag(
  status: "missing" | "configured" | "unavailable",
  label?: string,
) {
  const suffix = label ? ` · ${label}` : "";
  if (status === "configured") return <Tag color="green">已配置{suffix}</Tag>;
  if (status === "unavailable") return <Tag color="red">凭据不可用{suffix}</Tag>;
  return <Tag>未配置{suffix}</Tag>;
}

function joinList(values?: string[]): string {
  return values?.join(", ") ?? "";
}

function splitList(value?: string): string[] {
  return (value ?? "")
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

function savedMentionMode(
  format: NotificationWebhookFormat,
  webhook: NotificationConfigView["webhook"],
): NotificationMentionMode {
  if (format === "feishu") return webhook.feishu_at_all ? "all" : webhook.feishu_open_ids?.length ? "users" : "none";
  if (format === "dingtalk") return webhook.dingtalk_at_all ? "all" : webhook.dingtalk_mobiles?.length ? "users" : "none";
  if (format === "discord") {
    if (webhook.discord_everyone) return "all";
    if (webhook.discord_role_ids?.length) return "roles";
    if (webhook.discord_user_ids?.length) return "users";
  }
  return "none";
}

export function ChannelSettings() {
  const [form] = Form.useForm<NotificationFormValues>();
  const [activeTab, setActiveTab] = useState("bot");
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["notification", "channels"],
    queryFn: fetchNotificationConfig,
  });
  const format =
    Form.useWatch(["webhook", "format"], form) ?? data?.webhook.format ?? "generic";
  const mentionMode =
    Form.useWatch(["webhook", "config", "mention_mode"], form) ?? "none";
  const descriptor = webhookFormatDescriptor(format);
  const botCredentialStatus = credentialStatus(data?.bot.has_token ?? false, data?.bot.credential);
  const webhookURLStatus = credentialStatus(
    data?.webhook.has_url ?? false,
    data?.webhook.credential,
  );
  const webhookSecretStatus = credentialStatus(
    data?.webhook.has_secret ?? false,
    data?.webhook.credential,
  );

  useEffect(() => {
    if (!data) return;
    form.setFieldsValue({
      automatic_events: data.automatic_events ?? false,
      bot: {
        enabled: data.bot.enabled,
        token: "",
        chat_id: data.bot.chat_id,
      },
      webhook: {
        enabled: data.webhook.enabled,
        format: data.webhook.format,
        config: {
          url: "",
          secret: "",
          mention_mode: savedMentionMode(data.webhook.format, data.webhook),
          generic_signature_header: data.webhook.generic_signature_header ?? "",
          open_ids: joinList(data.webhook.feishu_open_ids),
          mobiles: joinList(data.webhook.dingtalk_mobiles),
          user_ids: joinList(data.webhook.discord_user_ids),
          role_ids: joinList(data.webhook.discord_role_ids),
        },
      },
    });
  }, [data, form]);

  const save = useAdminAction({
    action: (input: NotificationConfigSaveInput) => saveNotificationConfig(input),
    invalidate: [["notification", "channels"]],
    successText: (result) => result.message || "通知配置已保存。",
    onDone: () => {
      form.setFieldValue(["bot", "token"], "");
      form.setFieldValue(["webhook", "config", "url"], "");
      form.setFieldValue(["webhook", "config", "secret"], "");
    },
  });
  const test = useAdminAction({
    action: (channel: "bot" | "webhook") => testNotification(channel),
    successText: (result) => result.message || "测试消息已发送。",
  });
  const getChatID = useAdminAction({
    action: () => fetchNotificationBotChatID(),
    successText: (result) => result.message || "已获取最近会话 Chat ID。",
    onDone: (result) => form.setFieldValue(["bot", "chat_id"], result.chat_id),
  });

  const submit = (values: NotificationFormValues) => {
    const webhookFormat = values.webhook?.format ?? "generic";
    const currentDescriptor = webhookFormatDescriptor(webhookFormat);
    const rawConfig = values.webhook?.config;
    const mode = rawConfig?.mention_mode ?? "none";
    const webhook: NotificationWebhookInput = {
      enabled: values.webhook?.enabled ?? false,
      format: webhookFormat,
      url: rawConfig?.url?.trim() ?? "",
      secret: currentDescriptor.secret ? (rawConfig?.secret ?? "") : "",
    };
    if (webhookFormat === "generic") {
      webhook.generic_signature_header = rawConfig?.generic_signature_header?.trim() ?? "";
    }
    if (webhookFormat === "feishu") {
      webhook.feishu_open_ids = mode === "users" ? splitList(rawConfig?.open_ids) : [];
      webhook.feishu_at_all = mode === "all";
    }
    if (webhookFormat === "dingtalk") {
      webhook.dingtalk_mobiles = mode === "users" ? splitList(rawConfig?.mobiles) : [];
      webhook.dingtalk_at_all = mode === "all";
    }
    if (webhookFormat === "discord") {
      webhook.discord_user_ids = mode === "users" ? splitList(rawConfig?.user_ids) : [];
      webhook.discord_role_ids = mode === "roles" ? splitList(rawConfig?.role_ids) : [];
      webhook.discord_everyone = mode === "all";
    }

    void save.run({
      automatic_events: values.automatic_events ?? false,
      bot: {
        enabled: values.bot?.enabled ?? false,
        token: values.bot?.token ?? "",
        chat_id: values.bot?.chat_id?.trim() ?? "",
      },
      webhook,
    });
  };

  const selectedMention = descriptor.mentionOptions.find(
    (option) => option.value === mentionMode,
  );
  const mentionField = selectedMention?.fieldKey
    ? descriptor.textFields[selectedMention.fieldKey]
    : undefined;

  return (
      <PageQueryState
        initialLoading={isPending && !data}
        error={isError && !data}
        hasData={!!data}
        onRetry={() => void refetch()}
      >
        <Form<NotificationFormValues>
          form={form}
          layout="vertical"
          className="field-width-full settings-form"
          onFinish={submit}
        >
          <PageSection
            title="自动事件通知"
            description="开启后，系统事件按通知规则发送到已启用的 Bot 与 Webhook；关闭后仍保留兼容的管理员 Bot 通知，事件中心记录不受影响。"
          >
            <Form.Item
              name="automatic_events"
              valuePropName="checked"
              label="启用真实系统事件自动通知"
              className="layout-margin-bottom-0"
            >
              <Switch aria-label="启用真实系统事件自动通知" />
            </Form.Item>
          </PageSection>
          <Tabs
            activeKey={activeTab}
            onChange={setActiveTab}
            items={[
              {
                key: "bot",
                label: "Bot",
                children: (
                  <PageSection
                    title="Telegram Bot"
                    description="使用独立 Bot Token 向指定 Chat ID 发送通知。"
                    extra={data ? credentialTag(botCredentialStatus) : null}
                  >
                    <Space direction="vertical" size="middle" className="field-width-full">
                      {data?.bot.credential.available === false ? (
                        <Alert
                          type="error"
                          showIcon
                          message={data.bot.credential.message ?? "Bot 凭据不可用，请重新填写并保存。"}
                        />
                      ) : null}
                      <Form.Item name={["bot", "enabled"]} valuePropName="checked" label="启用 Bot 通知">
                        <Switch aria-label="启用 Bot 通知" />
                      </Form.Item>
                      <Form.Item
                        name={["bot", "token"]}
                        label="Bot Token"
                        extra="Token 不会回显；留空保存表示沿用已保存 Token。"
                      >
                        <Input.Password
                          autoComplete="new-password"
                          placeholder={credentialPlaceholder(
                            botCredentialStatus,
                            "请输入 Telegram Bot Token",
                          )}
                        />
                      </Form.Item>
                      <Form.Item name={["bot", "chat_id"]} label="Chat ID">
                        <Input placeholder="例如 -1001234567890" allowClear />
                      </Form.Item>
                      <Space wrap>
                        <Button
                          htmlType="button"
                          loading={getChatID.pending}
                          onClick={() => void getChatID.run(undefined)}
                        >
                          获取 Chat ID
                        </Button>
                        <Button
                          htmlType="button"
                          loading={test.pending}
                          onClick={() => void test.run("bot")}
                        >
                          测试 Bot
                        </Button>
                      </Space>
                      <Alert
                        type="info"
                        showIcon
                        message="获取 Chat ID 与测试发送都只使用服务端已保存的 Bot Token；请先保存新 Token。"
                      />
                    </Space>
                  </PageSection>
                ),
              },
              {
                key: "webhook",
                label: "Webhook",
                children: (
                  <PageSection
                    title="Webhook"
                    description="选择目标格式后仅展示该平台支持的签名与 @ 提醒字段。"
                    extra={
                      data
                        ? credentialTag(
                            webhookURLStatus,
                            webhookFormatDescriptor(data.webhook.format).label,
                          )
                        : null
                    }
                  >
                    <Space direction="vertical" size="middle" className="field-width-full">
                      {data?.webhook.credential.available === false ? (
                        <Alert
                          type="error"
                          showIcon
                          message={data.webhook.credential.message ?? "Webhook 凭据不可用，请重新填写并保存。"}
                        />
                      ) : null}
                      <Form.Item
                        name={["webhook", "enabled"]}
                        valuePropName="checked"
                        label="启用 Webhook 通知"
                      >
                        <Switch aria-label="启用 Webhook 通知" />
                      </Form.Item>
                      <Form.Item name={["webhook", "format"]} label="Webhook 格式">
                        <Select
                          virtual={false}
                          options={WEBHOOK_FORMAT_OPTIONS}
                          onChange={(next: NotificationWebhookFormat) => {
                            const currentEnabled = form.getFieldValue(["webhook", "enabled"]);
                            form.setFieldsValue({
                              webhook: {
                                enabled: currentEnabled,
                                format: next,
                                config: {
                                  url: "",
                                  secret: "",
                                  ...webhookFormatDescriptor(next).defaults,
                                },
                              },
                            });
                          }}
                        />
                      </Form.Item>
                      <Text type="secondary">当前保存格式：{descriptor.label}</Text>
                      <Text type="secondary">{descriptor.description}</Text>
                      <Form.Item
                        name={["webhook", "config", "url"]}
                        label="Webhook URL"
                        extra="URL 属于敏感字段且不会回显；留空保存表示沿用已保存 URL。"
                      >
                        <Input.Password
                          autoComplete="new-password"
                          placeholder={credentialPlaceholder(
                            webhookURLStatus,
                            descriptor.urlPlaceholder,
                          )}
                        />
                      </Form.Item>
                      {descriptor.secret ? (
                        <Form.Item
                          name={["webhook", "config", "secret"]}
                          label={descriptor.secret.label}
                          extra={descriptor.secret.extra}
                        >
                          <Input.Password
                            autoComplete="new-password"
                            placeholder={credentialPlaceholder(
                              webhookSecretStatus,
                              descriptor.secret.placeholder,
                            )}
                          />
                        </Form.Item>
                      ) : null}
                      {descriptor.alwaysFields?.map((fieldKey) => {
                        const field = descriptor.textFields[fieldKey];
                        return field ? (
                          <Form.Item
                            key={field.key}
                            name={["webhook", "config", field.key]}
                            label={field.label}
                            extra={field.extra}
                          >
                            <Input placeholder={field.placeholder} allowClear />
                          </Form.Item>
                        ) : null;
                      })}
                      {descriptor.mentionOptions.length > 0 ? (
                        <Form.Item
                          name={["webhook", "config", "mention_mode"]}
                          label="@ 提醒模式"
                        >
                          <Radio.Group
                            options={descriptor.mentionOptions.map(({ value, label }) => ({
                              value,
                              label,
                            }))}
                          />
                        </Form.Item>
                      ) : null}
                      {mentionField ? (
                        <Form.Item
                          name={["webhook", "config", mentionField.key]}
                          label={mentionField.label}
                          extra={mentionField.extra}
                        >
                          <Input placeholder={mentionField.placeholder} allowClear />
                        </Form.Item>
                      ) : null}
                      <Button
                        htmlType="button"
                        loading={test.pending}
                        onClick={() => void test.run("webhook")}
                      >
                        测试 Webhook
                      </Button>
                      <Alert
                        type="info"
                        showIcon
                        message="测试发送明确使用服务端已保存的 Webhook 配置，不会使用当前表单中尚未保存的内容。"
                      />
                    </Space>
                  </PageSection>
                ),
              },
            ]}
          />
          <FormActions>
            <Button type="primary" htmlType="submit" loading={save.pending}>
              保存通知配置
            </Button>
          </FormActions>
        </Form>
      </PageQueryState>
  );
}
