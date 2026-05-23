package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockerclient "github.com/moby/moby/client"
)

type cleanupUnmountResult struct {
	Output    string
	Unmounted bool
	Skipped   bool
}

type cleanupUnmountSpec struct {
	LogPrefix       string
	FriendlyName    string
	ContainerIDs    []string
	Volumes         []string
	VisibleRoot     string
	ContainerTarget string
}

func validateCleanupFriendlyName(friendlyName string) error {
	if friendlyName == "" {
		return fmt.Errorf("friendly name 不能为空")
	}
	if sanitizePathName(friendlyName) != friendlyName {
		return fmt.Errorf("friendly name %q 不是路径安全名称", friendlyName)
	}
	if strings.Contains(friendlyName, "..") {
		return fmt.Errorf("friendly name %q 不能包含连续点", friendlyName)
	}
	if filepath.IsAbs(friendlyName) || strings.Contains(friendlyName, string(filepath.Separator)) {
		return fmt.Errorf("friendly name %q 不能包含路径跳转", friendlyName)
	}
	return nil
}

func sortedCleanupTargets(targets map[string]cleanupTarget) []cleanupTarget {
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}
	sort.Strings(names)

	result := make([]cleanupTarget, 0, len(names))
	for _, name := range names {
		target := targets[name]
		sort.Strings(target.ContainerIDs)
		sort.Strings(target.Volumes)
		result = append(result, target)
	}
	return result
}

func cleanupUnmount(ctx context.Context, api DockerAPI, cfg Config, opts CleanupOptions, target cleanupTarget, logger Logger) (cleanupUnmountResult, error) {
	return cleanupUnmountAt(ctx, api, cfg, opts, cleanupUnmountSpec{
		LogPrefix:       "cleanup_umount",
		FriendlyName:    target.FriendlyName,
		ContainerIDs:    target.ContainerIDs,
		Volumes:         target.Volumes,
		VisibleRoot:     cfg.VisibleRoot,
		ContainerTarget: cleanupUnmountContainerTargetPath(target.FriendlyName),
	}, logger)
}

