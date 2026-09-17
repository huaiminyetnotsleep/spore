/**
 * 用户列表页组件测试：表格渲染、空态、错误态（含重试）与操作列快捷操作
 * （禁用/归档/重置今日用量的确认、提交与 owner 行为差异）。
 * API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import { fetchUsers, type ListEnvelope, type UserRow } from "../../api/admin";
import { addUser, resetUserQuota, setUserStatus } from "../../api/mutations";
import { UsersListPage } from "./UsersListPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchUsers: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    addUser: vi.fn(),
    setUserStatus: vi.fn(),
    resetUserQuota: vi.fn(),
  };
});

const fetchUsersMock = vi.mocked(fetchUsers);
const addUserMock = vi.mocked(addUser);
const setUserStatusMock = vi.mocked(setUserStatus);
const resetUserQuotaMock = vi.mocked(resetUserQuota);

function envelope(items: UserRow[]): ListEnvelope<UserRow> {
  return { items, page: 1, page_size: 20, total: items.length, total_pages: 1 };
}

function userRow(overrides: Partial<UserRow>): UserRow {
  return {
    id: 1001,
    username: "alice",
    display_name: "爱丽丝",
    status: "enabled",
    is_owner: false,
    note: "",
    last_used_at: 1756598400000,
    source_bot_id: 0,
    source_bot_username: "",
    total_requests: 12,
    has_total_requests: true,
    cloud_download: 0,
    effective_cloud_download: false,
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const invalidateSpy = vi.spyOn(client, "invalidateQueries");
  render(
    <QueryClientProvider client={client}>
      {/* antd App 上下文：message/modal 实例来自它（生产由 App.tsx 挂载） */}
      <AntApp component={false}>
        <MemoryRouter initialEntries={["/users"]}>
          <UsersListPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
  return invalidateSpy;
}

/**
 * 打开第 index 行操作列的「更多」菜单，返回最新打开的菜单元素。
 * 折叠的管理动作（禁用/归档/重置）在 RowActions 菜单里，须先展开再断言。
 * 若该行菜单已处于打开状态，第一次点击会切换为关闭，此处再点一次确保展开。
 */
async function openRowMenu(index = 0) {
  const moreButtons = await screen.findAllByRole("button", { name: "更多操作" });
  fireEvent.click(moreButtons[index]);
  const opened = await screen
    .findAllByRole("menu", {}, { timeout: 1500 })
    .catch(() => null);
  if (opened) {
    return opened[opened.length - 1];
  }
  fireEvent.click(moreButtons[index]);
  const menus = await screen.findAllByRole("menu");
  return menus[menus.length - 1];
}

