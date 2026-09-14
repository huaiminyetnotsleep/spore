# SPA 管理端验收入口与边界契约

> 本页记录 SPA 管理端的验收入口与边界。SSR → SPA 切换已完成，SSR 认证页代码已删除。**当前为 SPA-only**：旧页面路径（`/users`、`/settings`、`/login` 等）没有 302 兼容跳转，未匹配路径返回 404，不回退 SPA HTML。页面与路由的完整定义以 `frontend/src/router/routes.tsx` 和 [architecture.md](./architecture.md) 为准。

## 1. e2e 验收入口（按需）

e2e 会启动真实浏览器，耗时与资源消耗明显大于 lint / typecheck / 单测 / build 等轻量门禁，**非必做项**：日常改动跑轻量门禁即可，e2e 留给大改动或发版前按需执行。

默认在 `frontend/` 执行，Vite 启动并由 Playwright 注入本机 API 替身，无需真实凭据：

```bash
npm run test:e2e
```

覆盖：SPA 导航、深链接刷新与异步表单回填；session `401`、CSRF `403` 的 JSON 错误信封与页面错误态；未知 API `404` 不被 SPA 截获；CSV 下载与旧路径跳转。**注意**：现有 e2e 对旧路径的断言（如 `/login` → `/admin/login` 302）按历史验收时点（2026-08-28）行为编写；当前源码中旧路径为 404，两者不一致，复跑 e2e 前应先核对该断言。

隔离 Compose 模式：使用隔离 `.env`、隔离 `data/` 和一次性访问密钥，浏览器指向本机容器：

```bash
PLAYWRIGHT_BASE_URL=http://127.0.0.1:8080 \
PLAYWRIGHT_ACCESS_KEY='<隔离环境访问密钥>' \
npm run test:e2e
```

注意事项：

- 不得把生产 Token、Session、OAuth Secret 或二维码 URL 写入任何测试输出与日志；
- Playwright 关闭 trace、截图和视频，避免会话/CSRF 值进入产物；
- Compose HTTP 验收中，测试只在浏览器上下文把 Cookie 临时标记为非 Secure 以适配本机无 TLS，不改服务端配置。

## 2. Web 边界契约

- `/api/v1/**` 未认证统一 JSON `401`；CSRF 缺失或不匹配统一 JSON `403`；未知 API 返回 JSON `404`，不回退 SPA HTML。
- `/healthz`、`/readyz`、`/assets/**` 与下载、二维码端点优先于 SPA fallback；`/admin/**` 仅作 SPA 入口（未认证 GET 302 到 `/admin/login`），仍要求 Web session；其余未匹配路径一律 404，无旧路径兼容跳转。
- 敏感数据（Session Secret、访问密钥、OAuth Secret、MTProto Session、二维码 URL、消息正文、完整媒体 URL）不进入 SPA DTO、Playwright fixture、日志或测试产物。

## 3. 历史验收结论（2026-08-28）

Go（gofmt / build / vet / test / -race）与前端（lint / typecheck / test / build / e2e）全部门禁通过；隔离 Compose 启动健康、`/healthz` 200、重启后恢复、旧路径 302（历史时点行为，当前源码为 404）；默认替身与隔离 Compose 两模式 Playwright 各 4 项全部通过。以上均为 2026-08-28 的执行记录，不代表当前分支已重新验收。

当时延期的生产侧验收项（真实 HTTPS Secure Cookie、外部反向代理、生产 OAuth、真实 MTProto 扫码、备份恢复演练、重启观察窗口）按 [deployment.md](../guide/deployment.md) 的首次上线流程与 [operations.md](../ops/operations.md) 的备份恢复、排障章节执行。
