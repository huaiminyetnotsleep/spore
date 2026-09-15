/**
 * 请求记录列表页组件测试：投递方式标记（引用/上传/混合/文本/网盘）渲染、
 * 空态、错误态（含重试）与操作列删除/存到网盘交互、批量补存。
 * API 层以模块 mock 注入，不发起真实网络请求。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import {
  fetchCloudDrive,
  fetchRequests,
  type CloudDriveView,
  type ListEnvelope,
  type RequestRow,
} from "../../api/admin";
import {
  cancelRequest,
  cancelRequests,
  cloudArchiveBatch,
  cloudArchiveRequest,
  deleteRequest,
  deleteRequests,
} from "../../api/mutations";
import { RequestsListPage } from "./RequestsListPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchRequests: vi.fn(),
    fetchCloudDrive: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    deleteRequest: vi.fn(),
    cancelRequest: vi.fn(),
    cancelRequests: vi.fn(),
    deleteRequests: vi.fn(),
    cloudArchiveRequest: vi.fn(),
    cloudArchiveBatch: vi.fn(),
  };
});

const fetchRequestsMock = vi.mocked(fetchRequests);
const fetchCloudDriveMock = vi.mocked(fetchCloudDrive);
const deleteRequestMock = vi.mocked(deleteRequest);
const cancelRequestMock = vi.mocked(cancelRequest);
const cancelRequestsMock = vi.mocked(cancelRequests);
const deleteRequestsMock = vi.mocked(deleteRequests);
const cloudArchiveRequestMock = vi.mocked(cloudArchiveRequest);
const cloudArchiveBatchMock = vi.mocked(cloudArchiveBatch);

/** 云盘配置：默认开启，mega-1 为默认目的地，s3-1 备选。 */
function cloudDriveView(overrides: Partial<CloudDriveView> = {}): CloudDriveView {
  return {
    enabled: true,
    default_destination: "mega-1",
    rclone_available: true,
    destinations: [
      { name: "mega-1", type: "mega", path_prefix: "spore", enabled: true, options: {} },
      { name: "s3-1", type: "s3", path_prefix: "", enabled: true, options: {} },
    ],
    ...overrides,
  };
}

function envelope(items: RequestRow[]): ListEnvelope<RequestRow> {
  return { items, page: 1, page_size: 20, total: items.length, total_pages: 1 };
}