func cleanupUnmountAt(ctx context.Context, api DockerAPI, cfg Config, opts CleanupOptions, spec cleanupUnmountSpec, logger Logger) (cleanupUnmountResult, error) {
	targetPath, err := cleanupTargetPathChecked(spec.VisibleRoot, spec.FriendlyName)
	if err != nil {
		return cleanupUnmountResult{}, err
	}

	logger.Printf("%s_check friendly=%q target=%q lazy=%t", spec.LogPrefix, spec.FriendlyName, targetPath, opts.LazyUnmount)
	info, err := os.Lstat(targetPath)
	if err != nil {
		if os.IsNotExist(err) {
			return cleanupUnmountResult{
				Output:  "skip missing " + targetPath,
				Skipped: true,
			}, nil
		}
		return cleanupUnmountResult{}, fmt.Errorf("检查挂载点 %s 失败: %w", targetPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return cleanupUnmountResult{}, fmt.Errorf("拒绝卸载符号链接挂载点 %s", targetPath)
	}
	if err := cleanupRejectSymlinkEscape(spec.VisibleRoot, targetPath); err != nil {
		return cleanupUnmountResult{}, err
	}

	mountInfo, mounted, err := mountInfoForPath(targetPath)
	if err != nil {
		return cleanupUnmountResult{}, fmt.Errorf("读取 mountinfo 失败: %w", err)
	}
	if !mounted {
		return cleanupUnmountResult{
			Output:  "skip not-mounted " + targetPath,
			Skipped: true,
		}, nil
	}

	args := cleanupUnmountCommandArgs(opts, spec.ContainerTarget)
	logger.Printf("%s_exec friendly=%q container_target=%q command=%q args=%q mountinfo=%q", spec.LogPrefix, spec.FriendlyName, spec.ContainerTarget, "umount", strings.Join(args, " "), mountInfo)
	text, err := runCleanupUnmountContainer(ctx, api, cfg, opts, spec, logger)
	if err != nil {
		return cleanupUnmountResult{
			Output: text,
		}, cleanupUnmountFailedError(targetPath, opts, err, mountInfo, text)
	}

	return cleanupUnmountResult{
		Output:    strings.TrimSpace("unmounted " + targetPath + "\n" + text),
		Unmounted: true,
	}, nil
}

func runCleanupUnmountContainer(ctx context.Context, api DockerAPI, cfg Config, opts CleanupOptions, spec cleanupUnmountSpec, logger Logger) (string, error) {
	options := cleanupUnmountContainerCreateOptions(cfg, opts, spec)
	logger.Printf(
		"%s_container_create name=%q image=%q cmd=%q mounts=%q cap_add=%q cap_drop=%q readonly_rootfs=%t security=%q",
		spec.LogPrefix,
		options.Name,
		cfg.HelperImage,
		strings.Join(options.Config.Cmd, " "),
		formatCreateMounts(options.HostConfig.Mounts),
		strings.Join(options.HostConfig.CapAdd, ","),
		strings.Join(options.HostConfig.CapDrop, ","),
		options.HostConfig.ReadonlyRootfs,
		strings.Join(options.HostConfig.SecurityOpt, ","),
	)

	created, _, err := createContainerWithAutoPull(ctx, api, options, logger, spec.LogPrefix)
	if err != nil && (cerrdefs.IsAlreadyExists(err) || cerrdefs.IsConflict(err)) {
		logger.Printf("%s_container_conflict_remove name=%q error=%q", spec.LogPrefix, options.Name, err)
		if removeErr := removeContainer(ctx, api, options.Name); removeErr != nil {
			return "", fmt.Errorf("清理旧 cleanup 容器 %q 失败: %w", options.Name, removeErr)
		}
		created, _, err = createContainerWithAutoPull(ctx, api, options, logger, spec.LogPrefix)
	}
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return "", fmt.Errorf("cleanup 镜像 %q 不存在，自动拉取后仍无法创建容器: %w", cfg.HelperImage, err)
		}
		return "", fmt.Errorf("创建 cleanup 容器失败: %w", err)
	}
	logger.Printf("%s_container_created id=%q name=%q", spec.LogPrefix, shortID(created.ID), options.Name)

	defer func() {
		if err := removeContainer(ctx, api, created.ID); err != nil {
			logger.Printf("%s_container_remove_error id=%q error=%q", spec.LogPrefix, shortID(created.ID), err)
		}
	}()

	wait := api.ContainerWait(ctx, created.ID, dockerclient.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := api.ContainerStart(ctx, created.ID, dockerclient.ContainerStartOptions{}); err != nil {
		logger.Printf("%s_container_start_error id=%q error=%q", spec.LogPrefix, shortID(created.ID), err)
		return cleanupUnmountContainerLogs(ctx, api, created.ID, logger), fmt.Errorf("启动 cleanup 容器失败: %w", err)
	}
	logger.Printf("%s_container_started id=%q", spec.LogPrefix, shortID(created.ID))

	select {
	case err := <-wait.Error:
		if err != nil {
			logger.Printf("%s_container_wait_error id=%q error=%q", spec.LogPrefix, shortID(created.ID), err)
			return cleanupUnmountContainerLogs(ctx, api, created.ID, logger), fmt.Errorf("等待 cleanup 容器退出失败: %w", err)
		}
	case result := <-wait.Result:
		output := cleanupUnmountContainerLogs(ctx, api, created.ID, logger)
		if result.StatusCode != 0 {
			err := fmt.Errorf("cleanup 容器退出码 %d", result.StatusCode)
			if result.Error != nil && result.Error.Message != "" {
				err = fmt.Errorf("%w: %s", err, result.Error.Message)
			}
			logger.Printf("%s_container_failed id=%q status=%d output=%q", spec.LogPrefix, shortID(created.ID), result.StatusCode, output)
			return output, err
		}
		logger.Printf("%s_container_done id=%q output=%q", spec.LogPrefix, shortID(created.ID), output)
		return output, nil
	case <-ctx.Done():
		return cleanupUnmountContainerLogs(ctx, api, created.ID, logger), ctx.Err()
	}

	return cleanupUnmountContainerLogs(ctx, api, created.ID, logger), nil
}

func cleanupUnmountContainerCreateOptions(cfg Config, opts CleanupOptions, spec cleanupUnmountSpec) dockerclient.ContainerCreateOptions {
	return dockerclient.ContainerCreateOptions{
		Name: cleanupUnmountContainerName(spec.FriendlyName),
		Config: &container.Config{
			Image:           cfg.HelperImage,
			Cmd:             cleanupUnmountCommand(opts, spec.ContainerTarget),
			Labels:          cleanupUnmountLabels(spec),
			NetworkDisabled: true,
		},
		HostConfig: &container.HostConfig{
			AutoRemove:     false,
			NetworkMode:    container.NetworkMode("none"),
			CapDrop:        []string{"ALL"},
			CapAdd:         []string{"SYS_ADMIN"},
			ReadonlyRootfs: true,
			SecurityOpt:    []string{"no-new-privileges:true"},
			Mounts:         cleanupUnmountMounts(cfg),
		},
	}
}

