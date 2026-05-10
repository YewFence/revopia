package bridge

import (
	"fmt"
	"strings"
)

func restoreBridgeSourceMissingError() error {
	return withHints(
		fmt.Errorf("bridge source 不能为空"),
		"用 --bridge-source 指定宿主机传播桥路径，常见值是 /mnt/revopia",
	)
}

func restoreRootMissingError() error {
	return withHints(
		fmt.Errorf("restore root 不能为空"),
		"用 --restore-root 指定当前 Kopia 进程可见的恢复根路径，容器常见值是 /restore，宿主机常见值是 /mnt/revopia/restore",
	)
}

func restoreHelperImageMissingError() error {
	return withHints(
		fmt.Errorf("helper image 不能为空"),
		"用 --helper-image 指定一个带 sh、find 和 grep 的小镜像，默认 alpine 就够用",
	)
}

func restoreSourceVolumeMissingError() error {
	return withHints(
		fmt.Errorf("source volume 不能为空"),
		"用 --source-volume 指定要按哪个源 volume 的备份路径恢复",
		restoreListVolumesHint(),
	)
}

func restoreSameSourceTargetError(sourceVolume string) error {
	return withHints(
		fmt.Errorf("默认拒绝把源 volume %q 作为恢复目标，确实要这么做时请显式传入危险参数", sourceVolume),
		fmt.Sprintf("推荐改用新目标，例如 `--target-volume %s`", shellArg(sourceVolume+"-restore-20260508-153000")),
		"如果你已经停掉业务并确认要写回原 volume，再加 --dangerously-allow-source-target",
	)
}

func restoreUnsafeSessionIDError(opts RestoreOptions) error {
	return withHints(
		fmt.Errorf("session id %q 不是路径安全名称", opts.SessionID),
		"session id 只能使用路径安全字符，建议只用字母、数字、点、下划线和短横线",
		fmt.Sprintf("可以直接省略 --session，让程序自动生成，例如 `revopia restore --source-volume %s --target-volume %s`", shellArg(opts.SourceVolume), shellArg(opts.TargetVolume)),
	)
}

func restoreUnsafeSnapshotTimeError() error {
	return withHints(
		fmt.Errorf("snapshot time 不能包含换行或空字符"),
		"snapshot time 只用于打印 Kopia 参考命令，常见值是 latest 或一个具体快照时间",
		"如果你准备用 source directory id 恢复，可以省略 --snapshot-time 并传 --source-directory-id",
	)
}

func restoreSourceVolumeNotFoundError(sourceVolume string) error {
	return withHints(
		fmt.Errorf("源 volume %q 不存在", sourceVolume),
		restoreListVolumesHint(),
		fmt.Sprintf("用 `docker volume inspect %s` 确认源 volume 名称是否写对", shellArg(sourceVolume)),
	)
}

func restoreSourceVolumeInspectError(sourceVolume string, err error) error {
	return withHints(
		fmt.Errorf("检查源 volume %q 失败: %w", sourceVolume, err),
		restoreDockerAccessHint(),
		fmt.Sprintf("用 `docker volume inspect %s` 单独检查 Docker daemon 返回的错误", shellArg(sourceVolume)),
	)
}

func restoreSourceFriendlyNameError(sourceVolume string, err error) error {
	return withHints(
		err,
		"给源 volume 设置路径安全的 backup.name 标签，或者换用只包含字母、数字、点、下划线和短横线的 volume 名称",
		fmt.Sprintf("用 `docker volume inspect %s` 查看源 volume 的 labels", shellArg(sourceVolume)),
	)
}

func restoreTargetVolumeInspectError(targetVolume string, err error) error {
	return withHints(
		fmt.Errorf("检查目标 volume %q 失败: %w", targetVolume, err),
		restoreDockerAccessHint(),
		fmt.Sprintf("用 `docker volume inspect %s` 单独检查目标 volume 状态", shellArg(targetVolume)),
	)
}

func restoreTargetVolumeCreateError(targetVolume string, err error) error {
	return withHints(
		fmt.Errorf("创建目标 volume %q 失败: %w", targetVolume, err),
		"换一个符合 Docker 规则的目标 volume 名称，推荐只用字母、数字、点、下划线和短横线",
		fmt.Sprintf("也可以先手动验证 `docker volume create %s` 是否能成功", shellArg(targetVolume)),
	)
}

func restoreEmptyCheckCreateError(cfg Config, targetVolume string, err error) error {
	return withHints(
		fmt.Errorf("创建目标 volume 空目录检查容器失败: %w", err),
		restoreHelperImageHint(cfg.HelperImage),
		fmt.Sprintf("也可以用 `docker run --rm -v %s:/target %s sh -c 'find /target -mindepth 1 -maxdepth 1 -print -quit'` 手动确认目标卷是否为空", shellArg(targetVolume), shellArg(cfg.HelperImage)),
	)
}

