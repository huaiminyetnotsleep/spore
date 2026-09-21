/**
 * 频道加入设置页测试：开关回填、切换即保存与上限显式保存语义。
 * 查询与写接口以模块 mock 注入，不触网络。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { fetchSettings, type SettingsView } from "../../api/admin";
import { saveSettings } from "../../api/mutations";
import { JoinSettingsPage } from "./JoinSettingsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchSettings: vi.fn(),
  };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    saveSettings: vi.fn(),
  };
});

const fetchSettingsMock = vi.mocked(fetchSettings);
const saveSettingsMock = vi.mocked(saveSettings);

function settingsView(overrides: Partial<SettingsView> = {}): SettingsView {
  return {
    timezone: "Asia/Shanghai",
    dedup_window_min: 30,
    max_links_per_message: 10,
    queue_capacity: 64,
    queue_runtime: 64,
    queue_same: true,
    worker_count: 1,
    worker_count_runtime: 1,
    worker_count_same: true,
    max_file_size_bytes: 2000 * 1024 * 1024,
    stream_limit_bytes: 20 * 1024 * 1024,
    max_file_size_runtime_bytes: 2000 * 1024 * 1024,
    stream_limit_runtime_bytes: 20 * 1024 * 1024,
    temp_dir_max_size_bytes: 5 * 1024 * 1024 * 1024,
    temp_dir_max_size_runtime_bytes: 5 * 1024 * 1024 * 1024,
    media_same: true,
    memory_budget_bytes: 1024 * 1024 * 1024,
    channel_copy_enabled: true,
    tg_reuse_enabled: true,
    dump_channel_id: 0,
    dump_channel_title: "",
    join_enabled: false,
    join_auto_leave_external: false,
    join_require_approval: true,
    join_max_channels: 20,
    join_mute_enabled: true,
    join_archive_enabled: true,
    watch_apply_enabled: false,
    watch_require_approval: true,
    watch_max_sources: 20,
    watch_per_user_limit: 3,
    max_request_attempts: 3,
    download_threads: 4,
    upload_threads: 4,
    download_connections: 4,
    upload_connections: 4,
    download_threads_env: 4,
    upload_threads_env: 4,
    download_connections_env: 4,
    upload_connections_env: 4,
    download_threads_overridden: false,
    upload_threads_overridden: false,
    download_connections_overridden: false,
    upload_connections_overridden: false,
    last_backup_at: 0,
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <App>
        <MemoryRouter>
          <JoinSettingsPage />
        </MemoryRouter>
      </App>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  fetchSettingsMock.mockReset();
  saveSettingsMock.mockReset();
  vi.clearAllMocks();
});

describe("频道加入设置页", () => {
  it("渲染唯一 H1「受邀设置」页面标题（与路由标签一致）", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());

    renderPage();

    expect(
      await screen.findByRole("heading", { level: 1, name: "受邀设置" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    // 说明区在配置加载完成后渲染；明确「切换即保存」与「保存上限」显式提交的差异
    expect(
      await screen.findByText(/加入数量上限修改后需点击「保存上限」提交/),
    ).toBeInTheDocument();
  });

  it("首次加载完成前不渲染可提交 fallback 控件", async () => {
    let resolveSettings: (value: SettingsView) => void = () => undefined;
    fetchSettingsMock.mockReturnValue(
      new Promise((resolve) => {
        resolveSettings = resolve;
      }),
    );

    renderPage();

    expect(screen.queryByRole("switch", { name: "允许加入频道总开关" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "保存上限" })).not.toBeInTheDocument();
    expect(saveSettingsMock).not.toHaveBeenCalled();

    resolveSettings(settingsView());
    expect(await screen.findByRole("switch", { name: "允许加入频道总开关" })).not.toBeChecked();
  });

  it("回填服务端当前值（开关与数量上限）", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();

    expect(await screen.findByRole("switch", { name: "允许加入频道总开关" })).not.toBeChecked();
    expect(screen.getByRole("switch", { name: "加入需审核开关" })).toBeChecked();
    expect(screen.getByRole("switch", { name: "加入后静音开关" })).toBeChecked();
    expect(screen.getByRole("switch", { name: "加入后归档开关" })).toBeChecked();
    expect(screen.getByLabelText("加入数量上限")).toHaveValue("20");
    expect(screen.getByText("默认关闭")).toBeInTheDocument();
  });

  it("切换总开关即保存 join_enabled", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();

    const toggle = await screen.findByRole("switch", { name: "允许加入频道总开关" });
    fireEvent.click(toggle);

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledWith({ join_enabled: true }));
  });

  it("修改数量上限需点保存上限才提交", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();

    const input = await screen.findByLabelText("加入数量上限");
    fireEvent.change(input, { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "保存上限" }));

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledWith({ join_max_channels: 30 }));
  });

  it("读取失败时展示错误与重试", async () => {
    fetchSettingsMock.mockRejectedValue(new Error("boom"));

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    // antd Button 对双汉字文本自动插入空格（渲染为"重 试"），用正则匹配。
    expect(screen.getByRole("button", { name: /重\s*试/ })).toBeInTheDocument();
  });
});
