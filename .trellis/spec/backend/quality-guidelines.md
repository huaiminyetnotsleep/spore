# 质量与测试规范

## 基本门禁

Spore 是 Go 单仓库，前端为 `frontend/` 的 React SPA（有独立 lint/typecheck/test 工具链）。按变更范围提交前至少执行 Go 门禁；触及 `frontend/` 时追加前端门禁（见 `.trellis/spec/frontend/index.md`）。Go 门禁：

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./...
```

`gofmt -l .` 必须无输出；修改并发、共享状态、队列、缓存、媒体下载或退出路径时追加：

```bash
go test ./... -race
```

仓库的 `Makefile` 提供等价门面：`make build`、`make vet`、`make test`；`make dev` 只用于本地运行，`make linux` 用于 Linux 交叉构建。

## 编码要求

- 遵循 Go 编译器和 `go vet` 的约束，清理未使用变量、import 和不可达分支。
- 通过小接口隔离 Bot API、MTProto、下载和发送边界；不要把第三方 TL 类型扩散到不需要它的包。
- 错误显式返回并按 [错误处理规范](./error-handling.md) 分类；不要用 panic 代替业务错误。
- 资源获取后立即安排 `defer` 清理；临时文件、Pipe、文件句柄和 context cancel 都要覆盖成功/失败/取消路径。
- 共享常量只有一个来源，例如媒体大小限制、相册上限和临时文件命名规则。
- 修改前先搜索已有实现，避免为相同错误、重试、HTML 渲染或媒体能力新增平行逻辑。

## 测试重点

优先为不依赖 Telegram 网络的纯逻辑写表驱动单元测试：

- `internal/tmeurl`：公开/私有链接、参数、尾标点、非法 host 和嵌入文本；
- `internal/config`：必填项、默认值、跨字段校验和 Deny All 白名单；
- `internal/apperr`：错误码、包装/解包和用户文案；
- `internal/message`：CJK、emoji 的 UTF-16 entity 偏移、嵌套 HTML、转义和长度截断；
- `internal/queue`、`internal/media`、`internal/delivery`：队列满、取消、资源 Cleanup、媒体大小分支和轻量 429 重试。

涉及 Go map、mutex、channel、worker 或共享 Peer 缓存时必须考虑 race 测试；涉及临时文件时检查成功和错误路径都没有残留。

## 跨层检查

变更跨越两个以上包时，沿完整链路检查：

```text
Update → SourceRef → Job → Fetcher → Item/Media → Handle → Sender
```

确认：

- 边界数据的所有字段仍被保留；
- `context` 和错误码没有在层间丢失；
- `io.Reader` 只被消费一次，失败时仍能 Cleanup；
- Bot API 与 MTProto 的职责没有互换；
- 用户提示、日志和底层错误仍保持不同的暴露级别。

## Code Review 清单

- [ ] 变更文件属于正确的 `cmd/` 或 `internal/` 包。
- [ ] 没有引入当前 MVP 不需要的数据库、Redis、Web 层或新依赖。
- [ ] 新增公共 API 有中文 Go doc，命名和现有包一致。
- [ ] 错误、取消、重试和清理路径已覆盖。
- [ ] 未记录或提交凭据、Session、消息正文和媒体 URL。
- [ ] 运行 `gofmt`、build、vet、test；并按变更风险运行 `-race`。
- [ ] 相关文档和错误提示没有把未来能力描述成当前能力。

## Web 管理增强的额外检查

- [ ] 涉及用户资料、消息 URL、备份导入、OAuth、媒体配置或重启时，至少覆盖 Web handler 的认证/CSRF/失败路径和对应 store/跨层契约。
- [ ] 备份测试断言 `integrity_check`、schema 版本、必需表列、pending marker 单次应用、WAL/SHM 清理、回滚副本、安全设置保留和 Web session 清除。
- [ ] OAuth 测试断言 AES-256-GCM 密文不可明文读取、主密钥缺失时访问密钥通道仍可用、数据库配置优先级和 Secret 不进入审计/日志。
- [ ] 媒体设置测试断言 1MB–2GB、`StreamLimit <= MaxFileSize`、官方 Bot API 50MB 边界、配置值与运行值分离及下次重启生效。
- [ ] 重启测试使用注入的 fake `RestartFunc` 观察调用，不执行 Shell、Docker 或真实进程信号；真实 Docker supervisor 和 Telegram 行为另做隔离验收。

## 场景：前端依赖变更感知与默认验证

### 1. Scope / Trigger

- Trigger：仓库内引入或修改 `frontend/` 的 npm 依赖、前端构建命令或 Makefile 验证入口。
- Scope：本项目是单仓库；依赖安装属于显式或依赖变更触发的准备动作，不应成为每次源码验证的网络副作用。

### 2. Signatures

- `make frontend-install`：按 `frontend/package.json` 和 `frontend/package-lock.json` 安装依赖，并更新 `frontend/node_modules/.spore-install.stamp`。
- `make frontend-lint`、`make frontend-typecheck`、`make frontend-test`、`make frontend-build`：依赖 stamp 感知——stamp（`frontend/node_modules/.spore-install.stamp`）缺失或依赖文件较新时经 `frontend-install` 前置自动执行一次 `npm ci`，stamp 新鲜时直接复用现有依赖；直接 `cd frontend && npm run *` 不自动安装。`make docs-dev/build/preview` 没有 stamp，每次都先执行 `npm install`；只想复用已有依赖时绕过 Make 直接运行 `npm --prefix docs run <script>`。
- `make build`：构建前端和 Go，但只在依赖文件比安装标记更新或标记缺失时触发依赖安装。

### 3. Contracts

- 依赖变更输入仅为 `frontend/package.json`、`frontend/package-lock.json`；源码变更（如 `frontend/src/**`）不触发 npm 安装。
- 安装标记位于 `frontend/node_modules/.spore-install.stamp`；删除 `node_modules` 后标记随之消失，应允许下一次构建重新安装。
- 默认前端门禁为 `npm run lint`、`npm run typecheck`、`npm run test`、`npm run build`；Playwright 为延期能力，不进入默认 Makefile、CI 或当前子任务完成条件。
- 前端基线的发布门禁还需执行 `docker build -t spore-foundation-check .`，确认 Node builder 产出被 Go builder 嵌入，最终镜像只包含 Go 服务和前端静态资源；Docker daemon 不可用时必须明确记录为环境延期，不得伪报通过。
- `frontend/e2e/app-shell.spec.ts` 当前使用 `test.describe.skip(...)`，只保留为延期骨架，不执行验证，也不计入当前完成条件。
- 只读 SPA 页面必须通过 `/api/v1` DTO 获取数据，加载/空态/错误态可观察；查询失败不能伪装为空数据。旧 SSR 页面已移除（SPA-only，旧路径返回 404），CSV 下载继续可用。
- 所有 API 写端点必须在业务变更前执行服务端 `apiRequireAccess`；依赖未注入返回受控 503、无权限返回受控 403，且两种失败都不得更新数据或写成功审计。旧 SSR 回退入口已随 2026-09 SPA 最终切换删除，`/api/v1` 写端点是唯一入口。
- SPA 切换的 Compose/Playwright 验收必须使用隔离 `.env`、`data/`、端口和一次性访问密钥；仓库现有 `.env` 含真实凭据时禁止直接启动。失败或重试时不得把密钥、Cookie、Session、下载内容或二维码写入 trace、截图、视频和日志。
- SPA 入口每次响应生成 CSP nonce，同步写入 HTML `meta[name="csp-nonce"]` 与响应头 `style-src`/`style-src-elem`；nonce 生成或注入失败返回受控 500。Ant Design CSS-in-JS 经 `ConfigProvider csp` 与 `@ant-design/cssinjs` `StyleProvider nonce` 授权；应用源码禁止固定 `style={{...}}`（迁入 `styles.css` class）。
- SPA CSP 的唯一允许放宽是 `style-src-attr 'unsafe-inline'`（兼容 Ant Design 运行时 style 属性），必须有测试断言其未污染 `style-src`/`style-src-elem`/`script-src`；禁止使用 `style-src 'unsafe-inline'`。
- CSP 生效时 `getAttribute("nonce")` 返回空串，测试断言必须使用 `HTMLStyleElement.nonce` DOM 属性；rc-util/antd 内部无 nonce 的 `updateCSS`（滚动条测量探针等）会触发良性 `style-src-elem` 违规，Playwright 断言收敛为「非 style-src 指令零违规」。
- Docker 的 Node builder 可以在镜像构建中执行一次 `npm ci`，不改变本地日常命令的依赖感知规则。

### 4. Validation & Error Matrix

| 条件 | 处理 |
| --- | --- |
| 仅修改 `frontend/src/**` | 复用现有 `node_modules`，直接执行验证命令 |
| `package.json` 或 `package-lock.json` 更新 | 先重新安装，再执行验证命令 |
| `node_modules` 或安装标记不存在 | 先重新安装，再执行验证命令 |
| npm 安装失败 | 立即停止，不执行依赖未满足的前端验证，并保留原始错误 |
| Playwright 浏览器未安装或 `frontend/e2e/app-shell.spec.ts` 被跳过 | 当前阶段不阻塞默认门禁；发布验收阶段按需手动启用、安装浏览器和执行 |

### 5. Good / Base / Bad Cases

- Good：依赖文件作为安装标记的 Make 前置条件；源码迭代只运行 lint/typecheck/test/build。
- Base：首次克隆或依赖变更时执行一次 `make frontend-install`，之后复用本地依赖。
- Bad：让每个 `frontend-*` 目标无条件执行 `npm install`，或把 Playwright 浏览器下载加入日常验证。

### 6. Tests Required

- Make：依赖文件未变更时断言验证目标不运行 npm 安装；依赖文件变更或 `node_modules` 删除时断言只安装一次。
- Frontend：执行 lint、typecheck、unit test、build，并断言构建产物含 manifest 和哈希资源。
- Go：执行 `gofmt -l .`、`go build ./...`、`go vet ./...`、`go test ./...`，并断言 API、下载、探针和旧 SSR 不被 SPA fallback 截获。
- Playwright：仅在显式发布验收任务中执行；当前 `frontend/e2e/app-shell.spec.ts` 保持跳过，不作为本阶段默认测试。

### 7. Wrong vs Correct

#### Wrong

```make
frontend-build: frontend-install
frontend-install:
	cd frontend && npm install
```

#### Correct

```make
FRONTEND_INSTALL_STAMP := frontend/node_modules/.spore-install.stamp

frontend-build: $(FRONTEND_INSTALL_STAMP)

$(FRONTEND_INSTALL_STAMP): frontend/package.json frontend/package-lock.json
	cd frontend && npm ci
	touch $@
```

## 场景：高风险管理流程的跨层安全门禁

### 1. Scope / Trigger

- Trigger：修改 `internal/web` 的 OAuth、备份、受控重启、MTProto API/SSR，或修改对应 `frontend/src/features/{oauth,backup,mtproto,settings}` 页面。
- Scope：同时检查 Go handler、共享业务核心、SQLite/文件生命周期、审计/日志脱敏和 SPA 表单状态；真实同源 HTTPS 浏览器 E2E 由发布验收任务承接，不替代代码门禁。

### 2. Signatures

- `func (s *Server) scheduleRestart(reason string) bool`：同一 `Server` 生命周期只成功调度一次；`RestartFunc` 失败时复位允许重试。
- `POST /api/v1/restart` 与 `POST /restart`：固定 `confirm=restart`，成功 202，重复提交 409，成功调度后才写 `admin.restart` 审计。
- `POST /api/v1/oauth/settings`：参数错误 400；存储/内部错误保持受控 5xx；Secret 不出 DTO。
- `func ValidateBackup(ctx context.Context, path string) error`、`func ApplyPendingImport(...) (bool, error)`：候选库校验、marker、WAL/SHM 和 rollback 必须原子衔接。

### 3. Contracts

- 备份错误响应只使用受控文案；`ParseMultipartForm` 生成的临时文件在成功、失败和取消路径均调用 `RemoveAll`；`copyFile` 复制或 Close 失败必须删除不完整目标。
- `/mtproto/qr.png` 的成功和所有错误响应均设置 `Cache-Control: no-store`；二维码 URL 只在内存中短暂使用，不进入日志、审计或持久化。
- OAuth 绑定审计只记录动作/状态，不记录 GitHub account ID/login；SSR 与 API 都不得把 `err.Error()` 直接渲染给管理员。
- Ant Design `Form` 的异步查询数据必须使用 `form.setFieldsValue` hydrate，不能只依赖只在首次挂载读取的 `initialValues`。

### 4. Validation & Error Matrix

| 条件 | 处理 |
| --- | --- |
| 重启确认错误/未登录/CSRF 错误 | 400/401/403 受控响应，不审计成功，不调用 `RestartFunc` |
| 重启已调度 | 409 CONFLICT，不重复调用、不重复写成功审计 |
| OAuth 参数/主密钥无效 | 400 受控文案 |
| OAuth 存储失败 | 5xx 受控文案并保留错误码 |
| 备份格式/完整性/路径异常 | 400 受控文案，清理上传临时文件，当前库不变 |
| QR 无会话/无二维码/生成失败 | 对应 401/404/503/500，全部 `no-store` |
| 设置查询异步完成 | 表单显示服务端现值，保存不得因空初始值误覆盖 |

### 5. Good / Base / Bad Cases

- Good：先获取并锁定幂等状态，成功调度后审计；资源获得即安排清理；展示层只映射错误码和用户文案。
- Base：Go API/SSR 共享核心，前端只持有短暂操作状态，默认执行 Go 与前端四项门禁。
- Bad：先写重启成功审计再判断调度结果、把 SQLite 路径拼进错误提示、QR 错误响应可缓存、或用 `initialValues={data}` 期待异步更新表单。

### 6. Tests Required

- Go：重启 API/SSR 的确认、CSRF、不可用、成功、重复提交和失败复位；OAuth 参数 400/存储 5xx 与审计脱敏；备份错误脱敏、multipart 清理、rollback 半文件清理；QR 错误响应 `no-store`。
- Frontend：Settings 查询完成后断言 timezone/dedup/queue 表单值被回填；运行 lint、typecheck、unit test、build。
- Full scope：`gofmt -l .`、`go build ./...`、`go vet ./...`、`go test ./...`、`go test ./... -race`；同源 HTTPS E2E 在发布验收任务执行。

### 7. Wrong vs Correct

#### Wrong

```go
s.audit(ctx, "admin.restart", "process", detail)
s.scheduleRestart(reason)
```

#### Correct

```go
if !s.scheduleRestart(reason) {
    writeAPIError(w, http.StatusConflict, apiCodeConflict, "重启已在进行中，请勿重复提交。")
    return
}
s.audit(ctx, "admin.restart", "process", detail)
```

## 场景：请求记录单条与批量取消

### 1. Scope / Trigger

- Trigger：管理端新增按请求 ID 取消排队/执行中任务，或修改队列、Worker、媒体清理和请求终态更新。
- Scope：覆盖 `internal/store`、`internal/access`、`internal/queue`、`internal/web` 与 `frontend` 的跨层取消链路。

### 2. Signatures

```go
func (s *Store) CancelRequest(ctx context.Context, id, at int64) (Request, error)
func (s *Service) Cancel(ctx context.Context, actor string, requestID int64) error
func (s *Service) CancelMany(ctx context.Context, actor string, requestIDs []int64) []CancelOutcome
func (q *Queue) CancelRequest(requestID int64) bool
```

```text
POST /api/v1/requests/{id}/cancel
POST /api/v1/requests/cancel  {"ids":[12,13]}
```

### 3. Contracts

- 只有 `queued`/`processing` 可取消；条件更新为 `cancelled`，并写 `REQUEST_CANCELLED`、`finished_at` 和耗时。
- `MarkRequestStarted` 必须进行 `queued → processing` claim；channel 中迟到的 Job 不能仅凭出队就执行。
- 活动 Worker 使用请求级 `context.WithCancelCause`；进程根 context 取消仍归类为 `failed/INTERRUPTED`。
- `FinishRequest` 不得覆盖 `cancelled`；取消与自然完成的胜负由数据库条件 UPDATE 决定。
- `media.Handle.Cleanup` 无论成功、失败或取消都必须关闭句柄/管道，并删除该请求的临时下载文件。
- 批量 API 只接受明确的正整数 ID，单次最多 50 个；逐条返回 `cancelled`、`conflict`、`not_found` 或 `failed`，部分成功不回滚。

### 4. Validation & Error Matrix

| 条件 | 行为 |
| --- | --- |
| ID 不存在 | 单条 404；批量该项 `not_found` |
| 状态不是 queued/processing | 单条 409；批量该项 `conflict` |
| 请求仍在 channel 或入队前窗口 | 状态先标记 cancelled，出队 claim 跳过 |
| 活动任务取消 | 传播 RequestCancelled cause，跳过失败提示和失败事件 |
| 进程根 context 取消 | 保持 `failed/INTERRUPTED` 和退出 drain 语义 |
| 媒体下载/上传取消 | 尽快退出，Cleanup 删除临时文件；不重放已消费媒体流 |
| 批量 ID 非正数或超过 50 | 400，不执行任何批量项 |

### 5. Good / Base / Bad Cases

- Good：先以条件 UPDATE 持久化取消，再发送活动 Worker cancel cause；Worker 出队再次 claim；`defer Cleanup` 覆盖所有媒体路径。
- Base：当前页勾选 ID 逐条取消，列表/详情刷新并展示 `已取消`，审计记录 `request.cancel`。
- Bad：从 channel 强行删除 Job、无条件写终态、把请求取消记为 INTERRUPTED、或只 Close 不 Remove 临时下载文件。

### 6. Tests Required

- Store/Access：条件状态转换、并发取消与完成、批量部分成功、审计和额度不返还。
- Queue/Worker：活动/排队/入队窗口取消、cause 区分、注册表 race、根 context 回归。
- Media：Pipe、内存缓冲、临时文件和相册句柄取消后全部退出，断言临时路径不存在。
- Web/Frontend：认证、CSRF、参数上限、逐条结果、当前页选择、切页清空、状态标签和查询刷新。
- Full gate：`gofmt -l .`、`go build ./...`、`go vet ./...`、`go test ./...`、`go test ./... -race`，以及前端 lint/typecheck/test/build。

### 7. Wrong vs Correct

#### Wrong

```go
// 取消后仍允许 Worker 的自然完成覆盖状态，且临时文件可能残留。
UPDATE requests SET status = 'cancelled' WHERE id = ?;
// worker 随后无条件 FinishRequest(... succeeded)
```

#### Correct

```go
// 先限制可取消状态；所有终态写入也带状态条件。
UPDATE requests SET status = 'cancelled', error_code = 'REQUEST_CANCELLED'
WHERE id = ? AND status IN ('queued', 'processing');
```
