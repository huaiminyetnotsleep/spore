# 安全政策

## 支持范围

当前 `main` 分支和最新发布版本优先获得安全修复。Spore 的部署者负责保护自己的服务器、Telegram 账号、OAuth 凭据、云盘凭据和备份。

## 报告漏洞

请不要在公开 Issue、Pull Request、聊天记录或截图中披露尚未修复的漏洞、Token、Session 或个人数据。请通过 GitHub Security Advisories 的 **Report a vulnerability** 私下报告；如果该入口暂不可用，请通过[维护者的 GitHub 个人页](https://github.com/huaiminyetnotsleep)提供的私下联系方式联系维护者，并只发送最少的复现信息。

报告请包含：

- 受影响的版本或 commit；
- 复现步骤和影响范围；
- 必要的日志或代码片段（请先删除凭据和个人数据）；
- 建议的修复方向（如有）。

维护者会确认收到报告，评估严重性，并在修复或缓解措施可用后协调公开披露。请给维护者合理的处理时间，不要在漏洞修复前公开细节。

## 凭据泄露

如果 `BOT_TOKEN`、Telegram API Hash、用户号 Session、Bot Session、OAuth Secret、`WEB_OAUTH_ENCRYPTION_KEY` 或云盘凭据泄露：

1. 立即在对应服务撤销或轮换凭据；
2. 停止受影响实例并隔离日志、备份和数据目录；
3. 检查 Git 历史、CI 日志和镜像构建产物；
4. 再通过私下渠道报告，以便维护者判断是否需要清理发布物。

请先阅读 [部署指南](docs/guide/deployment.md) 和 [运维手册](docs/ops/operations.md) 中的数据边界说明。仓库内容与部署数据相互独立，运行实例中的数据不会随仓库或镜像发布。
