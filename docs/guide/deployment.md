# Spore Docker Compose 部署指南

> Spore 的官方部署方式是 Docker Compose。应用负责 Bot、MTProto 和管理端，宿主机已有的
> Caddy、Nginx 或其他反向代理负责公网 HTTPS 入口；业务数据库是 `data/spore.db`，
> MTProto 会话仍是独立的 `data/session.json` 文件。配置项完整说明见
> [architecture.md](../reference/architecture.md)，常见问题见 [troubleshooting.md](../ops/troubleshooting.md)。
> 上线后的升级回滚、备份恢复、卸载与故障排查见 [operations.md](../ops/operations.md)。

## 1. 部署前准备

### 1.1 服务器与网络

| 项目 | 要求 |
| --- | --- |
| Docker | Docker Engine 与 Compose v2 插件，可执行 `docker compose version` |
| 系统 | 支持 Docker 的 Linux 服务器；示例命令使用 Debian/Ubuntu |
| 域名 | 一个解析到服务器公网 IP 的域名，例如 `admin.example.com` |
| 反向代理 | VPS 宿主机已有 Caddy、Nginx 或其他代理，负责 HTTPS 并反代到 `127.0.0.1:<WEB_HOST_PORT>`（默认 `8080`） |
| 入站端口 | TCP 80、443；由反向代理负责 HTTP→HTTPS、证书和管理端访问 |
| Telegram 网络 | 服务器能访问 Telegram Bot API 和 MTProto；必要时按环境注入代理 |
| 磁盘 | 至少 1GB；传输大文件时，临时目录还须预留最大媒体文件大小（启用 §5 本地 Bot API 备选路线则须双份预留） |
| 权限 | 运行目录的 `data/` 和可选 `bot-api-data/` 可由容器 UID 10001 写入 |

Compose 不包含反向代理或证书服务。宿主机代理应自动申请和续期证书，域名未解析、
80/443 被其他程序占用、或服务器被防火墙拦截时，HTTPS 或证书申请会失败。

### 1.2 Telegram 与管理端凭据

准备以下信息，但不要把它们提交到 Git 或写入文档：

