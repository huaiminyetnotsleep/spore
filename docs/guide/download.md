# 下载功能说明

本文档说明 Spore 的下载功能原理、处理流程、当前网盘实现方式，以及未来扩展本地存储、OpenList/AList 和其他存储目的地的规划。

当前版本的下载入口是：

```text
/download <t.me 消息链接>
/download <目的地名称> <t.me 消息链接>
```

直接发送 `t.me` 消息链接仍然使用原有的 Telegram 重发逻辑，不会自动写入网盘。

也可以直接发送无参 `/download` 进入 ForceReply 输入引导：

- 回复 `目的地名称 + 链接`：立即按指定目的地提交；
- 只回复链接且当前没有启用目的地：返回功能不可用提示；
- 只回复链接且仅有一个启用目的地：自动使用该唯一目的地提交；
- 只回复链接且有多个启用目的地：显示动态目的地按钮，点击后提交；默认目的地排在最前并标记。

目的地选择阶段不会创建请求、扣额度或下载媒体；只有明确选择目的地后才进入正常提交链。选择按钮有效期为 5 分钟，支持查看用法和取消。

> 当前已实现的目的地是通过 rclone 连接的网盘，主要按 MEGA 进行配置和验证。本期不部署 OpenList/AList，不实现 WebDAV。

---

## 1. 下载功能的基本原理

### 1.1 下载不是 Telegram 转发

Spore 处理受保护消息时，不能简单地使用 Telegram 的 forward API。系统会使用已授权的 MTProto 用户会话读取源消息，然后将媒体重新下载，再交给目标目的地处理。

下载功能的核心链路是：

```text
Telegram 源消息
    │
    ▼
MTProto 用户会话读取消息
    │
    ▼
消息标准化：文本、图片、视频、文档、音频、相册
    │
    ▼
从 Telegram 下载媒体
    │
    ▼
目的地 Sink 接收媒体
    │
    ├── 当前：rclone → 网盘
    ├── 未来：LocalSink → VPS 本地目录
    ├── 未来：rclone → OpenList/AList → 多种网盘/NAS
    └── 未来：其他存储适配器
```

Spore 内部使用“目的地”概念，而不是把下载逻辑写死为某一家网盘。这样 `/download` 表示“下载”，目的地表示“下载到哪里”。

需要明确的是，`/download` 只改变结果去向，不放宽大小限制：源媒体仍受 `MAX_FILE_SIZE`
约束（默认 2000 MiB，也是当前配置上限），超限任务在下载开始前就会被拒绝。

### 1.2 媒体不会写入数据库

媒体内容和网盘凭据不写入 SQLite：

- 媒体字节通过内存管道或临时文件传输；
- 临时文件在任务完成、失败或取消后清理；
- 数据库只记录请求状态、目的地名称、远端路径、上传字节数和错误码；
- 网盘账号密码保存在 `data/cloud-drive.json`，文件权限为 `0600`；
- 管理端 API 返回敏感配置时使用 `********` 掩码。

这可以避免数据库备份中包含大量媒体数据或网盘凭据。SQLite 备份始终不包含
`data/cloud-drive.json`；需要备份或迁移目的地及真实凭据时，使用管理端独立的加密 ZIP
备份流程，不能把凭据并入 `.db` 备份。

### 1.3 普通链接和 `/download` 的区别

| 输入方式 | 处理方式 |
| --- | --- |
| 直接发送 `t.me/...` | 下载后重新发送到 Telegram，保持原有行为 |
| `/download t.me/...` | 下载后上传到默认目的地，不重新发送到 Telegram |
| `/download mega-2 t.me/...` | 下载后上传到指定目的地 |

两条路径都必须先通过相同的用户准入、限流、配额和并发校验。未授权用户不会因为使用 `/download` 而看到额外的云盘功能信息。

---

## 2. 下载处理流程

### 2.1 用户侧流程

```text
用户发送 /download 链接
    │
    ▼
确认是私聊消息
    │
    ▼
执行与普通链接相同的准入校验 + 用户级下载权限预检
    │
    ├── 未授权/待审批/停用 → 返回现有准入提示
    ├── 无用户级下载权限 → “管理员未开放你的云盘下载权限…”
    ├── 频率、额度或并发超限 → 返回现有拒绝提示
    └── 通过
          │
          ▼
检查下载总开关、目的地和 rclone 状态
          │
          ▼
解析 t.me 消息链接
          │
          ▼
创建请求记录并加入队列
          │
          ▼
回复“正在获取消息并下载到网盘”占位消息
```

