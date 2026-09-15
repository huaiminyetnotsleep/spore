#!/usr/bin/env bash
# Spore 一键部署管理脚本 —— 菜单式安装 / 升级 / 卸载 / 运维（Docker Compose 部署）：
#
#   curl -fsSL https://raw.githubusercontent.com/huaiminyetnotsleep/spore/main/install-spore.sh | bash
#
# 菜单项：安装 / 升级 / 升级后验证 / 查看状态 / 查看日志 / 查看访问密钥 / 重设密钥 /
# 清理临时文件 / 磁盘与数据检查 / 重启服务 / 停止服务 / 卸载 / 退出。
# 也支持子命令直接调用（便于脚本化）：install | upgrade | verify | status | logs | show-key |
# reset-key | clean-tmp | diskcheck | restart | stop | uninstall | exit
#
# 可选环境变量：
#   SPORE_DIR      部署目录，默认 ~/spore（管道形式：curl ... | SPORE_DIR=/opt/spore bash）
#   SPORE_RAW_BASE 配置文件下载基址，默认仓库 main 分支 raw 地址（fork 或镜像加速时覆盖）
#
# 安装流程含交互：Telegram 凭据（可留空跳过，稍后补进 .env）、宿主端口（默认 8080，
# 实时探测占用）以及是否立即启动（可选择只完成配置、稍后手动启动）。
# 首次扫码登录是固有人工环节：安装后在管理端「MTProto」页面完成，
# 见 docs/guide/deployment.md §4.4。
set -euo pipefail

REPO_RAW="${SPORE_RAW_BASE:-https://raw.githubusercontent.com/huaiminyetnotsleep/spore/main}"
SPORE_DIR="${SPORE_DIR:-$HOME/spore}"
CONTAINER_UID=10001
CONTAINER_NAME=spore
IMAGE="ghcr.io/huaiminyetnotsleep/spore:latest"
HEALTH_TIMEOUT=90

info() { printf '==> %s\n' "$*"; }
warn() { printf '[!] %s\n' "$*" >&2; }
die()  { printf '[x] %s\n' "$*" >&2; exit 1; }

trap 'warn "脚本在第 ${LINENO} 行执行失败，请携带上方完整输出排查"' ERR

# ---------- 交互辅助：curl|bash 下 stdin 被脚本本身占用，一律读 /dev/tty ----------
TTY_FD=""
# 用分组包住再重定向：exec 重定向失败发生在 2>/dev/null 生效前，直接写在同一列表压不住报错
if { exec 3<>/dev/tty; } 2>/dev/null; then
  TTY_FD=3
fi
have_tty() { [ -n "$TTY_FD" ]; }

# read_input：读一行到全局 REPLY（优先 /dev/tty，其次普通终端 stdin；两者皆无则失败）
read_input() {
  if have_tty; then
    IFS= read -r REPLY <&"$TTY_FD" || return 1
  elif [ -t 0 ]; then
    IFS= read -r REPLY || return 1
  else
    return 1
  fi
}

# prompt_value <提示> <校验正则> <格式错误提示> [允许留空跳过]：结果写入全局 REPLY；
# 第 4 参数非空时空输入直接返回 1（表示跳过），否则空输入重试
prompt_value() {
  local pattern="$2" err_msg="$3" allow_empty="${4:-}"
  while true; do
    printf '%s: ' "$1"
    read_input || die "读取输入失败"
    if [ -z "$REPLY" ]; then
      if [ -n "$allow_empty" ]; then
        return 1
      fi
      printf '[!] 不能为空\n'
      continue
    fi
    if [[ ! $REPLY =~ $pattern ]]; then
      printf '[!] %s\n' "$err_msg"
      continue
    fi
    return 0
  done
}

prompt_yesno() {
  while true; do
    printf '%s [y/N]: ' "$1"
    read_input || return 1
    case "$REPLY" in
      [yY] | [yY][eE][sS]) return 0 ;;
      '' | [nN] | [nN][oO]) return 1 ;;
    esac
  done
}

