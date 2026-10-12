# 错误处理规范

## 总体原则

Go 错误是显式返回值。函数在能判断的边界立即处理或包装错误，向上层返回；不要用 panic 作为业务控制流，也不要把底层错误字符串直接发给 Telegram 用户。

Spore 的错误边界是：底层 gotd/Bot API 错误在所属适配层分类，worker 在任务边界统一转为 `apperr.AppError`，用户收到 `apperr.UserText(code)`（任务失败提示另附来源消息链接，便于多条链接并发时区分失败来源），原始原因只进入结构化日志。

## AppError 约定

`internal/apperr/errors.go` 定义：

```go
type AppError struct {
    Code    Code
    Message string
    Cause   error
}

func New(code Code, message string) *AppError
func Wrap(code Code, cause error) *AppError
func From(err error) *AppError
func UserText(code Code) string
```

- `Code` 是稳定的机器可读分支键，例如 `INVALID_URL`、`CHANNEL_NOT_ACCESSIBLE`、`FILE_TOO_LARGE`。
- `Message` 是内部描述，不是用户协议；可以为空并回落到标准文案。
- `Cause` 保留底层原因，使用 `Unwrap` 支持 `errors.Is`/`errors.As`。
- 已经是 `AppError` 时不要再次覆盖其错误码；未知错误统一回落 `INTERNAL_ERROR`。
- 新增错误码必须同时补齐用户中文提示、触发边界和测试。

示例：媒体层在下载前做大小拒绝，不发起无意义的网络请求：

```go
if m.Size > opt.MaxFileSize {
    return nil, apperr.New(apperr.CodeFileTooLarge, "媒体超过配置上限")
}
```

底层错误需要保留原因时使用包装：

```go
return nil, apperr.Wrap(apperr.CodeMediaDownloadFailed, err)
```

## 分层处理

- `config` 返回配置错误，启动入口打印简短错误并退出，不启动 Bot。
- `tmeurl` 返回解析失败；handler 将其转为 `INVALID_URL` 用户文案。
- `mtproto` 将频道不可访问、消息不存在、FloodWait 和 file reference 失效等 Telegram 错误映射到业务边界。
- `media` 负责文件大小、下载失败和 Cleanup；不把上传错误伪装成下载错误。
- `delivery` 负责 Bot API 错误和轻量 429 重试；媒体流已经消费后不可盲目重试。
- `queue.Process` 是任务最终边界：记录 `Code`，发送 `UserText`（附来源消息链接，与取消终态同款受控 URL），最后尽力删除状态提示。

## Context、取消与清理

- 所有网络、下载和任务处理函数按约定接收第一个 `context.Context` 参数，并向下传递。
- worker 为单个任务设置 15 分钟超时；取消或超时后仍须执行临时文件、Pipe 和状态资源的 `defer` 清理。
- 进程退出时，已入队任务由 queue 的 drain/discard 路径处理；不要在已取消的 context 上重复发送无效请求。
- Cleanup 属于资源所有者的契约：返回 `media.Handle` 的代码必须让消费方无论发送成功或失败都调用 `Cleanup`。

## 用户提示与日志边界

用户看到稳定、简短的中文文案，例如“媒体下载失败，请稍后重试。”；不得显示 `err.Error()`、Token、手机号、Session、文件路径或远端 URL。

日志可以保留错误码和受控的底层错误用于排障，但遵守 [日志规范](./logging-guidelines.md) 的脱敏约束。

## 禁止模式

- `if err != nil { return nil }` 之类吞错，导致任务看似成功或资源不清理。
- 在每一层重复调用 `apperr.From` 并覆盖已有业务错误码。
- 用 `panic`、字符串匹配或全局变量传递业务错误。
- 捕获错误后不判断 context，却在退出阶段继续调用 Bot API。
- 任务失败后同时由多个层发送用户错误提示，造成重复消息。
- 把原始 Telegram 错误直接作为 Bot 回复，泄露内部细节或不稳定文本。

## Web 管理端错误分类契约

- `/api/v1` 与 SSR 必须分离参数错误和服务端错误：OAuth 参数/主密钥无效返回 400；SQLite、文件系统或配置存储失败保留 `apperr` 错误码并返回受控 5xx 文案。
- `safeBackupError` 及页面/API 映射不得截断或拼接 `err.Error()`；路径、SQLite 驱动信息、Secret、Session 和 token 只允许进入符合脱敏要求的结构化日志。
- 受控重启只有 `scheduleRestart` 成功占用调度资格后才能写成功审计；重复请求返回 `CONFLICT`/409，`RestartFunc` 失败需复位状态并允许重试。
- 参数错误示例：`oauthSettingsParamError` → 400；存储错误示例：`apperr.Wrap(apperr.CodeStoreUnavailable, err)` → 503；两者不能用字符串错误互相替代。

## SPA API 认证错误契约

- `/api/v1` 的认证失败永远返回 JSON `401`，不得重定向到登录 HTML；CSRF 缺失或不匹配返回 JSON `403` 和稳定码 `CSRF_FAILED`。
- 错误响应固定为 `{"error":{"code":"...","message":"..."}}`；`message` 只能来自 API/`apperr.UserText` 受控文案，不能使用底层 `err.Error()`。
- `GET /api/v1/session` 只交付当前用户摘要与会话级 CSRF token，必须 `Cache-Control: no-store`；不得交付 Session Secret、OAuth Secret、访问 Token 或 MTProto 二维码 URL。
- API 业务错误按 `store.ErrNotFound`、`STORE_CONSTRAINT`、`STORE_UNAVAILABLE` 等稳定码映射 404/409/503；新增端点必须补认证、CSRF、错误信封和旧 SSR 回退测试。
