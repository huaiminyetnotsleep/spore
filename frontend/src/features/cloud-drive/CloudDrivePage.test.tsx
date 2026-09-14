/**
 * 云盘下载设置页组件测试：配置回填、空状态引导、类型建议键预填、
 * 名称规则/唯一校验、保存载荷（options 行转对象）与目的地测试结果。
 * 查询与写接口以模块 mock 注入，不触网络。
 * 注意：类型 AutoComplete 的 placeholder 渲染为 span（非 input 属性），
 * 用 combobox 角色定位；Radio 的 value 会命中 getByDisplayValue，
 * 名称值断言统一经 placeholder 定位后 toHaveValue。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import {
  fetchCloudDrive,
  fetchCloudDriveBackupStatus,
  type CloudDriveBackupStatus,
  type CloudDriveView,
} from "../../api/admin";
import {
  cancelCloudDriveBackupPending,
  confirmCloudDriveBackupImport,
  exportCloudDriveBackup,
  importCloudDriveBackup,
  rollbackCloudDriveBackup,
  saveCloudDrive,
  testCloudDrive,
} from "../../api/mutations";
import { CloudDrivePage } from "./CloudDrivePage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchCloudDrive: vi.fn(),
    fetchCloudDriveBackupStatus: vi.fn(),
  };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    cancelCloudDriveBackupPending: vi.fn(),
    confirmCloudDriveBackupImport: vi.fn(),
    exportCloudDriveBackup: vi.fn(),
    importCloudDriveBackup: vi.fn(),
    rollbackCloudDriveBackup: vi.fn(),
    saveCloudDrive: vi.fn(),
    testCloudDrive: vi.fn(),
  };
});

const fetchCloudDriveMock = vi.mocked(fetchCloudDrive);
const fetchCloudDriveBackupStatusMock = vi.mocked(fetchCloudDriveBackupStatus);
const cancelCloudDriveBackupPendingMock = vi.mocked(cancelCloudDriveBackupPending);
const confirmCloudDriveBackupImportMock = vi.mocked(confirmCloudDriveBackupImport);
const exportCloudDriveBackupMock = vi.mocked(exportCloudDriveBackup);
const importCloudDriveBackupMock = vi.mocked(importCloudDriveBackup);
const rollbackCloudDriveBackupMock = vi.mocked(rollbackCloudDriveBackup);
const saveCloudDriveMock = vi.mocked(saveCloudDrive);
const testCloudDriveMock = vi.mocked(testCloudDrive);

function backupStatus(overrides: Partial<CloudDriveBackupStatus> = {}): CloudDriveBackupStatus {
  return {
    pending: false,
    rollback_available: false,
    ...overrides,
  };
}

function cloudView(overrides: Partial<CloudDriveView> = {}): CloudDriveView {
  return {
    enabled: false,
    default_destination: "mega-1",
    rclone_available: true,
    destinations: [
      {
        name: "mega-1",
        type: "mega",
        path_prefix: "spore",
        enabled: true,
        options: { user: "a@example.com", pass: "********" },
      },
    ],
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidateSpy = vi.spyOn(client, "invalidateQueries");
  render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <MemoryRouter>
          <CloudDrivePage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
  return invalidateSpy;
}

/** antd Button 对 CJK 文本自动插入空格（"保 存 配 置"），用正则匹配可访问名。 */
const SAVE_BUTTON = /保\s*存\s*配\s*置/;

/** 等待异步配置回填完成：名称输入出现且带服务端值（表单不再禁用）。 */
async function pageReady(name = "mega-1") {
  const nameInput = await screen.findByPlaceholderText("如 mega-1");
  await waitFor(() => expect(nameInput).toHaveValue(name));
  return nameInput;
}

