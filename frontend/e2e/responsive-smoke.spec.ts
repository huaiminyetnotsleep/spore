import { expect, test, type Page, type Route } from "@playwright/test";

/**
 * 全路由响应式 smoke（实施计划 8.2）：
 * - 400px 覆盖 23 个受保护页面 + 登录 + 通配 404：页面容器无横向溢出，
 *   页面操作区 / 筛选 / 表单主按钮可见且落在视口宽度内；
 * - 宽表断言滚动发生在表格容器（.ant-table-content）内部；
 * - 1440 / 1024 / 768 覆盖代表性列表、详情、图表与设置页；
 * - 详情路由使用替身数据提供的稳定样例 ID/key（101 / 9001 / fixture-channel），
 *   不访问字面量 :id/:key。
 * 结构与可操作性断言（非像素快照），与设计文档 7.2 一致。
 */

const PHONE_VIEWPORT = { width: 400, height: 850 } as const;

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
    mtproto_state: "offline",
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
  requests: { queued_rows: 0, processing_rows: 0 },
  users: { total: 1, enabled: 1, pending: 1, disabled: 0, archived: 0 },
  join: {
    pending: 1,
    approved: 0,
    rejected: 0,
    failed: 0,
    active_joined: 1,
    external_active: 0,
    left_total: 0,
    max_channels: 20,
    source_dist: [
      { key: "join_command", count: 0 },
      { key: "approved", count: 1 },
      { key: "external", count: 0 },
    ],
  },
};

const versionCheckFixture = {
  current_version: "e2e",
  status: "unknown",
  checked_at: 0,
};

function systemMetricsPoints() {
  const now = 1_700_000_000_000;
  return Array.from({ length: 12 }, (_, i) => ({
    at: now + i * 2000,
    rss_bytes: 220 * 1024 * 1024 + i * 1024 * 1024,
    temp_dir_bytes: 1024 * 1024,
    download_bytes_per_second: 1024 * 1024,
    upload_bytes_per_second: 512 * 1024,
    cpu_percent: 5 + i,
  }));
}

const statsFixture = {
  since_day: "2026-08-25",
  until_day: "2026-08-31",
  requests: {
    total: 12,
    succeeded: 9,
    failed: 2,
    unfinished: 1,
    active_users: 1,
    success_rate: 0.818,
    error_rate: 0.182,
    trend: [
      { day: "2026-08-30", total: 5, succeeded: 4, failed: 1, error_rate: 0.2 },
      { day: "2026-08-31", total: 7, succeeded: 5, failed: 1, error_rate: null },
    ],
    top_channels: [
      {
        key: "fixture-channel",
        total: 12,
        succeeded: 9,
        failed: 2,
        success_rate: 0.818,
        last_requested_at: 1_700_000_000_000,
      },
    ],
    top_users: [
      {
        id: 101,
        username: "fixture_user",
        display_name: "替身用户",
        total: 12,
        succeeded: 9,
        failed: 2,
        success_rate: 0.818,
        last_requested_at: 1_700_000_000_000,
      },
    ],
    media_dist: [
      { key: "video", count: 7 },
      { key: "photo", count: 5 },
    ],
    error_dist: [{ key: "FLOOD_WAIT", count: 2, ratio: 0.167 }],
    dc_dist: [
      { key: "2", count: 8 },
      { key: "4", count: 3 },
      { key: "", count: 1 },
    ],
    dc_trend: [
      { day: "2026-08-30", dist: [{ key: "2", count: 3 }, { key: "4", count: 2 }] },
      { day: "2026-08-31", dist: [{ key: "2", count: 5 }, { key: "4", count: 1 }, { key: "", count: 1 }] },
    ],
    bot_dist: [
      {
        bot_id: 42,
        bot_username: "spore_bot",
        total: 12,
        succeeded: 9,
        failed: 2,
        last_requested_at: 1_700_000_000_000,
      },
    ],
  },
};

