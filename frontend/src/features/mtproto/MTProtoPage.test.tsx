/**
 * MTProto 连接页测试（页面迁移后新增覆盖，此前无独立单测）：
 * 唯一 H1、首次加载 gate、连接状态语义 Tag、重连的 warning 确认
 * （非 danger、Promise 感知 pending）、二维码区域与失败重试。
 * 状态与写接口以模块 mock 注入，不触网络、不轮询真实端点。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { fetchMTProtoStatus, type MTProtoStatus } from "../../api/admin";
import { clearMTProtoSession, reloginMTProto } from "../../api/mutations";
import { MTProtoPage } from "./MTProtoPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchMTProtoStatus: vi.fn(),
  };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    reloginMTProto: vi.fn(),
    clearMTProtoSession: vi.fn(),
  };
});

const fetchMTProtoStatusMock = vi.mocked(fetchMTProtoStatus);
const reloginMTProtoMock = vi.mocked(reloginMTProto);
const clearSessionMock = vi.mocked(clearMTProtoSession);

function status(overrides: Partial<MTProtoStatus> = {}): MTProtoStatus {
  return {
    state: "ready",
    updated_at: 1757000000000,
    qr_available: false,
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <App>
        <MemoryRouter>
          <MTProtoPage />
        </MemoryRouter>
      </App>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  fetchMTProtoStatusMock.mockReset();
  reloginMTProtoMock.mockReset();
  clearSessionMock.mockReset();
  vi.clearAllMocks();
});

describe("MTProto 连接页", () => {
  it("渲染唯一 H1「Telegram 连接」页面标题", async () => {
    fetchMTProtoStatusMock.mockResolvedValue(status());

    renderPage();

    expect(
      await screen.findByRole("heading", { level: 1, name: "Telegram 连接" }),
    ).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  it("首次加载完成前不渲染状态与重连控件（统一初始 loading）", async () => {
    let resolveStatus: (value: MTProtoStatus) => void = () => undefined;
    fetchMTProtoStatusMock.mockReturnValue(
      new Promise((resolve) => {
        resolveStatus = resolve;
      }),
    );

    renderPage();

    expect(screen.queryByTestId("mtproto-state")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /重连/ })).not.toBeInTheDocument();
    expect(reloginMTProtoMock).not.toHaveBeenCalled();

    resolveStatus(status());
    expect(await screen.findByTestId("mtproto-state")).toHaveTextContent("就绪");
  });

  it("连接状态使用语义 Tag：ready 已连接，offline 未连接", async () => {
    fetchMTProtoStatusMock.mockResolvedValue(status({ state: "offline", bot_state: "ready", bot_dc_id: 2 }));

    renderPage();

    expect(await screen.findByTestId("mtproto-state")).toHaveTextContent("离线");
    expect(screen.getByText("未连接")).toHaveClass("status-tag--error");
    expect(screen.getByText("已连接")).toHaveClass("status-tag--success");
    expect(screen.getByText("DC 2")).toBeInTheDocument();
  });

  it("单 bot 部署也渲染逐 bot 直传会话列表（状态与 DC 可见）", async () => {
    fetchMTProtoStatusMock.mockResolvedValue(
      status({
        bot_state: "ready",
        bot_dc_id: 4,
        bots: [{ bot_id: 42, username: "spore_bot", state: "ready", dc_id: 4, updated_at: 1757000000000 }],
      }),
    );

    renderPage();

    const list = await screen.findByTestId("mtproto-bot-list");
    expect(list).toHaveTextContent("@spore_bot");
    expect(screen.getByText("已连接 · DC 4")).toBeInTheDocument();
  });

  it("无 bots 数据时不渲染逐 bot 会话列表", async () => {
    fetchMTProtoStatusMock.mockResolvedValue(status());

    renderPage();

    await screen.findByTestId("mtproto-state");
    expect(screen.queryByTestId("mtproto-bot-list")).not.toBeInTheDocument();
  });

  it("仅离线可触发重连：确认弹层为 warning（非 danger），确认后调用 API", async () => {
    reloginMTProtoMock.mockResolvedValue({ ok: true });
    fetchMTProtoStatusMock.mockResolvedValue(status({ state: "offline" }));

    renderPage();

    const reloginButton = await screen.findByRole("button", { name: /重连 \/ 重新扫码登录/ });
    expect(reloginButton).toBeEnabled();
    fireEvent.click(reloginButton);

    // 重连是可恢复的影响运行操作：确认文案与 SSR data-confirm 一致，按钮非 danger
    expect(await screen.findByText(/登录模式将重启 MTProto 客户端，期间 Bot 暂停服务/)).toBeInTheDocument();
    expect(reloginMTProtoMock).not.toHaveBeenCalled();
    const confirmButton = screen.getByRole("button", { name: "确 认" });
    expect(confirmButton).not.toHaveClass("ant-btn-dangerous");

    fireEvent.click(confirmButton);
    await waitFor(() => expect(reloginMTProtoMock).toHaveBeenCalledTimes(1));
  });

  it("ready 状态下重连按钮禁用", async () => {
    fetchMTProtoStatusMock.mockResolvedValue(status({ state: "ready" }));

    renderPage();

    expect(await screen.findByRole("button", { name: /重连 \/ 重新扫码登录/ })).toBeDisabled();
    expect(reloginMTProtoMock).not.toHaveBeenCalled();
  });

  it("qr_available 时展示同源二维码图片", async () => {
    fetchMTProtoStatusMock.mockResolvedValue(status({ state: "offline", qr_available: true }));

    renderPage();

    const qr = await screen.findByTestId("mtproto-qr");
    expect(qr).toHaveAttribute("src", "/mtproto/qr.png?v=1757000000000");
    expect(qr).toHaveAttribute("alt", "登录二维码");
  });

  it("读取失败时展示错误与重试", async () => {
    fetchMTProtoStatusMock.mockRejectedValue(new Error("boom"));

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /重\s*试/ })).toBeInTheDocument();
    // 重试后恢复状态展示
    fetchMTProtoStatusMock.mockResolvedValue(status());
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));
    await waitFor(() => expect(screen.getByTestId("mtproto-state")).toHaveTextContent("就绪"));
  });
});

describe("MTProto 离线原因分类与清理会话", () => {
  it("banned 分类展示封禁处置文案与清理会话按钮（仅离线可用）", async () => {
    fetchMTProtoStatusMock.mockResolvedValue(
      status({ state: "offline", error_kind: "banned", last_error: "USER_DEACTIVATED_BAN" }),
    );

    renderPage();

    const alert = await screen.findByTestId("mtproto-error-kind");
    expect(alert.textContent).toContain("账号已被封禁");
    expect(alert.textContent).toContain("清理会话文件");
    const clearBtn = screen.getByRole("button", { name: "清理会话文件" });
    expect(clearBtn).toBeEnabled();
  });

  it("revoked 分类展示会话撤销文案", async () => {
    fetchMTProtoStatusMock.mockResolvedValue(
      status({ state: "offline", error_kind: "revoked" }),
    );

    renderPage();

    expect(await screen.findByTestId("mtproto-error-kind")).toHaveTextContent("会话已被撤销");
  });

  it("在线状态不展示清理会话按钮", async () => {
    fetchMTProtoStatusMock.mockResolvedValue(status({ state: "ready" }));

    renderPage();

    await screen.findByTestId("mtproto-state");
    expect(screen.queryByRole("button", { name: "清理会话文件" })).toBeDisabled();
  });

  it("清理会话经 danger 确认后调用接口", async () => {
    fetchMTProtoStatusMock.mockResolvedValue(status({ state: "offline", error_kind: "revoked" }));
    clearSessionMock.mockResolvedValue({ ok: true } as never);

    renderPage();

    const clearBtn = await screen.findByRole("button", { name: "清理会话文件" });
    fireEvent.click(clearBtn);
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));
    await waitFor(() => expect(clearSessionMock).toHaveBeenCalledTimes(1));
  });
});
