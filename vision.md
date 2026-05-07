# Kopia Docker Volume Bridge Vision

## 目标

这个项目的目标是让 Kopia 继续用自己的 policy 管理备份路径、调度、保留策略和排除规则，同时用一个很小的 Go 可执行文件在备份前把带有 `backup.enable=true` 标签的 Docker volume 暴露到 Kopia 容器里的稳定目录。

Go 程序不负责组织 Kopia 的备份计划，也不主动调用 `kopia snapshot create`。它只做一件事，把 Docker volume 准备成 Kopia policy 可以看到的目录。

## 推荐模型

推荐使用一个宿主机 bind mount 目录作为挂载传播桥，例如 `/mnt/volumes-backup`，并把它挂到 Kopia 容器中的 `/volumes`。

Kopia 容器只读取 `/volumes/<name>`，其中 `<name>` 来自 volume 的 `backup.name` 标签。如果该标签为空，则回退到 volume 名称经过清洗后的结果。

Go 程序通过 Docker API 扫描所有带有 `backup.enable=true` 标签的 volume，然后为每个 volume 创建一个临时 helper 容器。helper 容器不需要执行 `mount --bind`，也不需要 `--privileged`，而是让 Docker daemon 在创建容器时完成挂载。

helper 容器的挂载结构如下。

```bash
docker run --rm -d \
  --name kopia-volume-bridge-<hash> \
  --label kopia.volume-bridge=true \
  --label kopia.volume-bridge.volume=<volume-name> \
  --mount type=bind,source=/mnt/volumes-backup,target=/bridge,bind-propagation=rshared \
  --mount type=volume,source=<volume-name>,target=/bridge/<friendly-name>,readonly \
  alpine sleep infinity
```

这个模型的关键点是，`/bridge` 是带传播能力的宿主机 bind mount，而目标 volume 被 Docker daemon 挂载到 `/bridge/<friendly-name>` 这个子路径。挂载事件通过 bridge 传播到宿主机，再传播到 Kopia 容器中的 `/volumes/<friendly-name>`。

## Kopia 集成方式

Kopia 的备份前脚本调用 Go 程序的 prepare 命令，确保所有启用备份的 volume 都已经出现在 `/volumes/<name>`。

Kopia 的备份后脚本调用 Go 程序的 cleanup 命令，停止带有 `kopia.volume-bridge=true` 标签的 helper 容器，让 Docker 自动清理这些临时挂载。

Kopia policy 只需要管理 `/volumes/<name>` 这些稳定路径，项目不实现自己的备份调度层。

## Compose 方向

Kopia 服务需要挂载 Docker socket，因为 Go 程序要通过 Docker API 创建和清理 helper 容器。

Kopia 服务还需要挂载 bridge 目录。更收敛的写法是让 Kopia 只接收传播进来的挂载。

```yaml
services:
  kopia:
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /mnt/volumes-backup:/volumes:rslave
```

helper 容器侧仍然使用 `bind-propagation=rshared`，因为它需要让 Docker daemon 创建的子挂载传播出去。

如果实际环境里 `rslave` 不能收到目标挂载，可以先退回 `rshared` 做概念验证。

## 为什么不完全使用命名卷

这个方案不能完全放进 Docker 命名卷里实现。Docker 命名卷的挂载传播固定为 `rprivate`，不能配置成 `shared` 或 `slave`，所以 helper 容器内部或 Docker daemon 创建在命名卷内的子挂载不会传播给 Kopia 容器。

因此必须保留一个宿主机 bind mount 目录作为传播桥。这个目录不是备份数据目录，只是临时挂载点目录。

## 实现约束

Go 程序需要使用 github.com/moby/moby/client 实现扫描 volume、创建 helper 容器、检查已有 helper 容器、清理 helper 容器的功能。

friendly name 必须做路径安全清洗，不能直接把 label 值拼进路径。清洗后为空时回退到 volume 名称，仍然为空则跳过并输出错误。

helper 容器名称应当稳定可预测，例如使用 volume 名称的哈希，避免重复 prepare 时创建多个容器。

prepare 命令需要具备幂等性，已存在且配置正确的 helper 容器可以复用，配置不一致的 helper 容器应当先清理再创建。

cleanup 命令只清理本项目创建并带有明确标签的 helper 容器，不能按名称模糊清理其他容器。

## 已知限制

挂载传播依赖 Linux shared subtree 机制。Docker Desktop 不支持 bind mount propagation，因此这个方案主要面向 Linux Docker Engine。

这个项目只解决 volume 暴露路径问题，不解决应用一致性问题。正在写入的数据库或状态服务仍然需要应用自己的 flush、锁定、停止写入或维护窗口策略。

Kopia policy 的目标路径需要和 Go 程序生成的 `/volumes/<friendly-name>` 保持一致。
