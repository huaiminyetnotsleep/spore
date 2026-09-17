import { useCallback, useEffect } from "react";
import { useQuery } from "@tanstack/react-query";
import { Tabs, Tag } from "antd";
import { useSearchParams } from "react-router-dom";

import { fetchNotificationConfig } from "../../api/admin";
import { PageScaffold } from "../shared/PageLayout";
import { ChannelSettings } from "./ChannelSettings";
import { MuteSchedules } from "./MuteSchedules";
import { NotificationSummary } from "./NotificationSummary";
import { PolicySettings } from "./PolicySettings";

const TAB_KEYS = ["channels", "rules", "mutes"] as const;
type NotificationTab = (typeof TAB_KEYS)[number];

const TAB_LABELS: Record<NotificationTab, string> = {
  channels: "通知渠道",
  rules: "通知规则",
  mutes: "静音计划",
};

function validTab(value: string | null): NotificationTab {
  return TAB_KEYS.includes(value as NotificationTab) ? (value as NotificationTab) : "channels";
}

export function NotificationSettingsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const rawTab = searchParams.get("tab");
  const activeTab = validTab(rawTab);
  const eventType = activeTab === "rules" ? searchParams.get("event") : null;
  const { data: channelConfig } = useQuery({
    queryKey: ["notification", "channels"],
    queryFn: fetchNotificationConfig,
  });

  useEffect(() => {
    if (rawTab === activeTab) return;
    const next = new URLSearchParams(searchParams);
    next.set("tab", activeTab);
    if (activeTab !== "rules") next.delete("event");
    setSearchParams(next, { replace: true });
  }, [activeTab, rawTab, searchParams, setSearchParams]);

  const handleEvent = useCallback(() => {
    const next = new URLSearchParams(searchParams);
    next.delete("event");
    setSearchParams(next, { replace: true });
  }, [searchParams, setSearchParams]);

  return (
    <PageScaffold
      title="通知设置"
      description="分别管理通知通道、事件策略与静音计划；各区域独立加载和保存。"
      status={<Tag>{TAB_LABELS[activeTab]}</Tag>}
    >
      <NotificationSummary config={channelConfig} />
      <Tabs
        activeKey={activeTab}
        onChange={(key) => {
          const nextTab = validTab(key);
          const next = new URLSearchParams(searchParams);
          next.set("tab", nextTab);
          if (nextTab !== "rules") next.delete("event");
          setSearchParams(next);
        }}
        items={[
          { key: "channels", label: TAB_LABELS.channels, children: <ChannelSettings /> },
          {
            key: "rules",
            label: TAB_LABELS.rules,
            children: <PolicySettings eventType={eventType} onEventHandled={handleEvent} />,
          },
          { key: "mutes", label: TAB_LABELS.mutes, children: <MuteSchedules /> },
        ]}
      />
    </PageScaffold>
  );
}
