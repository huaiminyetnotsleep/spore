import { defineConfig, devices } from "@playwright/test";

const externalBaseURL = process.env.PLAYWRIGHT_BASE_URL;

if (externalBaseURL && !process.env.PLAYWRIGHT_ACCESS_KEY) {
  throw new Error(
    "使用 PLAYWRIGHT_BASE_URL 验收 Compose 时必须同时提供隔离环境的 PLAYWRIGHT_ACCESS_KEY。",
  );
}

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  reporter: process.env.CI ? "github" : "list",
  use: {
    baseURL: externalBaseURL ?? "http://127.0.0.1:5173",
    // 不保留 trace/screenshot/video，避免会话 Cookie、CSRF 或下载响应进入产物。
    trace: "off",
    screenshot: "off",
    video: "off",
  },
  ...(externalBaseURL
    ? {}
    : {
        webServer: {
          command: "npm run dev -- --host 127.0.0.1",
          url: "http://127.0.0.1:5173",
          reuseExistingServer: !process.env.CI,
          timeout: 120_000,
        },
      }),
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
