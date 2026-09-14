/**
 * 频道绑定管理页组件测试：列表渲染、绑定弹窗成功流、解绑确认流与加载失败重试。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchChannelBindings } from "../../api/admin";
import { unbindChannel, bindChannel, type UnbindChannelResult } from "../../api/mutations";
import type { ChannelBindingRow } from "../../api/admin";
import { BindingsPage } from "./BindingsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return { ...actual, fetchChannelBindings: vi.fn() };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return { ...actual, bindChannel: vi.fn(), unbindChannel: vi.fn() };
});

const mockFetch = vi.mocked(fetchChannelBindings);
const mockBind = vi.mocked(bindChannel);
const mockUnbind = vi.mocked(unbindChannel);

function bindingRow(overrides: Partial<ChannelBindingRow> = {}): ChannelBindingRow {
  return {
    channel_id: -1001234567890,
    user_id: 7,
    username: "mychan",
    title: "我的频道",
    bound_via: "bot",
    created_at: 1757030400000,
    user_username: "alice",
    user_display_name: "Alice",
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <MemoryRouter initialEntries={["/channel-bindings"]}>
          <BindingsPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mockBind.mockReset();
  mockUnbind.mockReset();
});

describe("频道绑定管理页", () => {
  it("渲染绑定列表与所属用户", async () => {
    mockFetch.mockResolvedValue({ items: [bindingRow()] });
    renderPage();

    await waitFor(() => {
      expect(screen.getByText("我的频道")).toBeInTheDocument();
    });
    // 归属用户：显示名（@用户名）；频道用户名与来源标签
    expect(screen.getByText(/Alice（@alice）/)).toBeInTheDocument();
    expect(screen.getByText(/@mychan/)).toBeInTheDocument();
    expect(screen.getByText("Bot 指令")).toBeInTheDocument();
  });

  it("空列表展示占位文案", async () => {
    mockFetch.mockResolvedValue({ items: [] });
    renderPage();
    await waitFor(() => {
      expect(screen.getByText("还没有任何频道绑定。")).toBeInTheDocument();
    });
  });

  it("加载失败展示重试", async () => {
    mockFetch.mockRejectedValue(new Error("boom"));
    renderPage();
    await waitFor(() => {
      expect(screen.getByTestId("page-error")).toBeInTheDocument();
    });
  });

  it("绑定弹窗成功后关闭并刷新列表", async () => {
    mockFetch.mockResolvedValue({ items: [] });
    mockBind.mockResolvedValue({
      ok: true,
      binding: {
        channel_id: -1001,
        user_id: 7,
        username: "mychan",
        title: "我的频道",
        bound_via: "web",
        created_at: 1757030400000,
      },
    });
    renderPage();

    fireEvent.click(screen.getByRole("button", { name: "绑定频道" }));
    const userIdInput = await screen.findByLabelText(/所属用户/);
    const targetInput = screen.getByLabelText(/频道标识/);
    fireEvent.change(userIdInput, { target: { value: "7" } });
    fireEvent.change(targetInput, { target: { value: "@mychan" } });

    fireEvent.click(screen.getByRole("button", { name: "绑 定" }));
    await waitFor(() => {
      expect(mockBind).toHaveBeenCalledWith({ user_id: 7, target: "@mychan" });
    });
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  it("解绑需确认并调用删除端点", async () => {
    mockFetch.mockResolvedValue({ items: [bindingRow()] });
    mockUnbind.mockResolvedValue({ ok: true, binding: { channel_id: -1001234567890, user_id: 7, title: "我的频道" } } as UnbindChannelResult);
    renderPage();

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "解 绑" })).toBeInTheDocument();
    });
    fireEvent.click(screen.getByRole("button", { name: "解 绑" }));
    // 确认弹窗出现后点击确认（antd 中文两字按钮内含空格）
    const confirmBtn = await screen.findByRole("button", { name: "确 认" });
    fireEvent.click(confirmBtn);

    await waitFor(() => {
      expect(mockUnbind).toHaveBeenCalledWith(-1001234567890);
    });
  });
});
