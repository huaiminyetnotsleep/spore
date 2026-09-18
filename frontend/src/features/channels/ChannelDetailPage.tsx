/**
 * 频道详情页（SSR /channels/{key} 的 SPA 对应实现）。
 * 头部统计为全时段聚合（与 SSR 同口径），按日趋势与媒体/错误分布
 * 应用时间范围筛选；展示名直接用频道键，不为取名访问 Telegram。
 * KPI 使用响应式 MetricGrid，时间筛选走统一 FilterBar（筛选/重置），
 * 附表为紧凑密度 DataTable，空分区展示明确的空态。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, DatePicker, Form, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import dayjs, { type Dayjs } from "dayjs";
import { useState } from "react";
import { useNavigate, useParams } from "react-router-dom";

import { fetchChannelDetail, type ChannelBotRow, type DistRow, type TrendPoint } from "../../api/admin";
import { botLabel, distKeyText, errorCodeLabel, fmtRate, fmtTime } from "../../shared/format";
import { DataTable } from "../shared/DataTable";
import { FilterBar } from "../shared/FilterBar";
import { MetricGrid } from "../shared/MetricGrid";
import { PageScaffold, PageSection } from "../shared/PageLayout";
import { DetailGate } from "../shared/PageStates";

const { Text } = Typography;

const trendColumns: ColumnsType<TrendPoint> = [
  { title: "日期", dataIndex: "day", key: "day" },
  { title: "请求量", dataIndex: "total", key: "total", align: "right" },
  { title: "成功", dataIndex: "succeeded", key: "succeeded", align: "right" },
  { title: "失败", dataIndex: "failed", key: "failed", align: "right" },
];

const distColumns: ColumnsType<DistRow> = [
  { title: "类型", dataIndex: "key", key: "key", render: distKeyText },
  { title: "数量", dataIndex: "count", key: "count", align: "right" },
];

const errorDistColumns: ColumnsType<DistRow> = [
  { title: "错误码", dataIndex: "key", key: "key", render: (key: string) => errorCodeLabel(distKeyText(key)) },
  { title: "数量", dataIndex: "count", key: "count", align: "right" },
];

const botDistColumns: ColumnsType<ChannelBotRow> = [
  { title: "机器人", dataIndex: "bot_id", key: "bot", render: (_, row) => botLabel(row.bot_id, row.bot_username) },
  { title: "请求量", dataIndex: "total", key: "total", align: "right" },
  { title: "成功", dataIndex: "succeeded", key: "succeeded", align: "right" },
  { title: "失败", dataIndex: "failed", key: "failed", align: "right" },
  {
    title: "成功率",
    key: "rate",
    align: "right",
    render: (_, row) => {
      const done = row.succeeded + row.failed;
      return fmtRate(done > 0 ? row.succeeded / done : 0, done);
    },
  },
];

interface RangeFormValues {
  since?: Dayjs | null;
  until?: Dayjs | null;
}

export function ChannelDetailPage() {
  const { key } = useParams();
  const [form] = Form.useForm<RangeFormValues>();
  const [range, setRange] = useState<{ since?: string; until?: string }>({});
  const navigate = useNavigate();

  const query = useQuery({
    queryKey: ["channels", "detail", key, range],
    queryFn: () => fetchChannelDetail(key ?? "", range),
    enabled: Boolean(key),
  });
  const { data: detail } = query;

  return (
    <PageScaffold
      title="频道详情"
      description="全时段汇总与按日趋势、媒体/错误分布；数据全部来自请求记录聚合。"
      actions={
        detail ? (
          <>
            <Button
              onClick={() => void navigate(`/requests?channel=${encodeURIComponent(detail.key)}`)}
            >
              查看请求记录
            </Button>
            <Button onClick={() => void navigate("/channels")}>返回频道统计</Button>
          </>
        ) : (
          <Button onClick={() => void navigate("/channels")}>返回频道统计</Button>
        )
      }
    >
      <DetailGate
        loading={query.isPending}
        error={query.error}
        onRetry={() => void query.refetch()}
        notFoundTitle="频道不存在"
        backTo="/channels"
        backText="返回频道统计"
      >
        {detail && (
          <>
            <PageSection title={`频道 ${detail.key}`}>
              <div className="field-width-full">
                {/* 全时段聚合口径（与 SSR 一致），不受下方时间筛选影响 */}
                <MetricGrid
                  items={[
                    { label: "请求量", value: detail.stats.total },
                    { label: "成功", value: detail.stats.succeeded, tone: "success" },
                    { label: "失败", value: detail.stats.failed, tone: "danger" },
                    {
                      label: "成功率",
                      value: fmtRate(
                        detail.stats.success_rate,
                        detail.stats.succeeded + detail.stats.failed,
                      ),
                    },
                    { label: "最近请求", value: fmtTime(detail.stats.last_requested_at) },
                  ]}
                />

                <div className="layout-margin-top-16">
                  <FilterBar<RangeFormValues>
                    mode="submit"
                    form={form}
                    onFinish={(values) => {
                      setRange({
                        since: values.since ? values.since.format("YYYY-MM-DD") : undefined,
                        until: values.until ? values.until.format("YYYY-MM-DD") : undefined,
                      });
                    }}
                    onReset={() => {
                      // 条件未变化时显式刷新；变化时由新 query key 触发查询
                      if (range.since === undefined && range.until === undefined) {
                        void query.refetch();
                      } else {
                        setRange({});
                      }
                    }}
                  >
                    <Form.Item name="since">
                      <DatePicker placeholder="开始日期" maxDate={dayjs()} />
                    </Form.Item>
                    <Form.Item name="until">
                      <DatePicker placeholder="结束日期" maxDate={dayjs()} />
                    </Form.Item>
                  </FilterBar>
                </div>
              </div>
            </PageSection>

            <PageSection title="按日趋势">
              <DataTable<TrendPoint>
                density="compact"
                rowKey="day"
                columns={trendColumns}
                dataSource={detail.trend}
                pagination={false}
                emptyText="当前筛选范围内没有按日趋势数据。"
              />
            </PageSection>

            <PageSection title="媒体类型分布">
              <DataTable<DistRow>
                density="compact"
                rowKey="key"
                columns={distColumns}
                dataSource={detail.media_dist}
                pagination={false}
                emptyText="当前筛选范围内没有媒体类型分布数据。"
              />
            </PageSection>

            <PageSection title="错误分布">
              <DataTable<DistRow>
                density="compact"
                rowKey="key"
                columns={errorDistColumns}
                dataSource={detail.error_dist}
                pagination={false}
                emptyText="当前筛选范围内没有错误分布数据。"
              />
            </PageSection>

            <PageSection title="按机器人分布" data-testid="channel-bot-dist">
              <DataTable<ChannelBotRow>
                density="compact"
                rowKey="bot_id"
                columns={botDistColumns}
                dataSource={detail.bot_dist ?? []}
                pagination={false}
                emptyText="当前筛选范围内没有按机器人分布数据。"
              />
            </PageSection>

            <Text type="secondary">全部指标来自请求记录聚合，不触发主动抓取。</Text>
          </>
        )}
      </DetailGate>
    </PageScaffold>
  );
}
