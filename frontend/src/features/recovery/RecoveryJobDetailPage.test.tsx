import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { controlRecoveryJob, fetchRecoveryItems, fetchRecoveryJob, type RecoveryJob } from "../../api/recovery";
import { RecoveryJobDetailPage } from "./RecoveryJobDetailPage";
import { recoveryDocsUrl } from "./RecoveryPage";

vi.mock("../../api/recovery", async () => ({ ...await vi.importActual<typeof import("../../api/recovery")>("../../api/recovery"), fetchRecoveryJob: vi.fn(), fetchRecoveryItems: vi.fn(), controlRecoveryJob: vi.fn() }));
const job: RecoveryJob = {
  id: 71, status: "paused", target_chat_id: -1001234567890, target_title: "固定目标", bot_id: 42,
  filter: { channel_key: "source", user_id: 123, since: 0, until: 0 }, created_at: 1700000000000, updated_at: 1700000001000,
  total: 2, pending: 0, processing: 0, succeeded: 0, failed: 1, unrecoverable: 0, uncertain: 1, skipped: 0, last_error: "",
};
function envelope<T>(items: T[], total = items.length) { return { items, page: 1, page_size: 20, total, total_pages: Math.ceil(total / 20) }; }
function renderDetail(entry = "/recovery/jobs/71") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    ...render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[entry]}>
          <AntApp component={false}>
            <Routes>
              <Route path="/recovery/jobs/:id" element={<RecoveryJobDetailPage />} />
            </Routes>
          </AntApp>
        </MemoryRouter>
      </QueryClientProvider>
    ),
    client,
  };
}
describe("历史恢复详情页", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fetchRecoveryJob).mockResolvedValue(job);
    vi.mocked(fetchRecoveryItems).mockResolvedValue(envelope([]));
    vi.mocked(controlRecoveryJob).mockResolvedValue(job);
  });
  it("renders job snapshot fields and item coordinates", async () => {
    vi.mocked(fetchRecoveryItems).mockResolvedValue(envelope([{ id: 72, job_id: 71, channel_key: "source", message_id: 10, member_ids: [10, 11], cache_copies: [{ chat_id: -10012, message_ids: [30, 31] }], sent_ids: [501, 502], status: "uncertain", method: "cache", error_code: "SEND_UNCERTAIN", error_message: "部分发送，人工核对。", created_at: 0, updated_at: 0 }], 21));
    renderDetail();
    expect(await screen.findByText("固定机器人 ID")).toBeInTheDocument();
    const docs = screen.getByRole("link", { name: /操作文档/ });
    expect(docs).toHaveAttribute("href", recoveryDocsUrl);
    expect(docs).toHaveAttribute("target", "_blank");
    expect(screen.getByText("固定目标 (-1001234567890)")).toBeInTheDocument();
    expect(screen.getByText("501, 502")).toBeInTheDocument();
    expect(screen.getByText("-10012: 30, 31")).toBeInTheDocument();
    expect(screen.getByText("部分发送，人工核对。")).toBeInTheDocument();
    expect(screen.getByText(/空的已知发送 ID 不代表确定未发送/)).toBeInTheDocument();
    fireEvent.click(await screen.findByTitle("2"));
    await waitFor(() => expect(fetchRecoveryItems).toHaveBeenLastCalledWith(71, 2, 20, undefined));
  });
  it("rejects invalid task ids with the not-found fallback", async () => {
    renderDetail("/recovery/jobs/abc");
    expect(await screen.findByText("恢复任务不存在")).toBeInTheDocument();
    expect(fetchRecoveryJob).not.toHaveBeenCalled();
  });
  it.each([
    ["running", 1, ["暂停任务", "取消任务"], ["继续任务", "重试失败或不可恢复项"]],
    ["paused", 1, ["继续任务", "取消任务", "重试失败或不可恢复项"], ["暂停任务"]],
    ["completed", 1, ["重试失败或不可恢复项"], ["暂停任务", "继续任务", "取消任务"]],
    ["cancelled", 1, [], ["暂停任务", "继续任务", "取消任务", "重试失败或不可恢复项"]],
    ["paused", 0, ["继续任务", "取消任务"], ["重试失败或不可恢复项"]],
  ] as const)("controls match status %s and definite failure count %s", async (status, failed, shown, hidden) => {
    vi.mocked(fetchRecoveryJob).mockResolvedValue({ ...job, status, failed });
    renderDetail();
    await screen.findByText("固定机器人 ID");
    for (const name of shown) expect(screen.getByRole("button", { name })).toBeInTheDocument();
    for (const name of hidden) expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
  });
  it("allows manual retry of unrecoverable items after permission repair but never uncertain-only results", async () => {
    const repairable = { ...job, failed: 0, unrecoverable: 1 };
    vi.mocked(fetchRecoveryJob).mockResolvedValue(repairable);
    renderDetail();
    fireEvent.click(await screen.findByRole("button", { name: "重试失败或不可恢复项" }));
    expect(await screen.findByText(/可先补齐来源权限.*不确定结果永不重试/)).toBeInTheDocument();
    expect(controlRecoveryJob).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));
    await waitFor(() => expect(controlRecoveryJob).toHaveBeenCalledWith(71, "retry"));
  });
  it.each([["继续任务", "resume"], ["重试失败或不可恢复项", "retry"]] as const)("%s requires administrator confirmation", async (label, action) => {
    renderDetail();
    fireEvent.click(await screen.findByRole("button", { name: label }));
    expect((await screen.findAllByText(`确认${label}`)).length).toBeGreaterThan(0);
    expect(controlRecoveryJob).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "确 认" })).not.toHaveClass("ant-btn-dangerous");
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));
    await waitFor(() => expect(controlRecoveryJob).toHaveBeenCalledWith(71, action));
  });
  it.each([["暂停任务", "pause"], ["取消任务", "cancel"]] as const)("slow background polling does not block %s", async (label, action) => {
    const running = { ...job, status: "running" as const };
    vi.mocked(fetchRecoveryJob).mockResolvedValue(running);
    const { client, container } = renderDetail();
    await screen.findByText("固定机器人 ID");
    await screen.findByText("暂无符合条件的恢复项目。");
    vi.mocked(fetchRecoveryJob).mockImplementation(() => new Promise(() => undefined));
    vi.mocked(fetchRecoveryItems).mockImplementation(() => new Promise(() => undefined));
    await act(async () => { void client.refetchQueries({ queryKey: ["recovery"] }); });
    expect(await screen.findByText("正在刷新任务详情…")).toBeInTheDocument();
    expect(container.querySelector(".ant-spin-blur")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: label })).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: label }));
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));
    await waitFor(() => expect(controlRecoveryJob).toHaveBeenCalledWith(71, action));
  });
  it("filters items by status via the API parameter", async () => {
    renderDetail();
    const select = await screen.findByRole("combobox", { name: "逐项状态筛选" });
    fireEvent.mouseDown(select);
    fireEvent.click(await screen.findByRole("option", { name: "结果不确定" }));
    await waitFor(() => expect(fetchRecoveryItems).toHaveBeenLastCalledWith(71, 1, 20, "uncertain"));
  });
  it("stops polling when the job is not running and polls while it is", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(fetchRecoveryJob).mockResolvedValue({ ...job, status: "completed" });
      renderDetail();
      await act(async () => { await vi.advanceTimersByTimeAsync(50); });
      const initial = vi.mocked(fetchRecoveryJob).mock.calls.length;
      expect(initial).toBeGreaterThan(0);
      await act(async () => { await vi.advanceTimersByTimeAsync(12000); });
      expect(vi.mocked(fetchRecoveryJob).mock.calls.length).toBe(initial);
    } finally {
      vi.useRealTimers();
    }
  });
  it("keeps polling while the job is running", async () => {
    vi.useFakeTimers();
    try {
      vi.mocked(fetchRecoveryJob).mockResolvedValue({ ...job, status: "running" });
      renderDetail();
      await act(async () => { await vi.advanceTimersByTimeAsync(50); });
      const initial = vi.mocked(fetchRecoveryJob).mock.calls.length;
      await act(async () => { await vi.advanceTimersByTimeAsync(6000); });
      expect(vi.mocked(fetchRecoveryJob).mock.calls.length).toBeGreaterThan(initial);
    } finally {
      vi.useRealTimers();
    }
  });
});
