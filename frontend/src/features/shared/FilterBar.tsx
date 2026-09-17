import { Button, Form } from "antd";
import type { FormInstance, FormProps } from "antd";
import type { ReactNode } from "react";

import { ResponsiveActionBar } from "./PageLayout";

export interface FilterBarProps<Values = Record<string, unknown>>
  extends Omit<FormProps<Values>, "form" | "layout"> {
  mode: "submit" | "instant";
  form?: FormInstance<Values>;
  onReset?: () => void;
  actions?: ReactNode;
  submitText?: ReactNode;
  resetText?: ReactNode;
  showReset?: boolean;
  children: ReactNode;
}

/** Visual/form shell only. Query state, URL synchronization and debouncing stay with pages. */
export function FilterBar<Values = Record<string, unknown>>({
  mode,
  form,
  onReset,
  actions,
  submitText = "筛选",
  resetText = "重置",
  showReset = true,
  className,
  children,
  ...formProps
}: FilterBarProps<Values>) {
  const [internalForm] = Form.useForm<Values>();
  const activeForm = form ?? internalForm;
  const hasActions = mode === "submit" || actions;

  return (
    <Form<Values>
      {...formProps}
      form={activeForm}
      layout="inline"
      className={`filter-bar filter-bar--${mode} ${className ?? ""}`.trim()}
    >
      <div className="filter-bar__fields">{children}</div>
      {hasActions ? (
        <ResponsiveActionBar className="filter-bar__actions">
          {actions}
          {mode === "submit" ? (
            <>
              {showReset ? (
                <Button
                  onClick={() => {
                    activeForm.resetFields();
                    onReset?.();
                  }}
                >
                  {resetText}
                </Button>
              ) : null}
              <Button type="primary" htmlType="submit">
                {submitText}
              </Button>
            </>
          ) : null}
        </ResponsiveActionBar>
      ) : null}
    </Form>
  );
}
