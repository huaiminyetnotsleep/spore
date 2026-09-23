/**
 * 错误日志页测试：列表渲染与展开根因、请求 ID 深链筛选、来源筛选生效、
 * 单条与批量删除、按时间段清理弹层（无范围时确认禁用）。
 * API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchErrorLogs, type ErrorLogRow } from "../../api/admin";
import { deleteErrorLogs, deleteErrorLogsRange } from "../../api/mutations";
import { ErrorLogsPage as Page } from "./ErrorLogsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return { ...actual, fetchErrorLogs: vi.fn() };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return { ...actual, deleteErrorLogs: vi.fn(), deleteErrorLogsRange: vi.fn() };
});

const fetchMock = vi.mocked(fetchErrorLogs);
const deleteMock = vi.mocked(deleteErrorLogs);
const deleteRangeMock = vi.mocked(deleteErrorLogsRange);

function logRow(overrides: Partial<ErrorLogRow> = {}): ErrorLogRow {
  return {
    id: 1,
    source: "request",
    code: "BOT_SEND_FAILED",
    stage: "send",
    severity: "error",
    message: "任务失败",
    detail: "FLOOD_WAIT_9: 3000",
    context: { job_id: "j1", bot_id: 42 },
    request_id: 7,
    created_at: 1757030400000,
    ...overrides,
  };
}

function envelope(items: ErrorLogRow[]) {
  return { items, page: 1, page_size: 20, total: items.length };
}

function renderPage(initialEntry = "/error-logs") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <MemoryRouter initialEntries={[initialEntry]}>
          <Page />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

describe("错误日志页", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fetchMock.mockResolvedValue(envelope([logRow()]));
  });

  it("渲染列表：错误码、级别标签、请求链接与来源", async () => {
    renderPage();
    expect(await screen.findByText("任务失败")).toBeInTheDocument();
    expect(screen.getByText("BOT_SEND_FAILED")).toBeInTheDocument();
    expect(screen.getByText("错误")).toBeInTheDocument();
    expect(screen.getByText("请求管线")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "请求 7" })).toHaveAttribute(
      "href",
      "/requests/7",
    );
  });

  it("请求 ID 深链：?request_id= 直接作为初始筛选", async () => {
    renderPage("/error-logs?request_id=7");
    await screen.findByText("任务失败");
    await waitFor(() =>
      expect(fetchMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ request_id: 7, page: 1, page_size: 20 }),
      ),
    );
  });

  it("来源筛选：选择网盘后点查询生效", async () => {
    renderPage();
    await screen.findByText("任务失败");

    // 筛选下拉：combobox[0]=来源、combobox[1]=级别（其后是分页 size changer）
    const sourceSelect = screen.getAllByRole("combobox")[0]
      .closest(".ant-select")
      ?.querySelector(".ant-select-selector");
    expect(sourceSelect).not.toBeNull();
    fireEvent.mouseDown(sourceSelect as HTMLElement);
    const option = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('.ant-select-item-option[title="网盘"]');
      expect(el).not.toBeNull();
      return el as HTMLElement;
    });
    fireEvent.click(option);
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));

    await waitFor(() =>
      expect(fetchMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ source: "cloud", page: 1, page_size: 20 }),
      ),
    );
  });

  it("支持单条删除（确认后调用删除端点）", async () => {
    deleteMock.mockResolvedValueOnce({ ok: true, deleted: 1 });
    renderPage();
    await screen.findByText("任务失败");

    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    fireEvent.click(await screen.findByRole("button", { name: "删 除" }));

    await waitFor(() => expect(deleteMock).toHaveBeenCalledWith([1]));
  });

  it("支持勾选后批量删除", async () => {
    fetchMock.mockResolvedValue(envelope([logRow({ id: 1 }), logRow({ id: 2, message: "第二条" })]));
    deleteMock.mockResolvedValueOnce({ ok: true, deleted: 2 });
    renderPage();
    await screen.findAllByText("任务失败");

    const checkboxes = document.querySelectorAll<HTMLInputElement>(".ant-checkbox-input");
    expect(checkboxes.length).toBeGreaterThanOrEqual(3);
    fireEvent.click(checkboxes[0]);

    fireEvent.click(await screen.findByRole("button", { name: "批量删除" }));
    fireEvent.click(await screen.findByRole("button", { name: "删 除" }));

    await waitFor(() => expect(deleteMock).toHaveBeenCalledWith([1, 2]));
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "批量删除" })).not.toBeInTheDocument();
    });
  });

  it("按时间段清理：弹层打开且未选范围时确认禁用，取消关闭", async () => {
    renderPage();
    await screen.findByText("任务失败");

    fireEvent.click(screen.getByRole("button", { name: "按时间段清理" }));
    const okButton = await screen.findByRole("button", { name: "查条数并清理" });
    expect(okButton).toBeDisabled();
    expect(deleteRangeMock).not.toHaveBeenCalled();

    fireEvent.click(await screen.findByRole("button", { name: "取 消" }));
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "查条数并清理" })).not.toBeInTheDocument();
    });
  });
});
