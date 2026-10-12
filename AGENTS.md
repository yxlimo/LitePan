# AGENTS.md

本文件只记录可执行约定。命令定义在 `mise.toml`，本文不重复任务步骤。背景说明见 [docs/developer.md](docs/developer.md)。

## 版本与依赖

- 只在 `mise.toml` 修改工具链版本。不要在 `Dockerfile`、`go.mod`、`.golangci.yml`、文档中另写一份版本号。
- 前端依赖只安装在 `web/`。不要在仓库根目录安装依赖，不要创建根目录的 `package.json`。
- 个人命令写在 `mise.local.toml`，不要写进 `mise.toml`。

## 前端产物

- 不要提交 `internal/api/web/` 下的任何文件。需要占位页时执行 `mise run web:placeholder`。
- 新增 Go 侧任务时加 `depends = ["web:placeholder"]`。
- 不要在 Go 代码中为未构建前端添加回落逻辑。
- 升级前端依赖后执行 `mise run web:build` 验证。

## mise

- 首次进入仓库执行 `mise trust`。
- 列出任务用 `mise tasks`。仓库没有 `default` 任务，裸 `mise run` 会报错。
- 带冒号的任务名写成 `[tasks."web:build"]`。
- 不要给任务添加 `sources`。
- 任务中不要出现 `docker --format='{{...}}'`。改用 `[env]` 中定义的变量。
- 修改 `web/` 下任务的公共配置时改 `task_templates."web"`，不要逐个任务复制 `dir`。

## Docker

- 基础镜像使用 `docker.io/library/` 全限定名。
- 不要在 `Dockerfile` 中写死 Go 或 Node 版本，改用已有的 `ARG`。
- 执行 `docker rmi` 前确认目标镜像还有其他 tag，否则镜像会被一并删除，需要重新构建。

## 命令行

- 不要用 `pkill -f <模式>` 清理进程。删除容器用 `docker rm -f <容器名>`。
- 长输出命令先重定向到文件，再查看摘要。
- 工具调用失败时先确认工具名，不要重复调用同一个名字。

## 验证

- 提交前执行 `mise run test` 和 `mise run lint`。
- 更换 Go 版本后先执行 `mise run lint:install`。
- 改动前端或 Dockerfile 后执行 `mise run docker:build`，并启动容器确认首页返回完整 SPA。
