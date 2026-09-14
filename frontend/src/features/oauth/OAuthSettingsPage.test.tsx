/**
 * GitHub 登录配置页测试：当前 OAuth 回调以受控 oauth 查询参数回跳本页，
 * 断言横幅展示、参数清除与未知参数忽略。配置读取接口以模块 mock 注入。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { fetchOAuthSettings, type OAuthSettingsView } from "../../api/admin";
import { OAuthSettingsPage } from "./OAuthSettingsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchOAuthSettings: vi.fn(),
  };
});

const fetchOAuthSettingsMock = vi.mocked(fetchOAuthSettings);

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
    </QueryClientProvider>,
  );
}

afterEach(() => {
  fetchOAuthSettingsMock.mockReset();
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
