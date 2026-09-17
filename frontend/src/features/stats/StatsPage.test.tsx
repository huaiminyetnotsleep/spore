/**
 * 业务统计页组件测试：分区结构（时间范围工具栏/核心指标/图表懒加载/全时段快照）、
 * 快捷范围点击立即生效、自定义范围应用与服务端回显、错误态（含重试）、
 * 快照查询失败只影响快照区。API 层以模块 mock 注入，不发起真实网络请求；
 * 图表库 mock 为轻量占位。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import dayjs from "dayjs";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import {
  fetchOverview,
  fetchStats,
  type OverviewResponse,
  type StatsResponse,
} from "../../api/admin";
import { StatsPage } from "./StatsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchStats: vi.fn(),
    fetchOverview: vi.fn(),
  };
});

vi.mock("@ant-design/plots", () => ({
  Bar: () => <div data-testid="mock-plot" />,
  Column: () => <div data-testid="mock-plot" />,
  Line: () => <div data-testid="mock-plot" />,
  Pie: () => <div data-testid="mock-plot" />,
}));

const fetchStatsMock = vi.mocked(fetchStats);
const fetchOverviewMock = vi.mocked(fetchOverview);

function overviewResponse(): OverviewResponse {
  return {
    version: "dev",
    started_at: 1760000000000,
    addr: "127.0.0.1:8080",
    workers: 2,
    health: {
      store_ok: true,
      mtproto_state: "ready",
      db_size_bytes: 1024,
      db_path: "/data/spore.db",
      temp_dir_bytes: 2048,
      temp_dir: "/tmp/spore",
      github_configured: false,
    },
    queue: { len: 0, cap: 100 },
    requests: { queued_rows: 1, processing_rows: 2 },
    users: { total: 10, enabled: 8, pending: 1, disabled: 1, archived: 0 },
    join: {
      pending: 3,
      approved: 5,
      rejected: 7,
      failed: 9,
      active_joined: 11,
      external_active: 13,
      left_total: 15,
      max_channels: 20,
      source_dist: [
        { key: "join_command", count: 2 },
        { key: "approved", count: 7 },
        { key: "external", count: 2 },
      ],
    },
  };
}

function statsResponse(overrides: Partial<StatsResponse> = {}): StatsResponse {
  return {
    since_day: "2026-08-21",
    until_day: "2026-08-27",
    requests: {
      total: 12,
      succeeded: 9,
      failed: 2,
      unfinished: 1,
      active_users: 2,
      success_rate: 9 / 11,
      error_rate: 2 / 11,
      trend: [
        { day: "2026-08-27", total: 12, succeeded: 9, failed: 2, error_rate: 2 / 11 },
      ],
      top_channels: [
        { key: "alpha", total: 12, succeeded: 9, failed: 2, success_rate: 9 / 11, last_requested_at: 0 },
      ],
      top_users: [
        { id: 1, username: "alice", display_name: "Alice", total: 12, succeeded: 9, failed: 2, success_rate: 9 / 11, last_requested_at: 0 },
      ],
      media_dist: [{ key: "photo", count: 9 }],
      error_dist: [{ key: "MEDIA_DOWNLOAD_FAILED", count: 2, ratio: 1 }],
      dc_dist: [{ key: "2", count: 12 }],
      dc_trend: [{ day: "2026-08-27", dist: [{ key: "2", count: 12 }] }],
      bot_dist: [],
    },
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/stats"]}>
        <StatsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function pickRange(since: string, until: string) {
  // rc-picker（antd 5）的输入框键盘路径在 jsdom 中不提交范围值，走真实用户
  // 路径：聚焦打开弹层，翻页到目标月份后依次点击起止日期单元格。
  const start = screen.getByPlaceholderText("开始日期");
  fireEvent.focus(start);
  fireEvent.mouseDown(start);
  fireEvent.click(start);
  for (let i = 0; i < 12; i += 1) {
    if (document.querySelector(`.ant-picker-cell[title="${since}"]`)) break;
    const prev = document.querySelector(".ant-picker-header-prev-btn");
    if (!prev) break;
    fireEvent.click(prev);
  }
  const sinceCell = document.querySelector(`.ant-picker-cell[title="${since}"]`);
  const untilCell = document.querySelector(`.ant-picker-cell[title="${until}"]`);
  fireEvent.click(sinceCell as Element);
  fireEvent.click(untilCell as Element);
}

describe("业务统计页", () => {
  beforeEach(() => {
    fetchStatsMock.mockReset();
    fetchOverviewMock.mockReset();
    fetchOverviewMock.mockResolvedValue(overviewResponse());
  });

  it("渲染唯一 H1「业务统计」页面标题", async () => {
    fetchStatsMock.mockResolvedValue(statsResponse());

    renderPage();

    expect(
      await screen.findByRole("heading", { level: 1, name: "业务统计" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  it("缺省近 7 天：渲染核心指标、懒加载图表与全时段快照", async () => {
    fetchStatsMock.mockResolvedValue(statsResponse());

    renderPage();

    // 请求总数在核心指标卡片标题行回显
    expect(await screen.findByText("请求总数 12")).toBeInTheDocument();
    for (const title of ["请求结果", "成功率", "活跃用户"]) {
      expect(screen.getByText(title)).toBeInTheDocument();
    }
    // 请求结果合并卡：成功/失败/未完成 三个语义色数值
    expect(screen.getByText("成功")).toBeInTheDocument();
    expect(screen.getByText("失败")).toBeInTheDocument();
    expect(screen.getByText("未完成")).toBeInTheDocument();
    expect(screen.queryByText("错误率")).not.toBeInTheDocument();
    // fmtRate：9/11 → 81.8%
    expect(screen.getByText("81.8%")).toBeInTheDocument();
    // 生效范围在工具栏回显
    expect(screen.getByText("统计范围：2026-08-21 ~ 2026-08-27")).toBeInTheDocument();

    // 柱状图移除：折线 + DC 堆叠柱状 + 两排行 + 三饼图 = 7 张 mock 占位（页级 mock 不区分类型）
    expect(await screen.findAllByTestId("mock-plot")).toHaveLength(7);

    // 全时段快照：用户计数 + 频道加入指标 + 上限后缀
    expect(screen.getByText("全时段快照")).toBeInTheDocument();
    expect(screen.getByText("不受时间筛选影响")).toBeInTheDocument();
    expect(screen.getByText("用户状态")).toBeInTheDocument();
    expect(screen.getByText("加入审批")).toBeInTheDocument();
    expect(screen.getByText("当前加入")).toBeInTheDocument();
    // 面板头汇总位：用户总数与频道加入额度
    expect(screen.getByText("总数 10")).toBeInTheDocument();
    expect(screen.getByText("已加入 11 / 20")).toBeInTheDocument();
    expect(fetchStatsMock).toHaveBeenCalledWith({});
    expect(fetchOverviewMock).toHaveBeenCalledTimes(1);
  });

  it("点击快捷范围立即生效（显式日期查询）", async () => {
    fetchStatsMock.mockResolvedValue(statsResponse());

    renderPage();
    await screen.findByText("请求总数 12");

    const today = dayjs();
    fireEvent.click(screen.getByText("近 30 天"));

    await waitFor(() =>
      expect(fetchStatsMock).toHaveBeenCalledWith({
        since: today.subtract(29, "day").format("YYYY-MM-DD"),
        until: today.format("YYYY-MM-DD"),
      }),
    );
  });

  it("点击全量以无时间界查询、禁用范围选择并回显全量", async () => {
    fetchStatsMock.mockResolvedValue(statsResponse({ since_day: "", until_day: "" }));

    renderPage();
    await screen.findByText("请求总数 12");

    fireEvent.click(screen.getByText("全量"));

    await waitFor(() => expect(fetchStatsMock).toHaveBeenCalledWith({ all: "1" }));
    expect(await screen.findByText("统计范围：全量")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("开始日期")).toBeDisabled();
    expect(screen.getByPlaceholderText("结束日期")).toBeDisabled();
  });

  it("RangePicker 自定义范围选完立即查询并回显服务端范围", async () => {
    fetchStatsMock.mockResolvedValueOnce(statsResponse());
    fetchStatsMock.mockResolvedValueOnce(
      statsResponse({ since_day: "2026-08-01", until_day: "2026-08-05" }),
    );

    renderPage();
    await screen.findByText("统计范围：2026-08-21 ~ 2026-08-27");

    pickRange("2026-08-01", "2026-08-05");

    await waitFor(() =>
      expect(fetchStatsMock).toHaveBeenCalledWith({
        since: "2026-08-01",
        until: "2026-08-05",
      }),
    );
    // 工具栏回显服务端生效范围（而非本地输入）
    expect(await screen.findByText("统计范围：2026-08-01 ~ 2026-08-05")).toBeInTheDocument();
    // 自定义范围无需应用按钮
    expect(screen.queryByRole("button", { name: /应\s*用/ })).not.toBeInTheDocument();
  });

  it("查询失败时展示错误态，重试会重新发起查询", async () => {
    fetchStatsMock.mockRejectedValueOnce(new ApiError("API 请求失败（500）", 500));
    fetchStatsMock.mockResolvedValueOnce(statsResponse());

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));

    await waitFor(() => expect(screen.getByText("请求总数 12")).toBeInTheDocument());
    expect(fetchStatsMock).toHaveBeenCalledTimes(2);
  });

  it("快照查询失败只影响快照区，统计区正常渲染且可恢复", async () => {
    fetchStatsMock.mockResolvedValue(statsResponse());
    fetchOverviewMock.mockRejectedValueOnce(new ApiError("API 请求失败（500）", 500));

    renderPage();

    // 统计区照常渲染
    expect(await screen.findByText("请求总数 12")).toBeInTheDocument();
    expect(screen.getByText("统计范围：2026-08-21 ~ 2026-08-27")).toBeInTheDocument();
    // 快照区展示局部错误态
    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();

    // 重试成功后快照区恢复
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));
    await waitFor(() => expect(screen.getByText("加入审批")).toBeInTheDocument());
  });
});