# ---------- 凭据校验：除格式外还要排除 .env.example 的占位样例值 ----------
valid_token() {
  local re='^[0-9]+:[A-Za-z0-9_-]+$'
  [[ $1 =~ $re ]] && [ "$1" != "1234567890:replace-me" ]
}
valid_api_id() {
  local re='^[0-9]+$'
  [[ $1 =~ $re ]] && [ "$1" != "1234567" ]
}
valid_api_hash() {
  local re='^[0-9a-fA-F]{32}$'
  [[ $1 =~ $re ]] && [ "$1" != "0123456789abcdef0123456789abcdef" ]
}

# ---------- 工具函数 ----------
# fetch <url> <dest>：curl 优先、wget 回退
fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 -o "$2" "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -t 3 -O "$2" "$1"
  else
    die "下载配置文件需要 curl 或 wget 之一，请先安装"
  fi
}

# http_ok <url>：GET 探活
http_ok() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsS -o /dev/null --max-time 3 "$1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -T 3 -O /dev/null "$1"
  else
    return 1
  fi
}

# as_root <命令...>：data/ 目录属主是容器 UID 10001，宿主用户读写需经 sudo
as_root() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  else
    sudo "$@"
  fi
}

# env_get <KEY>：读取 .env 中 KEY 的值（取最后一次赋值）；需已 cd 到部署目录
env_get() {
  sed -n "s/^$1=//p" .env | tail -n 1
}

# set_env <KEY> <value>：经临时文件整行回写，避免 sed -i 在 GNU/BSD 间的参数差异
# （调用前已用正则约束 value 字符集，不含 |、& 等 sed 元字符）
set_env() {
  sed "s|^$1=.*|$1=$2|" .env > .env.spore-tmp
  mv .env.spore-tmp .env
  chmod 600 .env
}

ensure_docker() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    return 0
  fi
  if ! have_tty; then
    die "未检测到 Docker Engine + Compose v2，且当前无交互终端无法自动安装。请先参照 https://docs.docker.com/engine/install/ 安装后重试"
  fi
  warn "未检测到 Docker Engine 与 Compose v2 插件"
  if prompt_yesno "是否使用 get.docker.com 官方脚本自动安装 Docker？"; then
    local installer
    installer="$(mktemp)"
    fetch https://get.docker.com "$installer"
    if [ "$(id -u)" -eq 0 ]; then
      sh "$installer"
    else
      sudo sh "$installer"
    fi
    rm -f "$installer"
  else
    die "已取消。请手动安装 Docker Engine 与 Compose v2 插件后重新运行本命令"
  fi
  docker compose version >/dev/null 2>&1 || die "Docker 已安装但 compose 插件不可用，请手动检查"
}

# 部署目录与配置文件就绪；子命令/菜单操作的前置条件
prepare_deploy_dir() {
  mkdir -p "$SPORE_DIR"
  cd "$SPORE_DIR"
  if [ ! -f docker-compose.yml ]; then
    fetch "$REPO_RAW/docker-compose.yml" docker-compose.yml
    grep -q '^services:' docker-compose.yml || die "下载的 docker-compose.yml 内容异常，请检查网络后重试"
  fi
  if [ ! -f .env.example ]; then
    fetch "$REPO_RAW/.env.example" .env.example
    grep -q '^BOT_TOKEN=' .env.example || die "下载的 .env.example 内容异常，请检查网络后重试"
  fi
}

is_installed() {
  [ -f "$SPORE_DIR/docker-compose.yml" ] && [ -f "$SPORE_DIR/.env" ] &&
    grep -q '^services:' "$SPORE_DIR/docker-compose.yml"
}

require_installed() {
  is_installed || die "尚未安装（未找到 $SPORE_DIR 下的部署配置）。请先执行安装"
  cd "$SPORE_DIR"
}

is_running() {
  [ "$(docker inspect -f '{{.State.Running}}' "$CONTAINER_NAME" 2>/dev/null || echo false)" = "true" ]
}

