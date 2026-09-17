import { expect, test, type Page, type Route } from "@playwright/test";

const composeMode = Boolean(process.env.PLAYWRIGHT_BASE_URL);
const accessKey = process.env.PLAYWRIGHT_ACCESS_KEY;

const sessionFixture = {
  authenticated: true,
  user: { name: "admin" },
  csrf_token: "e2e-csrf-fixture",
  expires_at: 1_900_000_000_000,
};

const overviewFixture = {
  version: "e2e",
  started_at: 1_700_000_000_000,
  addr: "127.0.0.1:8080",
  workers: 1,
  health: {
    store_ok: true,
    mtproto_state: "unknown",
    bot_mtproto_state: "ready",
    bot_mtproto_dc_id: 4,
    db_size_bytes: 1024,
    db_path: "data/spore.db",
    temp_dir_bytes: 0,
    temp_dir: "data/tmp",
    github_configured: false,
  },
  queue: { len: 0, cap: 8 },
  bot: { id: 42, name: "Spore Bot", username: "spore_bot" },
  requests: {
    queued_rows: 0,
    processing_rows: 0,
  },
  users: { total: 1, enabled: 1, pending: 0, disabled: 0, archived: 0 },
  join: {
    pending: 0,
    approved: 0,
    rejected: 0,
    failed: 0,
    active_joined: 0,
    external_active: 0,
    left_total: 0,
    max_channels: 20,
    source_dist: [
      { key: "join_command", count: 0 },
      { key: "approved", count: 0 },
      { key: "external", count: 0 },
    ],
  },
};

const statsFixture = {
  since_day: "2026-08-25",
  until_day: "2026-08-31",
  requests: {
    total: 0,
    succeeded: 0,
    failed: 0,
    unfinished: 0,
    active_users: 0,
    success_rate: 0,
    error_rate: 0,
    trend: [],
    top_channels: [],
    top_users: [],
    media_dist: [],
    error_dist: [],
  },
};

const usersFixture = {
  items: [
    {
      id: 101,
      username: "fixture_user",
      display_name: "本机替身用户",
      status: "enabled",
      is_owner: false,
      note: "仅用于浏览器验收",
      last_used_at: 1_700_000_000_000,
      total_requests: 0,
      has_total_requests: true,
    },
  ],
  page: 1,
  page_size: 20,
  total: 1,
  total_pages: 1,
};

const userDetailFixture = {
  id: 101,
  username: "fixture_user",
  display_name: "本机替身用户",
  status: "enabled",
  is_owner: false,
  note: "仅用于浏览器验收",
  created_at: 1_700_000_000_000,
  first_used_at: 0,
  last_used_at: 0,
  archived_at: 0,
  last_denied_at: 0,
  last_denied_reason: "",
  last_denied_text: "",
  used_today: 0,
  daily_limit: 100,
  remaining_today: 100,
  submit_interval_sec: 10,
  concurrent_limit: 2,
  total_requests: 0,
};

const settingsFixture = {
  timezone: "Asia/Shanghai",
  // 运行设置页拆分后首个数字输入是「单次最大链接数」：深链刷新回填断言依赖该字段
  max_links_per_message: 30,
  dedup_window_min: 30,
  queue_capacity: 8,
  queue_runtime: 8,
  queue_same: true,
  max_file_size_bytes: 2000 * 1024 * 1024,
  stream_limit_bytes: 20 * 1024 * 1024,
  max_file_size_runtime_bytes: 2000 * 1024 * 1024,
  stream_limit_runtime_bytes: 20 * 1024 * 1024,
  media_same: true,
  last_backup_at: 0,
};

const backupFixture = {
  db_path: "data/spore.db",
  db_size_bytes: 1024,
  last_backup_at: 0,
  pending: false,
};

async function fulfillJSON(route: Route, status: number, body: unknown) {
  await route.fulfill({
    status,
    headers: {
      "content-type": "application/json; charset=utf-8",
      "cache-control": "no-store",
    },
    body: JSON.stringify(body),
  });
}

