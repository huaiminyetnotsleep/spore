import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "../../api/client";
import { fetchRequestDetail, fetchSettings, type RequestDetail } from "../../api/admin";
import { cancelRequest, dumpBackfillRequest, retryRequest } from "../../api/mutations";
import { RequestDetailPage } from "./RequestDetailPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchRequestDetail: vi.fn(),
    fetchSettings: vi.fn(),
  };
});

vi.mock("../../api/mutations", async () => {
  const actual = await vi.importActual<typeof import("../../api/mutations")>("../../api/mutations");
  return {
    ...actual,
    dumpBackfillRequest: vi.fn(),
    retryRequest: vi.fn(),
    cancelRequest: vi.fn(),
  };
});

const fetchRequestDetailMock = vi.mocked(fetchRequestDetail);
const fetchSettingsMock = vi.mocked(fetchSettings);
const dumpBackfillRequestMock = vi.mocked(dumpBackfillRequest);
const retryRequestMock = vi.mocked(retryRequest);
const cancelRequestMock = vi.mocked(cancelRequest);

function detail(overrides: Partial<RequestDetail> = {}): RequestDetail {
  return {
    id: 1,
    user_id: 301,
    username: "tester",
    display_name: "测试用户",
    source_kind: "public",
    channel_key: "example",
    message_id: 7,
    message_url: "https://t.me/example/7",
    channel_link_text: "example#7",
    status: "succeeded",
    attempt: 1,
    attempt_max: 3,
    error_code: "",
    error_text: "",
    media_type: "album",
    media_types: ["photo", "video"],
    source_media_dc_ids: [2, 4],
    bot_id: 0,
    bot_username: "",
    delivery_mode: "upload",
    file_name: "photo.jpg",
    file_size: 600,
    requested_at: 1756598400000,
    queued_at: 1756598400100,
    started_at: 1756598400200,
    finished_at: 1756598401000,
    duration_ms: 800,
    parent_request_id: 0,
    cloud_uploads: [],
    ...overrides,
  };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AntApp component={false}>
        <MemoryRouter initialEntries={["/requests/1"]}>
          <Routes>
            <Route path="/requests/:id" element={<RequestDetailPage />} />
          </Routes>
        </MemoryRouter>
      </AntApp>
    </QueryClientProvider>,
  );
}

