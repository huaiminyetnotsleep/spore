import { Card, Typography } from "antd";
import type { CardProps } from "antd";
import type { HTMLAttributes, ReactNode } from "react";

const { Paragraph, Title } = Typography;

function joinClassNames(...names: Array<string | undefined | false>): string {
  return names.filter(Boolean).join(" ");
}

export interface ResponsiveActionBarProps extends HTMLAttributes<HTMLDivElement> {
  align?: "start" | "end" | "between";
  children: ReactNode;
}

/** Responsive layout only; button intent and business behavior stay with callers. */
export function ResponsiveActionBar({
  align = "end",
  className,
  children,
  ...rest
}: ResponsiveActionBarProps) {
  return (
    <div
      {...rest}
      className={joinClassNames(
        "responsive-action-bar",
        `responsive-action-bar--${align}`,
        className,
      )}
    >
      {children}
    </div>
  );
}

export const PageActions = ResponsiveActionBar;

export interface PageScaffoldProps extends Omit<HTMLAttributes<HTMLDivElement>, "title"> {
  title: ReactNode;
  description?: ReactNode;
  status?: ReactNode;
  actions?: ReactNode;
  children: ReactNode;
}

/** Page structure without an implicit Card surface. */
export function PageScaffold({
  title,
  description,
  status,
  actions,
  className,
  children,
  ...rest
}: PageScaffoldProps) {
  return (
    <main {...rest} className={joinClassNames("page-scaffold", className)}>
      <header className="page-scaffold__header">
        <div className="page-scaffold__heading">
          <div className="page-scaffold__title-row">
            <Title level={1} className="page-scaffold__title">
              {title}
            </Title>
            {status ? <div className="page-scaffold__status">{status}</div> : null}
          </div>
          {description ? (
            <Paragraph type="secondary" className="page-scaffold__description">
              {description}
            </Paragraph>
          ) : null}
        </div>
        {actions ? <PageActions className="page-scaffold__actions">{actions}</PageActions> : null}
      </header>
      <div className="page-scaffold__content">{children}</div>
    </main>
  );
}

export interface PageSectionProps extends CardProps {
  description?: ReactNode;
}

/** Card-backed content section used only where a visible surface boundary is useful. */
export function PageSection({ title, description, className, children, ...rest }: PageSectionProps) {
  const sectionTitle =
    title || description ? (
      <div className="page-section__heading">
        {title ? <div className="page-section__title">{title}</div> : null}
        {description ? (
          <Typography.Text type="secondary" className="page-section__description">
            {description}
          </Typography.Text>
        ) : null}
      </div>
    ) : undefined;

  return (
    <Card {...rest} title={sectionTitle} className={joinClassNames("page-section", className)}>
      {children}
    </Card>
  );
}
