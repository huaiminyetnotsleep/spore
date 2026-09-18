/**
 * 业务统计图表区（StatsPage 懒加载）：按分析维度分三个区块——
 * 趋势（按日错误率折线 + 按日源媒体 DC 堆叠柱状，具体数值在 tooltip 中展示）、
 * 排行（频道/用户）、分布（媒体类型/错误原因/源媒体 DC），排行/分布区块内
 * 2 列网格。横向 Bar 统一走 shared/RankBarChart（跨层契约单一定义处），
 * 数据口径、tooltip 字段与点击跳转与拆页前保持一致。
 */
import { Column, Line, Pie, type ColumnConfig, type LineConfig, type PieConfig } from "@ant-design/plots";
import { Space } from "antd";
import { useNavigate, type NavigateFunction } from "react-router-dom";

import type {
  DCTrendPoint,
  DistRow,
  StatsBot,
  StatsChannel,
  StatsError,
  StatsRequests,
  StatsTrendPoint,
  StatsUser,
} from "../../api/admin";
import { botLabel, distKeyText, errorCodeLabel, fmtTime } from "../../shared/format";
import { chartPalette } from "../../theme";
import { ChartPanel } from "../shared/ChartPanel";
import { RankBarChart, type RankBarDatum } from "../shared/RankBarChart";
import { SectionCard } from "../shared/PageStates";

const CHART_HEIGHT = 280;
const ERROR_RATE_THRESHOLD = 0.2;
const ERROR_RATE_OK_COLOR = chartPalette.success;
const ERROR_RATE_ALERT_COLOR = chartPalette.error;
const EMPTY_POINT_STROKE = chartPalette.neutral;
const OTHER_ERROR_KEY = "__other__";

interface ErrorRateDatum {
  day: string;
  /** 无终态请求的日期为 null：轴上保留该日，折线跨日连接、不误标为 0。 */
  error_rate: number | null;
  /** 折线色彩通道哨兵值：null 日取 0，避免阈值色阶接触 null 导致整条折线渲染失败。 */
  rate_color: number;
  /** 空心点标记位置：0 = 底部轴线上。 */
  no_data_y: number;
  succeeded: number;
  failed: number;
  terminal_total: number;
}

interface PlotEvent {
  type?: string;
  data?: { data?: ErrorRateDatum | RankBarDatum };
}

function userLabel(user: StatsUser): string {
  if (user.display_name) return user.display_name;
  if (user.username) return `@${user.username}`;
  return `用户 ${user.id}`;
}

/** DC 分布切片标签：空 key = 未记录（纯文本/旧记录），其余为 "DC {id}"。 */
function dcKeyText(key: string): string {
  return key === "" ? distKeyText(key) : `DC ${key}`;
}

function errorRateRows(trend: StatsTrendPoint[]): ErrorRateDatum[] {
  // 保留范围内全部日期（后端已按筛选范围补齐空日），保证横轴日期数与
  // 筛选时间范围一致；空日 error_rate 为 null，由折线跨日连接 + 底部
  // 空心点表达，不剔除该日也不误标为 0。
  return trend.map((point) => ({
    day: point.day,
    error_rate: point.error_rate,
    rate_color: point.error_rate ?? 0,
    no_data_y: 0,
    succeeded: point.succeeded,
    failed: point.failed,
    terminal_total: point.succeeded + point.failed,
  }));
}

function errorRateColor(datum: ErrorRateDatum): string {
  return datum.error_rate !== null && datum.error_rate >= ERROR_RATE_THRESHOLD
    ? ERROR_RATE_ALERT_COLOR
    : ERROR_RATE_OK_COLOR;
}

interface ErrorRateTooltipItem {
  name?: unknown;
  value?: unknown;
  color?: string;
}

