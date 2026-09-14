/**
 * 系统资源监控区测试：时间范围默认值、同一标签页内的选择记忆与图表顺序。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SystemMetricsSection } from "./SystemMetricsSection";

const fetchMock = vi.fn();

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rendered = render(
    <QueryClientProvider client={client}>
      <SystemMetricsSection />
    </QueryClientProvider>,
  );
  return { ...rendered, client };
}

function requestedRange(range: string): boolean {
  return fetchMock.mock.calls.some(([path]) => String(path).includes(`range=${range}`));
}

describe("系统资源与传输监控", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
    fetchMock.mockReset();
    fetchMock.mockImplementation(async (path: string) => {
      const range = new URL(path, "http://localhost").searchParams.get("range") ?? "1d";
      return new Response(JSON.stringify({
        range,
        since: 0,
        until: 0,
        sample_interval_ms: 120000,
        points: [],
      }), {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    });
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("首次进入默认查询 1 天，并按两行布局顺序展示四项监控", async () => {
    const { container } = renderSection();

    await waitFor(() => expect(container.querySelector(".system-metrics-grid")).not.toBeNull());
    const grid = container.querySelector(".system-metrics-grid") as HTMLElement;
    expect(within(grid).getAllByRole("heading", { level: 5 }).map((heading) => heading.textContent)).toEqual([
      "进程 CPU",
      "进程内存",
      "临时目录大小",
      "传输速率",
    ]);
  });

  it("切换到实时后重新进入页面仍保留实时范围", async () => {
    const first = renderSection();
    await waitFor(() => expect(requestedRange("1d")).toBe(true));

    fireEvent.click(screen.getByText("实时"));
    await waitFor(() => expect(requestedRange("realtime")).toBe(true));
    expect(window.sessionStorage.getItem("spore:overview:system-metrics-range")).toBe("realtime");

    first.unmount();
    first.client.clear();
    fetchMock.mockClear();

    renderSection();
    await waitFor(() => expect(requestedRange("realtime")).toBe(true));
    expect(requestedRange("1d")).toBe(false);
  });

  it("缓存中的未知范围回退到 1 天", async () => {
    window.sessionStorage.setItem("spore:overview:system-metrics-range", "unknown");

    renderSection();

    await waitFor(() => expect(requestedRange("1d")).toBe(true));
  });
});
