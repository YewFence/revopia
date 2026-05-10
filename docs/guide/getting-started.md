# 快速开始

这一页给出从空环境到第一次 Kopia 备份的最短路径。生产环境请把密码、TLS 和仓库路径换成自己的部署规范。

如果你在本机使用 Mise，并且已经完成 activate，仓库里的 `[shell_alias]` 会自动生效，后面的长命令可以直接简写成 `kopia`、`exec-kopia` 和 `revopia`。

## 1. 构建或安装二进制

开发环境可以直接在仓库里构建。

```bash
mise run build
./bin/revopia version
```

生产 compose 提供了安装 profile，会把 release 二进制下载到 `revopia-tools` volume。

```bash
docker compose --profile install run --rm revopia-install
```

安装 profile 会调用仓库里的安装脚本。脚本默认安装 GitHub latest release，并使用 GitHub Release asset 的 sha256 digest 校验下载内容。如果要安装指定版本，可以设置 `VERSION=v0.1.0`。

也可以直接把二进制下载到当前目录。

```bash
curl -fsSL https://raw.githubusercontent.com/yewfence/revopia/main/scripts/install.sh | sh
```

## 2. 标记 Docker volume

只有带 `backup.enable=true` 的 volume 会被 `prepare` 扫描。

```bash
docker volume create app-data \
  --label backup.enable=true \
  --label backup.name=app-data

docker volume create db-data \
  --label backup.enable=true
```

`backup.name` 是可选的展示名，用来决定 Kopia 中的路径 `/volumes/<friendly-name>`。没有该标签时，会使用 Docker volume 名称清洗后的结果。

## 3. 启动 Kopia

仓库里的 `compose.yaml` 使用 `docker-socket-proxy`，避免直接把 Docker socket 暴露给 Kopia 容器。默认 bridge 路径和容器内路径如下。

```yaml
volumes:
  - revopia-tools:/tools:ro
  - /mnt/revopia:/volumes:rshared
  - /mnt/revopia/restore:/restore:rslave
```

宿主机 bridge 路径不需要提前手动创建或重新挂载，Docker 会在 compose bind mount 和 helper bind mount 阶段处理它。

启动服务。

```bash
docker compose up -d
```

如果你用的是开发 compose，则先构建本地二进制，再启动开发配置。

```bash
mise run build
docker compose -f compose.dev.yaml up -d
```

## 4. 创建或连接 Kopia 仓库

新仓库示例。

```bash
docker compose exec kopia kopia repository create filesystem \
  --path=/repository \
  --enable-actions
```

已有仓库示例。

```bash
docker compose exec kopia kopia repository connect filesystem \
  --path=/repository \
  --enable-actions
```

`--enable-actions` 必须启用，否则 Kopia 不会执行 snapshot 前后的 `revopia` 命令。

## 5. 配置 Kopia policy

给 `/volumes` 配置 snapshot 前后动作。

```bash
docker compose exec kopia kopia policy set /volumes \
  --before-snapshot-root-action="revopia prepare" \
  --after-snapshot-root-action="revopia cleanup" \
  --one-file-system=false
```

`--one-file-system=false` 用来允许 Kopia 进入传播出来的 volume 挂载点。

## 6. 创建快照

备份整个 volume 视图。

```bash
docker compose exec kopia kopia snapshot create /volumes
docker compose exec kopia kopia snapshot list /volumes
```

也可以备份单个 volume。

```bash
docker compose exec kopia kopia snapshot create /volumes/app-data
```

## 7. 检查状态

遇到路径不可见、helper 没有清理或 volume 删除 busy 时，先运行 inspect。

```bash
docker compose exec kopia revopia inspect
```

更详细的 busy volume 处理流程在仓库根目录的 `TROUBLESHOOTING.md`。
