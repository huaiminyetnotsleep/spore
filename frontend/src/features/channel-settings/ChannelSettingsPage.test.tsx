/**
 * 频道设置页测试：副本同步开关的回填与切换即保存语义。
 * 查询与写接口以模块 mock 注入，不触网络。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
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
    backup_interval_hours: 6,
    backup_keep_count: 8,
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

    expect(screen.queryByRole("switch", { name: "频道副本同步总开关" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /清除配置|验证并保存/ })).not.toBeInTheDocument();
    expect(saveSettingsMock).not.toHaveBeenCalled();

    resolveSettings(settingsView());
    expect(await screen.findByRole("switch", { name: "频道副本同步总开关" })).toBeChecked();
  });

  it("回填服务端当前开关值", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());

    renderPage();

    expect(await screen.findByRole("switch", { name: "频道副本同步总开关" })).toBeChecked();
  });

  it("切换开关即保存 channel_copy_enabled", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();

    const toggle = await screen.findByRole("switch", { name: "频道副本同步总开关" });
    fireEvent.click(toggle);

    await waitFor(() =>
      expect(saveSettingsMock).toHaveBeenCalledWith({ channel_copy_enabled: false }),
    );
  });

  it("缓存频道：显示当前配置并按输入保存 dump_channel", async () => {
    fetchSettingsMock.mockResolvedValue(
      settingsView({ dump_channel_id: -1001234567890, dump_channel_title: "Spore Cache" }),
    );
    saveSettingsMock.mockResolvedValue({
      ok: true,
      settings: settingsView({ dump_channel_id: -1001234567890, dump_channel_title: "Spore Cache" }),
    });

    renderPage();
    expect(await screen.findByText("Spore Cache")).toBeInTheDocument();

    const input = screen.getByRole("textbox", { name: "缓存频道目标" });
    fireEvent.change(input, { target: { value: "@sporecache" } });
    fireEvent.click(screen.getByRole("button", { name: /验证并保存/ }));

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledWith({ dump_channel: "@sporecache" }));
  });

  it("缓存频道：未配置时展示提示，输入为空按钮禁用", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());

    renderPage();

    expect(await screen.findByText("未配置（无复用）")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "验证并保存" })).toBeDisabled();
    expect(saveSettingsMock).not.toHaveBeenCalled();
  });

  it("缓存频道清除走 danger 二次确认，确认后才提交空载荷", async () => {
    fetchSettingsMock.mockResolvedValue(
      settingsView({ dump_channel_id: -1001234567890, dump_channel_title: "Cache" }),
    );
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();
    expect(await screen.findByText("Cache")).toBeInTheDocument();

    // 当前已配置且输入为空 → 按钮为「清除配置」，先弹 danger 确认
    fireEvent.click(screen.getByRole("button", { name: "清除配置" }));
    expect(saveSettingsMock).not.toHaveBeenCalled();
    expect(
      await screen.findByText(/确定清除缓存频道配置？重复链接将回到完整下载上传/),
    ).toBeInTheDocument();
    const confirmButton = screen.getByRole("button", { name: "清 除" });
    expect(confirmButton).toHaveClass("ant-btn-dangerous");

    fireEvent.click(confirmButton);
    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledWith({ dump_channel: "" }));
  });

  it("输入非空时按钮为「验证并保存」，不经过确认直接提交", async () => {
    fetchSettingsMock.mockResolvedValue(
      settingsView({ dump_channel_id: -1001234567890, dump_channel_title: "Cache" }),
    );
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();
    expect(await screen.findByText("Cache")).toBeInTheDocument();

    const input = screen.getByRole("textbox", { name: "缓存频道目标" });
    fireEvent.change(input, { target: { value: "@sporecache" } });
    fireEvent.click(screen.getByRole("button", { name: "验证并保存" }));

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledWith({ dump_channel: "@sporecache" }));
    expect(screen.queryByText(/确定清除缓存频道配置/)).not.toBeInTheDocument();
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
