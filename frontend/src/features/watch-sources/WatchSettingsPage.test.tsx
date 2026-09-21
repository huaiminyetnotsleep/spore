/**
 * 监听源配置页测试：首次加载 gate、开关切换即保存、
 * 上限显式提交与脏检查、异常重试态。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchSettings, type SettingsView } from "../../api/admin";
import { saveSettings } from "../../api/mutations";
import { WatchSettingsPage } from "./WatchSettingsPage";

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

const mockFetchSettings = vi.mocked(fetchSettings);
const mockSaveSettings = vi.mocked(saveSettings);

function sampleSettings(overrides: Partial<SettingsView> = {}): SettingsView {
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
      <AntApp component={false}>
        <MemoryRouter>
          <WatchSettingsPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

describe("监听源配置页", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("渲染唯一 H1 标题「监听源配置」并在数据到达后渲染表单控件", async () => {
    mockFetchSettings.mockResolvedValueOnce(sampleSettings());
    renderPage();

    expect(screen.getByRole("heading", { level: 1, name: "监听源配置" })).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getByLabelText("用户自助申请开关")).toBeInTheDocument();
    });

    const applySwitch = screen.getByLabelText("用户自助申请开关");
    expect(applySwitch).toHaveAttribute("aria-checked", "false");

    const approvalSwitch = screen.getByLabelText("申请审批开关");
    expect(approvalSwitch).toHaveAttribute("aria-checked", "true");

    // 保存上限按钮初始禁用（未脏）
    expect(screen.getByRole("button", { name: "保存上限" })).toBeDisabled();
  });

  it("切换用户自助申请开关即时保存", async () => {
    mockFetchSettings.mockResolvedValueOnce(sampleSettings({ watch_apply_enabled: false }));
    mockSaveSettings.mockResolvedValueOnce({
      ok: true,
      settings: sampleSettings({ watch_apply_enabled: true }),
    });

    renderPage();

    await waitFor(() => {
      expect(screen.getByLabelText("用户自助申请开关")).toBeInTheDocument();
    });

    const applySwitch = screen.getByLabelText("用户自助申请开关");
    fireEvent.click(applySwitch);

    await waitFor(() => {
      expect(mockSaveSettings).toHaveBeenCalledWith({ watch_apply_enabled: true });
    });
  });

  it("切换用户申请审批开关即时保存", async () => {
    mockFetchSettings.mockResolvedValueOnce(sampleSettings({ watch_require_approval: true }));
    mockSaveSettings.mockResolvedValueOnce({
      ok: true,
      settings: sampleSettings({ watch_require_approval: false }),
    });

    renderPage();

    await waitFor(() => {
      expect(screen.getByLabelText("申请审批开关")).toBeInTheDocument();
    });

    const approvalSwitch = screen.getByLabelText("申请审批开关");
    fireEvent.click(approvalSwitch);

    await waitFor(() => {
      expect(mockSaveSettings).toHaveBeenCalledWith({ watch_require_approval: false });
    });
  });

  it("修改上限后解除保存按钮禁用，点击后显式保存", async () => {
    mockFetchSettings.mockResolvedValueOnce(
      sampleSettings({ watch_max_sources: 20, watch_per_user_limit: 3 }),
    );
    mockSaveSettings.mockResolvedValueOnce({
      ok: true,
      settings: sampleSettings({ watch_max_sources: 30, watch_per_user_limit: 3 }),
    });

    renderPage();

    await waitFor(() => {
      expect(screen.getByLabelText("监听源总数上限")).toBeInTheDocument();
    });

    const maxInput = screen.getByLabelText("监听源总数上限");
    fireEvent.change(maxInput, { target: { value: "30" } });

    const saveBtn = screen.getByRole("button", { name: "保存上限" });
    expect(saveBtn).toBeEnabled();

    fireEvent.click(saveBtn);

    await waitFor(() => {
      expect(mockSaveSettings).toHaveBeenCalledWith({ watch_max_sources: 30, watch_per_user_limit: 3 });
    });
  });
});
