# Shell 脚本规范（bash）

> 适用范围：仓库内的 bash 脚本（`install.sh`、未来的辅助脚本、容器入口脚本）。
> 目标环境是 Linux 服务器（bash 5+），但脚本必须在 macOS 自带的 bash 3.2 下也可运行（本地验证依赖它）。

---

## 强制规则

### 1. 变量后紧邻非 ASCII 字符必须用 `${VAR}`

部分 bash 版本 + locale 组合（如 macOS bash 3.2）会把全角标点（如 `）`）的首字节并入
`$VAR` 的变量名继续展开，产生 `SPORE_DIR）: unbound variable` 这类诡异错误且值实际未定义：

```bash
# ❌ 危险：bash 3.2 + UTF-8 locale 下 $SPORE_DIR）会找不到变量
echo "目录：$SPORE_DIR）"

# ✅ 花括号终止名字解析
echo "目录：${SPORE_DIR}）"
```

同理 `$status）`、`$REPO_RAW）` 等。grep 自查：`grep -n '\$[A-Za-z_][A-Za-z0-9_]*[^\x00-\x7F]' script.sh`。

### 2. 压住 `exec` 打开失败的报错必须用分组

`exec 3<>/dev/tty 2>/dev/null` 压不住报错：重定向从左到右处理，`3<>/dev/tty` 失败发生在
`2>/dev/null` 生效之前，错误直接写到原始 stderr。用分组把重定向挂在组上：

```bash
# ✅ /dev/tty 不可用（curl|bash 的 CI、无控制终端）时静默降级
if { exec 3<>/dev/tty; } 2>/dev/null; then
  TTY_FD=3
fi
```

### 3. curl|bash 场景的交互一律读 /dev/tty

`curl ... | bash` 时 stdin 是脚本管道，`read` 从 stdin 读会消费脚本文本。所有交互输入显式
`read ... <&"$TTY_FD"`，无 tty 时走非交互降级路径（生成文件 + 提示手工补全 + 非零退出）。

### 4. 兼容性红线（bash 3.2 可运行）

- sed -i：GNU/BSD 参数不同 → 一律走「重定向到临时文件再 mv」，禁止 `sed -i`
- 空数组在 `set -u` 下展开：`${arr[@]}` 只能在 `${#arr[@]}` 非零判断之后出现
- 正则放入变量再 `[[ $x =~ $re ]]`，避免内联 `{n}` 量词在旧版本的解析差异
- bash 3.2 无 nameref（`local -n`）：`read -r "$var"` 间接赋值会触发 shellcheck
  SC2229 → 统一约定读入全局 `REPLY` 再由调用方取用（见 install-spore.sh read_input）

---

## 交互式菜单脚本模式（install-spore.sh）

- 每个菜单动作在**子 shell** 中执行：`(do_xxx) || true` —— 动作内部保留 `set -e`
  语义（失败中止该动作），但 `die` / 报错只退出子 shell，菜单存活
- 父进程 `trap ':' INT`：Ctrl-C 只中断前台子命令（如 `logs -f` 跟踪），不杀菜单；
  子 shell 的 INT 保持默认（收到即死，正合需求）
- 子命令直通（`install-spore.sh upgrade`）不经子 shell 包装，保留 `set -e` 语义供
  脚本化调用判断退出码
- 危险动作（卸载/清理）默认值必须是「取消」：确认 prompt 读不到输入（EOF/无 TTY）
  一律返回非确认
- data/ 属主是容器 UID 10001：宿主读写一律经 `as_root`（root 直跑，否则 sudo）；
  测试环境用 `exec "$@"` 的 sudo shim 模拟免密 sudo（只 exit 0 的 shim 会让 cp/chown
  静默不生效，测不出来）
- 文件名由脚本自己生成且含时间戳时，字典序即时间序 → 用 glob 循环取最新/列表，
  不要 `ls -t`（shellcheck SC2012）；权限检查用 `find <file> -maxdepth 0 -perm 600`
  （GNU/BSD 通用，避免 stat 参数差异与 ls 解析）

---

## 质量门（提交前必过）

```bash
bash -n install.sh
shellcheck install.sh   # 零告警
```

功能分支验证用 PATH shim 伪造 `docker` 子命令 + `SPORE_RAW_BASE=file://` 本地下载源 +
`expect` 驱动 pty 测交互（macOS `script` 会一次性灌完 stdin 后关闭 pty，不可用于喂交互输入）。