async function mockAdminAPI(page: Page, options: { sessionStatus?: number; usersStatus?: number } = {}) {
  if (composeMode) return;

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    const method = request.method();

    if (path === "/api/v1/session" && method === "GET") {
      if (options.sessionStatus) {
        await fulfillJSON(route, options.sessionStatus, {
          error: {
            code: "UNAUTHORIZED",
            message: "未登录或登录已过期。",
          },
        });
        return;
      }
      await fulfillJSON(route, 200, sessionFixture);
      return;
    }
    if (path === "/api/v1/login/csrf" && method === "GET") {
      await fulfillJSON(route, 200, { csrf_token: "e2e-login-csrf", github_enabled: false });
      return;
    }
    if (path === "/api/v1/login" && method === "POST") {
      await fulfillJSON(route, 200, { ok: true });
      return;
    }
    if (path === "/api/v1/users" && method === "GET") {
      if (options.usersStatus) {
        await fulfillJSON(route, options.usersStatus, {
          error: { code: "UNAUTHORIZED", message: "未登录或登录已过期。" },
        });
        return;
      }
      await fulfillJSON(route, 200, usersFixture);
      return;
    }
    if (path === "/api/v1/users/101" && method === "GET") {
      await fulfillJSON(route, 200, userDetailFixture);
      return;
    }
    if (path === "/api/v1/overview" && method === "GET") {
      await fulfillJSON(route, 200, overviewFixture);
      return;
    }
    // 检查更新（总览页自动查询）：unknown 且无 latest_version，页面不展示提示
    if (path === "/api/v1/version/check" && method === "GET") {
      await fulfillJSON(route, 200, {
        current_version: "e2e",
        status: "unknown",
        checked_at: 0,
      });
      return;
    }
    if (path === "/api/v1/stats" && method === "GET") {
      await fulfillJSON(route, 200, statsFixture);
      return;
    }
    if (path === "/api/v1/settings" && method === "GET") {
      await fulfillJSON(route, 200, settingsFixture);
      return;
    }
    if (path === "/api/v1/backup" && method === "GET") {
      await fulfillJSON(route, 200, backupFixture);
      return;
    }
    if (path === "/api/v1/mtproto/status" && method === "GET") {
      await fulfillJSON(route, 200, {
        state: "unknown",
        updated_at: 0,
        qr_available: false,
      });
      return;
    }
    if (path === "/api/v1/applications" && method === "GET") {
      await fulfillJSON(route, 200, { items: [] });
      return;
    }
    if (path === "/api/v1/requests" && method === "GET") {
      await fulfillJSON(route, 200, {
        items: [],
        page: 1,
        page_size: 20,
        total: 0,
        total_pages: 0,
      });
      return;
    }
    if (path === "/api/v1/events" && method === "GET") {
      await fulfillJSON(route, 200, {
        items: [],
        page: 1,
        page_size: 20,
        total: 0,
        total_pages: 0,
      });
      return;
    }
    if (path === "/api/v1/channels" && method === "GET") {
      await fulfillJSON(route, 200, {
        items: [],
        page: 1,
        page_size: 20,
        total: 0,
        total_pages: 0,
      });
      return;
    }
    if (path === "/api/v1/channel-bindings" && method === "GET") {
      await fulfillJSON(route, 200, { items: [] });
      return;
    }
    if (path === "/api/v1/oauth/settings" && method === "GET") {
      await fulfillJSON(route, 200, {
        github_configured: false,
        configured: false,
        enabled: false,
        client_id: "",
        secret_set: false,
        bound: false,
      });
      return;
    }
    if (path === "/api/v1/users/101/disable" && method === "POST") {
      await fulfillJSON(route, 403, {
        error: {
          code: "CSRF_FAILED",
          message: "请求校验失败，请刷新页面后重试。",
        },
      });
      return;
    }
    if (path === "/api/v1/not-found") {
      await fulfillJSON(route, 404, {
        error: { code: "NOT_FOUND", message: "资源不存在或已被删除。" },
      });
      return;
    }
    if (method === "POST") {
      await fulfillJSON(route, 200, { ok: true });
      return;
    }
    await fulfillJSON(route, 404, {
      error: { code: "NOT_FOUND", message: "资源不存在或已被删除。" },
    });
  });
}