直接命令只填写一个链接时使用配置中的 `default_destination`；填写两个参数时，第一个参数作为目的地名称。无参 `/download` 的 ForceReply 只回复链接时会实时读取启用目的地：唯一目的地自动提交，多个目的地等待用户点击选择。按钮生成后仍会在最终提交时重新校验用户权限、全局开关、rclone 与目的地状态。

#### 2.1.1 用户级下载权限

`/download` 受两层开关约束，**同时开启才可用**（AND 关系）：

| 层 | 存储位置 | 说明 |
| --- | --- | --- |
| 全局开关 | `data/cloud-drive.json` 的 `enabled` | 系统级熔断，对所有人（含 owner）生效；关闭时回「云盘下载功能未开启。」 |
| 用户级权限 | `users.cloud_download`（schema v11） | 三态：`0` 跟随角色默认（owner 允许、普通用户拒绝）、`1` 显式允许、`2` 显式拒绝；owner 也可被显式拒绝 |

- 管理端在用户详情页「云盘下载权限」按用户设置（`POST /api/v1/users/{id}/cloud-download`），即时生效并写审计；
- Bot 侧在准入预检（`UserDownloadStatus`）与 Submit 事务内（防预检后权限被改）各检查一次；
- 无权限用户收到专门文案「管理员未开放你的云盘下载权限，如有疑问请联系管理员。」；未授权用户仍回与裸链接相同的申请引导，不暴露功能存在；
- 裸链接（Telegram 重发）与管理端「存到网盘」补存都不受用户级权限限制——补存是管理员特权动作，只受全局开关约束。

### 2.2 Worker 流程

```text
从队列取出任务
    │
    ▼
同链接同目的地“已上传”检查（本地记录 + 云端核验，见 2.5）
    │
    ├── 已上传且文件仍在 → 跳过下载上传，直接成功并回复历史路径
    ├── 文件已被删除 → 继续下面的正常流程
    └── 核验失败（网络/限流）→ 任务失败 CLOUD_VERIFY_FAILED，提示稍后重试
    │
    ▼
获取 Telegram 源消息
    │
    ▼
识别单媒体或相册
    │
    ▼
下载媒体
    │
    ├── 小文件：流式管道
    ├── 中等文件：内存重排缓冲（受进程级内存预算约束，
    │   预算不足时自动降级临时文件路径）
    └── 大文件：临时文件
    │
    ▼
构造远端路径
    │
    ▼
rclone 上传到目的地
    │
    ▼
记录 cloud_uploads 上传结果
    │
    ├── 成功 → 更新请求状态并发送完成提示
    ├── 失败 → 清理远端残件并记录失败原因
    └── 取消 → 终止子进程、清理临时资源并标记 cancelled
```

### 2.3 混合媒体和配文的保存方式

Telegram 中“文字 + 图片 + 视频”通常表现为一个相册消息组。Spore 会将相册中的每个媒体作为独立文件保存到同一个目录：

```text
spore/
└── channel-name/
    └── 2026-09-10/
        └── 12345/
            ├── 01_photo.jpg
            ├── 02_video.mp4
            └── caption.txt
```

保存规则：

- 多张媒体：使用消息 ID 目录；媒体按组内顺序添加 `01_`、`02_` 等前缀；
- 单媒体但有配文：同样使用消息 ID 目录，并写入 `caption.txt`；
- 单媒体且无配文：直接放在日期目录下；
- 图片没有文件名时生成 `photo-{message_id}-{index}.jpg`；
- 文件名会清理路径分隔符和控制字符；
- 同目录冲突时追加 `-2`、`-3` 等后缀；
- 纯文本消息没有可下载媒体，`/download` 会返回明确提示。

### 2.4 进度、超时与取消

下载和上传复用 Spore 现有的任务队列、进度注册表和取消机制：

- 任务进入队列后即可通过请求记录观察状态；
- `/cancel <原消息链接>` 可以取消用户自己的在途任务；
- 管理端可以取消对应请求；
- 排队中的任务取消后不会开始抓取；
- 上传中的任务会取消 rclone 子进程；
- 取消或失败后会尽力删除远端残件；
- 进程退出时，任务会按照现有中断语义收尾。

