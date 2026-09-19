---
layout: home

hero:
  name: Spore
  text: Telegram 受保护消息提取机器人
  tagline: 把频道消息链接发给 Bot，收到的就是一条可再次转发的全新消息。
  actions:
    - theme: brand
      text: 快速部署 →
      link: /guide/deployment
    - theme: alt
      text: 使用指南
      link: /guide/usage
    - theme: alt
      text: GitHub 仓库
      link: https://github.com/huaiminyetnotsleep/spore

features:
  - icon: 📨
    title: 受保护消息提取
    details: 支持文本、图片、视频、文件、音频、语音条与相册，保留实体格式；超过 Bot API 50MB 上限的大文件经 Bot 身份 MTProto 直传（上限 2000MB），再超过的自动分卷拆分——视频切成可直接播放的分段，同一条相册消息送达（上限约 17.6GB）。
  - icon: 🤖
    title: Bot 使用
    details: 私聊发链接即可；11 条命令覆盖申请、额度查询、进度、取消、频道绑定与加入；相册整组、自动重试与去重都有明确边界。
  - icon: ☁️
    title: 云盘下载
    details: /download 把提取的媒体直接上传到网盘（经 rclone，实测 MEGA），不再重发回 Telegram；支持多目的地、排队与取消。
  - icon: ♻️
    title: 缓存频道复用
    details: 成功投递同步写无脚注干净副本到自有缓存频道，同链接再次提交整条复制秒回——不限媒体大小、相册保组。
  - icon: 🛠️
    title: Web 管理端
    details: 内置 SPA 管理面板：用户管理、频道加入审批、请求记录、频道统计、运行设置与云盘下载配置。
  - icon: 🧭
    title: 文档导航
    details: 部署与运维按指南操作；环境变量与生效时机见配置参考；接口契约、架构与开发入口齐备。
---

你可以直接使用 GHCR 镜像或从源码部署，也可以通过
[贡献指南](https://github.com/huaiminyetnotsleep/spore/blob/main/CONTRIBUTING.md) 提交改进。
请先阅读 [安全政策](https://github.com/huaiminyetnotsleep/spore/blob/main/SECURITY.md)，不要在公开 Issue、PR 或日志中提交凭据。
