/**
 * 云盘下载设置页组件测试：配置回填、空状态引导、类型建议键预填、
 * 名称规则/唯一校验、弹窗表单保存直接生效、弹窗内连通性自测、
 * 行内 Radio 与 Switch 直接生效、删除二次确认及配置备份恢复。
 * 查询与写接口以模块 mock 注入，不触网络。
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
  fetchUsers,
  type CloudDriveBackupStatus,
  type CloudDriveView,
  type ListEnvelope,
  type UserRow,
} from "../../api/admin";
import {
  cancelCloudDriveBackupPending,
  confirmCloudDriveBackupImport,
  exportCloudDriveBackup,
  importCloudDriveBackup,
  rollbackCloudDriveBackup,
  saveCloudDrive,
  setUserCloudDownload,
  testCloudDrive,
} from "../../api/mutations";
import { CloudDrivePage } from "./CloudDrivePage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchCloudDrive: vi.fn(),
    fetchCloudDriveBackupStatus: vi.fn(),
    fetchUsers: vi.fn(),
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
    setUserCloudDownload: vi.fn(),
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
const fetchUsersMock = vi.mocked(fetchUsers);
const setUserCloudDownloadMock = vi.mocked(setUserCloudDownload);

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

function userRow(overrides: Partial<UserRow> = {}): UserRow {
  return {
    id: 7,
    username: "alice",
    display_name: "Alice",
    status: "enabled",
    is_owner: false,
    note: "",
    last_used_at: 0,
    total_requests: 0,
    has_total_requests: true,
    cloud_download: 0,
    effective_cloud_download: false,
    auto_pin: false,
    source_bot_id: 0,
    ...overrides,
  };
}

function usersEnvelope(items: UserRow[]): ListEnvelope<UserRow> {
  return { items, page: 1, page_size: 20, total: items.length, total_pages: 1 };
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

/** 弹窗内提交按钮 */
const SAVE_BUTTON = /保\s*存\s*生\s*效/;

/** 等待异步配置回填完成：目的地行在表格中出现 */
async function pageReady(name = "mega-1") {
  return await screen.findByText(name);
}

