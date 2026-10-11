# 开发构建

Node、Go、pnpm 的版本统一由 [mise](https://mise.jdx.dev) 在项目根目录管理，所有命令都是 `mise task`：

```bash
mise trust                  # 首次拉取项目级工具链（node 24 / go 1.27.2 / pnpm 12）
mise run                    # 列出全部任务

mise run web:install        # 安装前端依赖（装在 web/ 下）
mise run web:dev            # 前端开发服务器（/api 代理到 127.0.0.1:5211）
mise run web:type-check     # 前端类型检查
mise run web:build          # 前端构建，产物写入 internal/api/web
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

## 目录约定

- **前端依赖只装在 `web/`**：`web/node_modules`、`web/pnpm-lock.yaml`、`web/pnpm-workspace.yaml`，仓库根目录不会出现 `node_modules`。任务用 `task_templates."web"` 统一切到 `web/` 并把 `web/node_modules/.bin` 加进 PATH。
- **命令定义只有一处**：在 `mise.toml`，直接调用 `vite`、`vue-tsc`、`node`，不经 pnpm 转发 `web/package.json` 的 `scripts`（该字段已删除）。
- **`web/package.json` 的版本写死**（不带 `^`），沿用 v0.5.7 验证过的组合；升级依赖请单独开一个提交并跑一遍 `mise run web:build`。

## 前端产物不入 Git

`internal/api/web/` 是 `//go:embed web` 的内嵌目录，仓库里**只保留一个占位 `index.html`**，让未构建前端时也能编译：

- `mise run web:build` 的产物直接覆盖这个 `index.html`，构建后 `git status` 会看到它变更，**不要提交**，其余产物（`assets/`、`static/` 等）由 `.gitignore` 排除。
- 直接跑 `mise run build` 得到的二进制只内嵌占位页，浏览器打开会提示“前端尚未构建”，API 不受影响。
- Dockerfile 不构建前端：先在本地跑 `mise run web:build`，`mise run docker:build` 会自动先构建前端再打镜像，镜像直接 `COPY . .` 把这份产物内嵌进二进制（`.dockerignore` 已排除 `web/node_modules`、`dist/` 等）。

## 镜像里的工具链版本

Go / Node 的版本只在 `mise.toml` 的 `[tools]` 里定义一次，构建镜像时由任务传入：

```bash
mise run docker:build
# 实际执行：docker build --build-arg GO_VERSION=1.27.2 --build-arg NODE_VERSION=24.21.0 …
```

Dockerfile 用 `ARG GO_VERSION` / `ARG NODE_VERSION` 接收，所以改版本只改 `mise.toml`；Dockerfile 里的默认值只是给不用 mise、直接 `docker build` 的人留的。停用的 node 前端阶段也预留了 `${NODE_VERSION}`，将来恢复时不用再改。

## 个人专属命令

放 `mise.local.toml`，这个文件已被 `.gitignore` 排除，优先级高于 `mise.toml`，两者合并生效：需要新增命令就只写任务，需要覆盖已有任务就写同名任务。注意同名任务以 `mise.local.toml` 为准、不逐字段合并，`depends` 这类依赖关系要在这里重写一遍。例如导出镜像给 NAS：

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
> 任务里的 `{{...}}` 会被 mise 当模板解析，docker 的 `--format='{{.Id}}'` 这类写法会直接报错，用 `[env]` 变量（`$IMAGE_NAME`）代替。