function renderErrorRateTooltip(
  _event: unknown,
  { title, items }: { title?: unknown; items: ErrorRateTooltipItem[] },
): HTMLElement {
  const root = document.createElement("div");
  root.className = "stats-error-rate-tooltip";

  const heading = document.createElement("div");
  heading.className = "stats-error-rate-tooltip__title";
  heading.textContent = String(title ?? "");
  root.appendChild(heading);

  const list = document.createElement("div");
  list.className = "stats-error-rate-tooltip__list";
  for (const item of items) {
    const row = document.createElement("div");
    row.className = "stats-error-rate-tooltip__item";
    if (item.name === "错误率") {
      row.classList.add(
        item.color === ERROR_RATE_ALERT_COLOR
          ? "stats-error-rate-tooltip__item--alert"
          : "stats-error-rate-tooltip__item--ok",
      );
    }

    const name = document.createElement("span");
    name.textContent = String(item.name ?? "");
    const value = document.createElement("span");
    value.className = "stats-error-rate-tooltip__value";
    value.textContent = String(item.value ?? "");
    row.append(name, value);
    list.appendChild(row);
  }
  root.appendChild(list);
  return root;
}

function dayNavigation(navigate: NavigateFunction) {
  return (_chart: unknown, event: PlotEvent) => {
    if (event.type !== "element:click") return;
    const datum = event.data?.data;
    if (!datum || !("day" in datum) || typeof datum.day !== "string") return;
    navigate(`/requests?since=${encodeURIComponent(datum.day)}&until=${encodeURIComponent(datum.day)}`);
  };
}

function distributionNavigation(navigate: NavigateFunction, navigateTo: (datum: RankBarDatum) => string | null) {
  return (_chart: unknown, event: PlotEvent) => {
    if (event.type !== "element:click") return;
    const datum = event.data?.data;
    if (!datum || !("key" in datum) || typeof datum.key !== "string") return;
    const destination = navigateTo(datum);
    if (destination) navigate(destination);
  };
}

function ErrorRateChart({ trend, navigate }: { trend: StatsTrendPoint[]; navigate: NavigateFunction }) {
  const data = errorRateRows(trend);
  const solidRows = data.filter((row) => row.error_rate !== null);
  const hollowRows = data.filter((row) => row.error_rate === null);
  const config: LineConfig = {
    data,
    xField: "day",
    height: CHART_HEIGHT,
    scale: {
      y: { domain: [0, 1] },
      color: {
        type: "threshold",
        domain: [ERROR_RATE_THRESHOLD],
        range: [ERROR_RATE_OK_COLOR, ERROR_RATE_ALERT_COLOR],
      },
    },
    legend: { color: { position: "top" } },
    annotations: [
      {
        type: "lineY",
        data: [ERROR_RATE_THRESHOLD],
        style: { stroke: chartPalette.warning, lineDash: [4, 4], lineWidth: 2 },
        label: { text: "阈值 20%", position: "right" },
      },
    ],
    axis: {
      x: { title: "日期" },
      y: { title: "错误率", labelFormatter: (value: string) => `${Number(value) * 100}%` },
    },
    tooltip: {
      title: { field: "day" },
      items: [
        (datum: ErrorRateDatum) => ({
          name: "错误率",
          value: datum.error_rate === null ? "—" : `${(datum.error_rate * 100).toFixed(1)}%`,
          color: datum.error_rate === null ? undefined : errorRateColor(datum),
        }),
        { field: "succeeded", name: "成功数" },
        { field: "failed", name: "失败数" },
        { field: "terminal_total", name: "终态总量" },
      ],
    },
    interaction: { tooltip: { render: renderErrorRateTooltip } },
    // 折线 + 点拆为多个 mark：空数据日 y 为 null，阈值色阶接触 null 会使
    // 整条折线渲染失败，故色彩通道用哨兵值（rate_color），有值日画实心点、
    // 空日在底部画空心点；style.connect 让折线跨空日连接。
    children: [
      {
        type: "line",
        yField: "error_rate",
        shapeField: "smooth",
        colorField: "rate_color",
        style: { gradient: "y", lineWidth: 2, lineJoin: "round", connect: true },
      },
      {
        type: "point",
        data: solidRows,
        yField: "error_rate",
        style: {
          size: 4,
          fill: (datum: ErrorRateDatum) => errorRateColor(datum),
          stroke: (datum: ErrorRateDatum) => errorRateColor(datum),
        },
        tooltip: false,
      },
      {
        type: "point",
        data: hollowRows,
        yField: "no_data_y",
        style: { size: 4, fill: "transparent", stroke: EMPTY_POINT_STROKE, lineWidth: 1.5 },
        tooltip: false,
      },
    ],
    onEvent: dayNavigation(navigate),
  };
  return (
    <ChartPanel
      title="按日错误率"
      description="日期轴覆盖完整筛选范围：有值日期为实心点，无终态日期在底部以空心点标记、折线跨日连接不补 0；橙色虚线为 20% 告警阈值，点击日期查看明细。"
      empty={data.length === 0}
      emptyMessage="当前范围内没有可计算错误率的终态请求。"
    >
      <Line {...config} />
    </ChartPanel>
  );
}

