# 故障排除

## Docker volume 删除失败

这个项目依赖 Linux bind mount propagation 把 Docker volume 暴露给 Kopia。某些 Linux Docker Engine 环境里，helper 容器已经停止或删除后，传播到 bridge 目录下的子挂载仍然可能残留，随后执行 `docker volume rm`、`docker compose down -v` 或删除恢复目标 volume 时，Docker 可能报告 `device or resource busy`。

这个状态通常不是 volume 本身损坏，而是宿主机上还有一个挂载点指向该 volume 的 `_data` 目录。默认 bridge 目录是 `/mnt/volumes-backup`，备份残留路径通常是 `/mnt/volumes-backup/<friendly-name>`，恢复残留路径通常是 `/mnt/volumes-backup/restore/<friendly-name>`。

如果 helper 容器已经不在了，不要继续卡在 `docker inspect <helper>`。恢复目标 volume 本身会保存本项目写入的审计标签，可以从这些标签反推出残留挂载点。

## 先运行项目清理命令

如果失败发生在备份流程后，先运行普通清理命令。

```bash
volume-backup cleanup
```

如果失败发生在恢复流程后，优先按 session 清理恢复 helper。

```bash
volume-backup restore-cleanup --session <session-id> --yes
```

如果不知道 session，可以先查看仍然存在的 helper 容器。

```bash
docker ps -a --filter label=kopia.volume-bridge=true
```

如果这里已经没有任何 helper 容器，继续按下面的步骤从要删除失败的 Docker volume 反查残留挂载。

## 确认 volume 没有被容器使用

删除 volume 前，应当先确认没有业务容器、Kopia 容器或 shell 进程还在使用它。下面的命令可以查看 Docker 侧是否还有容器引用该 volume。

```bash
docker ps -a --filter volume=<volume-name>
```

如果还有容器引用它，先停止或移除这些容器，再继续处理挂载残留。

```bash
docker stop <container>
docker rm <container>
```

## 从 volume 标签反查恢复挂载点

如果删除失败的是恢复目标 volume，先 inspect 这个 volume。

```bash
docker volume inspect <volume-name> --format '{{ .Mountpoint }}'
docker volume inspect <volume-name> --format '{{ json .Labels }}'
```

如果标签里有 `kopia.volume-bridge.mode=restore` 或 `kopia.volume-bridge.restore-target=true`，再看 `kopia.volume-bridge.name`。这个值就是恢复路径里的 friendly name，默认残留挂载点应该是 `/mnt/volumes-backup/restore/<friendly-name>`。

```bash
docker volume inspect <volume-name> --format '{{ index .Labels "kopia.volume-bridge.name" }}'
findmnt /mnt/volumes-backup/restore/<friendly-name>
```

如果你通过 `--bridge-source` 或 `KOPIA_VOLUME_BRIDGE_SOURCE` 改过 bridge 目录，请把 `/mnt/volumes-backup` 替换成实际宿主机路径。如果输出里的源路径指向 `/var/lib/docker/volumes/<volume-name>/_data` 或对应 Docker 数据目录，就说明这个恢复挂载还残留在 bridge 下。

例如目标 volume 是 `yew-resin-pro_test-db-data20260508-153000`，inspect 后看到 `kopia.volume-bridge.name=db-primary`，那么要检查的路径就是下面这个。

```bash
findmnt /mnt/volumes-backup/restore/db-primary
```

如果 `findmnt` 显示它的源路径指向 `/var/lib/docker/volumes/yew-resin-pro_test-db-data20260508-153000/_data`，就可以确认 Docker volume 删除失败是这个残留传播挂载导致的。

## 找到备份挂载点

如果删除失败的是原始备份 volume，而不是恢复目标 volume，friendly name 来自 `backup.name` 标签清洗后的结果，没有该标签时通常来自 volume 名称清洗结果。可以先查看所有 bridge 子挂载，再根据源路径确认哪一个指向要删除的 volume。

