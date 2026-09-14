import { QueryClient } from "@tanstack/react-query";

/** 管理端共享查询客户端；业务页面迁移时复用此实例。 */
export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
    },
  },
});
