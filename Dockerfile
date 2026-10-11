# syntax=docker/dockerfile:1

# 基础镜像用全限定名：podman 等实现不配置 unqualified-search-registry 时，
# 短名（golang:…）会解析失败，而 docker.io/library/… 两边都认。
#
# 版本从 mise.toml 传入（mise run docker:build 会带 --build-arg），
# 这里的默认值只是给不用 mise、直接 docker build 的人留的，
# 改 Go / Node 版本只需改 mise.toml 一处。
ARG DEBIAN_IMAGE=docker.io/library/debian:bookworm-slim
ARG GO_VERSION=1.27.2
ARG NODE_VERSION=24

# 原先的前端构建阶段已停用：现在前端在本地 mise run web:build，
# 产物随下面的 COPY . . 进镜像。需要改回镜像内构建时，取消下面这段注释，
# 并把末尾的 COPY --from=web 一并恢复。
#
# FROM docker.io/library/node:${NODE_VERSION}-bookworm-slim AS web
#
# WORKDIR /src/web
#
# COPY web/package.json web/package-lock.json ./
# RUN npm config set registry https://registry.npmmirror.com \
#     && npm ci
#
# COPY web/ ./
# RUN npm run type-check && npm run build
#
# 注意：这段沿用旧的 npm + package-lock.json 流程，两者都已删除；
# 现在的依赖管理是 pnpm（web/pnpm-lock.yaml，命令见 docs/developer.md）。

FROM docker.io/library/golang:${GO_VERSION}-bookworm AS build

WORKDIR /src

# 版本由 mise.toml 的 [tools] go 通过 --build-arg GO_VERSION 传入；
# local 禁止再去拉 toolchain，避免 proxy.golang.org 中断
ENV GOTOOLCHAIN=local \
    CGO_ENABLED=0 \
    GOPROXY=https://goproxy.cn,direct
ARG BUILD_TAGS=fuse

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# COPY --from=web /src/internal/api/web /src/internal/api/web

RUN go build -tags "${BUILD_TAGS}" -trimpath -ldflags="-s -w" -o /out/litepan ./cmd/litepan


FROM ${DEBIAN_IMAGE} AS runtime

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates tzdata fuse3 \
    && sed -i 's/^#user_allow_other/user_allow_other/' /etc/fuse.conf 2>/dev/null || true \
    && grep -q '^user_allow_other' /etc/fuse.conf || echo user_allow_other >> /etc/fuse.conf \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

RUN mkdir -p /app/data/log /app/strm /app/mounts

COPY --from=build /out/litepan /app/litepan

ENV LITEPAN_DATA_DIR=/app/data \
    LITEPAN_STRM_DIR=/app/strm \
    LITEPAN_LISTEN=:5211 \
    LITEPAN_LOG_LEVEL=info \
    TZ=Asia/Shanghai

EXPOSE 5211 42069/tcp 42069/udp

VOLUME ["/app/data", "/app/strm", "/app/mounts"]

ENTRYPOINT ["/app/litepan"]
