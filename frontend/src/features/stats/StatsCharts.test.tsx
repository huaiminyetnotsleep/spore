import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import type { StatsRequests } from "../../api/admin";
import { StatsCharts } from "./StatsCharts";

interface MockPlotProps {
  data?: unknown[];
  xField?: string;
  yField?: string;
  angleField?: string;
  shapeField?: string;
  colorField?: string;
  style?: { gradient?: string };
  scale?: { color?: { type?: string; domain?: number[] } };
  annotations?: Array<{
    type?: string;
    data?: number[];
    label?: { text?: string };
  }>;
  tooltip?: {
    items?: Array<
      | { channel?: string; field?: string }
      | ((datum: unknown) => { color?: string; name?: string; value?: string })
    >;
  };
  interaction?: { tooltip?: { render?: (...args: unknown[]) => HTMLElement } };
  onEvent?: (chart: unknown, event: { type: string; data: { data: unknown } }) => void;
  children?: unknown;
}

function MockPlot({
  data,
  xField,
  yField,
  shapeField,
  colorField,
  style,
  scale,
  tooltip,
  interaction,
  onEvent,
  annotations,
  children,
}: MockPlotProps) {
  const firstTooltip = tooltip?.items?.[0];
  const firstTooltipKey =
    typeof firstTooltip === "function" ? "fn" : firstTooltip?.channel ?? firstTooltip?.field ?? "";
  const tooltipColors =
    typeof firstTooltip === "function" && data
      ? data.slice(0, 2).map((datum) => firstTooltip(datum).color ?? "").join(",")
      : "";
  const childCount = Array.isArray(children) ? children.length : children ? 1 : 0;
  return (
    <button
      type="button"
      data-testid="mock-plot"
      onClick={() => onEvent?.({}, { type: "element:click", data: { data: data?.[0] } })}
    >
      {`${xField ?? ""}/${yField ?? ""}/${data?.length ?? 0}/${firstTooltipKey}/${shapeField ?? ""}/${colorField ?? ""}/${style?.gradient ?? ""}/${scale?.color?.type ?? ""}/${scale?.color?.domain?.[0] ?? ""}/${annotations?.[0]?.type ?? ""}/${annotations?.[0]?.data?.[0] ?? ""}/${annotations?.[0]?.label?.text ?? ""}/${tooltipColors}/${interaction?.tooltip?.render ? "custom" : ""}/-c${childCount}`}
    </button>
  );
}

function MockPie({ data, angleField, colorField, tooltip, onEvent }: MockPlotProps) {
  const firstTooltip = tooltip?.items?.[0];
  const firstTooltipField = typeof firstTooltip === "function" ? "" : firstTooltip?.field ?? "";
  return (
    <button
      type="button"
      data-testid="mock-pie"
      onClick={() => onEvent?.({}, { type: "element:click", data: { data: data?.[0] } })}
    >
      {`${angleField ?? ""}/${colorField ?? ""}/${data?.length ?? 0}/${firstTooltipField}`}
    </button>
  );
}

vi.mock("@ant-design/plots", () => ({
  Bar: MockPlot,
  Column: MockPlot,
  Line: MockPlot,
  Pie: MockPie,
}));

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname + location.search}</output>;
}

const requests: StatsRequests = {
  total: 12,
  succeeded: 9,
  failed: 2,
  unfinished: 1,
  active_users: 2,
  success_rate: 9 / 11,
  error_rate: 2 / 11,
  trend: [
    { day: "2026-09-01", total: 6, succeeded: 5, failed: 1, error_rate: 1 / 6 },
    { day: "2026-09-02", total: 6, succeeded: 4, failed: 1, error_rate: 1 / 5 },
  ],
  top_channels: [
    {
      key: "alpha",
      total: 8,
      succeeded: 6,
      failed: 2,
      success_rate: 0.75,
      last_requested_at: 0,
    },
  ],
  top_users: [
    {
      id: 1,
      username: "alice",
      display_name: "Alice",
      total: 8,
      succeeded: 6,
      failed: 2,
      success_rate: 0.75,
      last_requested_at: 0,
    },
  ],
  media_dist: [
    { key: "video", count: 4 },
    { key: "", count: 1 },
  ],
  error_dist: [{ key: "MESSAGE_NOT_FOUND", count: 2, ratio: 1 }],
  dc_dist: [
    { key: "2", count: 5 },
    { key: "", count: 1 },
  ],
  dc_trend: [
    {
      day: "2026-09-01",
      dist: [
        { key: "2", count: 3 },
        { key: "", count: 1 },
      ],
    },
    { day: "2026-09-02", dist: [{ key: "4", count: 2 }] },
  ],
};

