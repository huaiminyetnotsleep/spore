/**
 * 业务统计页（/stats）：职责 = 业务数字。自上而下分区块：
 * 时间范围工具栏（快捷范围 Segmented 立即生效，含"全量"；自定义范围走单个
 * RangePicker，选完即生效）→ 请求核心指标（总数在卡片标题行，结果三项合并）→
 * 趋势/排行/分布图表区（StatsCharts 懒加载）→ 全时段快照（SnapshotSection，
 * overview 接口，不受筛选影响）。数据经 GET /api/v1/stats 获取，缺省近 7 天。
 */
import { useQuery } from "@tanstack/react-query";
import { DatePicker, Segmented, Select, Space, Spin, Typography } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import { lazy, Suspense, useState } from "react";

import { fetchBots, fetchStats, type RangeParams } from "../../api/admin";
import { botLabel, fmtRate } from "../../shared/format";
import { LoadError, SectionCard } from "../shared/PageStates";
import { MetricGrid } from "../shared/MetricGrid";
import { SnapshotSection } from "./SnapshotSection";

const StatsCharts = lazy(() =>
  import("./StatsCharts").then(({ StatsCharts: Charts }) => ({ default: Charts })),
);

const { Text } = Typography;

const DATE_FORMAT = "YYYY-MM-DD";

type PresetKey = "today" | "last7" | "last30" | "all";

const DEFAULT_PRESET: PresetKey = "last7";

/** 快捷范围显式计算日期（今天 / 含当天的近 N 天），保证回显与服务端生效范围一致。 */
function presetRange(key: PresetKey): RangeParams {
  const today = dayjs();
  switch (key) {
    case "today":
      return { since: today.format(DATE_FORMAT), until: today.format(DATE_FORMAT) };
    case "last30":
      return { since: today.subtract(29, "day").format(DATE_FORMAT), until: today.format(DATE_FORMAT) };
    default:
      return { since: today.subtract(6, "day").format(DATE_FORMAT), until: today.format(DATE_FORMAT) };
  }
}

const PRESET_OPTIONS = [
  { label: "今天", value: "today" },
  { label: "近 7 天", value: "last7" },
  { label: "近 30 天", value: "last30" },
  { label: "全量", value: "all" },
];

type PickerRange = [Dayjs | null, Dayjs | null] | null;

export function StatsPage() {
  const [range, setRange] = useState<RangeParams>({});
  // 初始近 7 天（与后端缺省一致，不带参数请求）；自定义范围应用后为 "custom"，
  // Segmented 显示为未选中。
  const [preset, setPreset] = useState<PresetKey | "custom">(DEFAULT_PRESET);
  // RangePicker 受控显示值：快捷范围同步回显；全量时清空并禁用。
  const [pickerValue, setPickerValue] = useState<PickerRange>(null);
  // 机器人筛选（多机器人池）：切换即生效；查询失败不阻塞页面。
  const [botID, setBotID] = useState<string | undefined>(undefined);
  const bots = useQuery({ queryKey: ["bots"], queryFn: fetchBots });
  const botOptions = (bots.data?.bots ?? []).map((bot) => ({
    value: String(bot.bot_id),
    label: botLabel(bot.bot_id, bot.username),
  }));

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["stats", range, botID],
    queryFn: () => fetchStats({ ...range, bot_id: botID }),
  });

  if (isPending) {
    return (
      <SectionCard data-testid="spa-shell">
        <Spin className="page-loading" tip="加载中…" />
      </SectionCard>
    );
  }
  if (isError || !data) {
    return <LoadError onRetry={() => void refetch()} />;
  }

  const requests = data.requests;
  const done = requests.succeeded + requests.failed;
  const isAll = preset === "all";

  const applyPreset = (key: PresetKey) => {
    setPreset(key);
    if (key === "all") {
      setPickerValue(null);
      setRange({ all: "1" });
      return;
    }
    const next = presetRange(key);
    setPickerValue([dayjs(next.since!), dayjs(next.until!)]);
    setRange(next);
  };

  // 选完起止立即生效（无需应用按钮）；清空则恢复缺省近 7 天。
  const applyPickerRange = (values: PickerRange) => {
    if (values && values[0] && values[1]) {
      setPreset("custom");
      setPickerValue([values[0], values[1]]);
      setRange({
        since: values[0].format(DATE_FORMAT),
        until: values[1].format(DATE_FORMAT),
      });
      return;
    }
    setPreset(DEFAULT_PRESET);
    setPickerValue(null);
    setRange({});
  };

  return (
    <Space direction="vertical" size="middle" className="field-width-full" data-testid="spa-shell">
      <SectionCard
        title="时间范围"
        extra={
          <Text type="secondary">
            {data.since_day ? `统计范围：${data.since_day} ~ ${data.until_day}` : "统计范围：全量"}
          </Text>
        }
      >
        <Space wrap>
          <Segmented
            options={PRESET_OPTIONS}
            value={preset === "custom" ? undefined : preset}
            onChange={(value) => applyPreset(value as PresetKey)}
          />
          <DatePicker.RangePicker
            value={pickerValue}
            onChange={applyPickerRange}
            placeholder={["开始日期", "结束日期"]}
            maxDate={dayjs()}
            allowClear
            disabled={isAll}
          />
          <Select
            placeholder="机器人"
            allowClear
            className="field-width-140"
            virtual={false}
            loading={bots.isPending}
            value={botID}
            onChange={(value) => setBotID(value || undefined)}
            options={[{ value: "", label: "全部" }, ...botOptions]}
          />
        </Space>
      </SectionCard>

      <SectionCard title="核心指标" extra={<Text type="secondary">请求总数 {requests.total}</Text>}>
        <MetricGrid
          maxColumns={3}
          items={[
            {
              label: "请求结果",
              stats: [
                { label: "成功", value: requests.succeeded, tone: "success" },
                { label: "失败", value: requests.failed, tone: "danger" },
                { label: "未完成", value: requests.unfinished, tone: "warning" },
              ],
            },
            {
              label: "成功率",
              value: fmtRate(requests.success_rate, done),
              tone: "success",
              progress: done > 0 ? requests.success_rate : undefined,
            },
            { label: "活跃用户", value: requests.active_users },
          ]}
        />
      </SectionCard>

      <Suspense fallback={<Spin />}>
        <StatsCharts requests={requests} />
      </Suspense>

      <SnapshotSection />
    </Space>
  );
}
