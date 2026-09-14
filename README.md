# Spore

<div align="left">

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-Compose-2496ED?logo=docker&logoColor=white)
![Docs](https://img.shields.io/badge/Docs-VitePress-646CFF?logo=vitepress&logoColor=white)
![Image](https://img.shields.io/badge/image-ghcr.io%2Fhuaiminyetnotsleep%2Fspore-0969DA?logo=github)

</div>

把频道消息链接发给 Bot，收到的就是一条**可再次转发的全新消息**。

Spore 是一个 Telegram 受保护消息提取机器人：频道开启「限制保存内容」后，消息无法转发、无法复制；本机器人通过 **MTProto 用户账号**读取源消息，重新构造为一条全新消息发回——不保留 Forward Header，可正常再次转发。

> Bot API 负责「和用户聊天」，MTProto 负责「以用户身份访问 Telegram」，业务层负责「把源消息转换成新的消息」。

## ✨ 功能特性

- 📨 **受保护消息提取**：文本（保留实体格式）、图片、视频、文件、音频、语音条与相册；超过 Bot API 50MB 上限的大文件经 Bot 身份 MTProto 直传，上限 2000MB。
- 🤖 **Bot 交互**：私聊发链接即可使用；`/start` 申请、额度查询、进度、取消、频道绑定与加入等命令齐备，配额与重试边界明确。
- ☁️ **云盘下载**：`/download` 把提取的媒体直接上传到网盘（经 rclone），不再重发回 Telegram；支持多目的地、排队与取消。
- ♻️ **缓存频道复用**：成功投递后同步写无脚注干净副本到自有缓存频道，同链接再次提交整条复制秒回——不限媒体大小、相册保组。
- 🖥️ **Web 管理端**：内置 SPA 管理面板——用户审批与管理、请求记录、频道统计、运行设置、云盘配置、资源监控与审计。
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

## 🐳 Docker Compose 部署

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

完整文档在 [`docs/`](docs/)（VitePress 站点，本地预览 `make docs-dev`）：

| 文档 | 内容 |
| --- | --- |
| [项目介绍](docs/guide/introduction.md) | 目的、核心功能、设计原理与请求流程 |
| [快速部署](docs/guide/deployment.md) | 首次上线、访问密钥、反向代理 |
| [Bot 使用指南](docs/guide/usage.md) | 命令、链接写法、配额与限制 |
| [下载功能](docs/guide/download.md) | 云盘下载的目的地配置与边界 |
| [配置参考](docs/reference/configuration.md) | 环境变量、持久化设置、生效时机 |
| [管理端 API](docs/reference/api.md) | `/api/v1` 接口契约 |
| [架构文档](docs/reference/architecture.md) | 架构、模块职责与技术决策 |
| [运维手册](docs/ops/operations.md) | 升级、备份恢复、云盘运维 |
| [本地开发](docs/guide/development.md) | 构建、测试与验收门禁 |

## 🗺️ 当前状态

- 自动化测试覆盖纯逻辑包与 Web API/前端组件；**Telegram 登录、频道读取、媒体上传、相册、云盘与部署仍需真机/环境验收**，使用前请阅读各页「限制」与[运维手册](docs/ops/operations.md)。

## 🤝 参与贡献

欢迎通过 Issue、文档改进和 Pull Request 参与 Spore。建议先 Fork 仓库、创建主题分支，在本地完成测试后提交 PR；请不要在 Issue、PR、日志或截图中提交 Token、Session、数据库或其他个人凭据。

- [贡献指南](CONTRIBUTING.md)：开发环境、测试门禁和 PR 流程
- [安全政策](SECURITY.md)：漏洞报告和凭据泄露处理
- [在线文档](https://huaiminyetnotsleep.github.io/spore/)

## ⚖️ 使用边界

Spore 只是一个自托管的技术工具。使用者必须遵守 Telegram 的服务条款、适用法律以及源内容的版权和隐私要求，只处理自己有权访问和保存的内容；项目维护者不为部署者的运行内容、账号行为或数据处理承担责任。

## 📄 许可证

本项目以 [MIT License](LICENSE) 发布。第三方依赖和工具仍受其各自许可证约束，详见项目依赖声明和对应上游项目。

## 🙏 致谢

- [go-telegram/bot](https://github.com/go-telegram/bot) —— Bot API 客户端（流式上传）
- [gotd/td](https://github.com/gotd/td) —— MTProto 客户端（用户号 / Bot 号双会话）
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) —— 纯 Go SQLite 驱动
- [rclone](https://rclone.org/) —— 云盘上传