# port_in_use <port>：TCP 探测 127.0.0.1 端口是否已被监听（bash 内建 /dev/tcp，无外部依赖）
port_in_use() {
  (exec 6<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null
}

# prompt_port：安装时询问宿主访问端口，默认取 .env 现值（否则 8080）；被占用时要求更换
prompt_port() {
  local current default
  current="$(env_get WEB_HOST_PORT)"
  current="${current:-8080}"
  default="$current"
  echo
  echo "宿主访问端口（管理端仅绑定 127.0.0.1，公网入口由宿主机反向代理转发）："
  while true; do
    if ! prompt_value "WEB_HOST_PORT（1-65535，留空回车默认 ${default}）" '^[0-9]{1,5}$' \
      "端口应为 1-65535 的数字" skip; then
      REPLY="$default"
    fi
    REPLY=$((10#$REPLY))
    if [ "$REPLY" -lt 1 ] || [ "$REPLY" -gt 65535 ]; then
      printf '[!] 端口范围 1-65535\n'
      continue
    fi
    if port_in_use "$REPLY"; then
      # spore 容器自身映射的现端口（重装/恢复场景）不算冲突
      if [ "$REPLY" = "$current" ] && docker port "$CONTAINER_NAME" 2>/dev/null | grep -q ":$REPLY"; then
        break
      fi
      warn "端口 $REPLY 已被占用（可用 docker ps --filter publish=$REPLY 或 ss -ltnp | grep $REPLY 定位占用方）；请换一个端口"
      continue
    fi
    break
  done
  if [ "$REPLY" != "$current" ]; then
    set_env WEB_HOST_PORT "$REPLY"
    PORT_CHANGED=1
    info "宿主端口已设为 ${REPLY}（反向代理请指向 127.0.0.1:${REPLY}）"
  else
    info "宿主端口：$REPLY"
  fi
}

# init_env：确保 .env 存在且三个必填凭据有效；已完整则跳过（升级路径），缺失则交互补填
init_env() {
  if [ ! -f .env ]; then
    cp .env.example .env
    info "已从 .env.example 生成 .env"
  fi
  chmod 600 .env

  local token api_id api_hash
  token="$(env_get BOT_TOKEN)"
  api_id="$(env_get TG_API_ID)"
  api_hash="$(env_get TG_API_HASH)"

  local -a need=()
  valid_token "$token" || need+=(BOT_TOKEN)
  valid_api_id "$api_id" || need+=(TG_API_ID)
  valid_api_hash "$api_hash" || need+=(TG_API_HASH)

  if [ "${#need[@]}" -eq 0 ]; then
    info ".env 凭据已配置，跳过填写"
    return 0
  fi

  if ! have_tty; then
    warn ".env 缺少必填凭据：${need[*]}"
    warn "当前无交互终端无法填写。请编辑 ${SPORE_DIR}/.env 后重新运行本命令继续"
    exit 1
  fi

  echo
  echo "首次部署需要填写 Telegram 凭据（仅写入 ${SPORE_DIR}/.env，权限 600，不进入 shell 历史）："
  echo "  - BOT_TOKEN：找 @BotFather 创建 bot 获取"
  echo "  - TG_API_ID / TG_API_HASH：https://my.telegram.org → API development tools 申请"
  echo "  - 暂时不填可留空直接回车跳过，之后编辑 .env 补上即可"
  echo
  local key skipped=""
  for key in "${need[@]}"; do
    case "$key" in
      BOT_TOKEN)
        if prompt_value "BOT_TOKEN（数字ID:字符串，留空回车跳过）" '^[0-9]+:[A-Za-z0-9_-]+$' \
          "格式应为「数字ID:字符串」，请从 @BotFather 获取" skip; then
          set_env BOT_TOKEN "$REPLY"
        else
          skipped="$skipped BOT_TOKEN"
        fi
        ;;
      TG_API_ID)
        if prompt_value "TG_API_ID（纯数字，留空回车跳过）" '^[0-9]+$' \
          "TG_API_ID 应为纯数字" skip; then
          set_env TG_API_ID "$REPLY"
        else
          skipped="$skipped TG_API_ID"
        fi
        ;;
      TG_API_HASH)
        if prompt_value "TG_API_HASH（32 位十六进制，留空回车跳过）" '^[0-9a-fA-F]{32}$' \
          "TG_API_HASH 应为 32 位十六进制字符" skip; then
          set_env TG_API_HASH "$REPLY"
        else
          skipped="$skipped TG_API_HASH"
        fi
        ;;
    esac
  done
  if [ -n "$skipped" ]; then
    warn "本次跳过未填：${skipped}"
    warn "部署会继续，但 Telegram 登录会失败。请稍后编辑 ${SPORE_DIR}/.env 填入凭据，"
    warn "再执行 spore upgrade（或菜单「2) 升级 Spore」）使其生效"
  fi
  echo
}

