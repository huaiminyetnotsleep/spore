/**
 * 业务统计页的监听源模块 Tab：源状态计数、按源 / 按 Bot / 按用户排行与
 * 按日趋势，全部跟随页首时间范围与 bot 筛选（与请求统计同款参数）。
 * 本组件位于 StatsTabs 懒加载 chunk 内，可直接使用 @ant-design/plots，
 * 不会把图表栈卷回主入口。
 */
import { useQuery } from "@tanstack/react-query";
import { Line, type LineConfig } from "@ant-design/plots";
import { Space, Statistic, Typography } from "antd";
import dayjs from "dayjs";

import {
  fetchWatchStats,
  type RangeParams,
} from "../../api/admin";
import { fmtTime } from "../../shared/format";
import { chartPalette } from "../../theme";
import { ChartPanel } from "../shared/ChartPanel";
import { PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { RankBarChart, type RankBarDatum } from "../shared/RankBarChart";

const { Text } = Typography;

export function WatchStatsSection({ range, botID }: { range: RangeParams; botID?: string }) {
  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["watch-stats", range, botID],
    queryFn: () => fetchWatchStats({ ...range, bot_id: botID }),
  });

  const trend = fillTrendDays(data, range);
  const sourceData: RankBarDatum[] = (data?.by_source ?? []).map((row) => ({
    key: String(row.channel_id),
    label: row.title || row.username || String(row.channel_id),
    count: row.events,
    messages: row.messages,
    last_at: row.last_at,
  }));
  const botData: RankBarDatum[] = (data?.by_bot ?? []).map((row) => ({
    key: String(row.bot_id),
    label: row.bot_username ? `@${row.bot_username}` : `Bot ${row.bot_id}`,
    count: row.events,
  }));
  const userData: RankBarDatum[] = (data?.by_user ?? []).map((row) => ({
    key: String(row.added_by),
    label:
      row.added_by === 0
        ? "管理员添加"
        : row.user_display_name || row.user_username || `用户 ${row.added_by}`,
    count: row.events,
  }));

  const trendConfig: LineConfig = {
    data: trend,
    xField: "day",
    yField: "events",
    height: 280,
    color: chartPalette.primary,
    axis: { x: { title: "日期" }, y: { title: "转储批次" } },
    point: { size: 3 },
    tooltip: {
      title: { field: "day" },
      items: [{ channel: "y", name: "转储批次" }],
    },
  };

  return (
    <PageSection
      title="监听源"
      extra={
        <Text type="secondary">
          {data?.since_day
            ? `统计范围：${data.since_day} ~ ${data.until_day}`
            : "统计范围：全量"}
        </Text>
      }
    >
      <PageQueryState
        initialLoading={isPending && !data}
        error={isError && !data}
        hasData={!!data}
        onRetry={() => void refetch()}
      >
        <Space direction="vertical" size="middle" className="field-width-full">
          <Space size="large" wrap>
            <Statistic title="生效监听源" value={data?.sources.approved ?? 0} />
            <Statistic title="待审批" value={data?.sources.pending ?? 0} />
            <Statistic title="已拒绝" value={data?.sources.rejected ?? 0} />
          </Space>

          <div className="chart-grid">
            <ChartPanel
              title="转储趋势"
              description="按运营时区自然日统计转储批次；跟随页首时间范围与 Bot 筛选。"
              empty={trend.length === 0 || trend.every((row) => row.events === 0)}
              emptyMessage="当前范围内暂无监听转储数据。"
            >
              <Line {...trendConfig} />
            </ChartPanel>

            <ChartPanel
              title="按源排行"
              description="按转储批次排序；点击条目跳到该源的监听记录。"
              empty={sourceData.length === 0}
              emptyMessage="当前范围内暂无源排行。"
            >
              <RankBarChart
                data={sourceData}
                color={chartPalette.primary}
                xAxisTitle="监听源"
                yAxisTitle="转储批次"
                tooltip={{
                  title: { field: "label" },
                  items: [
                    { channel: "y", name: "转储批次" },
                    { field: "messages", name: "消息条数" },
                    {
                      field: "last_at",
                      name: "最近转储",
                      valueFormatter: (value: number) => fmtTime(value),
                    },
                  ],
                }}
                navigateTo={(datum) =>
                  `/watch-events?channel_id=${encodeURIComponent(datum.key)}`
                }
              />
            </ChartPanel>

            <ChartPanel
              title="按 Bot 排行"
              description="统计各受理 Bot 执行的转储批次。"
              empty={botData.length === 0}
              emptyMessage="当前范围内暂无 Bot 转储数据。"
            >
              <RankBarChart
                data={botData}
                color={chartPalette.cyan}
                xAxisTitle="Bot"
                yAxisTitle="转储批次"
                tooltip={{
                  title: { field: "label" },
                  items: [{ channel: "y", name: "转储批次" }],
                }}
                navigateTo={() => null}
              />
            </ChartPanel>

            <ChartPanel
              title="按用户排行"
              description="按监听源当前归属用户聚合；管理员直接添加单独统计。"
              empty={userData.length === 0}
              emptyMessage="当前范围内暂无用户归属统计。"
            >
              <RankBarChart
                data={userData}
                color={chartPalette.purple}
                xAxisTitle="用户"
                yAxisTitle="转储批次"
                tooltip={{
                  title: { field: "label" },
                  items: [{ channel: "y", name: "转储批次" }],
                }}
                navigateTo={(datum) =>
                  datum.key === "0" ? null : `/users/${encodeURIComponent(datum.key)}`
                }
              />
            </ChartPanel>
          </div>
        </Space>
      </PageQueryState>
    </PageSection>
  );
}

/** 范围查询时按 since~until 补零连续日序；全量按服务端实际点升序。 */
function fillTrendDays(
  data: { trend: { day: string; events: number }[] } | undefined,
  range: RangeParams,
): { day: string; events: number }[] {
  const points = data?.trend ?? [];
  if (!range.since || !range.until) return points;
  const byDay = new Map(points.map((point) => [point.day, point.events]));
  const out: { day: string; events: number }[] = [];
  const start = dayjs(range.since);
  const end = dayjs(range.until);
  if (end.diff(start, "day") > 366) return points;
  for (let day = start; !day.isAfter(end); day = day.add(1, "day")) {
    const key = day.format("YYYY-MM-DD");
    out.push({ day: key, events: byDay.get(key) ?? 0 });
  }
  return out;
}