### 2.5 同链接重复上传的跳过

对同一条链接重复执行 `/download`（同一目的地）时，Spore 不会盲目重新下载上传：

1. 先查本地记录：同一用户、同一链接、同一目的地是否已有成功的云盘请求；
2. 命中后再请求云盘核验（`rclone lsjson` 只读列举，按目录分组）这些文件是否仍存在——本地“成功”只代表上传当时 rclone 正常退出，文件之后可能已在网盘被删除；
3. 三种结果：
   - **全部存在**：跳过下载和上传，请求直接标记为成功，回复历史路径（相册折叠为目录一行）并附“该链接此前已上传，本次未重复下载”说明；
   - **有文件确定不存在**（远端已删除；上次“半成功”的任务请求状态是 failed，本来就不会命中本地查询）：重新走完整下载上传流程；
   - **核验本身失败**（网络错误、MEGA 限流等，错误码 `CLOUD_VERIFY_FAILED`）：任务失败并提示稍后重试，既不误报成功，也不盲目重传大文件。

补充说明：

- 匹配范围是“同用户 + 同链接 + 同目的地”。换一个目的地提交会正常上传，因为文件并不在新目的地；
- 同链接同目的地已有排队或上传中的任务时，重复提交会被拒绝（`DUPLICATE_LINK`），避免同路径并发上传在 MEGA 产生同名重复文件；普通链接提取的“成功去重窗口”不适用于云盘提交；
- 上一次同用户、同链接、同目的地的顶层云盘请求为 `failed` 时，重新发送 `/download` 会把这条最近记录原地重置后继续执行，不再新增一条重复请求记录；该次提交仍计入当日额度，尝试计数从 1 重新开始；
- Web 管理端对同一原请求、同一目的地再次执行「存到网盘」时，最近的补存子行若为 `failed` 也会原地重置；Bot 提交与 Web 补存的记录血缘彼此隔离，不会互相复用；
- 已有成功记录并命中远端跳过时，本次任务仍照常创建并计入当日额度，请求记录完整可查；
- 核验只比对路径存在性，不比较文件内容。

### 2.6 完成提示的格式

上传成功（含跳过重复上传）后，机器人回复：

```text
已上传到网盘 mega-1：
spore/example/2026-09-10/12345

原链接：https://t.me/example/123
网盘官网：https://mega.nz/
```

- 相册或带配文的成夹布局只显示目录一行，不逐文件罗列；
- “原链接”是该次请求的规范 t.me 消息链接；
- “网盘官网”由目的地类型（如 `mega`）内置映射得到，未知后端类型省略该行；
- 命中“已上传跳过”时会额外附一行“该链接此前已上传，本次未重复下载”。

---

## 3. 当前目的地实现：rclone

### 3.1 rclone 是什么