prepare_data_dir() {
  info "准备数据目录 data/（容器以 UID $CONTAINER_UID 运行）"
  mkdir -p data
  chmod 700 data
  if [ "$(id -u)" -eq 0 ]; then
    chown -R "$CONTAINER_UID:$CONTAINER_UID" data
  elif command -v sudo >/dev/null 2>&1; then
    sudo chown -R "$CONTAINER_UID:$CONTAINER_UID" data 2>/dev/null ||
      warn "data/ 属主修改失败；若容器启动报权限错误，请手动执行：sudo chown -R $CONTAINER_UID:$CONTAINER_UID ${SPORE_DIR}/data"
  else
    warn "无 root/sudo，跳过 data/ 属主修改；若容器启动报权限错误，请手动执行：sudo chown -R $CONTAINER_UID:$CONTAINER_UID ${SPORE_DIR}/data"
  fi
}

wait_healthy() {
  info "等待服务就绪（最长 ${HEALTH_TIMEOUT}s）..."
  local elapsed=0 status="unknown"
  while [ "$elapsed" -lt "$HEALTH_TIMEOUT" ]; do
    status="$(docker inspect -f '{{.State.Health.Status}}' "$CONTAINER_NAME" 2>/dev/null || echo unknown)"
    [ "$status" = "healthy" ] && return 0
    sleep 5
    elapsed=$((elapsed + 5))
  done
  warn "健康检查未通过（状态：${status}）。可执行 docker compose logs --tail 50 bot 排查后重试"
  return 1
}

# install_cmd：把脚本落一份到部署目录并软链为 /usr/local/bin/spore，
# 之后用户直接敲 `spore`（菜单）或 `spore <子命令>` 即可管理
install_cmd() {
  # 名字冲突保护：系统已有同名命令且不是我们的软链时不动它
  if command -v spore >/dev/null 2>&1 && [ "$(command -v spore)" != "/usr/local/bin/spore" ]; then
    warn "系统已存在 spore 命令（$(command -v spore)），跳过安装；如需本功能请先手动处理"
    return 0
  fi
  info "安装 spore 命令（部署目录脚本 + /usr/local/bin 软链）"
  # 先下到临时文件再原子替换：正在运行的旧脚本（经软链执行时）不受影响
  fetch "$REPO_RAW/install-spore.sh" "$SPORE_DIR/.install-spore.sh.tmp"
  grep -q 'main "\$@"' "$SPORE_DIR/.install-spore.sh.tmp" || die "下载的 install-spore.sh 内容异常"
  mv "$SPORE_DIR/.install-spore.sh.tmp" "$SPORE_DIR/install-spore.sh"
  chmod 755 "$SPORE_DIR/install-spore.sh"
  if as_root ln -sf "$SPORE_DIR/install-spore.sh" /usr/local/bin/spore &&
    [ "$(command -v spore 2>/dev/null)" = "/usr/local/bin/spore" ]; then
    info "已可用 spore 命令：直接输入 spore 打开菜单，或 spore status / spore upgrade 等"
  else
    warn "创建 /usr/local/bin/spore 失败，可手动执行：sudo ln -sf ${SPORE_DIR}/install-spore.sh /usr/local/bin/spore"
  fi
}

# web_port：宿主端口只经 .env 的 WEB_HOST_PORT 调整（compose 绑定 127.0.0.1）
web_port() {
  local port
  port="$(env_get WEB_HOST_PORT | tr -cd '0-9')"
  echo "${port:-8080}"
}

# ---------- 菜单动作 ----------