function ChannelRankChart({ channels }: { channels: StatsChannel[] }) {
  const data: RankBarDatum[] = channels.map((channel) => ({
    key: channel.key,
    label: channel.key,
    count: channel.total,
    succeeded: channel.succeeded,
    failed: channel.failed,
    success_rate: channel.success_rate,
    last_requested_at: channel.last_requested_at,
  }));
  return (
    <ChartPanel
      title="主要频道"
      description="按请求量排序，展示 Top 5；横轴为请求数，纵轴为频道；点击条目查看频道详情。"
      empty={data.length === 0}
      emptyMessage="当前范围内没有频道排行数据。"
    >
      <RankBarChart
        data={data}
        color={chartPalette.primary}
        xAxisTitle="频道"
        yAxisTitle="请求数"
        rowHeight={42}
        tooltip={{
          title: { field: "label" },
          items: [
            { channel: "y", name: "请求总数" },
            { field: "succeeded", name: "成功数" },
            { field: "failed", name: "失败数" },
            { field: "success_rate", name: "成功率", valueFormatter: (value: number) => `${(value * 100).toFixed(1)}%` },
            { field: "last_requested_at", name: "最近请求", valueFormatter: (value: number) => fmtTime(value) },
          ],
        }}
        navigateTo={(datum) => `/channels/${encodeURIComponent(datum.key)}`}
      />
    </ChartPanel>
  );
}

function UserRankChart({ users }: { users: StatsUser[] }) {
  const data: RankBarDatum[] = users.map((user) => ({
    key: String(user.id),
    label: userLabel(user),
    count: user.total,
    succeeded: user.succeeded,
    failed: user.failed,
    success_rate: user.success_rate,
    last_requested_at: user.last_requested_at,
  }));
  return (
    <ChartPanel
      title="主要用户"
      description="按请求量排序，展示 Top 10；横轴为请求数，纵轴为用户；点击条目查看用户详情。"
      empty={data.length === 0}
      emptyMessage="当前范围内没有用户排行数据。"
    >
      <RankBarChart
        data={data}
        color={chartPalette.purple}
        xAxisTitle="用户"
        yAxisTitle="请求数"
        tooltip={{
          title: { field: "label" },
          items: [
            { channel: "y", name: "请求总数" },
            { field: "succeeded", name: "成功数" },
            { field: "failed", name: "失败数" },
            { field: "success_rate", name: "成功率", valueFormatter: (value: number) => `${(value * 100).toFixed(1)}%` },
            { field: "last_requested_at", name: "最近请求", valueFormatter: (value: number) => fmtTime(value) },
          ],
        }}
        navigateTo={(datum) => `/users/${encodeURIComponent(datum.key)}`}
      />
    </ChartPanel>
  );
}

