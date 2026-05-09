# Kopia Docker Volume Bridge

一个轻量级 Go CLI 工具，利用 Linux 挂载传播机制，将带标签的 Docker Volume 暴露到 Kopia 容器的稳定路径中。

Kopia 仍然是唯一负责创建和恢复快照的工具。本项目只是一个「卷桥」—— 在备份前把 volume 暴露出来，在备份后清理干净，不插手 Kopia 的调度策略。

## 工作原理

```
┌──────────────────────────────────────────────────┐
│                    宿主机                         │
│  /mnt/volumes-backup  (bind mount, rshared)      │
│       │                                          │
│       ├── app-data/    ← 传播自 helper 容器       │
│       ├── db-data/     ← 传播自 helper 容器       │
│       └── restore/     ← 恢复视图                 │
└──────────┬───────────────┬───────────────────────┘
           │               │
    ┌──────▼──────┐  ┌─────▼──────┐
    │ Kopia 容器   │  │Kopia 容器   │
    │ /volumes    │  │ /restore   │
    │ (备份视图)   │  │ (恢复视图)  │
    └─────────────┘  └────────────┘
           ▲               ▲
           │ 挂载传播       │ 挂载传播
    ┌──────┴───────────────┴──────┐
    │    helper 容器               │
    │    alpine sleep infinity     │
    │    - /bridge (rshared)       │
    │    - /bridge/<name> (volume) │
    └──────────────────────────────┘
```

Helper 容器将 Docker Volume 挂载到宿主机 bridge 目录下。通过 Linux shared subtree 机制，挂载事件自动传播到 Kopia 容器的 `/volumes`（备份）和 `/restore`（恢复）目录。

## 命令

| 命令 | 用途 |
|------|------|
| `prepare` | 扫描带有 `backup.enable=true` 标签的 volume，为每个创建 helper 容器，等待挂载传播完成 |
| `cleanup` | 停止所有备份 helper 容器，回收传播挂载 |
| `restore` | 创建恢复 helper，将目标 volume 以可写方式暴露到 `/restore`，打印 `kopia snapshot restore` 参考命令 |
| `restore-cleanup` | 清理指定 session 的恢复 helper 和挂载，不删除目标 volume |
| `inspect` | 诊断命令，报告 bridge 状态、helper 容器、挂载点、孤立挂载 |
| `version` | 打印版本号 |

## 快速开始

### 1. 标记要备份的 Volume

```bash
docker volume create app-data --label backup.enable=true --label backup.name=app-data
docker volume create db-data  --label backup.enable=true
```

- `backup.enable=true` — 必选，标记该 volume 参与备份
- `backup.name` — 可选，指定在 Kopia 中的路径名。未设置时使用 volume 名称经安全清洗后的结果

### 2. 准备 bridge 目录

```bash
sudo mkdir -p /mnt/volumes-backup
```

### 3. Compose 配置

```yaml
services:
  kopia:
    image: kopia/kopia:unstable
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock        # 让 Go 程序操作 Docker API
      - /mnt/volumes-backup:/volumes:rslave               # 备份视图
      - /mnt/volumes-backup/restore:/restore:rslave       # 恢复视图
      - ./bin/volume-backup:/usr/local/bin/volume-backup  # Go 二进制
```

### 4. 配置 Kopia Policy

在 Kopia 中为 `/volumes` 路径设置备份前后脚本：

```bash
kopia policy set --global \
  --before-snapshot-root-action="volume-backup prepare" \
  --after-snapshot-root-action="volume-backup cleanup" \
  /volumes
```

### 5. 恢复数据

恢复是显式的人工流程：

```bash
# 准备恢复环境
volume-backup restore --source-volume app-data --target-volume app-data-restored

# 按提示执行 Kopia 恢复命令，例如：
kopia snapshot restore /volumes/app-data /restore/app-data --snapshot-time latest

# 清理恢复环境
volume-backup restore-cleanup
```

## 为什么需要宿主机 Bind Mount

Docker 命名卷的挂载传播固定为 `rprivate`，无法配置为 `shared` 或 `slave`。这意味着 helper 容器内部对命名卷创建的子挂载不会传播给 Kopia 容器。因此必须使用一个宿主机 bind mount 目录作为传播桥。

## 环境要求

- **Linux Docker Engine**（Docker Desktop 不支持 bind mount propagation）
- Docker API 访问权限（通过挂载 `/var/run/docker.sock`）
- 宿主机上的 `CAP_SYS_ADMIN`（用于 cleanup 容器执行 `umount`）

## 安全设计

- **路径清洗**：所有 volume 名称和标签值经过严格清洗，拒绝 `.`、`..`、`/` 和任何路径遍历尝试
- **Cleanup 容器沙箱**：只读根文件系统、无网络、丢弃全部 capability 后仅添加 `CAP_SYS_ADMIN`、`no-new-privileges`
- **危险操作需显式参数**：lazy unmount、覆盖源 volume、写入非空目标 volume 都需要 `--dangerously-*` 标志
- **标签隔离**：只操作带有 `kopia.volume-bridge=true` 标签的容器，不会误删其他容器

## 已知限制

- 仅支持 Linux Docker Engine，不支持 Docker Desktop
- 删除 helper 容器后，传播出来的子挂载可能不会自动消失，需通过 `cleanup` 命令回收
- 不解决应用一致性问题（数据库等需要在备份前自行 flush / 锁定）
- 恢复时不自动切换业务容器的 volume 引用

## 构建

```bash
go build -o bin/volume-backup .
```

## 许可证

MIT
