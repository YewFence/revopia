# Kopia Docker Volume Bridge Vision

## 目标

这个项目的目标是让 Kopia 继续用自己的 policy 管理备份路径、调度、保留策略和排除规则，同时用一个很小的 Go 可执行文件在备份前把带有 `backup.enable=true` 标签的 Docker volume 暴露到 Kopia 容器里的稳定目录。

Go 程序不负责组织 Kopia 的备份计划，也不主动调用 `kopia snapshot create`。对于恢复流程，Go 程序可以创建目标 volume，并把它准备成 Kopia 可以写入的目录，但不主动调用 `kopia snapshot restore`。Kopia 仍然是唯一负责创建快照和恢复快照的工具。

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

## 恢复模型

Kopia 目前有 snapshot 前后脚本，但没有等价的 restore 前后脚本，所以恢复流程不应当依赖 Kopia 自动调用 Go 程序。恢复应当是一个显式的人工流程，由用户先运行 Go 程序的 restore 命令准备目标 volume，再运行 Kopia 的恢复命令，最后手动运行 restore-cleanup。

恢复默认写入一个新 Docker volume，而不是覆盖原 volume。Go 程序的 restore 命令接收源 volume 和目标 volume，目标 volume 不存在时自动创建，存在时默认要求它为空。覆盖已有非空 volume 必须使用显式危险参数。

```bash
kopia-volume-bridge restore \
  --source-volume app-data \
  --target-volume app-data-restore-20260508-153000
```

restore 命令不会自动扫描所有带有 `backup.enable=true` 标签的 volume。恢复是点名式操作，只处理命令行明确指定的源 volume 和目标 volume。

Go 程序创建恢复 helper 容器时，把目标 volume 以可写方式挂载到恢复视图中。推荐默认路径是 `/restore/<friendly-name>`，这样备份视图 `/volumes/<friendly-name>` 和恢复视图保持分离，避免恢复期间被 Kopia 的自动备份 policy 意外扫到。

```bash
docker run --rm -d \
  --name kopia-volume-restore-bridge-<session-hash> \
  --label kopia.volume-bridge=true \
  --label kopia.volume-bridge.mode=restore \
  --label kopia.volume-bridge.session=<session-id> \
  --label kopia.volume-bridge.source-volume=<source-volume-name> \
  --label kopia.volume-bridge.target-volume=<target-volume-name> \
  --mount type=bind,source=/mnt/volumes-backup,target=/bridge,bind-propagation=rshared \
  --mount type=volume,source=<target-volume-name>,target=/bridge/restore/<friendly-name> \
  alpine sleep infinity
```

Kopia 容器中应当能看到 `/restore/<friendly-name>`。restore 命令执行成功后，应当打印完整的 Kopia 参考命令，让用户复制执行。

```bash
kopia snapshot restore <source-directory-id> /restore/app-data
```

如果用户更习惯按路径恢复，也可以使用路径源。实际实现可以在输出中提示用户先用 `kopia snapshot list /volumes/app-data` 或 Kopia UI 确认要恢复的快照。

```bash
kopia snapshot restore /volumes/app-data /restore/app-data --snapshot-time latest
```

恢复 helper 的 cleanup 必须和备份 cleanup 分开。`restore-cleanup` 只清理带有 `kopia.volume-bridge.mode=restore` 和指定 session 标签的 helper 容器，不删除目标 volume。

恢复创建的新 volume 默认不复制 `backup.enable=true`、`backup.name` 这类备份发现标签，避免下一轮备份自动把恢复卷纳入备份。Go 程序可以给目标 volume 添加审计标签，例如源 volume 名、目标 friendly name、恢复 session、创建时间和创建工具版本。

## Compose 方向

Kopia 服务需要挂载 Docker socket，因为 Go 程序要通过 Docker API 创建和清理 helper 容器。

Kopia 服务还需要挂载 bridge 目录。更收敛的写法是让 Kopia 只接收传播进来的挂载。备份视图推荐使用 `/volumes`，恢复视图推荐使用 `/restore`，两者可以来自同一个宿主机 bridge 目录下的不同子路径。

