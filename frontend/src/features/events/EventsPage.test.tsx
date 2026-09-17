/**
 * 事件中心页组件测试：唯一 H1、状态筛选提交与重置、表格状态标签渲染、
 * 标记解决携带事件 id 且行级 pending 只影响目标行、错误态重试。
 * API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchEvents, type EventRow, type ListEnvelope } from "../../api/admin";
import { resolveEvent } from "../../api/mutations";
import { EventsPage } from "./EventsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return { ...actual, fetchEvents: vi.fn() };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return { ...actual, resolveEvent: vi.fn() };
});

const fetchEventsMock = vi.mocked(fetchEvents);
const resolveEventMock = vi.mocked(resolveEvent);

function envelope(items: EventRow[]): ListEnvelope<EventRow> {
  return { items, page: 1, page_size: 20, total: items.length, total_pages: 1 };
}

function eventRow(overrides: Partial<EventRow> = {}): EventRow {
  return {
    id: 1,
    key: "mtproto.upload_failed",
    severity: "error",
    message: "上传失败",
    count: 3,
    first_at: 1757030400000,
    last_at: 1757116800000,
    last_notified_at: 1757116800000,
    status: "open",
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <EventsPage />
      </AntApp>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  fetchEventsMock.mockReset();
  resolveEventMock.mockReset();
});

describe("事件中心页", () => {
  it("渲染唯一 H1 页面标题「事件中心」", async () => {
    fetchEventsMock.mockResolvedValue(envelope([eventRow()]));
    renderPage();

    expect(
      await screen.findByRole("heading", { level: 1, name: "事件中心" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  it("渲染事件行：级别、事件 key 与状态中文标签", async () => {
    fetchEventsMock.mockResolvedValue(
      envelope([
        eventRow(),
        eventRow({ id: 2, key: "bot.send_failed", severity: "warn", status: "resolved" }),
      ]),
    );
    renderPage();

    expect(await screen.findByText("mtproto.upload_failed")).toBeInTheDocument();
    expect(screen.getByText("bot.send_failed")).toBeInTheDocument();
    expect(screen.getByText("error")).toBeInTheDocument();
    expect(screen.getByText("warn")).toBeInTheDocument();
    expect(screen.getByText("未解决")).toBeInTheDocument();
    expect(screen.getByText("已解决")).toBeInTheDocument();
    expect(fetchEventsMock).toHaveBeenCalledWith(
      expect.objectContaining({ page: 1, page_size: 20 }),
    );
  });

  it("状态筛选提交带条件查询，重置恢复全部", async () => {
    fetchEventsMock.mockResolvedValue(envelope([eventRow()]));
    renderPage();
    await screen.findByText("mtproto.upload_failed");

    // 状态下拉（第 1 个 combobox 为筛选 Select；其余是分页 size changer）：
    // antd Select 交互 mouseDown 打开选项列表后点选
    const statusSelect = screen.getAllByRole("combobox")[0]
      .closest(".ant-select")
      ?.querySelector(".ant-select-selector");
    expect(statusSelect).not.toBeNull();
    fireEvent.mouseDown(statusSelect as HTMLElement);
    fireEvent.click(await screen.findByRole("option", { name: "已解决" }));
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));

    await waitFor(() =>
      expect(fetchEventsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ status: "resolved", page: 1, page_size: 20 }),
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: "重 置" }));
    await waitFor(() =>
      expect(fetchEventsMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ status: undefined, page: 1, page_size: 20 }),
      ),
    );
  });

  it("标记解决调用 API，行级 pending 只让目标行按钮进入 loading", async () => {
    resolveEventMock.mockReturnValue(new Promise(() => undefined) as never);
    fetchEventsMock.mockResolvedValue(
      envelope([eventRow(), eventRow({ id: 2, key: "bot.send_failed" })]),
    );
    renderPage();

    const resolveButtons = await screen.findAllByRole("button", { name: /标记解决/ });
    expect(resolveButtons).toHaveLength(2);
    fireEvent.click(resolveButtons[0]);

    await waitFor(() => expect(resolveEventMock).toHaveBeenCalledWith(1));
    await waitFor(() => expect(resolveButtons[0]).toHaveClass("ant-btn-loading"));
    expect(resolveButtons[1]).not.toHaveClass("ant-btn-loading");
  });

  it("已解决行不再提供标记解决入口", async () => {
    fetchEventsMock.mockResolvedValue(envelope([eventRow({ status: "resolved" })]));
    renderPage();

    expect(await screen.findByText("已解决")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /标记解决/ })).not.toBeInTheDocument();
  });

  it("加载失败展示错误态，重试会重新发起查询", async () => {
    fetchEventsMock.mockRejectedValueOnce(new Error("boom"));
    fetchEventsMock.mockResolvedValue(envelope([eventRow()]));

    renderPage();

    expect(await screen.findByTestId("page-error")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));

    expect(await screen.findByText("mtproto.upload_failed")).toBeInTheDocument();
    expect(fetchEventsMock).toHaveBeenCalledTimes(2);
  });
});
