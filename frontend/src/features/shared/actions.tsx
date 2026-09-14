/**
 * 管理写操作的共享交互：统一成功/失败提示、相关 query 失效与二次确认。
 * message/modal 经 antd App 上下文取实例（React 19 下静态方法不可用，
 * 实例渲染在当前组件树内，测试同样可用）；失败直接展示服务端受控
 * 中文文案（apperr 同源），任何失败都不显示成功提示。
 */
import { useMutation, useQueryClient } from "@tanstack/react-query";
import type { QueryKey } from "@tanstack/react-query";
import { App } from "antd";

import { ApiError } from "../../api/client";

/** 操作失败时的兜底文案（网络层错误等无服务端文案的场景）。 */
const DEFAULT_ERROR_TEXT = "操作未能完成，请稍后重试。";

/** 提取可展示的错误文案：优先服务端受控 message。 */
export function errorText(err: unknown): string {
  return err instanceof ApiError && err.message ? err.message : DEFAULT_ERROR_TEXT;
}

interface AdminActionOptions<TData, TVars> {
  /** 写请求函数（api/mutations 中的操作）。 */
  action: (vars: TVars) => Promise<TData>;
  /** 成功后按前缀失效的 query key（如 ["users"]）。 */
  invalidate?: QueryKey[];
  /** 成功提示；缺省"操作成功。"。 */
  successText?: string | ((data: TData, vars: TVars) => string);
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
      const text =
        typeof options.successText === "function"
          ? options.successText(data, vars)
          : (options.successText ?? "操作成功。");
      void message.success(text);
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

/**
 * 破坏性/不可逆操作的二次确认 hook（content 与 SSR data-confirm 文案一致）。
 * 返回函数在用户点击"确认"后执行 onOk；确认期间弹窗自动进入加载态。
 */
export function useConfirmAction() {
  const { modal } = App.useApp();
  return (content: string, onOk: () => void) => {
    modal.confirm({
      title: "操作确认",
      content,
      okText: "确认",
      cancelText: "取消",
      okButtonProps: { danger: true },
      onOk: () => onOk(),
    });
  };
}
