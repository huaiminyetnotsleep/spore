import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  fetchNotificationConfig,
  type NotificationConfigView,
} from "../../api/admin";
import { ApiError } from "../../api/client";
import {
  fetchNotificationBotChatID,
  saveNotificationConfig,
  testNotification,
} from "../../api/mutations";
import { NotificationSettingsPage } from "./NotificationSettingsPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return { ...actual, fetchNotificationConfig: vi.fn() };
});
vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>(
    "../../api/mutations",
  );
  return {
    ...actual,
    fetchNotificationBotChatID: vi.fn(),
    saveNotificationConfig: vi.fn(),
    testNotification: vi.fn(),
  };
});

const fetchConfigMock = vi.mocked(fetchNotificationConfig);
const fetchChatIDMock = vi.mocked(fetchNotificationBotChatID);
const saveConfigMock = vi.mocked(saveNotificationConfig);
const testNotificationMock = vi.mocked(testNotification);

function configView(overrides: Partial<NotificationConfigView> = {}): NotificationConfigView {
  return {
    version: 1,
    bot: {
      enabled: true,
      chat_id: "-1001234567890",
      has_token: true,
      credential: { available: true },
    },
    webhook: {
      enabled: true,
      format: "feishu",
      has_url: true,
      has_secret: true,
      credential: { available: true },
      feishu_open_ids: ["ou_saved"],
      feishu_at_all: false,
    },
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AntApp>
        <MemoryRouter>
          <NotificationSettingsPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

async function openWebhookTab() {
  fireEvent.click(await screen.findByRole("tab", { name: "Webhook" }));
  await screen.findByRole("combobox", { name: "Webhook 格式" });
}

async function selectWebhookFormat(name: string) {
  const selector = screen.getByRole("combobox", { name: "Webhook 格式" });
  fireEvent.mouseDown(selector);
  fireEvent.click(await screen.findByRole("option", { name }));
}

afterEach(() => {
  fetchConfigMock.mockReset();
  fetchChatIDMock.mockReset();
  saveConfigMock.mockReset();
  testNotificationMock.mockReset();
  vi.clearAllMocks();
});

describe("通知设置页", () => {
  it("首次数据到达前 gate 住保存和测试控件", async () => {
    let resolveConfig: (value: NotificationConfigView) => void = () => undefined;
    fetchConfigMock.mockReturnValue(
      new Promise((resolve) => {
        resolveConfig = resolve;
      }),
    );

    renderPage();

    expect(screen.queryByRole("button", { name: "保存通知配置" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "测试 Bot" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Webhook" })).not.toBeInTheDocument();

    resolveConfig(configView());
    expect(await screen.findByRole("button", { name: "保存通知配置" })).toBeInTheDocument();
  });

  it("敏感字段不回填明文，并按凭据状态显示留空沿用占位", async () => {
    fetchConfigMock.mockResolvedValue(configView());

    renderPage();

    expect(await screen.findByLabelText("Bot Token")).toHaveValue("");
    expect(screen.getByPlaceholderText("已保存，留空则沿用")).toBeInTheDocument();
    await openWebhookTab();
    expect(screen.getAllByPlaceholderText("已保存，留空则沿用").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByLabelText("Webhook URL")).toHaveValue("");
    expect(screen.getByLabelText("签名密钥")).toHaveValue("");
  });

  it("格式切换由描述符展示平台专属字段与 @ 模式", async () => {
    fetchConfigMock.mockResolvedValue(configView());

    renderPage();
    await openWebhookTab();

    expect(screen.getByRole("radio", { name: "指定用户" })).toBeChecked();
    expect(screen.getByLabelText("飞书 open_id")).toHaveValue("ou_saved");

    await selectWebhookFormat("钉钉");
    expect(screen.getByText(/钉钉自定义机器人消息/)).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "指定手机号" })).toBeInTheDocument();
    expect(screen.queryByLabelText("飞书 open_id")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("radio", { name: "指定手机号" }));
    expect(await screen.findByLabelText("提醒手机号")).toBeInTheDocument();

    await selectWebhookFormat("Discord");
    expect(screen.queryByLabelText("加签密钥")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("radio", { name: "指定角色" }));
    expect(await screen.findByLabelText("Discord 角色 ID")).toBeInTheDocument();

    await selectWebhookFormat("通用 JSON");
    expect(screen.getByLabelText("签名请求头")).toBeInTheDocument();
    expect(screen.getByLabelText("HMAC 密钥")).toBeInTheDocument();
    expect(screen.queryByText("@ 提醒模式")).not.toBeInTheDocument();
  });

  it("保存提交完整 Bot 与当前 Webhook 格式 body，敏感空值保留沿用语义", async () => {
    fetchConfigMock.mockResolvedValue(configView());
    saveConfigMock.mockResolvedValue({ ok: true, message: "通知配置已保存。" });

    renderPage();

    fireEvent.change(await screen.findByLabelText("Bot Token"), {
      target: { value: "new-bot-token" },
    });
    fireEvent.change(screen.getByLabelText("Chat ID"), {
      target: { value: " -1009988 " },
    });
    await openWebhookTab();
    await selectWebhookFormat("Discord");
    fireEvent.change(screen.getByLabelText("Webhook URL"), {
      target: { value: " https://discord.com/api/webhooks/123/secret " },
    });
    fireEvent.click(screen.getByRole("radio", { name: "指定用户" }));
    fireEvent.change(await screen.findByLabelText("Discord 用户 ID"), {
      target: { value: " 111, 222 " },
    });
    fireEvent.click(screen.getByRole("button", { name: "保存通知配置" }));

    await waitFor(() =>
      expect(saveConfigMock).toHaveBeenCalledWith({
        bot: {
          enabled: true,
          token: "new-bot-token",
          chat_id: "-1009988",
        },
        webhook: {
          enabled: true,
          format: "discord",
          url: "https://discord.com/api/webhooks/123/secret",
          secret: "",
          discord_user_ids: ["111", "222"],
          discord_role_ids: [],
          discord_everyone: false,
        },
      }),
    );
  });

  it("Bot/Webhook 测试只提交通道名，明确使用已保存配置", async () => {
    fetchConfigMock.mockResolvedValue(configView());
    testNotificationMock.mockResolvedValue({ ok: true, message: "测试消息已发送。" });

    renderPage();

    fireEvent.change(await screen.findByLabelText("Bot Token"), {
      target: { value: "unsaved-token" },
    });
    fireEvent.click(screen.getByRole("button", { name: "测试 Bot" }));
    await waitFor(() => expect(testNotificationMock).toHaveBeenCalledWith("bot"));

    await openWebhookTab();
    expect(screen.getByText(/测试发送明确使用服务端已保存的 Webhook 配置/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "测试 Webhook" }));
    await waitFor(() => expect(testNotificationMock).toHaveBeenCalledWith("webhook"));
  });

  it("获取最近 Chat ID 后回填表单", async () => {
    fetchConfigMock.mockResolvedValue(configView());
    fetchChatIDMock.mockResolvedValue({
      ok: true,
      message: "已获取最近会话 Chat ID。",
      chat_id: "-100777",
    });

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "获取 Chat ID" }));
    await waitFor(() => expect(fetchChatIDMock).toHaveBeenCalledWith());
    await waitFor(() => expect(screen.getByLabelText("Chat ID")).toHaveValue("-100777"));
  });

  it("操作失败展示服务端受控中文错误", async () => {
    fetchConfigMock.mockResolvedValue(configView());
    testNotificationMock.mockRejectedValue(
      new ApiError("通知凭据不可用，请重新填写并保存。", 400, "CREDENTIAL_UNAVAILABLE"),
    );

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "测试 Bot" }));
    expect(
      await screen.findByText("通知凭据不可用，请重新填写并保存。"),
    ).toBeInTheDocument();
  });
});