```bash
docker volume inspect <volume-name> --format '{{ .Mountpoint }}'
findmnt -R /mnt/volumes-backup
```

备份残留通常在 `/mnt/volumes-backup/<friendly-name>`，恢复残留通常在 `/mnt/volumes-backup/restore/<friendly-name>`。

## 普通卸载残留挂载

优先使用普通 `umount`，它会在仍有进程占用时失败，这个失败信号有价值，可以避免误删正在使用的数据。

```bash
sudo umount /mnt/volumes-backup/<friendly-name>
```

恢复路径的残留挂载使用下面的命令。

```bash
sudo umount /mnt/volumes-backup/restore/<friendly-name>
```

卸载后继续用 `findmnt` 检查同一个路径。如果同一个 target 仍然有输出，说明可能叠了不止一层传播挂载，继续执行普通 `umount`，直到 `findmnt` 没有输出。

```bash
findmnt /mnt/volumes-backup/restore/<friendly-name>
sudo umount /mnt/volumes-backup/restore/<friendly-name>
findmnt /mnt/volumes-backup/restore/<friendly-name>
```

如果路径不是挂载点，`umount` 会报告 `not mounted` 或类似信息，这时不需要继续卸载该路径。

## 处理 device or resource busy

如果普通卸载仍然报告 `device or resource busy`，先找出占用这个路径的进程。

```bash
sudo fuser -vm /mnt/volumes-backup/<friendly-name>
```

恢复路径使用下面的命令。

```bash
sudo fuser -vm /mnt/volumes-backup/restore/<friendly-name>
```

常见占用来源包括仍在运行的 Kopia 备份或恢复进程、正在查看目录的 shell、还没有退出的业务容器、文件管理器以及日志采集进程。处理方式是让这些进程退出，或者把 shell 当前目录切换到 bridge 目录外，再重新执行普通 `umount`。

如果系统安装了 `lsof`，也可以用它辅助确认占用来源。

```bash
sudo lsof +f -- /mnt/volumes-backup/<friendly-name>
```

## 最后手动删除 volume

确认残留挂载已经消失后，再删除 Docker volume。

```bash
findmnt /mnt/volumes-backup/<friendly-name>
docker volume rm <volume-name>
```

如果是恢复目标 volume，也先确认恢复路径不再是挂载点。

```bash
findmnt /mnt/volumes-backup/restore/<friendly-name>
docker volume rm <target-volume-name>
```

`findmnt` 没有输出通常表示该路径当前不是挂载点，随后 `docker volume rm` 就不应该再因为这个传播挂载报 busy。

按照前面的实际例子，最后的删除命令是下面这样。

```bash
findmnt /mnt/volumes-backup/restore/db-primary
docker volume rm yew-resin-pro_test-db-data20260508-153000
```

## lazy unmount 只作为最后手段

只有在你已经确认没有备份、恢复、业务容器或 shell 进程需要该路径，但内核仍然保留传播挂载时，才考虑 lazy unmount。它会把挂载点从当前目录树里摘除，被进程持有的引用会等进程释放后再回收，所以它可能隐藏真正的占用问题。

```bash
sudo umount -l /mnt/volumes-backup/<friendly-name>
```

恢复路径使用下面的命令。

```bash
sudo umount -l /mnt/volumes-backup/restore/<friendly-name>
```

执行 lazy unmount 后，再确认挂载点消失并删除 volume。

```bash
findmnt /mnt/volumes-backup/<friendly-name>
docker volume rm <volume-name>
```

## 不要手动删除 _data 目录

不要用 `rm -rf /var/lib/docker/volumes/<volume-name>/_data` 处理这个问题。这个目录由 Docker 管理，直接删除可能绕过 Docker 的引用检查，也可能破坏仍然被容器使用的数据。正确顺序是先停止引用它的容器，再卸载 bridge 下残留的传播挂载，最后用 `docker volume rm` 删除 volume。
