/**
 * 备份页组件测试（高风险页面迁移的确认交互与失败态）：
 * 待确认状态渲染、确认导入弹窗（SSR data-confirm 同款文案、danger 意图）、
 * 导出确认不是 danger（读取型操作）、文件选择提供清除入口、确认成功后
 * 失效备份 query 并提示、失败展示服务端受控文案且不误报成功。
 * 新增测试：支持全量备份导出、JSON 分项导出与打包导出、
 * 定时备份 + R2 上云卡片（掩码回显、保存载荷、连通性测试与失败告警）。
 * API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import {
  fetchBackupSchedule,
  fetchBackupStatus,
  fetchCloudDriveBackupStatus,
  type BackupScheduleView,
  type BackupView,
  type CloudDriveBackupStatus,
} from "../../api/admin";
import {
  confirmBackupImport,
  exportBackup,
  exportBackupAllJSON,
  exportBackupFull,
  exportBackupJSON,
  saveBackupSchedule,
  testBackupR2Connection,
} from "../../api/mutations";
import { BackupPage, CONFIRM_IMPORT_TEXT } from "./BackupPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchBackupStatus: vi.fn(),
    fetchCloudDriveBackupStatus: vi.fn(),
    fetchBackupSchedule: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    cancelCloudDriveBackupPending: vi.fn(),
    confirmBackupImport: vi.fn(),
    confirmCloudDriveBackupImport: vi.fn(),
    exportBackup: vi.fn(),
    exportBackupAllJSON: vi.fn(),
    exportBackupFull: vi.fn(),
    exportBackupJSON: vi.fn(),
    exportCloudDriveBackup: vi.fn(),
    importCloudDriveBackup: vi.fn(),
    restartServer: vi.fn(),
    rollbackCloudDriveBackup: vi.fn(),
    saveBackupSchedule: vi.fn(),
    testBackupR2Connection: vi.fn(),
    uploadBackup: vi.fn(),
    uploadBackupAllJSON: vi.fn(),
    uploadBackupJSON: vi.fn(),
  };
});

const fetchBackupStatusMock = vi.mocked(fetchBackupStatus);
const fetchCloudDriveBackupStatusMock = vi.mocked(fetchCloudDriveBackupStatus);
const fetchBackupScheduleMock = vi.mocked(fetchBackupSchedule);
const confirmBackupImportMock = vi.mocked(confirmBackupImport);
const exportBackupMock = vi.mocked(exportBackup);
const exportAllJsonMock = vi.mocked(exportBackupAllJSON);
const exportFullMock = vi.mocked(exportBackupFull);
const exportSingleJsonMock = vi.mocked(exportBackupJSON);
const saveScheduleMock = vi.mocked(saveBackupSchedule);
const testR2Mock = vi.mocked(testBackupR2Connection);

function backupView(overrides: Partial<BackupView> = {}): BackupView {
  return {
    db_path: "/data/spore.db",
    db_size_bytes: 458752,
    last_backup_at: 1756598400000,
    pending: false,
    json_files: [
      {
        name: "session.json",
        description: "Telegram 用户会话凭据",
        size_bytes: 4096,
        mod_time: 1756598400000,
      },
      {
        name: "peers.json",
        description: "Telegram 实体缓存",
        size_bytes: 512,
        mod_time: 1756598400000,
      },
    ],
    ...overrides,
  };
}

function cloudBackupStatus(
  overrides: Partial<CloudDriveBackupStatus> = {},
): CloudDriveBackupStatus {
  return {
    pending: false,
    rollback_available: false,
    ...overrides,
  };
}

function scheduleView(overrides: Partial<BackupScheduleView> = {}): BackupScheduleView {
  return {
    interval_hours: 6,
    keep_count: 8,
    last_backup_at: 1756598400000,
    r2: {
      enabled: true,
      complete: true,
      account_id: "0123456789abcdef0123456789abcdef",
      bucket: "spore-backup",
      endpoint: "https://0123456789abcdef0123456789abcdef.r2.cloudflarestorage.com",
      access_key_id: "********",
      secret_access_key: "********",
      last_upload_at: 1756598400000,
      last_upload_error: "",
    },
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidateSpy = vi.spyOn(client, "invalidateQueries");
  render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <MemoryRouter initialEntries={["/backup"]}>
          <BackupPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
  return invalidateSpy;
}

describe("数据备份页", () => {
  beforeEach(() => {
    fetchBackupStatusMock.mockReset();
    fetchCloudDriveBackupStatusMock.mockReset();
    fetchBackupScheduleMock.mockReset();
    confirmBackupImportMock.mockReset();
    exportBackupMock.mockReset();
    exportAllJsonMock.mockReset();
    exportFullMock.mockReset();
    exportSingleJsonMock.mockReset();
    saveScheduleMock.mockReset();
    testR2Mock.mockReset();

    fetchBackupStatusMock.mockResolvedValue(backupView({}));
    fetchCloudDriveBackupStatusMock.mockResolvedValue(cloudBackupStatus({}));
    fetchBackupScheduleMock.mockResolvedValue(scheduleView({}));
  });

  it("渲染唯一 H1「数据备份」、备份状态与导出入口；无待导入时不出现确认按钮", async () => {
    renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "数据备份" })).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(await screen.findByText(/\/data\/spore\.db/)).toBeInTheDocument();
    expect(screen.getByText(/最近备份/)).toBeInTheDocument();
    // 卡片中的导出按钮
    expect(screen.getByRole("button", { name: /一键导出全量包/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /导出所有 JSON/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /仅导出数据库/ })).toBeInTheDocument();
    expect(screen.queryByText(/待导入状态/)).not.toBeInTheDocument();

    // JSON 列表渲染
    expect(await screen.findByText("session.json")).toBeInTheDocument();
    expect(screen.getByText("peers.json")).toBeInTheDocument();
  });

  it("左侧锚点目录与三个区块 id 一一对应", async () => {
    renderPage();

    expect(await screen.findByRole("link", { name: "备份与导出" })).toHaveAttribute(
      "href",
      "#backup-export",
    );
    expect(screen.getByRole("link", { name: "定时备份 R2" })).toHaveAttribute("href", "#backup-r2");
    expect(screen.getByRole("link", { name: "数据恢复与导入" })).toHaveAttribute(
      "href",
      "#backup-restore",
    );
    // 锚点目标区块存在（PageSection 透传 id 到 Card 根节点）
    expect(document.getElementById("backup-export")).toBeInTheDocument();
    expect(document.getElementById("backup-r2")).toBeInTheDocument();
    expect(document.getElementById("backup-restore")).toBeInTheDocument();
  });

  it("导出数据库备份确认使用 default 意图（非 danger），确认后触发导出", async () => {
    exportBackupMock.mockResolvedValue(undefined);
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /仅导出数据库/ }));
    expect(await screen.findByText(/确定导出数据库备份？/)).toBeInTheDocument();
    expect(exportBackupMock).not.toHaveBeenCalled();

    const confirmButton = screen.getByRole("button", { name: "确 认" });
    expect(confirmButton).not.toHaveClass("ant-btn-dangerous");
    fireEvent.click(confirmButton);

    await waitFor(() => expect(exportBackupMock).toHaveBeenCalledTimes(1));
  });

  it("导出所有 JSON 直接触发", async () => {
    exportAllJsonMock.mockResolvedValue(undefined);
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /导出所有 JSON/ }));
    await waitFor(() => expect(exportAllJsonMock).toHaveBeenCalledTimes(1));
  });

  it("全量备份导出弹窗确认后触发", async () => {
    exportFullMock.mockResolvedValue(undefined);
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /一键导出全量包/ }));
    expect(await screen.findByText(/确定导出完整备份？/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "确 认" }));
    await waitFor(() => expect(exportFullMock).toHaveBeenCalledTimes(1));
  });

  it("单个 JSON 导出支持 Select 选择与表格行导出按钮", async () => {
    exportSingleJsonMock.mockResolvedValue(undefined);
    renderPage();

    expect(await screen.findByText("session.json")).toBeInTheDocument();
    // Select 选择器和导出按钮
    expect(screen.getByRole("button", { name: /导出选中配置/ })).toBeInTheDocument();

    // 在数据资产表格中点击 session.json 的导出按钮（accessible name 含图标 "download 导出"）
    const exportButtons = screen.getAllByRole("button", { name: /^download 导出$/ });
    expect(exportButtons.length).toBeGreaterThan(0);
    // 第二个导出按钮对应第一个 JSON 文件 (第一个是 spore.db)
    fireEvent.click(exportButtons[1]);
    await waitFor(() => expect(exportSingleJsonMock).toHaveBeenCalledWith("session.json"));
  });

  it("待确认状态展示摘要，确认弹窗文案与 SSR data-confirm 一致，确认后提交", async () => {
    fetchBackupStatusMock.mockResolvedValue(
      backupView({ pending: true, pending_state: "待确认", pending_sha256: "abc123" }),
    );
    confirmBackupImportMock.mockResolvedValue({
      ok: true,
      message: "备份已确认，将在下一次服务启动时应用；当前数据库尚未改变。",
      backup: backupView({ pending: true, pending_state: "confirmed", pending_sha256: "abc123" }),
    });
    const invalidateSpy = renderPage();

    expect(await screen.findByText(/待导入状态：待确认，摘要 abc123/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "确认导入（下次启动应用）" }));

    // 破坏性操作先弹二次确认（SSR data-confirm 同款文案）
    expect(await screen.findByText(CONFIRM_IMPORT_TEXT)).toBeInTheDocument();
    expect(confirmBackupImportMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(confirmBackupImportMock).toHaveBeenCalledTimes(1));
    expect(await screen.findByText("备份已确认，将在下一次服务启动时应用；当前数据库尚未改变。")).toBeInTheDocument();
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ["backup"] })),
    );
  });

  it("选择文件后展示文件信息并提供「清除选择」取消入口", async () => {
    renderPage();
    await screen.findByText(/\/data\/spore\.db/);

    // 切换到「业务数据库整库恢复」tab
    fireEvent.click(screen.getByText("业务数据库整库恢复"));

    const backup = new File(["sqlite"], "spore-backup.db", { type: "application/octet-stream" });
    const fileInput = document.querySelector('input[name="backup"]');
    expect(fileInput).not.toBeNull();
    fireEvent.change(fileInput as HTMLInputElement, { target: { files: [backup] } });

    expect(await screen.findByText(/已选择：spore-backup\.db/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "清除选择" }));

    expect(screen.queryByText(/已选择：spore-backup\.db/)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "上传并校验" })).toBeDisabled();
  });

  it("确认失败（无待导入备份）时展示服务端受控文案，不提示成功也不失效", async () => {
    fetchBackupStatusMock.mockResolvedValue(
      backupView({ pending: true, pending_state: "待确认", pending_sha256: "abc123" }),
    );
    confirmBackupImportMock.mockRejectedValue(
      new ApiError("没有可确认的待导入备份。", 400, "BAD_REQUEST"),
    );
    const invalidateSpy = renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "确认导入（下次启动应用）" }));
    fireEvent.click(await screen.findByText(CONFIRM_IMPORT_TEXT));
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    expect(await screen.findByText("没有可确认的待导入备份。")).toBeInTheDocument();
    await waitFor(() => expect(confirmBackupImportMock).toHaveBeenCalledTimes(1));
    expect(invalidateSpy).not.toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: ["backup"] }),
    );
    expect(
      screen.queryByText("备份已确认，将在下一次服务启动时应用；当前数据库尚未改变。"),
    ).not.toBeInTheDocument();
  });

  it("定时备份卡片回填脱敏视图：掩码入框、开关与端点状态可见", async () => {
    renderPage();

    expect(
      await screen.findByText("定时备份与云端同步（Cloudflare R2）"),
    ).toBeInTheDocument();
    // 两个密钥字段都回填为掩码（沿用语义的视觉锚点）
    expect(await screen.findAllByDisplayValue("********")).toHaveLength(2);
    // 开关回填为已开启
    await waitFor(() => {
      expect(screen.getByRole("switch")).toHaveAttribute("aria-checked", "true");
    });
    // 端点与最近上传状态行
    expect(await screen.findByText(/端点：.*r2\.cloudflarestorage\.com/)).toBeInTheDocument();
    expect(screen.getByText(/最近 R2 上传/)).toBeInTheDocument();
  });

  it("保存定时备份配置提交完整载荷（掩码原样传回 = 服务端沿用）", async () => {
    saveScheduleMock.mockResolvedValue({ ok: true, backup_schedule: scheduleView({}) });
    renderPage();
    await screen.findAllByDisplayValue("********");

    fireEvent.click(screen.getByRole("button", { name: "保存配置" }));

    await waitFor(() => expect(saveScheduleMock).toHaveBeenCalledTimes(1));
    expect(saveScheduleMock).toHaveBeenCalledWith({
      interval_hours: 6,
      keep_count: 8,
      r2: {
        enabled: true,
        account_id: "0123456789abcdef0123456789abcdef",
        access_key_id: "********",
        secret_access_key: "********",
        bucket: "spore-backup",
      },
    });
  });

  it("测试连接按钮触发 R2 连通性测试并提示成功", async () => {
    testR2Mock.mockResolvedValue({ ok: true, connected: true, message: "连接成功：R2 存储桶可访问。" });
    renderPage();
    await screen.findAllByDisplayValue("********");

    fireEvent.click(screen.getByRole("button", { name: /测试连接/ }));

    await waitFor(() => expect(testR2Mock).toHaveBeenCalledTimes(1));
    expect(await screen.findByText("连接成功：R2 存储桶可访问。")).toBeInTheDocument();
  });

  it("最近上传失败时展示受控场景告警", async () => {
    fetchBackupScheduleMock.mockResolvedValue(
      scheduleView({
        r2: {
          ...scheduleView().r2,
          last_upload_error: "R2 密钥无效或无权限（请核对 Access Key ID / Secret，或重新生成 API Token）",
        },
      }),
    );
    renderPage();

    expect(await screen.findByText(/最近一次 R2 上传未成功/)).toBeInTheDocument();
    expect(screen.getByText(/本地快照不受影响/)).toBeInTheDocument();
  });
});
