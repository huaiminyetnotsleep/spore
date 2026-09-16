/**
 * 用户详情页写操作测试（管理操作迁移的核心 mutation 交互）：
 * 确认弹窗触发、成功后失效相关 query 并提示、失败展示服务端受控文案
 * 且不误报成功。API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import { fetchUserDetail, type UserDetail } from "../../api/admin";
import { setUserCloudDownload, setUserStatus } from "../../api/mutations";
import { UserDetailPage } from "./UserDetailPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchUserDetail: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    setUserStatus: vi.fn(),
    setUserCloudDownload: vi.fn(),
  };
});

const fetchUserDetailMock = vi.mocked(fetchUserDetail);
const setUserStatusMock = vi.mocked(setUserStatus);
const setUserCloudDownloadMock = vi.mocked(setUserCloudDownload);

function detail(overrides: Partial<UserDetail>): UserDetail {
  return {
    id: 7,
    username: "alice",
    display_name: "爱丽丝",
    status: "enabled",
    is_owner: false,
    note: "",
    source_bot_id: 0,
    source_bot_username: "",
    created_at: 1756598400000,
    first_used_at: 1756598400000,
    last_used_at: 1756598400000,
    archived_at: 0,
    last_denied_at: 0,
    last_denied_reason: "",
    last_denied_text: "",
    used_today: 3,
    daily_limit: 100,
    remaining_today: 97,
    submit_interval_sec: 30,
    concurrent_limit: 2,
    bind_limit: 0,
    effective_bind_limit: 1,
    cloud_download: 0,
    effective_cloud_download: false,
    total_requests: 12,
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
        <MemoryRouter initialEntries={["/users/7"]}>
          {/* 详情页从路由参数取 ID：必须挂真实 Route 才能拿到 useParams */}
          <Routes>
            <Route path="/users/:id" element={<UserDetailPage />} />
          </Routes>
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
  return invalidateSpy;
}

describe("用户详情页写操作", () => {
  beforeEach(() => {
    fetchUserDetailMock.mockReset();
    setUserStatusMock.mockReset();
    setUserCloudDownloadMock.mockReset();
    fetchUserDetailMock.mockResolvedValue(detail({}));
  });

  it("禁用用户：确认弹窗触发后提交，成功失效用户 query 并提示", async () => {
    setUserStatusMock.mockResolvedValue({ ok: true, status: "disabled" });
    const invalidateSpy = renderPage();

    // 已启用用户展示"禁用"入口（对齐 SSR 表单条件）
    fireEvent.click(await screen.findByRole("button", { name: "禁 用" }));

    // 破坏性操作先弹二次确认（SSR data-confirm 同款文案）
    expect(
      await screen.findByText("确定禁用该用户？其新请求将被立即拒绝。"),
    ).toBeInTheDocument();
    expect(setUserStatusMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(setUserStatusMock).toHaveBeenCalledWith(7, "disable"));
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ["users"] })),
    );
    expect(await screen.findByText("状态已更新为已禁用。")).toBeInTheDocument();
  });

  it("禁用失败（owner 约束）：展示服务端受控文案，不失效也不提示成功", async () => {
    setUserStatusMock.mockRejectedValue(
      new ApiError("操作与现有数据冲突（如停用 owner 身份或重复添加）", 409, "STORE_CONSTRAINT"),
    );
    fetchUserDetailMock.mockResolvedValue(detail({ is_owner: true }));
    const invalidateSpy = renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "禁 用" }));
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));

    expect(await screen.findByText("操作与现有数据冲突（如停用 owner 身份或重复添加）")).toBeInTheDocument();
    await waitFor(() => expect(setUserStatusMock).toHaveBeenCalled());
    expect(invalidateSpy).not.toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: ["users"] }),
    );
    expect(screen.queryByText("状态已更新为已禁用。")).not.toBeInTheDocument();
  });

  it("无确认的启用操作直接提交", async () => {
    setUserStatusMock.mockResolvedValue({ ok: true, status: "enabled" });
    fetchUserDetailMock.mockResolvedValue(detail({ status: "archived", archived_at: 1756598400000 }));
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "启 用" }));

    await waitFor(() => expect(setUserStatusMock).toHaveBeenCalledWith(7, "enable"));
    expect(screen.queryByText("操作确认")).not.toBeInTheDocument();
  });

  it("限额回显：bind_limit raw 0 的普通用户输入框显示角色默认 1", async () => {
    renderPage();
    expect(
      await screen.findByRole("spinbutton", { name: /频道绑定数量上限/ }),
    ).toHaveDisplayValue("1");
  });

  it("限额回显：bind_limit raw 0 的 owner 输入框显示角色默认 3", async () => {
    fetchUserDetailMock.mockResolvedValue(detail({ is_owner: true, effective_bind_limit: 3 }));
    renderPage();
    expect(
      await screen.findByRole("spinbutton", { name: /频道绑定数量上限/ }),
    ).toHaveDisplayValue("3");
  });

  it("云盘下载权限：回显三态与生效状态（默认普通用户拒绝）", async () => {
    renderPage();
    expect(await screen.findByText("不允许")).toBeInTheDocument();
    expect(screen.getByText("（跟随角色默认）")).toBeInTheDocument();
    expect(screen.getByText(/普通用户默认不允许/)).toBeInTheDocument();
    // 下拉回显 raw 0（跟随角色默认）
    expect(screen.getByTitle("跟随角色默认")).toBeInTheDocument();
  });

  it("云盘下载权限：owner 默认允许回显", async () => {
    fetchUserDetailMock.mockResolvedValue(
      detail({ is_owner: true, effective_cloud_download: true }),
    );
    renderPage();
    expect(await screen.findByText("允许")).toBeInTheDocument();
    expect(screen.getByText(/owner 默认允许/)).toBeInTheDocument();
  });

  it("云盘下载权限：切换为允许后提交并失效用户 query", async () => {
    setUserCloudDownloadMock.mockResolvedValue({
      ok: true,
      cloud_download: 1,
      effective_cloud_download: true,
    });
    const invalidateSpy = renderPage();

    const selector = (await screen.findByRole("combobox", { name: "云盘下载权限" }))
      .closest(".ant-select")
      ?.querySelector(".ant-select-selector");
    expect(selector).not.toBeNull();
    fireEvent.mouseDown(selector as HTMLElement);
    fireEvent.click(await screen.findByRole("option", { name: "允许" }));

    fireEvent.click(screen.getByRole("button", { name: "保存权限" }));

    await waitFor(() => expect(setUserCloudDownloadMock).toHaveBeenCalledWith(7, 1));
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ["users"] })),
    );
    expect(
      await screen.findByText("云盘下载权限已更新：当前允许该用户使用 /download。"),
    ).toBeInTheDocument();
  });

  it("云盘下载权限：失败展示服务端受控文案，不提示成功", async () => {
    setUserCloudDownloadMock.mockRejectedValue(
      new ApiError("cloud_download 取值必须为 0（跟随默认）、1（允许）或 2（拒绝）。", 400, "BAD_REQUEST"),
    );
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "保存权限" }));

    expect(
      await screen.findByText("cloud_download 取值必须为 0（跟随默认）、1（允许）或 2（拒绝）。"),
    ).toBeInTheDocument();
    expect(
      screen.queryByText("云盘下载权限已更新：当前允许该用户使用 /download。"),
    ).not.toBeInTheDocument();
  });
});
