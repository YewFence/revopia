# 恢复数据

Kopia 没有和 snapshot action 等价的 restore action，所以恢复流程是显式人工操作。`revopia restore` 只准备目标 volume 和恢复路径，不会调用 `kopia snapshot restore`。

## 默认恢复到新 volume

推荐先恢复到新的 Docker volume，确认数据无误后再切换业务容器。

```bash
docker compose exec kopia revopia restore app-data \
  --target-volume app-data-restore-20260509
```

命令成功后会输出类似下面的信息。

```text
创建 app-data-restore-20260509 -> /restore/app-data
RESTORE_SESSION_ID=app-data-to-app-data-restore-20260509-20260509-120000
RESTORE_TARGET_PATH=/restore/app-data
已创建目标 volume app-data-restore-20260509
推荐的 Kopia 恢复命令如下
kopia snapshot list /volumes/app-data
kopia snapshot restore /volumes/app-data /restore/app-data --snapshot-time latest
```

先查看快照，再执行恢复。

```bash
docker compose exec kopia kopia snapshot list /volumes/app-data
docker compose exec kopia kopia snapshot restore \
  /volumes/app-data \
  /restore/app-data \
  --snapshot-time latest
```

## 按 source directory id 恢复

如果已经从 Kopia 输出或界面拿到了 source directory id，可以让 `restore` 打印更精确的恢复命令。

```bash
docker compose exec kopia revopia restore app-data \
  --target-volume app-data-restore-20260509 \
  --source-directory-id kdbd123456789
```

这时输出会使用下面这种形式。

```bash
kopia snapshot restore kdbd123456789 /restore/app-data
```

## 清理恢复 helper

恢复结束后清理恢复 helper 和传播挂载。目标 volume 不会被删除。

```bash
docker compose exec kopia revopia restore-cleanup \
  --session <restore-session-id> \
  --yes
```

不传 `--session` 会清理所有恢复 helper，默认需要交互确认。自动化脚本里建议总是传 `--session` 和 `--yes`。

## 目标 volume 保护

默认情况下，`restore` 会拒绝两个危险操作。

| 场景 | 默认行为 | 显式参数 |
| --- | --- | --- |
| 源 volume 和目标 volume 相同 | 拒绝 | `--dangerously-allow-source-target` |
| 目标 volume 已存在且非空 | 拒绝 | `--dangerously-allow-non-empty-target` |

这两个参数只应该在已经停止业务、确认写入目标正确、并且有回滚方案时使用。

## 切换业务容器

`revopia` 不会修改 compose 文件，也不会把业务容器自动切到恢复后的 volume。常见做法是先停止业务，再把 compose volume 引用改到恢复目标 volume，启动后验证数据完整性。

```bash
docker compose stop app
docker compose up -d app
```

如果确认恢复目标无误，再按自己的运维流程处理旧 volume。

## 处理恢复挂载 busy

如果 `restore-cleanup` 报告普通 `umount` 失败，先确认没有 Kopia 恢复进程、业务容器或 shell 工作目录占用 `/restore/<friendly-name>`。

```bash
findmnt /mnt/revopia/restore/<friendly-name>
sudo fuser -vm /mnt/revopia/restore/<friendly-name>
```

确认无占用后，再显式使用 lazy unmount。

```bash
docker compose exec kopia revopia restore-cleanup \
  --session <restore-session-id> \
  --yes \
  --dangerously-lazy-umount
```