do_install() {
  ensure_docker
  docker info >/dev/null 2>&1 ||
    die "无法连接 Docker 守护进程。请将当前用户加入 docker 组（sudo usermod -aG docker \$USER 后重新登录）或用 sudo 重新运行本命令"

  if is_installed; then
    info "检测到现有安装，将保留配置并确保服务运行"
  else
    info "准备部署目录并下载配置（${REPO_RAW}）"
  fi
  prepare_deploy_dir
  init_env
  PORT_CHANGED=""
  prompt_port
  prepare_data_dir
  # 嵌套子 shell 隔离：命令注册失败只告警，不阻断安装主流程
  (install_cmd) || warn "spore 命令注册失败（不影响本次部署），可重新运行 install 重试或手动软链"

  if is_running; then
    if [ "$PORT_CHANGED" = "1" ]; then
      info "端口已变更，重建容器以应用 ..."
      docker compose up -d
      wait_healthy || true
    else
      info "服务运行中，如需更新镜像请使用菜单「升级」"
    fi
    return 0
  fi

  echo
  info "配置全部完成"
  local do_start=0
  if prompt_yesno "是否立即拉取镜像并启动服务？"; then
    do_start=1
  fi

  if [ "$do_start" != "1" ]; then
    info "已跳过启动。稍后启动：spore restart（或菜单「重启服务」；首次启动会先拉取镜像），"
    info "启动后在 bot 日志中查看管理端访问密钥：docker compose logs bot | grep -F '访问密钥'"
    return 0
  fi

  info "拉取镜像 $IMAGE ..."
  if ! docker compose pull; then
    warn "镜像拉取失败"
    die "若 GHCR 包为私有，请先 docker login ghcr.io 后重新运行；详见 docs/guide/deployment.md §4.2"
  fi
  info "启动服务 ..."
  docker compose up -d
  wait_healthy || true
  docker compose ps

  echo
  info "提取首次启动的管理端访问密钥（仅明文打印一次，请立即保存到密码管理器）"
  docker compose logs bot 2>&1 | grep -F '访问密钥' | tail -n 5 ||
    warn "日志中暂未找到访问密钥。可稍后执行：docker compose logs bot | grep -F '访问密钥'"

  local port
  port="$(web_port)"
  cat <<EOF

管理端入口 : http://127.0.0.1:${port}/admin/login
             （公网经宿主机反向代理的 https://<你的域名>/admin/login 访问）
首次登录   : 用上方访问密钥登录，再到管理端「MTProto」页面扫码完成用户会话登录
反向代理   : Compose 不含 HTTPS；在宿主机 Caddy/Nginx 把域名反代到 127.0.0.1:${port}
完整文档   : https://github.com/huaiminyetnotsleep/spore/blob/main/docs/guide/deployment.md
EOF
}

do_upgrade() {
  require_installed
  # 启用了 bigfile 备选路线（BOT_API_URL 非空）时，连带更新 bot-api 服务
  local -a pf=()
  [ -n "$(env_get BOT_API_URL)" ] && pf+=(--profile bigfile)
  info "拉取新镜像 $IMAGE ..."
  # ${pf[@]+...}：空数组在 bash 3.2 + set -u 下不可直接展开
  if ! docker compose ${pf[@]+"${pf[@]}"} pull; then
    warn "镜像拉取失败"
    die "若 GHCR 包为私有，请先 docker login ghcr.io 后重新运行；详见 docs/ops/operations.md §3"
  fi
  info "滚动更新 ..."
  docker compose ${pf[@]+"${pf[@]}"} up -d
  wait_healthy || true
  docker compose ps
  (install_cmd) || warn "spore 命令刷新失败（不影响本次升级），可重新运行 upgrade 重试"
  info "升级完成（配置未改动；如需回滚见 docs/ops/operations.md §3.3）"
}

do_uninstall() {
  require_installed
  if ! prompt_yesno "确认卸载 Spore？将停止并移除容器（${SPORE_DIR} 内配置与数据默认保留）"; then
    info "已取消卸载"
    return 0
  fi

  info "停止并移除容器与网络 ..."
  # --profile bigfile：连同可选启用的 bot-api 备选路线容器一起清理
  docker compose --profile bigfile down || true

  info "移除 spore 命令软链 ..."
  as_root rm -f /usr/local/bin/spore 2>/dev/null || warn "移除 /usr/local/bin/spore 失败，可手动执行：sudo rm -f /usr/local/bin/spore"

  if prompt_yesno "是否删除数据目录（data/ 与 bot-api-data/：数据库、MTProto 会话、临时媒体，删除后不可恢复）？"; then
    rm -rf data bot-api-data 2>/dev/null || sudo rm -rf data bot-api-data ||
      warn "删除失败，请手动执行：sudo rm -rf ${SPORE_DIR}/data ${SPORE_DIR}/bot-api-data"
    info "数据目录已删除"
  else
    info "保留数据目录（重新安装可继续使用）"
  fi

  if prompt_yesno "是否删除配置文件（.env / docker-compose.yml / .env.example）？"; then
    rm -f .env .env.spore-tmp docker-compose.yml .env.example
    info "配置文件已删除"
  else
    info "保留配置文件"
  fi

  # 脚本自身的部署目录副本一并清理（正在运行的实例不受影响：bash 持有已打开的 inode）
  rm -f install-spore.sh .install-spore.sh.tmp
  rmdir "$SPORE_DIR" 2>/dev/null || true
  info "卸载完成。镜像仍保留在本地，如需清理：docker rmi $IMAGE"
}

