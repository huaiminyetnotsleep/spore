// 将仓库根目录的 CHANGELOG.md 同步为文档站的 reference/changelog.md。
// CHANGELOG.md 由 release-please 随 release PR 自动维护，是唯一事实源；
// 本页是构建期生成物（dev/build 脚本自动执行，见 package.json），不入库。
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const docsDir = dirname(dirname(fileURLToPath(import.meta.url)))
const source = join(dirname(docsDir), 'CHANGELOG.md')
const target = join(docsDir, 'reference', 'changelog.md')

let content = readFileSync(source, 'utf8').trimEnd()
content += `

## 相关链接

- 各版本的发布说明与镜像标签：[GitHub Releases](https://github.com/huaiminyetnotsleep/spore/releases)
- 版本标签的使用与回滚方式：[运维手册](/ops/operations)
`
mkdirSync(dirname(target), { recursive: true })
writeFileSync(target, content)
console.log('已同步 CHANGELOG.md → docs/reference/changelog.md')
