import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { controlDumpMigrate, fetchDumpMigrateItems, fetchDumpMigrateRun, fetchDumpMigrateRuns, type DumpMigrateRun } from "../../api/admin";
import { DumpMigratePage } from "./DumpMigratePage";
import { DumpMigrateRunDetailPage } from "./DumpMigrateRunDetailPage";

vi.mock("../../api/admin", async () => ({
  ...(await vi.importActual<typeof import("../../api/admin")>("../../api/admin")),
  fetchDumpMigrateRuns: vi.fn(),
  controlDumpMigrate: vi.fn(),
  fetchDumpMigrateRun: vi.fn(),
  fetchDumpMigrateItems: vi.fn(),
}));

const run: DumpMigrateRun = {
  id: 12, status: "running", from_channel_id: -100111, to: [-100222, -100333], total: 423,
  created_at: 1700000000000, updated_at: 1700000001000, finished_at: 0, last_error: "",
  done: 151, skipped: 3, failed: 2,
};

function envelope<T>(items: T[], total = items.length) {
  return { items, page: 1, page_size: 20, total, total_pages: Math.ceil(total / 20) };
}
function renderPage(entry = "/dump-migrate") {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return {
    ...render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={[entry]}>
          <AntApp component={false}>
            <Routes>
              <Route path="/dump-migrate" element={<DumpMigratePage />} />
              <Route path="/dump-migrate/runs/:id" element={<DumpMigrateRunDetailPage />} />
            </Routes>
          </AntApp>
        </MemoryRouter>
      </QueryClientProvider>,
    ),
    client,
  };
}

describe("缓存迁移页", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fetchDumpMigrateRuns).mockResolvedValue(envelope([]));
    vi.mocked(controlDumpMigrate).mockResolvedValue(run);
  });

  it("renders unique H1 and empty state linking to channel settings", async () => {
    renderPage();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.getByRole("heading", { level: 1, name: "缓存迁移" })).toBeInTheDocument();
    expect(await screen.findByText("还没有迁移记录")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "频道设置" })).toHaveAttribute("href", "/channel-settings");
    expect(screen.getByText("暂无迁移运行。")).toBeInTheDocument();
  });

  it("shows latest run progress with counts and detail link", async () => {
    vi.mocked(fetchDumpMigrateRuns).mockResolvedValue(envelope([run]));
    renderPage();
    expect(await screen.findByText("运行 #12")).toBeInTheDocument();
    expect(screen.getByText("已迁移 151 · 跳过 3 · 失败 2 · 共 423")).toBeInTheDocument();
    // 进度百分比：已处理 156/423 = 37%
    expect(document.querySelector(".ant-progress-text")?.textContent).toContain("37%");
    expect(screen.getByRole("button", { name: "查看逐条明细" })).toBeInTheDocument();
  });

  it.each([
    ["running", ["暂 停", "取 消"], ["继 续"]],
    ["paused", ["继 续", "取 消"], ["暂 停"]],
  ] as const)("controls match status %s", async (status, shown, hidden) => {
    vi.mocked(fetchDumpMigrateRuns).mockResolvedValue(envelope([{ ...run, status }]));
    renderPage();
    await screen.findByText("运行 #12");
    for (const name of shown) expect(screen.getByRole("button", { name })).toBeInTheDocument();
    for (const name of hidden) expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
  });

  it.each([
    ["暂 停", "pause", "确认暂停迁移"],
    ["继 续", "resume", "确认继续迁移"],
    ["取 消", "cancel", "确认取消迁移"],
  ] as const)("%s requires confirmation", async (label, action, title) => {
    vi.mocked(fetchDumpMigrateRuns).mockResolvedValue(envelope([{ ...run, status: action === "resume" ? "paused" : "running" }]));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: label }));
    expect((await screen.findAllByText(title)).length).toBeGreaterThan(0);
    expect(controlDumpMigrate).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));
    await waitFor(() => expect(controlDumpMigrate).toHaveBeenCalledWith(action));
  });

  it("hides controls for terminal runs and shows abort reason", async () => {
    vi.mocked(fetchDumpMigrateRuns).mockResolvedValue(
      envelope([{ ...run, status: "interrupted", finished_at: 1700000099999, last_error: "执行中断（服务重启），可重新发起继续" }]),
    );
    renderPage();
    await screen.findByText("运行 #12");
    expect(screen.getByText("结束原因：执行中断（服务重启），可重新发起继续")).toBeInTheDocument();
    for (const name of ["暂 停", "继 续", "取 消"]) {
      expect(screen.queryByRole("button", { name })).not.toBeInTheDocument();
    }
  });

  it("navigates to run detail via table row", async () => {
    vi.mocked(fetchDumpMigrateRuns).mockResolvedValue(envelope([run]));
    vi.mocked(fetchDumpMigrateRun).mockResolvedValue(run);
    vi.mocked(fetchDumpMigrateItems).mockResolvedValue(envelope([]));
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "#12" }));
    await waitFor(() => expect(screen.getByRole("heading", { level: 1, name: "迁移运行详情" })).toBeInTheDocument());
  });
});

describe("迁移运行详情页", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fetchDumpMigrateRun).mockResolvedValue(run);
    vi.mocked(fetchDumpMigrateItems).mockResolvedValue(envelope([]));
  });

  it("renders run meta and item rows with target landing ids", async () => {
    vi.mocked(fetchDumpMigrateItems).mockResolvedValue(
      envelope([
        { id: 1, run_id: 12, channel_key: "news", message_id: 101, status: "migrated", results: [{ channel_id: -100222, message_ids: [701, 702] }, { channel_id: -100333, message_ids: [801] }], error: "", created_at: 1700000005000 },
        { id: 2, run_id: 12, channel_key: "news", message_id: 102, status: "failed", results: [{ channel_id: -100222, error: "boom" }], error: "1/2 个目标频道复制失败", created_at: 1700000006000 },
      ]),
    );
    renderPage("/dump-migrate/runs/12");
    expect(await screen.findByText("源 → 目标")).toBeInTheDocument();
    expect(screen.getByText("-100111 → -100222、-100333")).toBeInTheDocument();
    expect(screen.getByText("-100222: 701, 702；-100333: 801")).toBeInTheDocument();
    expect(screen.getByText("-100222：boom")).toBeInTheDocument();
    expect(screen.getByText("1/2 个目标频道复制失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "返回缓存迁移" })).toBeInTheDocument();
  });

  it("filters items by status", async () => {
    renderPage("/dump-migrate/runs/12");
    await screen.findByText("逐条明细");
    vi.mocked(fetchDumpMigrateItems).mockResolvedValue(envelope([]));
    // antd Select：打开下拉并选「失败」
    await act(async () => {
      fireEvent.mouseDown(screen.getAllByLabelText("明细状态筛选")[0].firstElementChild!);
    });
    fireEvent.click(await screen.findByText("失败"));
    await waitFor(() =>
      expect(fetchDumpMigrateItems).toHaveBeenCalledWith(12, 1, 20, "failed"),
    );
  });

  it("shows not-found gate for invalid id", () => {
    renderPage("/dump-migrate/runs/not-an-id");
    expect(screen.getByText("迁移运行不存在")).toBeInTheDocument();
    expect(fetchDumpMigrateRun).not.toHaveBeenCalled();
  });
});
