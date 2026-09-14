import { describe, expect, it, vi } from "vitest";

import { applyListFilters } from "./listFilters";

describe("applyListFilters", () => {
  it("条件变化时切换筛选并回到第一页，不额外 refetch", () => {
    const setPage = vi.fn();
    const setFilters = vi.fn();
    const refetch = vi.fn(() => Promise.resolve());

    applyListFilters({ status: "open" }, { status: "" }, 1, setPage, setFilters, refetch);

    expect(setPage).toHaveBeenCalledWith(1);
    expect(setFilters).toHaveBeenCalledWith({ status: "open" });
    expect(refetch).not.toHaveBeenCalled();
  });

  it("条件未变化且在第一页时显式刷新当前查询", () => {
    const setPage = vi.fn();
    const setFilters = vi.fn();
    const refetch = vi.fn(() => Promise.resolve());

    applyListFilters({ status: "" }, { status: "" }, 1, setPage, setFilters, refetch);

    expect(setPage).toHaveBeenCalledWith(1);
    expect(setFilters).toHaveBeenCalledWith({ status: "" });
    expect(refetch).toHaveBeenCalledTimes(1);
  });

  it("不在第一页时只重置页码，不请求旧页", () => {
    const setPage = vi.fn();
    const setFilters = vi.fn();
    const refetch = vi.fn(() => Promise.resolve());

    applyListFilters({ status: "" }, { status: "" }, 3, setPage, setFilters, refetch);

    expect(setPage).toHaveBeenCalledWith(1);
    expect(setFilters).toHaveBeenCalledWith({ status: "" });
    expect(refetch).not.toHaveBeenCalled();
  });
});
