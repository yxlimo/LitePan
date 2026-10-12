# 开发构建

本文说明本地开发与镜像构建使用的命令和约定。所有命令都在仓库根目录执行，前端任务在内部切换到 `web/` 目录。

## 前置条件

1. 安装 [mise](https://mise.jdx.dev)。
2. 在仓库根目录执行 `mise trust`。`mise trust` 只负责信任 `mise.toml`，不下载工具链。
3. 执行 `mise run` 或 `mise install`。mise 按 `mise.toml` 的 `[tools]` 下载工具链：Node 24、Go 1.27.2、pnpm 12.10.1。
4. 执行 `mise run web:install` 安装前端依赖。

## 常用命令

```bash
mise trust                  # 信任项目配置
mise run                    # 列出全部任务（首次执行会下载工具链）

mise run web:install        # 安装前端依赖（装在 web/ 下）
mise run web:dev            # 前端开发服务器（/api 代理到 127.0.0.1:5211）
mise run web:type-check     # 前端类型检查
mise run web:build          # 前端构建，产物写入 internal/api/web
mise run web:placeholder    # 只生成未构建时的占位页
mise run web:test           # 前端测试脚本（上传派发器 + 任务树）

mise run lint:install       # 安装 golangci-lint
mise run lint               # golangci-lint
mise run test               # 后端测试（-race）
mise run build              # 后端构建（fuse 标签）
mise run build:nofuse       # 后端构建（无 fuse 标签）

mise run docker:build       # 构建镜像 yxlimo/litepan-strmforge:dev（依赖 web:build，镜像直接内嵌本地产物）
mise run docker:up          # docker compose 起服务
mise run docker:down        # docker compose 停服务
```

`mise run docker:build` 完成后，本地会生成镜像 `yxlimo/litepan-strmforge:dev`。

## 前端依赖与命令定义

- 前端依赖只安装在 `web/`：`web/node_modules`、`web/pnpm-lock.yaml`、`web/pnpm-workspace.yaml`。仓库根目录不会出现 `node_modules`。
- 前端任务通过 `task_templates."web"` 统一切换到 `web/`，并把 `web/node_modules/.bin` 加入 `PATH`。
- 命令只在 `mise.toml` 中定义一次：任务直接调用 `vite`、`vue-tsc`、`node`，不通过 pnpm 转发 `web/package.json` 的 `scripts`。`web/package.json` 不再包含 `scripts` 字段。
- `web/package.json` 中构建链路相关的依赖（`vite`、`vue-tsc`、`vue`、`pdfjs-dist` 等）写死版本，不带 `^`；其余依赖仍使用 `^`。升级依赖后执行一次 `mise run web:build` 验证。

## 前端产物不入 Git

`internal/api/web/` 是 `//go:embed web` 的内嵌目录，整个目录不入库，由 `.gitignore` 排除。

- 未构建前端时，目录内没有文件，`go:embed` 会报 `pattern web: no matching files found`。`mise run web:placeholder` 负责生成占位页：从 `web/build/placeholder.html` 复制到 `internal/api/web/index.html`，目录已存在该文件时不覆盖。
- `build`、`build:nofuse`、`test`、`lint`、`docker:build` 都依赖 `web:placeholder`。裸 `go build ./...` 不经过 mise，在全新 clone 上会失败，先执行一次 `mise run web:placeholder` 或任意后端任务即可。
- 执行 `mise run web:build` 后，产物直接写入 `internal/api/web/`，包含真实构建的 `index.html`。产物不进入 Git，`git status` 不会出现内嵌目录。
- 直接执行 `mise run build` 得到的二进制只内嵌占位页，浏览器打开会提示「前端尚未构建」，接口不受影响。
- Dockerfile 不构建前端。`mise run docker:build` 先执行 `web:build`，再用 `COPY . .` 把产物内嵌进二进制。`.dockerignore` 已排除 `web/node_modules`、`dist/` 等目录。

## 镜像里的工具链版本

Go 和 Node 的版本只在 `mise.toml` 的 `[tools]` 中定义一次。构建镜像时，任务通过 `--build-arg` 把实际版本传给 Dockerfile：

```bash
mise run docker:build
# 实际执行：docker build --build-arg GO_VERSION=1.27.2 --build-arg NODE_VERSION=24.21.0 …
```

Dockerfile 用 `ARG GO_VERSION` 和 `ARG NODE_VERSION` 接收参数。修改版本时只需改 `mise.toml`；Dockerfile 中的默认值只供不通过 mise、直接执行 `docker build` 的情况使用。Dockerfile 中已停用的 node 前端构建阶段也预留了 `${NODE_VERSION}`，恢复该阶段时无需再改。

## 个人专属命令

个人命令写在 `mise.local.toml`。`mise.local.toml` 已被 `.gitignore` 排除，加载优先级高于 `mise.toml`，两份配置合并生效。

- 新增命令：只在 `mise.local.toml` 中写任务。
- 覆盖已有命令：写同名任务。同名任务以 `mise.local.toml` 为准，不逐字段合并，`depends` 等依赖关系需要在该文件中重写。

以下示例把镜像导出到 NAS 并在 NAS 上导入：

```toml
[tasks."docker:export"]
description = "构建镜像、导出 tar.gz、推到 NAS 并 docker load 导入"
depends = ["docker:build"]
run = [
  "docker save \"$IMAGE_NAME\" | gzip > dist/app-dev.tar.gz",
  "scp dist/app-dev.tar.gz \"$NAS_HOST:$NAS_IMAGE_DIR/\"",
  "ssh \"$NAS_HOST\" \"docker load -i $NAS_IMAGE_DIR/app-dev.tar.gz\"",
]
```

> [!NOTE]
> mise 会把任务里的 `{{...}}` 当作模板解析。`docker --format='{{.Id}}'` 这类写法会直接报错，请改用 `[env]` 中定义的变量，例如 `$IMAGE_NAME`。