func restoreEmptyCheckStartError(targetVolume string, err error) error {
	return withHints(
		fmt.Errorf("启动目标 volume 空目录检查容器失败: %w", err),
		restoreDockerAccessHint(),
		fmt.Sprintf("用 `docker volume inspect %s` 确认目标 volume 仍然存在", shellArg(targetVolume)),
	)
}

func restoreEmptyCheckWaitError(targetVolume string, err error) error {
	return withHints(
		fmt.Errorf("等待目标 volume 空目录检查容器失败: %w", err),
		restoreDockerAccessHint(),
		fmt.Sprintf("用 `docker ps -a --filter volume=%s` 查看相关容器状态", shellArg(targetVolume)),
	)
}

func restoreTargetVolumeNotEmptyError(session restoreSession) error {
	return withHints(
		fmt.Errorf("目标 volume %q 已存在且不是空目录，确实要复用非空目标时请显式传入危险参数", session.TargetVolume),
		fmt.Sprintf("推荐换一个新的目标 volume，例如 `--target-volume %s`", shellArg(session.TargetVolume+"-new")),
		fmt.Sprintf("如果这个目标卷不再需要，可以先运行 `docker volume rm %s` 删除它", shellArg(session.TargetVolume)),
		fmt.Sprintf("如果只想清空目标卷，可以运行 `docker run --rm -v %s:/target alpine sh -c 'find /target -mindepth 1 -maxdepth 1 -exec rm -rf {} +'`", shellArg(session.TargetVolume)),
		"确认要复用非空目标时再加 --dangerously-allow-non-empty-target",
	)
}

func restoreVisibleMountError(cfg Config, session restoreSession, err error) error {
	return withHints(
		fmt.Errorf("恢复目标 volume %q 没有在 %q 中变成可见挂载: %w", session.TargetVolume, session.TargetPath, err),
		restorePropagationHint(cfg),
		fmt.Sprintf("用 `docker ps -a --filter label=%s=%s` 查看恢复 helper 是否还在运行", labelSession, session.SessionID),
		restoreCleanupCommandHint(session.SessionID),
	)
}

func restoreHelperImageNotFoundError(helperImage string, err error) error {
	return withHints(
		fmt.Errorf("helper 镜像 %q 不存在，请先拉取这个镜像: %w", helperImage, err),
		restoreHelperImageHint(helperImage),
	)
}

func restoreHelperNameConflictError(session restoreSession, helperName string, err error) error {
	return withHints(
		fmt.Errorf("恢复 helper 容器名称 %q 已被占用: %w", helperName, err),
		restoreCleanupCommandHint(session.SessionID),
		fmt.Sprintf("用 `docker ps -a --filter name=%s` 查看占用这个名字的容器", shellArg(helperName)),
	)
}

func restoreHelperCreateError(session restoreSession, err error) error {
	return withHints(
		fmt.Errorf("创建恢复 helper 容器失败: %w", err),
		restoreCleanupCommandHint(session.SessionID),
		restoreDockerAccessHint(),
	)
}

func restoreHelperStartError(session restoreSession, err error) error {
	return withHints(
		fmt.Errorf("启动恢复 helper 容器失败: %w", err),
		restoreCleanupCommandHint(session.SessionID),
		restoreDockerAccessHint(),
	)
}

func restoreCleanupListError(sessionID string, err error) error {
	return withHints(
		fmt.Errorf("扫描恢复 helper 容器失败: %w", err),
		restoreDockerAccessHint(),
		restoreCleanupCommandHint(sessionID),
	)
}

func restoreListVolumesHint() string {
	return "用 `docker volume ls` 查看当前 Docker volume 名称"
}

func restoreDockerAccessHint() string {
	return "确认当前进程可以访问 Docker socket，宿主机服务用户通常需要加入 docker 组，Kopia 容器里通常需要挂载 /var/run/docker.sock"
}

func restoreHelperImageHint(helperImage string) string {
	return fmt.Sprintf("先拉取 helper 镜像 `docker pull %s`，或者用 --helper-image 换成环境里已有的小镜像", shellArg(helperImage))
}

func restorePropagationHint(cfg Config) string {
	return fmt.Sprintf("确认宿主机 bridge 路径 %s 是 shared mount，并且当前 Kopia 进程能看到恢复根路径 %s", shellArg(cfg.BridgeSource), shellArg(cfg.RestoreVisibleRoot))
}

func restoreCleanupCommandHint(sessionID string) string {
	return fmt.Sprintf("恢复会话可以用 `revopia restore-cleanup --session %s` 清理，目标 volume 不会被删除", shellArg(sessionID))
}

func shellArg(value string) string {
	if value == "" {
		return "''"
	}
	for _, r := range value {
		if isShellSafeRune(r) {
			continue
		}
		return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
	}
	return value
}

func isShellSafeRune(r rune) bool {
	return r >= 'a' && r <= 'z' ||
		r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' ||
		r == '.' ||
		r == '_' ||
		r == '-' ||
		r == '/' ||
		r == ':' ||
		r == '='
}