/** 打开指定目的地的配置弹窗，返回弹窗中的名称输入框 */
async function openConfigModal(name = "mega-1") {
  await pageReady(name);
  fireEvent.click(screen.getByRole("button", { name: "配置" }));
  const dialog = await screen.findByRole("dialog");
  const nameInput = await within(dialog).findByPlaceholderText("如 mega-1");
  await waitFor(() => expect(nameInput).toHaveValue(name));
  return { dialog, nameInput };
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
    fetchUsersMock.mockReset().mockResolvedValue(usersEnvelope([userRow()]));
    setUserCloudDownloadMock
      .mockReset()
      .mockResolvedValue({ ok: true, cloud_download: 1, effective_cloud_download: true });
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("回填服务端配置：开关、目的地表格行、默认 Radio（无默认文案）及配置弹窗回填", async () => {
    renderPage();

    expect(await screen.findByRole("switch", { name: "云盘下载总开关" })).not.toBeChecked();
    await pageReady();

    // 表格行基础信息
    expect(screen.getByText("mega-1")).toBeInTheDocument();
    expect(screen.getByText("mega")).toBeInTheDocument();
    expect(screen.getByText("spore")).toBeInTheDocument();
    expect(screen.getByRole("switch", { name: "启用目的地 mega-1" })).toBeChecked();
    // 默认 Radio 选中且 cell 内不显示“默认”两个字的文字节点
    const defaultRadio = screen.getByRole("radio", { name: "默认目的地 mega-1" });
    expect(defaultRadio).toBeChecked();
    const row = defaultRadio.closest("tr");
    expect(row).toHaveClass("cloud-dest-row--highlight");

    // 打开配置弹窗核对字段回填
    const { dialog, nameInput } = await openConfigModal("mega-1");
    expect(nameInput).toHaveValue("mega-1");
    expect(within(dialog).getByRole("combobox")).toHaveValue("mega");
    expect(within(dialog).getByPlaceholderText("如 spore")).toHaveValue("spore");
    // options 对象转键值行编辑器
    expect(within(dialog).getByDisplayValue("user")).toBeInTheDocument();
    expect(within(dialog).getByDisplayValue("a@example.com")).toBeInTheDocument();
    expect(within(dialog).getByDisplayValue("********")).toBeInTheDocument();
  });

  it("rclone 不可用时展示警示条", async () => {
    fetchCloudDriveMock.mockResolvedValue(cloudView({ rclone_available: false }));

    renderPage();

    expect(
      await screen.findByText("未检测到 rclone 二进制，云盘上传与目的地测试暂不可用"),
    ).toBeInTheDocument();
  });

  it("无目的地时展示空状态引导，点击「添加目的地」打开新增弹窗", async () => {
    fetchCloudDriveMock.mockResolvedValue(cloudView({ destinations: [] }));

    renderPage();

    expect(await screen.findByText(/尚未配置目的地/)).toBeInTheDocument();
    const addButton = screen.getByRole("button", { name: /添\s*加\s*目\s*的\s*地/ });
    fireEvent.click(addButton);

    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("添加目的地")).toBeInTheDocument();
    const nameInput = await within(dialog).findByPlaceholderText("如 mega-1");
    expect(nameInput).toHaveValue("");
    expect(within(dialog).getByRole("switch")).toBeChecked();
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
    const { dialog } = await openConfigModal("new-dest");
    const typeInput = within(dialog).getByRole("combobox");
    fireEvent.change(typeInput, { target: { value: "mega" } });

    // mega → user/pass/2fa 三个空值行；pass 与 2fa 命中敏感键名规则
    expect(await within(dialog).findByDisplayValue("user")).toBeInTheDocument();
    expect(within(dialog).getByDisplayValue("pass")).toBeInTheDocument();
    expect(within(dialog).getByDisplayValue("2fa")).toBeInTheDocument();
    const passwordInputs = dialog.querySelectorAll('input[type="password"]');
    expect(passwordInputs.length).toBeGreaterThanOrEqual(2);
  });

  it("已有 options 的目的地切换类型不覆盖既有参数行", async () => {
    renderPage();
    const { dialog } = await openConfigModal("mega-1");
    fireEvent.change(within(dialog).getByRole("combobox"), { target: { value: "s3" } });

    await waitFor(() =>
      expect(within(dialog).getByDisplayValue("a@example.com")).toBeInTheDocument(),
    );
    // s3 建议键未注入（仍只有 user/pass 两行）
    expect(within(dialog).queryByDisplayValue("access_key_id")).not.toBeInTheDocument();
  });

  it("名称非法（大写/下划线）时阻断保存并展示规则文案", async () => {
    renderPage();
    const { dialog, nameInput } = await openConfigModal("mega-1");
    fireEvent.change(nameInput, { target: { value: "Bad_Name" } });

    fireEvent.click(within(dialog).getByRole("button", { name: SAVE_BUTTON }));

    expect(
      await within(dialog).findByText(
        "名称须为小写字母开头，仅含小写字母、数字或连字符，最长 32 字符（禁用下划线）。",
      ),
    ).toBeInTheDocument();
    expect(saveCloudDriveMock).not.toHaveBeenCalled();
  });

  it("列表内重名阻断保存并提示唯一性", async () => {
    renderPage();
    await pageReady();
    fireEvent.click(screen.getByRole("button", { name: /添\s*加\s*目\s*的\s*地/ }));
    const dialog = await screen.findByRole("dialog");
    const nameInput = await within(dialog).findByPlaceholderText("如 mega-1");
    fireEvent.change(nameInput, { target: { value: "mega-1" } });

    fireEvent.click(within(dialog).getByRole("button", { name: SAVE_BUTTON }));

    const error = await within(dialog).findByText("名称已存在：目的地名称在列表内必须唯一。");
    expect(error).toBeInTheDocument();
    expect(saveCloudDriveMock).not.toHaveBeenCalled();
  });

  it("保存提交整表单载荷（options 行转对象）并失效 cloud-drive 查询", async () => {
    const invalidateSpy = renderPage();
    const { dialog, nameInput } = await openConfigModal("mega-1");

    fireEvent.change(nameInput, { target: { value: "mega-2" } });
    fireEvent.click(within(dialog).getByRole("button", { name: SAVE_BUTTON }));

    await waitFor(() => expect(saveCloudDriveMock).toHaveBeenCalledTimes(1));
    expect(saveCloudDriveMock.mock.calls[0][0]).toEqual({
      enabled: false,
      default_destination: "mega-2",
      destinations: [
        {
          name: "mega-2",
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

  it("服务端校验失败（400 受控文案）只展示失败不提示成功且保持弹窗", async () => {
    saveCloudDriveMock.mockRejectedValue(new ApiError("目的地名称已存在", 400, "BAD_REQUEST"));
    renderPage();
    const { dialog } = await openConfigModal("mega-1");

    fireEvent.click(within(dialog).getByRole("button", { name: SAVE_BUTTON }));

    expect(await screen.findByText("目的地名称已存在")).toBeInTheDocument();
    expect(screen.queryByText("云盘下载配置已保存。")).not.toBeInTheDocument();
    expect(dialog).toBeInTheDocument();
  });

  it("表格行内「测试」按钮展示行内连通结果（成功与失败原因）", async () => {
    testCloudDriveMock.mockResolvedValueOnce({ ok: true, message: "连接成功：可访问该网盘。" });
    testCloudDriveMock.mockResolvedValueOnce({
      ok: false,
      message: "网盘账号验证失败，请联系管理员",
    });
    renderPage();
    await pageReady();

    fireEvent.click(screen.getByRole("button", { name: /测\s*试/ }));
    expect(await screen.findByText("连接成功：可访问该网盘。")).toBeInTheDocument();
    expect(testCloudDriveMock).toHaveBeenCalledWith("mega-1");

    fireEvent.click(screen.getByRole("button", { name: /测\s*试/ }));
    expect(await screen.findByText("网盘账号验证失败，请联系管理员")).toBeInTheDocument();
  });

  it("弹窗内支持保存前测试连通性，展示测试结果", async () => {
    testCloudDriveMock.mockResolvedValueOnce({ ok: true, message: "连接成功：测试正常。" });
    renderPage();
    const { dialog } = await openConfigModal("mega-1");

    const testBtn = within(dialog).getByRole("button", { name: "测试连通性" });
    fireEvent.click(testBtn);

    expect(await within(dialog).findByText("连接成功：测试正常。")).toBeInTheDocument();
    expect(testCloudDriveMock).toHaveBeenCalledWith({
      destination: {
        name: "mega-1",
        type: "mega",
        path_prefix: "spore",
        enabled: true,
        options: { user: "a@example.com", pass: "********" },
      },
    });
  });

  it("切换默认目的地 Radio 直接生效保存", async () => {
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
    const radios = await screen.findAllByRole("radio", { name: /默认目的地/ });
    await waitFor(() => expect(radios[0]).toBeChecked());
    fireEvent.click(radios[1]);

    await waitFor(() => expect(saveCloudDriveMock).toHaveBeenCalledTimes(1));
    const input = saveCloudDriveMock.mock.calls[0][0];
    expect(input.default_destination).toBe("s3-1");
    expect(input.destinations).toHaveLength(2);
  });

  it("行内切换启用状态直接生效保存", async () => {
    renderPage();
    await pageReady();
    const switchBtn = screen.getByRole("switch", { name: "启用目的地 mega-1" });
    expect(switchBtn).toBeChecked();

    fireEvent.click(switchBtn);

    await waitFor(() => expect(saveCloudDriveMock).toHaveBeenCalledTimes(1));
    expect(saveCloudDriveMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({
        destinations: [expect.objectContaining({ name: "mega-1", enabled: false })],
      }),
    );
  });

  it("全局总开关切换直接生效保存", async () => {
    renderPage();
    const globalSwitch = await screen.findByRole("switch", { name: "云盘下载总开关" });
    expect(globalSwitch).not.toBeChecked();

    fireEvent.click(globalSwitch);

    await waitFor(() => expect(saveCloudDriveMock).toHaveBeenCalledTimes(1));
    expect(saveCloudDriveMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({
        enabled: true,
      }),
    );
  });

  it("删除默认目的地需二次确认；确认后直接生效保存并清空默认选择", async () => {
    renderPage();
    await pageReady();

    fireEvent.click(screen.getByRole("button", { name: /删\s*除/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getAllByText(/确认删除目的地/).length).toBeGreaterThanOrEqual(1);

    // 确认删除
    fireEvent.click(within(dialog).getByRole("button", { name: "删 除" }));

    await waitFor(() => expect(saveCloudDriveMock).toHaveBeenCalledTimes(1));
    expect(saveCloudDriveMock.mock.calls[0][0]).toEqual(
      expect.objectContaining({
        default_destination: "",
        destinations: [],
      }),
    );
  });

  it("目的地删除在二次确认中取消则保留目的地", async () => {
    renderPage();
    await pageReady();

    fireEvent.click(screen.getByRole("button", { name: /删\s*除/ }));
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getAllByText(/确认删除目的地/).length).toBeGreaterThanOrEqual(1);

    fireEvent.click(within(dialog).getByRole("button", { name: "取 消" }));
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
    expect(saveCloudDriveMock).not.toHaveBeenCalled();
    expect(screen.getByText("mega-1")).toBeInTheDocument();
  });

  it("表格支持展开收起查看参数配置概览", async () => {
    renderPage();
    await pageReady();

    // 默认详情未展开
    expect(screen.queryByText(/参数配置（options）/)).not.toBeInTheDocument();

    // 点击自定义展开图标
    const expandBtn = screen.getByRole("button", { name: "展开目的地 mega-1 详情" });
    fireEvent.click(expandBtn);

    expect(await screen.findByText(/参数配置（options）/)).toBeInTheDocument();
    expect(screen.getByText("user")).toBeInTheDocument();
    expect(screen.getByText("a@example.com")).toBeInTheDocument();

    // 再次点击收起
    fireEvent.click(screen.getByRole("button", { name: "收起目的地 mega-1 详情" }));
    await waitFor(() => {
      expect(screen.queryByText(/参数配置（options）/)).not.toBeInTheDocument();
    });
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

  it("用户云盘配置：默认收起且不请求用户列表", async () => {
    renderPage();
    await pageReady();

    expect(screen.getByText("用户云盘配置")).toBeInTheDocument();
    expect(fetchUsersMock).not.toHaveBeenCalled();
    // 未展开时面板内容不渲染
    expect(screen.queryByText(/完整搜索与用户管理见/)).not.toBeInTheDocument();
  });

  it("用户云盘配置：展开后拉取并回显三态与生效提示", async () => {
    fetchUsersMock.mockResolvedValue(
      usersEnvelope([
        userRow({ id: 7, username: "alice", cloud_download: 1, effective_cloud_download: true }),
        userRow({ id: 8, username: "bob", cloud_download: 0, effective_cloud_download: false }),
        userRow({ id: 9, username: "carol", display_name: "", cloud_download: 2 }),
        userRow({
          id: 1,
          username: "",
          display_name: "Owner",
          is_owner: true,
          cloud_download: 0,
          effective_cloud_download: true,
        }),
      ]),
    );
    renderPage();
    await pageReady();

    fireEvent.click(screen.getByText("用户云盘配置"));

    expect(await screen.findByText(/@alice/)).toBeInTheDocument();
    expect(fetchUsersMock).toHaveBeenCalled();
    // 三态回显：跟随默认（bob 与 owner）、允许（alice）、拒绝（carol）
    expect(screen.getAllByTitle("跟随角色默认")).toHaveLength(2);
    expect(screen.getByTitle("允许")).toBeInTheDocument();
    expect(screen.getByTitle("拒绝")).toBeInTheDocument();
    // raw=0 的行展示角色默认的生效值
    expect(screen.getByText("生效：拒绝")).toBeInTheDocument();
    expect(screen.getByText("生效：允许")).toBeInTheDocument();
  });

  it("用户云盘配置：行内切换权限直接提交并提示成功", async () => {
    fetchUsersMock.mockResolvedValue(usersEnvelope([userRow({ id: 7, cloud_download: 1 })]));
    setUserCloudDownloadMock.mockResolvedValue({
      ok: true,
      cloud_download: 2,
      effective_cloud_download: false,
    });
    const invalidateSpy = renderPage();
    await pageReady();

    fireEvent.click(screen.getByText("用户云盘配置"));
    const selector = (await screen.findByRole("combobox", { name: "云盘下载权限 7" }))
      .closest(".ant-select")
      ?.querySelector(".ant-select-selector");
    expect(selector).not.toBeNull();
    fireEvent.mouseDown(selector as HTMLElement);
    // antd 的 role=option 命中可访问性副本（无事件处理），
    // 交互需点 .ant-select-item-option（title 为 label）
    const option = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('.ant-select-item-option[title="拒绝"]');
      expect(el).not.toBeNull();
      return el as HTMLElement;
    });
    fireEvent.click(option);

    await waitFor(() => expect(setUserCloudDownloadMock).toHaveBeenCalledWith(7, 2));
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ["users"] })),
    );
    expect(
      await screen.findByText("云盘下载权限已更新：该用户使用 /download 将被拒绝。"),
    ).toBeInTheDocument();
  });

  it("用户云盘配置：失败展示服务端受控文案且不提示成功", async () => {
    fetchUsersMock.mockResolvedValue(usersEnvelope([userRow({ id: 7, cloud_download: 1 })]));
    setUserCloudDownloadMock.mockRejectedValue(
      new ApiError("cloud_download 取值必须为 0（跟随默认）、1（允许）或 2（拒绝）。", 400, "BAD_REQUEST"),
    );
    renderPage();
    await pageReady();

    fireEvent.click(screen.getByText("用户云盘配置"));
    const selector = (await screen.findByRole("combobox", { name: "云盘下载权限 7" }))
      .closest(".ant-select")
      ?.querySelector(".ant-select-selector");
    expect(selector).not.toBeNull();
    fireEvent.mouseDown(selector as HTMLElement);
    // 同上：点击可见的 .ant-select-item-option 而非可访问性副本
    const option = await waitFor(() => {
      const el = document.querySelector<HTMLElement>('.ant-select-item-option[title="拒绝"]');
      expect(el).not.toBeNull();
      return el as HTMLElement;
    });
    fireEvent.click(option);

    expect(
      await screen.findByText(
        "cloud_download 取值必须为 0（跟随默认）、1（允许）或 2（拒绝）。",
      ),
    ).toBeInTheDocument();
    expect(screen.queryByText(/云盘下载权限已更新/)).not.toBeInTheDocument();
  });
});
