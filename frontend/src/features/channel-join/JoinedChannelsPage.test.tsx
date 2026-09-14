/**
 * 已加入频道页组件测试：进入自动加载、刷新后渲染列表与来源标签、
 * 创建者行禁用退出、客户端筛选与批量退出按钮禁用逻辑、
 * 频道名默认暗文与显示明文切换。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchJoinedChannels, type JoinedChannelRow } from "../../api/admin";
import { leaveJoinedChannels } from "../../api/mutations";
import { JoinedChannelsPage } from "./JoinedChannelsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return { ...actual, fetchJoinedChannels: vi.fn() };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return { ...actual, leaveJoinedChannels: vi.fn() };
});

const mockJoined = vi.mocked(fetchJoinedChannels);
const mockLeave = vi.mocked(leaveJoinedChannels);

function joinedRow(overrides: Partial<JoinedChannelRow> = {}): JoinedChannelRow {
  return {
    channel_id: 42,
    title: "私有频道",
    username: "",
    kind: "channel",
    source: "approved",
    joined_by: 100,
    joined_at: 1757030400000,
    creator: false,
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <JoinedChannelsPage />
      </AntApp>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  mockJoined.mockReset();
  mockLeave.mockReset();
});

describe("已加入频道页", () => {
  it("进入页面自动加载一次；频道名默认暗文，切换显示明文", async () => {
    mockJoined.mockResolvedValue({ items: [joinedRow()] });
    renderPage();
    // 频道名默认脱敏暗文（titleMask），明文经「显示明文」切换
    await waitFor(() => {
      expect(screen.getByText("***")).toBeInTheDocument();
    });
    expect(screen.queryByText("私有频道")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /显示明文/ }));
    await waitFor(() => {
      expect(screen.getByText("私有频道")).toBeInTheDocument();
    });
    expect(mockJoined).toHaveBeenCalledTimes(1);
  });

  it("点击刷新后加载列表并渲染来源标签，创建者行禁用退出", async () => {
    mockJoined.mockResolvedValue({
      items: [
        joinedRow(),
        joinedRow({ channel_id: 43, title: "外部拉的", source: "external" }),
        joinedRow({ channel_id: 44, title: "我建的", creator: true, source: "join_command" }),
      ],
    });
    renderPage();

    // 三行先以暗文渲染完成，再切明文断言行身份与来源标签
    await waitFor(() => {
      expect(screen.getAllByText("***").length).toBe(3);
    });
    fireEvent.click(screen.getByRole("button", { name: /显示明文/ }));
    await waitFor(() => {
      expect(screen.getByText("外部拉的")).toBeInTheDocument();
    });
    expect(screen.getByText("外部拉入")).toBeInTheDocument();
    expect(screen.getByText("审批通过")).toBeInTheDocument();
    expect(screen.getByText(/创建者（不可退出）/)).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /退 出/ }).length).toBeGreaterThanOrEqual(2);
  });

  it("客户端筛选：关键词过滤行", async () => {
    mockJoined.mockResolvedValue({
      items: [
        joinedRow(),
        joinedRow({ channel_id: 43, title: "外部拉的", source: "external" }),
      ],
    });
    renderPage();

    // 筛选断言依赖明文行身份：先切明文
    await waitFor(() => {
      expect(screen.getAllByText("***").length).toBe(2);
    });
    fireEvent.click(screen.getByRole("button", { name: /显示明文/ }));
    await waitFor(() => {
      expect(screen.getByText("外部拉的")).toBeInTheDocument();
    });
    // 关键词命中第二行标题 → 仅其可见
    const search = screen.getByPlaceholderText("频道标题或用户名");
    fireEvent.change(search, { target: { value: "外部" } });
    await waitFor(() => {
      expect(screen.queryByText("私有频道")).not.toBeInTheDocument();
      expect(screen.getByText("外部拉的")).toBeInTheDocument();
    });
  });

  it("未选择时批量退出按钮禁用", async () => {
    mockJoined.mockResolvedValue({ items: [joinedRow()] });
    renderPage();

    await screen.findByText("***");
    expect(screen.getByRole("button", { name: /批量退出/ })).toBeDisabled();
  });

  it("退出操作完成后自动刷新列表", async () => {
    mockJoined.mockResolvedValue({ items: [joinedRow(), joinedRow({ channel_id: 43, title: "另一个" })] });
    mockLeave.mockResolvedValue({
      ok: true,
      outcomes: [{ channel_id: 42, ok: true }],
    });
    renderPage();

    // 两行默认暗文，以数量锚定渲染完成
    expect((await screen.findAllByText("***")).length).toBe(2);
    expect(mockJoined).toHaveBeenCalledTimes(1);
    // 行内退出（Popconfirm 确认）
    fireEvent.click(screen.getAllByRole("button", { name: /退 出/ })[0]);
    // Popconfirm 弹层挂在 body 末尾：确认键取最后一个同名按钮
    const confirmBtn = (await screen.findAllByRole("button", { name: /退 出/ })).at(-1);
    fireEvent.click(confirmBtn!);
    await waitFor(() => {
      expect(mockLeave).toHaveBeenCalledWith([42]);
    });
    await waitFor(() => {
      expect(mockJoined.mock.calls.length).toBeGreaterThanOrEqual(2);
    });
  });

  it("加载失败展示重试", async () => {
    mockJoined.mockRejectedValue(new Error("boom"));
    renderPage();
    await waitFor(() => {
      expect(screen.getByTestId("page-error")).toBeInTheDocument();
    });
  });
});
