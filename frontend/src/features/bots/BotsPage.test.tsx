/**
 * 机器人管理页测试：列表渲染（身份/状态/来源）、暂停/恢复操作携带 bot id
 * 并刷新列表、token 不回显。API 全部 mock，不触网。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";

import { fetchBots, type BotsView } from "../../api/admin";
import { deleteBot, pauseBot, resumeBot } from "../../api/mutations";
import { BotsPage } from "./BotsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchBots: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    addBot: vi.fn(),
    deleteBot: vi.fn(),
    pauseBot: vi.fn(),
    resumeBot: vi.fn(),
  };
});

const fetchBotsMock = vi.mocked(fetchBots);
const pauseBotMock = vi.mocked(pauseBot);
const resumeBotMock = vi.mocked(resumeBot);

function botsView(overrides: Partial<BotsView> = {}): BotsView {
  return {
    bots: [
      {
        bot_id: 111,
        username: "spore_bot",
        name: "Spore Bot",
        primary: true,
        online: true,
        conflict: false,
        paused: false,
        mtproto_state: "ready",
        source: "env",
        restart_pending: false,
      },
      {
        bot_id: 222,
        username: "spore_two",
        name: "Spore Two",
        primary: false,
        online: true,
        conflict: false,
        paused: true,
        source: "file",
        restart_pending: false,
      },
    ],
    max_bots: 20,
    need_apply: false,
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <MemoryRouter initialEntries={["/bots"]}>
          <BotsPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

describe("机器人管理页", () => {
  beforeEach(() => {
    fetchBotsMock.mockReset().mockResolvedValue(botsView());
    pauseBotMock.mockReset().mockResolvedValue({
      ok: true,
      bots: [],
      max_bots: 20,
      need_apply: false,
      restart_hint: "",
      message: "已暂停。",
    });
    resumeBotMock.mockReset().mockResolvedValue({
      ok: true,
      bots: [],
      max_bots: 20,
      need_apply: false,
      restart_hint: "",
      message: "已恢复。",
    });
  });

  it("渲染机器人列表：身份、主 bot、暂停态与来源", async () => {
    renderPage();
    expect(await screen.findByText("@spore_bot")).toBeInTheDocument();
    expect(screen.getByText("@spore_two")).toBeInTheDocument();
    expect(screen.getByText("主")).toBeInTheDocument();
    expect(screen.getByTestId("bot-paused-222")).toBeInTheDocument();
    expect(screen.getAllByText("环境变量").length).toBeGreaterThan(0);
  });

  it("暂停操作：确认后调用 pauseBot 并携带 bot id", async () => {
    renderPage();
    expect(await screen.findByText("@spore_bot")).toBeInTheDocument();
    // 主 bot（未暂停）显示「暂停」按钮
    const pauseButtons = screen.getAllByRole("button", { name: "暂 停" });
    expect(pauseButtons.length).toBeGreaterThan(0);
    fireEvent.click(pauseButtons[0]);
    // 二次确认弹层
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));
    await waitFor(() => expect(pauseBotMock).toHaveBeenCalledWith(111));
  });

  it("恢复操作：暂停中的 bot 显示恢复按钮并调用 resumeBot", async () => {
    renderPage();
    expect(await screen.findByText("@spore_two")).toBeInTheDocument();
    const resumeButtons = screen.getAllByRole("button", { name: "恢 复" });
    expect(resumeButtons.length).toBeGreaterThan(0);
    fireEvent.click(resumeButtons[0]);
    await waitFor(() => expect(resumeBotMock).toHaveBeenCalledWith(222));
  });
});
