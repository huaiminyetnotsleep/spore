/**
 * 运行设置页测试：基础设置的回填与"缺省不变更"保存语义。
 * 频道加入与频道同步配置已拆分至「受邀设置」「频道设置」独立页面，
 * 本页不再渲染对应区块；媒体大小输入留空时载荷不携带。
 * 查询与写接口以模块 mock 注入，不触网络。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { fetchBackupStatus, fetchSettings, type SettingsView } from "../../api/admin";
import { saveSettings, type SettingsSaveInput } from "../../api/mutations";
import { SettingsPage } from "./SettingsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchSettings: vi.fn(),
    fetchBackupStatus: vi.fn(),
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
const fetchBackupStatusMock = vi.mocked(fetchBackupStatus);
const saveSettingsMock = vi.mocked(saveSettings);

const backupView = {
  db_path: "data/spore.db",
  db_size_bytes: 1,
  last_backup_at: 0,
  pending: false,
};

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
          <SettingsPage />
        </MemoryRouter>
      </App>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  fetchSettingsMock.mockReset();
  fetchBackupStatusMock.mockReset();
  saveSettingsMock.mockReset();
  vi.clearAllMocks();
});

describe("运行设置页", () => {
  it("渲染唯一 H1「运行设置」页面标题", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    fetchBackupStatusMock.mockResolvedValue(backupView);

    renderPage();

    expect(
      await screen.findByRole("heading", { level: 1, name: "运行设置" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  it("回填服务端当前值（时区/队列容量）", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    fetchBackupStatusMock.mockResolvedValue(backupView);

    const { container } = renderPage();

    await waitFor(() =>
      expect(screen.getByDisplayValue("Asia/Shanghai")).toBeInTheDocument(),
    );
    expect(container.querySelector("form")).toHaveClass("settings-form");
    await waitFor(() => expect(screen.getByDisplayValue("64")).toBeInTheDocument());
    expect(screen.queryByText("重复链接检测窗口（分钟）")).not.toBeInTheDocument();
  });

  it("显示传输配置的生效值、环境默认和来源", async () => {
    fetchSettingsMock.mockResolvedValue(
      settingsView({
        download_threads: 8,
        download_threads_env: 4,
        download_threads_overridden: true,
      }),
    );
    fetchBackupStatusMock.mockResolvedValue(backupView);

    renderPage();

    expect(await screen.findByText("数据库覆盖")).toBeInTheDocument();
    // 元信息合并为紧凑单行：当前生效 · .env 默认 + 来源 Tag
    expect(screen.getByText(/当前生效：8 · \.env 默认：4/)).toBeInTheDocument();
    expect(screen.getAllByText(/\.env 默认：4/)).toHaveLength(4);
    expect(screen.getAllByRole("button", { name: "恢复环境默认" })).toHaveLength(4);
  });

  it("恢复环境默认提交 clear_transfer_overrides", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView({ upload_connections_overridden: true }));
    fetchBackupStatusMock.mockResolvedValue(backupView);
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();

    await screen.findByText("数据库覆盖");
    const restore = await screen.findAllByRole("button", { name: "恢复环境默认" });
    const enabledRestore = restore.find((button) => !button.hasAttribute("disabled"));
    expect(enabledRestore).toBeDefined();
    fireEvent.click(enabledRestore!);

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledWith({
      clear_transfer_overrides: ["upload_connections"],
    }));
  });

  it("保存基础字段：未编辑的媒体大小输入不进载荷（缺省不变更）", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    fetchBackupStatusMock.mockResolvedValue(backupView);
    saveSettingsMock.mockResolvedValue({
      ok: true,
      settings: settingsView(),
    });

    renderPage();

    await waitFor(() => expect(screen.getByDisplayValue("Asia/Shanghai")).toBeInTheDocument());
    const timezone = screen.getByDisplayValue("Asia/Shanghai");
    fireEvent.change(timezone, { target: { value: "UTC" } });
    fireEvent.click(screen.getByRole("button", { name: "保存设置" }));

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledTimes(1));
    const input = saveSettingsMock.mock.calls[0][0] as SettingsSaveInput;
    expect(input.timezone).toBe("UTC");
    expect(input.dedup_window_min).toBeUndefined();
    expect(input.max_file_size).toBeUndefined();
    expect(input.stream_limit).toBeUndefined();
    expect(input.temp_dir_max_size).toBeUndefined();
  });

  it("保存单次最大链接数并携带到载荷", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    fetchBackupStatusMock.mockResolvedValue(backupView);
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView({ max_links_per_message: 20 }) });

    renderPage();

    const limit = await screen.findByRole("spinbutton", { name: /单次最大链接数/ });
    await waitFor(() => expect(limit).toHaveValue("10"));
    fireEvent.change(limit, { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: "保存设置" }));

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledTimes(1));
    const input = saveSettingsMock.mock.calls[0][0] as SettingsSaveInput;
    expect(input.max_links_per_message).toBe(20);
  });

  it("保存任务并发 worker 数并携带到载荷", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    fetchBackupStatusMock.mockResolvedValue(backupView);
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();

    const worker = await screen.findByDisplayValue("1");
    fireEvent.change(worker, { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "保存设置" }));

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledTimes(1));
    const input = saveSettingsMock.mock.calls[0][0] as SettingsSaveInput;
    expect(input.worker_count).toBe(4);
  });

  it("保存内存预算：携带数值与单位，占位符显示当前生效值", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    fetchBackupStatusMock.mockResolvedValue(backupView);
    saveSettingsMock.mockResolvedValue({ ok: true, settings: settingsView() });

    renderPage();

    // 占位符展示当前生效值（1GB → "1.00 GiB"）
    expect(await screen.findByPlaceholderText("当前 1.00 GiB")).toBeInTheDocument();
    const budget = screen.getByPlaceholderText("当前 1.00 GiB");
    fireEvent.change(budget, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "保存设置" }));

    await waitFor(() => expect(saveSettingsMock).toHaveBeenCalledTimes(1));
    const input = saveSettingsMock.mock.calls[0][0] as SettingsSaveInput;
    expect(input.memory_budget).toBe("2");
    expect(input.memory_budget_unit).toBe("GB");
  });

  it("worker 数存在待重启差异时显示提示与状态", async () => {
    fetchSettingsMock.mockResolvedValue(
      settingsView({ worker_count: 4, worker_count_runtime: 1, worker_count_same: false }),
    );
    fetchBackupStatusMock.mockResolvedValue(backupView);

    renderPage();

    expect(await screen.findByText(/当前配置：4；当前进程：1/)).toBeInTheDocument();
    expect(
      await screen.findByText(/任务并发 worker 数（4）将在下次重启后生效/),
    ).toBeInTheDocument();
  });

  it("频道加入与频道同步区块已拆分至独立页面，本页不再渲染", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    fetchBackupStatusMock.mockResolvedValue(backupView);

    renderPage();

    await waitFor(() => expect(screen.getByDisplayValue("Asia/Shanghai")).toBeInTheDocument());
    expect(screen.queryByText("频道加入（/join）")).not.toBeInTheDocument();
    expect(screen.queryByText("频道同步")).not.toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "允许加入频道总开关" })).not.toBeInTheDocument();
    expect(screen.queryByRole("switch", { name: "频道副本同步总开关" })).not.toBeInTheDocument();
  });

  it("媒体大小字段为可选覆盖语义：留空保持当前值，无 required 标记", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    fetchBackupStatusMock.mockResolvedValue(backupView);

    const { container } = renderPage();

    // 四个「留空保持当前值」字段不再展示视觉 required 星号
    for (const label of [
      "下载内存预算（64MB–8GB，留空保持当前值）",
      "文件大小上限（1MB–2000MB，留空保持当前值）",
      "流式传输阈值（1MB–2GB，留空保持当前值）",
      "临时目录最大大小（1MB–1TB，留空保持当前值）",
    ]) {
      const labelEl = await screen.findByText(label);
      expect(labelEl.closest(".ant-form-item")).not.toHaveClass("ant-form-item-required");
    }
    // 占位符仍回显当前生效值
    expect(container.querySelector('input[placeholder^="当前 "]')).not.toBeNull();
  });

  it("分区使用 PageSection 且保存按钮位于 FormActions", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    fetchBackupStatusMock.mockResolvedValue(backupView);

    const { container } = renderPage();

    await waitFor(() => expect(screen.getByDisplayValue("Asia/Shanghai")).toBeInTheDocument());
    // 表单内 3 个分区 + 状态网格 2 个分区（页面骨架不再套外层 Card）
    const form = container.querySelector("form");
    expect(form).not.toBeNull();
    expect(form?.querySelectorAll(".page-section").length).toBe(3);
    expect(container.querySelectorAll(".settings-status-grid .page-section").length).toBe(2);
    expect(form).toHaveClass("settings-form");
    expect(container.querySelector(".settings-field-grid")).toBeInTheDocument();
    expect(container.querySelector(".settings-status-grid")).toBeInTheDocument();
    // 保存按钮仍在 Form 内（提交语义不变），位于统一 FormActions
    const submit = screen.getByRole("button", { name: "保存设置" });
    expect(submit.closest("form")).toBe(form);
    expect(submit.closest(".form-actions")).not.toBeNull();
  });

  it("传输并发说明使用紧凑小字样式", async () => {
    fetchSettingsMock.mockResolvedValue(settingsView());
    fetchBackupStatusMock.mockResolvedValue(backupView);

    renderPage();

    const note = await screen.findByText(
      /线程数将在下一任务（上传为下一次上传）生效/,
      { selector: ".settings-note" },
    );
    expect(note).toBeInTheDocument();
    expect(note.textContent).toContain("单线程/单连接基线");
  });

  it("队列容量差异计入待重启状态，不再显示没有待生效变更", async () => {
    fetchSettingsMock.mockResolvedValue(
      settingsView({ queue_capacity: 128, queue_runtime: 64, queue_same: false }),
    );
    fetchBackupStatusMock.mockResolvedValue(backupView);

    renderPage();

    expect(
      await screen.findByText(/全局队列容量（128）将在下次重启后生效/),
    ).toBeInTheDocument();
    expect(screen.queryByText("当前没有检测到待生效变更。")).not.toBeInTheDocument();
    // 生效状态卡同步展示队列差异
    const queueLabels = screen.getAllByText("全局队列容量：");
    expect(queueLabels.length).toBeGreaterThan(0);
  });

  it("优雅重启只保留头像菜单入口，页面不出现第二个重启按钮", async () => {
    fetchSettingsMock.mockResolvedValue(
      settingsView({ worker_count: 4, worker_count_runtime: 1, worker_count_same: false }),
    );
    fetchBackupStatusMock.mockResolvedValue(backupView);

    renderPage();

    expect(await screen.findByText(/检测到 1 项待重启变更/)).toBeInTheDocument();
    expect(
      screen.getByText(/右上角头像菜单 →「优雅重启」/),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /优雅重启|重启/ })).not.toBeInTheDocument();
  });
});