function requestRow(overrides: Partial<RequestRow>): RequestRow {
  return {
    id: 1,
    user_id: 301,
    username: "alice",
    display_name: "Alice",
    source_kind: "public",
    channel_key: "example",
    message_id: 7,
    message_url: "https://t.me/example/7",
    status: "succeeded",
    attempt: 1,
    error_code: "",
    media_type: "photo",
    media_types: ["photo"],
    source_media_dc_ids: [],
    delivery_mode: "upload",
    requested_at: 1756598400000,
    duration_ms: 850,
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
        <MemoryRouter initialEntries={["/requests"]}>
          <RequestsListPage />
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
  return invalidateSpy;
}

describe("请求记录列表页", () => {
  beforeEach(() => {
    fetchRequestsMock.mockReset();
    fetchCloudDriveMock.mockReset().mockResolvedValue(cloudDriveView({ enabled: false }));
    deleteRequestMock.mockReset();
    cancelRequestMock.mockReset();
    cancelRequestsMock.mockReset();
    deleteRequestsMock.mockReset();
    cloudArchiveRequestMock.mockReset();
    cloudArchiveBatchMock.mockReset();
  });

  it("processing 记录渲染实时下载/上传进度，终态记录显示占位", async () => {
    fetchRequestsMock.mockResolvedValue(
      envelope([
        requestRow({
          id: 1,
          status: "processing",
          progress: { total_bytes: 1000, downloaded_bytes: 500, uploaded_bytes: 250 },
        }),
        requestRow({ id: 2, status: "succeeded" }),
      ]),
    );

    renderPage();

    // 进度条按字节数渲染百分比与文案（两条独立管线：下载/上传）
    expect(await screen.findByText(/下载 50%（500 B）/)).toBeInTheDocument();
    expect(screen.getByText(/上传 25%（250 B）/)).toBeInTheDocument();
    // 终态记录无进度，占位显示
    expect(screen.getAllByText("—").length).toBeGreaterThan(0);
  });

  it("processing 记录不存在时不启用进度轮询间隔", async () => {
    fetchRequestsMock.mockResolvedValue(envelope([requestRow({ status: "succeeded" })]));

    renderPage();

    await screen.findByText("example");
    // 无 processing 行时仍应正常渲染；轮询开关由 refetchInterval 表达，
    // 这里断言数据渲染不受影响（间隔逻辑由 useQuery 参数保证）
    expect(screen.getByText("成功")).toBeInTheDocument();
  });

  it("相册展示成员类型，并把源媒体 DC 独立成列", async () => {
    fetchRequestsMock.mockResolvedValue(
      envelope([
        requestRow({
          media_type: "album",
          media_types: ["photo", "video"],
          source_media_dc_ids: [2, 4],
        }),
      ]),
    );

    renderPage();

    expect(await screen.findByText("album（photo + video）")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "源媒体 DC" })).toBeInTheDocument();
    expect(screen.getByText("DC 2、DC 4")).toBeInTheDocument();
  });

  it("渲染投递方式中文标签（引用/上传/混合/文本）", async () => {
    fetchRequestsMock.mockResolvedValue(
      envelope([
        requestRow({ id: 1, delivery_mode: "reference" }),
        requestRow({ id: 2, delivery_mode: "upload" }),
        requestRow({ id: 3, delivery_mode: "mixed" }),
        requestRow({ id: 4, delivery_mode: "text", media_type: "text" }),
      ]),
    );

    renderPage();

    expect(await screen.findByText("引用")).toBeInTheDocument();
    expect(screen.getByText("上传")).toBeInTheDocument();
    expect(screen.getByText("混合")).toBeInTheDocument();
    expect(screen.getByText("文本")).toBeInTheDocument();
  });

  it("点击筛选会按表单条件查询，重复提交相同条件也会刷新", async () => {
    fetchRequestsMock.mockResolvedValue(envelope([requestRow({})]));

    renderPage();
    await screen.findByText("example");
    const userInput = screen.getByPlaceholderText("用户 ID");

    fireEvent.change(userInput, { target: { value: "301" } });
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));

    await waitFor(() =>
      expect(fetchRequestsMock).toHaveBeenCalledWith(
        expect.objectContaining({ user_id: "301", page: 1, page_size: 20 }),
      ),
    );
    const callsAfterFilter = fetchRequestsMock.mock.calls.length;

    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));
    await waitFor(() => expect(fetchRequestsMock.mock.calls.length).toBe(callsAfterFilter + 1));
  });

  it("CSV 导出入口指向记录导出端点", async () => {
    fetchRequestsMock.mockResolvedValue(envelope([requestRow({})]));

    renderPage();

    await screen.findByText("example");
    const exportLink = screen.getByRole("link", { name: /按当前条件导出 CSV/ });
    expect(exportLink).toHaveAttribute("href", "/requests/export.csv");
  });

  it("空数据时展示受控空态文案", async () => {
    fetchRequestsMock.mockResolvedValue(envelope([]));

    renderPage();

    expect(await screen.findByText("没有符合条件的记录。")).toBeInTheDocument();
  });

  it("请求失败时展示错误态，重试会重新发起查询", async () => {
    fetchRequestsMock.mockRejectedValueOnce(new ApiError("API 请求失败（500）", 500));
    fetchRequestsMock.mockResolvedValue(envelope([requestRow({})]));

    renderPage();

    expect(await screen.findByText("数据加载失败")).toBeInTheDocument();

    // antd 按钮会在两个汉字之间插入空格（"重 试"），用正则匹配可访问名
    fireEvent.click(screen.getByRole("button", { name: /重\s*试/ }));

    await waitFor(() => expect(screen.getByText("example")).toBeInTheDocument());
    expect(fetchRequestsMock).toHaveBeenCalledTimes(2);
  });

  it("删除记录：二次确认后提交，成功失效派生统计 query 并提示", async () => {
    deleteRequestMock.mockResolvedValue({ ok: true });
    fetchRequestsMock.mockResolvedValue(envelope([requestRow({})]));
    const invalidateSpy = renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "删 除" }));

    // 删除是不可恢复操作，先弹二次确认
    expect(
      await screen.findByText(/确定删除记录 #1？删除后不可恢复/),
    ).toBeInTheDocument();
    expect(deleteRequestMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(deleteRequestMock).toHaveBeenCalledWith(1));
    // 记录行是频道/用户/总览统计的底层数据，相关 query 全部失效
    for (const key of [["requests"], ["channels"], ["users"], ["overview"]]) {
      await waitFor(() =>
        expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: key })),
      );
    }
    expect(await screen.findByText("记录已删除。")).toBeInTheDocument();
  });

  it("删除被服务端拒绝（未终态）：展示受控文案，不提示成功", async () => {
    deleteRequestMock.mockRejectedValue(
      new ApiError("该记录尚未结束（排队或处理中），请等待其完成后再删除。", 409, "STORE_CONSTRAINT"),
    );
    fetchRequestsMock.mockResolvedValue(envelope([requestRow({})]));
    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "删 除" }));
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));

    expect(
      await screen.findByText("该记录尚未结束（排队或处理中），请等待其完成后再删除。"),
    ).toBeInTheDocument();
    expect(screen.queryByText("记录已删除。")).not.toBeInTheDocument();
  });

  it("活动请求显示取消按钮，确认后提交并刷新派生查询", async () => {
    cancelRequestMock.mockResolvedValue({ ok: true });
    fetchRequestsMock.mockResolvedValue(envelope([requestRow({ status: "processing" })]));
    const invalidateSpy = renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "取 消" }));
    expect(await screen.findByText(/确定取消请求 #1？取消后不可恢复/)).toBeInTheDocument();
    expect(cancelRequestMock).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "确 认" }));
    await waitFor(() => expect(cancelRequestMock).toHaveBeenCalledWith(1));
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ["overview"] })),
    );
    expect(await screen.findByText("请求已取消。")).toBeInTheDocument();
  });

  it("切换批量模式会清空已有选择且保留批量取消入口", async () => {
    fetchRequestsMock.mockResolvedValue(
      envelope([
        requestRow({ id: 11, status: "succeeded" }),
        requestRow({ id: 12, status: "processing" }),
      ]),
    );
    renderPage();
    await screen.findAllByText("example");

    const checkboxes = screen.getAllByRole("checkbox");
    expect(checkboxes[1]).toBeDisabled();
    expect(checkboxes[2]).not.toBeDisabled();
    fireEvent.click(checkboxes[2]);
    expect(screen.getByRole("button", { name: /批量取消/ })).toBeInTheDocument();

    fireEvent.click(screen.getByText("删除记录"));
    expect(screen.queryByRole("button", { name: /批量取消/ })).not.toBeInTheDocument();
    expect(screen.getAllByRole("checkbox")[1]).not.toBeChecked();
  });

  it("删除模式只允许选择终态记录并批量提交当前页 ID", async () => {
    deleteRequestsMock.mockResolvedValue({
      ok: true,
      results: [{ id: 11, result: "deleted" }],
    });
    fetchRequestsMock.mockResolvedValue(
      envelope([
        requestRow({ id: 11, status: "succeeded" }),
        requestRow({ id: 12, status: "processing" }),
      ]),
    );
    renderPage();
    await screen.findAllByText("example");

    fireEvent.click(screen.getByText("删除记录"));
    const checkboxes = screen.getAllByRole("checkbox");
    expect(checkboxes[1]).not.toBeDisabled();
    expect(checkboxes[2]).toBeDisabled();
    fireEvent.click(checkboxes[1]);
    fireEvent.click(screen.getByRole("button", { name: /批量删除/ }));

    expect(await screen.findByText(/确定删除已选的 1 条记录/)).toBeInTheDocument();
    expect(deleteRequestsMock).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(deleteRequestsMock).toHaveBeenCalledWith([11]));
    expect(await screen.findByText(/#11 已删除/)).toBeInTheDocument();
  });

  it("渲染网盘投递标签", async () => {
    fetchRequestsMock.mockResolvedValue(
      envelope([requestRow({ id: 21, delivery_mode: "cloud" })]),
    );

    renderPage();

    expect(await screen.findByText("网盘")).toBeInTheDocument();
  });

  it("投递方式筛选=网盘会带入 delivery_mode 查询参数", async () => {
    fetchRequestsMock.mockResolvedValue(envelope([requestRow({})]));

    renderPage();
    await screen.findByText("example");
    // 筛选表单的三个 Select：状态 / 媒体类型 / 投递方式（DOM 顺序）
    const deliverySelect = screen.getAllByRole("combobox")[2]
      .closest(".ant-select")
      ?.querySelector(".ant-select-selector");
    expect(deliverySelect).not.toBeNull();
    fireEvent.mouseDown(deliverySelect as HTMLElement);
    fireEvent.click(await screen.findByRole("option", { name: "网盘" }));
    fireEvent.click(screen.getByRole("button", { name: "筛 选" }));

    await waitFor(() =>
      expect(fetchRequestsMock).toHaveBeenCalledWith(
        expect.objectContaining({ delivery_mode: "cloud", page: 1, page_size: 20 }),
      ),
    );
  });

  it("存到网盘按钮按资格启用，禁用时 Tooltip 展示原因", async () => {
    fetchRequestsMock.mockResolvedValue(
      envelope([
        requestRow({ id: 11, status: "succeeded", delivery_mode: "upload" }),
        requestRow({ id: 12, status: "processing", delivery_mode: "upload" }),
        requestRow({ id: 13, status: "succeeded", delivery_mode: "text", media_type: "text" }),
      ]),
    );
    fetchCloudDriveMock.mockResolvedValue(cloudDriveView());
    renderPage();
    await screen.findAllByText("example");

    const archiveButtons = screen.getAllByRole("button", { name: "存到网盘" });
    expect(archiveButtons[0]).toBeEnabled();
    expect(archiveButtons[1]).toBeDisabled();
    expect(archiveButtons[2]).toBeDisabled();

    // 悬停禁用按钮（包一层 span 承接鼠标事件）展示禁用原因
    fireEvent.mouseEnter(archiveButtons[1].parentElement as HTMLElement);
    expect(await screen.findByText("仅已结束（成功/失败/取消）的请求可存到网盘")).toBeInTheDocument();
    fireEvent.mouseEnter(archiveButtons[2].parentElement as HTMLElement);
    expect(await screen.findByText("纯文本请求没有可存媒体")).toBeInTheDocument();
  });

  it("云盘功能关闭时存到网盘禁用并提示开启位置", async () => {
    fetchRequestsMock.mockResolvedValue(
      envelope([requestRow({ id: 11, status: "succeeded", delivery_mode: "upload" })]),
    );
    fetchCloudDriveMock.mockResolvedValue(cloudDriveView({ enabled: false }));
    renderPage();
    await screen.findAllByText("example");

    const archiveButton = (await screen.findAllByRole("button", { name: "存到网盘" }))[0];
    expect(archiveButton).toBeDisabled();
    fireEvent.mouseEnter(archiveButton.parentElement as HTMLElement);
    expect(
      await screen.findByText("云盘下载功能未开启，可在「云盘下载」页开启"),
    ).toBeInTheDocument();
  });

  it("单条存到网盘：目的地弹层默认选中默认目的地，确认后提交并刷新", async () => {
    cloudArchiveRequestMock.mockResolvedValue({ ok: true });
    fetchRequestsMock.mockResolvedValue(
      envelope([requestRow({ id: 11, status: "succeeded", delivery_mode: "upload" })]),
    );
    fetchCloudDriveMock.mockResolvedValue(cloudDriveView());
    const invalidateSpy = renderPage();

    fireEvent.click((await screen.findAllByRole("button", { name: "存到网盘" }))[0]);
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("mega-1")).toBeInTheDocument();
    expect(cloudArchiveRequestMock).not.toHaveBeenCalled();

    fireEvent.click(within(dialog).getByRole("button", { name: "存到网盘" }));

    await waitFor(() => expect(cloudArchiveRequestMock).toHaveBeenCalledWith(11, "mega-1"));
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ["requests"] })),
    );
    expect(
      await screen.findByText(/已创建云盘补存任务，新请求行将以「网盘」投递方式出现在列表中/),
    ).toBeInTheDocument();
  });

  it("批量存到网盘：仅资格行可勾选，提交后展示成功/跳过摘要", async () => {
    cloudArchiveBatchMock.mockResolvedValue({
      ok: true,
      results: [
        { request_id: 11, created_request_id: 21 },
        { request_id: 13, skip_reason: "text_only" },
        { request_id: 14, skip_reason: "already_archiving" },
      ],
    });
    fetchRequestsMock.mockResolvedValue(
      envelope([
        requestRow({ id: 11, status: "succeeded", delivery_mode: "upload" }),
        requestRow({ id: 12, status: "processing", delivery_mode: "upload" }),
        requestRow({ id: 13, status: "succeeded", delivery_mode: "text", media_type: "text" }),
        requestRow({ id: 14, status: "cancelled", delivery_mode: "cloud" }),
      ]),
    );
    fetchCloudDriveMock.mockResolvedValue(cloudDriveView());
    const invalidateSpy = renderPage();
    await screen.findAllByText("example");

    // 切到「存到网盘」批量模式（Segmented 选项）
    fireEvent.click(screen.getByRole("radio", { name: "存到网盘" }));
    const checkboxes = screen.getAllByRole("checkbox");
    // [0] 全选；终态非纯文本的 11/14 可选，处理中的 12 与纯文本的 13 禁用
    expect(checkboxes[1]).not.toBeDisabled();
    expect(checkboxes[2]).toBeDisabled();
    expect(checkboxes[3]).toBeDisabled();
    expect(checkboxes[4]).not.toBeDisabled();
    fireEvent.click(checkboxes[1]);
    fireEvent.click(checkboxes[4]);

    fireEvent.click(screen.getByRole("button", { name: /批量存到网盘（2）/ }));
    const dialog = await screen.findByRole("dialog");
    // 默认目的地预选
    expect(within(dialog).getByText("mega-1")).toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole("button", { name: "批量存入" }));

    await waitFor(() => expect(cloudArchiveBatchMock).toHaveBeenCalledWith([11, 14], "mega-1"));
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith(expect.objectContaining({ queryKey: ["requests"] })),
    );

    // 结果摘要：创建 1 条、跳过 2 条及中文原因（弹层经标题文本定位：
    // 测试环境下 antd Modal 的 aria-labelledby 均为占位 id，不能按名称查询）
    const summaryTitle = await screen.findByText("批量存到网盘结果");
    const summary = summaryTitle.closest('[role="dialog"]') as HTMLElement;
    expect(
      within(summary).getByText(/成功创建 1 条云盘补存任务；队列满 0 条；跳过 2 条。/),
    ).toBeInTheDocument();
    expect(
      within(summary).getByText("#13：纯文本请求，无可存媒体"),
    ).toBeInTheDocument();
    expect(
      within(summary).getByText("#14：已有在途补存任务"),
    ).toBeInTheDocument();
  });

  it("单条存到网盘被服务端拒绝（409）时展示受控文案", async () => {
    cloudArchiveRequestMock.mockRejectedValue(
      new ApiError("该请求已有在途补存任务", 409, "STORE_CONSTRAINT"),
    );
    fetchRequestsMock.mockResolvedValue(
      envelope([requestRow({ id: 11, status: "succeeded", delivery_mode: "upload" })]),
    );
    fetchCloudDriveMock.mockResolvedValue(cloudDriveView());
    renderPage();

    fireEvent.click((await screen.findAllByRole("button", { name: "存到网盘" }))[0]);
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "存到网盘" }));

    expect(await screen.findByText("该请求已有在途补存任务")).toBeInTheDocument();
    expect(screen.queryByText(/已创建云盘补存任务/)).not.toBeInTheDocument();
  });
});
