import type { HTMLAttributes, ReactNode } from "react";

import { ResponsiveActionBar } from "./PageLayout";

export interface FormActionsProps extends HTMLAttributes<HTMLDivElement> {
  children: ReactNode;
}

export function FormActions({ className, children, ...rest }: FormActionsProps) {
  return (
    <ResponsiveActionBar
      {...rest}
      align="end"
      className={`form-actions ${className ?? ""}`.trim()}
    >
      {children}
    </ResponsiveActionBar>
  );
}
