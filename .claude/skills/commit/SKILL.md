---
name: commit
description: '提交代码。当用户要求提交代码、commit、提交变更时触发。'
user-invocable: true
---

# 提交代码

## 执行步骤

### 1. 收集信息

并行执行以下命令：

- `git status` — 查看工作区状态
- `git diff` — 查看未暂存的变更
- `git diff --cached` — 查看已暂存的变更
- `git log --oneline -5` — 查看最近提交记录，参考 commit message 风格
- `git config user.name` — 获取当前 Git 用户名，用于 Co-Authored-By
- `git branch --show-current` — 获取当前分支名
- `git rev-parse --abbrev-ref @{upstream} 2>/dev/null` — 检查是否已设置远端跟踪分支

**如果工作区没有任何变更（无未暂存、无已暂存、无未跟踪文件），直接告知用户"没有需要提交的变更"并终止流程。**

### 2. 分析变更并生成 commit message

- 分析所有变更内容，归纳变更类型（新功能、bug修复、重构、样式、文档等）
- **敏感文件检查**：如果变更中包含 `.env`、`credentials`、密钥文件等敏感文件，从提交列表中排除并警告用户
- commitlint 要求 subject 不能以大写开头
- 根据 commitlint 规范生成 commit message，格式为：

  `<type>(<scope>): <subject>` 换行 `<body>`

- type 可选值：`feat`、`fix`、`docs`、`refactor`、`chore`、`ci`、`style`、`test`
- scope 为可选的业务域或模块名称
- subject 为简洁的中文描述
- body 为可选的详细说明（多行变更时补充）

### 3. 确认提交

向用户展示：

- 即将提交的文件列表（逐个列出文件路径）
- 生成的 commit message（用代码块展示，方便用户预览完整格式）

然后使用 AskUserQuestion 工具询问用户是否确认提交，提供两个选项：

- **Yes** — 确认执行 `git add` 和 `git commit`
- **No** — 取消本次提交

用户选择 Yes 才执行后续提交操作，选择 No 则终止流程。

### 4. 执行提交

确认后执行：

1. `git add <具体文件>` — 按文件名添加，不使用 `git add -A` 或 `git add .`
2. 使用 HEREDOC 格式提交：

```bash
git commit -m "$(cat <<'EOF'
<type>(<scope>): <subject>

<body>

Co-Authored-By: <步骤1获取的 git user.name>
EOF
)"
```

3. `git status` — 验证提交是否成功

### 5. 询问是否 push 到远端

提交成功后，使用 AskUserQuestion 工具询问用户是否需要 push 到远端仓库，提供两个选项：

- **Yes** — 推送到远端
- **No** — 仅保留本地提交，不推送

用户选择 Yes 时：

- 如果步骤1检测到已有远端跟踪分支：执行 `git push`
- 如果没有远端跟踪分支（新分支）：执行 `git push -u origin <当前分支名>`

用户选择 No 则流程结束。

## 注意事项

- 不要提交包含敏感信息的文件（`.env`、`credentials`、私钥等），发现时主动警告用户
- 未被明确要求时，不要顺手提交无关变更
- 如果 pre-commit hook 失败：
  - hook 失败意味着 commit **没有成功创建**
  - 排查并修复问题后，创建**新的** commit
  - 不要使用 `--no-verify` 跳过 hook
  - 不要使用 `--amend`，因为 amend 会修改上一个已存在的 commit，而非失败的那次
- commit message 使用中文，footer 只使用当前 Git 配置中的 `user.name`
