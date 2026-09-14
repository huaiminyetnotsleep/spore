/**
 * 统一横向排行/分布 Bar 图：跨层契约的唯一定义处——
 * xField "label" / yField "count"（组件内部转置，视觉横轴为数量、纵轴为分类），
 * 主数值 tooltip 读 channel "y"，条目数值标签右侧，点击经 barNavigation 跳转。
 * 复用方：统计页频道/用户/媒体/错误原因四图 + 频道加入来源分布图。
 */
import { Bar, type BarConfig } from "@ant-design/plots";
import { useNavigate } from "react-router-dom";

import { barNavigation, type ChartRankDatum } from "./chartNavigation";

const CHART_HEIGHT = 280;

/** 横向 Bar 数据行：key/label 供跳转，count 为主数值，其余字段供 tooltip 读取。 */
export interface RankBarDatum extends ChartRankDatum {
  count: number;
  [field: string]: unknown;
}

export function RankBarChart({
  data,
  color,
  xAxisTitle,
  yAxisTitle,
  tooltip,
  navigateTo,
  rowHeight = 36,
  minHeight = CHART_HEIGHT,
}: {
  data: RankBarDatum[];
  color: string;
  xAxisTitle: string;
  yAxisTitle: string;
  tooltip: BarConfig["tooltip"];
  navigateTo: (datum: ChartRankDatum) => string | null;
  rowHeight?: number;
  minHeight?: number;
}) {
  const navigate = useNavigate();
  const config: BarConfig = {
    data,
    xField: "label",
    yField: "count",
    height: Math.max(minHeight, data.length * rowHeight),
    color,
    axis: { x: { title: xAxisTitle }, y: { title: yAxisTitle } },
    tooltip,
    label: { text: "count", position: "right" },
    onEvent: barNavigation(navigate, navigateTo),
  };
  return <Bar {...config} />;
}
