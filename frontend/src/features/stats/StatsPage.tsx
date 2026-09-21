/**
 * 业务统计页（/stats）：职责 = 业务数字。自上而下分区块：
 * 时间范围工具栏（页首，统领全页：核心指标与全部页签都随其查询；快捷
 * Segmented 含"全量"，自定义走 RangePicker 选完即生效，另有机器人筛选）→
 * 请求核心指标 → 趋势/排行/分布/监听源 Tab（StatsTabs 懒加载，活跃页签
 * 持久化）→ 全时段快照（SnapshotSection，不受筛选影响）。
 * 数据经 GET /api/v1/stats 获取，缺省近 7 天。
 * 页面状态（时间范围 / bot 筛选 / 活跃 Tab）写入 sessionStorage：切到其他
 * 菜单再回来不重置；快捷范围恢复时按当天重新计算（滚动窗口不指向过去）。
 */
import { useQuery } from "@tanstack/react-query";
import { DatePicker, Segmented, Select, Space, Typography } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import { lazy, Suspense, useEffect, useState } from "react";

import { fetchBots, fetchStats, type RangeParams } from "../../api/admin";
import { botLabel, fmtRate } from "../../shared/format";
import { MetricGrid } from "../shared/MetricGrid";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { PageQueryState } from "../shared/QueryStates";
import { SnapshotSection } from "./SnapshotSection";

const StatsTabs = lazy(() =>
  import("./StatsTabs").then(({ StatsTabs: Tabs }) => ({ default: Tabs })),
);

type PersistedStatsState = {
  preset: PresetKey | "custom";
  range: RangeParams;
  botID?: string;
  activeTab: "trend" | "rank" | "dist" | "watch";
};

const STATE_STORAGE_KEY = "stats-page-state-v1";

/** 恢复持久化状态：快捷范围按当天重算（滚动窗口），自定义沿用保存区间。 */
function restoreState(): PersistedStatsState | null {
  try {
    const raw = sessionStorage.getItem(STATE_STORAGE_KEY);
    if (!raw) return null;
    const saved = JSON.parse(raw) as PersistedStatsState;
    if (saved.preset && saved.preset !== "custom" && saved.preset !== "all") {
      return { ...saved, range: presetRange(saved.preset) };
    }
    if (!saved.range) return null;
    return saved;
  } catch {
    return null;
  }
}

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
  const restored = restoreState();
  const [range, setRange] = useState<RangeParams>(restored?.range ?? {});
  // 初始近 7 天（与后端缺省一致）；自定义范围应用后为 "custom"（未选中）。
  const [preset, setPreset] = useState<PresetKey | "custom">(
    restored?.preset ?? DEFAULT_PRESET,
  );
  // RangePicker 受控显示值：快捷范围同步回显；全量时清空并禁用；恢复自定义
  // 区间时按保存的日期重建。
  const [pickerValue, setPickerValue] = useState<PickerRange>(() => {
    if (restored?.preset === "custom" && restored.range.since && restored.range.until) {
      return [dayjs(restored.range.since), dayjs(restored.range.until)];
    }
    return null;
  });
  // 机器人筛选（多机器人池）：切换即生效；查询失败不阻塞页面。
  const [botID, setBotID] = useState<string | undefined>(restored?.botID);
  // 趋势/排行/分布/监听源 Tab 活跃页签（跨菜单往返保持）。
  const [activeTab, setActiveTab] = useState<"trend" | "rank" | "dist" | "watch">(
    restored?.activeTab ?? "trend",
  );

  // 页面状态持久化：任一项变化即写 sessionStorage（菜单往返不重置）。
  useEffect(() => {
    const state: PersistedStatsState = { preset, range, botID, activeTab };
    try {
      sessionStorage.setItem(STATE_STORAGE_KEY, JSON.stringify(state));
    } catch {
      // 隐私模式等存储不可用：静默降级为不持久化
    }
  }, [preset, range, botID, activeTab]);
  const bots = useQuery({ queryKey: ["bots"], queryFn: fetchBots });
  const botOptions = (bots.data?.bots ?? []).map((bot) => ({
    value: String(bot.bot_id),
    label: botLabel(bot.bot_id, bot.username),
  }));

  const { data, isPending, isError, refetch } = useQuery({
    queryKey: ["stats", range, botID],
    queryFn: () => fetchStats({ ...range, bot_id: botID }),
  });

  const requests = data?.requests;
  const done = requests ? requests.succeeded + requests.failed : 0;
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
    <PageScaffold
      title="业务统计"
      description="请求核心指标、趋势、排行与分布；时间范围与机器人筛选选完即时生效。"
      data-testid="spa-shell"
    >
      <PageQueryState
        initialLoading={isPending}
        error={isError}
        hasData={data !== undefined}
        onRetry={() => void refetch()}
      >
        {data && requests ? (
          <>
            <PageSection
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
            </PageSection>

            <PageSection title="核心指标" extra={<Text type="secondary">请求总数 {requests.total}</Text>}>
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
            </PageSection>

            <Suspense fallback={<PageSection loading />}>
              <StatsTabs
                requests={requests}
                range={range}
                botID={botID}
                activeKey={activeTab}
                onChange={setActiveTab}
              />
            </Suspense>

            <SnapshotSection />
          </>
        ) : null}
      </PageQueryState>
    </PageScaffold>
  );
}
