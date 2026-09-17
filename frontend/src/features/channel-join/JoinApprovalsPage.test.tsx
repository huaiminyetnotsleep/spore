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
  it("渲染唯一 H1 页面标题「加入审批」", async () => {
    mockRequests.mockResolvedValue(pageOf([requestRow()]));
    renderPage();

    expect(
      await screen.findByRole("heading", { level: 1, name: "加入审批" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

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
    // 同意保持行内主操作（primary 按钮两字间插空格），拒绝为链接动作
    expect(screen.getByRole("button", { name: "同 意" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "拒绝" })).toBeInTheDocument();
    await waitFor(() => {
      expect(mockRequests).toHaveBeenCalledWith(
        expect.objectContaining({ status: "pending", page: 1, page_size: 20 }),
      );
    });
  });

  it("后台刷新时保留已加载表格内容", async () => {
    mockRequests
      .mockResolvedValueOnce(pageOf([requestRow()]))
      .mockReturnValueOnce(new Promise(() => undefined));
    renderPage();

    await screen.findByText("***");
    fireEvent.click(screen.getByRole("button", { name: /刷新/ }));

    expect(screen.getByText("***")).toBeInTheDocument();
    expect(screen.getByRole("table")).toBeInTheDocument();
    expect(screen.queryByTestId("page-error")).not.toBeInTheDocument();
  });

  it("筛选提交带条件查询，重置恢复默认待审批筛选", async () => {
    mockRequests.mockResolvedValue(pageOf([requestRow()]));
    renderPage();

    await screen.findByText("***");

    // 提交筛选：状态 + 用户 ID
    fireEvent.change(screen.getByPlaceholderText("申请用户 ID"), { target: { value: "100" } });
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));
    await waitFor(() => {
      expect(mockRequests).toHaveBeenLastCalledWith(
        expect.objectContaining({ status: "pending", user_id: 100, page: 1, page_size: 20 }),
      );
    });

    // 重置：恢复仅默认待审批筛选，输入框清空
    fireEvent.click(screen.getByRole("button", { name: "重 置" }));
    await waitFor(() => {
      expect(mockRequests).toHaveBeenLastCalledWith(
        expect.objectContaining({ status: "pending", page: 1, page_size: 20 }),
      );
    });
    expect(screen.getByPlaceholderText("申请用户 ID")).toHaveValue("");
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
    expect(screen.getByRole("button", { name: "删除" })).toBeInTheDocument();
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

  it("拒绝审批需确认，并在请求完成前保持确认 pending", async () => {
    let resolveReject: (value: unknown) => void = () => undefined;
    mockReject.mockReturnValue(
      new Promise((resolve) => {
        resolveReject = resolve;
      }) as never,
    );
    mockRequests.mockResolvedValue(pageOf([requestRow()]));
    renderPage();

    // 拒绝为 RowActions 链接动作（link 按钮两个汉字间不插空格）
    fireEvent.click(await screen.findByRole("button", { name: "拒绝" }));
    expect(mockReject).not.toHaveBeenCalled();
    const confirmButton = await screen.findByRole("button", { name: "确 认" });
    fireEvent.click(confirmButton);

    await waitFor(() => expect(mockReject).toHaveBeenCalledWith(1));
    await waitFor(() => expect(confirmButton).toHaveClass("ant-btn-loading"));

    resolveReject({
      ok: true,
      request: { id: 1, user_id: 100, channel_title: "私有频道", status: "rejected" },
    });
    await waitFor(() =>
      expect(screen.queryByText(/确定拒绝该加入申请/)).not.toBeInTheDocument(),
    );
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
    // 批量删除为危险操作：使用 danger Modal 二次确认（与单条删除同一危险语义）
    const confirmButton = await screen.findByRole("button", { name: /确认删除/ });
    expect(confirmButton).toHaveClass("ant-btn-dangerous");
    fireEvent.click(confirmButton);
    await waitFor(() => {
      expect(mockDelete).toHaveBeenCalledWith([1, 2]);
    });
  });

  it("单条删除仅目标行 pending，部分结果使用 warning", async () => {
    let resolveDelete: (value: unknown) => void = () => undefined;
    mockDelete.mockReturnValue(
      new Promise((resolve) => {
        resolveDelete = resolve;
      }) as never,
    );
    mockRequests.mockResolvedValue(
      pageOf([
        requestRow({ id: 1, status: "approved", reviewed_at: 1757030500000 }),
        requestRow({ id: 2, status: "rejected", reviewed_at: 1757030500000 }),
      ]),
    );
    renderPage();

    const deleteButtons = await screen.findAllByRole("button", { name: "删除" });
    fireEvent.click(deleteButtons[0]);
    // 单条删除确认弹层的 OK 按钮文案为「删除」（普通按钮两字间插空格）
    const confirmButton = await screen.findByRole("button", { name: "删 除" });
    fireEvent.click(confirmButton);

    await waitFor(() => expect(mockDelete).toHaveBeenCalledWith([1]));
    await waitFor(() => expect(deleteButtons[0]).toHaveClass("ant-btn-loading"));
    expect(deleteButtons[1]).not.toHaveClass("ant-btn-loading");

    resolveDelete({ ok: true, outcomes: [{ id: 1, ok: false, error: "conflict" }] });
    expect(await screen.findByText(/已删除 0 条，1 条无法删除/)).toBeInTheDocument();
    expect(document.querySelector(".ant-message-warning")).toBeInTheDocument();
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