do_status() {
  require_installed
  docker compose ps
  if is_running; then
    local port
    port="$(web_port)"
    if http_ok "http://127.0.0.1:${port}/healthz"; then
      info "健康检查：通过（http://127.0.0.1:${port}/healthz）"
    else
      warn "健康检查：未通过（服务可能仍在启动，稍后重试或查看日志）"
    fi
  else
    warn "容器未运行，可从菜单「重启服务」或 docker compose up -d 启动"
  fi
}

do_logs() {
  require_installed
  echo "显示 bot 容器日志（按 Ctrl-C 返回菜单）..."
  docker compose logs -f --tail 100 bot || true
}

do_reset_key() {
  require_installed
  if ! is_running; then
    info "容器未运行，先启动 ..."
    docker compose up -d bot
    wait_healthy || true
  fi
  info "重置管理端访问密钥（新密钥仅打印一次，请立即保存；旧密钥与全部 Web 会话立即失效）"
  docker compose exec bot spore admin reset-key
}

do_restart() {
  require_installed
  if is_running; then
    info "重启服务 ..."
    docker compose restart bot
  else
    info "容器未运行，直接启动 ..."
    docker compose up -d bot
  fi
  wait_healthy || true
  docker compose ps
}

do_stop() {
  require_installed
  info "停止服务 ..."
  docker compose stop
  docker compose ps
}

# 升级后验证：探针 + 容器健康 + MTProto 大文件通道登录记录（operations.md §2.4 的自动化）
do_verify() {
  require_installed
  local port
  port="$(web_port)"
  local fail=0

  docker compose ps

  local health
  health="$(docker inspect -f '{{.State.Health.Status}}' "$CONTAINER_NAME" 2>/dev/null || echo unknown)"
  if [ "$health" = "healthy" ]; then
    info "容器健康检查：healthy"
  else
    warn "容器健康检查：${health}（刚重启属正常，稍后重试）"
    fail=1
  fi

  if http_ok "http://127.0.0.1:${port}/healthz"; then
    info "存活探针 /healthz：通过"
  else
    warn "存活探针 /healthz：失败"
    fail=1
  fi

  if http_ok "http://127.0.0.1:${port}/readyz"; then
    info "就绪探针 /readyz（数据库可读写）：通过"
  else
    warn "就绪探针 /readyz：失败（数据库未就绪）"
    fail=1
  fi

  if docker compose logs --tail 200 bot 2>&1 | grep -q 'Bot MTProto 会话有效，静默登录'; then
    info "Bot MTProto 大文件通道：已静默登录"
  else
    warn "日志未见 Bot MTProto 会话登录（刚重启属正常）；建议发一条测试链接观察投递"
  fi

  if [ "$fail" -eq 0 ]; then
    info "验证通过"
  else
    warn "存在未通过项，可执行 docker compose logs --tail 200 bot 排查"
    return 1
  fi
}

# 查看访问密钥：只查当前容器日志（密钥仅首启/重置时明文打印一次）
do_show_key() {
  require_installed
  info "从当前容器日志查找访问密钥 ..."
  if ! docker compose logs bot 2>&1 | grep -F '访问密钥' | tail -n 5; then
    warn "当前日志中未找到。容器重建后旧日志丢失、密钥不会重现——可从菜单「重设密钥」生成新密钥"
  fi
}

