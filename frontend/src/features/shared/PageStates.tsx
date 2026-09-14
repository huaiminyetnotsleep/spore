/**
 * 只读页面的共享外壳与状态组件：页卡片、加载态、空态与错误态。
 * 列表/详情页统一复用，避免每个 feature 复制状态处理。
 */
import { Alert, Button, Card, Result, Spin, Typography } from "antd";
import type { CardProps } from "antd";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";

import { ApiError } from "../../api/client";

const { Title } = Typography;

/** 页面卡片：统一标题与右上角扩展区（如 CSV 导出）；底板复用 SectionCard。 */
export function PageCard({
  title,
  extra,
  children,
}: {
  title: string;
  extra?: ReactNode;
  children: ReactNode;
}) {
  return (
    <SectionCard
      title={
        <Title level={3} className="layout-margin-0">
          {title}
        </Title>
      }
      extra={extra}
    >
      {children}
    </SectionCard>
  );
}

/**
 * 页面分区卡：页面内分组内容（设置区块、图表表格、状态说明等）的统一全宽底板，
 * 铺满内容区域并保持 page-card 视觉；PageCard 也基于它构建。需要人为收窄的
 * 卡片（如 MTProto 页的 320px 窄卡）通过 className 叠加宽度类覆盖。
 */
export function SectionCard({ className = "", ...rest }: CardProps) {
  return <Card {...rest} className={`page-card section-card ${className}`} />;
}

/** 列表/详情查询失败态：受控文案 + 重试（错误细节只留在控制台）。 */
export function LoadError({ onRetry }: { onRetry: () => void }) {
  return (
    <Alert
      type="error"
      showIcon
      message="数据加载失败"
      description="请求管理端数据失败，请确认登录状态后重试。"
      action={
        <Button size="small" onClick={onRetry}>
          重试
        </Button>
      }
      data-testid="page-error"
    />
  );
}

/** 详情资源不存在（API 404）→ 独立空结果态。 */
export function DetailNotFound({
  title,
  backTo,
  backText,
}: {
  title: string;
  backTo: string;
  backText: string;
}) {
  return (
    <Result
      status="404"
      title={title}
      subTitle="该资源不存在或已被删除。"
      extra={
        <Link to={backTo}>
          <Button type="primary">{backText}</Button>
        </Link>
      }
    />
  );
}

/** 详情页查询的整体状态分发：加载 / 404 / 错误 / 正常渲染。 */
export function DetailGate({
  loading,
  error,
  onRetry,
  notFoundTitle,
  backTo,
  backText,
  children,
}: {
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  notFoundTitle: string;
  backTo: string;
  backText: string;
  children: ReactNode;
}) {
  if (loading) {
    return <Spin className="page-loading" tip="加载中…" />;
  }
  if (error instanceof ApiError && error.status === 404) {
    return <DetailNotFound title={notFoundTitle} backTo={backTo} backText={backText} />;
  }
  if (error) {
    return <LoadError onRetry={onRetry} />;
  }
  return <>{children}</>;
}
