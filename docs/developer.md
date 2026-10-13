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

mise run test:scanflow       # 扫描流程的端到端集成测试（需 tests/rel 素材；独立于单测）
mise run test:scanflow:rebuild  # 强制重建集成测试的基线库
mise run dev:seed           # 准备 tests/ 测试数据（幂等，可反复执行）
mise run dev:run            # 本地启动服务（端口 5211），Ctrl+C 停止

mise run docker:build       # 构建镜像 docker.io/yxlimo/litepan-strmforge:dev（依赖 web:build，镜像直接内嵌本地产物）
mise run docker:up          # docker compose 起服务
mise run docker:down        # docker compose 停服务
```

`mise run docker:build` 完成后，本地会生成镜像 `docker.io/yxlimo/litepan-strmforge:dev`。镜像名写全 registry 是有意为之：podman 不会给含 registry 的名字补 `localhost/` 前缀，而 docker compose 归一化后也是这个名字，两端对齐后本地 `docker compose up` 不会再误判成「本地没有镜像」而跑去 Docker Hub 拉。背景见 `docs/podman-local-image-name.md`。

## 待确认删除的接口约定

扫描没读到的文件会先打墓碑，再由用户在界面上决定去向。两个接口的入参一致：

```jsonc
POST /api/strmforge/tasks/{id}/deletions/confirm   // 物理移除
POST /api/strmforge/tasks/{id}/deletions/restore    // 标记误报，恢复为活跃

{ "rel_paths": ["tv/剧集/S01E01.mkv"], "all": false }  // 按路径处理
{ "rel_paths": [], "all": true }                       // 全部处理
```

`rel_paths` 与 `all` 必须至少给一个：`rel_paths` 为空且 `all` 不为 true 时直接返回 400。空数组曾经被当成「全部」，一次误调用就会清掉所有墓碑，与墓碑机制「防止把网盘没读到的文件误判为删除」的初衷矛盾，因此改成必须显式声明意图。前端在未勾选任何行时禁用两个按钮，就是走这条规则。

## 本地开发环境

`mise run dev:run` 直接跑二进制，不走 Docker，监听端口 `5211`。运行数据全部落在 `tests/`（已入库忽略）：

```text
tests/media/   ffmpeg 生成的测试媒体，命名与结构贴近真实网盘，由 dev:seed 产出
tests/rel/     真实样本，从影视库压缩而来，结构与命名更接近真实场景
tests/data/    数据库、密钥、日志、缓存
tests/strm/    STRM 输出目录
```

`mise run dev:seed`（`dev:run` 会自动先跑）做三件事，均可反复执行：

1. 用 ffmpeg 生成 22 个测试视频（每个 3 秒、320x180、带静音 AAC 音轨），命名与
   目录结构按真实 PT 组习惯拼装，文件已存在则保留原内容；
2. 执行数据库迁移；**仅在首次运行时**写入管理员口令 `admin / DevAdmin-2026!`，库里已有口令时不覆盖，需要重置加 `-force-admin-password`；
3. 注册本地目录账号：「本地测试目录」指向 `tests/media`，「本地影视库」指向 `tests/rel`（目录不存在时跳过）。这些账号走 `localfs` 驱动，无需认证，适合测试不依赖具体网盘的功能。

前端改动后需要重新 `mise run web:build`，因为服务内嵌是构建产物；只改后端时直接重启 `mise run dev:run` 即可。

`tests/rel` 的样本来自 `tools/testdata/prep-from-nas.sh`：在远端机器上挑 10 部电影、1 部系列合集、1 个裸文件，以及 8 部剧集（多季则每季取 3 集，整季的有 13 集），共 68 个文件。用 ffmpeg 压成 360p、3 分钟、**保留音轨与字幕流**，整套约 234 MB，可用于扫描、diff 与播放测试。重建方式：

```bash
# 远端机器上跑，约 35 分钟
SRC=/vol3/1000/ptfiles/origin OUT=/tmp/.strmforge-test bash tools/testdata/prep-from-nas.sh
# 本地
rsync -a nas:<OUT>/ tests/rel/
```

样本覆盖的命名方式：点分隔、空格、方括号、圆括号、中文与英文混排、分号、`@`、`&`、上标 `³`；扩展名有 `.mkv`、`.mp4`、`.ts`。目录结构上：电影有「单目录单文件」与「裸文件直接躺在 `movie/` 下」两种，剧集既有平铺，也有「剧集 / 季度 / 文件」两层嵌套，因此最深需要 `max_depth=3`。

音频统一转 AAC 48k 立体声；字幕用 `-c:s copy` 原样保留，所以文本字幕（ass）与位图字幕（hdmv_pgs_subtitle）都会在，读 media info 时能看到真实存在的字幕流。源片本身没有字幕的文件就自然没有，不是抽样丢失。

`tests/media` 则由 `mise run dev:seed` 用 ffmpeg 现造，同样按 PT 组习惯拼装命名，覆盖的结构有：单目录同名文件、裸文件直接躺在目录里、`[中文名].englishName`、`韩文名.englishName`、目录名带 `S01` 而文件名是 `S01Exx`、剧集 / 季目录 / `S01EPxx` 三层、目录名用「第N季 + 年份」而文件名是 `E01+日期`、目录名不带季号而文件名是 `EPxx`、目录名带空格与全角括号而文件名是纯中文加序号、以及多部合集。来源、分辨率、编码、音频与制作组按固定种子随机组合，保证每次生成的命名完全一致，测试才可复现。

本地目录驱动与网盘驱动的差异，写扫描类逻辑前要知道：

| 差异点 | `localfs` | 网盘驱动 |
| --- | --- | --- |
| file_id | 就是绝对路径（`domain.IDPath`），根目录为空串 | 不透明字符串 |
| 文件改名 | id 随之变化 | 通常保持不变 |
| 认证 | 无（AuthNone），不会进冷却 | 有 token 刷新与冷却，失败要重试 |

## 前端依赖与命令定义

- 前端依赖只安装在 `web/`：`web/node_modules`、`web/pnpm-lock.yaml`、`web/pnpm-workspace.yaml`。仓库根目录不会出现 `node_modules`。
- 前端任务通过 `task_templates."web"` 统一切换到 `web/`。
- `web/node_modules/.bin` 由顶层 `[env]` 的 `_.path` 加入 `PATH`，对所有任务生效，任务内可直接调用 `vite`、`vue-tsc`。
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

## 扫描流程的集成测试

`mise run test:scanflow` 跑一组端到端测试：**编译并启动真实二进制，全部经 HTTP 接口操作**，覆盖驱动注册、鉴权、数据库迁移与扫描全链路，而不是在进程内拼装 handler。

它与日常单测是分开的：文件带 `//go:build integration` 标签，`mise run test`（`go test -race ./...`）不会编译它，必须显式跑 `test:scanflow`。素材用 `tests/rel` 的副本，每个用例都从原始素材重新拷贝，互不影响。

