/**
 * 审计页组件测试：表格渲染、before/after 原始 JSON 展开展示、服务端分页
 * 与错误态（含重试）。API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchAudit, type AuditRow, type ListEnvelope } from "../../api/admin";
import { ApiError } from "../../api/client";
import { clearAudit, deleteAuditEntries } from "../../api/mutations";
import { AuditPage } from "./AuditPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchAudit: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    deleteAuditEntries: vi.fn(),
    clearAudit: vi.fn(),
  };
});

const fetchAuditMock = vi.mocked(fetchAudit);
const deleteAuditEntriesMock = vi.mocked(deleteAuditEntries);
const clearAuditMock = vi.mocked(clearAudit);

function envelope(items: AuditRow[], total = items.length): ListEnvelope<AuditRow> {
  return { items, page: 1, page_size: 20, total, total_pages: Math.ceil(total / 20) };
}

function auditRow(overrides: Partial<AuditRow> = {}): AuditRow {
  return {
    id: 1,
    actor: "admin",
    action: "user.enable",
    target: "user:1001",
    created_at: 1756598400000,
    before: { status: "pending" },
    after: { status: "enabled" },
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <MemoryRouter initialEntries={["/audit"]}>
          <AuditPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

describe("审计日志页", () => {
  beforeEach(() => {
    fetchAuditMock.mockReset();
    deleteAuditEntriesMock.mockReset();
    clearAuditMock.mockReset();
  });

  it("渲染唯一 H1 页面标题「审计日志」", async () => {
    fetchAuditMock.mockResolvedValue(envelope([auditRow({})]));

    renderPage();

    expect(
      await screen.findByRole("heading", { level: 1, name: "审计日志" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  it("渲染审计行：动作、操作者与空对象占位", async () => {
    fetchAuditMock.mockResolvedValue(
      envelope([auditRow({}), auditRow({ id: 2, action: "backup.export", target: "" })]),
    );

    renderPage();

    expect(await screen.findByText("user.enable")).toBeInTheDocument();
    expect(screen.getByText("backup.export")).toBeInTheDocument();
    // 空对象（target 为空串）展示为受控占位
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(fetchAuditMock).toHaveBeenCalledWith({ page: 1, page_size: 20 });
  });

  it("展开行展示变更前后原始 JSON；无快照行不可展开", async () => {
    fetchAuditMock.mockResolvedValue(
      envelope([auditRow({}), auditRow({ id: 2, action: "backup.export", before: null, after: null })]),
    );

    renderPage();

    // 仅首行（有快照）可展开；无快照行渲染占位图标而非按钮
    const expandButtons = await screen.findAllByRole("button", { name: "Expand row" });
    expect(expandButtons).toHaveLength(1);
    fireEvent.click(expandButtons[0]);

    expect(await screen.findByText("变更前")).toBeInTheDocument();
    expect(screen.getByText("变更后")).toBeInTheDocument();
    // 原始 JSON 直接呈现（缩进文本含字段名与值）
    expect(screen.getByText(/"status": "pending"/)).toBeInTheDocument();
    expect(screen.getByText(/"status": "enabled"/)).toBeInTheDocument();
  });

  it("翻页时按服务端分页参数重新查询", async () => {
    fetchAuditMock.mockResolvedValue(
      envelope(
        Array.from({ length: 20 }, (_, i) => auditRow({ id: i + 1, action: `action.${i + 1}` })),
        35,
      ),
    );

    renderPage();

    expect(await screen.findByText("action.1")).toBeInTheDocument();
    fireEvent.click(screen.getByTitle("2"));

    await waitFor(() =>
      expect(fetchAuditMock).toHaveBeenLastCalledWith({ page: 2, page_size: 20 }),
    );
  });

  it("按日期筛选时把起止日期传给查询", async () => {
    fetchAuditMock.mockResolvedValue(envelope([auditRow({})]));
    renderPage();
    await screen.findByText("user.enable");

    const since = screen.getByPlaceholderText("开始日期");
    const until = screen.getByPlaceholderText("结束日期");
    fireEvent.change(since, { target: { value: "2026-09-01" } });
    fireEvent.keyDown(since, { key: "Enter" });
    fireEvent.change(until, { target: { value: "2026-09-04" } });
    fireEvent.keyDown(until, { key: "Enter" });
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));

    await waitFor(() =>
      expect(fetchAuditMock).toHaveBeenLastCalledWith({
        since: "2026-09-01",
        until: "2026-09-04",
        page: 1,
        page_size: 20,
      }),
    );
  });

  it("重置筛选后恢复无条件查询", async () => {
    fetchAuditMock.mockResolvedValue(envelope([auditRow({})]));
    renderPage();
    await screen.findByText("user.enable");

    const since = screen.getByPlaceholderText("开始日期");
    fireEvent.change(since, { target: { value: "2026-09-01" } });
    fireEvent.keyDown(since, { key: "Enter" });
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));

    await waitFor(() =>
      expect(fetchAuditMock).toHaveBeenLastCalledWith(
        expect.objectContaining({ since: "2026-09-01" }),
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: "重 置" }));

    // 重置后无条件查询：仅分页参数（筛选键不再出现）
    await waitFor(() =>
      expect(fetchAuditMock).toHaveBeenLastCalledWith({ page: 1, page_size: 20 }),
    );
    expect(screen.getByPlaceholderText("开始日期")).toHaveValue("");
  });

  it("删除当前页明确选中的审计记录", async () => {
    deleteAuditEntriesMock.mockResolvedValue({ ok: true, deleted: 1 });
    fetchAuditMock.mockResolvedValue(envelope([auditRow({ id: 11 }), auditRow({ id: 12 })]));
    renderPage();
    await screen.findAllByText("user.enable");

    const checkboxes = screen.getAllByRole("checkbox");
    fireEvent.click(checkboxes[1]);
    fireEvent.click(screen.getByRole("button", { name: /删除已选/ }));
    expect(await screen.findByText(/当前页明确选中的 1 条审计记录/)).toBeInTheDocument();
    expect(deleteAuditEntriesMock).not.toHaveBeenCalled();

    const confirmButton = screen.getByRole("button", { name: "确 认" });
    // 删除已选为危险操作：确认按钮使用 danger 语义
    expect(confirmButton).toHaveClass("ant-btn-dangerous");
    fireEvent.click(confirmButton);
    await waitFor(() => expect(deleteAuditEntriesMock).toHaveBeenCalledWith([11]));
    expect(await screen.findByText(/已删除 1 条审计记录/)).toBeInTheDocument();
  });

  it("批量删除提交使用快照并冻结 selection", async () => {
    deleteAuditEntriesMock.mockReturnValue(new Promise(() => undefined) as never);
    fetchAuditMock.mockResolvedValue(envelope([auditRow({ id: 11 }), auditRow({ id: 12 })]));
    renderPage();
    await screen.findAllByText("user.enable");

    const checkboxes = screen.getAllByRole("checkbox");
    fireEvent.click(checkboxes[1]);
    fireEvent.click(screen.getByRole("button", { name: /删除已选/ }));
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));

    // 提交的是确认瞬间的快照；pending 期间勾选被冻结
    await waitFor(() => expect(deleteAuditEntriesMock).toHaveBeenCalledWith([11]));
    await waitFor(() =>
      expect(
        screen.getAllByRole("checkbox").every((box) => (box as HTMLInputElement).disabled),
      ).toBe(true),
    );
    expect(screen.getByRole("button", { name: /删除已选/ })).toHaveClass("ant-btn-loading");
  });

  it("清除全部要求确认且明确忽略当前筛选", async () => {
    clearAuditMock.mockResolvedValue({ ok: true, deleted: 3 });
    fetchAuditMock.mockResolvedValue(envelope([auditRow({})]));
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /清除全部审计日志/ }));
    expect(await screen.findByText(/不受当前时间筛选影响/)).toBeInTheDocument();
    expect(clearAuditMock).not.toHaveBeenCalled();

    const confirmButton = screen.getByRole("button", { name: "确 认" });
    // 清除全部为危险操作：确认按钮使用 danger 语义
    expect(confirmButton).toHaveClass("ant-btn-dangerous");
    fireEvent.click(confirmButton);
    await waitFor(() => expect(clearAuditMock).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(/已清除 3 条历史审计/)).toBeInTheDocument();
  });

  it("请求失败时展示错误态，重试会重新发起查询", async () => {
    fetchAuditMock.mockRejectedValueOnce(new ApiError("API 请求失败（500）", 500));
    fetchAuditMock.mockResolvedValue(envelope([auditRow({})]));

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    expect(screen.queryByText("user.enable")).not.toBeInTheDocument();

    // antd 按钮会在两个汉字之间插入空格（"重 试"），用正则匹配可访问名
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));

    expect(await screen.findByText("user.enable")).toBeInTheDocument();
    expect(fetchAuditMock).toHaveBeenCalledTimes(2);
  });

  it("空数据时展示受控空态文案", async () => {
    fetchAuditMock.mockResolvedValue(envelope([]));

    renderPage();

    expect(await screen.findByText("暂无审计记录。")).toBeInTheDocument();
  });
});
