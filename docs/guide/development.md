# 本地开发与调试指南

> 面向在开发机上直接运行、调试 Spore 的场景。生产部署见 [deployment.md](deployment.md)，
> 线上排障见 [troubleshooting.md](../ops/troubleshooting.md)。
> 本地调试不需要 Docker 和反向代理：`go run` 单进程即可同时跑 Bot、Web 管理端和 SQLite。

## 1. 首次准备

```bash
git clone https://github.com/huaiminyetnotsleep/spore.git
cd spore
cp .env.example .env
chmod 600 .env
```

如果你从个人 Fork 开发，请将地址替换为自己的 Fork，并在完成测试后通过 Pull Request
提交到 `main`。项目的协作约定见 [贡献指南](https://github.com/huaiminyetnotsleep/spore/blob/main/CONTRIBUTING.md)。

`.env` 至少填写：

| 变量 | 说明 |
| --- | --- |
| `BOT_TOKEN` | @BotFather 签发，Bot 收发消息用 |
| `TG_API_ID` / `TG_API_HASH` | [my.telegram.org](https://my.telegram.org) 申请，MTProto 用户账号用（流程见 [telegram-api-credentials.md](telegram-api-credentials.md)） |
| `LOGIN_MODE` | 留 `auto`：优先终端扫二维码，本地调试最方便 |

`ALLOWED_USER_IDS` 可留空（旧白名单迁移用），用户改由 Web 审批管理。
申请凭据的完整步骤见 [telegram-api-credentials.md](telegram-api-credentials.md)。

## 2. 启动

```bash
make dev        # 等价于 go run ./cmd/bot，工作目录必须是仓库根（.env 按相对路径读取）
```

单进程同时启动三部分：

| 组件 | 位置 | 说明 |
| --- | --- | --- |
| Bot 长轮询 | Telegram | 收发消息与任务处理 |
| Web 管理端 | `http://127.0.0.1:8080` | `WEB_ADDR` 可改端口；本地直连，**无需反代，`WEB_TRUSTED_PROXY` 保持 `false`** |
| SQLite | `data/spore.db` | 与 `session.json`、`peers.json`、`tmp/` 同在 `data/` |

首次启动注意：

- 终端（stderr）会打印一次**管理端访问密钥**，只在出现这一次，务必保存；
- 密钥丢失无需重置数据：`go run ./cmd/bot admin reset-key` 重新生成并踢掉全部 Web 会话（该子命令不启动 Bot、不需要 Telegram 凭据）；
- MTProto 用户号首次运行按终端提示扫码登录；成功后 `data/session.json` 持久化，之后重启免扫码。
  本地也可以走 Web 扫码：登录管理端 → MTProto 页面 → 重连。
- Bot 身份 MTProto 会话（`data/bot-session.json`）用 `.env` 里的 `BOT_TOKEN` 在首次启动
  自动登录，日志出现 `Bot MTProto 会话登录成功`；无需扫码或任何人工操作，删掉文件
  下次启动会自动重登。

### 2.1 访问密钥丢失与重置

访问密钥丢失时不需要删除数据库或 `data/`，直接在仓库根目录执行：

```bash
go run ./cmd/bot admin reset-key
```

命令只重置 Web 管理端访问密钥，不启动 Bot，也不需要 Telegram 凭据。新密钥会在终端
输出一次；请立即保存到密码管理器。

重置后，旧密钥和所有已有 Web 会话立即失效，但 SQLite 数据库、白名单、消息记录、
统计、审计记录和 MTProto Session 都会保留。

## 3. 日常调试

| 需求 | 做法 |
| --- | --- |
| 详细日志 | `.env` 设 `LOG_LEVEL=debug`（默认 `info`），重启生效 |
| 改 Go 代码后重启 | `Ctrl+C` 停止后重新 `make dev`；Go 侧未设置热重载 |
| **改前端页面（热更新）** | 见下方「前端热更新开发模式」：`npm run dev` 后改 `frontend/src` 即时生效 |
| 断点调试 | `dlv debug ./cmd/bot`（Delve），或在 IDE 中以 Go 应用运行 `./cmd/bot`，工作目录设为仓库根 |
| 重置业务数据 | 停进程后删除 `data/spore.db`（Session 保留，不用重扫码） |
| 连会话一起重置 | 删除整个 `data/`（下次启动重新扫码） |
| 大文件调试 | `docker compose --profile bigfile up bot-api` 启动本地 Bot API Server（备选路线），`.env` 配 `BOT_API_URL`（上限 2000MB），见 [deployment.md §5](deployment.md#_5-大文件说明与本地-bot-api-备选路线) |

### 2.2 前端热更新开发模式

`http://127.0.0.1:8080/admin` 的页面是**编译期嵌入**的前端构建产物（`make build` 时把
`frontend/dist` 同步进 `internal/web/frontend-dist`），所以改前端后直接刷新 8080 不会更新，
需要重新构建并重启 `go run`。日常改前端请使用 Vite 热更新服务器：

```bash
# 终端 1：Go 服务（API 与登录态）
make dev

# 终端 2：前端热更新（/api 自动代理到 8080）
cd frontend && npm run dev
```

然后访问 `http://127.0.0.1:5173/admin`（注意端口是 **5173**）：登录态经代理与 8080 共享，
修改 `frontend/src` 下的代码会即时热替换，无需重启或构建。

注意事项：

- 5173 只用于本地开发；8080 始终提供嵌入构建产物的"生产形态"，两者可同时运行；
- 建议用 Chrome 访问 5173：本机 HTTP 下会话 Cookie 带 `Secure` 属性，
  Chromium 将 `127.0.0.1` 视为可信来源可正常携带，其他浏览器如遇登录异常可改用
  `http://localhost:5173`；
- 提交前仍需跑一次完整构建与测试（`npm run build` / `npm run test:e2e`），并按 Makefile
  `frontend-build` 的方式把 `frontend/dist/.` 同步到 `internal/web/frontend-dist/`；
- 重启 `go run` 时若 8080 仍被占用，可能是上次 `go run` 的子进程残留：
  `lsof -nP -iTCP:8080 -sTCP:LISTEN` 找到进程后 `kill` 掉再启动。

管理端设置页可在线调整运营时区、去重窗口、文件大小上限、流式阈值与临时目录上限。媒体发送统一走”下载 + 上传”：不超过 Bot API 上限（官方服务器 50MB）的媒体走 Bot API multipart 上传，超过的经 Bot 身份 MTProto 会话直传（上限 2000MB），无需配置。媒体与队列容量改动标注”重启生效”，可用”受控重启”按钮触发 SIGTERM。备份页支持上传并校验业务数据库，确认后需重启才会应用；直接 `make dev` 运行时进程退出后需手动重新启动。

## 4. 验证完整链路

1. 用另一个 Telegram 账号（或自己的 owner 账号）向 Bot 发送 `/start`；
2. 打开 `http://127.0.0.1:8080`，用访问密钥登录，在"申请审批"中批准该用户；
3. 该用户发送一条 `t.me` **媒体**消息链接：应正常"下载 + 上传"送达（小媒体走流式
   直传不落盘，Web"消息记录"该条记录的投递方式为"上传"）；再发一条文本链接验证渲染；
4. 临时把 `data/bot-session.json` 改名后重启模拟 Bot 会话离线，再发一条 >50MB 的
   媒体链接：应提示"大文件发送通道暂不可用"（小文件不受影响）；验证完恢复文件；
5. Bot 内发 `/usage` 查询剩余额度；管理端"频道统计""事件中心""审计日志"应同步更新；
6. 想验证异常通知：先在管理端“通知设置”的“通知渠道”中开启系统事件自动通知并配置测试通道，再临时把 `data/session.json` 改名后重启制造会话失效；事件会按“通知规则”与“静音计划”决定是否投递，事件中心仍会保留记录（30 分钟冷却内不重复）。未启用配置化通道时，旧版 owner Bot 兼容通知仍按原语义工作。

## 5. 只跑测试（不依赖真实 Telegram）

```bash
make test                                  # 全量
go test ./... -race                        # 并发路径（改队列/会话/缓存后必须）
go test ./internal/web -run TestLogin -v   # 单包单用例
```

测试全部使用临时 SQLite、`httptest` 与假 Telegram 服务，不读取 `.env`、不消耗额度、
不触碰真实 `data/`。容量与备份验收测试：`go test ./internal/store -run TestCapacityAcceptance -v`。

## 6. 本机 SPA / Compose 验收

前端日常验证以轻量门禁为主（快速、无浏览器开销）。

**依赖准备**：干净 clone 后先执行一次 `make frontend-install`（或在 `frontend/` 内
`npm ci`），再运行下面的命令；直接执行 `npm run *` 不会自动安装依赖。改用
`make frontend-lint` / `frontend-typecheck` / `frontend-test` / `frontend-build` 时，
Makefile 会在依赖文件变化或 `node_modules` 缺失时自动执行一次 `npm ci`（按 stamp
判断），依赖未变时直接复用现有安装；文档站的 `make docs-dev` / `docs-build` /
`docs-preview` 没有该机制，每次执行都会先运行一次 `npm install`。

```bash
cd frontend
npm run lint
npm run typecheck
npm run test -- --run
npm run build
```

`npm run test:e2e` 启动真实浏览器（Playwright 注入安全的本机 API 替身），耗时与资源消耗较大，非必做，按需执行。

如需验证构建产物嵌入和 Compose 健康检查，应使用隔离 `.env` 与隔离 `data/`，不要使用生产凭据（Compose 的 `./data` 挂载固定指向当前部署目录，真正的隔离做法见 [deployment.md §6](deployment.md#_6-本地开发机-compose-与-spa-验收)）：

```bash
docker compose config
docker build -t spore-cutover-check .
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8080/healthz

# 浏览器级 Compose 验收，按需：
PLAYWRIGHT_BASE_URL=http://127.0.0.1:8080 \
PLAYWRIGHT_ACCESS_KEY='<隔离环境访问密钥>' \
npm run test:e2e --prefix frontend

docker compose restart bot
curl -fsS http://127.0.0.1:8080/healthz
docker compose down
```

Compose 浏览器测试只针对本机 HTTP；测试会话 Cookie 的 Secure 属性仅在浏览器上下文内临时调整以适配无 TLS 的验收环境，不改变服务端配置。测试不保留 trace、截图或视频，避免会话/CSRF 值进入产物。完整验收入口、边界契约与历史验收结论见
[SPA 验收记录](../reference/admin-acceptance.md)。

## 7. 注意事项

- 本地 `data/` 与 VPS 是两套独立数据；部署时只同步代码与 `.env`，不要把本地调试的
  `data/`（含测试用户、请求记录、你的开发会话）拷贝上服务器；
- `.env` 与 `data/session.json` 都等效敏感凭据：不提交 Git、不写入截图或工单
  （`data/bot-session.json` 同样不提交，泄漏后果限于该 Bot 可被用来发消息，敏感度较低）；
- 本地 Web 若要给外网演示，走 `ssh -L 8080:127.0.0.1:8080 <server>` 端口转发，
  不要把 `127.0.0.1:8080` 直接暴露公网（生产同样只允许回环 + 反代，见 [deployment.md](deployment.md)）。
