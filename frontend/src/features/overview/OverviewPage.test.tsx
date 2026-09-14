/**
 * 总览页组件测试：状态一览四卡（MTProto/数据库/队列水位/待办）与服务信息
 * 渲染、状态色调与待办跳转链接；旧版本后端响应缺少 join 字段时回退零值
 * 不崩白。经全局 fetch mock 注入响应，走真实 fetchOverview（含 join 兜底）
 * 和系统监控查询，不发起真实网络请求。业务统计图表仍由 /stats 承担。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { OverviewResponse } from "../../api/admin";
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

/** 用指定 payload 应答 /api/v1/overview（模拟后端响应，可缺字段）。 */
function stubResponse(payload: unknown) {
  fetchMock.mockImplementationOnce(async () =>
    new Response(JSON.stringify(payload), {
      status: 200,
      headers: { "content-type": "application/json" },
    }),
  );
}

function stubOverviewResponse(payload: unknown) {
  stubResponse(payload);
  stubResponse({ range: "1d", since: 0, until: 0, sample_interval_ms: 120000, points: [] });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/"]}>
        <OverviewPage />
      </MemoryRouter>
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
    stubOverviewResponse(overviewResponse());

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

    // 系统监控独立区块挂在服务信息之后；业务统计仍由 /stats 承担
    // （懒加载 chunk 在并行测试负载下可能超过默认 1s，显式放宽等待）
    expect(await screen.findByText("资源与传输监控", {}, { timeout: 5000 })).toBeInTheDocument();
    expect(screen.getByText("1 天")).toBeInTheDocument();
    expect(screen.queryByText("频道加入")).not.toBeInTheDocument();
    expect(screen.queryByText("已加入频道")).not.toBeInTheDocument();
  });

  it("旧版本后端响应缺少 join 字段时回退零值，不触发错误边界", async () => {
    const legacy = overviewResponse() as Partial<OverviewResponse>;
    delete legacy.join;
    stubOverviewResponse(legacy);

    renderPage();

    expect(await screen.findByTestId("status-tile-todo")).toBeInTheDocument();
    // join 兜底零值：待办卡待审批加入显示 0，页面正常展示而非崩白
    expect(screen.getByRole("link", { name: "待审批加入 0" })).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("Bot MTProto 会话离线时显示离线，不带 DC", async () => {
    const { health, ...rest } = overviewResponse();
    stubOverviewResponse({
      ...rest,
      health: { ...health, bot_mtproto_state: "offline", bot_mtproto_dc_id: 0 },
    });

    renderPage();

    expect(await screen.findByText("Bot MTProto 会话")).toBeInTheDocument();
    expect(screen.getByText("离线")).toBeInTheDocument();
    expect(screen.queryByText(/已连接/)).not.toBeInTheDocument();
  });

  it("Bot MTProto 会话就绪但 DC 未知时显示 DC 未知", async () => {
    const { health, ...rest } = overviewResponse();
    stubOverviewResponse({
      ...rest,
      health: { ...health, bot_mtproto_dc_id: 0 },
    });

    renderPage();

    expect(await screen.findByText("已连接 · DC 未知")).toBeInTheDocument();
  });

  it("旧后端未接入 Bot 会话字段时显示未接入", async () => {
    const { health, ...rest } = overviewResponse();
    const { bot_mtproto_state, bot_mtproto_dc_id, ...plainHealth } = health;
    expect(bot_mtproto_state).toBeDefined();
    expect(bot_mtproto_dc_id).toBeDefined();
    stubOverviewResponse({ ...rest, health: plainHealth });

    renderPage();

    expect(await screen.findByText("未接入")).toBeInTheDocument();
  });
});
