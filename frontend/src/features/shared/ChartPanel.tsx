/**
 * 图表块外壳：小节标题 + 副描述 + 空态文案 + 内容区。
 * 卡片底板由页面层 PageSection/PageCard 承担，这里只负责区块内单个图表的版式；
 * 空态判定由调用方给出（empty=true 时渲染 emptyMessage，不渲染图表）。
 */
import { Typography } from "antd";
import type { ReactNode } from "react";

const { Text } = Typography;

export function ChartPanel({
  title,
  currentValue,
  description,
  descriptionInline = false,
  empty,
  emptyMessage,
  children,
}: {
  title: ReactNode;
  currentValue?: ReactNode;
  description?: string;
  descriptionInline?: boolean;
  empty?: boolean;
  emptyMessage?: string;
  children: ReactNode;
}) {
  return (
    <section className="chart-panel">
      <div className="chart-panel__head">
        <div className="chart-panel__title-row">
          <div className="chart-panel__title-content">
            <Typography.Title level={5} className="layout-margin-0">
              {title}
            </Typography.Title>
            {descriptionInline && description ? (
              <Text type="secondary" className="chart-panel__desc chart-panel__desc--inline">
                {description}
              </Text>
            ) : null}
          </div>
        </div>
        {currentValue ? <div className="chart-panel__current-value-row"><Text strong>{currentValue}</Text></div> : null}
        {!descriptionInline && description ? (
          <Text type="secondary" className="chart-panel__desc">
            {description}
          </Text>
        ) : null}
      </div>
      <div className="chart-panel__body">
        {empty ? <Text type="secondary">{emptyMessage}</Text> : children}
      </div>
    </section>
  );
}
