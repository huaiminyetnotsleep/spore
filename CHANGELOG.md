# 更新日志

所有重要变更会记录在这里。版本号遵循 [Semantic Versioning](https://semver.org/)；带 `v` 的 Git tag 会触发容器镜像发布。

## [Unreleased]

### Changed

- 容器镜像约减半（224MB → 117MB）：ffmpeg 改为源码最小化编译（仅保留视频封面抽帧所需的容器/解码器与 mjpeg 编码，二进制约 4MB），替换 alpine 仓库 ffmpeg 连带的 110MB+ 编码器依赖链。常见编码（h264/hevc/vp8/vp9/av1/mpeg4 等）抽帧不受影响，冷门编码失败时仍自动降级为无封面；特殊视频可经 `FFMPEG_PATH` 指向宿主全功能 ffmpeg。