# 清理临时媒体：data/tmp 是传输临时目录，仅在服务停止时清空才是安全的
do_clean_tmp() {
  require_installed
  if [ ! -d data/tmp ]; then
    info "临时目录 data/tmp 不存在，无需清理"
    return 0
  fi
  local usage
  usage="$(as_root du -sh data/tmp 2>/dev/null | cut -f1)"
  info "临时媒体目录 data/tmp 当前占用：${usage:-未知}"
  prompt_yesno "确认清空？将先停止服务；进行中的传输任务会失败，需在 Bot 中重新提交" ||
    { info "已取消"; return 0; }
  docker compose stop bot
  as_root find data/tmp -mindepth 1 -delete 2>/dev/null || as_root rm -rf data/tmp
  info "已清空，重新启动服务 ..."
  docker compose up -d bot
  wait_healthy || true
}

# 磁盘与数据检查：容量、关键文件、回滚底档、.env 权限
do_diskcheck() {
  require_installed
  info "部署目录所在文件系统："
  df -h "$SPORE_DIR"

  echo
  info "数据目录占用："
  as_root du -sh data 2>/dev/null || true
  as_root du -sh data/tmp 2>/dev/null || warn "data/tmp 不存在（尚未产生临时媒体）"

  echo
  info "关键数据文件（data/）："
  as_root ls -lh data/ 2>/dev/null || warn "无法读取 data/（权限不足？）"

  if as_root ls data/spore.db.rollback-* >/dev/null 2>&1; then
    warn "存在 Web 导入留下的回滚底档 data/spore.db.rollback-*；确认运行正常后可手动清理释放空间"
  fi

  if find .env -maxdepth 0 -perm 600 >/dev/null 2>&1; then
    info ".env 权限：600（正确）"
  else
    warn ".env 权限异常（应为 600），可执行：chmod 600 .env"
  fi
}

# ---------- 菜单 ----------

show_banner() {
  cat <<'EOF'

==============================================================
                Spore 消息重构工具 Installer
         https://github.com/huaiminyetnotsleep/spore
==============================================================
EOF
}

show_menu() {
  cat <<'EOF'

请选择操作：
   1) 安装服务
   2) 升级服务
   3) 升级验证
   4) 查看状态
   5) 查看日志
   6) 查看密钥
   7) 重设密钥
   8) 清理临时
   9) 磁盘检查
  10) 重启服务
  11) 停止服务
  12) 卸载服务
  13) 退出脚本
EOF
  printf '输入选项 [1-13]: '
}

main_menu() {
  show_banner
  # Ctrl-C 只中断前台子命令（如日志跟踪），父进程忽略之使菜单存活；
  # 动作在子 shell 中执行，单个动作失败（含 die）不会退出菜单
  trap ':' INT
  while true; do
    show_menu
    read_input || die "无可用交互终端；请用子命令方式运行：install-spore.sh <install|upgrade|verify|status|logs|show-key|reset-key|clean-tmp|diskcheck|restart|stop|uninstall|exit>"
    case "$REPLY" in
      1) (do_install) || true ;;
      2) (do_upgrade) || true ;;
      3) (do_verify) || true ;;
      4) (do_status) || true ;;
      5) (do_logs) || true ;;
      6) (do_show_key) || true ;;
      7) (do_reset_key) || true ;;
      8) (do_clean_tmp) || true ;;
      9) (do_diskcheck) || true ;;
      10) (do_restart) || true ;;
      11) (do_stop) || true ;;
      12) (do_uninstall) || true ;;
      13) echo "再见"; exit 0 ;;
      '') : ;;
      *) warn "无效选项，请输入 1-13" ;;
    esac
    echo
  done
}

main() {
  # 子命令直通（便于脚本化；uninstall 仍会交互确认；此模式保留 set -e 语义）
  case "${1:-}" in
    install) do_install ;;
    upgrade) do_upgrade ;;
    verify) do_verify ;;
    status) do_status ;;
    logs) do_logs ;;
    show-key) do_show_key ;;
    reset-key) do_reset_key ;;
    clean-tmp) do_clean_tmp ;;
    diskcheck) do_diskcheck ;;
    restart) do_restart ;;
    stop) do_stop ;;
    uninstall) do_uninstall ;;
    exit) echo "再见"; exit 0 ;;
    "")
      main_menu
      ;;
    *)
      die "未知子命令：$1（可用：install | upgrade | verify | status | logs | show-key | reset-key | clean-tmp | diskcheck | restart | stop | uninstall | exit）"
      ;;
  esac
}

main "$@"
