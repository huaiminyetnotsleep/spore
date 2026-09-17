import { CheckCircleOutlined, DisconnectOutlined } from "@ant-design/icons";
import { Space, Tag, Typography } from "antd";

import type { NotificationConfigView } from "../../api/admin";
import { webhookFormatDescriptor } from "./webhookFormats";

const { Text } = Typography;

interface NotificationSummaryProps {
  config?: NotificationConfigView;
}

function channelTag(label: string, enabled: boolean, detail?: string) {
  if (enabled) {
    return (
      <Tag color="green" icon={<CheckCircleOutlined />}>
        {label}{detail ? ` · ${detail}` : ""}
      </Tag>
    );
  }
  return (
    <Tag icon={<DisconnectOutlined />}>
      {label} 未启用{detail ? ` · ${detail}` : ""}
    </Tag>
  );
}

export function NotificationSummary({ config }: NotificationSummaryProps) {
  if (!config) return null;
  const webhookLabel = webhookFormatDescriptor(config.webhook.format).label;
  return (
    <div className="notification-summary" aria-label="通知配置摘要">
      <Space wrap size={[8, 8]}>
        <Text type="secondary">自动事件通知</Text>
        <Tag color={config.automatic_events ? "green" : "default"}>
          {config.automatic_events ? "已开启" : "已关闭"}
        </Tag>
        {channelTag("Telegram", config.bot.enabled && config.bot.has_token)}
        {channelTag("Webhook", config.webhook.enabled && config.webhook.has_url, webhookLabel)}
      </Space>
    </div>
  );
}
