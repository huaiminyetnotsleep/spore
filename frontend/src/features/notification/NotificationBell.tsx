import { BellOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { Badge, Button } from "antd";
import { useNavigate } from "react-router-dom";

import {
  fetchEvents,
  fetchNotificationEventCatalog,
  fetchNotificationMutes,
  fetchNotificationPolicy,
  type EventRow,
  type NotificationEventCatalogItem,
  type NotificationMute,
  type NotificationPolicy,
} from "../../api/admin";

function muteMatches(mute: NotificationMute, event: EventRow, category: string, now: number): boolean {
  if (!mute.enabled || !mute.channels.includes("admin_badge")) return false;
  if (mute.starts_at > 0 && now < mute.starts_at) return false;
  if (!mute.permanent && (mute.ends_at <= 0 || now >= mute.ends_at)) return false;
  if (mute.match_mode === "all") return true;
  if (mute.match_mode === "category") return mute.category === category;
  return mute.event_types.includes(event.key);
}

function visibleEventCount(
  events: EventRow[],
  policy: NotificationPolicy,
  catalog: NotificationEventCatalogItem[],
  mutes: NotificationMute[],
): number {
  const categories = new Map(catalog.map((item) => [item.type, item.category]));
  const now = Date.now();
  return events.filter((event) => {
    const category = categories.get(event.key) ?? "system_alert";
    const override = policy.events?.[event.key]?.admin_badge ?? "inherit";
    if (override === "disabled") return false;
    if (override !== "enabled" && !policy.categories?.[category]?.admin_badge) return false;
    return !mutes.some((mute) => muteMatches(mute, event, category, now));
  }).length;
}

async function fetchBadgeCount(): Promise<number> {
  const events = await fetchEvents({ status: "open", page: 1, page_size: 200 });
  try {
    const [policy, catalog, mutes] = await Promise.all([
      fetchNotificationPolicy(),
      fetchNotificationEventCatalog(),
      fetchNotificationMutes(),
    ]);
    return visibleEventCount(events.items, policy, catalog, mutes);
  } catch {
    // 策略读取失败时采用故障可见优先：仍显示未解决事件数量，避免
    // 配置损坏把真正的系统告警从管理端入口隐藏。
    return events.total;
  }
}

export function NotificationBell() {
  const navigate = useNavigate();
  const { data, isError } = useQuery({
    queryKey: ["notification", "badge"],
    queryFn: fetchBadgeCount,
    refetchInterval: 60_000,
    refetchIntervalInBackground: false,
    staleTime: 30_000,
  });
  const count = isError ? 0 : data ?? 0;
  const label = count > 0 ? `未解决事件 ${count} 条` : "事件中心";

  return (
    <Badge count={count > 99 ? "99+" : count} showZero={false} overflowCount={99}>
      <Button
        type="text"
        aria-label={label}
        title={label}
        icon={<BellOutlined />}
        onClick={() => navigate("/events?status=open")}
      />
    </Badge>
  );
}
