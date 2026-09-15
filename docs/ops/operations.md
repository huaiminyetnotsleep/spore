# Spore 运维手册

> 本手册覆盖 Spore **上线之后**的日常运维、升级回滚、备份恢复、卸载与故障排查。
> 首次安装与启动、反向代理、大文件说明见 [deployment.md](../guide/deployment.md)；
> 配置项完整说明见 [architecture.md](../reference/architecture.md)，常见问题见 [troubleshooting.md](./troubleshooting.md)。

## 1. 日常运维与健康检查

### 1.1 `spore` 运维命令（install-spore.sh）

一键安装脚本自带完整运维菜单，在任意目录运行均可（自动定位部署目录）。共有三种唤起方式：

| 唤起方式 | 命令 | 适用场景 |
| --- | --- | --- |
| ① 远程一行命令（首次部署） | `curl -fsSL https://raw.githubusercontent.com/huaiminyetnotsleep/spore/main/install-spore.sh \| bash` | 全新服务器，无需 clone 仓库；执行后直接打开交互菜单 |
| ② `spore` 命令（日常推荐） | `spore`（打开菜单）或 `spore status` 等子命令 | 首次安装/升级完成后，脚本自动把自身注册为系统命令（软链 `/usr/local/bin/spore`），此后服务器任意目录可用 |
| ③ 本地运行脚本 | `bash ~/spore/install-spore.sh [子命令]` | 与 ② 完全等价；软链被误删或不想用 `spore` 名字时使用 |

菜单共 13 项（安装、升级、升级后验证、状态、日志、查看访问密钥、重设密钥、清理临时文件、
磁盘与数据检查、重启、停止、卸载、退出）；子命令与菜单项一一对应，见下方速查表。

安装（或升级）完成时，脚本会把自身注册为系统的 **`spore` 命令**（软链
`/usr/local/bin/spore`）。之后在服务器任意目录：

- 输入 `spore` —— 打开交互菜单：

```text
请选择操作：
   1) 安装 Spore          8) 清理临时文件
   2) 升级 Spore          9) 磁盘与数据检查
   3) 升级后验证         10) 重启服务
   4) 查看状态           11) 停止服务
   5) 查看日志           12) 卸载 Spore
   6) 查看访问密钥       13) 退出
   7) 重设密钥
```

- 或使用子命令直接调用（便于放进 cron 或工单流程；`uninstall` 仍会交互确认，
  `clean-tmp` 会先停止服务）：

| 子命令 | 作用 | 等价的原始操作 |
| --- | --- | --- |
| `spore install` | 首次安装（含凭据交互、数据目录授权） | [deployment.md §4.2](../guide/deployment.md) 全流程 |
| `spore upgrade` | 拉新镜像并滚动更新，不动配置（同时刷新 spore 命令自身） | `docker compose pull && docker compose up -d` |
| `spore verify` | 升级后验证：探针 + 容器健康 + MTProto 通道登录 | 本节 §2.4 验证序列 |
| `spore status` | 容器状态 + `/healthz` 探活 | `docker compose ps` + curl |
| `spore logs` | 跟踪 bot 日志（Ctrl-C 返回） | `docker compose logs -f --tail 100 bot` |
| `spore show-key` | 从日志查首次访问密钥 | `docker compose logs bot \| grep -F '访问密钥'` |
| `spore reset-key` | 重置管理端访问密钥（新密钥只打印一次） | `docker compose exec bot spore admin reset-key`（见 §6.3） |
| `spore clean-tmp` | 清空临时媒体目录 `data/tmp`（先停机） | §6.5 磁盘满处理的第一步 |
| `spore diskcheck` | 磁盘容量、数据文件、`.env` 权限体检 | §3.1 数据边界的人工检查 |
| `spore restart` | 重启应用（未运行则直接启动） | `docker compose restart bot` |
| `spore stop` | 停止服务（保留容器与数据） | `docker compose stop` |
| `spore uninstall` | 卸载容器与 spore 命令（数据/配置按提示决定去留） | §5 卸载与停用 |
| `spore exit` | 退出脚本（等价菜单 13） | —（仅结束脚本自身） |

`spore` 命令等价于部署目录内的 `install-spore.sh`（如 `~/spore/install-spore.sh status`）。
软链被误删时恢复：`sudo ln -sf ~/spore/install-spore.sh /usr/local/bin/spore`。

