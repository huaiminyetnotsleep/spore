import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchApplications, type ApplicationRow } from "../../api/admin";
import { approveApplication, rejectApplication } from "../../api/mutations";
import { ApplicationsPage } from "./ApplicationsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return { ...actual, fetchApplications: vi.fn() };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    approveApplication: vi.fn(),
    rejectApplication: vi.fn(),
  };
});

const mockApplications = vi.mocked(fetchApplications);
const mockApprove = vi.mocked(approveApplication);
const mockReject = vi.mocked(rejectApplication);

function application(overrides: Partial<ApplicationRow> = {}): ApplicationRow {
  return {
    id: 100,
    username: "applicant",
    display_name: "申请用户",
    applied_at: 1757030400000,
    source_bot_id: 1,
    source_bot_username: "spore_bot",
    ...overrides,
  };
}

function renderPage(rows = [application()]) {
  mockApplications.mockResolvedValue({ items: rows });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AntApp>
        <MemoryRouter>
          <ApplicationsPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mockApplications.mockReset();
  mockApprove.mockReset();
  mockReject.mockReset();
});

describe("申请审批页", () => {
  it("渲染唯一 H1 页面标题「申请审批」", async () => {
    renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "申请审批" })).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  it("通知失败时以 warning 表达部分成功", async () => {
    mockApprove.mockResolvedValue({ ok: true, notified: false, status: "enabled" });
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "批 准" }));

    expect(
      await screen.findByText(/审批已生效，但结果通知发送失败/),
    ).toBeInTheDocument();
    expect(document.querySelector(".ant-message-warning")).toBeInTheDocument();
  });

  it("拒绝操作先确认，并在 Promise 完成前保持确认 pending", async () => {
    let resolveReject: (value: unknown) => void = () => undefined;
    mockReject.mockReturnValue(
      new Promise((resolve) => {
        resolveReject = resolve;
      }) as never,
    );
    renderPage();

    // 拒绝为 RowActions 链接动作（link 按钮两个汉字间不插空格）
    fireEvent.click(await screen.findByRole("button", { name: "拒绝" }));
    expect(mockReject).not.toHaveBeenCalled();

    const confirmButton = await screen.findByRole("button", { name: "确 认" });
    expect(confirmButton).not.toHaveClass("ant-btn-dangerous");
    fireEvent.click(confirmButton);

    await waitFor(() => expect(mockReject).toHaveBeenCalledWith(100));
    await waitFor(() => expect(confirmButton).toHaveClass("ant-btn-loading"));
    expect(screen.getByText("确定拒绝该申请？用户将收到未通过通知。")).toBeInTheDocument();

    resolveReject({ ok: true, notified: true, status: "disabled" });
    await waitFor(() =>
      expect(screen.queryByText("确定拒绝该申请？用户将收到未通过通知。")).not.toBeInTheDocument(),
    );
  });

  it("审批 pending 只在目标行显示 loading", async () => {
    mockApprove.mockReturnValue(new Promise(() => undefined) as never);
    renderPage([application({ id: 100 }), application({ id: 101, username: "second" })]);

    const approveButtons = await screen.findAllByRole("button", { name: "批 准" });
    fireEvent.click(approveButtons[0]);

    await waitFor(() => expect(approveButtons[0]).toHaveClass("ant-btn-loading"));
    expect(approveButtons[1]).not.toHaveClass("ant-btn-loading");
  });
});
