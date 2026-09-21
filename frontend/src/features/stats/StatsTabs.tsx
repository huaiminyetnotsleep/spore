/**
 * 业务统计页的图表/模块 Tab 容器（懒加载）：趋势 / 排行 / 分布 / 监听源
 * 四个随时间范围查询的模块分页签切换；时间范围工具栏在页首统领全页
 * （核心指标与全部页签同受其约束）。Tab 的活跃状态由 StatsPage 持有并
 * 持久化（菜单往返不重置）。
 * 本模块与 StatsCharts 同 chunk（图表栈重，路由级懒加载）。
 */
import { Tabs } from "antd";

import type { StatsRequests } from "../../api/admin";
import { DistCharts, RankCharts, TrendCharts } from "./StatsCharts";
import { WatchStatsSection } from "./WatchStatsSection";
import type { RangeParams } from "../../api/admin";

export const STATS_TAB_KEYS = ["trend", "rank", "dist", "watch"] as const;
export type StatsTabKey = (typeof STATS_TAB_KEYS)[number];

export function StatsTabs({
  requests,
  range,
  botID,
  activeKey,
  onChange,
}: {
  requests: StatsRequests;
  range: RangeParams;
  botID?: string;
  activeKey: StatsTabKey;
  onChange: (key: StatsTabKey) => void;
}) {
  return (
    <Tabs
      activeKey={activeKey}
      onChange={(key) => onChange(key as StatsTabKey)}
      items={[
        {
          key: "trend",
          label: "趋势",
          children: <TrendCharts requests={requests} />,
        },
        {
          key: "rank",
          label: "排行",
          children: <RankCharts requests={requests} />,
        },
        {
          key: "dist",
          label: "分布",
          children: <DistCharts requests={requests} />,
        },
        {
          key: "watch",
          label: "监听源",
          children: <WatchStatsSection range={range} botID={botID} />,
        },
      ]}
    />
  );
}