describe("用户列表页", () => {
  beforeEach(() => {
    fetchUsersMock.mockReset();
    addUserMock.mockReset();
    setUserStatusMock.mockReset();
    resetUserQuotaMock.mockReset();
  });

  it("渲染唯一 H1 页面标题「用户管理」与页面操作区", async () => {
    fetchUsersMock.mockResolvedValue(envelope([userRow({})]));

    renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "用户管理" })).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.getByRole("button", { name: "新增用户" })).toBeInTheDocument();
  });

  it("筛选提交带条件查询，重置恢复无条件查询", async () => {
    fetchUsersMock.mockResolvedValue(envelope([userRow({})]));

    renderPage();
    await screen.findByText(/@alice/);

    fireEvent.change(screen.getByPlaceholderText("ID / 用户名 / 显示名 / 备注"), {
      target: { value: "alice" },
    });
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));

    await waitFor(() =>
      expect(fetchUsersMock).toHaveBeenCalledWith(
        expect.objectContaining({ q: "alice", page: 1, page_size: 20 }),
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: "重 置" }));

    await waitFor(() =>
      expect(fetchUsersMock).toHaveBeenCalledWith(
        expect.objectContaining({ q: undefined, status: undefined, page: 1, page_size: 20 }),
      ),
    );
    expect(screen.getByPlaceholderText("ID / 用户名 / 显示名 / 备注")).toHaveValue("");
  });

  it("渲染用户表格行、状态中文标签与 CSV 导出入口", async () => {
    fetchUsersMock.mockResolvedValue(
      envelope([
        userRow({}),
        userRow({ id: 1002, username: "bob", display_name: "", status: "pending", is_owner: true }),
      ]),
    );

    renderPage();

    // 用户名与显示名渲染在同一个单元格内，用子串匹配
    expect(await screen.findByText(/@alice/)).toBeInTheDocument();
    expect(screen.getByText(/爱丽丝/)).toBeInTheDocument();
    expect(screen.getByText("1001")).toBeInTheDocument();
    expect(screen.getByText("已启用")).toBeInTheDocument();
    expect(screen.getByText("待审批")).toBeInTheDocument();
    expect(screen.getByText("owner")).toBeInTheDocument();

    const exportLink = screen.getByRole("link", { name: "导出 CSV" });
    expect(exportLink).toHaveAttribute("href", "/users/export.csv");
    expect(fetchUsersMock).toHaveBeenCalledWith(
      expect.objectContaining({ page: 1, page_size: 20 }),
    );
  });

  it("空数据时展示受控空态文案", async () => {
    fetchUsersMock.mockResolvedValue(envelope([]));

    renderPage();

    expect(await screen.findByText("没有符合条件的用户。")).toBeInTheDocument();
  });

  it("请求失败时展示错误态，重试会重新发起查询", async () => {
    fetchUsersMock.mockRejectedValueOnce(new ApiError("API 请求失败（500）", 500));
    fetchUsersMock.mockResolvedValue(envelope([userRow({})]));

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();
    expect(screen.queryByText("@alice")).not.toBeInTheDocument();

    // antd 按钮会在两个汉字之间插入空格（"重 试"），用正则匹配可访问名
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));

    await waitFor(() => expect(screen.getByText(/@alice/)).toBeInTheDocument());
    expect(fetchUsersMock).toHaveBeenCalledTimes(2);
  });

  it("操作列：查看入口内联，管理动作按行收敛进「更多」菜单", async () => {
    fetchUsersMock.mockResolvedValue(
      envelope([
        userRow({}), // enabled、非 owner：禁用+归档+重置齐全
        userRow({ id: 1002, status: "pending" }), // 待审批：不可禁用（未启用）
        userRow({ id: 1003, status: "enabled", is_owner: true }), // owner：只保留重置
        userRow({ id: 1004, status: "archived" }), // 已归档：不重复提供归档
      ]),
    );

    renderPage();

    // 每行内联的导航动作（link 按钮）与「更多」触发
    expect(await screen.findAllByRole("button", { name: "查看请求记录" })).toHaveLength(4);
    expect(screen.getAllByRole("button", { name: "查看频道绑定" })).toHaveLength(4);
    const moreButtons = await screen.findAllByRole("button", { name: "更多操作" });
    expect(moreButtons).toHaveLength(4);

    // 第 1 行（启用、非 owner）：禁用 + 归档 + 重置今日用量
    fireEvent.click(moreButtons[0]);
    const firstMenu = await screen.findByRole("menu");
    expect(within(firstMenu).getByRole("menuitem", { name: "禁用" })).toBeInTheDocument();
    expect(within(firstMenu).getByRole("menuitem", { name: "归档" })).toBeInTheDocument();
    expect(within(firstMenu).getByRole("menuitem", { name: "重置今日用量" })).toBeInTheDocument();

    // 第 2 行（待审批）：不提供禁用（未启用），保留归档 + 重置
    fireEvent.click(moreButtons[1]);
    const secondMenu = (await screen.findAllByRole("menu")).at(-1);
    expect(secondMenu).toBeDefined();
    expect(within(secondMenu!).queryByRole("menuitem", { name: "禁用" })).not.toBeInTheDocument();
    expect(within(secondMenu!).getByRole("menuitem", { name: "归档" })).toBeInTheDocument();

    // 第 3 行（owner）：仅重置今日用量
    fireEvent.click(moreButtons[2]);
    const thirdMenu = (await screen.findAllByRole("menu")).at(-1);
    expect(thirdMenu).toBeDefined();
    expect(within(thirdMenu!).queryByRole("menuitem", { name: "禁用" })).not.toBeInTheDocument();
    expect(within(thirdMenu!).queryByRole("menuitem", { name: "归档" })).not.toBeInTheDocument();
    expect(within(thirdMenu!).getByRole("menuitem", { name: "重置今日用量" })).toBeInTheDocument();

    // 第 4 行（已归档）：未启用不再提供禁用，已归档不重复提供归档，仅保留重置
    fireEvent.click(moreButtons[3]);
    const fourthMenu = (await screen.findAllByRole("menu")).at(-1);
    expect(fourthMenu).toBeDefined();
    expect(within(fourthMenu!).queryByRole("menuitem", { name: "禁用" })).not.toBeInTheDocument();
    expect(within(fourthMenu!).queryByRole("menuitem", { name: "归档" })).not.toBeInTheDocument();
    expect(within(fourthMenu!).getByRole("menuitem", { name: "重置今日用量" })).toBeInTheDocument();
  });

  it("快捷禁用：菜单动作经二次确认后提交，成功失效用户 query 并提示", async () => {
    setUserStatusMock.mockResolvedValue({ ok: true, status: "disabled" });
    fetchUsersMock.mockResolvedValue(envelope([userRow({})]));
    const invalidateSpy = renderPage();

    const menu = await openRowMenu();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "禁用" }));

    // 破坏性操作先弹二次确认（与详情页同一文案）
    expect(
      await screen.findByText("确定禁用该用户？其新请求将被立即拒绝。"),
    ).toBeInTheDocument();
    expect(setUserStatusMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(setUserStatusMock).toHaveBeenCalledWith(1001, "disable"));
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ["users"] })),
    );
    expect(await screen.findByText("状态已更新为已禁用。")).toBeInTheDocument();
  });

  it("重置今日用量：菜单动作确认后提交 reset-quota 并提示", async () => {
    resetUserQuotaMock.mockResolvedValue({ ok: true });
    fetchUsersMock.mockResolvedValue(envelope([userRow({})]));
    const invalidateSpy = renderPage();

    const menu = await openRowMenu();
    fireEvent.click(within(menu).getByRole("menuitem", { name: "重置今日用量" }));

    expect(await screen.findByText("确定重置该用户今日已用额度为 0？")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(resetUserQuotaMock).toHaveBeenCalledWith(1001));
    expect(await screen.findByText("当日已用额度已重置。")).toBeInTheDocument();
    expect(invalidateSpy).toHaveBeenCalledWith(
      expect.objectContaining({ queryKey: ["users"] }),
    );
  });

  // 多阶段交互（展开菜单→确认→取消→再展开→恢复）串行等待较多，
  // 并行测试负载下可能超出默认 5s，显式放宽该用例超时。
  it(
    "快捷操作 pending 期间菜单动作禁用（label 保留），完成后恢复",
    async () => {
      let resolveStatus: (value: { ok: boolean; status: string }) => void = () => undefined;
      setUserStatusMock.mockImplementation(
        () =>
          new Promise<{ ok: boolean; status: string }>((resolve) => {
            resolveStatus = resolve;
          }),
      );
      fetchUsersMock.mockResolvedValue(
        envelope([userRow({}), userRow({ id: 1002, username: "bob" })]),
      );

      renderPage();

      // 第 1 行禁用：确认弹层保持 pending，取消弹层后 mutation 仍在途
      const firstMenu = await openRowMenu(0);
      fireEvent.click(within(firstMenu).getByRole("menuitem", { name: "禁用" }));
      // loading 图标会并入 OK 按钮可访问名，先取引用再断言 loading
      const confirmOk = await screen.findByRole("button", { name: "确 认" });
      fireEvent.click(confirmOk);
      await waitFor(() => expect(confirmOk).toHaveClass("ant-btn-loading"));
      fireEvent.click(screen.getByRole("button", { name: "取 消" }));

      // pending 期间重新展开菜单：动作禁用且 label 保留（RowActions 折叠动作加载契约）
      const pendingMenu = await openRowMenu(0);
      const pendingItem = within(pendingMenu).getByRole("menuitem", { name: "禁用" });
      expect(pendingItem).toHaveAttribute("aria-disabled", "true");
      expect(pendingItem).toHaveTextContent("禁用");

      // mutation 完成并刷新后动作恢复可用
      resolveStatus({ ok: true, status: "disabled" });
      expect(await screen.findByText("状态已更新为已禁用。")).toBeInTheDocument();
      const recoveredMenu = await openRowMenu(0);
      await waitFor(
        () => {
          expect(
            within(recoveredMenu).getByRole("menuitem", { name: "禁用" }),
          ).not.toHaveAttribute("aria-disabled", "true");
        },
        { timeout: 3000 },
      );
    },
    15_000,
  );

  it("手动添加用户：FormModal 取消后默认重置草稿", async () => {
    fetchUsersMock.mockResolvedValue(envelope([userRow({})]));

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "新增用户" }));
    const idInput = await screen.findByLabelText(/Telegram 用户 ID/);
    fireEvent.change(idInput, { target: { value: "123456789" } });

    fireEvent.click(screen.getByRole("button", { name: "取 消" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());

    fireEvent.click(screen.getByRole("button", { name: "新增用户" }));
    expect(await screen.findByLabelText(/Telegram 用户 ID/)).toHaveValue("");
    expect(addUserMock).not.toHaveBeenCalled();
  });

  it("手动添加用户：提交调用 addUser 并在成功后关闭弹窗", async () => {
    addUserMock.mockResolvedValue({ ok: true, user_id: 123456789 });
    fetchUsersMock.mockResolvedValue(envelope([userRow({})]));

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "新增用户" }));
    fireEvent.change(await screen.findByLabelText(/Telegram 用户 ID/), {
      target: { value: "123456789" },
    });
    fireEvent.click(screen.getByRole("button", { name: "添加（默认启用）" }));

    await waitFor(() =>
      expect(addUserMock).toHaveBeenCalledWith({ user_id: 123456789, note: undefined }),
    );
    expect(await screen.findByText("用户已添加（默认启用）。")).toBeInTheDocument();
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });
});
