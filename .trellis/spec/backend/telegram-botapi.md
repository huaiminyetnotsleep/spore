# Bot 命令菜单与 botapi 契约

> 来源：`.trellis/tasks/09-07-private-command-cleanup`（线上事故：私聊残留 chat 菜单覆盖 default，重启无效）。适用于 `internal/botapi` 中命令注册、命令菜单及 `/start` 兼容行为相关变更。

## Scenario: 命令菜单按 (scope, language) 独立存储

### 1. Scope / Trigger

- 触发：改动 `RegisterCommands`、公开命令列表、`/start` 行为，或排障「/help 有命令但快捷菜单缺命令」时，必须遵守本契约。
- `RegisterCommands`（`internal/botapi/bot.go`）只写 **default scope + 空语言** 的十六条公开命令（含 `/download`、`/pin`、`/watch`、`/watchtarget`、`/watchlist`、`/unwatch`）；启动注册失败仅 Warn 后继续长轮询（有意设计——命令文本分流不依赖菜单），无独立重试；MTProto 每次进入 ready 回调都会重新注册一次。
- Telegram 命令菜单按 `(scope, language_code)` 独立存储，**chat scope 覆盖 default**：更新 default 永远不会清除其他 scope 的残留——重启、重发 `setMyCommands`、换客户端都无法修复残留的 chat 菜单（真机 2026-09-07）。
- 兼容修复：私聊 `/start` 先完成原回复，再尽力 `deleteMyCommands`——请求必须**显式** `scope=chat + 实际 msg.Chat.ID`（不是 `from.ID`），语言集合 = 空语言 + 用户语言经 `golang.org/x/text/language` 归一后的两字母码（非法/无法归一跳过，不透传原始标签）；失败仅 Warn 受控字段（`chat_id`/`language_code`/`category`，不记原始错误——SDK 错误可能包含带 token 的请求 URL）；实例内按 `(chatID, language)` 去重，成功记住、失败留待下次 `/start` 重试，总预算 2s。
- 红线：绝不删除 default / `all_private_chats` / 其他 chat；绝不用 `SetMyCommands` 往 chat scope 写命令来「修复显示」（制造新覆盖）；`language_code` 空串不是通配符，只代表空语言槽位。

### 2. 排障

- `getMyCommands` 必须按 `(scope, language)` 逐个查询：default、`all_private_chats`、`chat+chat_id` 各查 `""` 与 `zh`；只查 default 不能证明用户实际看到的菜单。
- 日志关键字 `Bot 命令菜单`（注册成功/失败各一条）；成功日志只证明 API 返回 ok，不证明客户端展示。
- 服务端配置确认正确后再怀疑客户端缓存：换一个官方客户端对照，不要反复重启服务。

### 3. Tests Required

- `internal/botapi/commands_test.go`：全部删除请求断言 `scope.type=chat` 与实际 chatID（构造 chatID≠userID 的消息）、语言归一表驱动、成功去重/部分失败重试/并发不重复（`-race`）、父 ctx 取消不污染回复 context、日志脱敏、default 十六条注册回归（含 `/download`、`/pin`、`/watch`、`/watchtarget`、`/watchlist`、`/unwatch`）。

## Scenario: 无参命令 ForceReply 与 `/download` 目的地选择

### 1. Contracts

- 参数必填命令无参时使用每 Bot 实例隔离的内存 pending；ForceReply、用法/取消按钮和目的地按钮都必须校验 owner、chat、TTL，连续新命令只自动取消仍在等待输入/选择的旧 pending。
- `/download 链接` 继续立即使用默认目的地，`/download 目的地 链接` 继续立即使用指定目的地；仅无参 `/download` 的 ForceReply 回复使用智能分流。
- ForceReply 回复 `目的地 链接` 直接进入现有 `handleDownload`；只回复链接时先按 `UserDownloadStatus → rclone Available → 全局 Enabled → 链接解析/上限 → EnabledDestinations` 顺序预检，未授权用户不得看到目的地名称。
- 启用目的地为 0 个时返回受控不可用文案；1 个时显式使用唯一目的地直接提交；2 个及以上时暂存链接并显示动态按钮，默认目的地排首标记、每行两个、每页八个。
- callback_data 只携带 prompt ID 与目的地快照 index/页码，禁止包含链接、消息正文或凭据；目的地点击必须在 mutex 内原子消费，重复/并发点击最多一次进入提交。
- 点击目的地后必须重写 `/download <目的地> <链接>` 并重新进入现有 handler，不得直接调用 `Access.Submit`；最终链路重新校验用户权限、配置、目的地、链接上限和队列，worker 继续执行时最终复核。
- 暂存链接只存在内存，进入选择阶段后 TTL 重新计 5 分钟；过期、重启、错误用户、取消或新命令均不得提交或扣额度。

