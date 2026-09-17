import { Tag } from "antd";
import type { ComponentProps, ReactNode } from "react";

export type StatusTone =
  | "success"
  | "warning"
  | "error"
  | "inactive"
  | "processing"
  | "default";

const toneColors: Record<StatusTone, string> = {
  success: "success",
  warning: "warning",
  error: "error",
  inactive: "default",
  processing: "processing",
  default: "default",
};

export function statusTone(tone: StatusTone): string {
  return toneColors[tone];
}

export interface StatusTagProps extends Omit<ComponentProps<typeof Tag>, "color"> {
  tone: StatusTone;
  children: ReactNode;
}

/** Semantic color only; domain modules remain responsible for mapping statuses to tones. */
export function StatusTag({ tone, children, className, ...rest }: StatusTagProps) {
  return (
    <Tag
      {...rest}
      color={statusTone(tone)}
      className={`status-tag status-tag--${tone}${className ? ` ${className}` : ""}`}
    >
      {children}
    </Tag>
  );
}
