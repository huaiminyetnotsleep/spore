/**
 * 频道详情页组件测试（统一化迁移补齐的覆盖缺口）：
 * 唯一 H1、全时段 KPI 响应式指标栅格、时间筛选 FilterBar（筛选/重置）、
 * 紧凑密度附表、空分区的明确空态、404 Gate 不泄漏页面说明、
 * 页面操作按钮保持原目的地且不再是 Link 嵌套 Button。
 * API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import { fetchChannelDetail, type ChannelDetail } from "../../api/admin";
import { ChannelDetailPage } from "./ChannelDetailPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchChannelDetail: vi.fn(),
  };
});

const fetchChannelDetailMock = vi.mocked(fetchChannelDetail);

function detail(overrides: Partial<ChannelDetail> = {}): ChannelDetail {
  return {
    key: "example",
    stats: {
      key: "example",
      total: 10,
      succeeded: 8,
      failed: 2,
      success_rate: 0.8,
      last_requested_at: 1756598400000,
    },
    trend: [{ day: "2026-08-27", total: 10, succeeded: 8, failed: 2 }],
    media_dist: [{ key: "photo", count: 9 }],
    error_dist: [{ key: "MEDIA_DOWNLOAD_FAILED", count: 2 }],
    bot_dist: [{ bot_id: 1, bot_username: "alpha_bot", total: 10, succeeded: 8, failed: 2 }],
    since_day: "",
    until_day: "",
    ...overrides,
  };
}

/** 导航目的地探针：渲染路由标签与查询串，断言跳转目的地与参数。 */
function RouteProbe({ label }: { label: string }) {
  const location = useLocation();
  return <div>{`${label}${location.search}`}</div>;
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      {/* antd App 上下文：message/modal 实例来自它（生产由 App.tsx 挂载） */}
      <AntApp component={false}>
        <MemoryRouter initialEntries={["/channels/example"]}>
          {/* 详情页从路由参数取 key：必须挂真实 Route 才能拿到 useParams */}
          <Routes>
            <Route path="/channels/:key" element={<ChannelDetailPage />} />
            <Route path="/channels" element={<RouteProbe label="频道统计列表" />} />
            <Route path="/requests" element={<RouteProbe label="请求记录列表" />} />
          </Routes>
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

function pickDay(placeholder: string, day: string) {
  // antd v5 单个 DatePicker：聚焦打开弹层后点击目标日期单元格；目标月份
  // 不在默认面板时逐月前翻。仅作用于当前打开（非 hidden）的弹层。
  const input = screen.getByPlaceholderText(placeholder);
  fireEvent.focus(input);
  fireEvent.mouseDown(input);
  fireEvent.click(input);
  const visibleDropdown = () =>
    document.querySelector(".ant-picker-dropdown:not(.ant-picker-dropdown-hidden)");
  let cell: Element | null = null;
  for (let i = 0; i < 12; i += 1) {
    cell = visibleDropdown()?.querySelector(`.ant-picker-cell[title="${day}"]`) ?? null;
    if (cell) break;
    const prev = visibleDropdown()?.querySelector(".ant-picker-header-prev-btn");
    if (!prev) break;
    fireEvent.click(prev);
  }
  expect(cell).not.toBeNull();
  fireEvent.click(cell as Element);
}

describe("频道详情页", () => {
  beforeEach(() => {
    fetchChannelDetailMock.mockReset();
    fetchChannelDetailMock.mockResolvedValue(detail({}));
  });

  it("渲染唯一 H1「频道详情」与全时段 KPI 指标栅格", async () => {
    const { container } = renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "频道详情" })).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);

    // KPI 从固定 Col 改为响应式 MetricGrid：五张指标卡，全时段口径
    await screen.findByText("最近请求");
    expect(container.querySelectorAll(".metric-card")).toHaveLength(5);
    for (const label of ["请求量", "成功", "失败", "成功率", "最近请求"]) {
      expect(screen.getAllByText(label).length).toBeGreaterThan(0);
    }
    // 页面说明保持在 Gate 内（成功态可见）
    expect(screen.getByText("全部指标来自请求记录聚合，不触发主动抓取。")).toBeInTheDocument();
  });

  it("附表使用紧凑密度并保留机器人分布区块", async () => {
    const { container } = renderPage();

    await screen.findByText("2026-08-27");
    // 四张附表全部为 compact（size=small）
    expect(container.querySelectorAll(".ant-table-small")).toHaveLength(4);
    expect(screen.getByTestId("channel-bot-dist")).toBeInTheDocument();
    expect(screen.getByText("@alpha_bot")).toBeInTheDocument();
    expect(screen.getByText("媒体下载失败")).toBeInTheDocument();
  });

  it("空分区展示明确空态，而不是隐藏分区", async () => {
    fetchChannelDetailMock.mockResolvedValue(
      detail({ trend: [], media_dist: [], error_dist: [], bot_dist: [] }),
    );

    renderPage();

    expect(
      await screen.findByText("当前筛选范围内没有按日趋势数据。"),
    ).toBeInTheDocument();
    expect(screen.getByText("当前筛选范围内没有媒体类型分布数据。")).toBeInTheDocument();
    expect(screen.getByText("当前筛选范围内没有错误分布数据。")).toBeInTheDocument();
    expect(screen.getByText("当前筛选范围内没有按机器人分布数据。")).toBeInTheDocument();
  });

  it("时间筛选提交后按显式日期重新查询，重置恢复全时段", async () => {
    renderPage();
    await screen.findByText("2026-08-27");
    expect(fetchChannelDetailMock).toHaveBeenCalledWith("example", {});

    pickDay("开始日期", "2026-08-01");
    pickDay("结束日期", "2026-08-05");
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));

    await waitFor(() =>
      expect(fetchChannelDetailMock).toHaveBeenLastCalledWith("example", {
        since: "2026-08-01",
        until: "2026-08-05",
      }),
    );

    // 范围变化触发新查询（详情 Gate 短暂进入加载态），等筛选区重新渲染后再重置
    await screen.findByText("2026-08-27");
    fireEvent.click(screen.getByRole("button", { name: "重 置" }));

    await waitFor(() => expect(fetchChannelDetailMock).toHaveBeenLastCalledWith("example", {}));
    await screen.findByText("2026-08-27");
    expect(screen.getByPlaceholderText("开始日期")).toHaveValue("");
    expect(screen.getByPlaceholderText("结束日期")).toHaveValue("");
  });

  it("查看请求记录按钮保持原目的地并携带频道参数", async () => {
    renderPage();

    // 按钮可访问名与迁移前一致，且不再是 <Link><Button/></Link> 嵌套
    const viewRequests = await screen.findByRole("button", { name: "查看请求记录" });
    expect(viewRequests.closest("a")).toBeNull();

    fireEvent.click(viewRequests);
    expect(await screen.findByText("请求记录列表?channel=example")).toBeInTheDocument();
  });

  it("返回频道统计按钮保持原目的地", async () => {
    renderPage();

    const backToList = await screen.findByRole("button", { name: "返回频道统计" });
    expect(backToList.closest("a")).toBeNull();

    fireEvent.click(backToList);
    expect(await screen.findByText("频道统计列表")).toBeInTheDocument();
  });

  it("404 时展示详情未找到，不泄漏页面口径说明", async () => {
    fetchChannelDetailMock.mockRejectedValue(new ApiError("频道不存在", 404, "NOT_FOUND"));

    renderPage();

    expect(await screen.findByText("频道不存在")).toBeInTheDocument();
    expect(
      screen.queryByText("全部指标来自请求记录聚合，不触发主动抓取。"),
    ).not.toBeInTheDocument();
  });

  it("非 404 错误展示受控错误态与重试", async () => {
    fetchChannelDetailMock.mockRejectedValueOnce(new ApiError("API 请求失败（500）", 500));
    fetchChannelDetailMock.mockResolvedValueOnce(detail({}));

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));
    await waitFor(() => expect(screen.getByText("2026-08-27")).toBeInTheDocument());
  });
});
