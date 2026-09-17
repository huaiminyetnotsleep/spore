import { Button, Form, Input, InputNumber, Switch, Tag } from "antd";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { DataTable } from "./DataTable";
import { FilterBar } from "./FilterBar";
import { FormActions } from "./FormActions";
import { FormModal } from "./FormModal";
import { PageActions, PageScaffold, PageSection, ResponsiveActionBar } from "./PageLayout";
import { PageCard, SectionCard } from "./PageStates";
import {
  EmptyState,
  InlineQueryState,
  PageQueryState,
  SectionQueryState,
  TableQueryState,
} from "./QueryStates";
import { SettingItem } from "./SettingItem";
import { StatusTag, statusTone } from "./StatusTag";
import { RowActions, type RowActionItem } from "./RowActions";

describe("shared page foundations", () => {
  it("PageScaffold renders the accessible heading, description, status and actions", () => {
    render(
      <PageScaffold
        title="用户管理"
        description="管理访问用户"
        status={<StatusTag tone="processing">同步中</StatusTag>}
        actions={<Button>新增用户</Button>}
      >
        <PageSection title="用户列表">内容</PageSection>
      </PageScaffold>,
    );

    expect(screen.getByRole("heading", { level: 1, name: "用户管理" })).toBeInTheDocument();
    expect(screen.getByText("管理访问用户")).toBeInTheDocument();
    expect(screen.getByText("同步中")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "新增用户" })).toBeInTheDocument();
    expect(screen.getByText("用户列表").closest(".page-section")).toBeInTheDocument();
  });

  it("action bars expose stable responsive and alignment classes", () => {
    const { container } = render(
      <>
        <ResponsiveActionBar align="between">工具</ResponsiveActionBar>
        <PageActions>页面操作</PageActions>
        <FormActions>保存</FormActions>
      </>,
    );

    expect(container.querySelector(".responsive-action-bar--between")).toHaveTextContent("工具");
    expect(screen.getByText("页面操作")).toHaveClass("responsive-action-bar");
    expect(screen.getByText("保存")).toHaveClass("form-actions");
  });

  it("legacy PageCard and SectionCard no longer share width classes", () => {
    const { container } = render(
      <MemoryRouter>
        <PageCard title="旧页面">
          <SectionCard title="旧分区">内容</SectionCard>
        </PageCard>
      </MemoryRouter>,
    );

    const pageCard = container.querySelector(".page-card");
    const sectionCard = container.querySelector(".section-card");
    expect(pageCard).not.toHaveClass("section-card");
    expect(sectionCard).not.toHaveClass("page-card");
  });
});

describe("shared query states", () => {
  it("distinguishes initial loading from background refreshing", () => {
    const { rerender } = render(
      <PageQueryState initialLoading loadingText="首次加载" hasData={false}>
        已有内容
      </PageQueryState>,
    );
    expect(screen.getByText("首次加载")).toBeInTheDocument();
    expect(screen.queryByText("已有内容")).not.toBeInTheDocument();

    rerender(
      <PageQueryState refreshing hasData>
        已有内容
      </PageQueryState>,
    );
    expect(screen.getByText("已有内容")).toBeInTheDocument();
    expect(screen.getByText("已有内容").closest(".query-state")).toHaveAttribute("aria-busy", "true");
  });

  it("keeps page, section, table, inline, error and empty states scoped", () => {
    const retry = vi.fn();
    const { container } = render(
      <>
        <SectionQueryState error={new Error("failed")} hasData={false} onRetry={retry} />
        <TableQueryState state="error" description="用户列表暂时不可用" />
        <TableQueryState state="empty" description="没有记录" />
        <InlineQueryState pending text="正在保存" />
        <EmptyState description="暂无配置" action={<Button>添加</Button>} />
      </>,
    );

    fireEvent.click(screen.getByRole("button", { name: "重 试" }));
    expect(retry).toHaveBeenCalledTimes(1);
    expect(screen.getByText("用户列表暂时不可用")).toBeInTheDocument();
    expect(screen.getByText("没有记录")).toBeInTheDocument();
    expect(screen.getByText("正在保存")).toBeInTheDocument();
    expect(screen.getByText("暂无配置")).toBeInTheDocument();
    expect(container.querySelector(".inline-query-state .ant-spin")).toBeInTheDocument();
  });

  it("keeps cached content visible when a scoped refresh fails", () => {
    render(
      <SectionQueryState error={new Error("refresh failed")} hasData>
        已缓存的用户列表
      </SectionQueryState>,
    );

    expect(screen.getByText("已缓存的用户列表")).toBeInTheDocument();
    expect(screen.getByText("数据加载失败")).toBeInTheDocument();
  });
});

