# Spore 发版流程

> 本页说明 Spore 的自动发版机制：日常提交如何累积为 release PR、合并后自动执行什么、
> GHCR 镜像标签如何生成。面向项目维护者；部署者只需关注 §4 的镜像标签选择。
> 版本标签的升级与回滚操作见 [operations.md](./operations.md)，常见问题见
> [troubleshooting.md](./troubleshooting.md)。

CI 与发版由五个 GitHub Actions workflow 分工完成；日常 PR 先通过构建门禁，正式发版仍只需人工**合并 release PR**：

| Workflow | 职责 | 触发时机 |
| --- | --- | --- |
| `docker-build-check` | 完整验证 Dockerfile（前端、Go、FFmpeg、运行时镜像），不产出、不推送镜像 | **所有目标为 main 的 Pull Request**；**所有 push 到 main（含直接 push）**；手动 |
| `docs-build-check` | 执行 `npm ci && npm run build` 验证 VitePress，不部署 Pages | **所有目标为 main 的 Pull Request**；**所有 push 到 main（含直接 push）**；手动 |
| `release` | 运行 release-please：分析提交、计算版本号、维护 CHANGELOG，以 release PR 呈现；合并后创建 tag 与 GitHub Release | 每次 push 到 main |
| `docker-release` | 构建并推送**全部镜像**：`vX.Y.Z` / `vX.Y` / `vX` / `latest` / `sha-<commit>` | 版本 tag 出现时（自动发版经 `release` 派发的事件中转） |
| `docs-release` | 构建并部署该版本文档到 GitHub Pages | 版本 tag 出现时（自动发版经 `release` 派发的事件中转） |

`main` 的仓库 ruleset/branch protection 应把 `docker-build-check / build-check` 与
`docs-build-check / build-check` 设为 required status checks：任一失败都不得合并。
两个 workflow 对每个 main PR 和 main push 都启动，以保证 required status 始终存在；
内部先按变更路径判断，只有确实影响镜像（cmd/internal/frontend/public/Dockerfile 等）
或文档站（docs/public）的变更才执行昂贵构建，其余走明确的成功跳过步骤。手动触发
始终执行完整构建。两个检查只读源码，不拥有 GHCR/Pages 写权限；镜像推送与文档
部署仍只发生在正式 release。Docker 检查的 GHA cache 仅是性能优化，缓存
reservation 或后端故障会被忽略，不会把已成功的镜像构建误判为失败。

**release PR 无需人工审核**：release PR 由 `github-actions[bot]` 用
`GITHUB_TOKEN` 创建，而 GitHub 防循环规则规定 **`GITHUB_TOKEN` 创建的 PR 不
触发任何 `pull_request` workflow**——两项 build-check 在 release PR 上不会
运行，required 状态会永远停在 Expected。因此 `release` workflow 在创建/更新
release PR 后，按与 build-check 相同的路径判定预填两项门禁状态：release PR
只含 CHANGELOG（release-please 无 extra-files），判定"不影响镜像/文档站"
并标记 success，合并门禁随即放行，由维护者手动合并，全程无人工审核。若未来
release PR 含真正影响镜像或文档的文件，状态不预填（合并框会显示等待）——
由维护者向 release 分支推一个空提交（人工 push 会触发真实 CI）后再合并。
ruleset 姿态：只勾 **Require status checks**（两项 build-check）、不勾
Require approvals。注意 ruleset 的 Bypass Apps 列表只收录安装到仓库的
GitHub App，内置的 `github-actions[bot]` 无法入选——平台限制，不是配置缺失。

## 1. 日常提交与版本号规则

提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/zh-hans/)，
版本号由提交类型自动推导：

| 提交类型 | 版本影响 | 示例 |
| --- | --- | --- |
| `fix:` | 修订号 +1 | v1.4.0 → v1.4.1 |
| `feat:` | 次版本 +1 | v1.4.0 → v1.5.0 |
| `feat!:` 或含 `BREAKING CHANGE:` | 主版本 +1 | v1.4.0 → v2.0.0 |
| `docs:` `style:` `chore:` `refactor:` `test:` `ci:` | 不触发发版 | — |

直接 push 到 main 的提交无需任何额外操作：只要自上次发版以来存在 `feat`/`fix`
提交，它们就会自动累积进下一个 release PR；不想发版就不合并该 PR。

## 2. release PR：发版前唯一的人工步骤

每次 push 到 main，`release` workflow 会检查自上一个版本 tag 以来的提交。存在
可发布内容时，release-please 会维护一个标题为 `chore(main): release X.Y.Z` 的
release PR，内容是该版本的 CHANGELOG 更新，并打上 `autorelease: pending` 标签。

