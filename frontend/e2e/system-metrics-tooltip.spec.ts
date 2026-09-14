import { expect, test, type Locator, type Page, type Route } from "@playwright/test";

/**
 * 实时监控图表 tooltip 固定的回归验收：
 * 数据轮询触发 G2 整图重渲染时，悬停中的 tooltip 必须保持在原时间点并展示
 * 新数据；移动到其他点跟随，移出图表后隐藏且不得被后续刷新复活。
 */

const sessionFixture = {
  authenticated: true,
  user: { name: "admin" },
  csrf_token: "e2e-csrf-fixture",
  expires_at: 1_900_000_000_000,
};

const overviewFixture = {
  version: "e2e",
  started_at: 1_700_000_000_000,
  addr: "127.0.0.1:8080",
  workers: 1,
  health: {
    store_ok: true,
    mtproto_state: "unknown",
    bot_mtproto_state: "ready",
    bot_mtproto_dc_id: 4,
    db_size_bytes: 1024,
    db_path: "data/spore.db",
    temp_dir_bytes: 0,
    temp_dir: "data/tmp",
    github_configured: false,
  },
  queue: { len: 0, cap: 8 },
  requests: { queued_rows: 0, processing_rows: 0 },
  users: { total: 1, enabled: 1, pending: 0, disabled: 0, archived: 0 },
  join: {
    pending: 0,
    approved: 0,
    rejected: 0,
    failed: 0,
    active_joined: 0,
    external_active: 0,
    left_total: 0,
    max_channels: 20,
    source_dist: [],
  },
};

async function fulfillJSON(route: Route, status: number, body: unknown) {
  await route.fulfill({
    status,
    headers: {
      "content-type": "application/json; charset=utf-8",
      "cache-control": "no-store",
    },
    body: JSON.stringify(body),
  });
}

/**
 * system-metrics 替身：窗口随调用时间滑动，且同一时间点的数值每次轮询都
 * 变化（tick 抖动），用于断言 tooltip 在原 x 上展示的是新数据。
 */
function systemMetricsFixture() {
  let tick = 0;
  let cursor = Date.now();
  return () => {
    tick += 1;
    cursor += 2000;
    const n = 40;
    const points = Array.from({ length: n }, (_, i) => {
      const at = cursor - (n - 1 - i) * 2000;
      return {
        at,
        rss_bytes:
          220 * 1024 * 1024 + i * 1.5 * 1024 * 1024 + Math.sin(i / 3 + tick) * 8 * 1024 * 1024 + tick * 2 * 1024 * 1024,
        temp_dir_bytes: 1024 * 1024 * 1024 + Math.cos(i / 4 + tick) * 50 * 1024 * 1024,
        download_bytes_per_second: Math.max(0, 2 * 1024 * 1024 + Math.sin(i / 2 + tick) * 1024 * 1024),
        upload_bytes_per_second: Math.max(0, 512 * 1024 + Math.cos(i / 2 + tick) * 300 * 1024),
      };
    });
    return { range: "realtime", since: cursor - n * 2000, until: cursor, sample_interval_ms: 2000, points };
  };
}

async function mockMetricsAPI(page: Page) {
  const metrics = systemMetricsFixture();
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/v1/session" && request.method() === "GET") {
      await fulfillJSON(route, 200, sessionFixture);
      return;
    }
    if (path === "/api/v1/overview" && request.method() === "GET") {
      await fulfillJSON(route, 200, overviewFixture);
      return;
    }
    if (path === "/api/v1/system-metrics" && request.method() === "GET") {
      await fulfillJSON(route, 200, metrics());
      return;
    }
    await fulfillJSON(route, 404, { error: { code: "NOT_FOUND", message: "资源不存在或已被删除。" } });
  });
}

interface TooltipSnapshot {
  count: number;
  title: string;
  value: string;
}

async function readTooltip(chart: Locator): Promise<TooltipSnapshot> {
  return chart.locator(".g2-tooltip").evaluateAll((nodes) => {
    if (nodes.length === 0) return { count: 0, title: "", value: "" };
    const el = nodes[0] as HTMLElement;
    return {
      count: nodes.length,
      title: el.querySelector(".g2-tooltip-title")?.textContent?.trim() ?? "",
      value: el.querySelector(".g2-tooltip-list")?.textContent?.replace(/\s+/g, " ").trim() ?? "",
    };
  });
}

