# 文档同步约束（Docs Sync）

> **核心规则**：任何功能的新增、变更、删除，完成代码后**必须同步更新对应文档**，让功能与文档保持一致。文档更新是提交前的必检项，不是"有空再补"的后续工作。
>
> 文档是功能的契约面：运维按它排障、用户按它使用、未来的 AI 会话按它理解代码库。过时的文档比没有文档更糟——它会给出错误指引。

---

## 触发条件

以下任一情况发生时，必须走一遍文末的同步检查清单：

- [ ] 新增 / 修改 / 删除环境变量、配置键（env 或数据库 settings 键）
- [ ] 新增 / 修改 / 删除 HTTP API 端点或其字段
- [ ] 新增 / 修改 / 删除用户可见功能（Bot 命令、管理端页面/字段、监控指标）
- [ ] 修改核心链路的数据流、并发模型或路径选择（如媒体管线、队列）
- [ ] 改动 `docs/reference/architecture.md` §12 业务全景流程图覆盖的任一环节：
      提交与准入、队列优先级、取数/下载/发送路由、成功后副作用（缓存写回 /
      绑定频道投递 / 置顶 / 云盘）、监听预热、缓存迁移、历史恢复、事件告警——
      必须在同一提交批次同步更新 §12 流程图（含分支条件与开关名称）
- [ ] 修复了一个有排障价值的线上问题（现象/根因/方案值得留档）
- [ ] 重命名或删除了任何"文档里可能提到"的标识符

---

## 改动 → 文档映射表

按改动内容查表，"必须检查"列的文档逐个确认是否需要更新（更新或确认无需改动都算完成检查）：

| 改动内容 | 必须检查的文档 |
| --- | --- |
| 环境变量（新增/默认值/边界/删除） | `.env.example`、`docs/reference/configuration.md`（变量表 + 覆盖规则节） |
| 数据库 settings 键（管理端可调项） | `docs/reference/configuration.md`、`docs/reference/api.md`（`/api/v1/settings` GET/POST 字段表）、前端 `SettingsPage.tsx` 的字段说明文案 |
| HTTP API 端点或响应字段 | `docs/reference/api.md` |
| 媒体管线 / 下载路径 / 并发模型 | `docs/reference/architecture.md`（含 §3.4 流程图）、`.trellis/spec/backend/telegram-media.md`、`docs/ops/troubleshooting.md`（若有排障价值） |
| 业务链路任一环节（提交/准入/队列/执行/复用/投递/缓存/监听/恢复/迁移/事件） | `docs/reference/architecture.md` **§12 业务全景流程图（必须同步更新，唯一权威版本）**与 §11 执行顺序、`docs/guide/usage.md` |
| 监控指标（新增/删除采样项） | `docs/reference/api.md`（`/api/v1/system-metrics`）、前端 `SystemMetricsSection.tsx` 图表与描述、`docs/reference/architecture.md` 中 monitor 的描述 |
| 用户可见功能（面向使用者的行为变化） | `README.md`（功能特性）、`docs/guide/introduction.md`、`docs/guide/usage.md` |
| Bot 命令 / 准入规则 | `docs/guide/usage.md`、`.trellis/spec/backend/telegram-botapi.md` |
| 部署形态 / 低内存等运维建议 | `docs/guide/deployment.md`、`docs/ops/operations.md`、`docs/reference/architecture.md` 运维建议段 |
| 排障知识（踩坑/真机结论） | `docs/ops/troubleshooting.md`（新条目加最上面，含日期、现象、根因、方案、防复发要点） |
| 架构决策 / 新增组件 | `docs/reference/architecture.md`（目录树 + 对应章节）、`.trellis/spec/backend/`（或 frontend/）对应规范 |

`.trellis/spec/` 内的规范文件同样是文档：改动并发、共享缓存、队列、媒体管线、Web 契约时，同步更新对应 spec（这是 workflow Phase 3.3 的要求，与本约束是同一件事的两层——spec 面向未来开发者，docs/ 面向用户与运维）。

---

## 提交前同步检查（并入质量检查）

1. **查表**：对照上方映射表，列出本次改动涉及的文档清单（写进任务的 implement.md/check 项）。
2. **更新**：文档中的流程图、字段表、默认值、生效方式（即时/重启）、边界范围与代码逐项核对。
3. **反查残留**：对重命名/删除的标识符，用 grep 反查全库文档，确认没有旧名称残留：

   ```bash
   grep -rn "旧键名或旧标识符" docs/ README.md .trellis/spec/ .env.example
   ```

4. **一致性**：同一事实在多个文档出现时（如默认值、边界），确认所有出处同步改掉——文档间的相互矛盾比单处缺失更伤信任。

---

## 反模式（不要这样做）

- ❌ "文档后面再补"——没有后续任务会真的补它；文档更新属于同一个任务的同一个提交批次。
- ❌ 只改 `docs/reference/` 不改 `.env.example`（或反之）——配置类事实至少有三处出处（示例文件、配置参考、API 参考），漏一处就是矛盾。
- ❌ 在文档里复述实现细节（行号、函数名）——写行为与契约，不写代码坐标；行号式文档第一次重构就过期。
- ❌ 把排障结论只留在对话或提交信息里——有复现价值的进 `docs/ops/troubleshooting.md`，有防复发价值的进 `.trellis/spec/`。