理解它的关键点：

- **永远只有一个 release PR**。PR 未合并期间，后续提交只会累积进同一个 PR
  （changelog 变长，版本号不变）；手动关闭后，下次 push main 会重新创建。
- **`autorelease: pending` 表示"已准备好、尚未发布"**。release-please 靠这个
  标签识别并持续同步自己的 PR；合并后状态变为 tagged。
- **版本号顺序是锁死的**。只有 v1.5.0 发布（tag 诞生）后，后续提交才会催生
  v1.6.0 的 PR；不存在跳过 1.5.0 直接合并 1.6.0 的场景。
- **合并 PR = 批准发布这一版**。发版是对外动作（用户会固定 tag、拉取镜像），
  因此保留人工确认：合并前审阅 PR 中的 changelog，确认这一版的范围与描述。

## 3. 合并 release PR 后的自动链路

合并 PR 后无需任何手动操作，以下步骤依次自动执行：

1. `release` workflow 识别到发版提交，创建 git tag `vX.Y.Z` 与 GitHub Release
   （Release 说明即 PR 中的 changelog）。
2. 由于 `GITHUB_TOKEN` 创建的 tag 不会触发其他 workflow（GitHub 防循环规则），
   同一 workflow 派发 `repository_dispatch` 事件，把 tag 名传递给镜像构建。
3. `docker-release` 检出该 tag 的源码构建镜像并推送到 GHCR：一次打出 `vX.Y.Z`、
   `vX.Y`、`vX` 三个标签，注入版本号（镜像内 `spore version` 输出与 tag 同名），
   并附带 SBOM 与来源证明。
4. main 上的普通提交不产出任何镜像：`docker-build-check` 只做构建验证、不推送。
   `latest` 与 `sha-<commit>` 同样在本步骤随发版更新，永远指向有版本号的发布。

## 4. GHCR 镜像标签

镜像地址为 `ghcr.io/huaiminyetnotsleep/spore`，可用标签分两类：

| 标签 | 生成来源 | 说明 |
| --- | --- | --- |
| `vX.Y.Z` | `docker-release`（发版构建） | 正式发版，固定不变，生产推荐 |
| `vX.Y` / `vX` | `docker-release`（发版构建） | 滚动标签，指向该次版本线/主版本线的最新发布 |
| `latest` | `docker-release`（发版构建） | 最新一次正式发布，随发版更新 |
| `sha-<commit>` | `docker-release`（发版构建） | 该次发布对应 commit 的完整 SHA，用于精确定位与回滚 |

所有标签只在发版时更新：main 上的中间提交不会产生镜像。生产环境建议固定到
`vX.Y.Z` 或完整 `sha-<commit>`，不要长期依赖 `latest`；升级与回滚操作见
[operations.md §2](./operations.md)。

## 5. 特殊场景

**指定下一个版本号**：在 push 到 main 的提交信息中加入
`Release-As: X.Y.Z` footer（如想直接发主版本或跳过若干版本），release-please
会按该版本号生成 release PR。

**手改 release PR 的版本号**：机制上可行（tag、Release、镜像都会按改后的版本
生成），但被跳过的号永久空缺，且若只改标题不改 PR 内的 CHANGELOG diff，会出现
tag 与 CHANGELOG.md 不一致。除非明确知道后果，否则不要手改。

## 6. 设计要点

- **镜像只随发版产出**：main 上的中间提交不产生镜像（`docker-build-check` 仅
  验证构建），`latest` 与 `sha-<commit>` 均随发版更新，保证部署者拉到的任何
  镜像都对应一个有版本号、有 changelog 的正式发布。
- **版本镜像构建独立且无路径过滤**：`paths` 过滤对 tag 推送同样生效，而发版
  合并往往只改 CHANGELOG.md——v1.1.0 曾因此漏发镜像。现在镜像由
  `docker-release` 无条件构建，只要版本 tag 出现就必然出镜像。
- **`repository_dispatch` 中转**：`GITHUB_TOKEN` 创建的 tag 其 push 事件不会
  触发其他 workflow，这是 GitHub 的防循环规则；派发事件是官方豁免的接续方式，
  同时覆盖人工打 tag 的场景（直接由 `push: tags` 触发）。
- **PR 作为人工门禁**：release-please 只能判断"有变化"，无法判断"想不想现在
  发布"。release PR 把版本号与 changelog 呈现给维护者审阅，同时天然支持把多个
  提交攒成一次发布。