脚本统一约定：部署目录默认 `~/spore`（`SPORE_DIR` 可覆盖）；所有交互输入读
`/dev/tty`，`curl | bash` 管道方式可用；涉及 `data/` 的读写自动经 sudo（数据目录
属主是容器 UID 10001）。备份与恢复不在脚本内：请使用管理端「备份 → 导出数据库」
（在线一致快照）与 [§4 备份与恢复](#4-备份与恢复) 的手工序列。

### 1.2 常用原始命令

| 操作 | 命令 |
| --- | --- |
| 查看服务 | `docker compose ps` |
| 查看应用日志 | `docker compose logs -f bot` |
| 重启应用 | `docker compose restart bot` |
| 停止服务 | `docker compose down` |
| 进入应用容器 | `docker compose exec bot sh` |
| 探活 | `curl -fsS https://<域名>/healthz` |
| 就绪检查 | `curl -fsS https://<域名>/readyz` |

`docker compose down` 不会删除 `data/` bind mount。宿主机反向代理的证书和配置由
代理自身管理，升级或重启 Spore 不应删除这些配置。

管理端总览页可以查看 Bot API、MTProto、数据库、临时目录、队列和 Worker 状态。
异常事件会进入 Web 事件中心；Bot 通道可用且已设置 owner 时，还会按冷却窗口通知
管理员 Telegram 私聊。

## 2. Docker Compose 更新操作手册（升级、验证与回滚）

本节适用于在部署目录（默认 `~/spore`）中运行 Docker Compose 的环境。先确认当前
使用的是 GHCR 镜像部署还是源码构建部署；两种方式的更新命令不同。普通镜像更新不需要
先执行 `docker compose down`：`pull` 只负责下载新镜像，随后必须执行 `up -d`，让
Compose 按新镜像重新创建需要更新的容器。

### 2.1 更新前准备

1. 在管理端执行 **备份 → 导出数据库**，下载 `.db` 文件并保存到受保护位置；如果启用了
   云盘下载，还应在 **云盘下载 → 配置备份与恢复** 中另行导出加密 ZIP。两类备份彼此独立：
   数据库备份不包含 `data/cloud-drive.json` 或网盘凭据，云盘配置包也不包含数据库、Session、
   `.env`、临时媒体、反向代理配置和证书。详细边界见 [§4.1](#_4-1-web-一键备份) 与
   [§7.5](#_7-5-云盘配置备份与恢复)。
2. 确认当前工作目录和 Compose 配置：

   ```bash
   cd ~/spore
   docker compose config
   ```

   确认 `image:`、`.env` 和数据目录仍指向当前生产环境，不要在更新前删除 `data/`。
3. 如果计划更新到指定版本而不是 `latest`，先记录当前镜像版本或完整 commit SHA，便于
   回滚。公开发布的 GHCR 镜像同时提供 `latest`、完整 commit SHA 和 `vX.Y.Z` 语义化版本标签；
   生产环境建议固定到完整 SHA 或正式版本标签，不要长期依赖 `latest`。

### 2.2 GHCR 镜像更新（推荐）

适用于 [deployment.md §4.2](../guide/deployment.md) 的 GHCR 镜像部署。在部署目录执行：

```bash
cd ~/spore
docker compose pull bot
docker compose up -d
```

如果启用了 `bigfile` profile（本地 Bot API 备选路线），使用 profile 拉取并启动：

```bash
docker compose --profile bigfile pull
docker compose --profile bigfile up -d
```

只更新应用服务时，`docker compose pull bot` 不会删除 `data/` bind mount，也不会删除
宿主机反向代理的证书和配置。若 Compose 文件或 `.env` 有变更，仍需执行 `up -d`，让
服务按新配置重建。

#### GHCR 拉取失败

当 GHCR 包可见性为 Public 时无需登录。如果 `docker compose pull` 报 `unauthorized`、`denied` 或 `403`：

1. 确认 Compose 文件中的镜像地址和 tag 正确；
2. 确认网络没有拦截 `ghcr.io`，并检查 GitHub Packages 状态；
3. 如果部署者所在组织把镜像设为私有，再使用具有最小 `read:packages` 权限的凭据登录：

   ```bash
   read -rs GHCR_TOKEN
   echo "$GHCR_TOKEN" | docker login ghcr.io -u <GitHub 用户名> --password-stdin
   unset GHCR_TOKEN
   docker compose pull bot
   docker compose up -d
   ```

不要把 Token 写在命令行参数、`.env`、文档或 Git 仓库中。`docker login` 必须由实际运行
Compose 的同一用户执行；如果使用 `sudo docker`，root 与普通用户的 Docker 凭据并不共用。

### 2.3 源码构建更新

适用于 [deployment.md §4.3](../guide/deployment.md) 的源码构建部署：

```bash
cd ~/spore
git pull
docker compose build bot
docker compose up -d
```

如果服务器工作树有本地修改，先处理或保存这些修改，再执行 `git pull`，避免产生合并
冲突。也可以在构建前用 `docker compose config` 检查展开后的 Compose 配置。

### 2.4 更新后验证

应用启动时会根据 SQLite 的 `PRAGMA user_version` 自动执行只增不改的数据库迁移。更新
完成后依次检查：

```bash
docker compose ps
docker compose logs --tail=200 bot
curl -fsS https://<域名>/healthz
curl -fsS https://<域名>/readyz
```

应满足以下条件：

- `docker compose ps` 中 `bot` 正常运行并通过健康检查；
- `/healthz` 返回成功，表示进程存活；
- `/readyz` 返回成功，表示数据库等依赖已就绪；
- 日志中出现 `Bot MTProto 会话有效，静默登录`。首次升级到需要重新建立 Bot
  MTProto 会话的版本时，可能出现 `Bot MTProto 会话登录成功`；该过程使用 `.env` 中的
  `BOT_TOKEN`，无需人工扫码；
- 发送一条媒体链接进行业务冒烟测试，并在管理端“转发记录”中确认投递方式符合预期
  （普通投递为「上传」，缓存频道复用命中为「复用」）。

首次部署或访问密钥被重置时，才需要按 [deployment.md §4.4](../guide/deployment.md) 的说明
从日志获取访问密钥；普通更新不需要重新保存密钥。

### 2.5 回滚到已知可用版本

如果新版本启动失败或业务验证不通过，先保留新版本的日志，再切回已知可用版本。

#### GHCR 镜像回滚

每次发布的镜像都有完整 commit SHA 标签。将 `docker-compose.yml` 中的 `image:` 临时
改为上一个可用版本，例如：

```yaml
image: ghcr.io/huaiminyetnotsleep/spore:<旧 commit SHA>
```

然后重新拉取并启动：

```bash
docker compose down
docker compose pull bot
docker compose up -d
```

`latest` 会随着 `main` 变化，不适合作为回滚时的固定版本。回滚完成后，按 [§2.4](#_2-4-更新后验证) 重新验证。

#### 源码构建回滚

```bash
docker compose down
git checkout <旧版本>
docker compose up -d --build
```

### 2.6 数据库版本不兼容时恢复

应用迁移是自动执行的。只要旧版本仍能读取已迁移的数据库，通常可以直接回滚；如果
旧版本拒绝打开更高版本的数据库，才需要使用升级前导出的数据库备份恢复：

```bash
docker compose stop bot
cp data/spore.db data/spore.db.before-restore
cp /受保护位置/spore-backup-YYYYMMDD-HHMMSS.db data/spore.db
rm -f data/spore.db-wal data/spore.db-shm
sudo chown 10001:10001 data/spore.db
chmod 600 data/spore.db
docker compose up -d bot
```

恢复后检查页面、用户、请求和审计记录，并按 [§2.4](#_2-4-更新后验证) 执行健康检查和业务
冒烟测试。删除旧 `-wal`/`-shm` 侧文件是为了避免替换后的主文件被过期 WAL 记录污染，
与应用启动时导入流程的处理一致。数据库备份不包含 `data/session.json`；如果 MTProto
Session 不可用，需要在管理端重新扫码登录。Bot 身份的 `data/bot-session.json` 不在
数据库备份内，但启动后会使用 `BOT_TOKEN` 自动重登。

## 3. 数据库、表结构与 `.env` 更新/迁移

本节说明业务数据、数据库表结构和运行环境变量的保存与迁移。当前 Compose 使用宿主机
目录挂载：

```yaml
./data:/app/data
```

因此数据库和 Session 不在容器可写层中。正常执行 `docker compose pull`、`docker compose
up -d` 或重建容器不会删除宿主机的 `data/`；不要在普通更新时执行 `docker compose down -v`
或删除 `data/`。

### 3.1 数据保存与备份边界

升级或迁移前，优先在管理端执行 **备份 → 导出数据库**，下载 `.db` 文件并保存到受保护
位置。服务使用 SQLite `VACUUM INTO` 生成一致性快照，比在服务运行中直接复制
`data/spore.db` 更安全。

数据库导出包含业务数据库中的用户、请求、用量、审计、事件、设置和 Web 会话数据，但不
包含以下文件或目录：

```text
data/session.json
data/bot-session.json
data/peers.json
data/cloud-drive.json
data/tmp/
bot-api-data/
.env
反向代理配置、证书和私钥
```

如果需要完整迁移，应将数据库、云盘配置、Session、`.env` 和反向代理配置作为不同类型的
敏感资料分别保存。云盘配置不要手工并入 `.db` 或数据库备份；优先使用管理端独立导出的
加密 ZIP，具体流程见 [§7.5](#_7-5-云盘配置备份与恢复)。数据库备份建议设置为仅所有者可读，
并使用加密存储和安全通道传输：

```bash
chmod 600 /受保护位置/spore-backup-YYYYMMDD-HHMMSS.db
shasum -a 256 /受保护位置/spore-backup-YYYYMMDD-HHMMSS.db
```

不要把数据库备份、Session 或 `.env` 提交 Git、放入公开网盘，或粘贴到日志、工单和截图。

### 3.2 数据库表结构自动更新

应用启动时会读取 SQLite 的 `PRAGMA user_version`，自动执行当前版本缺少的内嵌迁移。
迁移按版本递增，每个版本在独立事务中提交；已发布的迁移脚本不得修改，新的表结构变化
应随新版本应用一起发布。

同机升级时，GHCR 镜像部署执行：

```bash
cd ~/spore
docker compose pull bot
docker compose up -d
```

一键脚本部署的目录，重跑安装命令后在菜单选 **2 升级** 即可（不改任何配置，等价于
上面的 pull + up）：

```bash
curl -fsSL https://raw.githubusercontent.com/huaiminyetnotsleep/spore/main/install-spore.sh | bash
```

源码构建部署执行：

```bash
cd ~/spore
git pull
docker compose build bot
docker compose up -d
```

如果启用了 `bigfile` profile：

```bash
docker compose --profile bigfile pull
docker compose --profile bigfile up -d
```

升级后检查迁移日志和就绪状态：

```bash
docker compose ps
docker compose logs --tail=500 bot
curl -fsS https://<域名>/healthz
curl -fsS https://<域名>/readyz
```

不要让旧版本程序直接打开新版本已经升级过的数据库。如果数据库版本高于旧程序支持的
版本，旧程序会拒绝启动；此时应先切回兼容版本，或恢复升级前的数据库备份。

### 3.3 `.env` 环境变量更新

`.env` 不属于数据库，也不会随镜像更新自动改变。更新前先保存旧配置到受保护位置，
然后手工编辑当前 `.env`；不要直接执行 `cp .env.example .env`，否则可能覆盖生产凭据：

```bash
cd ~/spore
cp .env /受保护位置/spore.env.before-update
chmod 600 /受保护位置/spore.env.before-update
chmod 600 .env
```

新版本新增或重命名变量时，应对照同版本的 `.env.example` 或发布说明，逐项合并变量。不要
将 PAT、`BOT_TOKEN`、`TG_API_HASH`、OAuth Secret 或加密密钥写进命令参数、Git、日志或文档。

编辑完成后只验证配置，不要把包含敏感值的完整渲染结果打印或保存到共享位置：

```bash
docker compose config --quiet
```

应用新的环境变量必须重新创建容器：

```bash
docker compose up -d --force-recreate bot
```

仅执行 `docker compose restart bot` 不应作为环境变量变更后的标准流程。如果修改的是
`bot-api` 或其他 profile 服务，应使用：

```bash
docker compose --profile bigfile up -d --force-recreate
```

注意，部分运行设置已持久化到数据库，可能优先于环境变量。修改 `.env` 后如果管理端设置
仍覆盖该值，应同时在管理端“运行设置”中调整并重新验证。

几个需要特别注意的变量：

- `BOT_TOKEN`、`TG_API_ID`、`TG_API_HASH` 或手机号配置变化后，要检查 Bot/MTProto 登录状态；
- `ALLOWED_USER_IDS` 主要用于空数据库的首次兼容性导入，数据库已有用户后，修改它不会自动
  更新数据库白名单；
- `DATA_DIR` 会同时改变数据库、Session、Peer 缓存和临时目录位置，不是普通变量替换，
  必须按停机迁移流程处理；
- `GITHUB_CLIENT_ID` 和 `GITHUB_CLIENT_SECRET` 应同时配置或同时留空；
- 更换 `WEB_OAUTH_ENCRYPTION_KEY` 可能导致旧的加密 OAuth Secret 无法解密，必须先确认
  旧密钥仍可保留或已完成凭据轮换。

### 3.4 数据库与 `.env` 同时更新

当一个版本同时包含表结构和环境变量变化时，按以下顺序执行：

```bash
cd ~/spore

# 1. 管理端：备份 → 导出数据库，并将文件保存到受保护位置

# 2. 保存当前 .env，再手工合并新变量
cp .env /受保护位置/spore.env.before-update
chmod 600 /受保护位置/spore.env.before-update
# 编辑 .env
chmod 600 .env

# 3. 验证 Compose 配置（不输出展开后的 Secret）
docker compose config --quiet

# 4. 拉取新镜像；源码部署则先 git pull 并 build
# GHCR：
docker compose pull bot

# 5. 重建容器并触发数据库自动迁移
docker compose up -d --force-recreate

# 6. 检查状态、日志和就绪探针
docker compose ps
docker compose logs --tail=500 bot
curl -fsS https://<域名>/healthz
curl -fsS https://<域名>/readyz
```

只修改 `.env` 时不需要重新拉取镜像，但仍应执行 `docker compose up -d --force-recreate`。
只修改数据库表结构时，必须使用包含迁移代码的新应用镜像或新构建产物。

### 3.5 跨服务器数据迁移

推荐使用“数据库导出/导入 + 配置单独迁移”，不要直接把整个部署目录当作业务数据库备份。

#### 源服务器

1. 在管理端执行 **备份 → 导出数据库**；
2. 将 `.db` 文件保存到受保护位置，并记录校验和；
3. 若已配置云盘目的地，在 **云盘下载 → 配置备份与恢复** 中设置专用备份密码并导出
   加密 ZIP；把 ZIP 与密码分开保管，密码遗忘后无法恢复；
4. 如需保留 MTProto 登录状态，在停止服务后将 `data/session.json`、
   `data/bot-session.json` 和 `data/peers.json` 作为独立高敏文件保存；
5. 若启用了 `bigfile` profile，另行保存 `bot-api-data/`。

#### 目标服务器

先按 [deployment.md](../guide/deployment.md) 准备 `docker-compose.yml` 和 `.env`。`.env` 应通过
Secret 管理或安全通道单独配置，不要放进数据库备份：

```bash
cd ~/spore
mkdir -p data
sudo chown -R 10001:10001 data
chmod 700 data
chmod 600 .env
docker compose config --quiet
docker compose pull bot
docker compose up -d
```

目标服务正常启动后，在管理端打开“备份”，上传源服务器导出的 `.db`，完成完整性、版本和
必要表结构校验，并执行二次确认。导入标记会在下一次启动时应用：

```bash
docker compose restart bot
```

数据库应用并重新登录管理端后，再打开 **云盘下载 → 配置备份与恢复**，上传源服务器导出的
加密 ZIP 并输入原备份密码。上传阶段只验证候选，不修改当前配置；核对候选目的地名称和创建
时间后执行二次确认，确认后整体替换云盘配置并在线立即生效，无需再次重启。已经开始的任务
继续使用旧配置，新建任务使用恢复后的配置。

数据库导入只替换业务数据库，不会导入源服务器的云盘配置、`.env`、Session、Peer 缓存或
反向代理配置。目标服务器的运行设置和访问密钥按数据库导入规则保留，Web 会话会被清空，
因此导入后需要重新登录管理端；MTProto Session 未迁移时需要重新扫码。不要让源服务器和
目标服务器同时使用同一账号处理生产流量，避免重复消费或 Session 冲突。

如果无法使用管理端，只能停机后直接替换数据库：

```bash
cd ~/spore
docker compose stop bot
cp data/spore.db data/spore.db.before-restore
cp /受保护位置/spore-backup-YYYYMMDD-HHMMSS.db data/spore.db
rm -f data/spore.db-wal data/spore.db-shm
sudo chown 10001:10001 data/spore.db
chmod 600 data/spore.db
docker compose up -d bot
```

直接替换后按 [§3.2](#_3-2-数据库表结构自动更新) 和 [§3.6](#_3-6-迁移后的验证) 验证。若迁移
的是低于当前程序版本的数据库，应用会在启动时补齐缺少的迁移；高于当前程序支持版本的
数据库会被拒绝。

### 3.6 迁移后的验证

```bash
docker compose ps
docker compose logs --tail=500 bot
curl -fsS https://<域名>/healthz
curl -fsS https://<域名>/readyz
```

确认以下内容：

- `bot` 正常运行并通过健康检查；
- 数据库迁移没有报错，`/readyz` 返回成功；
- 管理端可以登录，用户、请求、用量和审计记录完整；
- owner、白名单和运行设置符合预期；
- MTProto 状态正常；
- 发送一条测试链接，并检查转发记录和投递方式。

### 3.7 回滚与数据库恢复

环境变量变更有问题时，恢复之前保存的 `.env` 并强制重建：

```bash
cd ~/spore
cp /受保护位置/spore.env.before-update .env
chmod 600 .env
docker compose up -d --force-recreate bot
```

应用版本有问题时，按 [§2.5](#_2-5-回滚到已知可用版本) 固定到已知可用的完整 commit SHA
并回滚。若旧版本不能读取已升级的数据库，恢复升级前导出的数据库：

```bash
docker compose stop bot
cp data/spore.db data/spore.db.before-restore
cp /受保护位置/spore-backup-YYYYMMDD-HHMMSS.db data/spore.db
rm -f data/spore.db-wal data/spore.db-shm
sudo chown 10001:10001 data/spore.db
chmod 600 data/spore.db
docker compose up -d bot
```

恢复后按 [§3.6](#_3-6-迁移后的验证) 验证。数据库恢复不会恢复 `data/session.json`；Session
不可用时需要重新扫码。若环境变量中更换过 Bot Token，还应在确认新 Token 工作后按需撤销
旧 Token。

## 4. 备份与恢复

### 4.1 Web 一键备份

在管理端打开 **备份**，点击导出。服务使用 SQLite `VACUUM INTO` 生成一致快照，
流式下载完成后删除服务器临时文件；导出动作写入审计，并在页面记录最近备份时间。
备份只包含业务数据库，不包含 Session、Peer 缓存、临时媒体、`.env` 或宿主机反向
代理的证书、私钥与配置。`data/bot-session.json`（Bot 身份 MTProto 会话）同样不在
备份内，但恢复后下次启动会用 `BOT_TOKEN` 自动重登，无需任何人工操作。

管理端也支持从“备份”页面导入 Spore 导出的 `.db` 文件。上传后先执行 SQLite 完整性、
版本和必要表结构校验，校验通过仍需管理员二次确认；确认只写入 `data/pending-import.json`
标记，下一次启动才会应用，不会在线替换数据库。应用时保留当前访问密钥、GitHub 配置和
运行时设置，清空 Web 会话并保留 `data/session.json`、`data/peers.json` 不变；旧数据库
会留在带时间戳的 `spore.db.rollback-*` 文件中。没有 confirmed marker 的普通重启不会触发导入。

SQLite 备份与云盘配置备份不是同一个包：`.db` 中没有 `data/cloud-drive.json` 或网盘凭据。
需要保护或迁移云盘配置时，必须另行执行 [§7.5](#_7-5-云盘配置备份与恢复) 的加密 ZIP 导出。

不要把下面的 tar 命令当作业务备份方案：它会把 Session 或 `.env` 一并复制，超出
Web 备份边界。需要迁移服务器时，应分别按 Secret 管理规范配置 `.env`，按本节恢复
业务数据库，并重新扫码登录。

### 4.2 恢复数据库

恢复前停止 bot，保留原数据库作为回滚副本：

```bash
docker compose stop bot
cp data/spore.db data/spore.db.before-restore
cp /受保护位置/spore-backup-YYYYMMDD-HHMMSS.db data/spore.db
rm -f data/spore.db-wal data/spore.db-shm
sudo chown 10001:10001 data/spore.db
chmod 600 data/spore.db
docker compose up -d bot
```

注意区分两类恢复的生效方式：数据库替换在**下次启动时**生效（管理端导入确认同样只写
标记、下次启动应用）；云盘配置恢复则是**在线立即生效**、无需重启。数据库恢复不包含
云盘配置，如需恢复目的地与凭据，按 [§7.5](#_7-5-云盘配置备份与恢复) 单独执行。

恢复后验证页面、用户、请求和审计记录。恢复文件只还原业务数据，不会恢复
`data/session.json` 或 `data/peers.json`；若是新服务器或 Session 不可用，登录管理端
的 MTProto 页面重新扫码（Bot 身份会话 `data/bot-session.json` 用 Token 自动重登，无需处理）。必要时可删除失效的 `data/session.json` 后重试，Peer 缓存
会按需重建。

恢复演练应在隔离目录或临时服务器进行：导出数据库 → 停止服务 → 替换数据库 → 启动
并确认记录可读 → 重新扫码 → 发送一条测试链接。演练不得使用生产 `.env` 内容作为
文档或测试夹具。

## 5. 卸载与停用

### 5.1 临时停用（保留数据）

只是暂时下线（例如换服务器、暂停运营），保留全部数据以便日后恢复：

```bash
docker compose down          # 停止并移除容器，保留 data/ 与镜像
# 或完全保留容器定义，仅停止：
docker compose stop
```

`data/` 目录（数据库、Session、Peer 缓存）与 `bot-api-data/` 原样保留，重新
`docker compose up -d` 即可继续使用，无需重新扫码。停用期间建议保持 `.env` 与
`data/` 的 600/最小权限设置。

### 5.2 彻底卸载（清除服务器数据）

卸载前确认不再需要任何数据。若还想保留运营记录，先按 §4.1 从 Web 导出数据库，
或直接复制整个 `data/` 到受保护位置；然后再执行：

```bash
# bigfile profile 在用时先连它一起停：
docker compose --profile bigfile down

# 删除服务器上构建/拉取的镜像（可选）：
docker compose --profile bigfile down --rmi local
docker image rm ghcr.io/huaiminyetnotsleep/spore:latest 2>/dev/null || true

# 删除代码与数据目录（含数据库、Session、Peer 缓存、临时媒体、.env）：
cd ..
rm -rf spore/
```

同时清理宿主机反向代理中该域名的虚拟主机配置与证书（按 [deployment.md](../guide/deployment.md)
§3 的 Caddy/Nginx 示例反向操作），DNS 记录按需删除。

### 5.3 安全注意事项

- `data/session.json` 等效 MTProto 账号控制权：目录删除后凭据虽随文件销毁，
  但该登录会话仍显示为活跃设备。卸载后请在 Telegram 官方客户端
  **设置 → 设备 → 终止会话** 中结束对应会话；
- `.env` 含 Bot Token 与 Telegram API 凭据：确认已随目录删除；Bot Token 不再
  使用时到 @BotFather 执行 revoke；
- `data/spore.db` 内含用户白名单、请求元数据与审计记录：如属他人个人信息，
  按 §8 数据最小化原则在卸载时一并删除，不随镜像或备份残留在服务器上。

## 6. 故障排查

### 6.1 宿主机反向代理无法启动或证书申请失败

```bash
docker compose ps
docker compose logs --tail=200 bot
```

检查管理端域名是否解析到这台服务器、代理自身配置是否正确、80/443 是否被其他程序
占用，以及防火墙是否放行。Caddy、Nginx 或其他代理必须能从公网接收 ACME 挑战；仅在
内网或没有 DNS 解析的环境中不能完成自动公认证书申请。

### 6.2 bot 不健康或反向代理返回 502

```bash
docker compose logs --tail=200 bot
curl -fsS "http://127.0.0.1:${WEB_HOST_PORT:-8080}/healthz"
docker compose exec bot wget -qO- http://127.0.0.1:8080/healthz
```

检查必填配置、`data/` 权限以及容器是否反复重启。宿主机那条 curl 用的是发布端口
`WEB_HOST_PORT`（默认 8080），容器内那条恒为 8080。正式 Compose 将 bot 只发布到宿主
回环 `127.0.0.1:<WEB_HOST_PORT>`；宿主机代理应反代到该地址，公网请求则通过代理的
HTTPS 地址检查。

### 6.3 登录密钥丢失

```bash
docker compose exec bot spore admin reset-key
```

或使用一键脚本菜单「7) 重设密钥」（`install-spore.sh reset-key`）。

只保存命令输出的新密钥。不要直接修改数据库中的哈希，也不要将旧密钥写入 URL。

### 6.4 MTProto 未就绪或会话失效

在管理端打开 MTProto 页面触发重新登录，观察：

```bash
docker compose logs -f bot
```

确认 `data/session.json` 所属用户为 10001 且权限足够。会话失效时 Web 仍应可访问；
完成扫码后，Bot 和 Worker 会随 MTProto ready 状态重新启动。来源频道必须由该用户账号
可访问，私有链接还需要账号是频道成员。

### 6.5 数据库未就绪或写入失败

查看 `/readyz`、bot 日志和 `data/` 权限。不要在运行中复制 SQLite 主文件作为备份，
使用管理端导出以获得一致快照。若磁盘满，先处理 `data/tmp/` 中确认的孤儿临时文件
（一键脚本菜单「8) 清理临时文件」会自动停机清空再启动），再检查数据库和宿主机反向
代理的证书/配置存储空间；`install-spore.sh diskcheck` 可快速查看容量与数据文件分布。

### 6.6 大文件发送失败（超过 50MB 的媒体）

大文件经 Bot 身份 MTProto 会话直传，依赖第二个 MTProto 会话就绪。检查日志：

```bash
docker compose logs --tail=200 bot | grep -i "Bot MTProto"
```

- 用户收到 `LARGE_CHANNEL_UNAVAILABLE`（大文件发送通道暂不可用）：Bot 会话在
  自动重连期间不可用，仅超过 Bot API 上限的媒体失败，小文件不受影响；反复出现则检查
  `BOT_TOKEN` 是否有效（能否正常长轮询）、服务器到 Telegram 的网络。
- 发送失败 `BOT_SEND_FAILED` / `TELEGRAM_RATE_LIMIT`：查看同条日志的 `error`
  字段；429 限流稍后重试即可。
- `data/bot-session.json` 损坏或误删：无需处理，下次启动自动重登。
- 选择了本地 Bot API 服务器路线（`BOT_API_URL`）时：确认启用了 `--profile bigfile`、
  `bot-api` 为 running 且 `MAX_FILE_SIZE` 不超过实际服务端能力；不要把服务名写成
  `localhost`。

## 7. 云盘下载（/download）

`/download` 指令把提取的媒体直接上传到管理员配置的网盘（本期实测 MEGA，经 rclone），
不再重发回 Telegram——为授权用户提供不经 Bot 发送通道的网盘获取方式。注意这**不是**
绕过文件大小上限：单文件仍受 `MAX_FILE_SIZE`（默认 2000 MiB）约束，云盘路径只是把
结果送往网盘而不回传 Telegram。裸链接行为不受任何影响。完整的下载原理、处理流程、
rclone/MEGA 官方参考和后续目的地规划见 [下载功能说明](../guide/download.md)。

### 7.1 功能开关与边界

- 全局开关 `enabled` 默认**关闭**：关闭时授权用户 `/download` 收「云盘下载功能未开启」；
  配置存于 `data/cloud-drive.json`（0600），管理端「云盘下载」页编辑；
- 准入、频率、配额、并发与裸链接完全一致；未授权用户得到与陌生链接相同的申请引导，
  不暴露功能存在；
- 媒体字节不落 VPS 持久盘（沿用内存/临时文件管道，传完即删）；网盘凭据只存
  `data/cloud-drive.json`，不进数据库，也**不在数据库备份内**（迁移服务器需按
  [§3.5](#_3-5-跨服务器数据迁移) 单独安全转移该文件）；
- 上传布局 `{path_prefix}/{频道名}/{YYYY-MM-DD}/`：相册或带配文以消息 ID 命名文件夹
  （组内顺序 `01_`/`02_` 前缀，配文写 `caption.txt`），单媒体无配文直接平铺；
- 用户侧用法：`/download <t.me链接>`（默认目的地）或 `/download <目的地名> <t.me链接>`
  （名称错误会返回可用目的地列表）。

### 7.2 目的地配置（以 MEGA 为例）

配置文件格式、管理端页面字段、常用网盘 `options` 参数对照与逐步配置操作，以
[下载功能说明 §5](../guide/download.md#_5-在-spore-中配置-mega)为唯一详细说明；
配置文件的持久化位置与敏感边界见[配置参考 §6](../reference/configuration.md#_6-云盘配置文件-cloud-drive-json)。此处只保留运维要点：

- 注册建议：MEGA 免费账号注册即用（免费额度与是否需要付款方式以 MEGA 官方页面为准，
  额度与政策会变化，不构成项目保证）；建议为 Spore 单独注册账号，不与个人主力账号
  混用。
- 手动编辑 `data/cloud-drive.json` 时，`pass` 必须填 `rclone obscure` 混淆后的值；
  管理端页面填写原始密码即可，服务端会在保存前自动混淆（Docker 镜像已内置 rclone）：

```bash
docker compose exec bot rclone obscure '网盘密码'
```

- 开启两步验证（2FA）的账号需在 options 中额外提供 `2fa` 参数。
- 除 MEGA 外，`s3`、`drive`、`onedrive`、`webdav` 等类型在结构上会把 `options` 传给
  rclone，但本项目只在 MEGA 上完成过配置与真机验证；启用其他后端前先用管理端「测试」
  确认连通，认证与限额差异以 rclone 官方后端文档为准。本地目的地、OpenList/AList 等
  扩展路线见[下载功能说明 §8](../guide/download.md#_8-后续扩展-todo)。

### 7.3 限流注意

MEGA 对连续快速的管理类调用会触发封禁（官方文档举例连续约 90 次即触发，解封周期以
MEGA 官方说明为准）；**上传会话不受影响**。Spore 的只读连通性探测（`lsd`）只在
管理端「测试」按钮显式触发；保存配置时只做结构校验与 MEGA 新密码的混淆处理，**不执行**
连通性探测——测试按需点击，不要高频测试或反复保存。

### 7.4 取消与补存

- **取消**：用户 `/cancel <链接>` 与管理端请求取消对云盘任务同样生效（排队中与上传中
  均可）——终止 rclone 子进程、清理媒体句柄、best-effort 删除网盘残件，请求标记
  cancelled。
- **补存**：请求列表任意**终态**请求（正常转发、失败、取消均可）可点「存到网盘」
  选择目的地（默认 `default_destination`），按原链接新建云盘下载请求并上传网盘；
  若同用户、同链接与目的地已有成功云盘记录且远端核验通过，会复用已上传结果而不是
  重复下载上传。补存会新建一条「网盘」请求行并以「补存自 #id」关联原请求，管理端
  动作绕过用户配额与去重窗口。支持多选批量（单次上限 100 条），逐条返回创建/跳过
  及原因；源消息已删除时补存明确失败；队列满时对应行标记 `QUEUE_FULL`，可经现有
  重试入口重试。

### 7.5 云盘配置备份与恢复

云盘配置备份用于保护 `data/cloud-drive.json` 中的目的地定义和真实凭据，独立于 SQLite
数据库备份。导出文件是格式版本 1 的加密 ZIP，固定包含 `manifest.json` 与
`cloud-drive.json.enc`：完整配置使用管理员输入的密码经 Argon2id 派生密钥，再以
AES-256-GCM 加密；manifest 只含算法参数、创建时间、应用版本、加密载荷 hash/大小和目的地
名称摘要，不含 options 值。

#### 导出与密码保管

1. 打开管理端 **云盘下载 → 配置备份与恢复**；
2. 点击导出，输入两次相同的高强度备份密码；
3. 下载加密 ZIP 后，核对文件已完整保存，再关闭弹窗；
4. 将 ZIP 放入受保护的备份存储，将密码保存到密码管理器，并与 ZIP 分开保管；
5. 在隔离环境定期执行一次上传验证与取消候选演练。

备份密码只在本次请求内存中使用，Spore 不记录、不保存，也不写入日志或审计。忘记密码后
无法解密或恢复该备份，服务端没有找回、重置或绕过机制。不要把密码放在文件名、命令行、
工单、聊天、截图或与 ZIP 相同的公开网盘目录中。ZIP 虽已加密，仍应按高敏凭据备份管理。

#### 上传验证、确认与取消

1. 在目标实例同一页面选择加密 ZIP，并输入创建它时的备份密码；
2. 点击上传。服务端会检查固定 entry、格式版本、路径安全、解压限制、载荷 SHA-256、密码
   与完整 Cloud Drive Config；**此阶段只生成待确认候选，不应用配置**；
3. 核对页面显示的 `format_version`、`created_at`、`destination_names` 与哈希摘要；该
   SHA-256 是**加密载荷**（加密后的配置 payload）的摘要，不是整个 ZIP 包的摘要；
4. 如果文件或摘要不符合预期，点击“取消候选”。这只删除候选，不影响当前配置和 rollback；
5. 确认无误后执行二次确认。系统先保存当前配置为唯一的 rollback-latest，再整体替换
   `data/cloud-drive.json`，并在线发布新的运行快照。

确认恢复是**全量替换**而不是合并：备份中不存在的当前目的地会被删除。恢复后无需重启；
已经开始的上传任务继续使用启动时取得的旧目的地配置，新建任务使用恢复后的配置。确认后
建议立即回到目的地列表检查全局开关、默认目的地和目的地名称，并按需对关键目的地执行一次
只读连通性测试，避免高频测试触发网盘限制。

#### rollback-latest

每次确认恢复前，系统自动保存当时的完整配置为 `cloud-drive.json.rollback-latest`，只保留
最近一份。在管理端点击回滚并输入固定确认值后，可整体恢复上一份配置并立即生效；任务的新旧
快照切换规则与确认恢复相同。回滚不是多版本历史，也不能代替定期导出。若只是上传了错误包且
尚未确认，使用“取消候选”，不要执行回滚。

#### Docker 数据持久化与敏感边界

云盘当前配置、已验证候选、候选 marker 和 rollback-latest 都位于 `/app/data` 对应的宿主机
`./data` bind mount 中，并以 `0600` 保存。正常执行 `docker compose pull`、`docker compose
up -d`、容器重建或 `docker compose down` 不会清除它们；删除宿主机 `data/`、改变挂载到空
目录或执行会删除卷/数据的操作则会丢失这些状态。迁移前应先通过 Web 导出加密 ZIP，不要只
依赖容器可写层或手工复制未加密的 `cloud-drive.json`。

服务端为了让“上传验证”和后续“确认”分成两个请求，会使用 Web 敏感配置根密钥派生的独立
密钥加密候选；不会保存用户的备份密码。`WEB_OAUTH_ENCRYPTION_KEY` 不可用时，候选相关端点
会受控返回不可用，不能通过把密码写入配置或磁盘来规避。所有相关日志和审计只允许记录格式
版本、包 hash/大小、目的地名称/数量、动作和结果，禁止记录备份密码或 `options` 值。

#### 备份恢复故障排查

| 现象 | 处理 |
| --- | --- |
| 提示密码错误或备份包无效 | 确认使用导出时的原密码、文件未被截断或修改；出于安全原因，服务端不会区分错误密码与认证失败的加密载荷 |
| 上传被拒绝 | 确认文件为 Spore 导出的 v1 ZIP、大小不超过 4 MiB，且没有重新打包、增加 entry 或修改 manifest |
| 上传成功但当前配置没变化 | 这是预期的两阶段流程；上传只验证候选，必须核对摘要后再确认 |
| 确认后某目的地消失 | 恢复是整体替换，不是合并；如需撤销，使用 rollback-latest，之后重新导出正确备份 |
| 候选端点返回 503 | 检查 `WEB_OAUTH_ENCRYPTION_KEY` 是否按当前部署要求可用，修复后重建容器；不要轮换或删除仍需解密现有候选的根密钥 |
| 页面显示无可回滚配置 | 尚未成功确认过恢复、rollback 文件已丢失，或 `data/` 挂载/权限异常；回滚只保留最近一份 |
| 容器重建后候选或 rollback 消失 | 检查 Compose 是否仍为 `./data:/app/data`、是否切换了部署目录或挂载到新的空目录 |
| 恢复后新任务失败、旧任务正常 | 新任务已使用新配置；检查恢复后的默认目的地、凭据和开关，并执行一次按需连通性测试 |

排查时可以查看受控动作结果和加密载荷 hash，但不要把 ZIP 解密后的 JSON、密码、完整 options、
rclone 环境变量或凭据相关 stderr 粘贴到日志、工单或聊天中。

### 7.6 失败排查

- **事件页**：`cloud.upload_failed`（云盘任务终态失败，连续失败达到阈值即触发管理员
  通知——当前固定为 3 次，没有对应环境变量可调，成功后计数清零）、
  `cloud.config_invalid`（配置文件损坏或 default 目的地悬空）、`cloud.disabled`
  （开关开启但 rclone 不可用，启动探测与每 10 分钟复查，恢复后自动解决）；
- **请求详情**：「云盘上传」区块逐文件记录目的地/远端路径/状态/字节数/错误码；
- 常见错误码与处理：

| 错误码 | 处理 |
| --- | --- |
| `CLOUD_AUTH_FAILED` | 网盘账号验证失败：核对 `user`，用 `rclone obscure` 重新生成 `pass`；2FA 账号检查 `2fa` 参数 |
| `CLOUD_QUOTA` | 网盘空间不足：清理网盘或扩容 |
| `CLOUD_NETWORK` | 网盘网络异常：稍后重试；持续出现时检查 VPS 出网 |
| `CLOUD_UPLOAD_FAILED` | 兜底失败：查看应用日志中 rclone 原始 stderr（已脱敏，不含凭据） |
| `CLOUD_UPLOAD_TIMEOUT` | 云盘上传超过任务时限：稍后重试；反复出现时检查网盘服务状态与网络 |
| `CLOUD_VERIFY_FAILED` | 同链接已上传结果的远端核验暂时不可用：稍后重试；核验失败不会盲目重传覆盖远端文件 |

- **rclone 不可用**：Docker 镜像内置固定版本（`docker compose exec bot rclone version`
  可验证）；`.env` 的 `RCLONE_BIN` 可覆盖二进制路径（本地开发 `brew install rclone`）。
  启动探测失败且开关开启时功能自动禁用，并产生 `cloud.disabled` 事件。

## 8. 安全清单

- [ ] `.env` 权限为 600，未提交 Git，未写入备份或截图；
- [ ] `data/`、`session.json` 和 `peers.json` 权限最小化，Session 不经 Web 下载；
- [ ] Docker 镜像以非 root UID 10001 运行；
- [ ] bot Web 端口仅发布到宿主回环 `127.0.0.1:<WEB_HOST_PORT>`（默认 8080），公网只经宿主机反向代理的 80/443；
- [ ] 宿主机反向代理的证书、私钥和配置已持久化且不公开下载；
- [ ] 管理端只使用 HTTPS，访问密钥存入密码管理器，不复用 Telegram 凭据；
- [ ] GitHub OAuth 回调仅使用 HTTPS 域名，绑定唯一管理员账号；
- [ ] 防火墙只开放 SSH、80、443，SSH 使用密钥并限制来源；
- [ ] 定期从 Web 导出数据库，并在隔离环境演练恢复；
- [ ] 恢复或回滚后重新扫码登录 MTProto，并验证 owner、白名单和审计记录；
- [ ] 业务数据只保存请求元数据，不保存消息正文、Caption 或媒体本体；
- [ ] 云盘凭据（`data/cloud-drive.json`）权限最小化（600），不进 Git、数据库备份或截图；
- [ ] 云盘配置使用独立加密 ZIP 定期导出，备份密码保存在密码管理器并与 ZIP 分开；已演练上传验证、取消候选与 rollback；
- [ ] 运营人员确认对处理内容拥有合法授权，系统不会绕过 MTProto 账号本身的访问权限。

## 9. 管理端 SPA 切换（已归档）

SSR → SPA 切换已完成并归档：SSR 认证页代码已删除（登录页与 CSV/二维码/OAuth 端点
保留），`/static/admin.js` 返回 404、认证 POST 表单路由不再存在，属预期行为。当前为
SPA-only：旧页面路径（如 `/users`、`/settings`）没有 302 兼容跳转，未匹配路径返回
404，不回退 SPA HTML。

SPA/Compose/e2e 的边界契约与历史验收结论统一见
[admin-acceptance.md](../reference/admin-acceptance.md)；日常发布冒烟按 [§2.4](#_2-4-更新后验证)
执行即可。
