import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { NotificationSummary } from "./NotificationSummary";

describe("NotificationSummary", () => {
  it("显示自动通知状态和已配置 Webhook 格式", () => {
    render(
      <NotificationSummary
        config={{
          version: 1,
          automatic_events: true,
          bot: {
            enabled: true,
            chat_id: "42",
            has_token: true,
            credential: { available: true },
          },
          webhook: {
            enabled: true,
            format: "feishu",
            has_url: true,
            has_secret: true,
            credential: { available: true },
          },
        }}
      />,
    );

    expect(screen.getByText("自动事件通知")).toBeInTheDocument();
    expect(screen.getByText("已开启")).toBeInTheDocument();
    expect(screen.getByText("Telegram")).toBeInTheDocument();
    expect(screen.getByText("Webhook · 飞书")).toBeInTheDocument();
  });
});