test.describe("资源与传输监控", () => {
  test("保留时间范围选择，并在宽屏双列、窄屏单列展示", async ({ page }) => {
    await mockMetricsAPI(page);
    await page.goto("/admin");

    const grid = page.locator(".system-metrics-grid");
    await expect(page.getByRole("heading", { name: "资源与传输监控" })).toBeVisible();
    await expect(grid.locator(".chart-panel h5")).toHaveText([
      "进程 CPU",
      "进程内存",
      "临时目录大小",
      "传输速率",
    ]);
    await expect.poll(() => grid.evaluate((element) => getComputedStyle(element).gridTemplateColumns.split(" ").length)).toBe(2);

    const range = page.getByRole("radiogroup");
    await range.getByText("实时", { exact: true }).click();
    await expect(range.locator(".ant-segmented-item-selected")).toHaveText("实时");

    await page.getByRole("link", { name: "业务统计" }).click();
    await expect(page).toHaveURL(/\/admin\/stats$/);
    await page.getByRole("link", { name: "总览" }).click();
    await expect(page.getByRole("heading", { name: "资源与传输监控" })).toBeVisible();
    await expect(page.getByRole("radiogroup").locator(".ant-segmented-item-selected")).toHaveText("实时");

    await page.setViewportSize({ width: 390, height: 844 });
    await expect.poll(() => grid.evaluate((element) => getComputedStyle(element).gridTemplateColumns.split(" ").length)).toBe(1);
  });

  test("轮询刷新不隐藏悬停 tooltip，移出后不复活", async ({ page }) => {
    await mockMetricsAPI(page);
    await page.goto("/admin");
    await expect(page.getByRole("heading", { name: "资源与传输监控" })).toBeVisible();

    // 切到实时（2s 轮询），等待首个图表渲染并滚入视口中部
    await page.getByRole("radiogroup").getByText("实时", { exact: true }).click();
    const chart = page.locator(".system-metrics-chart").first();
    const canvas = chart.locator("canvas").first();
    await expect(canvas).toBeVisible();
    await chart.evaluate((el) => el.scrollIntoView({ block: "center" }));
    // 等 range 切换引发的查询与重渲染稳定，避免悬停事件落在交互重建窗口里
    await page.waitForTimeout(1500);
    const box = await canvas.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.y).toBeGreaterThan(0);
    expect(box!.y + box!.height).toBeLessThan(page.viewportSize()!.height);

    // 悬停在图表 70% 位置（较新的时间点）；模拟真实鼠标的连续小幅移动
    const hoverX = box!.x + box!.width * 0.7;
    const hoverY = box!.y + box!.height * 0.5;
    for (let i = 0; i < 5; i++) {
      await page.mouse.move(hoverX + i * 7, hoverY + (i % 2) * 5);
      await page.waitForTimeout(300);
    }
    await expect
      .poll(async () => (await readTooltip(chart)).count, { timeout: 3_000 })
      .toBe(1);
    const shown = await readTooltip(chart);
    expect(shown.title).not.toBe("");
    expect(shown.value).toContain("RSS");

    // 指针保持不动，跨越至少 3 个轮询周期：tooltip 必须一直存在，
    // 且数值至少变化一次（说明 tooltip 内容在随新数据重建，而非僵死）
    const samples: TooltipSnapshot[] = [];
    for (let i = 0; i < 10; i++) {
      await page.waitForTimeout(700);
      samples.push(await readTooltip(chart));
    }
    expect(samples.map((s) => s.count)).toEqual(Array.from({ length: samples.length }, () => 1));
    expect(new Set(samples.map((s) => s.title))).toEqual(new Set([shown.title]));
    expect(new Set(samples.map((s) => s.value)).size).toBeGreaterThan(1);

    // 移动到另一个时间点：tooltip 跟随到新位置
    await page.mouse.move(box!.x + box!.width * 0.7, box!.y + box!.height * 0.5);
    await expect
      .poll(async () => (await readTooltip(chart)).title, { timeout: 3_000 })
      .not.toBe(shown.title);

    // 移出图表：tooltip 隐藏，且跨多个轮询周期不得复活
    await page.mouse.move(8, 8);
    await expect
      .poll(async () => (await readTooltip(chart)).count, { timeout: 3_000 })
      .toBe(0);
    for (let i = 0; i < 5; i++) {
      await page.waitForTimeout(700);
    }
    expect(await readTooltip(chart)).toMatchObject({ count: 0 });
  });
});
