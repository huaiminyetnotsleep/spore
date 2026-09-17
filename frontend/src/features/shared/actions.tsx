/**
 * 管理写操作的共享交互：统一成功/失败提示、相关 query 失效与二次确认。
 * message/modal 经 antd App 上下文取实例（React 19 下静态方法不可用，
 * 实例渲染在当前组件树内，测试同样可用）；失败直接展示服务端受控
 * 中文文案（apperr 同源），任何失败都不显示成功提示。
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { QueryKey } from "@tanstack/react-query";
import { App } from "antd";
import type { ReactNode } from "react";

import { ApiError } from "../../api/client";

/** 操作失败时的兜底文案（网络层错误等无服务端文案的场景）。 */
const DEFAULT_ERROR_TEXT = "操作未能完成，请稍后重试。";

/** 提取可展示的错误文案：优先服务端受控 message。 */
export function errorText(err: unknown): string {
  return err instanceof ApiError && err.message ? err.message : DEFAULT_ERROR_TEXT;
}

export interface ActionFeedback {
  type: "success" | "warning" | "info";
  text: string;
}

type ActionFeedbackValue = string | ActionFeedback;

interface AdminActionOptions<TData, TVars> {
  /** 写请求函数（api/mutations 中的操作）。 */
  action: (vars: TVars) => Promise<TData>;
  /** 成功后按前缀失效的 query key（如 ["users"]）。 */
  invalidate?: QueryKey[];
  /** 成功反馈；字符串兼容旧调用并按 success 展示。 */
  successText?: ActionFeedbackValue | ((data: TData, vars: TVars) => ActionFeedbackValue);
  /** 成功后的附加回调（如关闭弹窗、跳转）。 */
  onDone?: (data: TData, vars: TVars) => void;
}

/**
 * 封装 TanStack Query mutation 的管理操作 hook：
 * 提交中 pending 可用于防重复点击；run() 已内部消化错误
 * （错误经 message.error 提示），调用方无需再捕获。
 * 依赖组件树中存在 antd <App> 上下文（App.tsx 已挂载）。
 */
export function useAdminAction<TData, TVars>(options: AdminActionOptions<TData, TVars>) {
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: options.action,
    onSuccess: (data, vars) => {
      const feedback =
        typeof options.successText === "function"
          ? options.successText(data, vars)
          : (options.successText ?? "操作成功。");
      if (typeof feedback === "string") {
        void message.success(feedback);
      } else {
        void message[feedback.type](feedback.text);
      }
      for (const key of options.invalidate ?? []) {
        void queryClient.invalidateQueries({ queryKey: key });
      }
      options.onDone?.(data, vars);
    },
    onError: (err) => {
      void message.error(errorText(err));
    },
  });
  return {
    pending: mutation.isPending,
    /** 执行操作；失败已提示并吞掉异常（返回 undefined）。 */
    run: (vars: TVars) => mutation.mutateAsync(vars).catch(() => undefined),
  };
}

export type ConfirmIntent = "default" | "warning" | "danger";

export interface ConfirmActionOptions {
  intent: ConfirmIntent;
  title: string;
  content: ReactNode;
  okText?: string;
  cancelText?: string;
  action: () => Promise<unknown>;
}

interface ConfirmAction {
  (options: ConfirmActionOptions): void;
  /** 兼容旧调用；迁移前继续保持 danger 样式与同步 action 支持。 */
  (content: string, onOk: () => void | Promise<unknown>): void;
}

/**
 * 二次确认 hook：新调用显式声明意图并返回 action Promise，让 Ant Design
 * 在异步操作期间保持弹窗 pending；旧的 (content, onOk) 形式暂按 danger 兼容。
 */
export function useConfirmAction(): ConfirmAction {
  const { modal } = App.useApp();
  return ((optionsOrContent: ConfirmActionOptions | string, legacyAction?: () => void | Promise<unknown>) => {
    const options: ConfirmActionOptions =
      typeof optionsOrContent === "string"
        ? {
            intent: "danger",
            title: "操作确认",
            content: optionsOrContent,
            action: async () => legacyAction?.(),
          }
        : optionsOrContent;
    modal.confirm({
      title: options.title,
      content: options.content,
      okText: options.okText ?? "确认",
      cancelText: options.cancelText ?? "取消",
      okButtonProps: options.intent === "danger" ? { danger: true } : undefined,
      icon: options.intent === "default" ? null : undefined,
      onOk: options.action,
    });
  }) as ConfirmAction;
}
