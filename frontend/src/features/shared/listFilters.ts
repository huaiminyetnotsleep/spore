import type { Dispatch, SetStateAction } from "react";

/**
 * 提交列表筛选条件：条件变化时切换 query key，条件未变化时显式刷新当前查询。
 * 当前不在第一页时只重置页码，避免同时请求旧页和新页。
 */
export function applyListFilters<T extends object>(
  nextFilters: T,
  currentFilters: T,
  page: number,
  setPage: Dispatch<SetStateAction<number>>,
  setFilters: Dispatch<SetStateAction<T>>,
  refetch: () => Promise<unknown>,
): void {
  const unchanged = JSON.stringify(nextFilters) === JSON.stringify(currentFilters);
  setPage(1);
  setFilters(nextFilters);
  if (unchanged && page === 1) {
    void refetch();
  }
}
