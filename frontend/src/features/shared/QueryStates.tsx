import { Alert, Button, Empty, Spin, Typography } from "antd";
import type { ReactNode } from "react";

const { Text } = Typography;

export interface EmptyStateProps {
  description: ReactNode;
  action?: ReactNode;
  compact?: boolean;
}

export function EmptyState({ description, action, compact = false }: EmptyStateProps) {
  return (
    <Empty
      image={compact ? Empty.PRESENTED_IMAGE_SIMPLE : undefined}
      description={description}
      className={compact ? "empty-state empty-state--compact" : "empty-state"}
    >
      {action}
    </Empty>
  );
}

export interface QueryErrorProps {
  onRetry?: () => void;
  title?: ReactNode;
  description?: ReactNode;
  compact?: boolean;
}

export function QueryError({
  onRetry,
  title = "数据加载失败",
  description,
  compact = false,
}: QueryErrorProps) {
  const visibleDescription =
    description ?? (compact ? undefined : "请求管理端数据失败，请确认登录状态后重试。");

  return (
    <Alert
      type="error"
      showIcon
      message={title}
      description={visibleDescription}
      action={
        onRetry ? (
          <Button size="small" onClick={onRetry}>
            重试
          </Button>
        ) : undefined
      }
      data-testid="page-error"
    />
  );
}

interface QueryStateProps {
  initialLoading?: boolean;
  refreshing?: boolean;
  error?: unknown;
  hasData?: boolean;
  onRetry?: () => void;
  empty?: boolean;
  emptyDescription?: ReactNode;
  emptyAction?: ReactNode;
  loadingText?: ReactNode;
  children?: ReactNode;
}

function QueryState({
  scope,
  initialLoading = false,
  refreshing = false,
  error,
  hasData = false,
  onRetry,
  empty = false,
  emptyDescription = "暂无数据。",
  emptyAction,
  loadingText = "加载中…",
  children,
}: QueryStateProps & { scope: "page" | "section" }) {
  if (initialLoading && !hasData) {
    return (
      <Spin className={`${scope}-loading`} tip={loadingText}>
        <div className="query-state__loading-placeholder" />
      </Spin>
    );
  }
  if (error && !hasData) {
    return <QueryError onRetry={onRetry} />;
  }
  if (empty) {
    return <EmptyState description={emptyDescription} action={emptyAction} />;
  }

  return (
    <div className={`query-state query-state--${scope}`} aria-busy={refreshing || undefined}>
      {error ? <QueryError onRetry={onRetry} compact /> : null}
      <Spin spinning={refreshing}>{children}</Spin>
    </div>
  );
}

export function PageQueryState(props: QueryStateProps) {
  return <QueryState {...props} scope="page" />;
}

export function SectionQueryState(props: QueryStateProps) {
  return <QueryState {...props} scope="section" />;
}

export type TableQueryStateProps =
  | { state: "loading"; text?: ReactNode }
  | { state: "empty"; description?: ReactNode; action?: ReactNode }
  | { state: "error"; onRetry?: () => void; description?: ReactNode };

/** Suitable for Ant Design Table locale.emptyText or a table-sized scoped failure. */
export function TableQueryState(props: TableQueryStateProps) {
  if (props.state === "loading") {
    return (
      <Spin size="small" tip={props.text ?? "加载中…"}>
        <div className="query-state__loading-placeholder" />
      </Spin>
    );
  }
  if (props.state === "error") {
    return (
      <QueryError
        compact
        onRetry={props.onRetry}
        title="表格数据加载失败"
        description={props.description}
      />
    );
  }
  return <EmptyState compact description={props.description ?? "暂无数据。"} action={props.action} />;
}

export interface InlineQueryStateProps {
  pending?: boolean;
  error?: unknown;
  text?: ReactNode;
  errorText?: ReactNode;
}

export function InlineQueryState({
  pending = false,
  error,
  text,
  errorText = "操作未能完成。",
}: InlineQueryStateProps) {
  if (error) {
    return <Text type="danger">{errorText}</Text>;
  }
  if (pending) {
    return (
      <span className="inline-query-state" aria-live="polite">
        <Spin size="small" />
        {text ? <Text type="secondary">{text}</Text> : null}
      </span>
    );
  }
  return text ? <Text type="secondary">{text}</Text> : null;
}

export const InlineMutationState = InlineQueryState;
