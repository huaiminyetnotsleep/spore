/**
 * 用户详情页写操作测试（管理操作迁移的核心 mutation 交互）：
 * 确认弹窗触发、成功后失效相关 query 并提示、失败展示服务端受控文案
 * 且不误报成功。API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import { fetchUserDetail, type UserDetail } from "../../api/admin";
import { resetUserQuota, setUserCloudDownload, setUserStatus } from "../../api/mutations";
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
    resetUserQuota: vi.fn(),
  };
});

const fetchUserDetailMock = vi.mocked(fetchUserDetail);
const setUserStatusMock = vi.mocked(setUserStatus);
const setUserCloudDownloadMock = vi.mocked(setUserCloudDownload);
const resetUserQuotaMock = vi.mocked(resetUserQuota);

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
    resetUserQuotaMock.mockReset();
    fetchUserDetailMock.mockResolvedValue(detail({}));
  });

  it("渲染唯一 H1「用户详情」，数据口径说明仅在加载成功后出现，返回入口为按钮", async () => {
    renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "用户详情" })).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    // 数据口径说明在 Gate 内：成功态可见
    expect(await screen.findByText(/今日已用按运营时区当日统计/)).toBeInTheDocument();
    // 返回列表保持原目的地/可访问名，但不再是 <Link><Button/></Link> 嵌套
    const backButton = screen.getByRole("button", { name: "返回列表" });
    expect(backButton.closest("a")).toBeNull();
  });

  it("404 时展示详情未找到，且不泄漏数据口径说明", async () => {
    fetchUserDetailMock.mockRejectedValue(new ApiError("用户不存在", 404, "NOT_FOUND"));

    renderPage();

    expect(await screen.findByText("用户不存在")).toBeInTheDocument();
    expect(screen.queryByText(/今日已用按运营时区当日统计/)).not.toBeInTheDocument();
    expect(screen.queryByText("保存限额")).not.toBeInTheDocument();
  });

  it("查看请求记录/查看频道绑定聚合进「操作」分区（普通按钮，资料行不再嵌链接）", async () => {
    renderPage();

    // 资料表「累计请求数」行只保留数字，不再嵌入链接
    expect(await screen.findByText("累计请求数")).toBeInTheDocument();
    const descriptions = screen.getByText("累计请求数").closest(".ant-descriptions");
    expect(descriptions).not.toBeNull();
    expect(
      within(descriptions as HTMLElement).queryByRole("link", { name: "查看请求记录" }),
    ).not.toBeInTheDocument();
    expect(
      within(descriptions as HTMLElement).queryByRole("link", { name: "查看频道绑定" }),
    ).not.toBeInTheDocument();

    // 操作分区平铺聚合所有用户操作：查看入口为普通按钮（不再是 <Link>）
    const actionsSection = screen.getByText("操作").closest(".page-section");
    expect(actionsSection).not.toBeNull();
    const viewRequests = within(actionsSection as HTMLElement).getByRole("button", {
      name: "查看请求记录",
    });
    const viewBindings = within(actionsSection as HTMLElement).getByRole("button", {
      name: "查看频道绑定",
    });
    expect(viewRequests.closest("a")).toBeNull();
    expect(viewBindings.closest("a")).toBeNull();
    // 原有管理动作与查看入口聚合在同一操作区
    expect(within(actionsSection as HTMLElement).getByRole("button", { name: "禁 用" })).toBeInTheDocument();
    expect(
      within(actionsSection as HTMLElement).getByRole("button", { name: "重置今日用量" }),
    ).toBeInTheDocument();
    expect(
      within(actionsSection as HTMLElement).getByRole("button", { name: "刷新 Telegram 资料" }),
    ).toBeInTheDocument();
  });

  it("禁用确认按 warning 意图弹窗（确认按钮非 danger），文案与列表页一致", async () => {
    setUserStatusMock.mockResolvedValue({ ok: true, status: "disabled" });
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "禁 用" }));

    const confirmButton = await screen.findByRole("button", { name: "确 认" });
    // warning 意图：确认按钮不再是旧版一律 danger 的红色
    expect(confirmButton).not.toHaveClass("ant-btn-dangerous");
    expect(await screen.findByText("确定禁用该用户？其新请求将被立即拒绝。")).toBeInTheDocument();
  });

  it("重置今日用量按 default 意图确认后提交", async () => {
    resetUserQuotaMock.mockResolvedValue({ ok: true } as never);
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "重置今日用量" }));
    expect(await screen.findByText("确定重置该用户今日已用额度为 0？")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(resetUserQuotaMock).toHaveBeenCalledWith(7));
    expect(await screen.findByText("当日已用额度已重置。")).toBeInTheDocument();
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