describe("业务统计图表", () => {
  it("排行图使用分类 x 数值 y，组件转置后显示横轴数量、纵轴分类", () => {
    render(
      <MemoryRouter>
        <StatsCharts requests={requests} />
      </MemoryRouter>,
    );

    const plots = screen.getAllByTestId("mock-plot");
    expect(plots).toHaveLength(4);
    expect(plots.map((plot) => plot.textContent)).toEqual([
      "day//2/fn////threshold/0.2/lineY/0.2/阈值 20%/#52c41a,#ff4d4f/custom/-c3",
      "day/count/6/count//label/////////-c0",
      "label/count/1/y///////////-c0",
      "label/count/1/y///////////-c0",
    ]);
    const pies = screen.getAllByTestId("mock-pie");
    expect(pies).toHaveLength(3);
    expect(pies.map((pie) => pie.textContent)).toEqual([
      "count/label/2/count",
      "count/label/1/count",
      "count/label/2/count",
    ]);
    expect(screen.getByText(/横轴为请求数，纵轴为频道/)).toBeInTheDocument();
    expect(screen.getByText(/横轴为请求数，纵轴为用户/)).toBeInTheDocument();
    expect(screen.getByText(/按请求数展示媒体类型占比；空媒体类型显示为“未记录”/)).toBeInTheDocument();
    expect(screen.getByText(/按请求数展示源媒体所在 Telegram DC 占比/)).toBeInTheDocument();
    expect(screen.getByText(/按日统计源媒体所在 DC 的请求数/)).toBeInTheDocument();
    expect(screen.getByText(/橙色虚线为 20% 告警阈值/)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  it("排行和趋势条目提供明细跳转", () => {
    render(
      <MemoryRouter initialEntries={["/overview"]}>
        <LocationProbe />
        <StatsCharts requests={requests} />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getAllByTestId("mock-plot")[2]);
    expect(screen.getByTestId("location")).toHaveTextContent("/channels/alpha");
  });

  it("时间轴图保留范围内空数据日期，横轴与筛选范围一致", () => {
    const sparse: StatsRequests = {
      ...requests,
      trend: [
        { day: "2026-09-01", total: 6, succeeded: 5, failed: 1, error_rate: 1 / 6 },
        { day: "2026-09-02", total: 0, succeeded: 0, failed: 0, error_rate: null },
        { day: "2026-09-03", total: 5, succeeded: 4, failed: 1, error_rate: 1 / 5 },
      ],
      dc_trend: [
        { day: "2026-09-01", dist: [{ key: "2", count: 3 }] },
        { day: "2026-09-02", dist: [] },
      ],
    };
    render(
      <MemoryRouter>
        <StatsCharts requests={sparse} />
      </MemoryRouter>,
    );

    const plots = screen.getAllByTestId("mock-plot");
    // 折线：空数据日不剔除（3 天全保留），错误率为 null 由底部空心点 + 跨日连接表达
    expect(plots[0].textContent).toBe(
      "day//3/fn////threshold/0.2/lineY/0.2/阈值 20%/#52c41a,/custom/-c3",
    );
    // 堆叠柱状：空日按序列全集补 0（2 天 × 1 个序列）
    expect(plots[1].textContent).toBe("day/count/2/count//label/////////-c0");
  });

  it("DC 图表不做切片跳转（请求列表尚无 DC 筛选参数）", () => {
    render(
      <MemoryRouter initialEntries={["/overview"]}>
        <LocationProbe />
        <StatsCharts requests={requests} />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getAllByTestId("mock-plot")[1]);
    fireEvent.click(screen.getAllByTestId("mock-pie")[2]);
    expect(screen.getByTestId("location")).toHaveTextContent("/overview");
  });

  it("分布饼图切片提供明细跳转，并忽略其他错误汇总", () => {
    render(
      <MemoryRouter initialEntries={["/overview"]}>
        <LocationProbe />
        <StatsCharts requests={requests} />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getAllByTestId("mock-pie")[0]);
    expect(screen.getByTestId("location")).toHaveTextContent("/requests?media_type=video");
    cleanup();

    render(
      <MemoryRouter initialEntries={["/overview"]}>
        <LocationProbe />
        <StatsCharts requests={requests} />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getAllByTestId("mock-pie")[1]);
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/requests?status=failed&error_code=MESSAGE_NOT_FOUND",
    );
    cleanup();

    render(
      <MemoryRouter initialEntries={["/overview"]}>
        <LocationProbe />
        <StatsCharts
          requests={{
            ...requests,
            error_dist: [{ key: "__other__", count: 2, ratio: 1 }],
          }}
        />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getAllByTestId("mock-pie")[1]);
    expect(screen.getByTestId("location")).toHaveTextContent("/overview");
  });

  it("各图表在没有数据时显示明确空态而不是空坐标轴", () => {
    render(
      <MemoryRouter>
        <StatsCharts
          requests={{
            ...requests,
            trend: [],
            top_channels: [],
            top_users: [],
            media_dist: [],
            error_dist: [],
            dc_dist: [],
            dc_trend: [],
          }}
        />
      </MemoryRouter>,
    );

    expect(screen.queryByTestId("mock-plot")).not.toBeInTheDocument();
    expect(screen.getByText("当前范围内没有可计算错误率的终态请求。")).toBeInTheDocument();
    expect(screen.getByText("当前范围内没有频道排行数据。")).toBeInTheDocument();
    expect(screen.getByText("当前范围内没有用户排行数据。")).toBeInTheDocument();
    expect(screen.getByText("当前范围内没有媒体类型分布数据。")).toBeInTheDocument();
    expect(screen.getByText("当前范围内没有失败请求或错误原因数据。")).toBeInTheDocument();
    expect(screen.getByText("当前范围内没有源媒体 DC 日分布数据。")).toBeInTheDocument();
    expect(screen.getByText("当前范围内没有源媒体 DC 分布数据。")).toBeInTheDocument();
  });
});
