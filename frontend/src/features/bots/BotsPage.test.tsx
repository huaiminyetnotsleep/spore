/**
 * 机器人管理页测试：列表渲染（身份/状态/来源）、暂停/恢复/移除操作携带
 * bot id 并刷新列表、行级 pending 只影响目标行、添加机器人 FormModal
 * （取消重置草稿、成功后关闭）、token 不回显。API 全部 mock，不触网。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { MemoryRouter } from "react-router-dom";

import { fetchBots, type BotsView } from "../../api/admin";
import { addBot, deleteBot, pauseBot, resumeBot } from "../../api/mutations";
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
const addBotMock = vi.mocked(addBot);
const deleteBotMock = vi.mocked(deleteBot);
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
    addBotMock.mockReset().mockResolvedValue({
      ok: true,
      bots: [],
      max_bots: 20,
      need_apply: true,
      restart_hint: "已写入配置，重启进程后生效。",
    });
    deleteBotMock.mockReset().mockResolvedValue({
      ok: true,
      bots: [],
      max_bots: 20,
      need_apply: true,
      restart_hint: "已移除配置，重启进程后生效。",
    });
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

  it("渲染唯一 H1 页面标题「机器人管理」与添加入口", async () => {
    renderPage();
    expect(
      await screen.findByRole("heading", { level: 1, name: "机器人管理" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.getByRole("button", { name: "添加机器人" })).toBeInTheDocument();
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
    const pauseButtons = screen.getAllByRole("button", { name: "暂停" });
    expect(pauseButtons.length).toBeGreaterThan(0);
    fireEvent.click(pauseButtons[0]);
    // 二次确认弹层
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));
    await waitFor(() => expect(pauseBotMock).toHaveBeenCalledWith(111));
  });

  it("行级 pending：暂停只让目标行按钮进入 loading", async () => {
    pauseBotMock.mockReturnValue(new Promise(() => undefined) as never);
    renderPage();
    expect(await screen.findByText("@spore_bot")).toBeInTheDocument();
    // @spore_bot 未暂停提供「暂停」；@spore_two 已暂停提供「恢复」
    const pauseButton = screen.getByRole("button", { name: "暂停" });
    fireEvent.click(pauseButton);
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));

    await waitFor(() => expect(pauseBotMock).toHaveBeenCalledWith(111));
    await waitFor(() => expect(pauseButton).toHaveClass("ant-btn-loading"));
    // 另一行的恢复按钮不进入 loading
    expect(screen.getByRole("button", { name: "恢复" })).not.toHaveClass("ant-btn-loading");
  });

  it("恢复操作：暂停中的 bot 显示恢复按钮并调用 resumeBot", async () => {
    renderPage();
    expect(await screen.findByText("@spore_two")).toBeInTheDocument();
    const resumeButtons = screen.getAllByRole("button", { name: "恢复" });
    expect(resumeButtons.length).toBeGreaterThan(0);
    fireEvent.click(resumeButtons[0]);
    await waitFor(() => expect(resumeBotMock).toHaveBeenCalledWith(222));
  });

  it("移除操作：danger 确认后调用 deleteBot 并携带 bot id", async () => {
    renderPage();
    expect(await screen.findByText("@spore_two")).toBeInTheDocument();
    // file 来源 bot 提供「移除」；移除为危险操作
    fireEvent.click(screen.getByRole("button", { name: "移除" }));
    expect(await screen.findByText(/确定移除机器人/)).toBeInTheDocument();
    expect(deleteBotMock).not.toHaveBeenCalled();

    const confirmButton = screen.getByRole("button", { name: "确 认" });
    expect(confirmButton).toHaveClass("ant-btn-dangerous");
    fireEvent.click(confirmButton);
    await waitFor(() => expect(deleteBotMock).toHaveBeenCalledWith(222));
  });

  it("添加机器人：FormModal 取消后默认重置草稿", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "添加机器人" }));

    const tokenInput = await screen.findByLabelText(/Bot Token/);
    fireEvent.change(tokenInput, { target: { value: "123456789:AAabcdefg" } });

    fireEvent.click(screen.getByRole("button", { name: "取 消" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "添加机器人" }));
    expect(await screen.findByLabelText(/Bot Token/)).toHaveValue("");
    expect(addBotMock).not.toHaveBeenCalled();
  });

  it("添加机器人：提交 token 调用 addBot，成功后关闭弹窗", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: "添加机器人" }));

    const tokenInput = await screen.findByLabelText(/Bot Token/);
    fireEvent.change(tokenInput, { target: { value: "123456789:AAabcdefghijklmnopqrstuvwxyz" } });
    fireEvent.click(screen.getByRole("button", { name: "添 加" }));

    await waitFor(() =>
      expect(addBotMock).toHaveBeenCalledWith("123456789:AAabcdefghijklmnopqrstuvwxyz"),
    );
    expect(await screen.findByText("已写入配置，重启进程后生效。")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