六个场景按顺序执行：

| 用例 | 验证内容 |
| --- | --- |
| 01 | 空库全量扫描，结果与磁盘文件逐条一致；成功后**固化基线库** |
| 02 | 删掉 movie 与 tv 各一个文件 → 墓碑生效 → 确认删除后结果里消失 |
| 03 | 把两个文件移走 → 墓碑生效 → 放回并标记误报 → 结果依旧保留 |
| 04 | 新增文件 → 扫描后出现在结果里 |
| 05 | 重命名文件 → 表现为「新增 1 + 墓碑 1」 |
| 06 | 移动文件到其他目录 → 同样表现为「新增 1 + 墓碑 1」 |

### 基线库是固化产物

只有 01 需要空库，它跑完后把数据目录存成基线；**之后每次运行都直接复用基线，01 会被跳过**，与 01 再无关系。因此日常跑一次只需 6 秒左右，而不是每次都重跑全量扫描。

```
/tmp/strmforge-e2e/
├── baseline/          固化下来的基线（litepan.db + corpus.json 素材指纹）
├── work-corpus/       每个用例重新拷贝的工作素材
└── empty.db           空库，用到时才现建
```

素材变动会让指纹对不上，测试直接中止并提示重建——否则旧基线会把「素材本身的差异」误报成扫描差异，排查很费时间：

```bash
mise run test:scanflow:rebuild   # 或删掉 /tmp/strmforge-e2e/baseline
```

`test:scanflow` 只清理 `empty.db` 与 `work-corpus`，保留 `baseline/`。

### 环境变量

- `STRMFORGE_E2E_DB`：**必需的**临时目录路径，测试在其下生成上述内容。未设置直接失败，避免误用开发库。
- `STRMFORGE_E2E_CORPUS`：素材目录，默认 `<仓库根>/tests/rel`。
- `STRMFORGE_E2E_REBUILD`：设为 1 时强制重建基线（等价于 `test:scanflow:rebuild`）。

账号与登录属于前置条件，不在测试范围内：每个用例的数据目录由测试自己建好，并直接写入管理员口令与 localfs 账号；任务、扫描、待确认删除等业务动作一律走 API。

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
