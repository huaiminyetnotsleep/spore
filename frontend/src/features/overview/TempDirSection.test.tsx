import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { App as AntApp } from "antd";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setCSRFToken } from "../../api/session";
import { TempDirSection } from "./TempDirSection";

const fetchMock = vi.fn();

const fileA = { path: "nested/a.mkv", size_bytes: 1024, modified_at: 1_700_000_000_000 };
const fileB = { path: "b.bin", size_bytes: 2048, modified_at: 1_700_000_000_000 };
let currentFiles = [fileA, fileB];

function renderSection(embedded = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rendered = render(
    <QueryClientProvider client={client}>
      <AntApp>
        <TempDirSection embedded={embedded} />
      </AntApp>
    </QueryClientProvider>,
  );
  return { ...rendered, client };
}

describe("临时目录管理", () => {
  beforeEach(() => {
    currentFiles = [fileA, fileB];
    fetchMock.mockReset();
    fetchMock.mockImplementation(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
      if (url === "/api/v1/temp-dir" && (!init || init.method === undefined)) {
        const files = currentFiles;
        return new Response(JSON.stringify({
          files,
          file_count: files.length,
          total_bytes: files.reduce((sum, file) => sum + file.size_bytes, 0),
        }), { status: 200, headers: { "content-type": "application/json" } });
      }
      if (url === "/api/v1/temp-dir/clear" && init?.method === "POST") {
        expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("csrf-test-token");
        currentFiles = [];
        return new Response(JSON.stringify({
          ok: true,
          temp_dir: { files: [], file_count: 0, total_bytes: 0 },
        }), { status: 200, headers: { "content-type": "application/json" } });
      }
      if (url === "/api/v1/temp-dir/delete" && init?.method === "POST") {
        expect(new Headers(init.headers).get("X-CSRF-Token")).toBe("csrf-test-token");
        const body = JSON.parse(String(init.body)) as { paths: string[] };
        currentFiles = currentFiles.filter((file) => !body.paths.includes(file.path));
        return new Response(JSON.stringify({
          ok: true,
          temp_dir: {
            files: currentFiles,
            file_count: currentFiles.length,
            total_bytes: currentFiles.reduce((sum, file) => sum + file.size_bytes, 0),
          },
        }), { status: 200, headers: { "content-type": "application/json" } });
      }
      throw new Error(`未匹配的 fetch 请求: ${url}`);
    });
    setCSRFToken("csrf-test-token");
    vi.stubGlobal("fetch", fetchMock);
  });

  afterEach(() => {
    setCSRFToken("");
    vi.unstubAllGlobals();
  });

  it("列出文件路径、单文件大小、总大小和空状态", async () => {
    const { client } = renderSection();

    expect(await screen.findByText("nested/a.mkv")).toBeInTheDocument();
    expect(screen.getByText(/占用 3\.00 KiB/)).toBeInTheDocument();
    expect(screen.getByText("1.00 KiB")).toBeInTheDocument();
    expect(screen.getByText("2.00 KiB")).toBeInTheDocument();

    currentFiles = [];
    await client.invalidateQueries({ queryKey: ["temp-dir"] });
    expect(await screen.findByRole("status")).toHaveTextContent("没有可列出的普通文件");
  });

  it("嵌入抽屉时不渲染额外卡片标题，并沿统一列表布局排列", async () => {
    const { container } = renderSection(true);
    expect(await screen.findByText("nested/a.mkv")).toBeInTheDocument();
    expect(container.querySelector(".page-section")).toBeNull();
    expect(container.querySelector(".filter-bar")).not.toBeNull();
    expect(screen.getByRole("button", { name: /刷新/ })).toBeInTheDocument();
  });

  it("按文件名关键字即时过滤列表", async () => {
    renderSection();
    await screen.findByText("nested/a.mkv");

    fireEvent.change(screen.getByPlaceholderText("文件名"), { target: { value: "b.bin" } });

    await waitFor(() => expect(screen.queryByText("nested/a.mkv")).not.toBeInTheDocument());
    expect(screen.getByText("b.bin")).toBeInTheDocument();
  });

  it("确认后删除单个文件", async () => {
    renderSection();
    await screen.findByText("nested/a.mkv");
    const row = screen.getByRole("row", { name: /nested\/a\.mkv/ });
    expect(row.querySelector(".row-actions")).not.toBeNull();
    fireEvent.click(within(row).getByRole("button", { name: "delete" }));

    const dialog = await screen.findByRole("dialog", { name: "删除临时文件" });
    fireEvent.click(within(dialog).getByRole("button", { name: /删\s*除/ }));

    await waitFor(() => expect(currentFiles).toHaveLength(1));
    const deleteCall = fetchMock.mock.calls.find(([input]) => String(input) === "/api/v1/temp-dir/delete");
    expect(JSON.parse(String(deleteCall?.[1]?.body))).toEqual({ paths: ["nested/a.mkv"] });
  });

  it("确认后批量删除勾选的文件并刷新列表", async () => {
    renderSection();
    await screen.findByText("nested/a.mkv");

    const checkboxes = screen.getAllByRole("checkbox");
    fireEvent.click(checkboxes[1]);
    fireEvent.click(checkboxes[2]);
    fireEvent.click(screen.getByRole("button", { name: /批量删除（2）/ }));

    const dialog = await screen.findByRole("dialog", { name: "批量删除临时文件" });
    fireEvent.click(within(dialog).getByRole("button", { name: /删\s*除/ }));

    await waitFor(() => expect(currentFiles).toHaveLength(0));
    expect(await screen.findByRole("status")).toHaveTextContent("没有可列出的普通文件");
    expect(fetchMock.mock.calls.some(([input]) => String(input) === "/api/v1/temp-dir/delete")).toBe(true);
  });

  it("确认后清空整个临时目录", async () => {
    renderSection();
    await screen.findByText("nested/a.mkv");
    fireEvent.click(screen.getByRole("button", { name: /清理全部/ }));

    const dialog = await screen.findByRole("dialog", { name: "清理临时目录" });
    fireEvent.click(within(dialog).getByRole("button", { name: /清\s*理/ }));

    await waitFor(() => expect(currentFiles).toHaveLength(0));
    expect(await screen.findByRole("status")).toHaveTextContent("没有可列出的普通文件");
    expect(fetchMock.mock.calls.some(([input]) => String(input) === "/api/v1/temp-dir/clear")).toBe(true);
  });
});
