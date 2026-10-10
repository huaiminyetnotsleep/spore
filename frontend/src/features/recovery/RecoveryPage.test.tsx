import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import dayjs from "dayjs";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fetchBots } from "../../api/admin";
import { ApiError } from "../../api/client";
import { controlRecoveryJob, createRecoveryJob, fetchRecoveryItems, fetchRecoveryJob, fetchRecoveryJobs, previewRecovery, type RecoveryJob } from "../../api/recovery";
import { RecoveryPage, recoveryInput } from "./RecoveryPage";

vi.mock("../../api/admin", async () => ({ ...await vi.importActual<typeof import("../../api/admin")>("../../api/admin"), fetchBots: vi.fn() }));
vi.mock("../../api/recovery", async () => ({ ...await vi.importActual<typeof import("../../api/recovery")>("../../api/recovery"), previewRecovery: vi.fn(), createRecoveryJob: vi.fn(), fetchRecoveryJobs: vi.fn(), fetchRecoveryJob: vi.fn(), fetchRecoveryItems: vi.fn(), controlRecoveryJob: vi.fn() }));
const job: RecoveryJob = {
  id: 71, status: "paused", target_chat_id: -1001234567890, target_title: "固定目标", bot_id: 42,
  filter: { channel_key: "source", user_id: 123, since: 0, until: 0 }, created_at: 1700000000000, updated_at: 1700000001000,
  total: 2, pending: 0, processing: 0, succeeded: 0, failed: 1, unrecoverable: 0, uncertain: 1, skipped: 0, last_error: "",
};
function envelope<T>(items: T[], total = items.length) { return { items, page: 1, page_size: 20, total, total_pages: Math.ceil(total / 20) }; }
function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { ...render(<QueryClientProvider client={client}><AntApp component={false}><RecoveryPage /></AntApp></QueryClientProvider>), client };
}
async function preflight() {
  fireEvent.change(await screen.findByLabelText("目标频道 / 超级群组"), { target: { value: "@target" } });
  fireEvent.click(screen.getByRole("button", { name: "权限预检" }));
  await screen.findByRole("button", { name: "开始恢复" });
}
describe("历史恢复", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fetchBots).mockResolvedValue({ bots: [], max_bots: 3, need_apply: false });
    vi.mocked(fetchRecoveryJobs).mockResolvedValue(envelope([]));
    vi.mocked(fetchRecoveryJob).mockResolvedValue(job);
    vi.mocked(fetchRecoveryItems).mockResolvedValue(envelope([]));
    vi.mocked(previewRecovery).mockResolvedValue({ target_chat_id: job.target_chat_id, target_title: job.target_title, bot_id: 42, total: 2, with_cache: 1, without_cache: 1, cache_channels: [{ channel_id: -10012, readable: false, message: "机器人无读取权限" }], warnings: ["历史相册可能不完整"] });
    vi.mocked(createRecoveryJob).mockResolvedValue(job);
    vi.mocked(controlRecoveryJob).mockResolvedValue(job);
  });
  it("renders unique H1, empty jobs and permission-only warnings", async () => {
    renderPage();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.getByRole("heading", { level: 1, name: "历史恢复" })).toBeInTheDocument();
    expect(await screen.findByText("暂无恢复任务。")).toBeInTheDocument();
    await preflight();
    expect(screen.getByText("有缓存 / 无缓存")).toBeInTheDocument();
    expect(screen.getByText("1 / 1")).toBeInTheDocument();
    expect(screen.getByText("机器人无读取权限")).toBeInTheDocument();
    expect(screen.getByText("历史相册可能不完整")).toBeInTheDocument();
  });
  it("query errors stay scoped and can retry", async () => {
    vi.mocked(fetchRecoveryJobs).mockRejectedValueOnce(new ApiError("读取失败", 503));
    renderPage();
    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    expect(await screen.findByLabelText("目标频道 / 超级群组")).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));
    expect(await screen.findByText("暂无恢复任务。")).toBeInTheDocument();
  });
  it("form changes invalidate preview and require a new preflight", async () => {
    renderPage();
    await preflight();
    fireEvent.change(screen.getByLabelText("来源频道"), { target: { value: "-1001234567890" } });
    expect(screen.queryByRole("button", { name: "开始恢复" })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "权限预检" }));
    await screen.findByRole("button", { name: "开始恢复" });
    expect(previewRecovery).toHaveBeenLastCalledWith({ filter: { channel_key: "-1001234567890", user_id: 0, since: 0, until: 0 }, target: "@target", bot_id: 0 });
  });
  it("requires confirmation and creates the exact preview snapshot even if form changes", async () => {
    renderPage();
    await preflight();
    fireEvent.click(screen.getByRole("button", { name: "开始恢复" }));
    expect((await screen.findAllByText("确认开始历史恢复")).length).toBeGreaterThan(0);
    expect(createRecoveryJob).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("来源频道"), { target: { value: "changed-after-confirm" } });
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));
    await waitFor(() => expect(createRecoveryJob).toHaveBeenCalledWith(vi.mocked(previewRecovery).mock.calls[0][0]));
    expect(await screen.findByText("任务 #71 详情")).toBeInTheDocument();
    expect(fetchRecoveryItems).toHaveBeenCalledWith(71, 1, 20, undefined);
  });
  it("creation failure displays only controlled API text", async () => {
    vi.mocked(createRecoveryJob).mockRejectedValue(new ApiError("已有运行中的恢复任务。", 409));
    renderPage();
    await preflight();
    fireEvent.click(screen.getByRole("button", { name: "开始恢复" }));
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));
    expect(await screen.findByText("已有运行中的恢复任务。")).toBeInTheDocument();
    expect(screen.queryByText("任务 #71 详情")).not.toBeInTheDocument();
  });
  it("gates form until bot metadata arrives and disables unavailable bots", async () => {
    const base = { primary: false, online: true, conflict: false, paused: false, disabled: false, source: "file", restart_pending: false };
    vi.mocked(fetchBots).mockResolvedValue({ bots: [
      { ...base, bot_id: 42, name: "可用机器人" },
      { ...base, bot_id: 43, name: "已停用机器人", disabled: true },
      { ...base, bot_id: 44, name: "待重启机器人", restart_pending: true },
      { ...base, bot_id: 45, name: "离线机器人", online: false },
    ], max_bots: 3, need_apply: true });
    renderPage();
    expect(screen.queryByRole("button", { name: "权限预检" })).not.toBeInTheDocument();
    const botSelect = await screen.findByLabelText("执行机器人");
    fireEvent.mouseDown(botSelect);
    expect(await screen.findByRole("option", { name: "已停用机器人 (43) · 已停用" })).toHaveClass("ant-select-item-option-disabled");
    expect(screen.getByRole("option", { name: "待重启机器人 (44) · 等待重启" })).toHaveClass("ant-select-item-option-disabled");
    expect(screen.getByRole("option", { name: "离线机器人 (45) · 离线" })).toHaveClass("ant-select-item-option-disabled");
    fireEvent.click(screen.getByRole("option", { name: "可用机器人 (42)" }));
    await preflight();
    expect(previewRecovery).toHaveBeenLastCalledWith(expect.objectContaining({ bot_id: 42 }));
  });
  it("freezes the form during creation and prevents duplicate submissions", async () => {
    vi.mocked(createRecoveryJob).mockImplementation(() => new Promise(() => undefined));
    renderPage();
    await preflight();
    fireEvent.click(screen.getByRole("button", { name: "开始恢复" }));
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));
    await waitFor(() => expect(screen.getByLabelText("来源频道")).toBeDisabled());
    expect(screen.getByRole("button", { name: /开始恢复/, hidden: true })).toBeDisabled();
    expect(screen.getByRole("button", { name: "权限预检", hidden: true })).toBeDisabled();
    expect(createRecoveryJob).toHaveBeenCalledTimes(1);
  });
  it("creates Unix ms boundaries without adding a day to exclusive until", () => {
    const since = dayjs("2026-10-01T12:30:00");
    const until = dayjs("2026-10-02T13:40:00");
    expect(recoveryInput({ target: " @target ", bot_id: 42, since, until }).filter).toEqual({ channel_key: "", user_id: 0, since: since.valueOf(), until: until.valueOf() });
  });
  it.each([
    ["running", 1, ["暂停任务", "取消任务"], ["继续任务", "重试失败或不可恢复项"]],
    ["paused", 1, ["继续任务", "取消任务", "重试失败或不可恢复项"], ["暂停任务"]],
    ["completed", 1, ["重试失败或不可恢复项"], ["暂停任务", "继续任务", "取消任务"]],
    ["cancelled", 1, [], ["暂停任务", "继续任务", "取消任务", "重试失败或不可恢复项"]],
    ["paused", 0, ["继续任务", "取消任务"], ["重试失败或不可恢复项"]],
  ] as const)("controls match status %s and definite failure count %s", async (status, failed, shown, hidden) => {
    vi.mocked(fetchRecoveryJobs).mockResolvedValue(envelope([{ ...job, status, failed }]));
    vi.mocked(fetchRecoveryJob).mockResolvedValue({ ...job, status, failed });
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "任务 #71" }));
    await screen.findByText("固定机器人 ID");
    for (const name of shown) expect(screen.getByRole("button", { name })).toBeInTheDocument();
    for (const name of hidden) expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    expect(screen.getByText(/空的已知发送 ID 不代表确定未发送/)).toBeInTheDocument();
  });
  it("allows manual retry of unrecoverable items after permission repair but never uncertain-only results", async () => {
    const repairable = { ...job, failed: 0, unrecoverable: 1 };
    vi.mocked(fetchRecoveryJobs).mockResolvedValue(envelope([repairable]));
    vi.mocked(fetchRecoveryJob).mockResolvedValue(repairable);
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "任务 #71" }));
    fireEvent.click(await screen.findByRole("button", { name: "重试失败或不可恢复项" }));
    expect(await screen.findByText(/可先补齐来源权限.*不确定结果永不重试/)).toBeInTheDocument();
    expect(controlRecoveryJob).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));
    await waitFor(() => expect(controlRecoveryJob).toHaveBeenCalledWith(71, "retry"));
  });
  it.each([["继续任务", "resume"], ["重试失败或不可恢复项", "retry"]] as const)("%s requires administrator confirmation", async (label, action) => {
    vi.mocked(fetchRecoveryJobs).mockResolvedValue(envelope([job]));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "任务 #71" }));
    fireEvent.click(await screen.findByRole("button", { name: label }));
    expect((await screen.findAllByText(`确认${label}`)).length).toBeGreaterThan(0);
    expect(controlRecoveryJob).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "确 认" })).not.toHaveClass("ant-btn-dangerous");
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));
    await waitFor(() => expect(controlRecoveryJob).toHaveBeenCalledWith(71, action));
  });
  it.each([["暂停任务", "pause"], ["取消任务", "cancel"]] as const)("slow background polling does not block %s or pagination", async (label, action) => {
    const running = { ...job, status: "running" as const };
    vi.mocked(fetchRecoveryJobs).mockResolvedValue(envelope([running], 21));
    vi.mocked(fetchRecoveryJob).mockResolvedValue(running);
    const { client, container } = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "任务 #71" }));
    await screen.findByText("固定机器人 ID");
    await screen.findByText("暂无符合条件的恢复项目。");
    vi.mocked(fetchRecoveryJobs).mockImplementation(() => new Promise(() => undefined));
    vi.mocked(fetchRecoveryJob).mockImplementation(() => new Promise(() => undefined));
    vi.mocked(fetchRecoveryItems).mockImplementation(() => new Promise(() => undefined));
    await act(async () => { void client.refetchQueries({ queryKey: ["recovery"] }); });
    expect(await screen.findByText("正在刷新任务详情…")).toBeInTheDocument();
    expect(container.querySelector(".ant-spin-blur")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: label })).toBeEnabled();
    fireEvent.click(screen.getByTitle("2"));
    await waitFor(() => expect(fetchRecoveryJobs).toHaveBeenLastCalledWith(2, 20));
    fireEvent.click(screen.getByRole("button", { name: label }));
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));
    await waitFor(() => expect(controlRecoveryJob).toHaveBeenCalledWith(71, action));
  });
  it("paginates jobs and items independently, renders known output IDs and errors", async () => {
    vi.mocked(fetchRecoveryJobs).mockResolvedValue(envelope([job], 21));
    vi.mocked(fetchRecoveryItems).mockResolvedValue(envelope([{ id: 72, job_id: 71, channel_key: "source", message_id: 10, member_ids: [10, 11], cache_copies: [{ chat_id: -10012, message_ids: [30, 31] }], sent_ids: [501, 502], status: "uncertain", method: "cache", error_code: "SEND_UNCERTAIN", error_message: "部分发送，人工核对。", created_at: 0, updated_at: 0 }], 21));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "任务 #71" }));
    expect(await screen.findByText("501, 502")).toBeInTheDocument();
    expect(screen.getByText("-10012: 30, 31")).toBeInTheDocument();
    expect(screen.getByText("部分发送，人工核对。")).toBeInTheDocument();
    const pageTwos = screen.getAllByTitle("2");
    fireEvent.click(pageTwos[0]);
    await waitFor(() => expect(fetchRecoveryJobs).toHaveBeenLastCalledWith(2, 20));
    fireEvent.click(pageTwos[1]);
    await waitFor(() => expect(fetchRecoveryItems).toHaveBeenLastCalledWith(71, 2, 20, undefined));
  });
});
