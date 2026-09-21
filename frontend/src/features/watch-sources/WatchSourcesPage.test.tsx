/**
 * 监听源管理页测试：渲染唯一 H1 标题「监听源管理」、添加输入框、
 * 监听源列表与操作行，以及确认不包含已被拆出的配置控件。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  fetchWatchSources,
  type WatchInviteRequestRow,
  type WatchSourceRow,
} from "../../api/admin";
import {
  addWatchSource,
  approveWatchInviteRequest,
  deleteWatchInviteRequest,
  deleteWatchSource,
  rejectWatchInviteRequest,
  retryWatchInviteRequest,
  reviewWatchSource,
  toggleWatchSource,
} from "../../api/mutations";
import { WatchSourcesPage } from "./WatchSourcesPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchWatchSources: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    addWatchSource: vi.fn(),
    approveWatchInviteRequest: vi.fn(),
    deleteWatchInviteRequest: vi.fn(),
    deleteWatchSource: vi.fn(),
    rejectWatchInviteRequest: vi.fn(),
    retryWatchInviteRequest: vi.fn(),
    reviewWatchSource: vi.fn(),
    toggleWatchSource: vi.fn(),
  };
});

const mockFetchSources = vi.mocked(fetchWatchSources);
const mockAdd = vi.mocked(addWatchSource);
const mockApproveInvite = vi.mocked(approveWatchInviteRequest);
const mockDeleteInvite = vi.mocked(deleteWatchInviteRequest);
const mockRejectInvite = vi.mocked(rejectWatchInviteRequest);
const mockRetryInvite = vi.mocked(retryWatchInviteRequest);
const mockReview = vi.mocked(reviewWatchSource);
const mockToggle = vi.mocked(toggleWatchSource);
const mockDeleteSource = vi.mocked(deleteWatchSource);

function sampleSource(overrides: Partial<WatchSourceRow> = {}): WatchSourceRow {
  return {
    channel_id: -1001234567890,
    title: "测试频道",
    username: "test_channel",
    kind: "channel",
    status: "approved",
    enabled: true,
    added_by: 0,
    user_username: "",
    user_display_name: "",
    bot_id: 999,
    bot_username: "test_bot",
    reviewed_by: "",
    created_at: 1757000000000,
    updated_at: 1757000000000,
    prewarm_count: 5,
    prewarm_last_at: 1757030400000,
    ...overrides,
  };
}

function sampleInviteRequest(
  overrides: Partial<WatchInviteRequestRow> = {},
): WatchInviteRequestRow {
  return {
    id: 7,
    user_id: 42,
    masked_hash: "+AbCd…5678",
    channel_id: 0,
    channel_title: "",
    participants: 0,
    status: "pending",
    enabled: true,
    bot_id: 999,
    bot_username: "test_bot",
    reviewed_by: "",
    note: "",
    created_at: 1757000000000,
    updated_at: 1757000000000,
    user_username: "applicant",
    user_display_name: "申请用户",
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <MemoryRouter>
          <WatchSourcesPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

describe("监听源管理页", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockFetchSources.mockReset();
  });

  it("渲染唯一 H1 页面标题「监听源管理」并提供添加输入框", async () => {
    mockFetchSources.mockResolvedValueOnce({ items: [] });
    renderPage();

    expect(screen.getByRole("heading", { level: 1, name: "监听源管理" })).toBeInTheDocument();
    expect(screen.getByText("添加监听源")).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/@用户名 \/ https:\/\/t.me\/链接/)).toBeInTheDocument();

    const addBtn = screen.getByRole("button", { name: "验证并添加" });
    expect(addBtn).toBeDisabled();

    // 确认配置项已不在该页面
    expect(screen.queryByLabelText("用户自助申请开关")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("监听源总数上限")).not.toBeInTheDocument();
  });

  it("输入目标后点击「验证并添加」触发接口并在成功后清空输入框", async () => {
    mockFetchSources.mockResolvedValue({ items: [] });
    mockAdd.mockResolvedValueOnce({
      ok: true,
      source: sampleSource({ title: "新频道", channel_id: -100999 }),
    });

    renderPage();
    const input = screen.getByPlaceholderText(/@用户名 \/ https:\/\/t.me\/链接/);
    fireEvent.change(input, { target: { value: "https://t.me/new_channel" } });

    const addBtn = screen.getByRole("button", { name: "验证并添加" });
    expect(addBtn).toBeEnabled();

    fireEvent.click(addBtn);

    await waitFor(() => {
      expect(mockAdd).toHaveBeenCalledWith("https://t.me/new_channel");
    });
    await waitFor(() => {
      expect((input as HTMLInputElement).value).toBe("");
    });
  });

  it("展示邀请申请的六种状态与权限帮助文案", async () => {
    mockFetchSources.mockResolvedValueOnce({
      items: [],
      invite_requests: [
        sampleInviteRequest({ id: 1, status: "pending" }),
        sampleInviteRequest({ id: 2, status: "waiting_telegram" }),
        sampleInviteRequest({ id: 3, status: "waiting_bot" }),
        sampleInviteRequest({ id: 4, status: "approved" }),
        sampleInviteRequest({ id: 5, status: "rejected" }),
        sampleInviteRequest({ id: 6, status: "failed" }),
      ],
    });

    renderPage();

    expect(await screen.findByText("待审批")).toBeInTheDocument();
    expect(screen.getByText("等待读取账号加入")).toBeInTheDocument();
    expect(screen.getByText("等待 Bot 管理员权限")).toBeInTheDocument();
    expect(screen.getByText("已通过")).toBeInTheDocument();
    expect(screen.getByText("已拒绝")).toBeInTheDocument();
    expect(screen.getByText("处理失败")).toBeInTheDocument();
    expect(screen.getByText(/私有邀请链接只会让读取账号加入目标/)).toBeInTheDocument();
    expect(screen.getByText(/Bot.*仍须由管理员在 Telegram 中人工设为管理员/)).toBeInTheDocument();
  });

  it("按邀请申请状态提供审批、拒绝、重试和删除操作", async () => {
    mockFetchSources.mockResolvedValue({
      items: [],
      invite_requests: [
        sampleInviteRequest({ id: 11, status: "pending" }),
        sampleInviteRequest({ id: 12, status: "waiting_telegram" }),
        sampleInviteRequest({ id: 13, status: "approved" }),
      ],
    });
    mockApproveInvite.mockResolvedValueOnce({ ok: true });
    mockRetryInvite.mockResolvedValueOnce({ ok: true });
    mockRejectInvite.mockResolvedValueOnce({ ok: true });
    mockDeleteInvite.mockResolvedValueOnce({ ok: true });

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: /审\s*批/ }));
    await waitFor(() => expect(mockApproveInvite).toHaveBeenCalledWith(11));

    fireEvent.click(screen.getByRole("button", { name: "重试" }));
    await waitFor(() => expect(mockRetryInvite).toHaveBeenCalledWith(12));

    fireEvent.click(screen.getAllByRole("button", { name: "拒绝" })[0]);
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));
    await waitFor(() => expect(mockRejectInvite).toHaveBeenCalledWith(11));

    fireEvent.click(screen.getByRole("button", { name: "删除" }));
    fireEvent.click(await screen.findByRole("button", { name: "删 除" }));
    await waitFor(() => expect(mockDeleteInvite).toHaveBeenCalledWith(13));
  });

  it("展示监听源列表数据与操作按钮", async () => {
    mockFetchSources.mockResolvedValue({
      items: [
        sampleSource({
          channel_id: -100111,
          title: "待审批源",
          status: "pending",
          enabled: false,
          added_by: 42,
          user_username: "applicant",
        }),
        sampleSource({
          channel_id: -100222,
          title: "生效中源",
          status: "approved",
          enabled: true,
        }),
      ],
    });

    renderPage();

    await waitFor(() => {
      expect(screen.getByText("待审批源")).toBeInTheDocument();
      expect(screen.getByText("生效中源")).toBeInTheDocument();
    });

    expect(screen.getByText("待审批")).toBeInTheDocument();
    expect(screen.getByText("生效中")).toBeInTheDocument();

    // 待审批源应有「同意」和「拒绝」按钮
    const approveBtn = screen.getByRole("button", { name: "同意" });
    expect(approveBtn).toBeInTheDocument();

    mockReview.mockResolvedValueOnce({
      ok: true,
      source: sampleSource({ channel_id: -100111, status: "approved" }),
    });
    fireEvent.click(approveBtn);
    await waitFor(() => {
      expect(mockReview).toHaveBeenCalledWith(-100111, true);
    });

    // 生效中源应有「暂停」按钮
    const pauseBtn = screen.getByRole("button", { name: "暂停" });
    expect(pauseBtn).toBeInTheDocument();

    mockToggle.mockResolvedValueOnce({
      ok: true,
      source: sampleSource({ channel_id: -100222, enabled: false }),
    });
    fireEvent.click(pauseBtn);
    await waitFor(() => {
      expect(mockToggle).toHaveBeenCalledWith(-100222, false);
    });
  });

  it("邀请申请支持状态与关键词查询（草稿态 + 查询按钮生效）", async () => {
    mockFetchSources.mockResolvedValue({
      items: [],
      invite_requests: [
        sampleInviteRequest({ id: 21, status: "pending", channel_title: "待审频道" }),
        sampleInviteRequest({ id: 22, status: "waiting_bot", channel_title: "私有群" }),
      ],
    });

    renderPage();
    await screen.findByText("待审频道");

    const inviteCard = screen.getByText(/邀请申请（/).closest(".ant-card");
    expect(inviteCard).not.toBeNull();
    const scope = within(inviteCard as HTMLElement);

    // 关键词（草稿态输入不立即生效，点「查 询」才过滤）
    fireEvent.change(scope.getByLabelText("邀请关键词筛选"), {
      target: { value: "私有群" },
    });
    expect(screen.getByText("待审频道")).toBeInTheDocument();
    fireEvent.click(scope.getByRole("button", { name: "查 询" }));
    await waitFor(() => {
      expect(screen.queryByText("待审频道")).not.toBeInTheDocument();
    });
    expect(screen.getByText("私有群")).toBeInTheDocument();

    // 重置恢复全部
    fireEvent.click(scope.getByRole("button", { name: "重 置" }));
    await waitFor(() => {
      expect(screen.getByText("待审频道")).toBeInTheDocument();
    });
  });

  it("监听源列表支持状态与关键词查询", async () => {
    mockFetchSources.mockResolvedValue({
      items: [
        sampleSource({ channel_id: -100111, title: "待审批源", status: "pending" }),
        sampleSource({ channel_id: -100222, title: "生效中源", status: "approved" }),
      ],
    });

    renderPage();
    await screen.findByText("待审批源");

    const sourcesCard = screen.getByText(/监听源列表（/).closest(".ant-card");
    expect(sourcesCard).not.toBeNull();
    const scope = within(sourcesCard as HTMLElement);

    fireEvent.change(scope.getByLabelText("监听源关键词筛选"), {
      target: { value: "生效中" },
    });
    fireEvent.click(scope.getByRole("button", { name: "查 询" }));
    await waitFor(() => {
      expect(screen.queryByText("待审批源")).not.toBeInTheDocument();
    });
    expect(screen.getByText("生效中源")).toBeInTheDocument();

    fireEvent.click(scope.getByRole("button", { name: "重 置" }));
    await waitFor(() => {
      expect(screen.getByText("待审批源")).toBeInTheDocument();
    });
  });

  it("监听源列表支持批量同意与批量删除", async () => {
    mockFetchSources.mockResolvedValue({
      items: [
        sampleSource({ channel_id: -100111, title: "待审批源", status: "pending" }),
        sampleSource({ channel_id: -100222, title: "生效中源", status: "approved" }),
      ],
    });
    mockReview.mockResolvedValue({ ok: true, source: sampleSource({ status: "approved" }) });
    mockDeleteSource.mockResolvedValue({ ok: true });

    renderPage();
    await screen.findByText("待审批源");

    const sourcesCard = screen.getByText(/监听源列表（/).closest(".ant-card");

    // 表头全选两行，出现批量操作条
    const allBoxes = (sourcesCard as HTMLElement).querySelectorAll<HTMLInputElement>(
      ".ant-checkbox-input",
    );
    expect(allBoxes.length).toBeGreaterThanOrEqual(3);
    fireEvent.click(allBoxes[0]);

    fireEvent.click(await screen.findByRole("button", { name: "批量同意（1）" }));
    await waitFor(() => {
      expect(mockReview).toHaveBeenCalledWith(-100111, true);
    });
    // 批量同意成功后清空选择，重新全选再批量删除
    await waitFor(() => {
      expect(screen.queryByRole("button", { name: "批量删除" })).not.toBeInTheDocument();
    });
    const allBoxesAgain = (sourcesCard as HTMLElement).querySelectorAll<HTMLInputElement>(
      ".ant-checkbox-input",
    );
    fireEvent.click(allBoxesAgain[0]);

    fireEvent.click(await screen.findByRole("button", { name: "批量删除" }));
    fireEvent.click(await screen.findByRole("button", { name: "删 除" }));
    await waitFor(() => {
      const calls = mockDeleteSource.mock.calls.map(([id]) => id);
      expect(calls).toEqual(expect.arrayContaining([-100111, -100222]));
    });
  });
});
