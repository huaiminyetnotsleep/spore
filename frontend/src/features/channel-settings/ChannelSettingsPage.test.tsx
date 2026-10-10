/**
 * 频道设置页测试：绑定频道投递开关的回填与切换即保存语义。
 * 查询与写接口以模块 mock 注入，不触网络。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "antd";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { fetchSettings, type SettingsView } from "../../api/admin";
import { saveSettings } from "../../api/mutations";
import { ChannelSettingsPage } from "./ChannelSettingsPage";

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
    dump_channels: [],
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
    watch_forward_channels: [],
    max_request_attempts: 3,
    backup_interval_hours: 6,
    backup_keep_count: 8,
    error_log_retention_days: 30,
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
          <ChannelSettingsPage />
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

describe("频道设置页", () => {
  it("渲染唯一 H1「频道设置」页面标题", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());

    renderPage();

    expect(
      await screen.findByRole("heading", { level: 1, name: "频道设置" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  it("首次加载完成前不渲染可提交 fallback 控件", async () => {
    let resolveSettings: (value: SettingsView) => void = () => undefined;
    fetchSettingsMock.mockReturnValue(
      new Promise((resolve) => {
        resolveSettings = resolve;
      }),
    );

    renderPage();

    expect(screen.queryByRole("switch", { name: "绑定频道投递总开关" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /清除配置|验证并添加/ })).not.toBeInTheDocument();
    expect(saveSettingsMock).not.toHaveBeenCalled();

    resolveSettings(settingsView());
    expect(await screen.findByRole("switch", { name: "绑定频道投递总开关" })).toBeChecked();
  });

  it("回填服务端当前开关值", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());

    renderPage();

    expect(await screen.findByRole("switch", { name: "绑定频道投递总开关" })).toBeChecked();
  });

  it("切换开关即保存 channel_copy_enabled", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();

    const toggle = await screen.findByRole("switch", { name: "绑定频道投递总开关" });
    fireEvent.click(toggle);

    await waitFor(() =>
      expect(saveSettingsMock).toHaveBeenCalledWith({ channel_copy_enabled: false }),
    );
  });

  it("缓存频道：多频道列表展示，新增频道整体替换保存", async () => {
    fetchSettingsMock.mockResolvedValue(
      settingsView({
        dump_channels: [
          { channel_id: -1001234567890, title: "Spore Cache", enabled: true },
          { channel_id: -100777, title: "备用缓存", enabled: false },
        ],
        dump_channel_id: -1001234567890,
        dump_channel_title: "Spore Cache",
      }),
    );
    saveSettingsMock.mockResolvedValue({
      ok: true,
      settings: settingsView({
        dump_channels: [
          { channel_id: -1001234567890, title: "Spore Cache", enabled: true },
          { channel_id: -100777, title: "备用缓存", enabled: false },
          { channel_id: -100555, title: "新缓存", enabled: true },
        ],
      }),
    });

    renderPage();
    expect((await screen.findAllByText("Spore Cache")).length).toBeGreaterThan(0);
    expect(screen.getByText("备用缓存")).toBeInTheDocument();
    expect(screen.getByText("已停用")).toBeInTheDocument();
    expect(screen.getByTestId("dump-channel-list")).toBeInTheDocument();

    const input = screen.getByRole("textbox", { name: "缓存频道目标" });
    fireEvent.change(input, { target: { value: "@sporecache" } });
    fireEvent.click(screen.getByRole("button", { name: /验证并添加/ }));

    await waitFor(() =>
      expect(saveSettingsMock).toHaveBeenCalledWith({
        dump_channels: [
          { id: -1001234567890, enabled: true },
          { id: -100777, enabled: false },
          { target: "@sporecache" },
        ],
      }),
    );
  });

  it("缓存频道：未配置时展示提示，添加按钮禁用", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());

    renderPage();

    expect(await screen.findByText("未配置（无复用）")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /验证并添加/ })).toBeDisabled();
    expect(saveSettingsMock).not.toHaveBeenCalled();
  });

  it("缓存频道启停：行内开关切换即保存，只翻转目标项", async () => {
    fetchSettingsMock.mockResolvedValue(
      settingsView({
        dump_channels: [
          { channel_id: -1001234567890, title: "Cache", enabled: true },
          { channel_id: -100777, title: "B", enabled: true },
        ],
        dump_channel_id: -1001234567890,
        dump_channel_title: "Cache",
      }),
    );
    saveSettingsMock.mockResolvedValue({
      ok: true,
      settings: settingsView({
        dump_channels: [
          { channel_id: -1001234567890, title: "Cache", enabled: false },
          { channel_id: -100777, title: "B", enabled: true },
        ],
      }),
    });

    renderPage();
    const toggle = await screen.findByRole("switch", { name: "启用缓存频道 Cache" });
    expect(toggle).toBeChecked();
    fireEvent.click(toggle);

    await waitFor(() =>
      expect(saveSettingsMock).toHaveBeenCalledWith({
        dump_channels: [
          { id: -1001234567890, enabled: false },
          { id: -100777, enabled: true },
        ],
      }),
    );
  });

  it("缓存频道移除走 danger 二次确认，确认后才提交替换列表", async () => {
    fetchSettingsMock.mockResolvedValue(
      settingsView({
        dump_channels: [{ channel_id: -1001234567890, title: "Cache", enabled: true }],
        dump_channel_id: -1001234567890,
        dump_channel_title: "Cache",
      }),
    );
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();
    expect((await screen.findAllByText("Cache")).length).toBeGreaterThan(0);

    // 移除按钮先弹 danger 确认，确认后才提交
    fireEvent.click(screen.getByRole("button", { name: "移 除" }));
    expect(saveSettingsMock).not.toHaveBeenCalled();
    expect(await screen.findByText(/确定移除缓存频道「Cache」/)).toBeInTheDocument();
    const dialog = within(screen.getByRole("dialog"));
    const confirmButton = dialog.getByRole("button", { name: /移\s*除/ });
    expect(confirmButton).toHaveClass("ant-btn-dangerous");

    fireEvent.click(confirmButton);
    await waitFor(() =>
      expect(saveSettingsMock).toHaveBeenCalledWith({ dump_channels: [] }),
    );
  });

  it("复用开关切换即保存 tg_reuse_enabled", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView({ tg_reuse_enabled: false }) });

    renderPage();
    const toggle = await screen.findByRole("switch", { name: "重复链接复用总开关" });
    expect(toggle).toBeChecked();
    fireEvent.click(toggle);

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledWith({ tg_reuse_enabled: false }));
  });

  it("重复链接检测窗口修改后显式保存 dedup_window_min", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView({ dedup_window_min: 30 }));
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView({ dedup_window_min: 60 }) });

    renderPage();
    const input = await screen.findByDisplayValue("30");
    fireEvent.change(input, { target: { value: "60" } });
    fireEvent.click(screen.getByRole("button", { name: /保存窗口/ }));

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledWith({ dedup_window_min: 60 }));
  });

  it("读取失败时展示错误与重试", async () => {
    fetchSettingsMock.mockRejectedValue(new Error("boom"));

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    // antd Button 对双汉字文本自动插入空格（渲染为"重 试"），用正则匹配。
    expect(screen.getByRole("button", { name: /重\s*试/ })).toBeInTheDocument();
  });
});
