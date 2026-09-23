/**
 * 监听记录页测试：查询条件（源/方式，草稿态 + 查询按钮生效）、重置恢复
 * 全部、单条与批量删除留痕（确认后调用批量端点）。
 * API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  fetchWatchEvents,
  fetchWatchSources,
  type WatchEventRow,
  type WatchSourceRow,
} from "../../api/admin";
import { deleteWatchEvents } from "../../api/mutations";
import { WatchEventsPage as Page } from "./WatchEventsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchWatchEvents: vi.fn(),
    fetchWatchSources: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return { ...actual, deleteWatchEvents: vi.fn() };
});

const fetchEventsMock = vi.mocked(fetchWatchEvents);
const fetchSourcesMock = vi.mocked(fetchWatchSources);
const deleteEventsMock = vi.mocked(deleteWatchEvents);

function sourceRow(overrides: Partial<WatchSourceRow> = {}): WatchSourceRow {
  return {
    channel_id: -100222,
    title: "私有频道",
    username: "privchan",
    kind: "channel",
    status: "approved",
    enabled: true,
    added_by: 0,
    user_username: "",
    user_display_name: "",
    bot_id: 999,
    bot_username: "test_bot",
    reviewed_by: "",
    created_at: 1757000000000,
    updated_at: 1757000000000,
    prewarm_count: 0,
    prewarm_last_at: 0,
    ...overrides,
  };
}

function eventRow(overrides: Partial<WatchEventRow> = {}): WatchEventRow {
  return {
    id: 1,
    channel_id: -100222,
    username: "privchan",
    title: "私有频道",
    message_id: 11,
    member_ids: [11],
    message_url: "https://t.me/privchan/11",
    dump_ids: [101],
    request_id: 0,
    bot_id: 999,
    bot_username: "test_bot",
    path: "copy",
    created_at: 1757030400000,
    ...overrides,
  };
}

function envelope(items: WatchEventRow[]) {
  return { items, page: 1, page_size: 20, total: items.length };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <MemoryRouter>
          <Page />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

describe("监听记录页", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fetchSourcesMock.mockResolvedValue({ items: [sourceRow()] });
    fetchEventsMock.mockResolvedValue(envelope([eventRow()]));
  });

  it("查询条件（源/方式）点击查询后生效，重置恢复全部", async () => {
    renderPage();
    await screen.findByText("私有频道");

    // 筛选下拉：combobox[0]=源、combobox[1]=方式（其后是分页 size changer）
    const pathSelect = screen.getAllByRole("combobox")[1]
      .closest(".ant-select")
      ?.querySelector(".ant-select-selector");
    expect(pathSelect).not.toBeNull();
    fireEvent.mouseDown(pathSelect as HTMLElement);
    // antd 的 role=option 命中可访问性副本（无事件处理），
    // 交互需点 .ant-select-item-option（title 为 label）
    const option = await waitFor(() => {
      const el = document.querySelector<HTMLElement>(
        '.ant-select-item-option[title="重传管线"]',
      );
      expect(el).not.toBeNull();
      return el as HTMLElement;
    });
    fireEvent.click(option);
    fireEvent.click(screen.getByRole("button", { name: "查 询" }));

    await waitFor(() =>
      expect(fetchEventsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ path: "fallback", page: 1, page_size: 20 }),
      ),
    );
    await waitFor(() =>
      expect(fetchEventsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ path: "fallback", page: 1, page_size: 20 }),
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: "重 置" }));
    await waitFor(() =>
      expect(fetchEventsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ path: undefined, channel_id: undefined }),
      ),
    );
  });

  it("条件不变时点查询仍触发重新请求（刷新意图）", async () => {
    renderPage();
    await screen.findByText("私有频道");
    expect(fetchEventsMock).toHaveBeenCalledTimes(1);

    // 条件未变：点「查询」强制 refetch 而不是命中相同的 queryKey
    fireEvent.click(screen.getByRole("button", { name: "查 询" }));
    await waitFor(() => expect(fetchEventsMock).toHaveBeenCalledTimes(2));
    fireEvent.click(screen.getByRole("button", { name: "查 询" }));
    await waitFor(() => expect(fetchEventsMock).toHaveBeenCalledTimes(3));
  });

  it("支持单条删除（确认后调用删除端点）", async () => {
    deleteEventsMock.mockResolvedValueOnce({ ok: true, deleted: 1 });
    renderPage();
    await screen.findByText("私有频道");

    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    fireEvent.click(await screen.findByRole("button", { name: "删 除" }));

    await waitFor(() => expect(deleteEventsMock).toHaveBeenCalledWith([1]));
  });

  it("支持勾选后批量删除", async () => {
    fetchEventsMock.mockResolvedValue(
      envelope([eventRow({ id: 1 }), eventRow({ id: 2, message_id: 12 })]),
    );
    deleteEventsMock.mockResolvedValueOnce({ ok: true, deleted: 2 });
    renderPage();
    await screen.findAllByText("私有频道");

    // 表头全选（第一个复选框），出现批量操作条
    const checkboxes = document.querySelectorAll<HTMLInputElement>(".ant-checkbox-input");
    expect(checkboxes.length).toBeGreaterThanOrEqual(3);
    fireEvent.click(checkboxes[0]);

    fireEvent.click(await screen.findByRole("button", { name: "批量删除" }));
    fireEvent.click(await screen.findByRole("button", { name: "删 除" }));

    await waitFor(() => expect(deleteEventsMock).toHaveBeenCalledWith([1, 2]));
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "批量删除" })).not.toBeInTheDocument();
    });
  });
});
