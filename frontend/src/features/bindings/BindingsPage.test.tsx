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
    bot_id: 0,
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
  it("渲染唯一 H1 页面标题「频道绑定」", async () => {
    mockFetch.mockResolvedValue({ items: [bindingRow()] });
    renderPage();

    expect(
      await screen.findByRole("heading", { level: 1, name: "频道绑定" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  function renderPageAt(initialEntry: string) {
    return render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <AntApp component={false}>
          <MemoryRouter initialEntries={[initialEntry]}>
            <BindingsPage />
          </MemoryRouter>
        </AntApp>
      </QueryClientProvider>,
    );
  }

  it("URL user_id 参数驱动筛选：查询带入参数且回填输入框", async () => {
    mockFetch.mockResolvedValue({ items: [] });
    renderPageAt("/channel-bindings?user_id=7");

    await waitFor(() => expect(mockFetch).toHaveBeenCalledWith({ user_id: "7" }));
    expect(await screen.findByPlaceholderText("按用户 ID 筛选")).toHaveValue("7");

    // 修改筛选并提交：URL 驱动查询更新
    fireEvent.change(screen.getByPlaceholderText("按用户 ID 筛选"), {
      target: { value: "9" },
    });
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));

    await waitFor(() => expect(mockFetch).toHaveBeenLastCalledWith({ user_id: "9" }));
  });

  it("重置筛选清除 URL user_id 并按无条件查询", async () => {
    mockFetch.mockResolvedValue({ items: [] });
    renderPageAt("/channel-bindings?user_id=7");

    await waitFor(() => expect(mockFetch).toHaveBeenCalledWith({ user_id: "7" }));

    fireEvent.click(await screen.findByRole("button", { name: "重 置" }));

    await waitFor(() => expect(mockFetch).toHaveBeenLastCalledWith({}));
    expect(screen.getByPlaceholderText("按用户 ID 筛选")).toHaveValue("");
  });

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

    // 行操作统一为 RowActions 链接按钮（link 按钮两个汉字间不插空格）
    fireEvent.click(await screen.findByRole("button", { name: "解绑" }));
    // 确认弹窗出现后点击确认（antd 中文两字按钮内含空格）
    const confirmBtn = await screen.findByRole("button", { name: "确 认" });
    fireEvent.click(confirmBtn);

    await waitFor(() => {
      expect(mockUnbind).toHaveBeenCalledWith(-1001234567890);
    });
  });

  it("解绑 pending 只让目标行按钮进入 loading", async () => {
    mockFetch.mockResolvedValue({
      items: [bindingRow(), bindingRow({ channel_id: -100999, title: "第二频道" })],
    });
    mockUnbind.mockReturnValue(new Promise(() => undefined) as never);
    renderPage();

    const unbindButtons = await screen.findAllByRole("button", { name: "解绑" });
    fireEvent.click(unbindButtons[0]);
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));

    await waitFor(() => expect(unbindButtons[0]).toHaveClass("ant-btn-loading"));
    expect(unbindButtons[1]).not.toHaveClass("ant-btn-loading");
  });

  it("绑定弹窗取消后默认重置草稿", async () => {
    mockFetch.mockResolvedValue({ items: [] });
    renderPage();

    fireEvent.click(screen.getByRole("button", { name: "绑定频道" }));
    fireEvent.change(await screen.findByLabelText(/所属用户/), { target: { value: "7" } });
    fireEvent.change(screen.getByLabelText(/频道标识/), { target: { value: "@mychan" } });

    fireEvent.click(screen.getByRole("button", { name: "取 消" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "绑定频道" }));
    expect(await screen.findByLabelText(/所属用户/)).toHaveValue("");
    expect(screen.getByLabelText(/频道标识/)).toHaveValue("");
    expect(mockBind).not.toHaveBeenCalled();
  });
});
