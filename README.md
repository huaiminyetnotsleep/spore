# Spore

<div align="left">

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)
![Docs](https://img.shields.io/badge/Docs-VitePress-646CFF?logo=vitepress&logoColor=white)
![Image](https://img.shields.io/badge/image-ghcr.io%2Fhuaiminyetnotsleep%2Fspore-0969DA?logo=github)
![License](https://img.shields.io/badge/License-PolyForm_NC_1.0.0-D13030)

</div>

把频道消息链接发给 Bot，收到的就是一条**可再次转发的全新消息**。

Spore 是一个面向**合法授权场景**的 Telegram 自托管消息重构工具：频道开启「限制保存内容」后，消息无法转发、无法复制；本工具通过 **MTProto 用户账号**读取该账号有权访问的源消息，重新构造为一条全新消息发回——不保留 Forward Header，可正常再次转发。

> Bot API 负责「和用户聊天」，MTProto 负责「以用户身份访问 Telegram」，业务层负责「把源消息转换成新的消息」。

> [!IMPORTANT]
> **项目性质与合规声明**：本项目由个人出于学习、研究及自用目的独立维护，当前不提供商业服务，也不隶属于、代表或获得 Telegram 官方认可。请仅处理你本人拥有，或已取得内容权利人、频道管理者及相关主体明确授权的内容。账号能够访问某条消息，**不代表**你当然拥有复制、下载、改编、转发或公开传播该内容的权利。使用前请阅读下方的[使用边界与合规要求](#compliance)。

> [!NOTE]
> 📖 完整文档请[在线阅读](https://huaiminyetnotsleep.github.io/spore/)。

## ✨ 功能特性

- 📨 **受保护消息提取**：文本（保留实体格式）、图片、视频、文件、音频、语音条与相册；超过 Bot API 50MB 上限的大文件经 Bot 身份 MTProto 直传，上限 2000MB。
- 🤖 **Bot 交互**：私聊发链接即可使用；`/start` 申请、额度查询、进度、取消、频道绑定与加入等命令齐备，配额与重试边界明确。
- ☁️ **云盘下载**：`/download` 把提取的媒体直接上传到网盘（经 rclone），不再重发回 Telegram；支持多目的地、排队与取消。
- ♻️ **缓存频道复用**：成功投递后同步写无脚注干净副本到自有缓存频道，同链接再次提交整条复制秒回——不限媒体大小、相册保组。
- 🖥️ **Web 管理端**：内置 SPA 管理面板——用户审批与管理、请求记录、频道统计、运行设置、云盘配置、资源监控与审计。
- 🔔 **通知设置**：管理端在同一页面配置 Telegram Bot、通用 JSON/飞书/钉钉/Discord Webhook，以及事件通知规则和静音计划；凭据加密入库并随数据库备份。活动通知（管理后台登录、新用户申请、频道加入申请）默认开启，逐次推送关键信息（时间/IP/操作系统/浏览器等）。
- 🔐 **频道加入**：`/join` 提交邀请链接让读取账号加入目标频道，管理端审批后生效，支持静音、归档与数量上限。

## ⚙️ 工作原理

```text
你 ──链接──▶ Bot API ──入队──▶ gotd/td 用户账号会话 ──读取源频道、下载媒体
                                     │
                                     ├─ ≤ Bot API 上限（官方服务器 50MB）
                                     │        │
                                     │        ▼
                                     │   Bot API 上传（multipart）
                                     │
                                     └─ 超过 Bot API 上限（至 2000MB）
                                              │
                                              ▼
                              gotd/td Bot 身份会话 ──MTProto 上传直传（messages.sendMedia）
```

- **Bot API** 负责「和你聊天」（收链接、发占位提示、≤50MB 上传）；**MTProto 用户账号**负责「以用户身份访问 Telegram」（读源频道、下载媒体）；**MTProto Bot 身份会话**负责大文件直传——三个通道各司其职。
- 私有频道 `t.me/c/...` 要求读取账号能访问；未加入时可用 `/join <邀请链接>` 让它加入（号主即时生效，其他用户默认需号主在管理端审核，功能默认关闭）。
- 完整的目的、设计原理与请求流程见 **[项目介绍](docs/guide/introduction.md)**；架构与模块职责见 **[架构文档](docs/reference/architecture.md)**。

## 🚀 快速开始

### 1. 准备凭据

| 凭据 | 获取方式 |
| --- | --- |
| Bot Token | 向 [@BotFather](https://t.me/BotFather) 创建 bot 获取 |
| API ID / API Hash | [my.telegram.org](https://my.telegram.org) → API development tools 申请，[完整流程](docs/guide/telegram-api-credentials.md) |
| 你的 Telegram 用户 ID | 通过 @userinfobot 等查询 |

### 2. 本地运行

```bash
cp .env.example .env   # 至少填入 BOT_TOKEN、TG_API_ID、TG_API_HASH
make dev
```

首次启动默认**扫码登录**：终端渲染二维码，手机 Telegram → 设置 → 设备 → 关联桌面设备，扫码授权即可，无需手机号与验证码。会话保存在 `data/session.json`，重启自动登录（`LOGIN_MODE=phone` 可改用手机号+验证码）。

同一进程内还有一个 **Bot 身份的 MTProto 会话**（`data/bot-session.json`），用 `.env` 里的 `BOT_TOKEN` 首次启动自动登录、之后静默复用，专职大文件直传，无需任何人工操作。

**多机器人池（可选）**：`BOT_TOKENS`（逗号分隔）或管理端「机器人池管理」页可绑定多个 bot（上限 20，修改后重启生效）。所有 bot 平等服务同一套频道绑定——用户从任意 bot 提交，回复从受理 bot 返回，请求记录/用户来源/频道统计/业务统计均按 bot 维度留痕，摊薄单 bot 的限流与封禁风险。绑定频道与缓存频道要求**所有** bot 均为频道管理员；每个 bot 独立 MTProto 大文件直传会话（`data/bot-session-<botID>.json`，首启自动登录）。详见 [配置参考](docs/reference/configuration.md)。

## 🐳 Docker Compose 部署

**一条命令部署**（全新 Linux 服务器，需已装 Docker；缺失时脚本可交互安装）：

```bash
curl -fsSL https://raw.githubusercontent.com/huaiminyetnotsleep/spore/main/install-spore.sh | bash
```

运行后出现管理菜单，选 **1** 安装：自动下载配置到 `~/spore` → 交互式填写 `BOT_TOKEN` / `TG_API_ID` / `TG_API_HASH`（可留空回车跳过，之后在 `.env` 补填）→ 选择宿主端口（默认 8080，自动检测占用）→ 授权 `data/` → 自选是否立即启动（不启动则稍后 `spore restart` 首启）→ 启动时打印首次访问密钥。安装完成后会把脚本注册为 **`spore` 命令**，之后直接输入 `spore` 打开管理菜单，或 `spore status` / `spore upgrade` 等子命令完成**升级、验证、状态、日志、查看/重设密钥、清理临时文件、磁盘检查、重启、停止、卸载**等运维操作；部署目录可经 `curl ... | SPORE_DIR=/opt/spore bash` 覆盖。详见[部署指南](docs/guide/deployment.md)。

```bash
spore             # 打开管理菜单（安装/升级/验证/状态/日志/密钥/清理/重启/停止/卸载）
spore status      # 例：查看运行状态（免菜单直通）
spore upgrade     # 例：升级到最新镜像
```

手动部署（或脚本不适用时）：

```bash
cp .env.example .env       # 填 Telegram 凭据
chmod 600 .env
mkdir -p data
sudo chown -R 10001:10001 data
docker compose up -d --build
```

- 当 GHCR 包可见性为 Public 时无需登录：直接执行 `docker compose pull && docker compose up -d`（镜像 `ghcr.io/huaiminyetnotsleep/spore`）。如果包被设为 Private，才需要先执行 `docker login ghcr.io`。
- Compose 仅把 Web 端口发布到宿主机 `127.0.0.1:8080`，域名、HTTPS 与 80/443 由反向代理（Caddy / Nginx 等）负责。
- 首次启动生成的管理端访问密钥只打印一次到 bot 日志，请立即保存；管理端入口为 `/admin/login`。
- 数据库备份不包含 MTProto 会话：恢复后**用户号会话**需重新扫码，**Bot 会话**会用 Token 自动重登。

## 📖 日常使用

私聊 Bot 发送消息链接即可：

```text
https://t.me/<用户名>/<消息ID>
t.me/c/<内部ID>/<消息ID>
```

- 收到「正在获取消息...」占位后稍候，Bot 会把重构后的新消息发回；占位上会显示下载/上传进度。
- 首次使用先发 `/start` 提交申请，管理员在 Web 管理端批准后即可使用。
- 全部命令、链接写法、配额与限制见 **[Bot 使用指南](docs/guide/usage.md)**。

## 🔒 安全须知

- `.env`、`data/`（session.json / bot-session.json / peers.json / cloud-drive.json / tmp）已在 [.gitignore](.gitignore) 中排除——**用户号 Session 等效于账号控制权，切勿泄露或提交**；Bot 会话与云盘凭据文件同样不要泄露。
- 白名单外用户发来的任何消息只得到固定拒绝文案。
- 敏感数据的存储与传递边界详见[配置参考 §7](docs/reference/configuration.md)。

## 🛠️ 开发

```bash
make build   # 编译（含前端构建与 embed 同步）
make vet     # 静态检查
make test    # 单元测试（提交前建议 go test ./... -race）
make linux   # 交叉编译 Linux 二进制
```

本地运行、前端热更新、测试门禁与 Compose 验收见 **[本地开发与调试](docs/guide/development.md)**。

## 📚 文档

完整文档在 [`docs/`](docs/)（VitePress 站点，本地预览 `make docs-dev`），推荐直接 [在线阅读](https://huaiminyetnotsleep.github.io/spore/)：

| 文档 | 在线版 | 内容 |
| --- | --- | --- |
| [项目介绍](docs/guide/introduction.md) | [↗](https://huaiminyetnotsleep.github.io/spore/guide/introduction) | 目的、核心功能、设计原理与请求流程 |
| [快速部署](docs/guide/deployment.md) | [↗](https://huaiminyetnotsleep.github.io/spore/guide/deployment) | 首次上线、访问密钥、反向代理 |
| [Bot 使用指南](docs/guide/usage.md) | [↗](https://huaiminyetnotsleep.github.io/spore/guide/usage) | 命令、链接写法、配额与限制 |
| [下载功能](docs/guide/download.md) | [↗](https://huaiminyetnotsleep.github.io/spore/guide/download) | 云盘下载的目的地配置与边界 |
| [配置参考](docs/reference/configuration.md) | [↗](https://huaiminyetnotsleep.github.io/spore/reference/configuration) | 环境变量、持久化设置、生效时机 |
| [管理端 API](docs/reference/api.md) | [↗](https://huaiminyetnotsleep.github.io/spore/reference/api) | `/api/v1` 接口契约 |
| [数据库设计](docs/reference/database-schema.md) | [↗](https://huaiminyetnotsleep.github.io/spore/reference/database-schema) | 表结构、字段、索引、迁移历史与接口持久化映射 |
| [架构文档](docs/reference/architecture.md) | [↗](https://huaiminyetnotsleep.github.io/spore/reference/architecture) | 架构、模块职责与技术决策 |
| [运维手册](docs/ops/operations.md) | [↗](https://huaiminyetnotsleep.github.io/spore/ops/operations) | 升级、备份恢复、云盘运维 |
| [本地开发](docs/guide/development.md) | [↗](https://huaiminyetnotsleep.github.io/spore/guide/development) | 构建、测试与验收门禁 |

### 官方与第三方文档

使用和开发时常查的官方资料，点击即可查看：

| 文档 | 内容 |
| --- | --- |
| [Telegram Bot API](https://core.telegram.org/bots/api) | Bot 接口官方参考（消息、文件上传上限等） |
| [Telegram MTProto API](https://core.telegram.org/api) | 用户账号 API 官方文档，本工具读取源消息所依赖 |
| [my.telegram.org](https://my.telegram.org) | 申请 API ID / API Hash（[申请流程](docs/guide/telegram-api-credentials.md)） |
| [Telegram 服务条款](https://telegram.org/tos) | 平台使用条款，使用前请阅读 |
| [Telegram API 服务条款](https://core.telegram.org/api/terms) | 使用 Telegram API 需遵守的条款 |
| [gotd/td](https://github.com/gotd/td) | MTProto 客户端（用户号 / Bot 号双会话） |
| [go-telegram/bot](https://github.com/go-telegram/bot) | Bot API 客户端（流式上传） |
| [rclone 文档](https://rclone.org/docs/) | 云盘远端配置与上传 |

## 🗺️ 当前状态

- 自动化测试覆盖纯逻辑包与 Web API/前端组件；**Telegram 登录、频道读取、媒体上传、相册、云盘与部署仍需真机/环境验收**，使用前请阅读各页「限制」与[运维手册](docs/ops/operations.md)。

## 🤝 参与贡献

欢迎通过 Issue、文档改进和 Pull Request 参与 Spore。建议先 Fork 仓库、创建主题分支，在本地完成测试后提交 PR；请不要在 Issue、PR、日志或截图中提交 Token、Session、数据库或其他个人凭据。

- [贡献指南](CONTRIBUTING.md)：开发环境、测试门禁和 PR 流程
- [安全政策](SECURITY.md)：漏洞报告和凭据泄露处理
- [在线文档](https://huaiminyetnotsleep.github.io/spore/)

<a id="compliance"></a>

## ⚖️ 使用边界与合规要求

Spore 只是一个由个人维护的自托管技术工具，不提供内容授权、托管运营、法律审查或合规保证。部署、配置或使用本项目即表示使用者理解并同意自行承担相应责任，包括但不限于：

1. **遵守平台规则**：遵守 [Telegram Terms of Service](https://telegram.org/tos)、[Telegram API Terms of Service](https://core.telegram.org/api/terms) 及 Telegram 不时更新的其他适用规则，不得滥用用户账号、Bot、API、邀请链接或平台资源。
2. **确保内容授权**：仅处理自己拥有或已取得合法、充分授权的内容。即使账号可以访问源消息，也不得在未经授权的情况下复制、下载、改编、转发、公开传播或用于其他目的。
3. **尊重保护措施**：不得利用本项目未经授权地规避访问控制、付费限制、「限制保存内容」等技术或平台保护措施，不得协助他人实施上述行为。
4. **保护版权与人格权益**：遵守著作权、商标权、肖像权、名誉权、商业秘密及其他知识产权或人格权益要求；转载或公开传播时，应按适用规则取得许可并保留必要的来源及权利信息。
5. **保护隐私和个人数据**：处理用户名、用户 ID、消息、媒体、日志、Session、Token、数据库及云盘文件时，应遵守适用的隐私、个人信息和数据保护法律，并根据实际场景履行告知、同意、安全保护、保存期限及删除等义务。
6. **禁止违法或有害用途**：不得将本项目用于侵权传播、非法获取或交易数据、骚扰、跟踪、诈骗、绕过监管，或制作、保存、传播任何违法及无权处理的内容。
7. **保障账号与系统安全**：使用隔离的测试或服务账号，妥善保管凭据，合理限制访问权限，并自行承担封号、限流、数据泄露、数据丢失及第三方索赔等风险。
8. **遵守所在地法律**：不同国家和地区对通信内容、数据抓取、技术措施规避、个人信息处理及跨境传输的规定可能不同。部署者和使用者应自行确认其使用方式在相关司法辖区内合法；如有疑问，应停止使用并咨询具备资质的专业人士。
9. **违法责任自负**：任何利用本项目实施的违法、违规、侵权或违反平台条款的行为，均由实际部署者、使用者、传播者及其他相关行为人依法独立承担责任。开源提供代码不代表维护者授权、认可、参与或协助任何具体使用行为，也不能成为相关行为人减轻或免除责任的理由。

本项目按现状提供，仅供合法用途。维护者无法控制第三方如何部署或使用本项目；在适用法律允许的范围内，维护者不对第三方未经授权、违反平台规则或违反法律法规的独立行为承担责任。本节仅用于提示一般风险，不构成法律意见，亦不能替代针对具体使用场景的专业法律咨询。

如果维护者发现或合理认为本项目已被用于违法违规、侵害他人权益，或者继续公开可能带来显著的法律、安全或滥用风险，维护者保留随时暂停维护、停止发布、关闭相关服务及镜像，并在可控范围内下架公开项目和源代码的权利，且可不另行通知。下架措施不代表维护者能够删除或控制第三方此前已经下载、复制或 Fork 的代码，也不影响相关行为人对其既有行为依法承担责任。

## 📄 许可证及第三方协议

- 本项目源代码以 [PolyForm Noncommercial License 1.0.0](LICENSE) 发布，软件按“原样”提供，不附带任何明示或默示担保。复制、修改、分发时应保留版权及许可声明（或 [LICENSE](LICENSE) 中的协议链接）。
- 本协议**仅授权非商业用途**：个人自用、学习研究、业余爱好项目，以及慈善、教育、公共研究等非商业组织的使用与 fork、分发均在授权范围内。**任何商业用途——包括但不限于商业部署、售卖、付费服务、集成进商业产品或服务、以营利为目的的运营——均未获授权**，如需商业使用请事先联系作者取得单独授权。
- Go、前端及构建工具等第三方依赖仍分别受其上游许可证、版权声明和使用条款约束；使用者在重新分发软件、容器镜像或衍生作品前，应自行核对并履行相应的许可证义务。
- Telegram 名称、商标、API、客户端协议和服务由其权利人提供并受相应条款约束；本项目的 PolyForm Noncommercial License 不授予任何第三方内容、商标、服务或数据的权利。

## 🙏 致谢

- [go-telegram/bot](https://github.com/go-telegram/bot) —— Bot API 客户端（流式上传）
- [gotd/td](https://github.com/gotd/td) —— MTProto 客户端（用户号 / Bot 号双会话）
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) —— 纯 Go SQLite 驱动
- [rclone](https://rclone.org/) —— 云盘上传
