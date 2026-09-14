import { Line, type LineConfig } from "@ant-design/plots";
import { Alert, Segmented, Spin, Typography } from "antd";
import { useQuery } from "@tanstack/react-query";
import { useRef, useState } from "react";

import {
  fetchSystemMetrics,
  type SystemMetricPoint,
  type SystemMetricsRange,
} from "../../api/admin";
import { fmtBytes } from "../../shared/format";
import { ChartPanel } from "../shared/ChartPanel";
import { PageCard } from "../shared/PageStates";

const rangeOptions: { label: string; value: SystemMetricsRange }[] = [
  { label: "实时", value: "realtime" },
  { label: "4 小时", value: "4h" },
  { label: "1 天", value: "1d" },
];

const rangeStorageKey = "spore:overview:system-metrics-range";

function isSystemMetricsRange(value: string | null): value is SystemMetricsRange {
  return value === "realtime" || value === "4h" || value === "1d";
}

function initialRange(): SystemMetricsRange {
  try {
    const stored = window.sessionStorage.getItem(rangeStorageKey);
    return isSystemMetricsRange(stored) ? stored : "1d";
  } catch {
    return "1d";
  }
}

function persistRange(range: SystemMetricsRange): void {
  try {
    window.sessionStorage.setItem(rangeStorageKey, range);
  } catch {
    // 浏览器禁用存储时只影响跨页面记忆，当前页面选择仍然有效。
  }
}

function fmtRate(value: number): string {
  return `${fmtBytes(value)}/s`;
}

function fmtPercent(value: number): string {
  return `${value.toFixed(1)}%`;
}

