import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  fetchNotificationEventCatalog,
  fetchNotificationPolicy,
  type NotificationPolicy,
} from "../../api/admin";
import { saveNotificationPolicy } from "../../api/mutations";
import { PolicySettings } from "./PolicySettings";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchNotificationEventCatalog: vi.fn(),
    fetchNotificationPolicy: vi.fn(),
  };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return { ...actual, saveNotificationPolicy: vi.fn() };
});

const fetchCatalogMock = vi.mocked(fetchNotificationEventCatalog);
const fetchPolicyMock = vi.mocked(fetchNotificationPolicy);
const savePolicyMock = vi.mocked(saveNotificationPolicy);

const policy: NotificationPolicy = {
  version: 1,
  minimum_severity: "warn",
  categories: {
    system_alert: { admin_badge: true, bot: true, webhook: true },
  },
  events: {},
};

function renderSettings(eventType?: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AntApp>
        <PolicySettings eventType={eventType} />
      </AntApp>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  fetchCatalogMock.mockReset();
  fetchPolicyMock.mockReset();
  savePolicyMock.mockReset();
});

describe("通知规则", () => {
  it("展示完整事件目录中的未发生事件", async () => {
    fetchPolicyMock.mockResolvedValue(policy);
    fetchCatalogMock.mockResolvedValue([
      {
        type: "disk.temp_usage",
        category: "system_alert",
        type_label: "临时目录占用过高",
        severity: "warn",
        title: "临时目录占用过高",
        description: "从未发生也可提前配置",
        supports_recovery: true,
      },
    ]);

    renderSettings();

    expect(await screen.findByText("disk.temp_usage")).toBeInTheDocument();
    expect(screen.getByText("从未发生也可提前配置")).toBeInTheDocument();
  });

  it("保存单事件三态覆盖", async () => {
    fetchPolicyMock.mockResolvedValue(policy);
    fetchCatalogMock.mockResolvedValue([
      {
        type: "disk.temp_usage",
        category: "system_alert",
        type_label: "临时目录占用过高",
        severity: "warn",
        title: "临时目录占用过高",
        description: "目录事件",
        supports_recovery: true,
      },
    ]);
    savePolicyMock.mockResolvedValue({ ok: true, message: "通知规则已保存。" });

    renderSettings("disk.temp_usage");

    const botSelect = await screen.findByRole("combobox", { name: "Bot" });
    fireEvent.mouseDown(botSelect);
    fireEvent.click(await screen.findByRole("option", { name: "明确关闭" }));
    fireEvent.click(screen.getByRole("button", { name: "应用覆盖" }));
    fireEvent.click(screen.getByRole("button", { name: "保存通知规则" }));

    await waitFor(() =>
      expect(savePolicyMock).toHaveBeenCalledWith(
        expect.objectContaining({
          events: {
            "disk.temp_usage": expect.objectContaining({ bot: "disabled" }),
          },
        }),
      ),
    );
  });
});
