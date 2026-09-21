/**
 * 监听源管理页测试：渲染唯一 H1 标题「监听源管理」、添加输入框、
 * 监听源列表与操作行，以及确认不包含已被拆出的配置控件。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchWatchSources, type WatchSourceRow } from "../../api/admin";
import {
  addWatchSource,
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
    reviewWatchSource: vi.fn(),
    toggleWatchSource: vi.fn(),
  };
});

const mockFetchSources = vi.mocked(fetchWatchSources);
const mockAdd = vi.mocked(addWatchSource);
const mockReview = vi.mocked(reviewWatchSource);
const mockToggle = vi.mocked(toggleWatchSource);

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
    mockFetchSources.mockResolvedValueOnce({ items: [] });
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

  it("展示监听源列表数据与操作按钮", async () => {
    mockFetchSources.mockResolvedValueOnce({
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
});