const usersFixture = {
  items: [
    {
      id: 101,
      username: "fixture_user",
      display_name: "替身用户",
      status: "enabled",
      is_owner: false,
      note: "仅用于响应式验收",
      last_used_at: 1_700_000_000_000,
      total_requests: 12,
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
  display_name: "替身用户",
  status: "enabled",
  is_owner: false,
  note: "仅用于响应式验收",
  created_at: 1_700_000_000_000,
  first_used_at: 1_700_000_000_000,
  last_used_at: 1_700_000_000_000,
  archived_at: 0,
  last_denied_at: 0,
  last_denied_reason: "",
  last_denied_text: "",
  used_today: 1,
  daily_limit: 100,
  remaining_today: 99,
  submit_interval_sec: 10,
  concurrent_limit: 2,
  total_requests: 12,
};

// 请求行使用长频道 key / 用户名 / 链接，确保 400px 下表格容器自身出现横向滚动，
// 用于断言「宽表只在表内滚动、页面容器不溢出」。
const requestRowBase = {
  user_id: 101,
  username: "fixture_user_with_a_long_username",
  display_name: "替身用户（响应式宽表样例）",
  source_kind: "public",
  channel_key: "fixture-channel-with-a-very-long-name",
  message_id: 8101,
  message_url:
    "https://t.me/fixture-channel-with-a-very-long-name/8101?comment=responsive-smoke-sample",
  status: "succeeded",
  attempt: 1,
  error_code: "",
  media_type: "video",
  media_types: ["video"],
  source_media_dc_ids: [2],
  delivery_mode: "reference",
  bot_id: 42,
  bot_username: "spore_bot",
  requested_at: 1_700_000_000_000,
  duration_ms: 3200,
};

const requestsFixture = {
  items: [
    { ...requestRowBase, id: 9001 },
    { ...requestRowBase, id: 9002, status: "failed", error_code: "FLOOD_WAIT" },
  ],
  page: 1,
  page_size: 20,
  total: 2,
  total_pages: 1,
};

const requestDetailFixture = {
  ...requestRowBase,
  id: 9001,
  channel_link_text: "fixture-channel-with-a-very-long-name#8101",
  error_text: "",
  attempt_max: 3,
  file_name: "responsive-smoke-sample.mp4",
  file_size: 1024 * 1024,
  queued_at: 1_700_000_000_000,
  started_at: 1_700_000_000_000,
  finished_at: 1_700_000_003_200,
  parent_request_id: 0,
  cloud_uploads: [],
};

const channelsFixture = {
  items: [
    {
      key: "fixture-channel",
      total: 12,
      succeeded: 9,
      failed: 2,
      success_rate: 0.818,
      last_requested_at: 1_700_000_000_000,
    },
  ],
  page: 1,
  page_size: 20,
  total: 1,
  total_pages: 1,
};

const channelDetailFixture = {
  key: "fixture-channel",
  stats: channelsFixture.items[0],
  trend: statsFixture.requests.trend.map(({ day, total, succeeded, failed }) => ({
    day,
    total,
    succeeded,
    failed,
  })),
  media_dist: statsFixture.requests.media_dist,
  error_dist: [
    { key: "FLOOD_WAIT", count: 2 },
  ],
  bot_dist: [
    { bot_id: 42, bot_username: "spore_bot", total: 12, succeeded: 9, failed: 2 },
  ],
  since_day: "",
  until_day: "",
};

const bindingsFixture = {
  items: [
    {
      channel_id: -100_1234_5678_9012,
      user_id: 101,
      username: "fixture-channel",
      title: "替身频道（响应式验收）",
      bound_via: "web",
      created_at: 1_700_000_000_000,
      user_username: "fixture_user",
      user_display_name: "替身用户",
    },
  ],
};

const joinRequestsFixture = {
  items: [
    {
      id: 7001,
      user_id: 101,
      channel_title: "替身频道（响应式验收）",
      status: "pending",
      requested_at: 1_700_000_000_000,
      reviewed_at: 0,
      reviewed_by: "",
      note: "",
      masked_hash: "ab***cd",
    },
  ],
  page: 1,
  page_size: 20,
  total: 1,
  total_pages: 1,
};

const joinedChannelsFixture = {
  items: [
    {
      channel_id: 1_234_567_890,
      title: "替身频道（响应式验收）",
      username: "fixture-channel",
      kind: "channel",
      source: "approved",
      joined_by: 101,
      joined_at: 1_700_000_000_000,
      creator: false,
    },
  ],
};

const eventsFixture = {
  items: [
    {
      id: 1,
      key: "MTPROTO_DISCONNECTED",
      severity: "warning",
      message: "MTProto 会话离线，需要重新登录。",
      count: 3,
      first_at: 1_700_000_000_000,
      last_at: 1_700_000_100_000,
      last_notified_at: 1_700_000_100_000,
      status: "open",
    },
  ],
  page: 1,
  page_size: 20,
  total: 1,
  total_pages: 1,
};

const auditFixture = {
  items: [
    {
      id: 1,
      actor: "admin",
      action: "user.enable",
      target: "user:101",
      created_at: 1_700_000_000_000,
      before: { status: "disabled" },
      after: { status: "enabled" },
    },
  ],
  page: 1,
  page_size: 20,
  total: 1,
  total_pages: 1,
};

const applicationsFixture = {
  items: [
    {
      id: 301,
      username: "apply_user",
      display_name: "待审批申请用户",
      applied_at: 1_700_000_000_000,
      source_bot_id: 42,
      source_bot_username: "spore_bot",
    },
  ],
};

const settingsFixture = {
  timezone: "Asia/Shanghai",
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
  worker_count: 2,
  worker_count_runtime: 2,
  download_threads_env: 3,
  upload_threads_env: 3,
  download_connections_env: 4,
  upload_connections_env: 4,
  download_threads_overridden: false,
  upload_threads_overridden: false,
  download_connections_overridden: false,
  upload_connections_overridden: false,
  last_backup_at: 0,
};

const systemConfigFixture = { system_name: "Spore" };

const oauthFixture = {
  github_configured: false,
  configured: false,
  enabled: false,
  client_id: "",
  secret_set: false,
  bound: false,
};

const backupFixture = {
  db_path: "data/spore.db",
  db_size_bytes: 1024,
  last_backup_at: 0,
  pending: false,
};

const mtprotoFixture = {
  state: "offline",
  updated_at: 1_700_000_100_000,
  qr_available: false,
  bot_state: "ready",
  bot_dc_id: 4,
  bot_updated_at: 1_700_000_100_000,
};

const botsFixture = {
  bots: [
    {
      bot_id: 42,
      username: "spore_bot",
      name: "Spore Bot",
      primary: true,
      online: true,
      conflict: false,
      paused: false,
      mtproto_state: "ready",
      source: "env",
      restart_pending: false,
    },
  ],
  max_bots: 3,
  need_apply: false,
};

const cloudDriveFixture = {
  enabled: true,
  default_destination: "mega-main",
  rclone_available: true,
  destinations: [
    {
      name: "mega-main",
      type: "mega",
      path_prefix: "spore",
      enabled: true,
      options: {},
    },
  ],
};

const cloudDriveBackupFixture = {
  pending: false,
  rollback_available: false,
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

/** 全量替身：覆盖 25 个路由触达的全部读取端点；写请求统一返回 ok。 */
async function mockAdminAPI(page: Page) {
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const method = request.method();

    if (method === "POST") {
      await fulfillJSON(route, 200, { ok: true });
      return;
    }
    switch (path) {
      case "/api/v1/session":
        await fulfillJSON(route, 200, sessionFixture);
        return;
      case "/api/v1/login/csrf":
        await fulfillJSON(route, 200, { csrf_token: "e2e-login-csrf", github_enabled: false });
        return;
      case "/api/v1/overview":
        await fulfillJSON(route, 200, overviewFixture);
        return;
      case "/api/v1/version/check":
        await fulfillJSON(route, 200, versionCheckFixture);
        return;
      case "/api/v1/system-metrics":
        await fulfillJSON(route, 200, {
          range: "realtime",
          since: 1_699_999_976_000,
          until: 1_700_000_000_000,
          sample_interval_ms: 2000,
          points: systemMetricsPoints(),
        });
        return;
      case "/api/v1/stats":
        await fulfillJSON(route, 200, statsFixture);
        return;
      case "/api/v1/users":
        await fulfillJSON(route, 200, usersFixture);
        return;
      case "/api/v1/users/101":
        await fulfillJSON(route, 200, userDetailFixture);
        return;
      case "/api/v1/requests":
        await fulfillJSON(route, 200, requestsFixture);
        return;
      case "/api/v1/requests/9001":
        await fulfillJSON(route, 200, requestDetailFixture);
        return;
      case "/api/v1/channels":
        await fulfillJSON(route, 200, channelsFixture);
        return;
      case "/api/v1/channels/fixture-channel":
        await fulfillJSON(route, 200, channelDetailFixture);
        return;
      case "/api/v1/channel-bindings":
        await fulfillJSON(route, 200, bindingsFixture);
        return;
      case "/api/v1/channel-join/requests":
        await fulfillJSON(route, 200, joinRequestsFixture);
        return;
      case "/api/v1/channel-join/channels":
        await fulfillJSON(route, 200, joinedChannelsFixture);
        return;
      case "/api/v1/events":
        await fulfillJSON(route, 200, eventsFixture);
        return;
      case "/api/v1/audit":
        await fulfillJSON(route, 200, auditFixture);
        return;
      case "/api/v1/applications":
        await fulfillJSON(route, 200, applicationsFixture);
        return;
      case "/api/v1/settings":
        await fulfillJSON(route, 200, settingsFixture);
        return;
      case "/api/v1/system/config":
        await fulfillJSON(route, 200, systemConfigFixture);
        return;
      case "/api/v1/oauth/settings":
        await fulfillJSON(route, 200, oauthFixture);
        return;
      case "/api/v1/backup":
        await fulfillJSON(route, 200, backupFixture);
        return;
      case "/api/v1/mtproto/status":
        await fulfillJSON(route, 200, mtprotoFixture);
        return;
      case "/api/v1/bots":
        await fulfillJSON(route, 200, botsFixture);
        return;
      case "/api/v1/cloud-drive":
        await fulfillJSON(route, 200, cloudDriveFixture);
        return;
      case "/api/v1/cloud-drive/backup/status":
        await fulfillJSON(route, 200, cloudDriveBackupFixture);
        return;
      default:
        await fulfillJSON(route, 404, {
          error: { code: "NOT_FOUND", message: "资源不存在或已被删除。" },
        });
    }
  });
}

/** 23 个受保护页面与页面骨架唯一 H1（与 routeMeta 的 title 一致）。 */
const protectedRoutes: Array<{ path: string; heading: string }> = [
  { path: "/admin", heading: "总览" },
  { path: "/admin/stats", heading: "业务统计" },
  { path: "/admin/applications", heading: "申请审批" },
  { path: "/admin/users", heading: "用户管理" },
  { path: "/admin/users/101", heading: "用户详情" },
  { path: "/admin/requests", heading: "请求记录" },
  { path: "/admin/requests/9001", heading: "请求详情" },
  { path: "/admin/channels", heading: "频道统计" },
  { path: "/admin/channels/fixture-channel", heading: "频道详情" },
  { path: "/admin/channel-bindings", heading: "频道绑定" },
  { path: "/admin/channel-settings", heading: "频道设置" },
  { path: "/admin/cloud-drive", heading: "云盘下载" },
  { path: "/admin/invite-approvals", heading: "加入审批" },
  { path: "/admin/joined-channels", heading: "已加入频道" },
  { path: "/admin/join-settings", heading: "受邀设置" },
  { path: "/admin/events", heading: "事件中心" },
  { path: "/admin/audit", heading: "审计日志" },
  { path: "/admin/settings", heading: "运行设置" },
  { path: "/admin/settings/system", heading: "系统设置" },
  { path: "/admin/settings/oauth", heading: "GitHub 登录" },
  { path: "/admin/backup", heading: "数据备份" },
  { path: "/admin/bots", heading: "机器人管理" },
  { path: "/admin/mtproto", heading: "Telegram 连接" },
];

async function gotoAdmin(page: Page, path: string) {
  await page.goto(path);
  await expect(page.locator(".page-content")).toBeVisible();
}

/** 页面容器不得横向溢出（不允许用 overflow: hidden 掩盖）。 */
async function expectNoHorizontalOverflow(page: Page, label: string) {
  await expect
    .poll(
      async () => {
        const metrics = await page.locator(".page-content").evaluate((el) => ({
          scrollWidth: el.scrollWidth,
          clientWidth: el.clientWidth,
        }));
        return metrics.scrollWidth - metrics.clientWidth;
      },
      { message: `${label}：.page-content 出现横向溢出` },
    )
    .toBeLessThanOrEqual(0);
}

/** 页面操作区 / 筛选 / 表单主按钮可见且完全落在视口宽度内（不被裁剪）。 */
async function expectActionsWithinViewport(page: Page, label: string) {
  const controls = page.locator(
    [
      ".page-scaffold__actions button:visible",
      ".filter-bar__actions button:visible",
      ".form-actions button:visible",
    ].join(", "),
  );
  const count = await controls.count();
  const viewportWidth = page.viewportSize()?.width ?? PHONE_VIEWPORT.width;
  for (let i = 0; i < count; i += 1) {
    const button = controls.nth(i);
    const name = (await button.textContent())?.trim() ?? `#${i}`;
    const box = await button.boundingBox();
    expect(box, `${label}：「${name}」应有可点击布局`).not.toBeNull();
    expect(
      box!.width,
      `${label}：「${name}」宽度为 0，不可点击`,
    ).toBeGreaterThan(0);
    expect(
      box!.x,
      `${label}：「${name}」左边缘超出视口`,
    ).toBeGreaterThanOrEqual(-0.5);
    expect(
      box!.x + box!.width,
      `${label}：「${name}」右边缘被视口裁剪`,
    ).toBeLessThanOrEqual(viewportWidth + 0.5);
  }
}

test.describe("全路由响应式 smoke", () => {
  test("400px：23 个受保护页面无横向溢出且主操作/筛选可见", async ({ page }) => {
    await page.setViewportSize(PHONE_VIEWPORT);
    await mockAdminAPI(page);

    for (const route of protectedRoutes) {
      await gotoAdmin(page, route.path);
      // 页面骨架唯一 H1（各页在加载/错误/数据态都渲染 PageScaffold）
      await expect(page.locator(".page-scaffold__title")).toHaveText(route.heading);
      // 替身数据必须覆盖每个页面的读取端点：出现错误态说明替身缺口或页面回归
      await expect(page.getByText("加载失败")).toHaveCount(0);
      await expectNoHorizontalOverflow(page, route.heading);
      await expectActionsWithinViewport(page, route.heading);
    }
  });

  test("400px：登录页与通配 404 无横向溢出且主操作可见", async ({ page }) => {
    await page.setViewportSize(PHONE_VIEWPORT);
    await mockAdminAPI(page);

    await page.goto("/admin/login");
    await expect(page.getByText("登录 Spore")).toBeVisible();
    const documentOverflow = await page.evaluate(() => ({
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth: document.documentElement.clientWidth,
    }));
    expect(
      documentOverflow.scrollWidth,
      "登录页文档出现横向溢出",
    ).toBeLessThanOrEqual(documentOverflow.clientWidth);
    const loginButton = page.getByRole("button", { name: /登\s*录/ });
    const loginBox = await loginButton.boundingBox();
    expect(loginBox).not.toBeNull();
    expect(loginBox!.x).toBeGreaterThanOrEqual(-0.5);
    expect(loginBox!.x + loginBox!.width).toBeLessThanOrEqual(PHONE_VIEWPORT.width + 0.5);

    await gotoAdmin(page, "/admin/no-such-page");
    await expect(page.getByText("页面不存在", { exact: true })).toBeVisible();
    await expectNoHorizontalOverflow(page, "通配 404");
    const backBox = await page.getByRole("button", { name: "返回总览" }).boundingBox();
    expect(backBox).not.toBeNull();
    expect(backBox!.x).toBeGreaterThanOrEqual(-0.5);
    expect(backBox!.x + backBox!.width).toBeLessThanOrEqual(PHONE_VIEWPORT.width + 0.5);
  });

  test("400px：宽表只在表格容器内滚动，页面容器不溢出且筛选可点击", async ({ page }) => {
    await page.setViewportSize(PHONE_VIEWPORT);
    await mockAdminAPI(page);

    await gotoAdmin(page, "/admin/requests");
    await expect(page.locator(".page-scaffold__title")).toHaveText("请求记录");
    // 等待替身数据行渲染，保证宽表宽度由真实内容决定（跳过 antd 隐藏测量行）
    await expect(page.locator(".ant-table-tbody tr.ant-table-row").first()).toBeVisible();

    const pageOverflow = await page.locator(".page-content").evaluate((el) => ({
      scrollWidth: el.scrollWidth,
      clientWidth: el.clientWidth,
    }));
    expect(
      pageOverflow.scrollWidth,
      "请求记录页页面容器出现横向溢出（宽表应只在表内滚动）",
    ).toBeLessThanOrEqual(pageOverflow.clientWidth);

    const tableContent = page.locator(".ant-table-content").first();
    await expect(tableContent).toBeVisible();
    const tableOverflow = await tableContent.evaluate((el) => ({
      scrollWidth: el.scrollWidth,
      clientWidth: el.clientWidth,
    }));
    expect(
      tableOverflow.scrollWidth,
      "400px 下宽表应超出表格容器（由容器自身横向滚动）",
    ).toBeGreaterThan(tableOverflow.clientWidth);

    // 真实点击校验：筛选主按钮可点（Playwright actionability 含可点击性检查）；
    // 两字按钮 AntD 自动插入空格，用正则匹配
    await page.getByRole("button", { name: /重\s*置/ }).click();
    await expect(page.locator(".page-scaffold__title")).toHaveText("请求记录");
  });

  test("400px：用户新增 Modal 主按钮在视口内且可关闭", async ({ page }) => {
    await page.setViewportSize(PHONE_VIEWPORT);
    await mockAdminAPI(page);

    await gotoAdmin(page, "/admin/users");
    await page.getByRole("button", { name: "新增用户" }).click();
    const modal = page.locator(".ant-modal");
    await expect(modal).toBeVisible();

    const submit = modal.locator(".form-actions .ant-btn-primary");
    await expect(submit).toBeVisible();
    const box = await submit.boundingBox();
    expect(box).not.toBeNull();
    expect(box!.x).toBeGreaterThanOrEqual(-0.5);
    expect(box!.x + box!.width).toBeLessThanOrEqual(PHONE_VIEWPORT.width + 0.5);

    // 两字按钮 AntD 自动插入空格：按 /取\s*消/ 匹配（见前端规范测试要求）
    await modal.getByRole("button", { name: /取\s*消/ }).click();
    await expect(modal).toBeHidden();
  });

  test("1440/1024/768px：代表性列表、详情、图表与设置页无横向溢出", async ({ page }) => {
    await mockAdminAPI(page);

    const checks: Array<{
      width: number;
      height: number;
      path: string;
      heading: string;
      label: string;
    }> = [
      { width: 1440, height: 900, path: "/admin/users", heading: "用户管理", label: "1440 列表" },
      {
        width: 1440,
        height: 900,
        path: "/admin/channels/fixture-channel",
        heading: "频道详情",
        label: "1440 详情",
      },
      { width: 1440, height: 900, path: "/admin/stats", heading: "业务统计", label: "1440 图表" },
      { width: 1440, height: 900, path: "/admin/settings", heading: "运行设置", label: "1440 设置" },
      { width: 1024, height: 768, path: "/admin/requests", heading: "请求记录", label: "1024 列表" },
      {
        width: 1024,
        height: 768,
        path: "/admin/users/101",
        heading: "用户详情",
        label: "1024 详情",
      },
      { width: 1024, height: 768, path: "/admin/stats", heading: "业务统计", label: "1024 图表" },
      { width: 768, height: 900, path: "/admin/users", heading: "用户管理", label: "768 列表" },
      { width: 768, height: 900, path: "/admin/stats", heading: "业务统计", label: "768 图表" },
      {
        width: 768,
        height: 900,
        path: "/admin/settings/system",
        heading: "系统设置",
        label: "768 设置",
      },
    ];

    for (const check of checks) {
      await page.setViewportSize({ width: check.width, height: check.height });
      await gotoAdmin(page, check.path);
      await expect(page.locator(".page-scaffold__title")).toHaveText(check.heading);
      await expectNoHorizontalOverflow(page, check.label);
    }
  });
});
