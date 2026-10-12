# 日志规范

## 日志实现

Spore 使用标准库 `log/slog`。入口 `cmd/bot/main.go` 根据 `LOG_LEVEL` 创建 logger，并通过构造参数传给 `botapi`、`queue`、`mtproto`、`media` 和 `delivery`。包内不要另建绕过配置的全局 logger。

当前日志使用文本 handler 输出到 stderr；字段名保持稳定的 snake_case，消息文本使用简短中文事件描述，便于人工查看和 grep。

## 日志级别

| 级别 | 使用场景 | 示例 |
| --- | --- | --- |
| `Debug` | 路径选择、详细诊断、可忽略清理失败 | 流式/临时文件路径、Pipe 下载结束 |
| `Info` | 正常生命周期和关键业务进展 | 启动、核心链路就绪、任务开始/结束 |
| `Warn` | 可恢复或尽力而为操作失败 | 状态提示发送失败、删除提示失败、回退登录 |
| `Error` | 任务失败或进程级异常 | 错误码明确的任务失败、初始化失败 |

不要为每个循环或每个媒体 chunk 打 `Info`；不要把可预期的用户输入错误记录成 Error。

## 结构化字段

任务相关日志优先使用：

- `job_id`：任务标识；
- `user_id` / `chat_id`：必要时用于定位授权和投递对象；
- `ref` 或 `message_id`：源消息定位信息；
- `media_type` / `size`：媒体诊断元数据；
- `code`：`apperr.Code`；
- `error`：受控的错误描述或 `Cause`。

字段应在事件发生处直接传入，例如：

```go
log.Info("任务开始", "job_id", j.ID, "user_id", j.UserID, "ref", j.Ref.String())
log.Error("任务失败", "job_id", j.ID, "code", ae.Code, "error", err.Error())
```

同一字段不要在不同层使用不同名字；不要把整个 `Config`、Update、Telegram Message 或 Session 序列化进日志。

## 必须记录的事件

- 启动配置通过后的 worker 数和数据目录（不包含凭据）；
- MTProto 登录/就绪和异常退出；
- 任务入队、开始、成功、失败和错误码；
- 关键媒体路径选择、大小超限拒绝和临时文件清理失败；
- 需要人工排查的 Telegram/Bot API 错误。

## 禁止记录

绝不记录：

- `BOT_TOKEN`、`TG_API_HASH`、验证码、2FA 密码和手机号明文；
- `session.json` 内容、AccessHash 原始缓存内容或认证 token；
- 用户消息正文、caption、实体内容和完整媒体 URL；
- 任意完整 Bot API 请求体或 MTProto 原始响应；
- 可以直接重放或控制用户账号的敏感路径/文件内容。

源链接如果确实需要关联任务，使用受控的 `ref.String()` 或消息 ID，并确认其中没有额外隐私；默认不要记录消息正文。

## 常见错误

- 在 `reply` 或某个包里调用包级 `slog.Default()`，使 `LOG_LEVEL` 和测试 logger 失效。
- 用 `Info` 打印完整错误链，结果把文件路径或远端响应泄露到生产日志。
- 为排查问题临时打印 Session、API Hash 或消息全文后忘记删除。
- 只记录“任务失败”而不带 `job_id` 和错误码，导致无法关联用户状态提示与失败原因。

## Web 管理、备份与 Secret 操作

- 备份上传/校验/确认/应用/失败/回滚、OAuth 配置与绑定、媒体阈值修改、用户资料刷新和 Web 重启必须记录结构化审计；日志和审计只记录动作、状态、操作者、字节数或脱敏状态，不记录 OAuth Secret、访问密钥、Session、完整消息正文或完整消息 URL。
- OAuth Secret 由 `WEB_OAUTH_ENCRYPTION_KEY` 保护；日志只能使用 `secret_set`/`secret_changed`/`secret_cleared` 等布尔状态。禁止打印密文、nonce、主密钥或解密错误的原始响应。
- `POST /restart` 只能记录固定原因和触发结果，生产实现只发送受控 `SIGTERM`；禁止把请求参数拼接成 Shell 命令或记录任意命令文本。
- 页面可以展示 `message_url`，但该 URL 不得进入普通运行日志；私有链接只在受保护页面/CSV中按产品契约输出。
- OAuth 绑定审计只记录 `status`/动作结果；不得记录 GitHub account ID、login、授权码、Secret、nonce 或 token。配置变更只记录 `enabled`、`client_id_changed`、`secret_changed` 等布尔状态。
- 重启审计只在服务端成功取得调度资格后写 `admin.restart`，内容限于固定 `reason`；重复请求不写成功审计，失败只写 `triggered=false` 等固定结果。
- 二维码 URL 不得出现在日志、审计、URL 历史或持久化文件中；二维码图片端点的所有状态（包括错误）都必须 `Cache-Control: no-store`。
