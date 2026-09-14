/**
 * 加入审批页组件测试：筛选与分页渲染、同意/删除调用、进行中连点防重、
 * 历史行无审批按钮、pending 行不可勾选删除、频道名默认暗文与显示明文切换。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchJoinRequests, type JoinRequestListPage, type JoinRequestRow } from "../../api/admin";
import { approveJoinRequest, deleteJoinRequests, rejectJoinRequest } from "../../api/mutations";
import { JoinApprovalsPage } from "./JoinApprovalsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return { ...actual, fetchJoinRequests: vi.fn() };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    approveJoinRequest: vi.fn(),
    rejectJoinRequest: vi.fn(),
    deleteJoinRequests: vi.fn(),
  };
});

const mockRequests = vi.mocked(fetchJoinRequests);
const mockApprove = vi.mocked(approveJoinRequest);
const mockReject = vi.mocked(rejectJoinRequest);
const mockDelete = vi.mocked(deleteJoinRequests);

function requestRow(overrides: Partial<JoinRequestRow> = {}): JoinRequestRow {
  return {
    id: 1,
    user_id: 100,
    channel_title: "私有频道",
    status: "pending",
    requested_at: 1757030400000,
    reviewed_at: 0,
    reviewed_by: "",
    note: "",
    masked_hash: "AbCd…5678",
    ...overrides,
  };
}

function pageOf(rows: JoinRequestRow[], total = rows.length): JoinRequestListPage {
  return { items: rows, page: 1, page_size: 20, total, total_pages: 1 };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <JoinApprovalsPage />
      </AntApp>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mockRequests.mockReset();
  mockApprove.mockReset();
  mockReject.mockReset();
  mockDelete.mockReset();
});

describe("加入审批页", () => {
  it("渲染待审批列表与操作按钮，默认按待审批筛选；频道名默认暗文可切换", async () => {
    mockRequests.mockResolvedValue(pageOf([requestRow()]));
    renderPage();

    // 频道名默认脱敏暗文（titleMask），明文经「显示明文」切换
    await waitFor(() => {
      expect(screen.getByText("***")).toBeInTheDocument();
    });
    expect(screen.queryByText("私有频道")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /显示明文/ }));
    await waitFor(() => {
      expect(screen.getByText("私有频道")).toBeInTheDocument();
    });
    expect(screen.getByText(/AbCd…5678/)).toBeInTheDocument();
    expect(screen.getAllByText(/待审批/).length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: "同 意" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "拒 绝" })).toBeInTheDocument();
    await waitFor(() => {
      expect(mockRequests).toHaveBeenCalledWith(
        expect.objectContaining({ status: "pending", page: 1, page_size: 20 }),
      );
    });
  });

  it("历史状态行无审批按钮，但可删除", async () => {
    mockRequests.mockResolvedValue(
      pageOf([requestRow({ status: "approved", reviewed_at: 1757030500000 })]),
    );
    renderPage();

    await waitFor(() => {
      expect(screen.getByText("已通过")).toBeInTheDocument();
    });
    expect(screen.queryByRole("button", { name: "同 意" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /删 除/ })).toBeInTheDocument();
  });

  it("同意审批调用 API", async () => {
    mockRequests.mockResolvedValue(pageOf([requestRow()]));
    mockApprove.mockResolvedValue({
      ok: true,
      request: { id: 1, user_id: 100, channel_title: "私有频道", status: "approved" },
    });
    renderPage();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "同 意" })).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole("button", { name: "同 意" }));
    await waitFor(() => {
      expect(mockApprove).toHaveBeenCalledWith(1);
    });
  });

  it("审批进行中连点只发送一次请求（防重复提交）", async () => {
    let resolveApprove: (v: unknown) => void = () => undefined;
    mockApprove.mockReturnValue(
      new Promise((resolve) => {
        resolveApprove = resolve;
      }) as never,
    );
    mockRequests.mockResolvedValue(pageOf([requestRow()]));
    renderPage();

    const btn = await screen.findByRole("button", { name: "同 意" });
    fireEvent.click(btn);
    fireEvent.click(btn);
    await waitFor(() => {
      expect(mockApprove).toHaveBeenCalledTimes(1);
    });
    resolveApprove({
      ok: true,
      request: { id: 1, user_id: 100, channel_title: "私有频道", status: "approved" },
    });
  });

  it("批量删除仅提交已选中的记录", async () => {
    mockRequests.mockResolvedValue(
      pageOf([
        requestRow({ id: 1, status: "approved", reviewed_at: 1757030500000 }),
        requestRow({ id: 2, status: "rejected", reviewed_at: 1757030500000 }),
      ]),
    );
    mockDelete.mockResolvedValue({
      ok: true,
      outcomes: [
        { id: 1, ok: true },
        { id: 2, ok: true },
      ],
    });
    renderPage();

    // 两行标题相同，用数量断言渲染完成（默认暗文均为 ***）
    await waitFor(() => {
      expect(screen.getAllByText("***").length).toBe(2);
    });
    // 勾选两行后批量删除（第 1 个是表头全选框，随后是两行的行选框）
    const checkboxes = screen.getAllByRole("checkbox");
    fireEvent.click(checkboxes[1]);
    fireEvent.click(checkboxes[2]);
    const batch = screen.getByRole("button", { name: /批量删除/ });
    await waitFor(() => {
      expect(batch).not.toBeDisabled();
    });
    fireEvent.click(batch);
    // Popconfirm 二次确认
    fireEvent.click(await screen.findByRole("button", { name: /确认删除/ }));
    await waitFor(() => {
      expect(mockDelete).toHaveBeenCalledWith([1, 2]);
    });
  });

  it("pending 行复选框禁用（不可勾选删除）", async () => {
    mockRequests.mockResolvedValue(pageOf([requestRow()]));
    renderPage();

    await screen.findByText("***");
    // 第 1 个是表头全选框，第 2 个是行选框
    const checkboxes = screen.getAllByRole("checkbox");
    expect(checkboxes.length).toBe(2);
    expect(checkboxes[1]).toBeDisabled();
    expect(screen.getByRole("button", { name: /批量删除/ })).toBeDisabled();
  });

  it("加载失败展示重试", async () => {
    mockRequests.mockRejectedValue(new Error("boom"));
    renderPage();
    await waitFor(() => {
      expect(screen.getByTestId("page-error")).toBeInTheDocument();
    });
  });
});