### 2. Tests Required

- 覆盖 0/1/多目的地、显式目的地直达、默认目的地排序、两列布局、分页、callback 64-byte 上限、无权限不泄露目的地、配置变化复核、取消/过期/重复/并发点击和连续命令自动取消。
- pending mutex 与 callback 原子消费变更必须运行 `go test ./internal/botapi -race`，并保留 direct `/download`、usage/cancel、常驻键盘及公开命令注册回归。

## Scenario: 无参 `/pin` 模式选择（仅置顶 / 置顶+转存网盘）

### 1. Contracts

- 仅无参 `/pin` 的 ForceReply 链接回复进入模式选择（`promptAwaitPinMode`，`pi:pinmode:` 回调）；直接 `/pin <链接>` 与引用回复路径保持立即执行，行为不变。
- 入口顺序：`requireEnabled → 链接解析/上限 → 云盘装配与运行时（CloudStatus==nil/!Available/!Enabled）`；云盘不可用时不出现选择面板，直接按仅置顶提交并附说明。
- 选「置顶+转存」时回调路径重检 `requireDownloadReady` 与默认目的地：失败降级为仅置顶；成功则以 `submitPinWithCloud` 配对提交——先 TG 任务（pin=true、不豁免提交间隔）后云盘任务（默认目的地、BatchContinuation=true 豁免间隔），两路独立 Submit/占位/反馈，某一路被拒不影响另一路；每条链接各扣 1 次额度（合计 2）。
- callback_data 只携带 prompt ID 与模式位（0/1），禁止包含链接或正文；`selectPinMode` 在 mutex 内原子消费，一次性。

### 2. Tests Required

- `internal/botapi/pin_prompt_test.go`：模式面板分流、仅置顶回调、配对提交两次 Submit 的参数（pin/cloudDest/BatchContinuation）、云盘关闭入口直提、无权限/无默认目的地降级、面板渲染与 64-byte 回调上限。

## Scenario: /pin、/cancel 引用回复交互（v22 锚点）

### 1. Scope / Trigger

- 触发：改动 `/pin`、`/cancel` 的回复路径、`sent_messages` 锚点写入/反查、或排障「回复了但提示未找到任务」时，必须遵守本契约。
- 命令作为对 bot 消息的回复发送时（`msg.ReplyToMessage`），入口层把 `replyMsgID` 传入 `handleUpdate`（仅 `/pin`、`/cancel` 消费）。优先级：**带链接参数 → 链接路径（回复忽略）；无参有回复 → 引用路径；无参无回复 → 用法提示**。
- 锚点反查按 `(bot_id, chat_id, message_id)` 三元组（坐标 bot 私有，接收 bot 的 ID 限定）；归属不匹配与坐标未命中**同形**返回 `store.ErrNotFound`（不泄露他人请求），回引导文案并记观测日志。
- 写入点五处（尽力而为，失败只记日志）：botapi `submitRefs` 占位（经 `access.RecordStatusMessage`）、worker 重试补发占位、worker 成功收尾投递消息（`meta.SentIDs`，含复用快路径）、binding `CopyToChannels` 频道副本组首（`kind=channel_copy`）、worker 失败通知。`INSERT OR IGNORE` 去重。
- `/cancel` 引用：仅在途（queued/processing）可取消（`access.CancelOwnByID`），终态各回固定友好文案。
- `/pin` 引用：在途 → 条件更新 `requests.pin=1`（`MarkRequestPinQueued`，affected=0 为竞态提示重试）；已完成 → `binding.PinExistingCopies` 按落库副本坐标逐目标 `PinChatMessage`（执行 bot 用副本 bot、回退主 bot；置顶 API 幂等）并置 pin=1 回写 pin_ok/pin_total；已全部置顶短路提示；无副本提示不可补置顶。

### 2. Tests Required

- `internal/botapi/reply_anchor_test.go`：/cancel 与 /pin 的状态矩阵（在途/已完成/失败/已取消/竞态/已置顶/无副本/他人请求/未命中/带参走链接路径/准入拦截）。
- `internal/store/sent_messages_test.go`：v21→v22 迁移、坐标反查与去重、`MarkRequestPinQueued` 条件矩阵。
- `internal/access/reply_anchor_test.go`：Resolve 归属折叠、CancelOwnByID 矩阵、RecordStatusMessage 回环。
- `internal/binding/reply_pin_test.go`：副本坐标落库、PinExistingCopies 成功/部分失败/无坐标/bot 回退/label 退化。
- `internal/queue/worker_anchor_test.go`：成功媒体行（含复用路径）、重试占位行、失败通知行。

