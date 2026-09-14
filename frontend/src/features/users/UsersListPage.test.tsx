/**
 * 用户列表页组件测试：表格渲染、空态、错误态（含重试）与操作列快捷操作
 * （禁用/归档/重置今日用量的确认、提交与 owner 行为差异）。
 * API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import { fetchUsers, type ListEnvelope, type UserRow } from "../../api/admin";
import { resetUserQuota, setUserStatus } from "../../api/mutations";
import { UsersListPage } from "./UsersListPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchUsers: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    setUserStatus: vi.fn(),
    resetUserQuota: vi.fn(),
  };
});

const fetchUsersMock = vi.mocked(fetchUsers);
const setUserStatusMock = vi.mocked(setUserStatus);
const resetUserQuotaMock = vi.mocked(resetUserQuota);

function envelope(items: UserRow[]): ListEnvelope<UserRow> {
  return { items, page: 1, page_size: 20, total: items.length, total_pages: 1 };
}

function userRow(overrides: Partial<UserRow>): UserRow {
  return {
    id: 1001,
    username: "alice",
    display_name: "爱丽丝",
    status: "enabled",
    is_owner: false,
    note: "",
    last_used_at: 1756598400000,
    total_requests: 12,
    has_total_requests: true,
    cloud_download: 0,
    effective_cloud_download: false,
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidateSpy = vi.spyOn(client, "invalidateQueries");
  render(
    <QueryClientProvider client={client}>
      {/* antd App 上下文：message/modal 实例来自它（生产由 App.tsx 挂载） */}
      <AntApp component={false}>
        <MemoryRouter initialEntries={["/users"]}>
          <UsersListPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
  return invalidateSpy;
}

describe("用户列表页", () => {
  beforeEach(() => {
    fetchUsersMock.mockReset();
    setUserStatusMock.mockReset();
    resetUserQuotaMock.mockReset();
  });

  it("渲染用户表格行、状态中文标签与 CSV 导出入口", async () => {
    fetchUsersMock.mockResolvedValue(
      envelope([
        userRow({}),
        userRow({ id: 1002, username: "bob", display_name: "", status: "pending", is_owner: true }),
      ]),
    );

    renderPage();

    // 用户名与显示名渲染在同一个单元格内，用子串匹配
    expect(await screen.findByText(/@alice/)).toBeInTheDocument();
    expect(screen.getByText(/爱丽丝/)).toBeInTheDocument();
    expect(screen.getByText("1001")).toBeInTheDocument();
    expect(screen.getByText("已启用")).toBeInTheDocument();
    expect(screen.getByText("待审批")).toBeInTheDocument();
    expect(screen.getByText("owner")).toBeInTheDocument();

    const exportLink = screen.getByRole("link", { name: "导出 CSV" });
    expect(exportLink).toHaveAttribute("href", "/users/export.csv");
    expect(fetchUsersMock).toHaveBeenCalledWith(
      expect.objectContaining({ page: 1, page_size: 20 }),
    );
  });

  it("空数据时展示受控空态文案", async () => {
    fetchUsersMock.mockResolvedValue(envelope([]));

    renderPage();

    expect(await screen.findByText("没有符合条件的用户。")).toBeInTheDocument();
  });

  it("请求失败时展示错误态，重试会重新发起查询", async () => {
    fetchUsersMock.mockRejectedValueOnce(new ApiError("API 请求失败（500）", 500));
    fetchUsersMock.mockResolvedValue(envelope([userRow({})]));

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    expect(screen.queryByText("@alice")).not.toBeInTheDocument();

    // antd 按钮会在两个汉字之间插入空格（"重 试"），用正则匹配可访问名
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));

    await waitFor(() => expect(screen.getByText(/@alice/)).toBeInTheDocument());
    expect(fetchUsersMock).toHaveBeenCalledTimes(2);
  });

  it("操作列：启用用户提供禁用/归档/重置，owner 与已归档行收敛入口", async () => {
    fetchUsersMock.mockResolvedValue(
      envelope([
        userRow({}), // enabled、非 owner：三个入口齐全
        userRow({ id: 1002, status: "pending" }), // 待审批：不可禁用（未启用）
        userRow({ id: 1003, status: "enabled", is_owner: true }), // owner：只保留重置
        userRow({ id: 1004, status: "archived" }), // 已归档：不重复提供归档
      ]),
    );

    renderPage();

    // 以操作按钮出现为准（各行用户名相同，文本查询会命中多行）
    // 4 行都提供"重置今日用量"（6 字按钮，antd 不插空格）
    expect(await screen.findAllByRole("button", { name: "重置今日用量" })).toHaveLength(4);
    // 禁用：仅 enabled 非 owner 行（alice）；归档：除已归档与 owner 外（alice + pending 行）
    expect(screen.getAllByRole("button", { name: "禁 用" })).toHaveLength(1);
    expect(screen.getAllByRole("button", { name: "归 档" })).toHaveLength(2);
  });

  it("快捷禁用：二次确认后提交，成功失效用户 query 并提示", async () => {
    setUserStatusMock.mockResolvedValue({ ok: true, status: "disabled" });
    fetchUsersMock.mockResolvedValue(envelope([userRow({})]));
    const invalidateSpy = renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "禁 用" }));

    // 破坏性操作先弹二次确认（与详情页同一文案）
    expect(
      await screen.findByText("确定禁用该用户？其新请求将被立即拒绝。"),
    ).toBeInTheDocument();
    expect(setUserStatusMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(setUserStatusMock).toHaveBeenCalledWith(1001, "disable"));
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ["users"] })),
    );
    expect(await screen.findByText("状态已更新为已禁用。")).toBeInTheDocument();
  });

  it("重置今日用量：确认后提交 reset-quota 并提示", async () => {
    resetUserQuotaMock.mockResolvedValue({ ok: true });
    fetchUsersMock.mockResolvedValue(envelope([userRow({})]));
    const invalidateSpy = renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "重置今日用量" }));

    expect(await screen.findByText("确定重置该用户今日已用额度为 0？")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(resetUserQuotaMock).toHaveBeenCalledWith(1001));
    expect(await screen.findByText("当日已用额度已重置。")).toBeInTheDocument();
    expect(invalidateSpy).toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: ["users"] }),
    );
  });
});