1. 从 [@BotFather](https://t.me/BotFather) 获取 `BOT_TOKEN`；
2. 从 [my.telegram.org](https://my.telegram.org) 获取 `TG_API_ID` 和 `TG_API_HASH`；
3. 可选：旧部署的 `ALLOWED_USER_IDS`。它只在业务数据库的 `users` 表为空时导入，
   后续白名单由 Web 管理端接管；
4. 可选：GitHub OAuth App 的 Client ID 和 Secret。申请步骤与完整配置指南见
   [github-oauth.md](github-oauth.md)。

GitHub OAuth App 的回调地址必须设置为：

```text
https://<你的管理端域名>/auth/github/callback
```

例如域名为 `admin.example.com` 时，回调地址是
`https://admin.example.com/auth/github/callback`。OAuth 凭据只放在服务器的 `.env`，
不要放进 Compose 文件、截图、日志或工单。若需要在 Web 中修改 GitHub Client Secret 或保存通知通道凭据，
还需配置 `WEB_OAUTH_ENCRYPTION_KEY`（32 字节原文、hex 或 base64）。推荐执行
`openssl rand -hex 32` 生成一次并把完整输出写入 `.env`；此后长期保留原值，升级时不要重新生成。
该主密钥不进入数据库备份，迁移数据库时须通过独立安全通道一起迁移；丢失后只能重新填写加密凭据。

本文只列最小启动凭据与部署相关变量；全部环境变量的默认值、取值范围、与数据库设置的
覆盖关系及生效方式（即时/重启）见 [configuration.md](../reference/configuration.md)。

## 2. 目录、端口与持久化边界

仓库部署目录建议如下（本文以 `~/spore` 为例，即 `/home/<用户名>/spore`，
由 §4.2 第 1 步创建）：

```text
spore/
├── .env                 # 服务器私密配置，不提交
├── docker-compose.yml
├── data/                # bind mount：SQLite、Session、Peer 缓存、临时媒体
└── bot-api-data/        # 可选：bigfile profile 的 Bot API 文件目录
```

Compose 不创建反向代理或证书命名卷。`bot` 将容器端 `8080` 发布到宿主机
`127.0.0.1:<WEB_HOST_PORT>`（默认 `8080`；宿主端口只经 `.env` 的 `WEB_HOST_PORT`
修改，同机多实例各自错开即可，容器内恒为 `8080`），只有宿主机上的反向代理可以
访问；可选的 `bot-api` 只在 `bigfile` profile 中启动，端口经 `.env` 的
`BOT_API_PORT` 配置（默认 `8081`），仅在 Compose 网络暴露，不对公网开放。

业务数据边界如下：

| 路径 | 内容 | 是否包含在 Web 数据库备份 |
| --- | --- | :---: |
| `data/spore.db` | 用户、请求、用量、审计、事件、设置、Web 会话 | 是 |
| `data/session.json` | MTProto 用户账号会话，等效账号控制权 | 否 |
| `data/bot-session.json` | MTProto Bot 身份会话（大文件直传用，凭 BOT_TOKEN 自动重登，丢失无感） | 否 |
| `data/peers.json` | Telegram Peer/AccessHash 缓存 | 否 |
| `data/tmp/` | 媒体临时文件 | 否 |
| `bot-api-data/` | 可选本地 Bot API 服务数据 | 否 |

## 3. 宿主机反向代理

Compose 不启动 Caddy、Nginx 或其他反向代理，也不占用宿主机的 80/443。请在宿主机
已有代理中配置一个 HTTPS 虚拟主机，将管理端域名反代到
`http://127.0.0.1:<WEB_HOST_PORT>`（默认 `8080`），并只允许该代理访问应用端口。代理必须：

- 监听公网 TCP 80/443，完成 HTTP→HTTPS 跳转和 ACME/证书续期；
- 转发 `Host`、`X-Forwarded-For`、`X-Forwarded-Proto`，并限制可信代理来源；
- 不把应用端口再暴露到公网，不与其他代理重复占用 80/443。

以下示例使用默认端口 `8080`；若在 `.env` 中修改了 `WEB_HOST_PORT`，请同步替换。

Caddy 配置示例（使用实际域名替换占位符）：

```text
admin.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

Nginx 的等价核心配置示例：

```nginx
server {
    listen 443 ssl;
    server_name admin.example.com;
    location / {
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_pass http://127.0.0.1:8080;
    }
}
```

证书、HTTP→HTTPS 跳转和 80 端口 ACME 配置由所选代理负责。若代理确实受信任，
再将 `.env` 中的 `WEB_TRUSTED_PROXY` 设为 `true`，否则保持 `false`。请按代理自身
文档配置证书路径、续期和安全组，本文不管理宿主机代理配置。

## 4. 首次安装与启动

### 4.1 选择部署方式

Spore 提供两种部署方式，都会运行相同的 `bot` 服务；GHCR 是否可匿名拉取取决于 GitHub Packages 的包可见性：

| 方式 | 适用场景 | 服务器需要 |
| --- | --- | --- |
| A：拉取 GHCR 镜像（推荐） | 常规部署，服务器无需访问源码 | `docker-compose.yml` + `.env`，Public 包可匿名拉取 |
| B：源码构建 | 需改源码、调试或未发布镜像时 | `git clone`、Go/Node 工具链 |

方式 A 是常规部署首选：服务器**无需 clone 源码、无需编译**，后续升级只需
`docker compose pull` + `docker compose up -d`。首次搭建只需从仓库下载
`docker-compose.yml` 和 `.env.example`；如果所在组织将 GHCR 包设为私有，再按组织策略
配置只读包凭据。仓库内容和镜像不包含任何 Telegram、OAuth 或云盘凭据。

### 4.2 方式 A：GHCR 镜像端到端（推荐）

#### 一键安装（推荐）

在 VPS 终端执行一条命令即可进入安装管理菜单：

```bash
curl -fsSL https://raw.githubusercontent.com/huaiminyetnotsleep/spore/main/install-spore.sh | bash
```

菜单按「部署管理 / 服务控制 / 密钥与维护」三组提供安装、升级（完成后自动验证）、卸载、
重启、完整重启（重建容器加载环境变量）、停止、状态、日志、密钥查看与重设、临时清理、
磁盘检查与退出（各子命令说明见
[operations.md §1.1](../ops/operations.md)）。选 **1 安装**
即完成下述第 1–5 步的全部动作（创建部署目录、下载两个配置文件、交互式填写凭据、
授权数据目录、拉取镜像并启动、等待健康检查、提示保存访问密钥）。

脚本行为说明：

- 部署目录默认 `~/spore`，可覆盖：`curl ... | SPORE_DIR=/opt/spore bash`；
- 所有交互输入读 `/dev/tty`，`curl | bash` 管道环境可用；无交互终端（CI 等）时安装
  会生成 `.env` 后提示手工填写，重新运行同一条命令即可继续；
- 凭据提问支持留空回车**跳过**：部署会继续，但 Telegram 登录会失败；之后编辑
  `~/spore/.env` 填入凭据，再运行 `spore upgrade`（或菜单「2) 升级服务」；仅改环境
  变量时用 `spore recreate` / 菜单「完整重启」即可）使其生效；
  重跑 install 只会补问缺失项；
- 安装时询问宿主访问端口，默认 `8080`，脚本会实时探测占用、被占时要求更换；服务已
  运行时改端口会自动重建容器使其生效。已安装后改端口：编辑 `.env` 的 `WEB_HOST_PORT`
  后执行 `docker compose up -d`（或 `spore upgrade`）；
- 安装结束询问**是否立即启动**：选择「否」只完成配置与 `spore` 命令注册，之后运行
  `spore restart`（未运行时会直接启动）完成首次启动；
- Docker 未安装时脚本会询问是否用 get.docker.com 官方脚本自动安装；
- 菜单「升级」不改任何配置，仅拉取新镜像并滚动更新；「卸载」默认保留 `.env` 与
  `data/`，按提示二次确认后才删除；
- 也可用子命令直接调用（便于脚本化）：`install-spore.sh install | upgrade | verify | status | logs | show-key | reset-key | clean-tmp | diskcheck | restart | recreate | stop | uninstall | exit`；安装或升级后脚本会把自己注册为系统的 `spore` 命令（软链到 `/usr/local/bin/spore`），之后直接输入 `spore` 打开菜单或 `spore <子命令>` 调用，子命令说明见 [operations.md §1.1](../ops/operations.md)；
- 首次扫码登录是固有人工环节：启动后按 §4.4 在管理端「MTProto」页面完成。

下面的分步说明是一键脚本的等价展开，便于核对脚本每一步做了什么，也可作为手动
部署路径。

#### 分步安装

每条命令都标注执行位置：**本机**指你自己的电脑
终端，**VPS** 指登录服务器后的终端。部署目录统一使用 `~/spore`（即
`/home/<用户名>/spore`），由第 1 步的 `mkdir` 创建，不需要替换任何占位路径。

**第 0 步（本机）：登录 VPS。** 仅这一条在本机执行，之后所有命令都在 VPS 终端：

```bash
ssh user@服务器
```

**第 1 步（VPS）：创建部署目录。**

```bash
mkdir -p ~/spore && cd ~/spore
```

**第 2 步（VPS）：下载两个配置文件 `docker-compose.yml` 与 `.env.example`。**
这一步下载的只是几 KB 的文本配置，不是应用镜像；应用镜像在第 5 步才拉取。
无需 GitHub 登录或 PAT：

```bash
curl -fsSL -o docker-compose.yml \
  https://raw.githubusercontent.com/huaiminyetnotsleep/spore/main/docker-compose.yml
curl -fsSL -o .env.example \
  https://raw.githubusercontent.com/huaiminyetnotsleep/spore/main/.env.example
```

> 备选：直接从仓库克隆后复制文件：
>
> ```bash
> git clone --depth 1 https://github.com/huaiminyetnotsleep/spore.git ~/spore-source
> cp ~/spore-source/docker-compose.yml ~/spore-source/.env.example .
> rm -rf ~/spore-source
> ```
>
> 如果企业网络或组织策略要求认证，使用只读凭据即可；不要把凭据写入 URL、脚本或
> Compose 文件。

**第 3 步（VPS）：创建配置并填写凭据。**

```bash
cp .env.example .env && chmod 600 .env
```

编辑 `.env`，至少填写：

```dotenv
BOT_TOKEN=从服务器 Secret 注入
TG_API_ID=你的数字 API ID
TG_API_HASH=你的 API Hash
```

管理端域名不进 `.env`：它只配置在 DNS 记录和宿主机反向代理的虚拟主机中，且必须
实际解析到这台服务器。不要把真实 Token、API Hash、访问密钥或 OAuth Secret 粘贴到
shell 历史、文档或版本库。

**第 4 步（VPS）：创建数据目录并授权给容器用户。**

```bash
mkdir -p data && sudo chown -R 10001:10001 data && chmod 700 data
```

大文件默认经 MTProto 直传，无需该目录；仅当按 §5 启用本地 Bot API 备选路线时，才需要提前创建 `bot-api-data/`。

**第 5 步（VPS）：拉取并启动 GHCR 镜像。** 这一步下载真正的应用镜像（几百 MB
的程序本体）：`docker compose pull` 会从 Compose 文件中的
`ghcr.io/huaiminyetnotsleep/spore` 拉取镜像；当该 GHCR 包为 Public 时不需要登录。

```bash
docker compose pull
docker compose up -d
docker compose ps
```

如果你的网络或组织策略要求认证，再按 [GitHub Packages 文档](https://docs.github.com/packages)
配置只读的 GHCR 凭据；不要把 Token 写进 `.env`、Compose 文件或 shell 脚本。

**第 6 步（VPS）：按 §4.4 验证并保存首次访问密钥。** 以后每次升级只需回到
`~/spore` 重新执行第 5 步的 `docker compose pull` + `docker compose up -d`，详见
[operations.md](../ops/operations.md) §2.1；回滚固定版本见 §2.2。

### 4.3 方式 B：源码构建部署

需要改源码、调试或镜像未发布时使用。仓库可直接克隆，构建
需要 Go/Node 工具链；克隆会自带 `docker-compose.yml` 与 `.env.example`：

```bash
# VPS：克隆并进入部署目录
git clone https://github.com/huaiminyetnotsleep/spore.git ~/spore
cd ~/spore
```

然后按 §4.2 第 4–5 步创建 `.env` 与数据目录，再检查 Compose 展开结果（不会启动
服务）、构建并后台启动：

```bash
docker compose config
docker compose up -d --build
```

启动后同样按 §4.4 验证。

### 4.4 启动后验证与首次登录

两种方式启动后都用同样的命令查看状态与日志：

```bash
docker compose ps
docker compose logs -f bot
```

对外验证（`<你的域名>` 为管理端域名）：

```bash
# 应返回 200 和简短的 ok，不含凭据
curl -fsS https://<你的域名>/healthz

# 数据库可用时应返回 200；数据库未就绪时返回 503
curl -fsS https://<你的域名>/readyz
```

`/healthz` 和 `/readyz` 不需要登录，但只返回存活/就绪状态，不提供业务数据。管理
页面、导出、审批、重试、备份等其他路由都必须先登录。

应用启动时会自动执行 SQLite 版本迁移，并在首次部署时生成管理端访问密钥。访问
密钥只会在 bot 容器的 stderr 日志中明文打印一次，请立即保存到离线密码管理器：

```bash
docker compose logs bot | grep -F '访问密钥'
```

不要把包含访问密钥的日志复制到公开位置。登录地址是管理端唯一入口：

```text
https://<你的域名>/admin/login
```

旧 SSR 登录页 `/login` 已删除，不会重定向，直接访问返回 404。

如果首次启动需要 MTProto 登录，优先使用管理端的 **MTProto** 页面触发 Web 扫码；
二维码由 `/mtproto/qr.png` 提供。按页面提示在 Telegram 手机端完成扫码和可能的
2FA 密码步骤。登录成功后会话写入 `data/session.json`，后续重启不需要再次扫码。
在 MTProto 尚未就绪时，宿主机反向代理、登录页、健康探针和管理端基础页面仍可访问。

> 也可以使用终端扫码作为降级路径：`docker compose run --rm -it bot`。完成后
> 按 `Ctrl-C` 退出，再执行 `docker compose up -d`。该命令不会把 Session 写入镜像，
> 只会写入挂载的 `data/`。

### 4.5 访问密钥与 GitHub OAuth

- 首次启动打印的访问密钥只存其 SHA-256 哈希到数据库；页面登录使用原始密钥，
  不把密钥放在 URL 中；
- 忘记或怀疑泄露时，在 VPS 上按以下步骤重置：

  ```bash
  cd ~/spore                        # §4.2 创建的部署目录
  docker compose ps                   # 确认 bot 服务名称与运行状态
  docker compose exec bot spore admin reset-key
  ```

  如果 `bot` 容器没有运行，先启动它再执行重置：

  ```bash
  docker compose up -d bot
  docker compose exec bot spore admin reset-key
  ```

  新密钥只会打印到当前 SSH 终端一次，请立即保存到密码管理器；然后使用现有的
  Caddy/Nginx 公网域名打开管理端，用新密钥登录并确认 `/healthz`、管理页面正常。
  重置不会删除 `data/spore.db`、白名单、消息记录、统计、审计记录或
  `data/session.json`，不需要重新扫码登录 MTProto；旧密钥和全部已有 Web 会话立即
  失效，动作会写入审计。
- GitHub OAuth 不是默认登录凭据。先使用访问密钥登录，在“设置 → GitHub 登录”中
  绑定唯一 GitHub 数字账号，之后只有该账号可以 OAuth 登录；绑定变更会使所有会话失效；
  OAuth App 的申请与配置详见 [github-oauth.md](github-oauth.md)。
- OAuth 未配置时，页面只显示访问密钥登录；OAuth 不可用不影响访问密钥通道。

## 5. 大文件说明与本地 Bot API 备选路线

官方 Bot API 服务器的上传硬上限是 50MB。**默认无需任何额外配置**：超过该上限的
媒体自动经 Bot 身份 MTProto 会话直传（`data/bot-session.json`，用 `BOT_TOKEN`
自动登录），上限 2000MB（`MAX_FILE_SIZE` 默认值）；Bot 会话不可用时仅大文件
发送失败，小文件不受影响。`/status` 的"大文件直传通道"可查看其可用性。
超过 2000MB 的媒体自动**分卷拆分**：视频经 ffmpeg 流复制切为可直接播放的
分段（无损、无需合并），与同组媒体合成一条相册送达（约 17.6GB 上限）；非
视频媒体回退字节分段（首段附合并提示）。需宿主机安装 ffmpeg（FFMPEG_PATH
可指定路径）；拆分任务磁盘峰值 ≈ 2× 文件大小（TEMP_DIR_MAX_SIZE 需覆盖）。
本地 Bot API 备选路线不承载拆分（其上限同为 2000MB）。

仓库另附一条**备选路线**：自建官方 telegram-bot-api 服务器（`bigfile` profile，
默认不启动），配置 `BOT_API_URL` 后全部上传改走它、不再使用 MTProto 直传，
两种路线的上限同为 2000MB。仅当默认直传通道异常需要应急切换时才启用；镜像
入口脚本的参数细节见 `docker-compose.yml` 内注释。

```dotenv
BOT_API_URL=http://bot-api:8081   # Compose 内部服务名；不要写 localhost（在 bot 容器内指向 bot 自身）
BOT_API_PORT=8081                 # bot-api 服务端口（默认 8081）；改动时需同步上面 BOT_API_URL 的端口
MAX_FILE_SIZE=2097152000
```

```bash
mkdir -p bot-api-data   # 首次启用前创建；目录权限要求见 §1.1
docker compose --profile bigfile up -d
```

注意磁盘成本：该服务器把上传媒体落盘到 `bot-api-data/`，加上 bot 自身
`data/tmp/` 的临时文件，需按最大单文件预留双份余量。回到默认路线：移除
`BOT_API_URL` 并重启容器即可。

## 6. 本地开发机 Compose 与 SPA 验收

发布前可在本地开发机用隔离配置复核镜像、路由边界与健康检查。该流程不是生产 HTTPS 验收，不使用生产 Token、Session、OAuth Secret 或二维码：

```bash
docker compose config
docker build -t spore-cutover-check .
docker compose up -d --build
docker compose ps
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/readyz
```

注意数据隔离的静态边界：Compose 把**当前部署目录下**的 `./data` 固定挂载为容器的
`/app/data`，没有单独的数据目录配置项。因此只换一份隔离 `.env` 并不会隔离数据；
要并存两套互不影响的本机环境，需要把整份部署目录（`docker-compose.yml` + 各自的
`.env` 与 `data/`）复制到不同目录分别执行。

前端轻量门禁与浏览器级 e2e 属于开发验证流程，命令与依赖准备见
[development.md](development.md)；e2e 与 Web 边界契约的唯一详细出处是
[SPA 验收记录](../reference/admin-acceptance.md)。

Compose 只应把管理端发布到宿主回环 `127.0.0.1:<WEB_HOST_PORT>`（默认 `8080`）。
验证失败时执行 `docker compose restart bot`，
确认 `/healthz` 恢复。旧 SSR 认证页与表单路由代码已移除：未登录访问 `/admin` 下的
受保护页面会被引导到 `/admin/login`，旧路径（如 `/users`、`/settings`）现在直接
返回 404（此切换不涉及数据库 schema 变更）。真实 HTTPS、反向代理、生产
OAuth/MTProto/备份和观察窗口需另行验收，历史验收结论同样见
[SPA 验收记录](../reference/admin-acceptance.md)。