func cleanupUnmountLabels(spec cleanupUnmountSpec) map[string]string {
	return map[string]string{
		labelProject:      labelTrue,
		labelMode:         modeCleanup,
		labelFriendlyName: spec.FriendlyName,
	}
}

func cleanupUnmountMounts(cfg Config) []mount.Mount {
	return []mount.Mount{bridgeBindMount(cfg)}
}

func cleanupUnmountContainerTargetPath(friendlyName string) string {
	return path.Join(helperTargetRoot, friendlyName)
}

func cleanupUnmountContainerLogs(ctx context.Context, api DockerAPI, containerID string, logger Logger) string {
	logs, err := api.ContainerLogs(ctx, containerID, dockerclient.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err != nil {
		logger.Printf("cleanup_umount_container_logs_error id=%q error=%q", shortID(containerID), err)
		return ""
	}
	defer func() {
		_ = logs.Close()
	}()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logs); err != nil {
		logger.Printf("cleanup_umount_container_logs_read_error id=%q error=%q", shortID(containerID), err)
		return ""
	}

	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(stdout.String()); text != "" {
		parts = append(parts, text)
	}
	if text := strings.TrimSpace(stderr.String()); text != "" {
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n")
}

func cleanupTargetPath(visibleRoot string, friendlyName string) string {
	root := filepath.Clean(visibleRoot)
	return filepath.Join(root, friendlyName)
}

func cleanupTargetPathChecked(visibleRoot string, friendlyName string) (string, error) {
	root := filepath.Clean(visibleRoot)
	target := filepath.Clean(filepath.Join(root, friendlyName))
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", fmt.Errorf("检查挂载点路径失败: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("friendly name %q 会逃逸 visible root 路径 %s", friendlyName, root)
	}
	return target, nil
}

func cleanupRejectSymlinkEscape(visibleRoot string, targetPath string) error {
	root, err := filepath.EvalSymlinks(filepath.Clean(visibleRoot))
	if err != nil {
		return fmt.Errorf("检查 visible root 路径符号链接失败: %w", err)
	}
	target, err := filepath.EvalSymlinks(targetPath)
	if err != nil {
		return fmt.Errorf("检查挂载点符号链接失败: %w", err)
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("检查挂载点符号链接关系失败: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return fmt.Errorf("拒绝卸载逃逸 visible root 路径的挂载点 %s", targetPath)
	}
	return nil
}

func cleanupUnmountCommandArgs(opts CleanupOptions, targetPath string) []string {
	if opts.LazyUnmount {
		return []string{"-l", targetPath}
	}
	return []string{targetPath}
}

func cleanupUnmountCommand(opts CleanupOptions, targetPath string) []string {
	return append([]string{"umount"}, cleanupUnmountCommandArgs(opts, targetPath)...)
}

func cleanupUnmountFailedError(targetPath string, opts CleanupOptions, err error, mountInfo string, output string) error {
	message := fmt.Sprintf("普通 umount 失败，挂载点 %s 待处理，可能仍被 Kopia、业务容器或 shell 工作目录占用: %v", targetPath, err)
	if opts.LazyUnmount {
		message = fmt.Sprintf("lazy umount 失败，挂载点 %s 待处理: %v", targetPath, err)
	}
	if output != "" {
		message += "，命令输出 " + output
	}
	if mountInfo != "" {
		message += "，mountinfo " + mountInfo
	}

	hints := []string{
		fmt.Sprintf("用 `docker ps -a --filter label=%s=%s` 确认 helper 容器已经消失", labelProject, labelTrue),
		fmt.Sprintf("用 `findmnt %s` 或 `mountpoint %s` 确认挂载仍然存在", shellArg(targetPath), shellArg(targetPath)),
	}
	if !opts.LazyUnmount {
		hints = append(hints, "确认没有备份、业务进程或 shell 工作目录持有该路径后，再显式加 --dangerously-lazy-umount")
	}
	return withHints(errors.New(message), hints...)
}
