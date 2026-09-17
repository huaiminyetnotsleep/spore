/**
 * 登录页组件测试：渲染、登录成功跳转 /admin、401/403 错误文案。
 * 网络层以 fetch stub 按路径模拟 /api/v1/login/csrf 与 /api/v1/login
 * （登录 CSRF 在挂载预热与提交时各取一次，同一 Cookie 返回同一 token）。
 */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LoginPage } from "./LoginPage";

function jsonResponse(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}

/** 按路径模拟登录 API；calls 记录 (path, init) 供断言。 */
function stubLoginAPI(options: {
  csrfToken?: string;
  githubEnabled?: boolean;
  loginStatus?: number;
  loginBody?: unknown;
}): { calls: Array<{ path: string; init: RequestInit }> } {
  const calls: Array<{ path: string; init: RequestInit }> = [];
  const csrfToken = options.csrfToken ?? "token-1";
  vi.stubGlobal(
    "fetch",
    vi.fn().mockImplementation((path: string, init: RequestInit = {}) => {
      calls.push({ path: String(path), init });
      if (path.includes("/api/v1/login/csrf")) {
        return Promise.resolve(
          jsonResponse(200, { csrf_token: csrfToken, github_enabled: options.githubEnabled ?? false }),
        );
      }
      if (path.endsWith("/api/v1/login")) {
        return Promise.resolve(
          jsonResponse(options.loginStatus ?? 200, options.loginBody ?? { ok: true }),
        );
      }
      return Promise.resolve(jsonResponse(404, { error: { code: "NOT_FOUND", message: "x" } }));
    }),
  );
  return { calls };
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/login"]}>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          {/* 登录成功跳转应用内 /（管理端入口）的观察点：目标路由渲染出占位 */}
          <Route path="/" element={<span>管理端工作台</span>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function fillAndSubmit(key = "test-key") {
  fireEvent.change(await screen.findByLabelText("访问密钥"), { target: { value: key } });
  fireEvent.click(screen.getByRole("button", { name: /登\s*录/ }));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("登录页", () => {
  it("渲染密钥输入与登录按钮；GitHub 已配置时展示入口", async () => {
    stubLoginAPI({ githubEnabled: true });

    renderPage();

    // GitHub 登录为次操作按钮（href 渲染为链接），可访问名与目的地保持不变
    const githubLink = await screen.findByRole("link", { name: "使用 GitHub 登录" });
    expect(githubLink).toHaveAttribute("href", "/auth/github");
    expect(screen.getByLabelText("访问密钥")).toBeInTheDocument();
  });

  it("登录成功后跳转 /admin", async () => {
    stubLoginAPI({});

    renderPage();
    await fillAndSubmit();

    expect(await screen.findByText("管理端工作台")).toBeInTheDocument();
  });

  it("提交请求携带登录 CSRF 请求头与密钥请求体", async () => {
    const { calls } = stubLoginAPI({ csrfToken: "token-9" });

    renderPage();
    await fillAndSubmit();
    await screen.findByText("管理端工作台");

    const login = calls.find(({ path }) => path.endsWith("/api/v1/login"));
    expect(login).toBeTruthy();
    // apiRequest 会把 headers 归一为 Headers 实例
    const headers = new Headers(login!.init.headers);
    expect(headers.get("X-CSRF-Token")).toBe("token-9");
    expect(login!.init.body).toBe(JSON.stringify({ access_key: "test-key" }));
  });

  it("密钥错误（401）展示服务端受控文案", async () => {
    stubLoginAPI({
      loginStatus: 401,
      loginBody: { error: { code: "WEB_AUTH_FAILED", message: "登录失败：访问密钥或账号不正确。" } },
    });

    renderPage();
    await fillAndSubmit();

    expect(await screen.findByRole("alert")).toHaveTextContent("登录失败：访问密钥或账号不正确。");
    // 停留在登录页
    expect(screen.getByLabelText("访问密钥")).toBeInTheDocument();
  });

  it("CSRF 失败（403）展示服务端受控文案", async () => {
    stubLoginAPI({
      loginStatus: 403,
      loginBody: {
        error: { code: "WEB_CSRF_INVALID", message: "请求校验失败，请刷新页面后重试。" },
      },
    });

    renderPage();
    await fillAndSubmit();

    expect(await screen.findByRole("alert")).toHaveTextContent("请求校验失败，请刷新页面后重试。");
  });

  it("空密钥提交不发起登录请求", async () => {
    const { calls } = stubLoginAPI({});

    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /登\s*录/ }));

    await waitFor(() => expect(screen.getByText("请输入访问密钥。")).toBeInTheDocument());
    expect(calls.filter(({ path }) => path.endsWith("/api/v1/login"))).toHaveLength(0);
  });
});
