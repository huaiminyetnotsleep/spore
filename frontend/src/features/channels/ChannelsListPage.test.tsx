/**
 * 频道统计列表页组件测试：聚合行渲染、空态、错误态（含重试）与
 * 操作列删除（确认、deleted 行数提示与服务端拒绝文案）。
 * API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import { fetchChannels, type ChannelRow, type ListEnvelope } from "../../api/admin";
import { deleteChannelRequests } from "../../api/mutations";
import { ChannelsListPage } from "./ChannelsListPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchChannels: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    deleteChannelRequests: vi.fn(),
  };
});

const fetchChannelsMock = vi.mocked(fetchChannels);
const deleteChannelRequestsMock = vi.mocked(deleteChannelRequests);

function envelope(items: ChannelRow[]): ListEnvelope<ChannelRow> {
  return { items, page: 1, page_size: 20, total: items.length, total_pages: 1 };
}

function channelRow(overrides: Partial<ChannelRow>): ChannelRow {
  return {
    key: "example",
    total: 10,
    succeeded: 8,
    failed: 2,
    success_rate: 0.8,
    last_requested_at: 1756598400000,
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidateSpy = vi.spyOn(client, "invalidateQueries");
  render(
    <QueryClientProvider client={client}>
      {/* antd App 上下文：message/modal 实例来自它（生产由 App.tsx 挂载） */}
      <AntApp component={false}>
        <MemoryRouter initialEntries={["/channels"]}>
          <ChannelsListPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
  return invalidateSpy;
}

describe("频道统计列表页", () => {
  beforeEach(() => {
    fetchChannelsMock.mockReset();
    deleteChannelRequestsMock.mockReset();
  });

  it("渲染聚合行与成功率", async () => {
    fetchChannelsMock.mockResolvedValue(envelope([channelRow({})]));

    renderPage();

    expect(await screen.findByText("example")).toBeInTheDocument();
    expect(screen.getByText("80.0%")).toBeInTheDocument();
    expect(fetchChannelsMock).toHaveBeenCalledWith(
      expect.objectContaining({ page: 1, page_size: 20 }),
    );
  });

  it("空数据时展示受控空态文案", async () => {
    fetchChannelsMock.mockResolvedValue(envelope([]));

    renderPage();

    expect(await screen.findByText("当前条件下没有频道数据。")).toBeInTheDocument();
  });

  it("请求失败时展示错误态，重试会重新发起查询", async () => {
    fetchChannelsMock.mockRejectedValueOnce(new ApiError("API 请求失败（500）", 500));
    fetchChannelsMock.mockResolvedValue(envelope([channelRow({})]));

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();

    // antd 按钮会在两个汉字之间插入空格（"重 试"），用正则匹配可访问名
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));

    await waitFor(() => expect(screen.getByText("example")).toBeInTheDocument());
    expect(fetchChannelsMock).toHaveBeenCalledTimes(2);
  });

  it("删除频道：二次确认（含记录数）后提交，提示实际删除行数并失效派生统计", async () => {
    deleteChannelRequestsMock.mockResolvedValue({ ok: true, deleted: 10 });
    fetchChannelsMock.mockResolvedValue(envelope([channelRow({})]));
    const invalidateSpy = renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "删 除" }));

    // 确认文案携带频道键与全部记录数（频道删除即清空其全部请求记录）
    expect(
      await screen.findByText(/确定删除频道 example？将删除该频道的全部 10 条请求记录/),
    ).toBeInTheDocument();
    expect(deleteChannelRequestsMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(deleteChannelRequestsMock).toHaveBeenCalledWith("example"));
    for (const key of [["requests"], ["channels"], ["users"], ["overview"]]) {
      await waitFor(() =>
        expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: key })),
      );
    }
    expect(await screen.findByText("已删除该频道的 10 条请求记录。")).toBeInTheDocument();
  });

  it("删除被服务端拒绝（仍有未完成请求）：展示受控文案，不提示成功", async () => {
    deleteChannelRequestsMock.mockRejectedValue(
      new ApiError("该频道仍有排队或处理中的请求，请等待其完成后再删除。", 409, "STORE_CONSTRAINT"),
    );
    fetchChannelsMock.mockResolvedValue(envelope([channelRow({})]));
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "删 除" }));
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));

    expect(
      await screen.findByText("该频道仍有排队或处理中的请求，请等待其完成后再删除。"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/已删除该频道的/)).not.toBeInTheDocument();
  });
});
