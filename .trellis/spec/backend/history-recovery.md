# 已处理历史恢复契约

## 1. Scope / Trigger

适用于 internal/recovery、store recovery DAO、queue direct-target 适配器、cmd/bot 生命周期、Web recovery API 及 frontend recovery 页面。恢复范围是系统已登记历史，不是 Telegram 全量扫描，不新增云盘功能，不改普通投递副作用。

## 2. Signatures

```go
recovery.New(ctx context.Context, st *store.Store, bots *botpool.Pool) (*recovery.Service, error)
Preview(context.Context, recovery.Input) (recovery.Preview, error)
Create(context.Context, recovery.Input) (store.RecoveryJob, error)
Control(context.Context, int64, string) (store.RecoveryJob, error)
List(context.Context, int, int) ([]store.RecoveryJob, int, error)
Get(context.Context, int64) (store.RecoveryJob, error)
Items(context.Context, int64, string, int, int) ([]store.RecoveryItem, int, error)
Run(context.Context, recovery.Runtime) error
```

表为 recovery_jobs / recovery_items；追加版本化迁移，不编辑历史迁移。创建与控制使用短事务，成功审计 recovery.create/pause/resume/cancel/retry 与状态变化原子提交；网络操作不能在事务内。

## 3. Contracts

- POST /api/v1/recovery/preview 与 /jobs：`{filter:{channel_key,user_id,since,until},target,bot_id}`。时间为Unix毫秒，since包含、until不包含；0/空表示该条件不限。
- GET /jobs、/jobs/{id}、/jobs/{id}/items；POST /jobs/{id}/{pause,resume,cancel,retry}。全部管理员认证，POST CSRF；单对象原始JSON，分页标准ListEnvelope，空items=[]。
- bot_id=0只在创建时选择主Bot并持久化实际ID。显式ID用精确MemberByID，不能SenderFor静默fallback。
- 预览只GetChat/GetChatMember，不创建测试复制/删除消息；缓存readable不保证逐消息存在。
- Job状态 running/paused/completed/cancelled；Item状态 pending/processing/succeeded/failed/unrecoverable/uncertain/skipped。method cache/source/空。正文、caption、Entity内容、媒体本体、Token、AccessHash、session、可重放URL不入库。
- 创建固定候选集与目标。候选按已知来源别名、相册成员、完整缓存坐标去重；无归属的watch DumpIDs不能猜当前缓存归属。确定已有同目标同Bot成功输出时跳过；既有uncertain/processing则拒绝新建，不能用新任务绕过盲重试保护。防重核对必须按来源+已知成员交集而不是完整identity相等：历史后补相册成员会改变identity，交集命中uncertain即拒绝、命中succeeded不得重发该成员。完整缓存必须证明来源成员覆盖，输出数（可能含分卷）不能证明来源完整性；无法证明覆盖的多输出缓存弃用，按原来源仅发送已确认成员。
- 单运行器缓存优先，只在确定没有发送时fallback。源提取只能走当前读取账号权限，不自动加入第三方来源。
- Run附着MTProto生命周期，取消并join后才能销毁客户端。进程启动把running转paused，processing转uncertain；数据库恢复不能自动重发。
- 恢复仅独立保存输出，不覆写dump_entries、不自动发布普通复用索引、不自动切配置、不投递私聊或其他绑定。
- UI编辑预览输入后必须废弃旧预览，提交冻结的预览快照；mutation期间禁用操作。retry条件paused/completed且failed+unrecoverable>0，uncertain不可重试。详情为独立路由/recovery/jobs/:id（routeMeta继承recovery菜单高亮，PageScaffold H1=恢复任务详情）；轮询只在存在running任务（列表或已打开详情）时5秒执行，空闲停止。来源/目标/用户输入用AutoComplete可下拉可输入：枚举来自生效监听源（含kind类型）、active绑定、提取历史key与enabled用户（仅作建议，枚举查询失败不影响手输）；无缓存项走普通下载上传管线直发恢复目标。
- 防重与不确定阻断按目标聊天匹配，不区分执行Bot（sent_ids坐标属于目标聊天本身）：换机器人对同一目标新建任务时旧Bot已succeeded的成员自动跳过，旧uncertain/processing交集仍然拒绝创建。

## 4. Validation & Error Matrix

| 条件 | 结果 |
| --- | --- |
| 未认证 / CSRF无效 | 401 / 403，服务不调用 |
| 空target、负bot/user/time、until非零且<=since、非法分页/status | 400 |
| 不存在任务 | 404 NOT_FOUND |
| 空范围、过大范围、状态冲突 | 409 STORE_CONSTRAINT |
| 目标非频道/非论坛超级群组或无发送权限 | 409 SEND_TARGET_INVALID |
| Bot不可用/禁用 | 受控503，不替换Bot |
| 部分输出、数量异常、网络超时、途中中断 | uncertain，保留已知IDs，禁止自动retry/fallback |
| 全部副本/来源无法读取 | unrecoverable，可补权限后显式retry |

## 5. Good / Base / Bad Cases

- Good：原来源已失效、私有缓存可读，完整复制相册，保存全部输出，原索引不变。
- Base：无缓存，读取账号有源权限，direct-target适配仅发送选定目标，不调用普通Process。
- Bad：CopyMessages返回部分ID后报错，保留部分ID并uncertain；不能再提取整组重发。

## 6. Tests Required

- 临时SQLite验证候选合并、用户/时间/来源过滤、范围上限、同目标成功去重、原表不变、审计原子提交、restart reconcile。
- fake Telegram验证只读预检、精确Bot、完整/部分相册、确定无发送fallback、timeout ambiguous、目标失权暂停、暂停/取消/退出join、来源权限失败。
- Web验证每条POST认证/CSRF、非法输入不调用服务、分页及空数组、受控错误不泄露原始细节。
- frontend验证预览失效与快照提交、bot可用性、控件mutation门控、failed/unrecoverable可重试但uncertain不可，400px假API端到端。
- 不启动真实Telegram、不读取生产凭据、不对外发测试消息。

## 7. Wrong vs Correct

Wrong：`ids, err := CopyMessages(...); if err != nil { fetchAndSendSource() }`。

Correct：先持久化processing；完整数量且无错误才成功。任何已知部分输出或可能发送成功的错误都保存IDs并uncertain；只有可证明未发送的错误才尝试下一副本或原来源。SQLite与Telegram不能原子提交，不能承诺exactly-once。
