# 更新日志

所有重要变更会记录在这里。版本号遵循 [Semantic Versioning](https://semver.org/)；带 `v` 的 Git tag 会触发容器镜像发布。

## [1.16.4](https://github.com/huaiminyetnotsleep/spore/compare/v1.16.3...v1.16.4) (2026-09-20)


### Bug Fixes

* **media:** 相册署名 caption 两步强制写入首末成员保证组级展示 ([7ad71d1](https://github.com/huaiminyetnotsleep/spore/commit/7ad71d18b8ddeef3fdb1f7effb274203d8d3332e))

## [1.16.3](https://github.com/huaiminyetnotsleep/spore/compare/v1.16.2...v1.16.3) (2026-09-20)


### Bug Fixes

* **media:** 拆分相册 caption 重写覆盖 Bot API 分支并补记 split 投递方式 ([63d641c](https://github.com/huaiminyetnotsleep/spore/commit/63d641cfc2451844cddcb35966805addb0386a24))

## [1.16.2](https://github.com/huaiminyetnotsleep/spore/compare/v1.16.1...v1.16.2) (2026-09-20)


### Bug Fixes

* **media:** MTProto 整组发送后经 Bot API 重写成员 caption 保证展示 ([ef754aa](https://github.com/huaiminyetnotsleep/spore/commit/ef754aa0806356289bf5b4fc4e647859c98be8ad))

## [1.16.1](https://github.com/huaiminyetnotsleep/spore/compare/v1.16.0...v1.16.1) (2026-09-20)


### Bug Fixes

* **media:** 拆分段首段无条件携带完整署名 ([c653682](https://github.com/huaiminyetnotsleep/spore/commit/c6536827073bfa4c9fd08f3eeb2d340147f73fd3))

## [1.16.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.15.1...v1.16.0) (2026-09-20)


### Features

* **media:** 切段能力下载前前置校验，不可用直接报错不降级 ([d3d9769](https://github.com/huaiminyetnotsleep/spore/commit/d3d9769886687638409612b355e0493fcd64ba03))

## [1.15.1](https://github.com/huaiminyetnotsleep/spore/compare/v1.15.0...v1.15.1) (2026-09-19)


### Bug Fixes

* **media:** 切段输出显式 matroska 并让整组切段失败自动回退逐条 ([dacec95](https://github.com/huaiminyetnotsleep/spore/commit/dacec9546f20f90c98f2d00c0d89f2bdd1fffff8))

## [1.15.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.14.0...v1.15.0) (2026-09-19)


### Features

* **media:** 拆分投递改为可播放的视频分段并修复缓存频道副本 ([a13c4ce](https://github.com/huaiminyetnotsleep/spore/commit/a13c4ceef18c60880432dee52590589c78a35481))

## [1.14.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.13.0...v1.14.0) (2026-09-19)


### Features

* **media:** 超过 2GB 的媒体分卷拆分投递 ([54176e8](https://github.com/huaiminyetnotsleep/spore/commit/54176e860062a5945c51d8f38a4f0371e73dc807))

## [1.13.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.12.0...v1.13.0) (2026-09-19)


### Features

* **queue:** 任务失败提示附带来源链接 ([175368a](https://github.com/huaiminyetnotsleep/spore/commit/175368a1e8629a37d3d4a0e22563797eb19cfb87))

## [1.12.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.11.0...v1.12.0) (2026-09-18)


### Features

* **apperr:** 错误码细化，网络/服务端/引用/发送目标单列 ([4db4232](https://github.com/huaiminyetnotsleep/spore/commit/4db423211c109f996512ded4c220b7490101e6bd))
* **stats:** 业务统计新增投递方式分布图 ([9df74fa](https://github.com/huaiminyetnotsleep/spore/commit/9df74fa967ff30181457df15a844faf241abbb34))
* **web-console:** 错误码中文标签映射与展示细化 ([70bbb11](https://github.com/huaiminyetnotsleep/spore/commit/70bbb11ce3d9bf66656cb1e73f7122a104951d73))

## [1.11.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.10.0...v1.11.0) (2026-09-18)


### Features

* **notify:** 通知正文模板化并新增活动通知（登录/申请/加入） ([6cae569](https://github.com/huaiminyetnotsleep/spore/commit/6cae569eeff0e6d619534d733112f9345a5763e4))

## [1.10.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.9.0...v1.10.0) (2026-09-17)


### Features

* **backup:** 重构数据备份页 — JSON/DB 导出导入全功能实现 ([aae39c5](https://github.com/huaiminyetnotsleep/spore/commit/aae39c55e5b5cb1b575c8417c797f5ef100d3993))
* **frontend:** 新增通知设置页、路由与 API 封装 ([1966968](https://github.com/huaiminyetnotsleep/spore/commit/196696899fa41bdc6af95b0aa78c1238e9da0cb8))
* **notification:** 增加通知策略与静音配置 ([4f82069](https://github.com/huaiminyetnotsleep/spore/commit/4f82069222718f8a1c848547497e6dba43e78e75))
* **notifycfg:** 通知通道配置、凭据加密与四格式适配器 ([13a5c49](https://github.com/huaiminyetnotsleep/spore/commit/13a5c493b73fbfcb479fe90ce5322f9d22d298e7))
* **web:** 通知设置 API、路由与启动装配 ([b93e59a](https://github.com/huaiminyetnotsleep/spore/commit/b93e59ac7aa4711c104bd704e3846f22337af7bb))


### Bug Fixes

* **docs:** 转义通知接口路径占位符 ([8981758](https://github.com/huaiminyetnotsleep/spore/commit/8981758c4f8070e9a86c8f3a3d9b75faa02e58d8))

## [1.9.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.8.0...v1.9.0) (2026-09-17)


### Features

* **frontend:** 总览页机器人池信息独立成区 ([d0113ea](https://github.com/huaiminyetnotsleep/spore/commit/d0113eaae73b1c39896da1b46a67501f17b3974c))
* **frontend:** 请求记录使用圆形进度条 ([00b028d](https://github.com/huaiminyetnotsleep/spore/commit/00b028def5efe2f5338bdb4d96c55128ef72714e))


### Bug Fixes

* **bot:** 管理端 Bot 会话状态挂到真实运行客户端，修复恒显离线 ([60ca2c7](https://github.com/huaiminyetnotsleep/spore/commit/60ca2c7f855a254194b4575eeb52b0dd8c85c6ca))

## [1.8.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.7.0...v1.8.0) (2026-09-17)


### Features

* **bot:** 机器人池支持手动暂停/恢复 + 冲突检测接管 + 独立菜单组 ([9dc8c67](https://github.com/huaiminyetnotsleep/spore/commit/9dc8c670a730048ff2f02f4d5f61e4c3b7df86b9))
* **frontend:** 统一应用壳、登录与 404 ([8fd0973](https://github.com/huaiminyetnotsleep/spore/commit/8fd09735af487278f7c0ea0a7d771cbede7501bc))
* **frontend:** 统一设计基础与共享组件库 ([b5f64b3](https://github.com/huaiminyetnotsleep/spore/commit/b5f64b31236c6ba6dc8b70aadd5dde87a1f0d6ee))
* **frontend:** 迁移事件、审计与机器人页面 ([641e82d](https://github.com/huaiminyetnotsleep/spore/commit/641e82d22f4a9f9d4fb6a294b09e3b2900a56941))
* **frontend:** 迁移总览与统计页面 ([ba0a788](https://github.com/huaiminyetnotsleep/spore/commit/ba0a788599b0362b8066806017450e908a363ae6))
* **frontend:** 迁移用户、申请与请求页面到统一骨架 ([02aac75](https://github.com/huaiminyetnotsleep/spore/commit/02aac753ea9d0109a68b0708bf030847308d25a1))
* **frontend:** 迁移设置与高风险页面 ([ce24daa](https://github.com/huaiminyetnotsleep/spore/commit/ce24daa4dfb7b07e953cf6faa4f1ff694250048e))
* **frontend:** 迁移频道、绑定与 BotUser 频道页面 ([422ede0](https://github.com/huaiminyetnotsleep/spore/commit/422ede0500fa1ed8cb9f41c9608f7a783471149c))
* **install:** 菜单分组加图标，升级后自动验证，新增完整重启 ([e09de99](https://github.com/huaiminyetnotsleep/spore/commit/e09de99cc30cd058bd628ebaebf8e4f49b56a643))

## [1.7.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.6.3...v1.7.0) (2026-09-16)


### Features

* **bot:** 支持绑定多个机器人（池化运行 + bot 维度留痕与展示） ([975db56](https://github.com/huaiminyetnotsleep/spore/commit/975db5651f5dc4a563ccf902d791e8edeed939dc))

## [1.6.3](https://github.com/huaiminyetnotsleep/spore/compare/v1.6.2...v1.6.3) (2026-09-15)


### Bug Fixes

* **bot:** 缓存补写试探复制移出 SQLite 事务，修复转存触发全站卡死 ([e694956](https://github.com/huaiminyetnotsleep/spore/commit/e69495614bb6169e02a15444248fdc22bcb0191e))

## [1.6.2](https://github.com/huaiminyetnotsleep/spore/compare/v1.6.1...v1.6.2) (2026-09-15)


### Bug Fixes

* **bot:** 缓存补写副本有效性改用试探复制判定，修复 already_dumped 误判 ([a371efb](https://github.com/huaiminyetnotsleep/spore/commit/a371efb640010d7a28a2e14ea20218ee7cbad7be))

## [1.6.1](https://github.com/huaiminyetnotsleep/spore/compare/v1.6.0...v1.6.1) (2026-09-15)


### Bug Fixes

* **bot:** 缓存补写支持大文件直传到频道，副本被删后可重新补写 ([1986c1c](https://github.com/huaiminyetnotsleep/spore/commit/1986c1cda0babb1dc6ba3c1ed6d22dc78d39ea47))

## [1.6.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.5.0...v1.6.0) (2026-09-15)


### Features

* **web:** 支持批量触发请求记录转存缓存频道（缓存补写） ([1e81863](https://github.com/huaiminyetnotsleep/spore/commit/1e818630ceb3f8599a21b43d75c098458d6f33bd))

## [1.5.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.4.0...v1.5.0) (2026-09-15)


### Features

* **web:** 服务版本行展示当前与最新版本，手动刷新 toast 提示 ([2110759](https://github.com/huaiminyetnotsleep/spore/commit/2110759729eefa3726305a941742adc77896308c))


### Bug Fixes

* **web:** 最新版本标签改用 success 绿色样式 ([71d6953](https://github.com/huaiminyetnotsleep/spore/commit/71d6953ab2539fb3b35b3d33296712d1ffc3c88a))
* **web:** 版本检查展示不做新旧判断，直接显示当前版本与最新版本 ([90bd75d](https://github.com/huaiminyetnotsleep/spore/commit/90bd75d62063d1337906e9503e17d5d9dbfc7151))

## [1.4.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.3.2...v1.4.0) (2026-09-15)


### Features

* **web:** enhance user navigation and request details ([f1ed70d](https://github.com/huaiminyetnotsleep/spore/commit/f1ed70da33a551cf7749a3b81c20a662ec3cd829))

## [1.3.2](https://github.com/huaiminyetnotsleep/spore/compare/v1.3.1...v1.3.2) (2026-09-15)


### Bug Fixes

* **web:** 检查更新支持 force 跳过缓存，手动刷新拿到实时结果 ([a90c53b](https://github.com/huaiminyetnotsleep/spore/commit/a90c53b815e74ce5bbbbfb1a8539e6a17d862dcb))

## [1.3.1](https://github.com/huaiminyetnotsleep/spore/compare/v1.3.0...v1.3.1) (2026-09-15)


### Bug Fixes

* **web:** 总览页服务版本展示截取完整 SHA 为 7 位短哈希 ([33465a4](https://github.com/huaiminyetnotsleep/spore/commit/33465a4eac5be0330cce3355e6b1ab503d097f93))

## [1.3.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.2.1...v1.3.0) (2026-09-15)


### Features

* **web:** 总览页展示机器人身份与真实服务版本，支持检查更新 ([d208249](https://github.com/huaiminyetnotsleep/spore/commit/d208249911930a2196d240b71a5f98e75dac6ede))

## [1.2.1](https://github.com/huaiminyetnotsleep/spore/compare/v1.2.0...v1.2.1) (2026-09-15)


### Bug Fixes

* **ci:** release-please 打 tag 经 repository_dispatch 接续镜像构建，修复发版镜像漏发 ([c409d45](https://github.com/huaiminyetnotsleep/spore/commit/c409d45fb08061e5b9a63cc082be4b3c8ed72fea))

## [1.2.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.1.0...v1.2.0) (2026-09-15)


### Features

* **bot:** 支持批量链接提交 ([75718a0](https://github.com/huaiminyetnotsleep/spore/commit/75718a06312c736007514c8323bd41cf5c77fc62))

## [1.1.0](https://github.com/huaiminyetnotsleep/spore/compare/v1.0.0...v1.1.0) (2026-09-15)


### Features

* **docs:** 文档站新增更新日志页，构建期自动同步 CHANGELOG.md ([15143c0](https://github.com/huaiminyetnotsleep/spore/commit/15143c00adc44e4882cc3c8b784d541b6fff1dfc))

## [Unreleased]
