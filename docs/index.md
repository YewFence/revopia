---
layout: home

hero:
  name: revopia
  text: Docker volume 到 Kopia 的挂载桥
  tagline: 让 Kopia 继续管理快照和恢复，本工具只负责把带标签的 Docker volume 暴露成稳定路径。
  actions:
    - theme: brand
      text: 快速开始
      link: /guide/getting-started
    - theme: alt
      text: 恢复数据
      link: /guide/restore
    - theme: alt
      text: 命令参考
      link: /guide/commands

features:
  - title: 标签驱动
    details: 只扫描 backup.enable=true 的 Docker volume，并用 backup.name 控制 Kopia 中看到的目录名。
  - title: Kopia 原生策略
    details: 通过 before 和 after snapshot action 接入 Kopia，不重新实现备份调度、保留规则或快照恢复。
  - title: 显式恢复流程
    details: restore 只准备目标 volume 和恢复路径，然后打印 Kopia 恢复命令，目标 volume 默认不会被删除。
  - title: 受控清理
    details: cleanup 只处理本项目标签标记的 helper 容器和传播挂载，lazy unmount 需要显式危险参数。
---

## 什么时候使用它

`revopia` 面向 Linux Docker Engine 上的 Kopia 部署。它解决的是 Docker named volume 不容易作为稳定文件路径交给 Kopia 备份的问题。

如果你使用 Docker Desktop，或者运行环境不支持 bind mount propagation，这个项目的挂载传播模型通常无法正常工作。

## 基本流程

1. 给要备份的 Docker volume 添加 `backup.enable=true` 标签。
2. 在 Kopia 容器中把宿主机 bridge 路径挂到 `/volumes`，并让 `revopia` 能访问 Docker API。
3. 给 Kopia 的 `/volumes` policy 配置 `revopia prepare` 和 `revopia cleanup`。
4. 恢复时手动运行 `revopia restore SOURCE_VOLUME`，再执行输出里的 `kopia snapshot restore` 命令。