```yaml
services:
  kopia:
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /mnt/volumes-backup:/volumes:rslave
      - /mnt/volumes-backup/restore:/restore:rslave
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

restore 命令需要生成 session id，并把 session id 写入 helper 容器标签。restore-cleanup 默认只清理当前 session，显式传入 session id 时只清理指定 session。

restore 命令创建目标 volume 时，默认拒绝使用源 volume 作为目标 volume。目标 volume 已存在且非空时默认拒绝继续。任何覆盖或复用非空目标的行为都必须要求显式危险参数。

restore 命令必须输出它实际准备的目标路径和推荐的 `kopia snapshot restore` 命令。它不应该尝试自动选择 snapshot，也不应该在默认模式下调用 Kopia CLI。

## 已知限制

挂载传播依赖 Linux shared subtree 机制。Docker Desktop 不支持 bind mount propagation，因此这个方案主要面向 Linux Docker Engine。

当前的传播桥模型会让 helper 容器把 Docker volume 的挂载传播到宿主机 bridge 目录下，例如 `/mnt/volumes-backup/<friendly-name>`。实测在 Linux Docker Engine 上，删除 helper 容器后这些传播出来的子挂载可能不会自动消失，即使已经没有任何容器运行，`docker compose down -v` 或 `docker volume rm` 仍然可能因为 volume 的 `_data` 路径处于 busy 状态而失败，典型错误是 `device or resource busy`。

把 helper 的 bind mount 从整个 bridge 根目录改成单个 `/mnt/volumes-backup/<friendly-name>:/bridge/<friendly-name>` 不能从根本上解决这个问题，因为问题来自 `rshared` 把子挂载传播回宿主机后的生命周期管理，而不是 bridge 根目录本身太宽。推荐继续保留传播桥模型，但把 cleanup 设计成两阶段流程，先删除本项目创建的 helper 容器，再对这些 helper 标签里记录的 friendly name 执行明确的子挂载回收。

子挂载回收可以由宿主机侧 Go 进程直接执行，也可以由 Go 程序临时创建一个受控 cleanup 容器执行。如果由宿主机侧 Go 进程执行 `umount`，该进程需要在宿主机上拥有 `CAP_SYS_ADMIN`，通常意味着以 root 运行，或给可执行文件授予受控 capability。如果由 cleanup 容器执行 `umount`，容器只需要挂载同一个 bridge 目录到 `/bridge`，使用 `bind-propagation=rshared`，禁用网络，并获得卸载挂载点所需的 `CAP_SYS_ADMIN`。

cleanup 容器不应该使用 `--privileged`。推荐的最小权限模型是丢弃全部默认 capability 后只加入 `CAP_SYS_ADMIN`，设置 `network=none`、只读根文件系统和 `no-new-privileges`，并且不挂载 Docker socket、宿主机根目录、宿主机设备目录，也不共享宿主机 pid 或 ipc namespace。`CAP_SYS_ADMIN` 本身仍然是很大的 Linux 能力，所以它必须只出现在短生命周期 cleanup 容器里，并且这个容器只能看到 bridge 目录。

cleanup 只允许处理 `/bridge/<friendly-name>` 这类由 helper 标签和路径安全清洗共同得到的目标，不能接受任意用户路径，不能扫描和卸载 bridge 外部路径。执行卸载前应当通过 `/proc/self/mountinfo` 确认目标确实是 bridge 下的挂载点，并且路径清洗结果不能包含 `..`、绝对路径跳转或符号链接逃逸。

cleanup 的默认策略应当先尝试普通 `umount`，普通卸载失败时输出 busy 诊断和待处理路径，不默认使用 lazy unmount。可以提供显式危险参数启用 lazy unmount，用于用户已经确认没有备份或业务进程持有该路径，但内核仍然留下传播挂载的场景。

lazy unmount 只能作为显式危险操作。它会先把挂载点从当前目录树中摘除，仍被进程持有的文件和目录会等引用释放后再真正回收，因此它可能隐藏仍在运行的 Kopia 备份进程、业务进程或 shell 工作目录。普通 `umount` 返回 `device or resource busy` 时，默认行为应当保留现场并报告诊断，而不是替用户强行清理。

inspect 命令应当能报告 bridge 目录下仍然存在但没有对应 helper 容器的 orphan 子挂载。cleanup 可以在普通模式下只处理本次通过 helper 标签确认的路径，在显式 orphan 清理模式下再处理这些遗留挂载。

这个项目只解决 volume 暴露路径问题，不解决应用一致性问题。正在写入的数据库或状态服务仍然需要应用自己的 flush、锁定、停止写入或维护窗口策略。

Kopia policy 的目标路径需要和 Go 程序生成的 `/volumes/<friendly-name>` 保持一致。

恢复时也不解决应用切换问题。恢复到新 volume 后，用户仍然需要停止业务容器、切换 compose volume 引用或迁移数据，并在业务启动后验证数据完整性。

Kopia snapshot 要求 `/volumes/<volume-name>` 存在，所以需要预先创建空目录，但是这样直接运行 restore 和预期会不一致，由于 hook 只在 snapshot 前后生效，而 restore 没有 hook ，所以没有恢复数据到原来的 volume，程序应该在这种情况下报 warn，提示用户出现了预期外的情况。

## 未来实现

考虑不仅备份 volume 数据，还备份 volume 的元数据，例如 labels、权限、时间戳、驱动等。恢复时可以选择性地恢复这些元数据。
