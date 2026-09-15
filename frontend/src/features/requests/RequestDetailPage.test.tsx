import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { App as AntApp } from "antd";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { fetchRequestDetail, type RequestDetail } from "../../api/admin";
import { RequestDetailPage } from "./RequestDetailPage";

vi.mock("../../api/admin", async () => {
  const actual = await vi.importActual<typeof import("../../api/admin")>("../../api/admin");
  return {
    ...actual,
    fetchRequestDetail: vi.fn(),
  };
});

const fetchRequestDetailMock = vi.mocked(fetchRequestDetail);

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
});