function MediaDistributionChart({ rows, total, navigate }: { rows: DistRow[]; total: number; navigate: NavigateFunction }) {
  const data: RankBarDatum[] = rows.map((row) => ({
    key: row.key,
    label: distKeyText(row.key),
    count: row.count,
    ratio: total > 0 ? row.count / total : 0,
  }));
  const config: PieConfig = {
    data,
    angleField: "count",
    colorField: "label",
    height: CHART_HEIGHT,
    radius: 0.82,
    innerRadius: 0.52,
    legend: { color: { position: "right" } },
    tooltip: {
      title: { field: "label" },
      items: [
        { field: "count", name: "请求数" },
        { field: "ratio", name: "占范围请求比例", valueFormatter: (value: number) => `${(value * 100).toFixed(1)}%` },
      ],
    },
    onEvent: distributionNavigation(navigate, (datum) =>
      datum.key === "" ? null : `/requests?media_type=${encodeURIComponent(datum.key)}`,
    ),
  };
  return (
    <ChartPanel
      title="媒体类型分布"
      description="按请求数展示媒体类型占比；空媒体类型显示为“未记录”；点击切片查看请求记录。"
      empty={data.length === 0}
      emptyMessage="当前范围内没有媒体类型分布数据。"
    >
      <Pie {...config} />
    </ChartPanel>
  );
}

function ErrorDistributionChart({ rows, navigate }: { rows: StatsError[]; navigate: NavigateFunction }) {
  const data: RankBarDatum[] = rows.map((row) => ({
    key: row.key,
    label: row.key === OTHER_ERROR_KEY ? "其他" : errorCodeLabel(distKeyText(row.key)),
    count: row.count,
    ratio: row.ratio,
  }));
  const config: PieConfig = {
    data,
    angleField: "count",
    colorField: "label",
    height: CHART_HEIGHT,
    radius: 0.82,
    innerRadius: 0.52,
    legend: { color: { position: "right" } },
    tooltip: {
      title: { field: "label" },
      items: [
        { field: "count", name: "失败数" },
        { field: "ratio", name: "占失败请求比例", valueFormatter: (value: number) => `${(value * 100).toFixed(1)}%` },
      ],
    },
    onEvent: distributionNavigation(navigate, (datum) =>
      datum.key === OTHER_ERROR_KEY
        ? null
        : `/requests?status=failed&error_code=${encodeURIComponent(datum.key)}`,
    ),
  };
  return (
    <ChartPanel
      title="错误原因排行"
      description="仅统计失败请求，按失败数展示 Top 5 与“其他”；点击具体原因切片查看请求记录。"
      empty={data.length === 0}
      emptyMessage="当前范围内没有失败请求或错误原因数据。"
    >
      <Pie {...config} />
    </ChartPanel>
  );
}

interface DCTrendDatum {
  day: string;
  label: string;
  count: number;
}

function DCTrendChart({ points }: { points: DCTrendPoint[] }) {
  // 长表摊平：每天每个 DC 一行，colorField 区分序列、stackY 堆叠。
  // 以全部出现过的 DC 为序列全集，为无数据的日期按每个序列补 0，
  // 保证横轴日期数与筛选时间范围一致（后端已按范围补齐空日）。
  const series: string[] = [];
  for (const point of points) {
    for (const row of point.dist) {
      const label = dcKeyText(row.key);
      if (!series.includes(label)) series.push(label);
    }
  }
  const data: DCTrendDatum[] = points.flatMap((point) => {
    const byLabel = new Map(point.dist.map((row) => [dcKeyText(row.key), row.count]));
    return series.map((label) => ({ day: point.day, label, count: byLabel.get(label) ?? 0 }));
  });
  const config: ColumnConfig = {
    data,
    xField: "day",
    yField: "count",
    colorField: "label",
    transform: [{ type: "stackY" }],
    height: CHART_HEIGHT,
    legend: { color: { position: "top" } },
    axis: { x: { title: "日期" }, y: { title: "请求数" } },
    tooltip: {
      title: { field: "day" },
      items: [{ field: "count", name: "请求数" }],
    },
  };
  return (
    <ChartPanel
      title="按日源媒体 DC"
      description="按日统计源媒体所在 DC 的请求数（堆叠）；一条请求跨多个 DC 时在每个 DC 各计一次，“未记录”含纯文本与旧记录，无数据日期补 0 保持日期轴完整。"
      empty={data.length === 0}
      emptyMessage="当前范围内没有源媒体 DC 日分布数据。"
    >
      <Column {...config} />
    </ChartPanel>
  );
}