function fmtAt(value: number, range: SystemMetricsRange): string {
  const date = new Date(value);
  return range === "1d"
    ? date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

function latestMetricValue(points: SystemMetricPoint[], field: keyof SystemMetricPoint): number | null {
  for (let index = points.length - 1; index >= 0; index -= 1) {
    const value = points[index][field];
    if (typeof value === "number") return value;
  }
  return null;
}

function lineConfig(data: Array<{ at: number; value: number; series?: string }>, range: SystemMetricsRange, yTitle: string, formatter: (v: number) => string, color?: string[], area = true): LineConfig {
  return {
    data,
    xField: "at",
    yField: "value",
    ...(data.some((item) => item.series) ? { colorField: "series" } : {}),
    height: 260,
    animate: false,
    scale: color ? { color: { range: color } } : undefined,
    axis: {
      x: { title: "时间", labelFormatter: (value: string) => fmtAt(Number(value), range) },
      y: { title: yTitle, labelFormatter: (value: string) => formatter(Number(value)) },
    },
    tooltip: {
      preserve: true,
      title: { field: "at", valueFormatter: (value: string) => fmtAt(Number(value), range) },
      items: [
        { channel: "y", name: yTitle, valueFormatter: (value: number) => formatter(value) },
      ],
    },
    ...(area ? { area: { style: { fillOpacity: 0.12 } } } : {}),
    style: { lineWidth: 2 },
  };
}

interface PlotEvent {
  nativeEvent?: boolean;
  data?: { data?: { x?: unknown } };
}

interface G2Chart {
  on(event: string, handler: (event: PlotEvent) => void): unknown;
  emit(event: string, payload: unknown): unknown;
}

interface PlotChart {
  chart?: G2Chart;
}

function PersistentLine({ config }: { config: LineConfig }) {
  const tooltipX = useRef<number | null>(null);

  return (
    <div className="system-metrics-chart">
      <Line
        {...config}
        onReady={(plot: PlotChart) => {
          const g2 = plot.chart;
          if (!g2) return;
          // 注意必须直接订阅内层 G2 chart 的 emitter 通道：Plot 包装器按
          // payload.type 转发事件，而 tooltip:show 的 payload.type 是原始指针
          // 事件名、afterrender 甚至没有 payload，二者都到不了 onEvent。
          g2.on("tooltip:show", (event: PlotEvent) => {
            const x = event?.data?.data?.x;
            if (typeof x === "number") tooltipX.current = x;
          });
          g2.on("tooltip:hide", (event: PlotEvent) => {
            // G2 在交互重建时会发 nativeEvent=false 的内部隐藏事件；只有
            // 原生指针离开/抬起才清除悬停位置，避免数据刷新丢失固定状态。
            if (event.nativeEvent !== false) tooltipX.current = null;
          });
          g2.on("afterrender", () => {
            const x = tooltipX.current;
            if (x == null) return;
            // 数据刷新会整体重渲染并销毁 tooltip；渲染完成后按上次悬停的 x
            // 用新数据重建 tooltip。
            g2.emit("tooltip:show", { nativeEvent: false, data: { data: { x } } });
          });
        }}
      />
    </div>
  );
}

function MetricLine({
  title,
  description,
  points,
  range,
  field,
  formatter,
  emptyMessage,
}: {
  title: string;
  description: string;
  points: SystemMetricPoint[];
  range: SystemMetricsRange;
  field: "rss_bytes" | "temp_dir_bytes" | "cpu_percent";
  formatter: (value: number) => string;
  emptyMessage: string;
}) {
  const data = points.flatMap((point) => point[field] == null ? [] : [{ at: point.at, value: point[field] as number }]);
  const latest = latestMetricValue(points, field);
  const yTitle = field === "rss_bytes" ? "RSS" : field === "cpu_percent" ? "CPU %" : "占用";
  return (
    <ChartPanel title={title} currentValue={latest == null ? undefined : formatter(latest)} description={description} descriptionInline empty={data.length === 0} emptyMessage={emptyMessage}>
      <PersistentLine config={lineConfig(data, range, yTitle, formatter)} />
    </ChartPanel>
  );
}

function TransferLine({ points, range }: { points: SystemMetricPoint[]; range: SystemMetricsRange }) {
  const data = points.flatMap((point) => [
    ...(point.download_bytes_per_second == null ? [] : [{ at: point.at, value: point.download_bytes_per_second, series: "下载" }]),
    ...(point.upload_bytes_per_second == null ? [] : [{ at: point.at, value: point.upload_bytes_per_second, series: "上传" }]),
  ]);
  const latestDownload = latestMetricValue(points, "download_bytes_per_second");
  const latestUpload = latestMetricValue(points, "upload_bytes_per_second");
  return (
    <ChartPanel
      title="传输速率"
      currentValue={latestDownload == null && latestUpload == null ? undefined : <>↓ {latestDownload == null ? "—" : fmtRate(latestDownload)} · ↑ {latestUpload == null ? "—" : fmtRate(latestUpload)}</>}
      description="全服务上下行速率。"
      descriptionInline
      empty={data.length === 0}
      emptyMessage="暂无可用的传输速率数据。"
    >
      <PersistentLine config={lineConfig(data, range, "速率", fmtRate, ["#1677ff", "#52c41a"], false)} />
    </ChartPanel>
  );
}

export function SystemMetricsSection() {
  const [range, setRange] = useState<SystemMetricsRange>(initialRange);
  const interval = range === "realtime" ? 2000 : range === "4h" ? 30000 : 60000;

  function changeRange(value: SystemMetricsRange): void {
    setRange(value);
    persistRange(value);
  }
  const query = useQuery({
    queryKey: ["system-metrics", range],
    queryFn: () => fetchSystemMetrics(range),
    refetchInterval: interval,
    placeholderData: (previous) => previous,
  });

  return (
    <PageCard
      title="资源与传输监控"
      extra={<Segmented options={rangeOptions} value={range} onChange={(value) => changeRange(value as SystemMetricsRange)} />}
    >
      {query.isPending && !query.data ? <Spin className="page-loading" tip="监控数据加载中…" /> : null}
      {query.isError && !query.data ? (
        <Alert type="error" showIcon message="监控数据加载失败" description="请稍后重试。" action={<Typography.Link onClick={() => void query.refetch()}>重试</Typography.Link>} />
      ) : null}
      {query.data ? (
        <div className="system-metrics-grid">
          <MetricLine title="进程 CPU" description="当前进程 CPU 占用（占全部核心）。" points={query.data.points} range={range} field="cpu_percent" formatter={fmtPercent} emptyMessage="暂无可用的 CPU 数据。" />
          <MetricLine title="进程内存" description="当前进程 RSS。" points={query.data.points} range={range} field="rss_bytes" formatter={fmtBytes} emptyMessage="暂无可用的 RSS 数据。" />
          <MetricLine title="临时目录大小" description="临时目录占用。" points={query.data.points} range={range} field="temp_dir_bytes" formatter={fmtBytes} emptyMessage="暂无可用的临时目录数据。" />
          <TransferLine points={query.data.points} range={range} />
        </div>
      ) : null}
    </PageCard>
  );
}
