/**
 * 总览页组件测试：状态一览四卡（MTProto/数据库/队列水位/待办）与服务信息
 * 渲染、机器人身份展示、进入页面自动检查更新（落后→升级提示/最新→绿色
 * 标签/失败→受控文案）、状态色调与待办跳转链接；旧版本后端响应缺少 join
 * 字段时回退零值不崩白。fetch 桩按 URL 路由应答——页面初始并发发起
 * overview / 版本检查 / 系统监控三个请求，到达顺序不定，不能按调用队列
 * 消费。业务统计图表仍由 /stats 承担。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { OverviewResponse, VersionCheckResponse } from "../../api/admin";
import { OverviewPage } from "./OverviewPage";

const fetchMock = vi.fn();

function overviewResponse(overrides: Partial<OverviewResponse> = {}): OverviewResponse {
  return {
    version: "dev",
    started_at: 1760000000000,
    addr: "127.0.0.1:8080",
    workers: 2,
    health: {
      store_ok: true,
      mtproto_state: "ready",
      bot_mtproto_state: "ready",
      bot_mtproto_dc_id: 4,
      db_size_bytes: 1024,
      db_path: "/data/spore.db",
      temp_dir_bytes: 2048,
      temp_dir: "/tmp/spore",
      github_configured: false,
    },
    queue: { len: 0, cap: 100 },
    bot: { id: 42, name: "Spore Bot", username: "spore_bot" },
    requests: { queued_rows: 1, processing_rows: 2 },
    users: { total: 10, enabled: 8, pending: 1, disabled: 1, archived: 0 },
    join: {
      pending: 3,
      approved: 5,
      rejected: 7,
      failed: 9,
      active_joined: 11,
      external_active: 13,
      left_total: 15,
      max_channels: 20,
      source_dist: [
        { key: "join_command", count: 2 },
        { key: "approved", count: 7 },
        { key: "external", count: 2 },
      ],
    },
    ...overrides,
  };
}

interface RouteSpec {
  payload: unknown;
  status?: number;
}

/** 按 URL 包含匹配路由应答（可重复应答同一路径，支持点击后重新查询）。 */
function stubRoutes(routes: Record<string, RouteSpec>) {
  fetchMock.mockImplementation(async (input: RequestInfo | URL) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    for (const [fragment, route] of Object.entries(routes)) {
      if (url.includes(fragment)) {
        return new Response(JSON.stringify(route.payload), {
          status: route.status ?? 200,
          headers: { "content-type": "application/json" },
        });
      }
    }
    throw new Error(`未匹配的 fetch 请求: ${url}`);
  });
}

const METRICS_FIXTURE = { range: "1d", since: 0, until: 0, sample_interval_ms: 120000, points: [] };

/** 检查更新响应 fixture。 */
function versionCheckResponse(overrides: Partial<VersionCheckResponse> = {}): VersionCheckResponse {
  return {
    current_version: "v1.0.0",
    latest_version: "v1.2.0",
    status: "outdated",
    release_url: "https://github.com/huaiminyetnotsleep/spore/releases/tag/v1.2.0",
    checked_at: 1_700_000_000_000,
    ...overrides,
  };
}

/** 总览页三个初始请求的路由表：overview 可传任意 payload（含缺字段变体），
 * versionCheck 缺省为"已是最新"（页面加载即自动检查）。 */
