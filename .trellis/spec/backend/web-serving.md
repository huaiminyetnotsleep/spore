# Web 服务与 SPA 契约

本层适用于 `internal/web/` 的服务形态与安全语义。2026-09 起（09-01-spa-final-cutover 任务）Go 侧**不再渲染任何 HTML**：`html/template` 已全仓移除，唯一 HTML 输出是 SPA 应用壳。

## 服务形态契约

### 1. Scope / Trigger

- 触发：任何涉及 `internal/web` 路由、页面、认证、静态资源或 CSP 的改动。
- 铁律：**不得重新引入 `html/template` 或任何服务端 HTML 模板渲染**（`grep -rn "html/template" --include="*.go"` 必须归零）。页面一律在 `frontend/`（React SPA）实现。
- 铁律：`/api/v1` 及配套功能端点（CSV 导出、`/mtproto/qr.png`、OAuth 流程）发生**新增、修改、删除**时，必须同步更新 `docs/reference/api.md`（接口参考文档），保持文档与实现一致——字段名、错误码、参数校验边界以源码为准逐项核对。

### 2. Signatures

```go
// frontend.go —— SPA 壳单点实现（/admin 与 /admin/login 共用）
func (s *Server) serveSPAShell(w http.ResponseWriter)        // 读 embed + 注入每响应 CSP nonce
func (s *Server) handleAdminApp(w, r, sess)                  // /admin、/admin/{path...}（requireAuth）
func (s *Server) handleAdminLoginApp(w, r)                   // /admin/login（公开，免认证）
// auth.go —— 登录安全原语（新增认证路径必须复用，不得另造）
func (s *Server) ensureLoginCSRF(w, r) (token, error)        // 登录级 CSRF 签发/复用
func verifyLoginCSRF(r *http.Request) bool                   // subtle.ConstantTimeCompare 头 vs Cookie
func (s *Server) issueSession(...)                           // 会话签发（Secure/HttpOnly/Lax，12h 滑动）
```

### 3. Contracts

| 路由/资源 | 形态 | 说明 |
| --- | --- | --- |
| `/admin`、`/admin/{path...}` | SPA 壳 + requireAuth | 未认证 302 `/admin/login` |
| `/admin/login` | SPA 壳 + **公开** | ServeMux 精确路径优先于 `{path...}`；`/admin/login/x`、大小写变体仍走受保护通配 |
| 旧 SSR 页面路径（/、/login、/users、/settings 等） | 未注册，返回 404 | SPA-only 入口；不再保留旧页面兼容跳转 |
| `/users/export.csv` 等 3 个 CSV、`/mtproto/qr.png` | 非 HTML 端点 | SPA 直接引用，永不迁移 |
| `/auth/github`、`/auth/github/callback` | OAuth 流程端点 | 回调结果 302 `/admin/settings/oauth?oauth=bound\|error`（固定枚举，禁止透传 code/state/错误原文） |
| `/static/admin.js` | **404** | SSR 时代脚本已删除 |
| `/healthz`、`/readyz`、`/assets/**` | 探针/静态 | 不变 |

CSP：唯一 HTML（SPA 壳）由 `serveSPAShell` 每响应生成 nonce（`validCSPNonce` 限定 token-safe 字符集 + `escapeHTMLAttr` 转义），`style-src 'self' 'nonce-...'`；前端 AntD CSS-in-JS 经 `ConfigProvider csp` 消费同一 nonce（`frontend/src/csp.ts`）。

### 4. Validation & Error Matrix（登录 API）

| 条件 | 处理 |
| --- | --- |
| `GET /api/v1/login/csrf` | 公开、`no-store`；种 `spore_login_csrf` cookie（Secure/HttpOnly/Lax/2h）并返回 `{"csrf_token","github_enabled"}` |
| `POST /api/v1/login` CSRF 缺失/不匹配 | 403 JSON `WEB_CSRF_INVALID`（比对必须常数时间） |
| 访问密钥错误 | 401 JSON `WEB_AUTH_FAILED` + 限流计数 + `auth.login_failed` 审计（detail 仅 `{method,ip}`，**不得含密钥**） |
| 连续失败达阈值 | 429 JSON `WEB_LOGIN_LOCKED`（内存限流器：`ip:<addr>` + `global` 双键、指数退避 1min→1h、`pruneLocked` 清理闲置条目防膨胀） |
| 存储读失败 | 503 JSON `STORE_UNAVAILABLE` |
| 成功 | 复用 `issueSession`（不得另造会话逻辑）+ `auth.login` 审计 |
| `GET /api/v1/login`、`POST /api/v1/login/csrf` | 405 JSON（方法回退） |

登录级 CSRF（`spore_login_csrf`）与会话级 CSRF（`web_sessions.csrf_token`，经 `GET /api/v1/session` 引导、写操作 `X-CSRF-Token` 回传）是**两套独立机制**，不得混用。

### 5. Good / Base / Bad Cases

- **Good**：新增公开端点前先核对上面公开面清单（当前仅 `/api/v1/login/csrf`、`/api/v1/login` 两个）；旧页面入口不注册兼容跳转；认证改动复用 `ensureLoginCSRF`/`verifyLoginCSRF`/`issueSession`。
- **Base**：SPA 壳、探针、CSV、二维码行为不动，仅改 `/api/v1` 业务。
- **Bad**：把限流状态放全局单键（ DoS 互踩）、用 `==` 比对 CSRF、在审计 detail 里记录提交的密钥、为“美观”重引入服务端渲染提示页。

### 6. Tests Required

- 登录矩阵：csrf 签发 → login 200 → 访问 `/admin` 200 → 登出 → 401；401/403/429 分支；审计 detail 不含密钥（`TestLoginWrongKeyRejected`）。
- 公开路由边界：`/admin/login` 精确路径 vs 子路径/大小写变体（`TestAdminLoginShellExactPathOnly`）。
- `TestAdminJSRemoved`（`/static/admin.js` 404）、`TestDeletedSSRFormRoutesUnavailable`（删除路由不回潮）、`TestDeletedLegacyPageRoutesUnavailable`（旧页面入口 404 矩阵）。

### 7. Wrong vs Correct

```go
// Wrong：新登录通道自建 CSRF 比对
if r.Header.Get("X-CSRF-Token") == cookie.Value { ... }

// Correct：复用常数时间原语
if !verifyLoginCSRF(r) { writeAPICSRFInvalid(w); return }
```

### Gotcha

> **Warning**：测试环境每次登录（`e.login(t)`）都会写一条 `auth.login` 审计。断言审计总数/列表的测试必须把该条计入（种子 N 条 + 登录 1 条）。

### Gotcha

> **Warning**：GitHub OAuth **绑定成功会失效全部会话**，`?oauth=bound` 横幅实际会落在登录页（302 链）而不可见——这是安全设计的已知取舍；错误分支（不清会话）横幅可见。
