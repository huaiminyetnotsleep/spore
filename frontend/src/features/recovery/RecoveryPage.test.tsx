import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import dayjs from "dayjs";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fetchBots, fetchChannels, fetchChannelBindings, fetchUsers, fetchWatchSources, type WatchSourceRow } from "../../api/admin";
import { ApiError } from "../../api/client";
import { controlRecoveryJob, createRecoveryJob, fetchRecoveryItems, fetchRecoveryJob, fetchRecoveryJobs, previewRecovery, type RecoveryJob } from "../../api/recovery";
import { RecoveryJobDetailPage } from "./RecoveryJobDetailPage";
import { buildRecoverySourceOptions, buildRecoveryTargetOptions, buildRecoveryUserOptions, RecoveryPage, recoveryDocsUrl, recoveryInput, recoveryOptionFilter } from "./RecoveryPage";

vi.mock("../../api/admin", async () => ({ ...await vi.importActual<typeof import("../../api/admin")>("../../api/admin"), fetchBots: vi.fn(), fetchChannels: vi.fn(), fetchChannelBindings: vi.fn(), fetchUsers: vi.fn(), fetchWatchSources: vi.fn() }));
vi.mock("../../api/recovery", async () => ({ ...await vi.importActual<typeof import("../../api/recovery")>("../../api/recovery"), previewRecovery: vi.fn(), createRecoveryJob: vi.fn(), fetchRecoveryJobs: vi.fn(), fetchRecoveryJob: vi.fn(), fetchRecoveryItems: vi.fn(), controlRecoveryJob: vi.fn() }));
const job: RecoveryJob = {
  id: 71, status: "paused", target_chat_id: -1001234567890, target_title: "固定目标", bot_id: 42,
  filter: { channel_key: "source", user_id: 123, since: 0, until: 0 }, created_at: 1700000000000, updated_at: 1700000001000,
  total: 2, pending: 0, processing: 0, succeeded: 0, failed: 1, unrecoverable: 0, uncertain: 1, skipped: 0, last_error: "",
};
function envelope<T>(items: T[], total = items.length) { return { items, page: 1, page_size: 20, total, total_pages: Math.ceil(total / 20) }; }
function renderPage(entry = "/recovery") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    ...render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[entry]}>
          <AntApp component={false}>
            <Routes>
              <Route path="/recovery" element={<RecoveryPage />} />
              <Route path="/recovery/jobs/:id" element={<RecoveryJobDetailPage />} />
            </Routes>
          </AntApp>
        </MemoryRouter>
      </QueryClientProvider>
    ),
    client,
  };
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
    vi.mocked(fetchChannels).mockResolvedValue(envelope([{ key: "legacy_source", total: 3, succeeded: 2, failed: 1, success_rate: 0.67, last_requested_at: 0 }]));
    vi.mocked(fetchChannelBindings).mockResolvedValue({ items: [{ channel_id: -1005550001, user_id: 7, username: "bound_target", title: "用户的绑定频道", bound_via: "web", bot_id: 0, status: "active", created_at: 0, user_username: "u7", user_display_name: "用户七" }, { channel_id: -1005550002, user_id: 8, username: "", title: "已解绑频道", bound_via: "bot", bot_id: 0, status: "unbound", created_at: 0, user_username: "", user_display_name: "" }] });
    const watchSourceFixture = (over: Partial<WatchSourceRow>): WatchSourceRow => ({ channel_id: 0, kind: "", username: "", title: "", status: "approved", enabled: true, added_by: 0, bot_id: 0, bot_username: "", reviewed_by: "", created_at: 0, updated_at: 0, user_username: "", user_display_name: "", prewarm_count: 0, prewarm_last_at: 0, ...over });
    vi.mocked(fetchWatchSources).mockResolvedValue({ items: [watchSourceFixture({ channel_id: -1004440001, kind: "supergroup", username: "news_source", title: "新闻源", status: "approved" }), watchSourceFixture({ channel_id: -1004440002, kind: "channel", title: "待审批源", status: "pending" })] });
    vi.mocked(fetchUsers).mockResolvedValue(envelope([{ id: 7, username: "user_seven", display_name: "用户七", status: "enabled", is_owner: false, note: "", last_used_at: 0, total_requests: 0, has_total_requests: false, cloud_download: 0, effective_cloud_download: false, auto_pin: false, source_bot_id: 0 }]));
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
    const docs = screen.getByRole("link", { name: /操作文档/ });
    expect(docs).toHaveAttribute("href", recoveryDocsUrl);
    expect(docs).toHaveAttribute("target", "_blank");
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
  it("enumerates source, target and user options from bindings, watch sources, history and users", () => {
    const sources = buildRecoverySourceOptions({
      watchSources: [{ channel_id: -1004440001, kind: "supergroup", username: "news_source", title: "新闻源", status: "approved" }, { channel_id: -1004440002, kind: "channel", username: "", title: "待审批源", status: "pending" }],
      bindings: [{ channel_id: -1005550001, username: "bound_target", title: "用户的绑定频道", status: "active" }, { channel_id: -1005550002, username: "", title: "已解绑频道", status: "unbound" }],
      channels: [{ key: "legacy_source" }],
    });
    expect(sources).toEqual([
      { value: "-1004440001", label: "新闻源 · 超级群组 · 监听源" },
      { value: "-1005550001", label: "用户的绑定频道 · 绑定频道" },
      { value: "legacy_source", label: "legacy_source · 提取历史来源" },
    ]);
    expect(buildRecoveryTargetOptions([{ channel_id: -1005550001, username: "bound_target", title: "用户的绑定频道", status: "active" }, { channel_id: -1005550002, username: "", title: "已解绑频道", status: "unbound" }])).toEqual([{ value: "-1005550001", label: "用户的绑定频道（@bound_target）" }]);
    expect(buildRecoveryUserOptions([{ id: 7, username: "user_seven", display_name: "用户七" }, { id: 9, username: "", display_name: "" }])).toEqual([{ value: "7", label: "用户七（@user_seven） · 7" }, { value: "9", label: "9 · 9" }]);
    expect(recoveryOptionFilter("新闻", { value: "-1004440001", label: "新闻源 · 超级群组 · 监听源" })).toBe(true);
    expect(recoveryOptionFilter("news_source", { value: "-1004440001", label: "新闻源 · 超级群组 · 监听源" })).toBe(false);
    expect(recoveryOptionFilter("4440001", { value: "-1004440001", label: "新闻源" })).toBe(true);
    expect(recoveryOptionFilter("999", { value: "-1004440001", label: "新闻源" })).toBe(false);
  });
  it("converts free-typed source, user id and target into the preview input", async () => {
    renderPage();
    fireEvent.change(await screen.findByLabelText("来源频道"), { target: { value: " -100123 " } });
    fireEvent.change(screen.getByLabelText("用户 ID"), { target: { value: " 42 " } });
    fireEvent.change(screen.getByLabelText("目标频道 / 超级群组"), { target: { value: " @target " } });
    fireEvent.click(screen.getByRole("button", { name: "权限预检" }));
    await waitFor(() => expect(previewRecovery).toHaveBeenCalledWith({ filter: { channel_key: "-100123", user_id: 42, since: 0, until: 0 }, target: "@target", bot_id: 0 }));
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
  it("navigates to the dedicated detail page when clicking a task", async () => {
    vi.mocked(fetchRecoveryJobs).mockResolvedValue(envelope([job]));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "任务 #71" }));
    expect(await screen.findByText("任务 #71 详情")).toBeInTheDocument();
    expect(fetchRecoveryJob).toHaveBeenCalledWith(71);
  });
  it("paginates the job list independently", async () => {
    vi.mocked(fetchRecoveryJobs).mockResolvedValue(envelope([job], 21));
    renderPage();
    fireEvent.click(await screen.findByTitle("2"));
    await waitFor(() => expect(fetchRecoveryJobs).toHaveBeenLastCalledWith(2, 20));
  });
  it("stops polling the list when no job is running", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(fetchRecoveryJobs).mockResolvedValue(envelope([{ ...job, status: "completed" }]));
      renderPage();
      await act(async () => { await vi.advanceTimersByTimeAsync(50); });
      const initial = vi.mocked(fetchRecoveryJobs).mock.calls.length;
      expect(initial).toBeGreaterThan(0);
      await act(async () => { await vi.advanceTimersByTimeAsync(12000); });
      expect(vi.mocked(fetchRecoveryJobs).mock.calls.length).toBe(initial);
    } finally {
      vi.useRealTimers();
    }
  });
  it("keeps polling while a job is running", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(fetchRecoveryJobs).mockResolvedValue(envelope([{ ...job, status: "running" }]));
      renderPage();
      await act(async () => { await vi.advanceTimersByTimeAsync(50); });
      const initial = vi.mocked(fetchRecoveryJobs).mock.calls.length;
      await act(async () => { await vi.advanceTimersByTimeAsync(6000); });
      expect(vi.mocked(fetchRecoveryJobs).mock.calls.length).toBeGreaterThan(initial);
    } finally {
      vi.useRealTimers();
    }
  });
});