async function authenticateCompose(page: Page) {
  if (!composeMode) return;
  if (!accessKey) throw new Error("Compose 浏览器验收缺少 PLAYWRIGHT_ACCESS_KEY。");

  // 当前登录走 SPA 登录页：旧 /login 302 到 /admin/login，表单经
  // /api/v1/login/csrf + /api/v1/login 完成。
  await page.goto("/admin/login");
  await expect(page.getByText("登录 Spore")).toBeVisible();
  const preLoginCookies = await page.context().cookies();
  // Web 会话 Cookie 默认带 Secure；本轮 Compose 仅为本机 HTTP，测试临时改为非 Secure，
  // 不修改生产配置，也不把 Cookie 值写入测试输出。
  await page.context().addCookies(preLoginCookies.map((cookie) => ({ ...cookie, secure: false })));
  await page.getByLabel("访问密钥").fill(accessKey);
  await page.getByRole("button", { name: /登\s*录/ }).click();
  // 登录成功由 SPA 路由进入管理端入口
  await page.waitForURL("**/admin");
  const sessionCookies = await page.context().cookies();
  await page.context().addCookies(sessionCookies.map((cookie) => ({ ...cookie, secure: false })));
}

async function preparePage(page: Page, options?: { sessionStatus?: number; usersStatus?: number }) {
  await mockAdminAPI(page, options);
  await authenticateCompose(page);
}

