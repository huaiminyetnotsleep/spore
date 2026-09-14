/**
 * 频道详情页（SSR /channels/{key} 的 SPA 对应实现）。
 * 头部统计为全时段聚合（与 SSR 同口径），按日趋势与媒体/错误分布
 * 应用时间范围筛选；展示名直接用频道键，不为取名访问 Telegram。
 */
import { useQuery } from "@tanstack/react-query";
import { Button, Col, DatePicker, Form, Row, Space, Statistic, Table, Typography } from "antd";
import type { ColumnsType } from "antd/es/table";
import dayjs, { type Dayjs } from "dayjs";
import { useState } from "react";
import { Link, useParams } from "react-router-dom";

import { fetchChannelDetail, type DistRow, type TrendPoint } from "../../api/admin";
import { distKeyText, fmtRate, fmtTime } from "../../shared/format";
import { DetailGate, PageCard, SectionCard } from "../shared/PageStates";

const { Text } = Typography;

const trendColumns: ColumnsType<TrendPoint> = [
  { title: "日期", dataIndex: "day", key: "day" },
  { title: "请求量", dataIndex: "total", key: "total", align: "right" },
  { title: "成功", dataIndex: "succeeded", key: "succeeded", align: "right" },
  { title: "失败", dataIndex: "failed", key: "failed", align: "right" },
];

const distColumns: ColumnsType<DistRow> = [
  { title: "类型 / 错误码", dataIndex: "key", key: "key", render: distKeyText },
  { title: "数量", dataIndex: "count", key: "count", align: "right" },
];

interface RangeFormValues {
  since?: Dayjs | null;
  until?: Dayjs | null;
}

export function ChannelDetailPage() {
  const { key } = useParams();
  const [form] = Form.useForm<RangeFormValues>();
  const [range, setRange] = useState<{ since?: string; until?: string }>({});

  const query = useQuery({
    queryKey: ["channels", "detail", key, range],
    queryFn: () => fetchChannelDetail(key ?? "", range),
    enabled: Boolean(key),
  });
  const { data: detail } = query;

  return (
    <Space direction="vertical" size="middle" className="field-width-full">
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
            <PageCard
              title={`频道 ${detail.key}`}
              extra={
                <Space>
                  <Link to={`/requests?channel=${encodeURIComponent(detail.key)}`}>
                    <Button>查看请求记录</Button>
                  </Link>
                  <Link to="/channels">
                    <Button>返回频道统计</Button>
                  </Link>
                </Space>
              }
            >
              <Row gutter={16}>
                <Col span={4}>
                  <Statistic title="请求量" value={detail.stats.total} />
                </Col>
                <Col span={4}>
                  <Statistic title="成功" value={detail.stats.succeeded} />
                </Col>
                <Col span={4}>
                  <Statistic title="失败" value={detail.stats.failed} />
                </Col>
                <Col span={4}>
                  <Statistic
                    title="成功率"
                    value={fmtRate(
                      detail.stats.success_rate,
                      detail.stats.succeeded + detail.stats.failed,
                    )}
                  />
                </Col>
                <Col span={8}>
                  <Statistic title="最近请求" value={fmtTime(detail.stats.last_requested_at)} />
                </Col>
              </Row>

              <Form
                form={form}
                layout="inline"
                className="layout-margin-top-16"
                onFinish={(values) => {
                  setRange({
                    since: values.since ? values.since.format("YYYY-MM-DD") : undefined,
                    until: values.until ? values.until.format("YYYY-MM-DD") : undefined,
                  });
                }}
              >
                <Form.Item name="since">
                  <DatePicker placeholder="开始日期" maxDate={dayjs()} />
                </Form.Item>
                <Form.Item name="until">
                  <DatePicker placeholder="结束日期" maxDate={dayjs()} />
                </Form.Item>
                <Form.Item>
                  <Button type="primary" htmlType="submit">
                    应用
                  </Button>
                </Form.Item>
              </Form>
            </PageCard>

            {detail.trend.length > 0 && (
              <SectionCard title="按日趋势">
                <Table<TrendPoint>
                  rowKey="day"
                  size="small"
                  columns={trendColumns}
                  dataSource={detail.trend}
                  pagination={false}
                />
              </SectionCard>
            )}

            <Space direction="vertical" size="middle" className="field-width-full">
              {detail.media_dist.length > 0 && (
                <SectionCard title="媒体类型分布">
                  <Table<DistRow>
                    rowKey="key"
                    size="small"
                    columns={distColumns}
                    dataSource={detail.media_dist}
                    pagination={false}
                  />
                </SectionCard>
              )}
              {detail.error_dist.length > 0 && (
                <SectionCard title="错误分布">
                  <Table<DistRow>
                    rowKey="key"
                    size="small"
                    columns={distColumns}
                    dataSource={detail.error_dist}
                    pagination={false}
                  />
                </SectionCard>
              )}
            </Space>
            <Text type="secondary">全部指标来自请求记录聚合，不触发主动抓取。</Text>
          </>
        )}
      </DetailGate>
    </Space>
  );
}