[rclone](https://rclone.org/) 是一个命令行云存储工具，可以用统一命令访问多种云存储后端。不同网盘的账号参数由 rclone 后端定义，Spore 只负责：

1. 选择目的地；
2. 组装远端路径；
3. 将媒体流交给 rclone；
4. 读取进度和错误；
5. 在取消或失败时终止并清理。

官方文档：

- [rclone Documentation](https://rclone.org/docs/)
- [Basic syntax](https://rclone.org/docs/#basic-syntax)
- [Remote paths](https://rclone.org/docs/#syntax-of-remote-paths)
- [Configuration](https://rclone.org/docs/#configure)
- [Connection strings](https://rclone.org/docs/#connection-strings)
- [rclone rcat](https://rclone.org/commands/rclone_rcat/)
- [rclone lsjson](https://rclone.org/commands/rclone_lsjson/)
- [rclone lsd](https://rclone.org/commands/rclone_lsd/)
- [Logging and JSON logs](https://rclone.org/docs/#use-json-log)

### 3.2 rclone 的基本使用方式

交互式配置 remote：

```bash
rclone config
```

查看远端目录：

```bash
rclone lsd <remote>:
```

从标准输入上传（Spore 的实际用法）：

```bash
cat ./video.mp4 | rclone rcat <remote>:spore/example/2026-09-10/video.mp4 --size 123456
```

查看传输进度：

```bash
cat ./video.mp4 | rclone rcat <remote>:path/video.mp4 --size 123456 --progress
```

输出 JSON Lines 日志，供程序解析：

```bash
cat ./video.mp4 | rclone rcat <remote>:path/video.mp4 --size 123456 \
  --use-json-log --stats 1s --stats-one-line -v
```

这些命令的完整参数、版本差异和后端限制以 [rclone 官方文档](https://rclone.org/docs/) 为准。本文只记录 Spore 实际依赖的用法，不复制整套 rclone 文档。

### 3.3 Spore 如何调用 rclone

当前实现把所有媒体统一交给 `rclone rcat` 从标准输入上传：

- 无论流式管道、内存缓冲还是临时文件，媒体都以 `Reader` 流交给 Sink，并传入 Telegram 报告的精确文件大小；
- 大文件的 Reader 由 fileGate 提供：rclone 消费已落盘的前缀时，Telegram 下载仍在并行写入后续分片，不会等待整个文件下载完成，也不会把大文件整体缓冲进内存；
- `copyto`（本地文件路径上传）只是上传 Sink 抽象保留的分支，当前云盘任务路径不使用；
- 使用 `--use-json-log`、`--stats` 读取上传进度；
- 使用 `exec.CommandContext` 绑定请求上下文；
- 上下文取消时终止 rclone；
- 非成功退出后尽力执行 `rclone deletefile` 清理残件。

rclone 的路径格式是：

```text
<remote>:<path>
```

在 Spore 中，目的地名称就是 `<remote>` 的逻辑名称，路径由频道、日期、消息 ID 和文件名组成。

### 3.4 rclone 环境变量

Spore 的目的地配置会映射为 rclone 的环境变量，避免把每个网盘后端硬编码到 Spore：

```text
RCLONE_CONFIG_<DESTINATION>_TYPE=mega
RCLONE_CONFIG_<DESTINATION>_USER=...
RCLONE_CONFIG_<DESTINATION>_PASS=...
RCLONE_CONFIG_<DESTINATION>_2FA=...
```

目的地名称只允许小写字母、数字和连字符。remote 名称中的连字符会原样保留在 `RCLONE_CONFIG_<REMOTE>_*` 环境变量中；只有 rclone 配置项名称中的连字符才转换为下划线。

rclone 官方环境变量、配置文件和命令参数可能随版本变化。生产环境使用项目镜像内置的固定版本；自定义部署可以通过 `RCLONE_BIN` 指定 rclone 路径。

---

## 4. 当前网盘实现：MEGA

### 4.1 MEGA 官方入口

- [MEGA 官方主页](https://mega.io/)
- [MEGA 注册](https://mega.nz/register)
- [MEGA 登录](https://mega.nz/login)
- [MEGA 存储方案](https://mega.io/storage)
- [MEGA 安全与隐私](https://mega.io/security)
- [MEGA 帮助中心](https://help.mega.io/)
- [rclone MEGA backend 文档](https://rclone.org/mega/)
- [MEGA Transfer Quota 官方说明](https://help.mega.io/plans-storage/space-storage/transfer-quota)

### 4.2 MEGA 与本项目相关的参数

rclone 的 MEGA 后端类型是 `mega`，常用参数为：

| 参数 | 说明 |
| --- | --- |
| `user` | MEGA 登录账号，通常为邮箱 |
| `pass` | MEGA 密码；管理端可填写原始密码并由服务端自动混淆，手动编辑配置文件时使用 `rclone obscure` 输出 |
| `2fa` | 开启双因素认证时使用的参数 |
| `use_https` | 需要时强制使用 HTTPS |
| `hard_delete` | 需要绕过回收站永久删除时使用；默认删除通常进入回收站 |

MEGA 密码混淆：

```bash
rclone obscure '你的 MEGA 密码'
```

手动编辑 Spore 配置文件时，将命令输出结果填入 `options.pass`；管理端页面可直接填写原始密码。不要把明文密码写入配置文件、日志或聊天消息。

### 4.3 MEGA 使用注意事项

根据 [rclone MEGA backend 官方文档](https://rclone.org/mega/)：

- MEGA 允许同一路径下出现同名文件；Spore 因此使用消息 ID、序号和冲突后缀主动保证路径可追踪；
- MEGA 后端不提供完整的修改时间和哈希支持；
- 特殊字符和无效 UTF-8 文件名可能被替换；
- 管理类命令不要高频连续执行，以免触发服务端限制；
- Spore 不会在每次上传前自动 Ping，连通性测试只通过管理端按需触发；
- 传输配额和存储空间是不同概念。MEGA 官方说明中，下载/在线播放主要消耗 transfer quota，而上传主要占用存储空间；本项目主要执行上传。

MEGA 的具体免费额度、服务条款和限制可能调整，容量和政策应以 [MEGA 官方方案页面](https://mega.io/storage) 与帮助中心为准。

---

## 5. 在 Spore 中配置 MEGA

### 5.1 准备 rclone 和 MEGA 账号

生产镜像已经包含固定版本的 rclone。若本地开发：

```bash
brew install rclone
rclone version
```

然后：

1. 在 [MEGA 官方注册页](https://mega.nz/register) 创建账号；
2. 通过浏览器登录并完成账号初始化；
3. 如果启用了双因素认证，准备 `2fa` 配置项；
4. 执行 `rclone obscure 'MEGA 密码'`；
5. 保存命令输出的混淆值，不保存明文密码。

### 5.2 配置文件格式

Spore 使用 `data/cloud-drive.json` 保存多目的地配置。一个 MEGA 账号对应一个目的地；多个 MEGA 账号就是多个目的地对象：

```json
{
  "enabled": true,
  "default_destination": "mega-1",
  "destinations": [
    {
      "name": "mega-1",
      "enabled": true,
      "type": "mega",
      "path_prefix": "spore",
      "options": {
        "user": "your-account@example.invalid",
        "pass": "<rclone obscure 输出>",
        "2fa": "<仅在启用双因素认证时填写>"
      }
    }
  ]
}
```

注意：

- `enabled` 是下载功能全局开关，默认应为 `false`；
- `default_destination` 必须指向已启用的目的地；
- `name` 必须唯一，并符合小写名称规则；
- `type` 对当前 MEGA 配置使用 `mega`；
- `options.pass` 应填写 `rclone obscure` 的结果，而不是明文密码；
- 配置文件权限应为 `0600`；
- 不要将该文件提交到 Git 或发送到外部服务。

### 5.3 管理端配置

#### Web 配置页字段说明

管理端「云盘下载」页面使用通用目的地表单，不为每一家网盘制作独立页面模板。所有目的地都使用相同的公共字段；不同网盘所需的账号、密钥和 endpoint 等差异，填写在 `options` 参数键值对中。

| Web 字段 | 对应配置 | 说明 |
| --- | --- | --- |
| 云盘下载总开关 | `enabled` | 全局开关。关闭后 `/download` 不会执行网盘任务，普通链接不受影响。默认关闭。 |
| 默认目的地 | `default_destination` | `/download <链接>` 未指定目的地时使用的 `name`。必须指向一个已启用的目的地。 |
| 目的地名称 | `destinations[].name` | 当前目的地的唯一标识，也用于 `/download <目的地名> <链接>`。只能使用小写字母、数字和连字符，不能使用下划线。 |
| 类型 | `destinations[].type` | rclone 后端类型，例如 `mega`、`s3`。类型决定 `options` 中应填写哪些参数。 |
| 路径前缀 | `destinations[].path_prefix` | 该目的地下的远端根目录，例如 `spore`；留空表示远端根目录。 |
| 启用 | `destinations[].enabled` | 单个目的地开关。可以先保存一个未启用的备用账号，但默认目的地必须启用才能开启全局功能。 |
| 参数键 | `destinations[].options` 的 key | 按所选后端的 rclone 文档填写，例如 MEGA 的 `user`、`pass`、`2fa`。 |
| 参数值 | `destinations[].options` 的 value | 对应参数的值。密码、Token、Secret、Key 等敏感值在页面上掩码显示；在管理端填写 MEGA 原始密码时，服务端会自动执行 `rclone obscure`；手动编辑配置文件时仍需填写混淆输出。 |
| 测试 | 不落配置 | 对目的地执行只读连通性测试；支持对已保存目的地测试，也可在弹窗保存前对当前草稿参数自测；测试不会修改配置。 |

不同网盘的参数不能混用。例如：

| 目的地类型 | 常用参数 | 说明 |
| --- | --- | --- |
| `mega` | `user`、`pass`、`2fa` | 管理端的 `pass` 可直接填写原始密码，服务端保存时自动执行 `rclone obscure`；手动编辑文件时填写混淆输出。`2fa` 仅在账号启用双因素认证时填写。 |
| `s3` | `provider`、`access_key_id`、`secret_access_key`、`endpoint` | Cloudflare R2、Backblaze B2 和其他 S3 兼容服务应以各自 rclone 后端文档为准。 |
| `drive` | OAuth token 等 rclone 参数 | Google Drive 需要按 rclone 官方授权流程生成 token；本项目不实现独立 OAuth 回调页。 |
| `onedrive` | OAuth token、drive ID 等 rclone 参数 | OneDrive 参数取决于账号和租户配置，应以 rclone 官方后端文档为准。 |

类型列表只是常用类型提示，并不是 Spore 的固定网盘模板。新增网盘时，原则上只需要填写该后端官方文档要求的 `type` 和 `options`。需要说明的是：除 MEGA 外，其他 `type`（如 `s3`、`drive`、`onedrive`、`webdav`）在结构上会以同样方式把 `options` 传给 rclone，但本项目只在 MEGA 上完成过配置与真机验证；启用其他后端前请先用「测试」自行确认连通性，其认证与限额差异以 rclone 官方后端文档为准。

推荐通过管理端的「云盘下载」页面配置，而不是手动编辑文件：

1. 打开「云盘下载」页面；
2. 打开全局开关前，在目的地表格右上角点击「添加目的地」打开弹窗；
3. 填写目的地名称、类型 `mega`、路径前缀；
4. 在 options 表格中填写 `user`、`pass`（可直接填写包含特殊字符的原始密码），有需要再填写 `2fa`；
5. 在弹窗底部点击「测试连通性」进行保存前自测，确认连通成功；
6. 点击「保存生效」，目的地即时创建并保存至服务端；
7. 在表格中将目的地设为默认（点击第一列单选 Radio，整行会高亮显示）并开启启用 Switch；
8. 最后打开全局开关。

管理端会对结构进行校验。保存只做结构与参数校验（含 MEGA 原始密码的自动混淆），不会强制连接网盘；连通性测试只通过「测试」按钮按需触发。读取配置时，密码、2FA、Token、Secret、Key 等敏感值显示为 `********`；如果不更换凭据，编辑保存或连通测试时可以保留掩码，服务端会继续使用原值。管理端新提交的 MEGA 原始密码会在落盘前自动转换为 rclone 混淆值；直接编辑 `data/cloud-drive.json` 时仍需手动填写 `rclone obscure` 输出。

### 5.4 使用命令

使用默认目的地：

```text
/download https://t.me/example/123
```

指定目的地：

```text
/download mega-1 https://t.me/example/123
```

完成后，机器人会回复目的地名称、远端路径、原链接与网盘官网（格式见 2.6）。任务执行期间可以取消：

```text
/cancel https://t.me/example/123
```

如果请求列表中有历史成功、失败或取消记录，也可以在管理端对该请求点击「存到网盘」。补存会新建一条独立的云盘任务进入队列，它与 `/download` 走同一条执行路径，因此同样先经过 2.5 的同链接“已上传”检查：同用户、同链接、同目的地已有成功记录且远端核验通过时，会直接复用历史结果而不重新下载上传；只有远端文件确定缺失时才重新抓取源消息走完整流程。

### 5.5 配置备份与恢复

管理端「云盘下载」页面提供独立的配置备份与恢复区块，用于迁移或保护
`data/cloud-drive.json` 中的完整目的地配置和真实凭据。它与 SQLite 数据库备份互不包含：

| 备份类型 | 包含 | 不包含 |
| --- | --- | --- |
| 数据库 `.db` 备份 | 用户、请求、设置、审计等 SQLite 业务数据 | 云盘配置、网盘凭据、Session、`.env`、媒体 |
| 云盘配置加密 ZIP | `cloud-drive.json` 的完整配置与凭据 | SQLite 数据库、Session、`.env`、媒体 |

云盘配置包的格式版本为 `1`，ZIP 固定只有两个文件：

```text
manifest.json
cloud-drive.json.enc
```

- `manifest.json` 保存 Argon2id 与 AES-256-GCM 参数、随机 salt/nonce、加密载荷 SHA-256 与
  大小、创建时间、应用版本和目的地名称摘要；不保存 `options` 值；
- `cloud-drive.json.enc` 保存经 Argon2id 派生 256 位密钥、再由 AES-256-GCM 加密的完整配置；
- 备份密码只存在于当前请求内存，不记录、不持久化，也不进入日志或审计；
- 备份密码遗忘后无法恢复，管理员必须把 ZIP 与密码管理器中的密码分开保管。

恢复使用两阶段流程。上传 ZIP 和密码时，服务端只验证格式、hash、AEAD 认证和完整配置，生成
待确认候选，**不会修改当前配置**。管理员核对格式版本、创建时间、包 hash 和目的地名称后再
二次确认；确认时先保存最近一份当前配置用于 rollback，再整体替换配置并在线立即生效。恢复
不是按目的地合并，候选中不存在的当前目的地会消失。

配置 Manager 以快照方式切换：已经开始的下载/上传任务继续使用它们启动时取得的旧目的地，
确认恢复或回滚之后创建的新任务使用新快照。无需为了应用云盘配置而重启服务。这与数据库
`.db` 备份的生效方式不同：数据库导入确认后要等下一次重启才会应用（见
[运维手册 §4](../ops/operations.md#_4-备份与恢复)）。上传了错误候选
但尚未确认时可以直接取消；确认后需要撤销时使用唯一的 `rollback-latest`，它只保留最近一份，
不是多版本历史。

Docker 部署中，当前配置、候选和 rollback 文件都位于 `/app/data`，由宿主机
`./data:/app/data` bind mount 持久化并以 `0600` 保存。正常拉取镜像和重建容器不会清除这些
文件；删除或错误切换 `data/` 挂载会造成丢失。详细操作步骤、密码保管与故障排查见
[运维手册 §7.5](../ops/operations.md#_7-5-云盘配置备份与恢复)，端点契约见
[API 参考 §5b](../reference/api.md#_5b-云盘配置备份与恢复)。

无论导出、上传、确认、取消或回滚，日志与审计都不得包含备份密码或目的地 `options` 值；只
允许记录格式版本、包 hash/大小、目的地名称/数量、动作与结果。

---

## 6. 管理端和请求记录

### 6.1 请求状态

每个云盘任务仍然使用 Spore 的请求生命周期：

```text
queued → processing → succeeded
                     ├→ failed
                     └→ cancelled
```

云盘任务的 `delivery_mode` 为 `cloud`。管理端请求列表支持按该字段筛选，并显示云盘状态。

失败请求有两种继续方式：管理员在详情页点“重试”会复用原行并累计尝试次数（不重复扣额度）；用户重新发送同一链接到同一目的地时也复用最近的失败行，但按一次新的提交重新扣额度，并把尝试次数重置为 1。两种方式都不会在列表中留下额外的失败请求行。

### 6.2 上传记录

每个媒体上传会记录：

- 原请求 ID；
- 目的地名称；
- 远端路径；
- 文件名；
- 上传状态；
- 已上传字节数；
- 错误码；
- 创建和完成时间。

这些记录用于排查问题，不保存媒体内容或网盘密码。

### 6.3 单条和批量补存

请求列表支持：

- 单条请求「存到网盘」；
- 勾选多条请求后批量「存到网盘」；
- 从默认目的地或指定目的地执行；
- 每条补存任务独立进入队列；
- 队列满时记录 `QUEUE_FULL`，之后可以重试；
- 源消息已经删除时，补存任务会失败并保留明确错误。

首次补存不会覆盖原请求记录，而是创建一条新的 `cloud` 请求并通过 `parent_request_id` 关联原请求。同一原请求、同一目的地的最近补存子行如果失败，再次点击「存到网盘」会原地重置该失败子行并继续，不再重复创建补存记录；最近一次补存已经成功或取消时仍会新建一行记录本次动作。补存任务与 `/download` 走同一条云盘任务路径，因此同样适用 2.5 的同链接“已上传”检查：远端文件仍在时直接复用历史结果，确定缺失才重新下载上传。

---

## 7. 故障排查

本节只覆盖云盘功能本身的配置语义与错误含义；上线后的例行诊断、日志查看、容量检查与
备份恢复操作见 [运维手册 §7](../ops/operations.md#_7-云盘下载-download)，通用错误与
历史踩坑记录见 [troubleshooting.md](../ops/troubleshooting.md)。

### rclone 不可用

检查：

```bash
rclone version
which rclone
```

如果使用自定义路径，检查 `.env`：

```text
RCLONE_BIN=/path/to/rclone
```

然后重启 Spore。管理端「云盘下载」页面中的 rclone 状态也会显示当前探测结果。

云盘开启但 rclone 不可用时，服务会产生 `cloud.disabled` 事件（由服务在启动时探测、
之后每 10 分钟复查产生，恢复后自动解除）；单个 `/download` 或补存请求只会收到
rclone 不可用的受控拒绝，不会重复触发该事件。

### MEGA 认证失败

重点检查：

- `options.user` 是否为正确的账号；
- `options.pass` 是否是 `rclone obscure` 输出，而不是明文密码；
- 开启双因素认证时是否填写了 `options.2fa`；
- 是否可以先在浏览器登录 MEGA；
- 是否因连续管理类操作触发了 MEGA 限制。

### 上传失败

按以下顺序检查：

1. 查看请求详情中的「云盘上传」记录；
2. 查看事件页中的 `cloud.upload_failed`、`cloud.config_invalid` 或 `cloud.disabled`；
3. 在管理端按需点击目的地「测试」；
4. 确认 MEGA 存储空间是否足够；
5. 确认远端路径和文件名不包含异常字符；
6. 对失败请求使用「重试」或重新点击「存到网盘」。

### 已上传核验失败（CLOUD_VERIFY_FAILED）

重复 `/download` 同一链接时，Spore 会先去云盘核验历史文件是否仍在（见 2.5）。核验调用本身因网络错误或 MEGA 限流失败时，任务会以 `CLOUD_VERIFY_FAILED` 失败并提示稍后重试。处理方式：

1. 稍等几分钟后重新发送同样的 `/download` 命令；
2. 若持续失败，检查管理端目的地「测试」是否通过、VPS 到网盘的网络；
3. 短时间内大量重复提交同一链接可能触发网盘限流，避免频繁重试。

不要把完整 rclone 命令、密码或配置 options 值复制到公开日志中。

---

## 8. 后续扩展 TODO

- [ ] **本地目的地**：增加 `type: local`，支持保存到 VPS 指定目录；明确磁盘配额、权限、生命周期和管理端文件访问策略。
- [ ] **OpenList/AList 目的地**：作为独立服务部署，由 OpenList/AList 自己挂载 MEGA、其他网盘、NAS 等；Spore 未来通过一个统一适配入口接入，不在本期部署。
- [ ] **WebDAV 目的地**：在 OpenList/AList 方案稳定后再评估，不纳入当前版本。
- [ ] **更多 rclone 后端**：在通用目的地模型下验证 S3、Cloudflare R2、Backblaze B2、OneDrive、Google Drive 等后端的认证和限额差异。
- [ ] **多目的地策略**：支持容量不足后的手动切换、自动故障转移和多目的地冗余；当前只支持默认目的地或命令显式指定。
- [ ] **分享链接**：在确认访问控制、内容安全和有效期策略后，再考虑生成临时分享链接。
- [ ] **断点续传与重试策略**：结合不同后端能力，完善大文件失败后的续传和退避重试。
- [ ] **容量与传输统计**：在管理端增加各目的地容量、成功率、失败率和传输量统计。
- [ ] **目的地健康检查**：在不触发网盘服务端频控的前提下增加低频健康检查。

---

## 9. 官方参考链接

- [rclone 官方文档](https://rclone.org/docs/)
- [rclone MEGA backend](https://rclone.org/mega/)
- [rclone rcat](https://rclone.org/commands/rclone_rcat/)
- [rclone lsjson](https://rclone.org/commands/rclone_lsjson/)
- [rclone lsd](https://rclone.org/commands/rclone_lsd/)
- [MEGA 官方主页](https://mega.io/)
- [MEGA 官方存储方案](https://mega.io/storage)
- [MEGA 官方帮助中心](https://help.mega.io/)
- [MEGA Transfer Quota](https://help.mega.io/plans-storage/space-storage/transfer-quota)