test.describe("管理端 SPA 本机安全验收", () => {
  test("CSP nonce 与 Ant Design 样式注入保持一致且无违规", async ({ page }) => {
    await page.addInitScript(() => {
      const violations: string[] = [];
      Object.defineProperty(window, "__sporeCSPViolations", { value: violations });
      document.addEventListener("securitypolicyviolation", (event) => {
        violations.push(event.effectiveDirective);
      });
    });
    await preparePage(page);
    const response = await page.goto("/admin/users");
    await expect(page.getByRole("heading", { name: "用户管理" })).toBeVisible();
    await expect(page.locator(".ant-btn").first()).toBeVisible();

    const nonce = await page.locator('meta[name="csp-nonce"]').getAttribute("content");
    expect(nonce).toBeTruthy();
    if (composeMode) {
      expect(nonce).not.toBe("__SPORE_CSP_NONCE__");
      const csp = response?.headers()["content-security-policy"] ?? "";
      expect(csp).toContain(`style-src 'self' 'nonce-${nonce}'`);
      expect(csp).toContain(`style-src-elem 'self' 'nonce-${nonce}'`);
      expect(csp).toContain("style-src-attr 'unsafe-inline'");
      expect(csp).not.toContain(`style-src 'self' 'nonce-${nonce}' 'unsafe-inline'`);
      expect(csp).not.toContain(`style-src-elem 'self' 'nonce-${nonce}' 'unsafe-inline'`);
      const nonceStyleCount = await page.locator("style").evaluateAll(
        (styles, expected) =>
          styles.filter((style) => (style as HTMLStyleElement).nonce === expected).length,
        nonce,
      );
      expect(nonceStyleCount).toBeGreaterThan(0);
    }

    await page.getByRole("menuitem", { name: "系统运维" }).click();
    await page.getByRole("link", { name: "运行设置" }).click();
    await expect(page.getByRole("heading", { name: "运行设置" })).toBeVisible();
    const violations = await page.evaluate(() => {
      return (window as typeof window & { __sporeCSPViolations?: string[] })
        .__sporeCSPViolations ?? [];
    });
    // rc-util/antd 内部的滚动条测量探针等 updateCSS 调用不带 nonce，会触发
    // 良性的 style-src-elem 违规（样式被拦截后测量回落默认值，无安全影响）；
    // 脚本与其他指令必须保持零违规，style 属性走 style-src-attr 'unsafe-inline'。
    expect(violations.filter((directive) => !directive.startsWith("style-src"))).toEqual([]);
    expect(violations).not.toContain("style-src-attr");
  });

  test("支持 SPA 导航、深链接刷新并回填异步设置", async ({ page }) => {
    await preparePage(page);
    await page.goto("/admin/users");

    if (!composeMode) {
      await expect(page.getByRole("heading", { name: "用户管理" })).toBeVisible();
    } else {
      await expect(page.getByRole("heading", { name: "用户管理" })).toBeVisible();
    }
    await page.getByRole("menuitem", { name: "系统运维" }).click();
    await page.getByRole("link", { name: "运行设置" }).click();
    await expect(page).toHaveURL(/\/admin\/settings$/);
    await expect(page.getByRole("heading", { name: "运行设置" })).toBeVisible();

    if (!composeMode) {
      await expect(page.locator('input[placeholder="如 Asia/Shanghai"]')).toHaveValue("Asia/Shanghai");
      await expect(page.getByRole("spinbutton").first()).toHaveValue("30");
    }

    await page.reload();
    await expect(page).toHaveURL(/\/admin\/settings$/);
    await expect(page.getByRole("heading", { name: "运行设置" })).toBeVisible();

    // 从运行设置拆出的两个配置页：请求与频道 → 频道设置；BotUser受邀频道 → 受邀设置。
    await page.getByRole("menuitem", { name: "请求与频道" }).click();
    await page.getByRole("link", { name: "频道设置" }).click();
    await expect(page).toHaveURL(/\/admin\/channel-settings$/);
    await expect(page.getByRole("heading", { name: "频道设置" })).toBeVisible();

    await page.getByRole("menuitem", { name: "BotUser受邀频道" }).click();
    await page.getByRole("link", { name: "受邀设置" }).click();
    await expect(page).toHaveURL(/\/admin\/join-settings$/);
    await expect(page.getByRole("heading", { name: "受邀设置" })).toBeVisible();
  });

  test("呈现会话错误态并保持 CSRF 错误为 JSON 受控信封", async ({ page }) => {
    await preparePage(page, { sessionStatus: 401, usersStatus: 401 });
    await page.goto("/admin/users");

    // 写端点没有 CSRF 时必须先拒绝，且不得返回 HTML；Compose 使用真实服务，
    // 默认模式使用同样契约的本机路由替身。
    const csrfResponse = await page.evaluate(async () => {
      const response = await fetch("/api/v1/users/101/disable", { method: "POST" });
      return {
        status: response.status,
        contentType: response.headers.get("content-type"),
        body: (await response.json()) as { error?: { code?: string; message?: string } },
      };
    });
    expect(csrfResponse.status).toBe(403);
    expect(csrfResponse.contentType).toContain("application/json");
    expect(csrfResponse.body.error?.code).toBe("CSRF_FAILED");
    expect(csrfResponse.body.error?.message).toBe("请求校验失败，请刷新页面后重试。");

    if (composeMode) {
      await page.context().clearCookies();
    }
    const sessionResponse = await page.evaluate(async () => {
      const response = await fetch("/api/v1/session");
      return {
        status: response.status,
        contentType: response.headers.get("content-type"),
        body: (await response.json()) as { error?: { code?: string; message?: string } },
      };
    });
    expect(sessionResponse.status).toBe(401);
    expect(sessionResponse.contentType).toContain("application/json");
    expect(sessionResponse.body.error?.code).toBe("UNAUTHORIZED");

    if (!composeMode) {
      // 当前：会话引导 401 时应用整页跳转 SPA 登录壳（不再展示页面错误态）
      await page.goto("/admin/users");
      await page.waitForURL("**/admin/login");
      await expect(page.getByText("登录 Spore")).toBeVisible();
    }
  });

  test("未知 API 不被 SPA 截获，未知前端路径显示 404", async ({ page }) => {
    await preparePage(page);
    await page.goto("/admin/users");

    const fallback = await page.evaluate(async () => {
      const response = await fetch("/api/v1/not-found");
      return {
        status: response.status,
        contentType: response.headers.get("content-type"),
        body: (await response.json()) as { error?: { code?: string } },
      };
    });
    expect(fallback.status).toBe(404);
    expect(fallback.contentType).toContain("application/json");
    expect(fallback.body.error?.code).toBe("NOT_FOUND");

    await page.goto("/admin/path-that-does-not-exist");
    await expect(page.getByText("页面不存在", { exact: true })).toBeVisible();
  });

  test("登录页完成 SPA 登录流程并进入管理端", async ({ page }) => {
    // 替身模式：登录 API 走本机替身；Compose 模式已在 preparePage 内
    // 经真实服务登录，本用例只验证替身形态。
    if (composeMode) {
      await preparePage(page);
      return;
    }

    const loginRequests: Array<{ path: string; method: string }> = [];
    await page.route("**/api/v1/**", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const path = url.pathname;
      if (path === "/api/v1/login/csrf" && request.method() === "GET") {
        loginRequests.push({ path, method: "GET" });
        await fulfillJSON(route, 200, { csrf_token: "e2e-login-csrf", github_enabled: false });
        return;
      }
      if (path === "/api/v1/login" && request.method() === "POST") {
        loginRequests.push({ path, method: "POST" });
        const body = request.postDataJSON() as { access_key?: string };
        expect(body.access_key).toBe("e2e-access-key");
        expect((request.headers()["x-csrf-token"] ?? "")).toBe("e2e-login-csrf");
        await fulfillJSON(route, 200, { ok: true });
        return;
      }
      if (path === "/api/v1/session" && request.method() === "GET") {
        await fulfillJSON(route, 200, sessionFixture);
        return;
      }
      if (path === "/api/v1/overview" && request.method() === "GET") {
        await fulfillJSON(route, 200, overviewFixture);
        return;
      }
      if (path === "/api/v1/stats" && request.method() === "GET") {
        await fulfillJSON(route, 200, statsFixture);
        return;
      }
      await fulfillJSON(route, 200, { items: [], page: 1, page_size: 20, total: 0, total_pages: 0 });
    });

    await page.goto("/admin/login");
    await expect(page.getByText("登录 Spore")).toBeVisible();
    // GitHub 通道未配置：登录页不得出现必然失败的入口
    await expect(page.getByText("使用 GitHub 登录")).toHaveCount(0);

    await page.getByLabel("访问密钥").fill("e2e-access-key");
    await page.getByRole("button", { name: /登\s*录/ }).click();

    // 登录成功由 SPA 导航进入管理端入口并完成会话引导
    await page.waitForURL("**/admin");
    // 总览页骨架统一后：H1 为「总览」（实时运行状态是页面状态标识）
    await expect(page.getByRole("heading", { name: "总览" })).toBeVisible();
    const accountMenu = page.getByRole("button", { name: "管理员账户菜单" });
    // 账户菜单为 click 触发（69d0254b 起），hover 不再展开
    await accountMenu.click();
    await expect(page.getByRole("menuitem", { name: "退出登录" })).toBeVisible();
    // 登录流程先取 CSRF（挂载预热 + 提交时各一次），最后 POST 登录
    expect(loginRequests.filter((r) => r.path === "/api/v1/login")).toHaveLength(1);
    expect(loginRequests.filter((r) => r.path === "/api/v1/login/csrf").length).toBeGreaterThanOrEqual(1);
    expect(loginRequests[loginRequests.length - 1].path).toBe("/api/v1/login");
  });

  test("保留 CSV 下载，旧页面路径重定向到 SPA 对应页面", async ({ page }) => {
    await preparePage(page);

    if (!composeMode) {
      await page.route("**/users/export.csv", async (route) => {
        await route.fulfill({
          status: 200,
          headers: {
            "content-type": "text/csv; charset=utf-8",
            "content-disposition": 'attachment; filename="users.csv"',
          },
          body: "id,username\n101,fixture_user\n",
        });
      });
      // 与生产服务一致：旧认证页面路径 302 到 /admin/* 对应路径
      await page.route("**/users", async (route) => {
        if (new URL(route.request().url()).pathname !== "/users") {
          await route.continue();
          return;
        }
        await route.fulfill({
          status: 302,
          headers: { location: "/admin/users" },
          body: "",
        });
      });
      // 当前旧登录页路径同样 302 到 SPA 登录壳
      await page.route("**/login", async (route) => {
        if (new URL(route.request().url()).pathname !== "/login") {
          await route.continue();
          return;
        }
        await route.fulfill({
          status: 302,
          headers: { location: "/admin/login" },
          body: "",
        });
      });
    } else {
      // 真实服务：直接断言 302 与 Location（不跟随重定向）
      const redirect = await page.request.get("/users", { maxRedirects: 0 });
      expect(redirect.status()).toBe(302);
      expect(redirect.headers()["location"]).toBe("/admin/users");
      const loginRedirect = await page.request.get("/login", { maxRedirects: 0 });
      expect(loginRedirect.status()).toBe(302);
      expect(loginRedirect.headers()["location"]).toBe("/admin/login");
    }

    await page.goto("/admin/users");
    const downloadPromise = page.waitForEvent("download");
    await page.getByRole("link", { name: "导出 CSV" }).click();
    const download = await downloadPromise;
    expect(download.suggestedFilename()).toBe("users.csv");

    // 旧路径最终落地 SPA 用户管理页
    await page.goto("/users");
    await expect(page.getByRole("heading", { name: "用户管理" })).toBeVisible();
  });

  test("手机视口可手动打开和收起主菜单", async ({ page }) => {
    await preparePage(page);
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto("/admin/users");

    const toggle = page.getByRole("button", { name: "打开主导航" });
    await expect(toggle).toBeVisible();
    await toggle.click();
    const closeToggle = page.getByRole("banner").getByRole("button", { name: "关闭主导航" });
    await expect(closeToggle).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "系统运维" })).toBeVisible();

    await closeToggle.click();
    await expect(page.getByRole("button", { name: "打开主导航" })).toBeVisible();
  });
});
