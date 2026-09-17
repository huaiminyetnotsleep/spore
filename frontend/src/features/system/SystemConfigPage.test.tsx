/**
 * 系统设置页测试（页面迁移后新增覆盖，此前无独立单测）：
 * 唯一 H1、名称回填、dirty/保存状态（未变更时保存不可用）、保存载荷
 * （去首尾空白）、失败展示受控文案与重试。查询与写接口以模块 mock 注入。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { fetchSystemConfig, type SystemConfigView } from "../../api/admin";
import { saveSystemConfig } from "../../api/mutations";
import { SystemConfigPage } from "./SystemConfigPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchSystemConfig: vi.fn(),
  };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    saveSystemConfig: vi.fn(),
  };
});

const fetchSystemConfigMock = vi.mocked(fetchSystemConfig);
const saveSystemConfigMock = vi.mocked(saveSystemConfig);

function configView(overrides: Partial<SystemConfigView> = {}): SystemConfigView {
  return { system_name: "Spore", ...overrides };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <App>
        <MemoryRouter>
          <SystemConfigPage />
        </MemoryRouter>
      </App>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  fetchSystemConfigMock.mockReset();
  saveSystemConfigMock.mockReset();
  vi.clearAllMocks();
});

describe("系统设置页", () => {
  it("渲染唯一 H1「系统设置」与页面说明", async () => {
    fetchSystemConfigMock.mockResolvedValue(configView());

    renderPage();

    expect(
      await screen.findByRole("heading", { level: 1, name: "系统设置" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    // 说明区在配置加载完成后渲染（当前名称回显说明数据已就位）
    expect(await screen.findByText("当前名称：Spore")).toBeInTheDocument();
    expect(screen.getByText(/系统设置只承载系统身份/)).toBeInTheDocument();
  });

  it("回填服务端当前系统名称", async () => {
    fetchSystemConfigMock.mockResolvedValue(configView({ system_name: "MySpore" }));

    renderPage();

    await waitFor(() =>
      expect(screen.getByRole("textbox", { name: /系统名称/ })).toHaveValue("MySpore"),
    );
  });

  it("未变更时保存不可用；变更后展示未保存提示并提交去空白载荷", async () => {
    fetchSystemConfigMock.mockResolvedValue(configView());
    saveSystemConfigMock.mockResolvedValue({ ok: true, system_name: "New Name" });

    renderPage();

    const input = await screen.findByRole("textbox", { name: /系统名称/ });
    await waitFor(() => expect(input).toHaveValue("Spore"));
    expect(screen.getByRole("button", { name: "保 存" })).toBeDisabled();
    expect(screen.queryByText("有未保存的更改")).not.toBeInTheDocument();

    fireEvent.change(input, { target: { value: "  New Name  " } });
    expect(screen.getByText("有未保存的更改")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "保 存" }));
    await waitFor(() => expect(saveSystemConfigMock).toHaveBeenCalledWith("New Name"));
  });

  it("保存成功后清除未保存提示", async () => {
    fetchSystemConfigMock.mockResolvedValue(configView());
    saveSystemConfigMock.mockResolvedValue({ ok: true, system_name: "Next" });

    renderPage();

    const input = await screen.findByRole("textbox", { name: /系统名称/ });
    await waitFor(() => expect(input).toHaveValue("Spore"));
    fireEvent.change(input, { target: { value: "Next" } });
    fireEvent.click(screen.getByRole("button", { name: "保 存" }));

    await waitFor(() =>
      expect(screen.queryByText("有未保存的更改")).not.toBeInTheDocument(),
    );
  });

  it("读取失败时展示错误与重试", async () => {
    fetchSystemConfigMock.mockRejectedValue(new Error("boom"));

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    // antd Button 对双汉字文本自动插入空格（渲染为"重 试"），用正则匹配。
    expect(screen.getByRole("button", { name: /重\s*试/ })).toBeInTheDocument();
  });
});
