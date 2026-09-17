import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  fetchNotificationEventCatalog,
  fetchNotificationMutes,
  type NotificationMute,
} from "../../api/admin";
import {
  createNotificationMute,
  deleteNotificationMute,
  updateNotificationMute,
} from "../../api/mutations";
import { MuteSchedules } from "./MuteSchedules";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchNotificationEventCatalog: vi.fn(),
    fetchNotificationMutes: vi.fn(),
  };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    createNotificationMute: vi.fn(),
    deleteNotificationMute: vi.fn(),
    updateNotificationMute: vi.fn(),
  };
});

const fetchCatalogMock = vi.mocked(fetchNotificationEventCatalog);
const fetchMutesMock = vi.mocked(fetchNotificationMutes);
const createMuteMock = vi.mocked(createNotificationMute);
const deleteMuteMock = vi.mocked(deleteNotificationMute);
const updateMuteMock = vi.mocked(updateNotificationMute);

function mute(overrides: Partial<NotificationMute> = {}): NotificationMute {
  return {
    id: "mute-1",
    name: "维护窗口",
    match_mode: "all",
    category: "",
    event_types: [],
    channels: ["bot", "webhook"],
    starts_at: 0,
    ends_at: 0,
    permanent: true,
    enabled: true,
    ...overrides,
  };
}

function renderSchedules() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AntApp>
        <MuteSchedules />
      </AntApp>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  fetchCatalogMock.mockReset();
  fetchMutesMock.mockReset();
  createMuteMock.mockReset();
  deleteMuteMock.mockReset();
  updateMuteMock.mockReset();
});

describe("静音计划", () => {
  it("创建永久静音计划", async () => {
    fetchCatalogMock.mockResolvedValue([]);
    fetchMutesMock.mockResolvedValue([]);
    createMuteMock.mockResolvedValue(mute({ name: "夜间维护" }));

    renderSchedules();

    fireEvent.click(await screen.findByRole("button", { name: "新增静音计划" }));
    fireEvent.change(screen.getByLabelText("计划名称"), { target: { value: " 夜间维护 " } });
    fireEvent.click(screen.getByRole("radio", { name: "永久" }));
    fireEvent.click(screen.getByRole("button", { name: /创\s*建/ }));

    await waitFor(() =>
      expect(createMuteMock).toHaveBeenCalledWith(
        expect.objectContaining({
          name: "夜间维护",
          match_mode: "all",
          permanent: true,
          starts_at: 0,
          ends_at: 0,
          enabled: true,
        }),
      ),
    );
  });

  it("确认后删除静音计划", async () => {
    fetchCatalogMock.mockResolvedValue([]);
    fetchMutesMock.mockResolvedValue([mute()]);
    deleteMuteMock.mockResolvedValue({ ok: true });

    renderSchedules();

    fireEvent.click(await screen.findByRole("button", { name: "删除" }));
    const confirmButtons = await screen.findAllByRole("button", { name: /删\s*除/ });
    fireEvent.click(confirmButtons.at(-1) as HTMLElement);

    await waitFor(() => expect(deleteMuteMock).toHaveBeenCalledWith("mute-1"));
  });
});
