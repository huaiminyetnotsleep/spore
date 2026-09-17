import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { App as AntApp, Button } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState, type ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import {
  useAdminAction,
  useConfirmAction,
  type ActionFeedback,
  type ConfirmIntent,
} from "./actions";

function renderWithProviders(node: ReactNode) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <AntApp>{node}</AntApp>
    </QueryClientProvider>,
  );
}

function FeedbackHarness({ feedback }: { feedback: string | ActionFeedback }) {
  const action = useAdminAction({
    action: async () => ({ ok: true }),
    successText: feedback,
  });
  return <Button onClick={() => void action.run(undefined)}>执行</Button>;
}

function ErrorHarness() {
  const [settled, setSettled] = useState(false);
  const action = useAdminAction({
    action: async () => {
      throw new ApiError("服务端受控错误。", 409, "CONFLICT");
    },
  });
  return (
    <Button
      onClick={() => {
        void action.run(undefined).then(() => setSettled(true));
      }}
    >
      {settled ? "已处理" : "触发失败"}
    </Button>
  );
}

function ConfirmHarness({
  action,
  intent = "danger",
}: {
  action: () => Promise<unknown>;
  intent?: ConfirmIntent;
}) {
  const confirm = useConfirmAction();
  const [opened, setOpened] = useState(false);
  return (
    <Button
      onClick={() => {
        setOpened(true);
        confirm({
          intent,
          title: "确认清除",
          content: "清除后不可恢复。",
          okText: "清除",
          action,
        });
      }}
    >
      {opened ? "已打开" : "打开确认"}
    </Button>
  );
}

describe("shared admin actions", () => {
  it("兼容字符串成功反馈", async () => {
    renderWithProviders(<FeedbackHarness feedback="操作完成。" />);

    fireEvent.click(screen.getByRole("button", { name: "执 行" }));

    expect(await screen.findByText("操作完成。")).toBeInTheDocument();
    expect(document.querySelector(".ant-message-success")).toBeInTheDocument();
  });

  it("失败继续使用受控服务端文案并保持 run 的吞错兼容行为", async () => {
    renderWithProviders(<ErrorHarness />);

    fireEvent.click(screen.getByRole("button", { name: "触发失败" }));

    expect(await screen.findByText("服务端受控错误。")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "已处理" })).toBeInTheDocument();
    expect(screen.queryByText("操作成功。")).not.toBeInTheDocument();
  });

  it("支持 warning 与 info 语义反馈", async () => {
    const { unmount } = renderWithProviders(
      <FeedbackHarness feedback={{ type: "warning", text: "部分完成。" }} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "执 行" }));
    expect(await screen.findByText("部分完成。")).toBeInTheDocument();
    expect(document.querySelector(".ant-message-warning")).toBeInTheDocument();
    unmount();

    renderWithProviders(<FeedbackHarness feedback={{ type: "info", text: "已提交。" }} />);
    fireEvent.click(screen.getByRole("button", { name: "执 行" }));
    expect(await screen.findByText("已提交。")).toBeInTheDocument();
    expect(document.querySelector(".ant-message-info")).toBeInTheDocument();
  });

  it("default 与 warning 确认不会误用 danger 按钮", async () => {
    const action = vi.fn(async () => undefined);
    const { unmount } = renderWithProviders(<ConfirmHarness action={action} intent="warning" />);
    fireEvent.click(screen.getByRole("button", { name: "打开确认" }));
    expect(await screen.findByRole("button", { name: "清 除" })).not.toHaveClass(
      "ant-btn-dangerous",
    );
    unmount();

    renderWithProviders(<ConfirmHarness action={action} intent="default" />);
    fireEvent.click(screen.getByRole("button", { name: "打开确认" }));
    expect(await screen.findByRole("button", { name: "清 除" })).not.toHaveClass(
      "ant-btn-dangerous",
    );
  });

  it("显式 danger 确认返回 action Promise 并展示真实 pending", async () => {
    let resolveAction: (value: unknown) => void = () => undefined;
    const action = vi.fn(
      () =>
        new Promise((resolve) => {
          resolveAction = resolve;
        }),
    );
    renderWithProviders(<ConfirmHarness action={action} />);

    fireEvent.click(screen.getByRole("button", { name: "打开确认" }));
    const confirmButton = await screen.findByRole("button", { name: "清 除" });
    expect(confirmButton).toHaveClass("ant-btn-dangerous");

    fireEvent.click(confirmButton);
    expect(action).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(confirmButton).toHaveClass("ant-btn-loading"));
    expect(screen.getByText("清除后不可恢复。")).toBeInTheDocument();

    resolveAction({ ok: true });
    await waitFor(() => expect(screen.queryByText("清除后不可恢复。")).not.toBeInTheDocument());
  });
});
