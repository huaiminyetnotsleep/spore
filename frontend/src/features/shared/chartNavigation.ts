/**
 * 图表点击导航共享模块：@ant-design/plots 排行/分布 Bar 图的条目点击
 * 跳转工厂（StatsCharts 使用）。
 * 事件形态保持最小化，便于单测以 mock 组件复现点击。
 */
import type { NavigateFunction } from "react-router-dom";

/** 排行/分布条目点击时可导航数据行的最小形态。 */
export interface ChartRankDatum {
  key: string;
  label: string;
}

/** @ant-design/plots onEvent 回调的最小事件形态。 */
export interface PlotEvent {
  type?: string;
  data?: { data?: unknown };
}

/**
 * 构造 Bar 图 onEvent 回调：点击含 key/label 的条目时按回调返回的目标
 * 路径跳转；返回 null 表示该条目不可跳转（如"其他"汇总行）。
 */
export function barNavigation(
  navigate: NavigateFunction,
  destination: (datum: ChartRankDatum) => string | null,
) {
  return (_chart: unknown, event: PlotEvent) => {
    if (event.type !== "element:click") return;
    const datum = event.data?.data;
    if (!datum || typeof datum !== "object" || !("key" in datum) || !("label" in datum)) return;
    const path = destination(datum as ChartRankDatum);
    if (path) navigate(path);
  };
}
