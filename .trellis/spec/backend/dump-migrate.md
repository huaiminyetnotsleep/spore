# 缓存迁移契约（迁移旧缓存）

## 1. Scope / Trigger

适用于 internal/dumpcache 迁移运行器、store dump_migrate DAO、web dumpcache 迁移 API、frontend `/admin/dump-migrate` 页面与频道设置页迁移入口。触发：改迁移执行、运行/明细持久化、暂停/继续/取消控制或迁移页面。字段表与端点详情以 docs/reference/api.md §12b 与 database-schema.md §3.21/3.22 为权威，此处只记执行契约。

## 2. Signatures

```go
// dumpcache —— StartMigrate 只做校验+建运行记录，执行在 WithoutCancel goroutine
StartMigrate(ctx context.Context, fromChannelID int64) error
// store（internal/store/dump_migrate.go）
CreateDumpMigrateRun(ctx, from int64, to []int64, total int) (DumpMigrateRun, error)
DumpMigrateRunStatusOf(ctx, id int64) (string, error)   // 迁移门轮询的轻量读
SetDumpMigrateRunStatus(ctx, id int64, to string) error // 状态机迁移（dumpMigrateAllowedFrom）
SetDumpMigrateFinished(ctx, id int64, lastErr string) error // 仅 running|paused → completed
InsertDumpMigrateItem / ListDumpMigrateItems / ReconcileDumpMigrate(ctx)
```

表 dump_migrate_runs / dump_migrate_items（v29 迁移）。唯一部分索引 `idx_dump_migrate_one_active`（WHERE status IN running|paused）硬保证全局单活跃运行，并发 StartMigrate 靠它兜底（约束错误 → 友好中文提示）。创建事务内裁剪只保留最近 20 条运行并同事务审计。

## 3. Contracts

- 状态机：run `running/paused/completed/cancelled/interrupted`；item `migrated/skipped/failed`（failed 口径是逐来源条目，逐目标失败记在 results_json）。
- **控制信号不得放内存**：dumpcache.Service 随 MTProto 生命周期重建，内存 channel/mutex 会在重建后失联或产生孤儿。暂停/继续/取消由 Web handler 直接写 DB；运行 goroutine 每条目 + 每批顶部轮询 `DumpMigrateRunStatusOf` 作门，paused 时按 `migratePausePoll` 空转等待，读到 cancelled/interrupted 静默退出（≤1 批延迟）。
- goroutine 只写 completed，且 `WHERE status IN (running,paused)`——取消后收尾不得覆盖 cancelled。`interrupted` 只由 `ReconcileDumpMigrate` 写（newDumpService 时调用，同时覆盖崩溃遗留与 MT 生命周期替换两类场景）；中断后管理员重新发起即可幂等续迁。
- 兼容：GET/POST `/api/v1/dumpcache/migrate` 响应形状保持 v24 不变（progress 新增可选 `paused`/`run_id`）；进度视图由 web 从 store 组合，dumpcache.Service 不再持有 MigrateProgress。
- 控制端点无 id（作用于最新 run）：无活跃 run 或状态不匹配 → 409 CONFLICT（与项目内 mtproto/restart 等状态冲突端点一致）；并发竞态经状态机 UPDATE 0 行 → STORE_CONSTRAINT 同样 409。POST 启动的参数错误仍走旧契约 400 + 具体 message。
- 数据红线：items 只存频道/消息 ID、状态、截断错误（`truncateErr` 200 runes）与逐目标结果；正文、媒体 URL、凭据不入库。
- 源频道不可读（`delivery.IsChannelGoneError`）：先记 failed item 再中止剩余条目（交给复用自愈重建）；单目标复制失败：item failed + results_json，源行保留不删。
- 页面：`/admin/dump-migrate`（最新运行进度 + 历史列表 + 控制按钮），`/admin/dump-migrate/runs/:id`（逐条明细 + 状态过滤）；发起入口只在频道设置页。前端轮询 2s，仅在 running/paused 时进行。

## 4. Validation & Error Matrix

| 条件 | 结果 |
| --- | --- |
| 未认证 / CSRF 无效 | 401 / 403，服务不调用 |
| POST 迁移：参数非法 / dump 未启用 / 已有活跃运行 | 400（保留旧契约，具体中文 message） |
| 控制端点：无活跃 run / 状态不匹配 / 并发竞态 | 409 CONFLICT / STORE_CONSTRAINT |
| runs/{id}、items 的 run 不存在 | 404 NOT_FOUND |
| items `status` 非 migrated/skipped/failed | 400 INVALID_URL |
| dumpCache 依赖不可用 | 受控 503 |
| 迁移门读到 cancelled / interrupted | goroutine 静默退出，不写 completed |

## 5. Good / Base / Bad Cases

- Good：423 条分批迁移，逐条明细可查；完成后（源频道未启用时）源行清理。
- Base：幂等重跑——目标已有最新条目记 skipped；暂停后继续沿用同一 run，不新建。
- Bad：用内存 channel 发暂停信号——MT 生命周期重建后信号失联、旧 goroutine 变孤儿继续写库。

## 6. Tests Required

- store（临时 SQLite）：状态机合法/非法迁移、0 行约束、创建竞态约束、裁剪保留 20、Reconcile 中断语义。
- dumpcache（fake sender + `-race`）：完成记明细、暂停→继续、取消后可重启、并发 StartMigrate 拒绝、幂等 skipped、源消失中止、部分目标失败记 failed。
- web：认证/CSRF/依赖 503、GET/POST 兼容形状、runs/items 分页过滤与 404、控制序列 409 与终态再控制 409。
- frontend：控件按状态渲染、确认流、明细状态过滤、e2e 冒烟路由与假 API fixture。

## 7. Wrong vs Correct

Wrong：`select { case <-s.pauseCh: ... }` —— 控制信号存在 Service 内存里。

Correct：控制端点写 DB 状态；运行 goroutine 每条目轮询 `DumpMigrateRunStatusOf` 作门。控制面（Web→DB）与执行面（goroutine 轮询 DB）只通过运行记录状态列交汇，跨 Service 重建安全。
