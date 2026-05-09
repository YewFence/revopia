# 命令参考

## prepare

`prepare` 扫描 `backup.enable=true` 的 Docker volume，创建或复用备份 helper 容器，并等待路径在 `/volumes/<friendly-name>` 下变成可见挂载。

```bash
revopia prepare
```

常用参数如下。

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--bridge-source` | `/mnt/revopia` | Docker daemon 侧的宿主机 bridge bind mount 路径 |
| `--visible-root` | `/volumes` | Kopia 容器内可见的备份根路径 |
| `--helper-image` | `alpine` | helper 容器镜像 |
| `--verify-timeout` | `5s` | 等待挂载传播到可见路径的时间 |
| `--timeout` | `30s` | Docker API 调用超时时间 |
| `--log-file` | `/app/logs/revopia.log` | 持久日志路径，留空可禁用文件日志 |

如果 Kopia 只备份 `/volumes/<name>` 这样的单卷路径，`prepare` 会根据 `KOPIA_SOURCE_PATH` 输出 `KOPIA_SNAPSHOT_PATH=<friendly-path>`，让 Kopia 使用清洗后的真实路径。

## cleanup

`cleanup` 删除备份 helper 容器，并回收备份视图中的传播挂载。

```bash
revopia cleanup
```

常用参数如下。

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--bridge-source` | `/mnt/revopia` | Docker daemon 侧的宿主机 bridge bind mount 路径 |
| `--visible-root` | `/volumes` | 当前进程可见的备份根路径 |
| `--helper-image` | `alpine` | cleanup 容器镜像 |
| `--dangerously-lazy-umount` | `false` | 普通 `umount` 失败后允许使用 lazy unmount |
| `--timeout` | `30s` | Docker API 调用超时时间 |
| `--log-file` | `/app/logs/revopia.log` | 持久日志路径，留空可禁用文件日志 |

普通卸载失败时不要马上加危险参数，应该先确认没有 Kopia 任务、业务容器或 shell 工作目录占用目标路径。

## restore

`restore` 准备恢复目标 Docker volume，并把它可写暴露到 `/restore/<friendly-name>`。

```bash
revopia restore SOURCE_VOLUME
revopia restore app-data --target-volume app-data-restore-20260509
```

常用参数如下。

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--target-volume`, `-t` | `SOURCE_VOLUME-restore-时间戳` | 目标 Docker volume，不存在时自动创建 |
| `--restore-root` | `/restore` | Kopia 容器内可见的恢复根路径 |
| `--visible-root` | `/volumes` | 用于打印路径恢复命令的备份根路径 |
| `--source-directory-id` | 空 | Kopia source directory id，用于打印精确恢复命令 |
| `--snapshot-time` | `latest` | 用于打印路径恢复命令的 snapshot time |
| `--session` | 自动生成 | 恢复 session id |
| `--dangerously-allow-source-target`, `-s` | `false` | 允许源 volume 和目标 volume 相同 |
| `--dangerously-allow-non-empty-target`, `-n` | `false` | 允许复用非空目标 volume |
| `--bridge-source` | `/mnt/revopia` | Docker daemon 侧的宿主机 bridge bind mount 路径 |
| `--helper-image` | `alpine` | helper 容器镜像 |
| `--verify-timeout` | `5s` | 等待恢复挂载传播到可见路径的时间 |
| `--timeout` | `30s` | Docker API 调用超时时间 |
| `--log-file` | `/app/logs/revopia.log` | 持久日志路径，留空可禁用文件日志 |

## restore-cleanup

`restore-cleanup` 删除恢复 helper 容器，并回收恢复视图中的传播挂载。它不会删除目标 Docker volume。

```bash
revopia restore-cleanup --session <session-id> --yes
```

常用参数如下。

| 参数 | 默认值 | 说明 |
| --- | --- | --- |
| `--session` | 空 | 要清理的恢复 session id，留空表示所有恢复 helper |
| `--yes`, `-y` | `false` | 跳过确认 |
| `--restore-root` | `/restore` | 当前进程可见的恢复根路径 |
| `--bridge-source` | `/mnt/revopia` | Docker daemon 侧的宿主机 bridge bind mount 路径 |
| `--helper-image` | `alpine` | cleanup 容器镜像 |
| `--dangerously-lazy-umount` | `false` | 普通 `umount` 失败后允许使用 lazy unmount |
| `--timeout` | `30s` | Docker API 调用超时时间 |
| `--log-file` | `/app/logs/revopia.log` | 持久日志路径，留空可禁用文件日志 |

## inspect

`inspect` 输出当前配置、可见根路径、启用备份的 volume、helper 容器和挂载状态。

```bash
revopia inspect
```

它不会修改 Docker volume 或 helper 容器，适合作为排障第一步。

## completion

`completion` 生成 Shell 补全脚本。

```bash
revopia completion bash
revopia completion zsh
revopia completion fish
revopia completion powershell
```

## 环境变量

| 环境变量 | 对应参数 | 默认值 |
| --- | --- | --- |
| `REVOPIA_BRIDGE_SOURCE` | `--bridge-source` | `/mnt/revopia` |
| `REVOPIA_VISIBLE_ROOT` | `--visible-root` | `/volumes` |
| `REVOPIA_RESTORE_ROOT` | `--restore-root` | `/restore` |
| `REVOPIA_HELPER_IMAGE` | `--helper-image` | `alpine` |
| `REVOPIA_LOG_FILE` | `--log-file` | `/app/logs/revopia.log` |
