import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  fetchEvents,
  fetchNotificationEventCatalog,
  fetchNotificationMutes,
  fetchNotificationPolicy,
  type EventRow,
} from "../../api/admin";
import { NotificationBell } from "./NotificationBell";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchEvents: vi.fn(),
    fetchNotificationEventCatalog: vi.fn(),
    fetchNotificationMutes: vi.fn(),
    fetchNotificationPolicy: vi.fn(),
  };
});

const fetchEventsMock = vi.mocked(fetchEvents);
const fetchCatalogMock = vi.mocked(fetchNotificationEventCatalog);
const fetchMutesMock = vi.mocked(fetchNotificationMutes);
const fetchPolicyMock = vi.mocked(fetchNotificationPolicy);

function eventRow(): EventRow {
  return {
    id: 1,
    key: "bot.poll_conflict",
    severity: "error",
    message: "机器人轮询冲突",
    count: 1,
    first_at: 1_700_000_000_000,
    last_at: 1_700_000_000_000,
    last_notified_at: 0,
    status: "open",
  };
}

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{`${location.pathname}${location.search}`}</output>;
}

function renderBell() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AntApp>
        <MemoryRouter initialEntries={["/overview"]}>
          <NotificationBell />
          <LocationProbe />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  fetchEventsMock.mockReset();
  fetchCatalogMock.mockReset();
  fetchMutesMock.mockReset();
  fetchPolicyMock.mockReset();
});

describe("NotificationBell", () => {
  it("显示未被策略屏蔽的未解决事件数量并跳转事件中心", async () => {
    fetchEventsMock.mockResolvedValue({
      items: [eventRow()], page: 1, page_size: 200, total: 1, total_pages: 1,
    });
    fetchCatalogMock.mockResolvedValue([{
      type: "bot.poll_conflict",
      category: "system_alert",
      type_label: "系统告警",
      severity: "error",
      title: "机器人轮询冲突",
      description: "机器人不可用",
      supports_recovery: true,
    }]);
    fetchPolicyMock.mockResolvedValue({
      version: 1,
      minimum_severity: "warn",
      categories: { system_alert: { admin_badge: true, bot: true, webhook: true } },
      events: {},
    });
    fetchMutesMock.mockResolvedValue([]);

    renderBell();

    const button = await screen.findByRole("button", { name: "未解决事件 1 条" });
    fireEvent.click(button);
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent("/events?status=open"),
    );
  });

  it("策略关闭管理端提醒时隐藏数量", async () => {
    fetchEventsMock.mockResolvedValue({
      items: [eventRow()], page: 1, page_size: 200, total: 1, total_pages: 1,
    });
    fetchCatalogMock.mockResolvedValue([{
      type: "bot.poll_conflict",
      category: "system_alert",
      type_label: "系统告警",
      severity: "error",
      title: "机器人轮询冲突",
      description: "机器人不可用",
      supports_recovery: true,
    }]);
    fetchPolicyMock.mockResolvedValue({
      version: 1,
      minimum_severity: "warn",
      categories: { system_alert: { admin_badge: false, bot: true, webhook: true } },
      events: {},
    });
    fetchMutesMock.mockResolvedValue([]);

    renderBell();

    await screen.findByRole("button", { name: "事件中心" });
    expect(screen.queryByText("1")).not.toBeInTheDocument();
  });
});
