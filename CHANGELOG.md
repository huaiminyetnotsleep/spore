# 更新日志

所有重要变更会记录在这里。版本号遵循 [Semantic Versioning](https://semver.org/)；带 `v` 的 Git tag 会触发容器镜像发布。

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
