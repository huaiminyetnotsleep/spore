/**
 * 备份页组件测试（高风险页面迁移的确认交互与失败态）：
 * 待确认状态渲染、确认导入弹窗（SSR data-confirm 同款文案）、确认成功后
 * 失效备份 query 并提示、失败展示服务端受控文案且不误报成功。
 * API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import { fetchBackupStatus, type BackupView } from "../../api/admin";
import { confirmBackupImport } from "../../api/mutations";
import { BackupPage, CONFIRM_IMPORT_TEXT } from "./BackupPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchBackupStatus: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    confirmBackupImport: vi.fn(),
  };
});

const fetchBackupStatusMock = vi.mocked(fetchBackupStatus);
const confirmBackupImportMock = vi.mocked(confirmBackupImport);

function backupView(overrides: Partial<BackupView> = {}): BackupView {
  return {
    db_path: "/data/spore.db",
    db_size_bytes: 458752,
    last_backup_at: 1756598400000,
    pending: false,
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidateSpy = vi.spyOn(client, "invalidateQueries");
  render(
    <QueryClientProvider client={client}>
      {/* antd App 上下文：message/modal 实例来自它（生产由 App.tsx 挂载） */}
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
    confirmBackupImportMock.mockReset();
    fetchBackupStatusMock.mockResolvedValue(backupView({}));
  });

  it("渲染备份状态与导出入口；无待导入时不出现确认按钮", async () => {
    renderPage();

    expect(await screen.findByRole("heading", { name: "数据备份" })).toBeInTheDocument();
    // 等待状态数据渲染后断言：表格单元格内含模板拼接文本（时间展示随本地时区变化）
    expect(await screen.findByText(/\/data\/spore\.db/)).toBeInTheDocument();
    expect(screen.getByText("最近备份")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /导出数据库备份/ })).toBeInTheDocument();
    expect(screen.queryByText(/待导入状态/)).not.toBeInTheDocument();
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
});
