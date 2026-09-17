/**
 * Compatibility exports for existing pages. New pages should prefer PageLayout and
 * QueryStates directly so page structure and Card surfaces remain separate.
 */
import { Button, Result, Typography } from "antd";
import type { CardProps } from "antd";
import type { ReactNode } from "react";
import { useNavigate } from "react-router-dom";

import { ApiError } from "../../api/client";
import { PageSection } from "./PageLayout";
import { PageQueryState, QueryError } from "./QueryStates";

const { Title } = Typography;

/** Legacy page Card. Kept full-width during gradual PageScaffold migration. */
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
    <PageSection
      className="page-card"
      title={
        <Title level={3} className="layout-margin-0">
          {title}
        </Title>
      }
      extra={extra}
    >
      {children}
    </PageSection>
  );
}

/** Legacy section Card. It no longer inherits page-card width semantics. */
export function SectionCard({ className = "", ...rest }: CardProps) {
  return <PageSection {...rest} className={`section-card ${className}`.trim()} />;
}

/** Existing controlled query failure, now backed by the scoped shared state. */
export function LoadError({ onRetry }: { onRetry: () => void }) {
  return <QueryError onRetry={onRetry} />;
}

/** Detail resource absent (API 404). Primary return action matches the wildcard 404. */
export function DetailNotFound({
  title,
  backTo,
  backText,
}: {
  title: string;
  backTo: string;
  backText: string;
}) {
  const navigate = useNavigate();
  // 目的地与可访问名由调用方保持不变；navigate 回调替代 <Link><Button/></Link> 嵌套。
  return (
    <Result
      status="404"
      title={title}
      subTitle="该资源不存在或已被删除。"
      extra={
        <Button type="primary" onClick={() => navigate(backTo)}>
          {backText}
        </Button>
      }
    />
  );
}

/** Existing detail gate adapted to the page-level query boundary. */
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
  if (error instanceof ApiError && error.status === 404) {
    return <DetailNotFound title={notFoundTitle} backTo={backTo} backText={backText} />;
  }
  return (
    <PageQueryState
      initialLoading={loading}
      error={error}
      hasData={!loading && !error}
      onRetry={onRetry}
    >
      {children}
    </PageQueryState>
  );
}
