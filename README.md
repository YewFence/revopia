# revopia

`revopia` 是一个配合 Kopia 使用的 Docker volume 挂载桥。它通过 Docker API 为带标签的 Docker volume 创建短生命周期 helper 容器，把这些 volume 暴露到 Kopia 容器里的稳定路径。

名字 `revopia` 是从旧项目 `yew-resin`、Docker volume 和 Kopia 里各取一部分拼出来的自造词。`re` 延续 resin 的命名来源，`vo` 指向 volume，`pia` 来自 Kopia，也对应这个工具把 named volume 通过挂载桥交给 Kopia 管理的定位。

Kopia 仍然负责仓库、策略、快照、保留规则和恢复。本项目只负责在备份前准备路径，在备份后回收 helper 和传播挂载。

## 适用场景

这个工具适合已经用 Kopia 备份文件系统，并且希望把 Docker named volume 纳入同一套 Kopia policy 的 Linux Docker Engine 环境。

不适合 Docker Desktop，不适合不允许挂载传播的容器平台，也不负责数据库一致性。数据库、消息队列、状态服务仍然需要自己的 flush、锁定、暂停写入或维护窗口方案。

## 工作方式

```
宿主机 /mnt/revopia
        |
        | 传播到 Kopia 容器
        v
Kopia /volumes/<friendly-name>   只读备份视图
Kopia /restore/<friendly-name>   可写恢复视图

helper 容器
  /bridge                         rshared bind mount
  /bridge/<friendly-name>         Docker volume 只读挂载
  /bridge/restore/<friendly-name> Docker volume 可写挂载
```

`prepare` 会扫描带有 `backup.enable=true` 标签的 Docker volume。每个符合条件的 volume 都会得到一个 helper 容器，helper 容器把 volume 只读挂载到 `/bridge/<friendly-name>`，这个子挂载会通过宿主机 bridge 目录传播到 Kopia 容器的 `/volumes/<friendly-name>`。

`cleanup` 会删除本项目管理的备份 helper 容器，并尝试回收传播出来的挂载点。普通卸载失败时会保留现场并给出诊断，只有显式传入 `--dangerously-lazy-umount` 才会使用 lazy unmount。

`restore` 不会直接调用 Kopia。它会准备一个目标 Docker volume，并把它可写暴露到 `/restore/<friendly-name>`，然后打印推荐的 `kopia snapshot restore` 命令。

## 环境要求

| 项目 | 要求 |
| --- | --- |
| 操作系统 | Linux |
| Docker | Linux Docker Engine，支持 bind mount propagation |
| Kopia | 需要启用 actions，才能在 snapshot 前后运行 `revopia` |
| Docker API | `revopia` 所在环境需要访问 Docker API |
| helper 镜像 | 默认 `alpine`，需要包含 `sh`、`find`、`grep`、`umount` |

## 安装

开发环境建议通过 mise 使用仓库固定的 Go 和 lint 工具。

```bash
mise run build
./bin/revopia version
```

不用 mise 时可以直接构建。

```bash
go build -trimpath -ldflags='-s -w -X main.version=dev' -o bin/revopia .
```

生产 compose 文件提供了一个安装 profile，会把 release 二进制下载到 `revopia-tools` volume 中，Kopia 容器通过 `/tools/revopia` 使用它。

```bash
docker compose --profile install run --rm revopia-install
docker compose up -d
```

安装 profile 会调用仓库里的安装脚本。脚本默认安装 GitHub latest release，并使用 GitHub Release asset 的 sha256 digest 校验下载内容。如果要安装指定版本，可以设置 `VERSION=v0.1.0`。

也可以直接把二进制下载到当前目录。

```bash
curl -fsSL https://raw.githubusercontent.com/yewfence/revopia/main/scripts/install.sh | sh
```

如果使用源码构建后的本地开发配置，可以运行下面的命令。

```bash
mise run build
docker compose -f compose.dev.yaml up -d
```

## 标记要备份的 volume

只有带 `backup.enable=true` 标签的 volume 会进入备份视图。

```bash
docker volume create app-data \
  --label backup.enable=true \
  --label backup.name=app-data

docker volume create db-data \
  --label backup.enable=true
```

`backup.name` 是可选标签，用来指定 Kopia 中看到的目录名。没有这个标签时，会使用 Docker volume 名称清洗后的结果。清洗规则只保留字母、数字、点、下划线和短横线，空格等字符会变成短横线。

如果两个 volume 清洗后得到同一个路径名，`prepare` 会报错，避免把两个 volume 暴露到同一个 Kopia 路径。

## 部署 Kopia

> 以下的代码使用了 `alias` 以简化操作。如果你使用了 Mise 且已经 Activate，`alias` 会自动加载，详见 [mise.toml](mise.toml)。
```bash
alias kopia="docker compose exec -T kopia kopia"
alias exec-kopia="docker compose exec kopia"
alias revopia="docker compose exec -T kopia revopia"
```

仓库里的 `compose.yaml` 是生产方向的参考配置。它通过 `docker-socket-proxy` 暴露有限 Docker API 给 Kopia 容器，默认把宿主机的 `/mnt/revopia` 挂到 `/volumes`，把 `/mnt/revopia/restore` 挂到 `/restore`。这个 bridge 路径不需要提前手动创建或重新挂载，Docker 会在 compose bind mount 和 helper bind mount 阶段处理它。