describe("shared filtering and table defaults", () => {
  it("FilterBar submit mode submits and resets while instant mode has no fake apply button", async () => {
    const submit = vi.fn();
    const reset = vi.fn();
    const { rerender } = render(
      <FilterBar<{ keyword: string }> mode="submit" onFinish={submit} onReset={reset}>
        <Form.Item name="keyword" label="关键词">
          <Input />
        </Form.Item>
      </FilterBar>,
    );

    fireEvent.change(screen.getByRole("textbox", { name: "关键词" }), {
      target: { value: "alice" },
    });
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));
    await waitFor(() => expect(submit).toHaveBeenCalledWith({ keyword: "alice" }));
    fireEvent.click(screen.getByRole("button", { name: "重 置" }));
    expect(reset).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("textbox", { name: "关键词" })).toHaveValue("");

    rerender(
      <FilterBar mode="instant">
        <Form.Item label="状态">
          <Input />
        </Form.Item>
      </FilterBar>,
    );
    expect(screen.queryByRole("button", { name: "筛 选" })).not.toBeInTheDocument();
  });

  it("DataTable applies default and compact density, horizontal scroll and empty state", () => {
    const columns = [{ title: "名称", dataIndex: "name" }];
    const { container, rerender } = render(
      <DataTable<{ id: number; name: string }>
        rowKey="id"
        columns={columns}
        dataSource={[]}
        pagination={false}
        emptyText="没有用户"
      />,
    );

    expect(container.querySelector(".ant-table-middle")).toBeInTheDocument();
    expect(container.querySelector(".ant-table-content")).toHaveStyle({ overflowX: "auto" });
    expect(screen.getByText("没有用户")).toBeInTheDocument();

    rerender(
      <DataTable<{ id: number; name: string }>
        density="compact"
        rowKey="id"
        columns={columns}
        dataSource={[]}
        scroll={{ x: 640 }}
        locale={{ emptyText: "调用方空态" }}
        pagination={false}
      />,
    );
    expect(container.querySelector(".ant-table-small")).toBeInTheDocument();
    expect(screen.getByText("调用方空态")).toBeInTheDocument();
    expect(screen.queryByText("暂无数据。")).not.toBeInTheDocument();
  });
});

interface ModalValues {
  name: string;
}

function FormModalHarness({
  preserve = false,
  submit,
}: {
  preserve?: boolean;
  submit: (values: ModalValues) => Promise<boolean>;
}) {
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm<ModalValues>();
  return (
    <>
      <Button onClick={() => setOpen(true)}>打开表单</Button>
      <FormModal<ModalValues>
        title="编辑用户"
        open={open}
        form={form}
        onOpenChange={setOpen}
        onSubmit={submit}
        preserveOnClose={preserve}
      >
        <Form.Item name="name" label="名称" rules={[{ required: true, message: "请输入名称" }]}>
          <Input />
        </Form.Item>
      </FormModal>
    </>
  );
}

