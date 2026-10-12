# 跨层契约思考指南

> Spore 的主要风险在 Telegram 双通道、队列、消息转换、媒体传输和资源清理的边界，而不是单个函数内部。

## 先画完整数据流

```text
Telegram Update
  → botapi 私聊/白名单过滤
  → tmeurl.Parse：SourceRef
  → queue：Job
  → mtproto：InputPeer / tg.Message
  → message：Item / Media
  → media：io.Reader + Cleanup
  → delivery：Bot API 新消息
```

对每条箭头确认：

- 输入和输出的 Go 类型是什么？
- 哪一层负责校验和错误分类？
- 数据是否丢失了 caption、entity、媒体元数据或目标 chat？
- 资源由谁创建、谁消费、谁关闭或删除？
- context 取消后下一层会看到什么？

## 关键边界

| 边界 | 必须保持的契约 | 典型风险 |
| --- | --- | --- |
| Bot API ↔ `botapi` | 只接受私聊消息，白名单先于业务 | 群聊误触发、未授权用户进入队列 |
| `botapi` ↔ `tmeurl` | 文本转为统一 `SourceRef` | 各层重复解析、私有 ID 语义错误 |
| `tmeurl` ↔ `queue` | Job 保存用户、目标 chat、SourceRef 和状态消息 ID | 失败提示发错 chat 或无法清理占位消息 |
| `mtproto` ↔ `message` | Telegram 原始消息转内部 Item/Media | 第三方 TL 类型泄漏、caption/entity 丢失 |
| `message` ↔ `media` | Media 提供下载位置、类型、大小和文件名 | 超限后仍下载、引用失效未刷新 |
| `media` ↔ `delivery` | 单次消费 Reader，发送后必须 Cleanup | Pipe goroutine 泄漏、临时文件残留、重复消费 |
| `queue` ↔ `delivery` | AppError 转用户中文提示，状态消息尽力删除 | 原始错误泄露、失败路径重复回复 |

## Telegram 特有契约

- MTProto entity 的 Offset/Length 按 UTF-16 code unit；`message` 转 Bot API HTML 时必须正确处理 CJK、emoji、嵌套实体和转义。
- Bot API 的文本与 caption 有长度限制；截断时要给附注预留预算，并保持 HTML 标签合法。
- `t.me/c/<id>/<message>` 保存裸 ChannelID；AccessHash 来自 Peer 缓存或 dialogs 遍历，不能用展示用的 `-100` ID 代替。
- 相册由 `GroupedID` 聚合，整组发送时 caption 只放一次；不支持整组时必须在下载前决定是否逐条降级。

## 资源生命周期检查

### 流式媒体

```text
media.Open
  → io.Pipe
  → 下载 goroutine
  → delivery 消费 Reader
  → Send 成功/失败/取消
  → Cleanup 关闭读端，释放写端
```

发送方不能只在成功分支清理。若消费者放弃，Pipe 写端必须能收到关闭信号，否则下载 goroutine 可能一直阻塞到任务超时。

### 临时文件

```text
创建受控路径 → 下载 → 打开 Reader → 发送 → Close + Remove
```

失败下载、打开失败、发送失败、context 取消和进程重启都要有清理策略；启动清理只能删除程序自己识别的孤儿文件。

## 错误和取消传播

- 外部库错误在最接近边界的包转换为 `apperr`，不要让上层猜字符串。
- `Code` 用于流程分支，`UserText` 用于用户回复，`Cause` 用于日志；三者不要混用。
- 任务 context 的取消必须贯穿 Fetch、下载和发送；退出 drain 使用独立的短暂发送窗口，不要让已取消的 context 反复失败。
- 对可重试操作明确请求体是否可重放。无请求体的 Bot API 操作可以按 RetryAfter 轻量重试，已经消费的媒体流不能盲目重试。

## 实现后检查清单

- [ ] 画过从 Update 到最终 delivery 的完整链路。
- [ ] 每个边界都有明确的类型、错误和资源所有者。
- [ ] 私聊/权限、URL、文件大小、HTML 长度等校验发生在正确入口。
- [ ] 测试覆盖空值、非法输入、emoji、超限、取消和失败清理。
- [ ] 跨包没有重复定义相同常量、错误文案或解析逻辑。
- [ ] `go test ./... -race` 覆盖涉及 channel、mutex、cache 或 worker 的修改。
- [ ] 日志只包含受控诊断字段，没有消息正文和凭据。
