/**
 * 管理端 SPA 应用壳测试：真实页面路由挂载与导航高亮。
 * 网络层以 fetch stub 返回空信封，页面应渲染为空列表而非报错。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { App } from "./App";
import { logout, restartServer } from "./api/mutations";
import { CONFIRM_RESTART_TEXT } from "./features/settings/restartAction";

vi.mock("./api/mutations", async () => {
  const actual = await vi.importActual<typeof import("./api/mutations")>("./api/mutations");
  return {
    ...actual,
    logout: vi.fn(),
    restartServer: vi.fn(),
  };
});

const logoutMock = vi.mocked(logout);
const restartServerMock = vi.mocked(restartServer);

function stubFetchWithEmptyEnvelope() {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((input: RequestInfo | URL) => {
      const url =
        typeof input === "string" ? input : input instanceof URL ? input.toString() : input.url;
      const body = url.endsWith("/api/v1/session")
        ? {
            authenticated: true,
            user: { name: "管理员" },
            csrf_token: "test-csrf",
            expires_at: 0,
          }
        : { items: [], page: 1, page_size: 20, total: 0, total_pages: 0 };
      return Promise.resolve(
        new Response(JSON.stringify(body), {
          status: 200,
          headers: { "content-type": "application/json" },
        }),
      );
    }),
  );
}

function renderApp() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <App />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  window.history.replaceState({}, "", "/admin/users");
  logoutMock.mockReset().mockResolvedValue({ ok: true });
  restartServerMock.mockReset().mockResolvedValue({
    ok: true,
    message: "应用将退出并重新启动。",
    restarting: true,
  });
  stubFetchWithEmptyEnvelope();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("管理端 SPA 应用壳", () => {
  it("用户路由挂载真实页面并保持导航高亮", async () => {
    renderApp();

    expect(await screen.findByRole("heading", { name: "用户管理" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "工作台" }).closest("li")).toHaveClass(
      "ant-menu-submenu-open",
    );
    expect(screen.getByRole("menuitem", { name: "用户运营" }).closest("li")).not.toHaveClass(
      "ant-menu-submenu-open",
    );
    fireEvent.click(screen.getByRole("menuitem", { name: "用户运营" }));
    expect(screen.getByRole("link", { name: "用户管理" })).toHaveClass("active");
    expect(screen.getByRole("link", { name: "导出 CSV" })).toHaveAttribute(
      "href",
      "/users/export.csv",
    );
  });

  it("申请与设置路由挂载真实管理页面", async () => {
    window.history.replaceState({}, "", "/admin/applications");
    const { unmount } = renderApp();
    expect(await screen.findByRole("heading", { name: "申请审批" })).toBeInTheDocument();
    unmount();

    window.history.replaceState({}, "", "/admin/settings");
    renderApp();
    expect(await screen.findByRole("heading", { name: "运行设置" })).toBeInTheDocument();
  });

  it("OAuth 子路由只高亮 GitHub 登录，不高亮运行设置", async () => {
    window.history.replaceState({}, "", "/admin/settings/oauth");
    renderApp();

    expect(await screen.findByRole("heading", { name: "GitHub 登录" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("menuitem", { name: "系统运维" }));
    expect(screen.getByRole("link", { name: "GitHub 登录" })).toHaveClass("active");
    expect(screen.getByRole("link", { name: "运行设置" })).not.toHaveClass("active");
  });

  it("Header Avatar 菜单可触发优雅重启和退出登录", async () => {
    renderApp();

    const avatar = await screen.findByRole("button", { name: "管理员账户菜单" });
    fireEvent.click(avatar);
    const restartLabel = await screen.findByText("优雅重启");
    const restartItem = restartLabel.closest("li");
    expect(restartItem).toHaveClass("ant-dropdown-menu-item-danger");
    expect(screen.getByText("退出登录")).toBeInTheDocument();

    fireEvent.click(restartLabel);
    expect(await screen.findByText(CONFIRM_RESTART_TEXT)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /确\s*认/ }));
    await waitFor(() => expect(restartServerMock).toHaveBeenCalledTimes(1));

    fireEvent.click(avatar);
    fireEvent.click(await screen.findByText("退出登录"));
    await waitFor(() => expect(logoutMock).toHaveBeenCalledTimes(1));
  });

  it("高风险与审计路由挂载真实页面（备份/MTProto/审计）", async () => {
    window.history.replaceState({}, "", "/admin/backup");
    const { unmount } = renderApp();
    expect(await screen.findByRole("heading", { name: "数据备份" })).toBeInTheDocument();
    unmount();

    window.history.replaceState({}, "", "/admin/mtproto");
    renderApp();
    expect(await screen.findByRole("heading", { name: "Telegram 连接" })).toBeInTheDocument();
    unmount();

    window.history.replaceState({}, "", "/admin/audit");
    renderApp();
    expect(await screen.findByRole("heading", { name: "审计日志" })).toBeInTheDocument();
    expect(await screen.findByText("暂无审计记录。")).toBeInTheDocument();
  });
});
