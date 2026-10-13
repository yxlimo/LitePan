# podman 本机构建镜像的 `localhost/` 前缀

排查日期：2026-10-12。环境：WSL，podman 5.7.0（`/usr/bin/docker` 就是 podman），Docker Compose v2.40.3 通过 podman socket 通信。

## 现象

本地构建出的镜像是：

```
localhost/yxlimo/litepan-strmforge:dev
```

而 `docker-compose.yml` 里写的是无前缀的 `yxlimo/litepan-strmforge:dev`。直接 `docker compose up` 时 compose 判定本地没有该镜像，转去 Docker Hub 拉取并失败：

```
Pull error for image: yxlimo/litepan-strmforge:dev
pull access denied, repository does not exist or may require authorization
```

## 结论：没有配置可以关掉这个前缀

`podman build --tag` 的行为是写死在命令语义里的，不是可配置项。手册原文：

> Specifies the name which is assigned to the resulting image if the build process
> completes successfully. If imageName does not include a registry name, the
> registry name localhost is prepended to the image name.

这是有意为之：镜像全名要能说明它从哪来，podman 不肯默认把本地构建产物挂到 `docker.io` 名下。`unqualified-search-registries`、`short-name-mode` 这些 `containers.conf` / `registries.conf` 里的选项只影响**短名解析**（例如 `podman pull alpine` 去哪里找），不影响 `podman build` 打标签时补前缀。相关 issue containers/buildah#1034 至今未关闭。

副作用：`podman pull localhost/xxx:dev` 仍会尝试连 `https://localhost/v2/`，并不会因为镜像在本地就跳过。

## 真正的问题在 compose 的名字归一化

compose 自己做归一化：短名按 Docker 规则补成 `docker.io/yxlimo/...`，然后拿这个全名去查本地镜像。podman 里存的是 `localhost/yxlimo/...`，对不上，于是判定“本地没有”→ 去 Hub 拉。

对比验证（同一台机器）：

| 命令 | 短名 `yxlimo/litepan-strmforge:dev` | 全名 `localhost/yxlimo/litepan-strmforge:dev` |
| --- | --- | --- |
| `podman image inspect` | 找到 | 找到 |
| `docker compose pull` | 去 Hub 拉，失败 | — |

podman 自己解析短名时用的也是 localhost（`registries.conf` 未配 `unqualified-search-registries`），所以 `docker image inspect` 短名能过；只有 compose 归一化到了 docker.io。

## 办法一：compose 写 `localhost/`（本机专用，不通用）

compose 文件里写全名 `localhost/`，并配 `pull_policy: never`，这样 compose 不再尝试拉取：

```yaml
services:
  litepan-strmforge:
    image: localhost/yxlimo/litepan-strmforge:dev
    pull_policy: never
```

验证结果（dry-run）：

```
DRY-RUN MODE -  litepan-strmforge Skipped        # compose pull
DRY-RUN MODE -  Container litepan-strmforge  Created   # compose up
```

两个注意点：

- 全名带 `localhost/` 后镜像只在本机可见，符合“本机构建产物”的定位；真要推到 Hub 得单独 `podman tag` 成 `docker.io/yxlimo/...` 再 push。
- `pull_policy: never` 表示镜像必须先本地存在，否则直接失败而不是自动拉。要保留“缺了再拉”的行为就用 `pull_policy: missing`。
- 换到装真 Docker 的机器上会反过来坏掉：那边镜像不带 `localhost/`，compose 找不到，还会去连 `localhost` registry。

## 办法二（采用）：构建时直接打 `docker.io/` 全名

compose 的归一化是固定的一步，短名一律补成 `docker.io/yxlimo/...` 再查本地。Docker 用户的本地 tag 恰好就是这个全名，本来就是好的；podman 存成 `localhost/yxlimo/...` 才错位。让 podman 补出同一个名字，问题从两端一起消失，且 `docker-compose.yml` 一个字都不用改。

`mise.toml` 的 `docker:build` 改为：

```toml
run = "mkdir -p dist && docker build --platform linux/amd64 --build-arg GO_VERSION={{tools.go.version}} --build-arg NODE_VERSION={{tools.node.version}} -t docker.io/yxlimo/litepan-strmforge:dev ."
```

验证（用仓库里未修改的 `docker-compose.yml`）：

```
podman tag localhost/yxlimo/litepan-strmforge:dev docker.io/yxlimo/litepan-strmforge:dev

docker compose pull --dry-run   → 仍尝试拉 Hub（pull 本来就会拉，可忽略）
docker compose up   --dry-run   → 正常，无拉取
```

对照实验，确认 dry-run 确实会暴露拉取行为（同样的命令换成本地不存在的镜像就报错去拉）：

```
image: yxlimo/definitely-not-here:dev
DRY-RUN MODE -  x Pulling
Error authorization failed
```

对两端的效果：

- podman：存成 `docker.io/yxlimo/...`，compose 命中。
- Docker：`docker build -t docker.io/...` 是正常写法，本地 tag 就是该全名，compose 同样命中；本机若无该镜像，去 Hub 拉的行为与现在一致。

`docker-compose.fnos.yml` 用的是 `ponphil/litepan:beta`（上游 Hub 镜像），不受影响。

## 名字改成什么样都没用

“短名”的判定与名字长短无关：**第一段里含 `.` 或 `:`（或恰好是 `localhost`）才被当成 registry 主机名**，否则整名算短名，podman 照样补 `localhost/`。实测：

| 写入的名字 | 实际存成 |
| --- | --- |
| `yxlimo-dev/foo:dev` | `localhost/yxlimo-dev/foo:dev` |
| `yxlimo-dev.example.com/bar:dev` | 原样保留 |
| `yxlimo-dev:5000/baz:dev` | 原样保留 |

加点或冒号能骗过 compose 的“是不是全名”判断，但那是把名字伪装成 registry 主机，真拉取时会去连那个假主机。

## 发布到 Hub：不用改名

`docker.io/yxlimo/litepan-strmforge` 既是本地存储名也是推送目标地址，`podman push docker.io/yxlimo/litepan-strmforge:dev` 直接可用，不关心图是怎么来的。反过来，`localhost/` 命名下 `podman push localhost/...` 会去连 `https://localhost/v2/`，那种命名才必须改名。

发布时：

- 先 `podman login docker.io`（`yxlimo` 是用户自己的 Hub 命名空间）。
- 正式发布不复用 `:dev`，改成 `-t docker.io/yxlimo/litepan-strmforge:v0.1.0`；名字不变，只改 tag。
- `--platform linux/amd64` 保留即可，单架构镜像直接 push 就是单条 manifest。将来若要在 arm 机器上同时发 amd64 + arm64，才需要 `podman manifest create/add` 拼 manifest list，与命名无关。
- 附带好处：buildah#5158（`--pull=always` 遇到 `localhost/` 名字会去连 localhost registry）在 `docker.io/` 命名下自动消失。

## 其他方向（未采用）

- 给 podman 换套别名去掉前缀：不存在这种配置，任何未限定 registry 的名字都会被打上 `localhost/`。
- 在 `registries.conf` 把 `yxlimo` 加进 `unqualified-search-registries`：只改 podman 的短名查找顺序，对 compose 无效。
- 用 `podman-compose` 替代 Docker Compose v2：可以绕开 docker.io 归一化，但会丢掉 v2 的大部分功能，且引入第二套 compose 语义，不划算。