# 贡献指南

感谢你愿意改进 Spore。欢迎提交 bug 修复、功能改进、文档和测试。

## 开始之前

1. 阅读 [README](README.md) 和 [开发指南](docs/guide/development.md)。
2. 处理安全问题时不要创建公开 Issue，请先阅读 [SECURITY.md](SECURITY.md)。
3. 对较大的功能或行为变更，先开 Issue 说明动机、范围和兼容性影响。

## 开发流程

```bash
git clone https://github.com/huaiminyetnotsleep/spore.git
cd spore
cp .env.example .env
make frontend-install
```

从个人 Fork 创建主题分支，完成修改后再提交 Pull Request：

```bash
git checkout -b fix/short-description
make vet
make test
make frontend-lint
make frontend-typecheck
make frontend-test
make frontend-build
```

真实 Telegram 登录、频道读取、媒体上传、云盘和部署验收需要自己的测试账号与隔离环境；不要在 CI、Issue 或 PR 中上传 Token、Session、数据库、OAuth Secret 或真实用户数据。完整命令和边界见 [本地开发与调试](docs/guide/development.md)。

## Pull Request 要求

- 标题清楚描述变更；一个 PR 尽量只解决一个主题。
- 说明变更动机、实现范围、兼容性/迁移影响和测试结果。
- 用户可见行为变化要同步更新 README 或 `docs/`。
- 不提交构建缓存、`node_modules`、`data/`、`.env`、Session 或凭据。
- 通过自动检查后再请求审核；维护者可能要求补充测试或文档。

## 代码与文档约定

- Go 代码遵循 `gofmt`、`go vet` 和现有包结构。
- 前端使用项目已有的 TypeScript、React 和 ESLint 配置。
- 文档面向第一次使用仓库的读者，使用可复制但不含真实凭据的命令。
- 第三方依赖、Telegram API 和 rclone 的许可与条款仍然适用。

## 许可

提交到本项目的贡献将按照项目 [MIT License](LICENSE) 发布。提交内容前请确认你有权授予该许可。