## Scenario: /bind 私有邀请链接解析

### 1. Scope / Trigger

- 触发：修改 `/bind` 的目标解析、MTProto 邀请能力、频道绑定权限校验或 `joined_channels.joined_via`。
- `/bind` 与 Web 绑定接受 `t.me/+…`、`t.me/joinchat/…` 链接；裸 hash 与频道用户名冲突，不识别为邀请。

### 2. Contracts

- 先 `CheckInvite`，确认链接有效且指向频道/超级群组后才允许产生加入副作用；普通群组直接拒绝。
- 读取账号未加入时调用 `JoinInvite` 获取裸频道 ID，再经 `binding.BotChannelID` 转为 Bot API `-100…` ID，复用原有绑定查重、数量上限、权限与审计链路。
- 只有本次实际加入才写 `joined_channels.joined_via=bind_resolve`；已是成员时不得覆盖原始加入来源。
- 邀请链接只让读取账号加入，不会添加 Bot。Bot 命令路径的 `GetChat` 与权限硬校验使用接收 `/bind` 的同一台 Bot；Web 路径使用主 Bot。
- `TELEGRAM_RATE_LIMIT`、`PEER_FLOOD`、网络与 Telegram 服务端错误保留原分类；无效/过期/普通群组为 `CHANNEL_INVITE_INVALID`，读取账号离线或加入需审核为 `CHANNEL_INVITE_UNRESOLVED`。

### 3. Tests Required

- 成功解析、已加入短路、普通群组预检拒绝、读取账号离线、加入需审核、无效邀请、限流与 PEER_FLOOD 透传。
- 多 Bot 场景断言元数据查询使用接收命令的 Bot，而不是固定主 Bot。
- 加入留痕、总览来源统计、Bot 用法文案与 Web 表单说明同步覆盖。

## Scenario: /join 自动静音与归档的创建者保护

### 1. Scope / Trigger

- 修改 `internal/joinmgr` 的自动静音/归档或 `internal/mtproto` 的邀请预检时适用；防止客户端自建频道被误当成外部拉入。

### 2. Signatures

- `InviteInfo.Creator bool`：邀请响应中当前读取账号的创建者标记。
- `JoinedChannel.Creator bool`：实时更新、已加入频道列表携带的同一标记。
- 消费入口：`Submit`、`OnChannelsSeen`、`archiveExternalPass`、`ReconcilePendingJoins`。

### 3. Contracts

- 创建者频道/超级群组不执行自动静音/归档，与是否存在加入留痕、留痕为 `external` 无关。
- `inviteInfoFromChat` 从 `tg.Channel.Creator` 传递标记，不能因预检转换而丢失角色信息。
- 创建者仍出现在列表中；待批准加入对账仍完成留痕及备注回写，备注说明跳过动作，避免反复对账。
- 无记录或 `external` 仅表示未通过本系统加入，不证明账号是被其他人拉入。

### 4. Validation & Error Matrix

| 条件 | 行为 |
| --- | --- |
| `Creator=true` | 跳过自动静音/归档，保留账号自行设置的状态 |
| 普通外部成员、自动退出关闭 | 按静音/归档配置处理 |
| 自动退出开启 | 外部自动静音/归档让位于退出；创建者仍跳过退出 |
| `/join` 总开关关闭 | 拒绝提交，外部处理仍独立生效且保留创建者保护 |

### 5. Good / Base / Bad Cases

- Good：无留痕或已有 `external` 留痕的自建频道均跳过，普通外部频道照常处理。
- Base：从 Telegram 读取当前创建者标记，无新增配置或数据库迁移。
- Bad：只保护自动退出，或只依据加入来源判断是否可改变对话设置。

### 6. Tests Required

- `joinmgr`：列表刷新、实时更新、周期对账覆盖频道/超级群组 × 无留痕/已有 `external`，重复触发只处理普通外部成员；创建者继续显示在列表。
- `joinmgr`：已是成员提交跳过创建者；待批准加入对账跳过动作、回写准确备注并结束重复对账。
- `mtproto`：频道与超级群组的邀请预检保留 `Creator=true/false`。

### 7. Wrong vs Correct

- Wrong：无系统加入记录就直接执行自动静音/归档。
- Correct：先跳过 `Creator=true`，再根据来源与运行配置执行动作。