function DCDistributionChart({ rows, total }: { rows: DistRow[]; total: number }) {
  const data: RankBarDatum[] = rows.map((row) => ({
    key: row.key,
    label: dcKeyText(row.key),
    count: row.count,
    ratio: total > 0 ? row.count / total : 0,
  }));
  const config: PieConfig = {
    data,
    angleField: "count",
    colorField: "label",
    height: CHART_HEIGHT,
    radius: 0.82,
    innerRadius: 0.52,
    legend: { color: { position: "right" } },
    tooltip: {
      title: { field: "label" },
      items: [
        { field: "count", name: "请求数" },
        { field: "ratio", name: "占范围请求比例", valueFormatter: (value: number) => `${(value * 100).toFixed(1)}%` },
      ],
    },
  };
  return (
    <ChartPanel
      title="源媒体 DC 分布"
      description="按请求数展示源媒体所在 Telegram DC 占比；跨多个 DC 的请求在每个 DC 各计一次，空显示为“未记录”。"
      empty={data.length === 0}
      emptyMessage="当前范围内没有源媒体 DC 分布数据。"
    >
      <Pie {...config} />
    </ChartPanel>
  );
}

function BotDistributionChart({ rows, total }: { rows: StatsBot[]; total: number }) {
  const data: RankBarDatum[] = rows.map((row) => ({
    key: String(row.bot_id),
    label: botLabel(row.bot_id, row.bot_username),
    count: row.total,
    succeeded: row.succeeded,
    failed: row.failed,
    ratio: total > 0 ? row.total / total : 0,
    last_requested_at: row.last_requested_at,
  }));
  return (
    <ChartPanel
      title="机器人分布"
      description="按受理机器人统计请求数占比（多机器人池）；bot_id 为 0 的切片是存量记录/非 Bot 通道创建；点击切片查看该机器人的请求记录。"
      empty={data.length === 0}
      emptyMessage="当前范围内没有机器人分布数据。"
    >
      <RankBarChart
        data={data}
        color={chartPalette.cyan}
        xAxisTitle="机器人"
        yAxisTitle="请求数"
        tooltip={{
          title: { field: "label" },
          items: [
            { channel: "y", name: "请求总数" },
            { field: "succeeded", name: "成功数" },
            { field: "failed", name: "失败数" },
            { field: "ratio", name: "占范围请求比例", valueFormatter: (value: number) => `${(value * 100).toFixed(1)}%` },
            { field: "last_requested_at", name: "最近请求", valueFormatter: (value: number) => fmtTime(value) },
          ],
        }}
        navigateTo={(datum) => `/requests?bot_id=${encodeURIComponent(datum.key)}`}
      />
    </ChartPanel>
  );
}

export function StatsCharts({ requests }: { requests: StatsRequests }) {
  const navigate = useNavigate();
  return (
    <Space direction="vertical" size="middle" className="field-width-full">
      <SectionCard title="趋势">
        <Space direction="vertical" size="middle" className="field-width-full">
          <ErrorRateChart trend={requests.trend} navigate={navigate} />
          <DCTrendChart points={requests.dc_trend} />
        </Space>
      </SectionCard>
      <SectionCard title="排行">
        <div className="chart-grid">
          <ChannelRankChart channels={requests.top_channels} />
          <UserRankChart users={requests.top_users} />
        </div>
      </SectionCard>
      <SectionCard title="分布">
        <div className="chart-grid">
          <MediaDistributionChart rows={requests.media_dist} total={requests.total} navigate={navigate} />
          <ErrorDistributionChart rows={requests.error_dist} navigate={navigate} />
          <DCDistributionChart rows={requests.dc_dist} total={requests.total} />
          <BotDistributionChart rows={requests.bot_dist} total={requests.total} />
        </div>
      </SectionCard>
    </Space>
  );
}
