# spore 容器镜像：前端与 Go 多阶段构建，alpine 运行时（含 CA 证书、rclone 与精简 ffmpeg）
FROM node:24-alpine AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
# 品牌资源在仓库根 public/，由前端与文档站共享（两处 vite publicDir 均指 ../public）；
# 缺了它 dist 里没有 favicon.svg/icon.svg，运行时图标 404。
COPY public/ /src/public/
RUN npm run build

FROM golang:1.25 AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend-build /src/frontend/dist ./internal/web/frontend-dist
# 复用 Makefile 的 build-linux（构建参数单一来源）；前端产物随 Go 二进制嵌入。
# TARGETARCH 与最终阶段的 rclone 架构保持一致。
RUN case "${TARGETARCH}" in \
      arm64) go_arch=arm64 ;; \
      *) go_arch=amd64 ;; \
    esac \
    && make build-linux GOARCH="${go_arch}" \
    && mkdir -p /out && mv spore-linux /out/spore

# 精简 ffmpeg：只编译视频封面兜底抽帧（internal/media/thumb.go）所需的
# 容器/解析器/解码器与 mjpeg 编码，二进制约 5MB。alpine 仓库的 ffmpeg 会
# 连带 110MB+ 的编码器依赖链（x265/aom/SVT-AV1 等，抽帧用不上），全功能
# 静态构建单文件也普遍 100MB+；这里全部用 ffmpeg 内置解码器，无需第三方
# 编码库。冷门编码（theora 等）解码失败时抽帧自动降级为无封面，与 ffmpeg
# 缺失同语义；特殊视频可经 FFMPEG_PATH 指向宿主全功能 ffmpeg。
FROM alpine:3.20 AS ffmpeg-build
# 抽帧功能对版本不敏感，升级时只改这里的版本号（https://ffmpeg.org/releases/）。
ARG FFMPEG_RELEASE=7.1.2
RUN apk add --no-cache build-base xz zlib-dev
RUN wget -q "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_RELEASE}.tar.xz" -O /tmp/ffmpeg.tar.xz \
    && tar -xJf /tmp/ffmpeg.tar.xz -C /tmp \
    && cd /tmp/ffmpeg-${FFMPEG_RELEASE} \
    && ./configure \
      --disable-everything --disable-autodetect \
      --disable-doc --disable-debug --disable-network --disable-x86asm \
      --disable-ffplay --disable-ffprobe \
      --enable-small \
      --enable-zlib --enable-protocol=file,pipe \
      --enable-demuxer=mov,matroska,avi,mpegts,flv,asf,ogg \
      --enable-parser=h264,hevc,mpeg4video,mpegvideo,vp8,vp9,av1,h263,mjpeg \
      --enable-decoder=h264,hevc,mpeg1video,mpeg2video,mpeg4,h263,vp8,vp9,av1,mjpeg,flv,wmv1,wmv2,wmv3,vc1 \
      --enable-encoder=mjpeg --enable-muxer=mjpeg \
      --enable-filter=scale \
    && make -j"$(nproc)" ffmpeg \
    && strip ffmpeg \
    && ./ffmpeg -version \
    && mv ffmpeg /usr/local/bin/ffmpeg

FROM alpine:3.20
# 云盘下载（/download）依赖的 rclone：固定版本，取官方发布页二进制
# https://downloads.rclone.org/v<版本>/rclone-v<版本>-linux-<arch>.zip，
# 不依赖发行版仓库的陈旧版本；升级时只改这里的版本号。
ARG RCLONE_RELEASE=1.75.1
# buildx 多架构构建自动注入（amd64/arm64）；经典构建器未注入时回退 amd64，
# 与上面 build-linux 当前固定 GOARCH=amd64 一致。
ARG TARGETARCH
# ffmpeg 用 ffmpeg-build 阶段编译的精简二进制（见该阶段注释），
# 运行时只抽帧解码，无需发行版的全功能 ffmpeg 及其百 MB 依赖链。
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 spore
# rclone 解压到 PATH；alpine 无 unzip，临时安装后移除。
# rclone version 仅作构建期自检（二进制可在目标架构运行）。
RUN apk add --no-cache unzip \
    && case "${TARGETARCH}" in \
         arm64) rclone_arch=arm64 ;; \
         *) rclone_arch=amd64 ;; \
       esac \
    && wget -q "https://downloads.rclone.org/v${RCLONE_RELEASE}/rclone-v${RCLONE_RELEASE}-linux-${rclone_arch}.zip" -O /tmp/rclone.zip \
    && unzip -j -o /tmp/rclone.zip "*/rclone" -d /usr/local/bin \
    && chmod 755 /usr/local/bin/rclone \
    && /usr/local/bin/rclone version >/dev/null \
    && rm -f /tmp/rclone.zip \
    && apk del unzip
WORKDIR /app
# 预建数据目录并归属运行用户：bind mount 宿主目录不存在时 Docker 以 root 创建，
# 这里保证镜像内路径权限正确；宿主手动挂载时仍需 chown 10001（见 deployment.md）
RUN mkdir -p /app/data && chown spore:spore /app/data
COPY --from=ffmpeg-build /usr/local/bin/ffmpeg /usr/local/bin/ffmpeg
COPY --from=build /out/spore /usr/local/bin/spore
USER spore
VOLUME ["/app/data"]
CMD ["spore"]
