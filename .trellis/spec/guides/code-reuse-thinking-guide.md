# 代码复用思考指南

> 适用于 Spore Go 代码。目标不是为了抽象而抽象，而是避免同一业务契约在多个包中漂移。

## 写新代码前先搜索

```bash
grep -RIn "相似函数或错误码" internal cmd
grep -RIn "MaxFileSize\|StreamLimit\|AlbumMaxItems" internal
grep -RIn "Cleanup\|RefreshMedia\|UserText" internal
```

先回答：

- 相同逻辑是否已经属于某个现有包？
- 是否存在现成接口、模型、错误码或 helper？
- 新抽象是否拥有清晰的数据和资源所有权？
- 复用是否会让包依赖反向或泄漏 Telegram 第三方类型？

## Spore 中需要保持单一来源的契约

### 配置和限制

媒体大小限制、流式分界和相册上限必须各有一个权威定义。调用点读取 `config.Config` 或 `message.AlbumMaxItems`，不要在 `queue`、`media`、`delivery` 各写一份数字。

### 媒体生命周期

`media.Open` 统一选择流式或临时文件路径并返回 `Handle{Reader, Cleanup}`。发送方只消费 `Reader`，并通过 `defer Cleanup()` 覆盖成功、失败和取消；不要在 `queue` 外再实现一套下载和删除逻辑。

### 错误和刷新

- 用户文案统一从 `apperr.UserText` 获取；不要在 handler、worker、delivery 各维护同一错误码的不同中文提示。
- file reference 失效由 `mtproto.Fetcher.RefreshMedia` 负责刷新；调用编排层只负责决定是否按约定重试一次。
- Bot API 轻量 429 重试由 `delivery` 统一处理，媒体流上传不能被多个调用点重复重试。

## 什么时候抽象

适合抽象：

- 同一非平凡逻辑已在三个位置出现；
- 逻辑有资源、错误或并发风险，集中后更容易测试；
- 多个调用方确实共享同一个输入/输出契约。

不适合抽象：

- 只有一次使用的简单包装；
- 为了隐藏一个一行表达式而增加新包；
- 抽象需要让 `message` 依赖 `delivery`，或让纯函数依赖网络客户端；
- 只是名字相似、生命周期和错误语义实际不同的代码。

## Go 特有检查

- 先搜索现有接口再新增接口；Go 接口应由消费者按最小方法集定义。
- 不要复制 slice/map 后假设深拷贝；明确是否需要 `slices.Clone` 或独立 map。
- 不要复制 `context`、`Cleanup` 或 `io.Reader`；它们代表生命周期或一次性消费契约。
- 修改枚举式 `switch` 时检查所有分支，尤其是 `MediaKind`、错误码和登录模式。
- 同一字段只能有一个规范化入口：例如 `SourceRef` 由 `tmeurl.Parse` 生成，业务层不再重新解析 URL。

## 批量修改后

```bash
grep -RIn "旧常量或旧 helper" internal cmd
gofmt -l .
go test ./...
```

确认没有漏改调用点、重复定义或出现新旧逻辑并存。对于跨包复用，补一个能证明共享行为的表驱动测试，而不是只检查两个调用点都能编译。

## 提交前清单

- [ ] 搜索过现有函数、接口、常量和错误码。
- [ ] 没有复制媒体下载、HTML 渲染、错误提示或清理逻辑。
- [ ] 共享限制和资源命名只有一个来源。
- [ ] 新 helper 放在真正拥有该数据/资源的包。
- [ ] 包依赖方向仍然清晰，没有为了复用制造 import cycle。
- [ ] 复用后的成功、失败和取消行为都有验证。