function overviewRoutes(
  payload: unknown = overviewResponse(),
  versionCheck: RouteSpec = { payload: versionCheckResponse({ status: "up_to_date", current_version: "dev" }) },
): Record<string, RouteSpec> {
  return {
    "/api/v1/overview": { payload },
    "/api/v1/system-metrics": { payload: METRICS_FIXTURE },
    "/api/v1/version/check": versionCheck,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      {/* antd App 上下文：message 实例来自它（生产由 App.tsx 挂载） */}
      <AntApp>
        <MemoryRouter initialEntries={["/"]}>
          <OverviewPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

describe("总览页", () => {
  beforeEach(() => {
    window.sessionStorage.clear();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("渲染状态一览四卡、服务信息与系统监控区；业务统计图表不出现在本页", async () => {
    stubRoutes(overviewRoutes());

    renderPage();

    // MTProto：就绪 → 绿色调 + 管理链接
    const mtproto = await screen.findByTestId("status-tile-mtproto");
    expect(mtproto.className).toContain("status-tile--ok");
    expect(screen.getByText("就绪")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "管理" })).toHaveAttribute("href", "/mtproto");

    // 数据库：正常 → 绿色调 + 占用大小
    const store = screen.getByTestId("status-tile-store");
    expect(store.className).toContain("status-tile--ok");
    expect(screen.getByText("正常")).toBeInTheDocument();
    expect(screen.getByText("占用 1.00 KiB")).toBeInTheDocument();

    // 队列水位：有排队/处理中记录 → 橙色调
    const queue = screen.getByTestId("status-tile-queue");
    expect(queue.className).toContain("status-tile--warn");
    expect(screen.getByText("0 / 100")).toBeInTheDocument();
    expect(screen.getByText("排队中 1 · 处理中 2（记录）")).toBeInTheDocument();

    // 待办：有待审批 → 橙色调 + 跳转链接
    const todo = screen.getByTestId("status-tile-todo");
    expect(todo.className).toContain("status-tile--warn");
    expect(screen.getByRole("link", { name: "待审批加入 3" })).toHaveAttribute(
      "href",
      "/invite-approvals",
    );
    expect(screen.getByRole("link", { name: "待审批用户 1" })).toHaveAttribute("href", "/users");

    // 服务信息（紧凑详情）
    for (const text of ["服务信息", "dev", "127.0.0.1:8080", "/data/spore.db", "（/tmp/spore）"]) {
      expect(screen.getByText(text)).toBeInTheDocument();
    }
    // Bot MTProto 会话：就绪并展示当前主 DC（数据中心，非地理位置）
    expect(screen.getByText("已连接 · DC 4")).toBeInTheDocument();
    // 机器人：展示接入机器人的 Name 与 @username
    expect(screen.getByText("机器人")).toBeInTheDocument();
    expect(screen.getByText("Spore Bot（@spore_bot）")).toBeInTheDocument();

    // 系统监控独立区块挂在服务信息之后；业务统计仍由 /stats 承担
    // （懒加载 chunk 在并行测试负载下可能超过默认 1s，显式放宽等待）
    expect(await screen.findByText("资源与传输监控", {}, { timeout: 5000 })).toBeInTheDocument();
    expect(screen.getByText("1 天")).toBeInTheDocument();
    expect(screen.queryByText("频道加入")).not.toBeInTheDocument();
    expect(screen.queryByText("已加入频道")).not.toBeInTheDocument();
  });

  it("进入页面自动检查更新，已是最新时展示绿色标签（无需点击）", async () => {
    stubRoutes(overviewRoutes());

    renderPage();

    expect(await screen.findByTestId("version-up-to-date")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "检查更新" })).toBeInTheDocument();
  });

  it("完整 commit SHA 版本号截取为 7 位短哈希展示，悬停可见完整值", async () => {
    const full = "9c2f7ab4c1e5d67890a1b2c3d4e5f60718293a4b";
    stubRoutes(overviewRoutes({ ...overviewResponse(), version: full }));

    renderPage();

    const shown = await screen.findByText("9c2f7ab");
    expect(shown).toHaveAttribute("title", full);
  });

  it("点击检查更新重新查询，落后于上游时展示有新版本标签并弹 toast", async () => {
    stubRoutes(overviewRoutes(overviewResponse(), { payload: versionCheckResponse() }));

    renderPage();

    // 自动检查已给出「有新版本」提示（蓝色标签，链接发布页）
    const hint = await screen.findByTestId("version-upgrade-hint");
    expect(hint).toBeInTheDocument();
    // 手动点击刷新按钮重查：请求带 force=1（跳过服务端缓存），结果保持，
    // 且经 toast 再次提示新版本号（标签 + toast 两处同文案）
    fireEvent.click(screen.getByRole("button", { name: "检查更新" }));
    const link = await screen.findByRole("link", { name: "有新版本 v1.2.0" });
    expect(link).toHaveAttribute(
      "href",
      "https://github.com/huaiminyetnotsleep/spore/releases/tag/v1.2.0",
    );
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noreferrer");
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("force=1"))).toBe(true);
    await waitFor(() =>
      expect(screen.getAllByText("有新版本 v1.2.0").length).toBeGreaterThanOrEqual(2),
    );
  });

  it("点击检查更新后仍是最新时展示绿色标签", async () => {
    stubRoutes(overviewRoutes());

    renderPage();
    await screen.findByTestId("version-up-to-date");
    fireEvent.click(screen.getByRole("button", { name: "检查更新" }));

    expect(await screen.findByTestId("version-up-to-date")).toBeInTheDocument();
  });

  it("dev 构建无法比较时展示上游最新版本", async () => {
    stubRoutes(
      overviewRoutes(overviewResponse(), {
        payload: versionCheckResponse({ status: "unknown", current_version: "dev" }),
      }),
    );

    renderPage();

    expect(await screen.findByText("最新 v1.2.0")).toBeInTheDocument();
  });

  it("检查更新失败时展示受控错误文案", async () => {
    stubRoutes(
      overviewRoutes(overviewResponse(), {
        status: 503,
        payload: {
          error: { code: "SERVICE_UNAVAILABLE", message: "查询最新版本失败，请检查服务器到 GitHub 的网络后重试。" },
        },
      }),
    );

    renderPage();

    expect(await screen.findByTestId("version-check-error")).toHaveTextContent(
      "查询最新版本失败，请检查服务器到 GitHub 的网络后重试。",
    );
  });

  it("旧版本后端响应缺少 join 字段时回退零值，不触发错误边界", async () => {
    const legacy = overviewResponse() as Partial<OverviewResponse>;
    delete legacy.join;
    stubRoutes(overviewRoutes(legacy));

    renderPage();

    expect(await screen.findByTestId("status-tile-todo")).toBeInTheDocument();
    // join 兜底零值：待办卡待审批加入显示 0，页面正常展示而非崩白
    expect(screen.getByRole("link", { name: "待审批加入 0" })).toBeInTheDocument();
  });

  it("Bot MTProto 会话离线时显示离线，不带 DC", async () => {
    const { health, ...rest } = overviewResponse();
    stubRoutes(
      overviewRoutes({
        ...rest,
        health: { ...health, bot_mtproto_state: "offline", bot_mtproto_dc_id: 0 },
      }),
    );

    renderPage();

    expect(await screen.findByText("Bot MTProto 会话")).toBeInTheDocument();
    expect(screen.getByText("离线")).toBeInTheDocument();
    expect(screen.queryByText(/已连接/)).not.toBeInTheDocument();
  });

  it("Bot MTProto 会话就绪但 DC 未知时显示 DC 未知", async () => {
    const { health, ...rest } = overviewResponse();
    stubRoutes(
      overviewRoutes({
        ...rest,
        health: { ...health, bot_mtproto_dc_id: 0 },
      }),
    );

    renderPage();

    expect(await screen.findByText("已连接 · DC 未知")).toBeInTheDocument();
  });

  it("旧后端未接入 Bot 会话字段时显示未接入", async () => {
    const { health, ...rest } = overviewResponse();
    const { bot_mtproto_state, bot_mtproto_dc_id, ...plainHealth } = health;
    expect(bot_mtproto_state).toBeDefined();
    expect(bot_mtproto_dc_id).toBeDefined();
    stubRoutes(
      overviewRoutes({
        ...rest,
        health: plainHealth,
      }),
    );

    renderPage();

    expect(await screen.findByText("未接入")).toBeInTheDocument();
  });

  it("响应缺少 bot 身份字段时机器人行显示未接入", async () => {
    const legacy = overviewResponse() as Partial<OverviewResponse>;
    delete legacy.bot;
    stubRoutes(overviewRoutes(legacy));

    renderPage();

    // Bot MTProto 会话就绪（fixture 默认），此处"未接入"仅来自机器人行
    expect(await screen.findByText("未接入")).toBeInTheDocument();
  });

  it("机器人无公开用户名时只显示 Name", async () => {
    const { bot, ...rest } = overviewResponse();
    expect(bot).toBeDefined();
    stubRoutes(
      overviewRoutes({ ...rest, bot: { ...bot!, username: "" } }),
    );

    renderPage();

    expect(await screen.findByText("Spore Bot")).toBeInTheDocument();
  });
});
