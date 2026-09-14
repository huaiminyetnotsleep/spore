# spore 容器镜像：前端与 Go 多阶段构建，alpine 运行时（含 CA 证书与 rclone）
FROM node:24-alpine AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
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

FROM alpine:3.20
# 云盘下载（/download）依赖的 rclone：固定版本，取官方发布页二进制
# https://downloads.rclone.org/v<版本>/rclone-v<版本>-linux-<arch>.zip，
# 不依赖发行版仓库的陈旧版本；升级时只改这里的版本号。
ARG RCLONE_RELEASE=1.75.1
# buildx 多架构构建自动注入（amd64/arm64）；经典构建器未注入时回退 amd64，
# 与上面 build-linux 当前固定 GOARCH=amd64 一致。
ARG TARGETARCH
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
COPY --from=build /out/spore /usr/local/bin/spore
USER spore
VOLUME ["/app/data"]
CMD ["spore"]
