# 监听源专属转发目标契约

## 1. Scope / Trigger

适用于 `internal/watch`（forward.go/plan.go 投递计划与选择校验）、`internal/listener` 快路径、`cmd/bot/app.go` 受保护内容回退镜像、`internal/botapi` `/watchtarget` 选择面板、`internal/store` v28 策略列、`internal/web` 逐源全局镜像开关与 frontend 监听源页。改动任何转发目标解析、Bot 路由或循环防护的代码必须读本规范。

## 2. Signatures

```go
// internal/watch
func (s *Service) ForwardTargets(ctx context.Context, src store.WatchSource, cacheChannels []int64) []ForwardTarget
// ForwardTarget{ChannelID int64; Title string; Kind string /* user_binding|global_mirror */; BotID int64}
func (s *Service) EligibleForwardTargets(ctx context.Context, src store.WatchSource) ([]store.ChannelBinding, error)
func (s *Service) EligibleForwardTargetsForInvite(ctx context.Context, userID, inviteID int64) ([]store.ChannelBinding, error)
func (s *Service) SetForwardTarget(ctx context.Context, userID int64, srcTarget string, bindingChannelID int64) (store.WatchSource, error)
func (s *Service) ClearForwardTarget(ctx context.Context, userID int64, srcTarget string) (store.WatchSource, error)
func (s *Service) StageInviteForwardTarget(ctx context.Context, userID, inviteID, bindingChannelID int64) (store.WatchInviteRequest, error)
func (s *Service) ClearInviteForwardTarget(ctx context.Context, userID, inviteID int64) (store.WatchInviteRequest, error)
func (s *Service) UserSourceByTarget(ctx context.Context, userID int64, target string) (store.WatchSource, error)
func (s *Service) ForwardTargetViewOf(ctx context.Context, src store.WatchSource) ForwardTargetView
func (s *Service) SetGlobalMirror(ctx context.Context, id int64, enabled bool) (store.WatchSource, error)
```

两个投递路径（listener 快路径、app.go watchDumpMirror 回退）**必须**经同一 `ForwardTargets` 解析计划，不得各自直读 `watch_forward_channels`。

## 3. Contracts

- 计划合并顺序：缓存频道 → 专属目标（`forward_binding_channel_id` 复验 active+归属+BotID 同路由）→ 全局镜像（仅 `global_mirror_enabled` 为真），按 channel ID 去重各投一次；`watch_events.targets_json` 只记成功落点。
- **精确 Bot 路由**：user_binding 目标按 `src.BotID` 精确取 sender（listener `senderForBot` / app `pool.MemberByID`）；Bot 缺失或禁用只跳过 + 结构化日志「不回退主 Bot」，绝不回退主 Bot 或全局目标。global_mirror 经主 Bot。
- 选择校验（SetForwardTarget/Stage 同一套）：源归当前用户且非管理员源；绑定 active 且 `UserID == src.AddedBy`；绑定 `BotID` 与源投递 Bot 同路由；目标不是任何 watch_sources 行、不是任何缓存频道。私有邀请激活时 `transferStagedForwardTarget` 按同一套校验复验后原子转移。
- 暂存生命周期：`watch_invite_requests.forward_binding_channel_id` 无外键（等待期绑定被删保留脏 ID，激活复验兜底）；**rejected/approved 清空，failed 保留**（重试连续性，偏差见任务 design.md 2026-10-15 注）；读取经 COALESCE 归一为 0。
- Bot 面板：`/watch` 提交成功即自动弹面板（`SubmitOutcome.RequestID`/`Channel` 驱动），`/watchtarget <源>` 重开；锚点消息 + 按钮面板两段式；回调数据只带 `pi:wtgt:|pi:wtpage:|pi:wtclr:` + 锚点 prompt ID + 下标，频道 ID 只存服务端 pending；TTL 5 分钟、按 chat 单活动、原子消费。
- `/watchlist` 逐源显示 `↳ 专属转发：标题 / 未设置 / 目标已失效（/watchtarget 重选）`。
- Web：`POST /api/v1/watch-sources/{id}/global-mirror` `{enabled}`（认证+CSRF+审计 `watch_source.global_mirror`）；列表行带 `forward_target`（set/stale/unset，管理员源缺省）与 `global_mirror_enabled`。管理员只读用户专属目标，不代选。
- `channel_bindings.kind`（v28）：绑定时从 `chat.Type` 快照（channel/supergroup），管理端「监听转发频道」候选只列 `kind == "channel"`（与 `VerifyChannel` 仅接受频道一致）；历史空串经重绑或 `refreshBindings` 回填；刷新 upsert 不携带 kind 时保留库中既有值。

## 4. Validation & Error Matrix

| 条件 | 结果 |
| --- | --- |
| 源不归当前用户 / 邀请非本人或已终态 | `store.ErrNotFound`（不区分"别人的源"与"不存在"） |
| 目标是任何监听源 / 缓存频道 | `ErrForwardTargetLoop`（选择时）；`SubmitForwardConflict`（/watch 提交侧） |
| 新源 ID 已被专属目标或全局转发频道占用 | `SubmitForwardConflict`（Submit/AdminAdd/邀请激活） |
| 绑定非 active / 归属不符 / Bot 路由不符 | `ErrForwardTargetInvalid`（选择时）；运行时静默跳过该目标 |
| 全局转发频道保存撞监听源 | `SaveWatchForwardChannels` 拒绝（syscfg） |
| 终态邀请变更暂存 | `ErrInviteRequestClosed` |
| 运行时目标 ID 是监听源（历史脏数据/并发窗口） | 计划内 `notWatchSource` 过滤 + 告警日志，不投递；store 读失败按不安全跳过 |

## 5. Good / Base / Bad Cases

- Good：用户源带有效专属目标 + 管理员开镜像 → 缓存 + 专属（源 Bot 精确投递）+ 全局各一份，`targets_json` 与实际一致。
- Base：v28 前旧用户源无专属目标且镜像默认关 → 仅缓存预热，行为不劣化。
- Bad：回退路径直读全局频道绕过计划；专属目标 Bot 失联回退主 Bot；把频道 ID 塞进回调数据。

## 6. Tests Required

- store：v27→v28 真实升级收敛（镜像回填、旧列空值、kind 空串）；upsert 不重置策略列；绑定物理删除同事务清引用（FK + 暂存列）。
- watch：候选过滤（active/归属/路由/循环）、暂存转移复验、failed 保留 / rejected 清空、Submit 冲突分支。
- listener + cmd/bot：两路径共用计划去重、精确 Bot、失联跳过、运行时循环过滤、targets_json 只记成功。
- botapi：面板选择/翻页/清除/过期重放、错误哨兵文案（剥离 apperr 壳）、watchlist 状态行。
- web/frontend：镜像开关 403/400/审计、forward_target 视图渲染、候选只列 kind=channel。

## 7. Wrong vs Correct

Wrong（回退路径绕过计划）：

```go
channels := syscfg.WatchForwardChannels(ctx) // 直读全局配置
for _, ch := range channels { mirror(ctx, ch) }
```

Correct：

```go
plan := watchSvc.ForwardTargets(ctx, src, cacheChannels) // 与快路径同一计划
for _, t := range plan { send(t) }                        // user_binding 按 t.BotID 精确路由
```

前者让受保护内容回退无条件镜像到全局频道（用户源隐私泄漏）且绕过循环过滤；后者保证两条路径目标集合、去重与循环防护完全一致。
