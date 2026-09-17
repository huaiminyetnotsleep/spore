/**
 * GitHub 登录配置页测试：当前 OAuth 回调以受控 oauth 查询参数回跳本页，
 * 断言横幅展示、参数清除与未知参数忽略；配置读取接口以模块 mock 注入。
 * 另覆盖异步 gate 与清除/解绑的 danger 二次确认（Promise 感知 pending）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { fetchOAuthSettings, type OAuthSettingsView } from "../../api/admin";
import { saveOAuthSettings, unbindOAuth } from "../../api/mutations";
import { OAuthSettingsPage } from "./OAuthSettingsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchOAuthSettings: vi.fn(),
  };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    saveOAuthSettings: vi.fn(),
    unbindOAuth: vi.fn(),
  };
});

const fetchOAuthSettingsMock = vi.mocked(fetchOAuthSettings);
const saveOAuthSettingsMock = vi.mocked(saveOAuthSettings);
const unbindOAuthMock = vi.mocked(unbindOAuth);

function settingsView(overrides: Partial<OAuthSettingsView> = {}): OAuthSettingsView {
  return {
    github_configured: false,
    configured: false,
    enabled: false,
    client_id: "",
    secret_set: false,
    bound: false,
    ...overrides,
  };
}

/** 展示当前路由（断言查询参数被清除用）。 */
function LocationProbe() {
  return <span data-testid="location">{useLocation().search || "(empty)"}</span>;
}

function renderPage(search: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      {/* antd App 上下文：message/modal 实例来自它（生产由 App.tsx 挂载） */}
      <AntApp>
        <MemoryRouter initialEntries={[`/settings/oauth${search}`]}>
          <Routes>
            <Route
              path="/settings/oauth"
              element={
                <>
                  <OAuthSettingsPage />
                  <LocationProbe />
                </>
              }
            />
          </Routes>
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  fetchOAuthSettingsMock.mockReset();
  saveOAuthSettingsMock.mockReset();
  unbindOAuthMock.mockReset();
  vi.clearAllMocks();
});

describe("GitHub 登录配置页回调横幅", () => {
  it("oauth=bound 展示绑定成功横幅并清除查询参数", async () => {
    fetchOAuthSettingsMock.mockResolvedValue(settingsView());

    renderPage("?oauth=bound");

    expect(
      await screen.findByText(/GitHub 登录通道已绑定。为安全起见，全部登录会话已失效/),
    ).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("(empty)"));
  });

  it("oauth=error 展示通用失败横幅并清除查询参数", async () => {
    fetchOAuthSettingsMock.mockResolvedValue(settingsView());

    renderPage("?oauth=error");

    expect(await screen.findByText("GitHub 授权流程未完成，请稍后重试。")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("(empty)"));
  });

  it("无 oauth 参数时不展示横幅（正常页面形态）", async () => {
    fetchOAuthSettingsMock.mockResolvedValue(settingsView());

    renderPage("");

    expect(await screen.findByText("GitHub 登录")).toBeInTheDocument();
    expect(screen.queryByText("GitHub 授权流程未完成，请稍后重试。")).not.toBeInTheDocument();
  });
});

describe("GitHub 登录配置页表单回显", () => {
  it("首次加载完成前不渲染配置表单、清除或解绑操作", async () => {
    let resolveSettings: (value: OAuthSettingsView) => void = () => undefined;
    fetchOAuthSettingsMock.mockReturnValue(
      new Promise((resolve) => {
        resolveSettings = resolve;
      }),
    );

    renderPage("");

    expect(screen.queryByRole("button", { name: "保存配置" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "清除凭据" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "解绑 GitHub 账号" })).not.toBeInTheDocument();

    resolveSettings(settingsView());
    expect(await screen.findByRole("button", { name: "保存配置" })).toBeInTheDocument();
  });

  it("加载后「启用 GitHub 登录」勾选框反映服务端 enabled 状态", async () => {
    fetchOAuthSettingsMock.mockResolvedValue(
      settingsView({ enabled: true, configured: true, github_configured: true }),
    );

    renderPage("");

    const checkbox = await screen.findByRole("checkbox", { name: "启用 GitHub 登录" });
    await waitFor(() => expect(checkbox).toBeChecked());
  });

  it("服务端 enabled=false 时勾选框保持未选中", async () => {
    fetchOAuthSettingsMock.mockResolvedValue(
      settingsView({ enabled: false, configured: true, github_configured: true }),
    );

    renderPage("");

    // 等待服务端数据加载完成（client_id 回显说明 setFieldsValue 已执行）
    await screen.findByText(/当前 Client ID：/);
    expect(screen.getByRole("checkbox", { name: "启用 GitHub 登录" })).not.toBeChecked();
  });
});

describe("GitHub 登录配置页危险确认", () => {
  it("清除凭据走 danger 确认弹层，返回 mutation Promise 并保持确认 pending", async () => {
    saveOAuthSettingsMock.mockReturnValue(new Promise(() => undefined) as never);
    fetchOAuthSettingsMock.mockResolvedValue(
      settingsView({ configured: true, github_configured: true, client_id: "client-id", secret_set: true }),
    );
    renderPage("");

    fireEvent.click(await screen.findByRole("button", { name: "清除凭据" }));
    expect(saveOAuthSettingsMock).not.toHaveBeenCalled();
    expect(
      await screen.findByText(/确定清除 GitHub Client ID 与 Secret？/),
    ).toBeInTheDocument();
    const confirmButton = await screen.findByRole("button", { name: "确 认" });
    expect(confirmButton).toHaveClass("ant-btn-dangerous");
    fireEvent.click(confirmButton);

    await waitFor(() => expect(saveOAuthSettingsMock).toHaveBeenCalledWith({ action: "clear" }));
    await waitFor(() => expect(confirmButton).toHaveClass("ant-btn-loading"));
  });

  it("解绑按钮与确认弹层均为 danger 语义，返回 mutation Promise 并保持 pending", async () => {
    unbindOAuthMock.mockReturnValue(new Promise(() => undefined) as never);
    fetchOAuthSettingsMock.mockResolvedValue(
      settingsView({
        configured: true,
        github_configured: true,
        bound: true,
        github_login: "owner",
        github_id: 123,
        bound_at: 1757030400000,
      }),
    );
    renderPage("");

    const unbindButton = await screen.findByRole("button", { name: "解绑 GitHub 账号" });
    // 解绑使全部会话失效：触发按钮本身具备视觉 danger 语义
    expect(unbindButton).toHaveClass("ant-btn-dangerous");
    fireEvent.click(unbindButton);
    expect(unbindOAuthMock).not.toHaveBeenCalled();
    expect(await screen.findByText(/全部会话将立即失效/)).toBeInTheDocument();
    const confirmButton = await screen.findByRole("button", { name: "确 认" });
    expect(confirmButton).toHaveClass("ant-btn-dangerous");
    fireEvent.click(confirmButton);

    await waitFor(() => expect(unbindOAuthMock).toHaveBeenCalledWith());
    await waitFor(() => expect(confirmButton).toHaveClass("ant-btn-loading"));
  });
});