describe("云盘下载设置页", () => {
  it("提供下载配置文档链接", async () => {
    fetchCloudDriveMock.mockResolvedValue(cloudView());
    renderPage();

    const link = await screen.findByRole("link", { name: "查看下载配置文档" });
    expect(link).toHaveAttribute(
      "href",
      "https://github.com/huaiminyetnotsleep/spore/blob/main/docs/guide/download.md",
    );
    expect(link).toHaveAttribute("target", "_blank");
  });

  beforeEach(() => {
    fetchCloudDriveMock.mockReset().mockResolvedValue(cloudView());
    fetchCloudDriveBackupStatusMock.mockReset().mockResolvedValue(backupStatus());
    cancelCloudDriveBackupPendingMock.mockReset().mockResolvedValue({ ok: true });
    confirmCloudDriveBackupImportMock
      .mockReset()
      .mockResolvedValue({ ok: true, rollback_available: true });
    exportCloudDriveBackupMock.mockReset().mockResolvedValue();
    importCloudDriveBackupMock.mockReset().mockResolvedValue({ ok: true });
    rollbackCloudDriveBackupMock.mockReset().mockResolvedValue({ ok: true });
    saveCloudDriveMock.mockReset().mockResolvedValue({ ok: true, config: cloudView() });
    testCloudDriveMock.mockReset();
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("回填服务端配置：开关、目的地公共字段、参数键值行与默认 Radio", async () => {
    renderPage();

    expect(await screen.findByRole("switch", { name: "云盘下载总开关" })).not.toBeChecked();
    const nameInput = await pageReady();
    expect(nameInput).toHaveValue("mega-1");
    expect(screen.getByRole("combobox")).toHaveValue("mega");
    expect(screen.getByPlaceholderText("如 spore")).toHaveValue("spore");
    // options 对象转键值行编辑器
    expect(screen.getByDisplayValue("user")).toBeInTheDocument();
    expect(screen.getByDisplayValue("a@example.com")).toBeInTheDocument();
    expect(screen.getByDisplayValue("********")).toBeInTheDocument();
    // default_destination=mega-1 → 该行默认 Radio 选中
    expect(screen.getByRole("radio", { name: /默认/ })).toBeChecked();
  });

  it("rclone 不可用时展示警示条", async () => {
    fetchCloudDriveMock.mockResolvedValue(cloudView({ rclone_available: false }));

    renderPage();

    expect(
      await screen.findByText("未检测到 rclone 二进制，云盘上传与目的地测试暂不可用"),
    ).toBeInTheDocument();
  });

  it("无目的地时展示空状态引导，点击「添加目的地」出现新卡片", async () => {
    fetchCloudDriveMock.mockResolvedValue(cloudView({ destinations: [] }));

    renderPage();

    expect(await screen.findByText(/尚未配置目的地/)).toBeInTheDocument();
    const addButton = screen.getByRole("button", { name: "添加目的地" });
    // 查询加载中表单整体禁用，先等按钮可用再点击
    await waitFor(() => expect(addButton).toBeEnabled());
    fireEvent.click(addButton);
    const nameInput = await screen.findByPlaceholderText("如 mega-1");
    expect(nameInput).toHaveValue("");
    expect(screen.getByRole("switch", { name: /启用目的地/ })).toBeChecked();
  });

  it("类型切换按建议键数据表预填空值行，敏感键名的值用密码输入", async () => {
    fetchCloudDriveMock.mockResolvedValue(
      cloudView({
        destinations: [
          { name: "new-dest", type: "", path_prefix: "", enabled: true, options: {} },
        ],
      }),
    );

    renderPage();
    await pageReady("new-dest");
    const typeInput = screen.getByRole("combobox");
    fireEvent.change(typeInput, { target: { value: "mega" } });

    // mega → user/pass/2fa 三个空值行；pass 与 2fa 命中敏感键名规则
    expect(await screen.findByDisplayValue("user")).toBeInTheDocument();
    expect(screen.getByDisplayValue("pass")).toBeInTheDocument();
    expect(screen.getByDisplayValue("2fa")).toBeInTheDocument();
    const passwordInputs = document.querySelectorAll('input[type="password"]');
    expect(passwordInputs.length).toBeGreaterThanOrEqual(2);
  });

  it("已有 options 的目的地切换类型不覆盖既有参数行", async () => {
    renderPage();
    await pageReady();
    fireEvent.change(screen.getByRole("combobox"), { target: { value: "s3" } });

    await waitFor(() =>
      expect(screen.getByDisplayValue("a@example.com")).toBeInTheDocument(),
    );
    // s3 建议键未注入（仍只有 user/pass 两行）
    expect(screen.queryByDisplayValue("access_key_id")).not.toBeInTheDocument();
  });

  it("名称非法（大写/下划线）时阻断保存并展示规则文案", async () => {
    renderPage();
    const nameInput = await pageReady();
    fireEvent.change(nameInput, { target: { value: "Bad_Name" } });

    fireEvent.click(screen.getByRole("button", { name: SAVE_BUTTON }));

    expect(
      await screen.findByText(
        "名称须为小写字母开头，仅含小写字母、数字或连字符，最长 32 字符（禁用下划线）。",
      ),
    ).toBeInTheDocument();
    expect(saveCloudDriveMock).not.toHaveBeenCalled();
  });

  it("列表内重名阻断保存并提示唯一性", async () => {
    renderPage();
    await pageReady();
    fireEvent.click(screen.getByRole("button", { name: "添加目的地" }));
    const nameInputs = await screen.findAllByPlaceholderText("如 mega-1");
    await waitFor(() => expect(nameInputs[1]).toBeEnabled());
    fireEvent.change(nameInputs[1], { target: { value: "mega-1" } });

    fireEvent.click(screen.getByRole("button", { name: SAVE_BUTTON }));

    // 唯一性校验对重复双方同时报错，断言至少出现一处
    const errors = await screen.findAllByText("名称已存在：目的地名称在列表内必须唯一。");
    expect(errors.length).toBeGreaterThanOrEqual(1);
    expect(saveCloudDriveMock).not.toHaveBeenCalled();
  }, 15_000);

  it("保存提交整表单载荷（options 行转对象）并失效 cloud-drive 查询", async () => {
    const invalidateSpy = renderPage();
    await pageReady();

    fireEvent.click(screen.getByRole("button", { name: SAVE_BUTTON }));

    await waitFor(() => expect(saveCloudDriveMock).toHaveBeenCalledTimes(1));
    expect(saveCloudDriveMock).toHaveBeenCalledWith({
      enabled: false,
      default_destination: "mega-1",
      destinations: [
        {
          name: "mega-1",
          type: "mega",
          path_prefix: "spore",
          enabled: true,
          options: { user: "a@example.com", pass: "********" },
        },
      ],
    });
    expect(await screen.findByText("云盘下载配置已保存。")).toBeInTheDocument();
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(
        expect.objectContaining({ queryKey: ["cloud-drive"] }),
      ),
    );
  });

  it("服务端校验失败（400 受控文案）只展示失败不提示成功", async () => {
    saveCloudDriveMock.mockRejectedValue(new ApiError("目的地名称已存在", 400, "BAD_REQUEST"));
    renderPage();
    await pageReady();

    fireEvent.click(screen.getByRole("button", { name: SAVE_BUTTON }));

    expect(await screen.findByText("目的地名称已存在")).toBeInTheDocument();
    expect(screen.queryByText("云盘下载配置已保存。")).not.toBeInTheDocument();
  });

  it("「测试」按钮展示行内连通结果（成功与失败原因）", async () => {
    testCloudDriveMock.mockResolvedValueOnce({ ok: true, message: "连接成功：可访问该网盘。" });
    testCloudDriveMock.mockResolvedValueOnce({
      ok: false,
      message: "网盘账号验证失败，请联系管理员",
    });
    renderPage();
    await pageReady();

    fireEvent.click(screen.getByRole("button", { name: "测 试" }));
    expect(await screen.findByText("连接成功：可访问该网盘。")).toBeInTheDocument();
    expect(testCloudDriveMock).toHaveBeenCalledWith("mega-1");

    fireEvent.click(screen.getByRole("button", { name: "测 试" }));
    expect(await screen.findByText("网盘账号验证失败，请联系管理员")).toBeInTheDocument();
  });

  it("切换默认目的地 Radio 后保存提交新的 default_destination", async () => {
    fetchCloudDriveMock.mockResolvedValue(
      cloudView({
        destinations: [
          {
            name: "mega-1",
            type: "mega",
            path_prefix: "spore",
            enabled: true,
            options: { user: "a@example.com", pass: "********" },
          },
          {
            name: "s3-1",
            type: "s3",
            path_prefix: "",
            enabled: true,
            options: { provider: "AWS" },
          },
        ],
      }),
    );
    renderPage();
    const radios = await screen.findAllByRole("radio", { name: /默认/ });
    await waitFor(() => expect(radios[0]).toBeChecked());
    fireEvent.click(radios[1]);
    await waitFor(() => expect(radios[1]).toBeChecked());

    fireEvent.click(screen.getByRole("button", { name: SAVE_BUTTON }));
    await waitFor(() => expect(saveCloudDriveMock).toHaveBeenCalledTimes(1));
    const input = saveCloudDriveMock.mock.calls[0][0];
    expect(input.default_destination).toBe("s3-1");
    expect(input.destinations).toHaveLength(2);
    expect(input.destinations[1].options).toEqual({ provider: "AWS" });
  });

  it("删除默认目的地时清空默认选择", async () => {
    renderPage();
    await pageReady();
    expect(screen.getByRole("radio", { name: /默认/ })).toBeChecked();

    fireEvent.click(screen.getByRole("button", { name: "删 除" }));
    // 唯一目的地已删除：卡片与其默认 Radio 一并消失
    expect(screen.queryByRole("radio", { name: /默认/ })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: SAVE_BUTTON }));
    await waitFor(() => expect(saveCloudDriveMock).toHaveBeenCalledTimes(1));
    expect(saveCloudDriveMock.mock.calls[0][0].default_destination).toBe("");
  });

  it("导出弹窗校验密码长度与确认值，成功后清空并关闭", async () => {
    renderPage();
    await pageReady();

    fireEvent.click(screen.getByRole("button", { name: /导\s*出\s*加\s*密\s*备\s*份/ }));
    const dialog = await screen.findByRole("dialog", { name: "导出云盘配置加密备份" });
    const password = within(dialog).getByLabelText("备份密码");
    const confirmation = within(dialog).getByLabelText("确认密码");
    fireEvent.change(password, { target: { value: "short" } });
    fireEvent.change(confirmation, { target: { value: "short" } });
    fireEvent.click(screen.getByRole("button", { name: /导\s*出\s*备\s*份/ }));

    expect(await screen.findByText("备份密码至少 8 个字符。")).toBeInTheDocument();
    expect(exportCloudDriveBackupMock).not.toHaveBeenCalled();

    fireEvent.change(password, { target: { value: "correct-password" } });
    fireEvent.change(confirmation, { target: { value: "different-password" } });
    fireEvent.click(screen.getByRole("button", { name: /导\s*出\s*备\s*份/ }));
    expect(await screen.findByText("两次输入的备份密码不一致。")).toBeInTheDocument();

    fireEvent.change(confirmation, { target: { value: "correct-password" } });
    fireEvent.click(screen.getByRole("button", { name: /导\s*出\s*备\s*份/ }));
    await waitFor(() =>
      expect(exportCloudDriveBackupMock).toHaveBeenCalledWith("correct-password"),
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "导出云盘配置加密备份" })).not.toBeInTheDocument(),
    );
  }, 15_000);

  it("上传 ZIP 与密码只验证候选，不触发配置应用", async () => {
    renderPage();
    await pageReady();
    const backup = new File(["zip"], "cloud.zip", { type: "application/zip" });
    const fileInput = document.querySelector('input[type="file"]');
    expect(fileInput).not.toBeNull();
    fireEvent.change(fileInput as HTMLInputElement, { target: { files: [backup] } });
    fireEvent.change(screen.getByLabelText("备份密码"), {
      target: { value: "correct-password" },
    });

    fireEvent.click(screen.getByRole("button", { name: /上\s*传\s*并\s*验\s*证/ }));

    await waitFor(() =>
      expect(importCloudDriveBackupMock).toHaveBeenCalledWith(backup, "correct-password"),
    );
    expect(confirmCloudDriveBackupImportMock).not.toHaveBeenCalled();
    expect(saveCloudDriveMock).not.toHaveBeenCalled();
    expect(await screen.findByText("备份已验证，尚未应用。")).toBeInTheDocument();
  });

  it("展示候选元数据，确认前不应用，并支持确认与取消", async () => {
    fetchCloudDriveBackupStatusMock.mockResolvedValue(
      backupStatus({
        pending: true,
        format_version: 1,
        created_at: "2025-09-10T00:00:00Z",
        sha256: "1234567890abcdef1234",
        destination_names: ["mega-1", "s3-main"],
      }),
    );
    const invalidateSpy = renderPage();
    await pageReady();

    expect(await screen.findByText("候选备份已通过验证，尚未应用")).toBeInTheDocument();
    expect(screen.getByText("1234567890ab…")).toBeInTheDocument();
    expect(screen.getByText("mega-1、s3-main")).toBeInTheDocument();
    expect(screen.getByText("2")).toBeInTheDocument();
    expect(confirmCloudDriveBackupImportMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: /确\s*认\s*整\s*体\s*替\s*换/ }));
    expect(confirmCloudDriveBackupImportMock).not.toHaveBeenCalled();
    fireEvent.click(await screen.findByRole("button", { name: /^确\s*认$/ }));
    await waitFor(() => expect(confirmCloudDriveBackupImportMock).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(
        expect.objectContaining({ queryKey: ["cloud-drive"] }),
      ),
    );
    expect(invalidateSpy).toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: ["cloud-drive-backup"] }),
    );

    fireEvent.click(screen.getByRole("button", { name: /取\s*消\s*候\s*选/ }));
    await waitFor(() => expect(cancelCloudDriveBackupPendingMock).toHaveBeenCalledTimes(1));
  }, 15_000);

  it("rollback 可用时二次确认恢复上一个配置", async () => {
    fetchCloudDriveBackupStatusMock.mockResolvedValue(
      backupStatus({ rollback_available: true }),
    );
    renderPage();
    await pageReady();

    fireEvent.click(screen.getByRole("button", { name: /恢\s*复\s*上\s*一\s*个\s*配\s*置/ }));
    expect(rollbackCloudDriveBackupMock).not.toHaveBeenCalled();
    fireEvent.click(await screen.findByRole("button", { name: /^确\s*认$/ }));
    await waitFor(() => expect(rollbackCloudDriveBackupMock).toHaveBeenCalledTimes(1));
  });

  it("导入失败只展示服务端受控文案", async () => {
    importCloudDriveBackupMock.mockRejectedValue(
      new ApiError("备份密码错误或备份已损坏。", 400, "INVALID_BACKUP"),
    );
    renderPage();
    await pageReady();
    const backup = new File(["zip"], "cloud.zip", { type: "application/zip" });
    fireEvent.change(document.querySelector('input[type="file"]') as HTMLInputElement, {
      target: { files: [backup] },
    });
    fireEvent.change(screen.getByLabelText("备份密码"), { target: { value: "wrong-password" } });
    fireEvent.click(screen.getByRole("button", { name: /上\s*传\s*并\s*验\s*证/ }));

    expect(await screen.findByText("备份密码错误或备份已损坏。")).toBeInTheDocument();
    expect(screen.queryByText("备份已验证，尚未应用。")).not.toBeInTheDocument();
  });

  it("导出 Blob 使用响应文件名并立即释放 object URL", async () => {
    const actual = await vi.importActual<typeof import("../../api/mutations")>(
      "../../api/mutations",
    );
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(new Blob(["encrypted"]), {
        status: 200,
        headers: {
          "Content-Type": "application/zip",
          "Content-Disposition": 'attachment; filename="cloud-drive.zip"',
        },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const createObjectURL = vi.fn().mockReturnValue("blob:cloud-backup");
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: createObjectURL });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: revokeObjectURL });
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    await actual.exportCloudDriveBackup("correct-password");

    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/cloud-drive/backup/export",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({
          password: "correct-password",
          password_confirmation: "correct-password",
        }),
      }),
    );
    expect(createObjectURL).toHaveBeenCalledTimes(1);
    expect(clickSpy).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:cloud-backup");
    clickSpy.mockRestore();
    vi.unstubAllGlobals();
  });

  it("确认、取消与 rollback API 使用固定方法和确认值", async () => {
    const actual = await vi.importActual<typeof import("../../api/mutations")>(
      "../../api/mutations",
    );
    const fetchMock = vi.fn().mockImplementation(async () =>
      new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    );
    vi.stubGlobal("fetch", fetchMock);

    await actual.confirmCloudDriveBackupImport();
    await actual.cancelCloudDriveBackupPending();
    await actual.rollbackCloudDriveBackup();

    expect(fetchMock).toHaveBeenNthCalledWith(
      1,
      "/api/v1/cloud-drive/backup/import/confirm",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ confirm: "import_cloud_drive" }),
      }),
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      2,
      "/api/v1/cloud-drive/backup/pending",
      expect.objectContaining({ method: "DELETE" }),
    );
    expect(fetchMock).toHaveBeenNthCalledWith(
      3,
      "/api/v1/cloud-drive/backup/rollback",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ confirm: "rollback_cloud_drive" }),
      }),
    );
    vi.unstubAllGlobals();
  });

  it("读取失败时展示错误与重试", async () => {
    fetchCloudDriveMock.mockRejectedValue(new Error("boom"));
    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /重\s*试/ })).toBeInTheDocument();
  });
});