describe("请求记录详情页", () => {
  beforeEach(() => {
    fetchRequestDetailMock.mockReset();
    fetchSettingsMock
      .mockReset()
      .mockResolvedValue({ dump_channel_id: -1001234567890 } as never);
    dumpBackfillRequestMock.mockReset();
    retryRequestMock.mockReset();
    cancelRequestMock.mockReset();
  });

  it("渲染唯一 H1「请求详情」，返回入口为按钮且取消/转存/重试集中在详情操作区", async () => {
    fetchRequestDetailMock.mockResolvedValue(detail({ status: "failed" }));

    renderPage();

    expect(await screen.findByRole("heading", { level: 1, name: "请求详情" })).toBeInTheDocument();
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    // 返回请求记录保持原可访问名，但不再是 <Link><Button/></Link> 嵌套
    const backButton = screen.getByRole("button", { name: "返回请求记录" });
    expect(backButton.closest("a")).toBeNull();
    // 详情操作分区存在，失败请求的重试入口在其中
    expect(await screen.findByText("详情操作")).toBeInTheDocument();
    expect(screen.getByText("该请求失败，可受控重试")).toBeInTheDocument();
  });

  it("404 时展示详情未找到", async () => {
    fetchRequestDetailMock.mockRejectedValue(new ApiError("请求不存在", 404, "NOT_FOUND"));

    renderPage();

    expect(await screen.findByText("请求不存在")).toBeInTheDocument();
    expect(screen.queryByText("详情操作")).not.toBeInTheDocument();
  });

  it("失败请求重试：确认后提交（不扣额度文案保留）并提示成功", async () => {
    retryRequestMock.mockResolvedValue({ ok: true } as never);
    fetchRequestDetailMock.mockResolvedValue(detail({ status: "failed" }));

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "重 试" }));
    expect(await screen.findByText("确定重试该请求？不扣减额度，累计尝试 +1。")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "确 认" }));

    await waitFor(() => expect(retryRequestMock).toHaveBeenCalledWith(1));
    expect(await screen.findByText("已重新入队（不扣减额度）")).toBeInTheDocument();
  });

  it("处理中请求取消：warning 意图确认后提交", async () => {
    cancelRequestMock.mockResolvedValue({ ok: true } as never);
    fetchRequestDetailMock.mockResolvedValue(detail({ status: "processing" }));

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "取消请求" }));
    const confirmButton = await screen.findByRole("button", { name: "确 认" });
    // 取消是可恢复的状态变更：确认按钮非 danger（warning 意图）
    expect(confirmButton).not.toHaveClass("ant-btn-dangerous");
    fireEvent.click(confirmButton);

    await waitFor(() => expect(cancelRequestMock).toHaveBeenCalledWith(1));
    expect(await screen.findByText("请求已取消。")).toBeInTheDocument();
  });

  it("展示相册成员类型和独立的源媒体 DC", async () => {
    fetchRequestDetailMock.mockResolvedValue(detail());

    renderPage();

    expect(await screen.findByText("相册内容")).toBeInTheDocument();
    expect(screen.getByText("photo + video")).toBeInTheDocument();
    expect(screen.getByText("DC 2、DC 4")).toBeInTheDocument();
  });

  it("云盘请求展示「云盘上传」区块：逐文件记录与补存来源跳转", async () => {
    fetchRequestDetailMock.mockResolvedValue(
      detail({
        delivery_mode: "cloud",
        parent_request_id: 12,
        cloud_uploads: [
          {
            destination: "mega-1",
            remote_path: "spore/example/2026-09-09/7/01_video.mp4",
            file_name: "01_video.mp4",
            status: "succeeded",
            error_code: "",
            bytes: 2048,
            created_at: 1757366400000,
            finished_at: 1757366460000,
          },
          {
            destination: "mega-1",
            remote_path: "spore/example/2026-09-09/7/02_photo.jpg",
            file_name: "02_photo.jpg",
            status: "failed",
            error_code: "CLOUD_UPLOAD_FAILED",
            bytes: 512,
            created_at: 1757366400000,
            finished_at: 1757366420000,
          },
        ],
      }),
    );

    renderPage();

    expect(await screen.findByText("云盘上传（2 个文件）")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "远端路径" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "目的地" })).toBeInTheDocument();
    expect(screen.getByText("spore/example/2026-09-09/7/01_video.mp4")).toBeInTheDocument();
    // 两条上传记录的目的地列
    expect(screen.getAllByText("mega-1")).toHaveLength(2);
    // 上传状态 Tag（与请求状态 Tag 同文案，用重复计数断言）
    expect(screen.getAllByText("成功").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByText("失败")).toBeInTheDocument();
    expect(screen.getByText("CLOUD_UPLOAD_FAILED")).toBeInTheDocument();
    expect(screen.getByText("2.00 KiB")).toBeInTheDocument();
    expect(screen.getByText("512 B")).toBeInTheDocument();
    // 补存来源链接跳转原请求详情
    expect(screen.getByText("补存自 #12")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "补存自 #12" })).toHaveAttribute(
      "href",
      "/requests/12",
    );
  });

  it("普通请求（非网盘且无上传记录）不渲染云盘上传区块", async () => {
    fetchRequestDetailMock.mockResolvedValue(detail());

    renderPage();

    expect(await screen.findByText("相册内容")).toBeInTheDocument();
    expect(screen.queryByText(/云盘上传/)).not.toBeInTheDocument();
    expect(screen.queryByText(/补存自/)).not.toBeInTheDocument();
  });

  it("终态记录展示转存入口：确认后提交并提示成功", async () => {
    dumpBackfillRequestMock.mockResolvedValue({ ok: true });
    fetchRequestDetailMock.mockResolvedValue(detail({ status: "failed" }));

    renderPage();

    fireEvent.click(await screen.findByRole("button", { name: "转存缓存频道" }));
    fireEvent.click(await screen.findByRole("button", { name: "确 认" }));

    await waitFor(() => expect(dumpBackfillRequestMock).toHaveBeenCalledWith(1));
    expect(
      await screen.findByText(/已创建缓存补写任务，完成后副本写入缓存频道/),
    ).toBeInTheDocument();
  });

  it("缓存频道未配置时转存入口禁用", async () => {
    fetchSettingsMock.mockResolvedValue({ dump_channel_id: 0 } as never);
    fetchRequestDetailMock.mockResolvedValue(detail({ status: "succeeded" }));

    renderPage();

    expect(await screen.findByText(/缓存频道未配置，可先在「运行设置」页配置缓存频道/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "转存缓存频道" })).toBeDisabled();
  });

  it("处理中记录不展示转存入口", async () => {
    fetchRequestDetailMock.mockResolvedValue(detail({ status: "processing" }));

    renderPage();

    expect(await screen.findByText("该请求仍在执行，可以取消")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "转存缓存频道" })).not.toBeInTheDocument();
  });
});