首次使用 Kopia 时，需要按 Kopia 正常流程创建或连接仓库。使用仓库里的 compose 默认文件系统仓库时，路径是 `/repository`。

```bash
kopia repository create filesystem \
  --path=/repository \
  --enable-actions
```

如果仓库已经存在，可以改用 `repository connect`，也要带上 `--enable-actions`。

## 配置 Kopia policy

给 `/volumes` 设置 snapshot 前后动作。备份开始前运行 `prepare`，备份结束后运行 `cleanup`。

```bash
kopia policy set /volumes \
  --before-snapshot-root-action="revopia prepare" \
  --after-snapshot-root-action="revopia cleanup" \
  --one-file-system=false
```

然后照常让 Kopia 创建快照。

```bash
kopia snapshot create /volumes
kopia snapshot list /volumes
```

也可以只备份某一个 volume 对应的路径。

```bash
# kopia 无法快照不存在的目录，所以需要预先创建空目录，挂载点会覆盖它
exec-kopia mkdir -p /volumes/app-data
kopia snapshot create /volumes/app-data
```

## 恢复数据

恢复是显式流程。先用 `restore` 准备目标 Docker volume，再执行 Kopia 恢复命令，最后用 `restore-cleanup` 清理恢复 helper 和挂载。目标 volume 不会被 `restore-cleanup` 删除。

```bash
revopia restore app-data \
  --target-volume app-data-restore-20260509
```

命令会输出 `RESTORE_SESSION_ID`、`RESTORE_TARGET_PATH` 和推荐的 Kopia 命令。按输出执行恢复即可，常见路径恢复命令类似下面这样。

```bash
kopia snapshot list /volumes/app-data
kopia snapshot restore \
  /volumes/app-data \
  /restore/app-data \
  --snapshot-time latest
```

恢复结束后清理本次恢复会话。

```bash
revopia restore-cleanup \
  --session <restore-session-id> \
  --yes
```

默认情况下，`restore` 会自动创建 `SOURCE_VOLUME-restore-时间戳` 形式的新目标 volume。它会拒绝直接写回源 volume，也会拒绝复用非空目标 volume。确实要覆盖这些保护时，必须显式使用 `--dangerously-allow-source-target` 或 `--dangerously-allow-non-empty-target`。

## 命令参考

| 命令 | 用途 |
| --- | --- |
| `prepare` | 扫描 `backup.enable=true` 的 volume，创建或复用备份 helper |
| `cleanup` | 删除备份 helper，并回收 `/volumes/<friendly-name>` 的传播挂载 |
| `restore SOURCE_VOLUME` | 准备恢复目标 volume，并暴露到 `/restore/<friendly-name>` |
| `restore-cleanup` | 清理恢复 helper 和恢复挂载，不删除目标 volume |
| `inspect` | 查看配置、启用备份的 volume、helper 容器和可见路径状态 |
| `completion` | 生成 Bash、Zsh、Fish 或 PowerShell 补全脚本 |
| `version` | 输出版本号 |

常用全局配置可以通过环境变量设置，也可以用同名命令行参数覆盖。

| 环境变量 | 参数 | 默认值 |
| --- | --- | --- |
| `REVOPIA_BRIDGE_SOURCE` | `--bridge-source` | `/mnt/revopia` |
| `REVOPIA_VISIBLE_ROOT` | `--visible-root` | `/volumes` |
| `REVOPIA_RESTORE_ROOT` | `--restore-root` | `/restore` |
| `REVOPIA_HELPER_IMAGE` | `--helper-image` | `alpine` |
| `REVOPIA_LOG_FILE` | `--log-file` | `/app/logs/revopia.log` |

## 诊断和清理

先用 `inspect` 看当前状态。

```bash
docker compose exec kopia revopia inspect
```

如果普通清理报告 `device or resource busy`，优先确认没有 Kopia 任务、业务容器或 shell 工作目录占用 bridge 路径。确认无占用后，再显式使用 lazy unmount。

```bash
docker compose exec kopia revopia cleanup --dangerously-lazy-umount
docker compose exec kopia revopia restore-cleanup \
  --session <restore-session-id> \
  --yes \
  --dangerously-lazy-umount
```

更详细的 busy volume 排查流程见 [TROUBLESHOOTING.md](TROUBLESHOOTING.md)。

## 安全边界

所有对外暴露的 volume 路径名都会经过清洗，不会直接把 Docker label 拼进文件路径。

备份 helper 默认只读挂载源 volume，禁用网络，并用本项目标签标记。清理逻辑只处理带 `revopia=true` 的容器。

cleanup 容器只在需要卸载传播挂载时短暂运行，禁用网络，使用只读根文件系统，丢弃默认 capability 后只添加 `CAP_SYS_ADMIN`。

恢复目标 volume 默认不会继承 `backup.enable=true`，避免下一轮备份把恢复卷自动纳入备份。

## 开发

```bash
mise run test
mise run build
mise run check
mise run test-e2e
```

`mise run test-e2e` 会启动真实 Kopia 和 Docker volume 进行端到端测试，需要本机 Docker daemon 可用。

## 许可证

MIT