describe("FormModal lifecycle", () => {
  it("resets cancelled drafts by default and preserves only with explicit opt-in", async () => {
    const submit = vi.fn(async () => true);
    const { unmount } = render(<FormModalHarness submit={submit} />);
    fireEvent.click(screen.getByRole("button", { name: "打开表单" }));
    fireEvent.change(await screen.findByRole("textbox", { name: "名称" }), {
      target: { value: "draft" },
    });
    fireEvent.click(screen.getByRole("button", { name: "取 消" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "打开表单" }));
    expect(await screen.findByRole("textbox", { name: "名称" })).toHaveValue("");
    unmount();

    render(<FormModalHarness preserve submit={submit} />);
    fireEvent.click(screen.getByRole("button", { name: "打开表单" }));
    fireEvent.change(await screen.findByRole("textbox", { name: "名称" }), {
      target: { value: "kept" },
    });
    fireEvent.click(screen.getByRole("button", { name: "取 消" }));
    fireEvent.click(screen.getByRole("button", { name: "打开表单" }));
    expect(await screen.findByRole("textbox", { name: "名称" })).toHaveValue("kept");
  });

  it("keeps the draft open when the submit result is not successful", async () => {
    const submit = vi.fn(async () => false);
    render(<FormModalHarness submit={submit} />);
    fireEvent.click(screen.getByRole("button", { name: "打开表单" }));
    fireEvent.change(await screen.findByRole("textbox", { name: "名称" }), {
      target: { value: "Needs retry" },
    });
    fireEvent.click(screen.getByRole("button", { name: "确 定" }));

    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "确 定" })).not.toHaveClass("ant-btn-loading"),
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "名称" })).toHaveValue("Needs retry");
  });

  it("does not submit when client validation fails", async () => {
    const submit = vi.fn(async () => true);
    render(<FormModalHarness submit={submit} />);
    fireEvent.click(screen.getByRole("button", { name: "打开表单" }));
    fireEvent.click(await screen.findByRole("button", { name: "确 定" }));

    expect(await screen.findByText("请输入名称")).toBeInTheDocument();
    expect(submit).not.toHaveBeenCalled();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("keeps the draft open after an API rejection", async () => {
    const submit = vi.fn(async () => {
      throw new Error("api failed");
    });
    render(<FormModalHarness submit={submit} />);
    fireEvent.click(screen.getByRole("button", { name: "打开表单" }));
    fireEvent.change(await screen.findByRole("textbox", { name: "名称" }), {
      target: { value: "Retry me" },
    });
    fireEvent.click(screen.getByRole("button", { name: "确 定" }));

    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(screen.getByRole("button", { name: "确 定" })).not.toHaveClass("ant-btn-loading"),
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "名称" })).toHaveValue("Retry me");
  });

  it("clears a preserved draft after a successful submit", async () => {
    const submit = vi.fn(async () => true);
    render(<FormModalHarness preserve submit={submit} />);
    fireEvent.click(screen.getByRole("button", { name: "打开表单" }));
    fireEvent.change(await screen.findByRole("textbox", { name: "名称" }), {
      target: { value: "Saved" },
    });
    fireEvent.click(screen.getByRole("button", { name: "确 定" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole("button", { name: "打开表单" }));
    expect(await screen.findByRole("textbox", { name: "名称" })).toHaveValue("");
  });

  it("prevents duplicate submit and closes only after successful resolution", async () => {
    let resolveSubmit: (value: boolean) => void = () => undefined;
    const submit = vi.fn(
      () =>
        new Promise<boolean>((resolve) => {
          resolveSubmit = resolve;
        }),
    );
    render(<FormModalHarness submit={submit} />);
    fireEvent.click(screen.getByRole("button", { name: "打开表单" }));
    fireEvent.change(await screen.findByRole("textbox", { name: "名称" }), {
      target: { value: "Alice" },
    });
    const submitButton = screen.getByRole("button", { name: "确 定" });
    fireEvent.click(submitButton);
    fireEvent.click(submitButton);

    await waitFor(() => expect(submit).toHaveBeenCalledTimes(1));
    expect(submitButton).toHaveClass("ant-btn-loading");
    const cancelButton = screen.getByRole("button", { name: "取 消" });
    expect(cancelButton).toBeDisabled();
    fireEvent.click(cancelButton);
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    resolveSubmit(true);
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});

describe("StatusTag", () => {
  it("maps semantic tones without owning domain statuses", () => {
    render(
      <>
        <StatusTag tone="success" className="domain-status">在线</StatusTag>
        <StatusTag tone="inactive">离线</StatusTag>
      </>,
    );
    expect(screen.getByText("在线")).toHaveClass("status-tag--success", "domain-status");
    expect(screen.getByText("离线")).toHaveClass("status-tag--inactive");
    expect(statusTone("processing")).toBe("processing");
  });
});

describe("RowActions", () => {
  function rowActions(): RowActionItem[] {
    return [
      { key: "view", label: "查看详情", onClick: vi.fn() },
      { key: "reset", label: "重置", danger: true, onClick: vi.fn() },
      { key: "remove", label: "移除", danger: true, onClick: vi.fn() },
    ];
  }

  it("renders the first maxVisible actions inline and collapses the rest into a more menu", async () => {
    const actions = rowActions();
    const { container } = render(<RowActions actions={actions} />);

    // 前两个动作内联为链接按钮，其余不直接出现
    //（antd 不给 link 按钮的两个汉字间插空格，可访问名即原文案）
    expect(screen.getByRole("button", { name: "查看详情" })).toHaveClass(
      "row-actions__action",
      "ant-btn-link",
    );
    expect(screen.getByRole("button", { name: "重置" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "移除" })).not.toBeInTheDocument();

    // 折叠触发为图标按钮：aria-label 可访问名 + antd Tooltip「更多」悬停提示
    const more = screen.getByRole("button", { name: "更多操作" });
    expect(more).toHaveClass("row-actions__more");
    expect(container.querySelector(".row-actions__more .anticon-more")).toBeInTheDocument();
    fireEvent.mouseEnter(more);
    const tooltip = await screen.findByRole("tooltip");
    expect(tooltip).toHaveTextContent("更多");

    fireEvent.click(more);
    const menu = await screen.findByRole("menu");
    expect(menu).toHaveTextContent("移除");

    // 菜单项点击派发到对应动作
    fireEvent.click(screen.getByRole("menuitem", { name: "移除" }));
    expect(actions[2].onClick).toHaveBeenCalledTimes(1);
    expect(actions[0].onClick).not.toHaveBeenCalled();
  });

  it("renders danger menu items and keeps loading collapsed actions visible but disabled", async () => {
    const onClick = vi.fn();
    const actions: RowActionItem[] = [
      { key: "a", label: "详情", onClick: vi.fn() },
      { key: "b", label: "归档", onClick: vi.fn() },
      { key: "c", label: "删除", danger: true, loading: true, onClick },
    ];
    const { rerender } = render(<RowActions actions={actions} />);

    fireEvent.click(screen.getByRole("button", { name: "更多操作" }));
    const item = await screen.findByRole("menuitem", { name: "删除" });
    expect(item).toHaveClass("ant-dropdown-menu-item-danger");
    expect(item).toHaveClass("ant-dropdown-menu-item-disabled");
    expect(item).toHaveAttribute("aria-disabled", "true");
    expect(item).toHaveTextContent("删除");

    // loading 结束后菜单项自动恢复可用
    rerender(
      <RowActions
        actions={[{ key: "a", label: "详情", onClick: vi.fn() }, { key: "b", label: "归档", onClick: vi.fn() }, { key: "c", label: "删除", danger: true, onClick }]}
      />,
    );
    fireEvent.click(await screen.findByRole("menuitem", { name: "删除" }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("keeps inline loading buttons in loading state and supports disabled tooltips", () => {
    const actions: RowActionItem[] = [
      { key: "a", label: "暂停", loading: true, onClick: vi.fn() },
      { key: "b", label: "移除", danger: true, disabled: true, title: "环境变量配置不可移除", onClick: vi.fn() },
    ];
    render(<RowActions actions={actions} maxVisible={2} />);

    // loading 图标（aria-label="loading"）会并入可访问名，用正则匹配文案
    expect(screen.getByRole("button", { name: /暂停/ })).toHaveClass("ant-btn-loading");
    const disabled = screen.getByRole("button", { name: "移除" });
    expect(disabled).toBeDisabled();
    // 禁用按钮不触发鼠标事件：title 经外层 Tooltip/span 保留
    expect(disabled.closest(".row-actions__item")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "更多操作" })).not.toBeInTheDocument();
  });
});

describe("SettingItem", () => {
  it("lays out control, label, state extra and description", () => {
    render(
      <SettingItem
        control={<Switch aria-label="示例开关" />}
        label="允许加入频道"
        extra={<Tag color="green">已开启</Tag>}
        description="切换即保存并即时生效。"
      />,
    );

    expect(screen.getByRole("switch", { name: "示例开关" })).toBeInTheDocument();
    expect(screen.getByText("允许加入频道")).toBeInTheDocument();
    expect(screen.getByText("已开启")).toBeInTheDocument();
    expect(screen.getByText("切换即保存并即时生效。")).toHaveClass("ant-typography-secondary");
  });

  it("keeps an adjacent inline action button usable", () => {
    render(
      <SettingItem
        control={<InputNumber aria-label="数量上限" />}
        label="加入数量上限"
        extra={<Button size="small">保存上限</Button>}
      />,
    );

    expect(screen.getByRole("button", { name: "保存上限" })).toBeInTheDocument();
    expect(screen.queryByText(/生效/)).not.toBeInTheDocument();
  });
});
