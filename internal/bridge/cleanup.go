package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
)

type cleanupTarget struct {
	FriendlyName string
	ContainerIDs []string
	Volumes      []string
}

func Cleanup(ctx context.Context, api DockerAPI, cfg Config, opts CleanupOptions, out io.Writer, logger Logger) error {
	if strings.TrimSpace(cfg.BridgeSource) == "" {
		return withHints(
			fmt.Errorf("bridge source 不能为空"),
			"用 --bridge-source 指定 Docker daemon 侧的宿主机传播桥路径，常见值是 /mnt/volumes-backup",
		)
	}
	if strings.TrimSpace(cfg.VisibleRoot) == "" {
		return withHints(
			fmt.Errorf("visible root 不能为空"),
			"用 --visible-root 指定当前进程可见的 volume 根路径，常见值是 /volumes",
		)
	}
	if strings.TrimSpace(cfg.HelperImage) == "" {
		return withHints(
			fmt.Errorf("helper image 不能为空"),
			"用 --helper-image 指定一个带 sh 和 umount 的小镜像，默认 alpine 就够用",
		)
	}

	logger.Printf("cleanup_start bridge_source=%q visible_root=%q helper_image=%q lazy_unmount=%t", cfg.BridgeSource, cfg.VisibleRoot, cfg.HelperImage, opts.LazyUnmount)
	helpers, err := listBackupHelpers(ctx, api)
	if err != nil {
		logger.Printf("cleanup_list_error error=%q", err)
		return err
	}
	logger.Printf("cleanup_list count=%d", len(helpers))

	var errs []error
	targets := make(map[string]cleanupTarget)
	blockedTargets := make(map[string]struct{})
	removed := 0
	for _, helper := range helpers {
		logger.Printf("cleanup_candidate id=%q names=%q state=%q labels=%q", shortID(helper.ID), strings.Join(helper.Names, ","), helper.State, formatLabels(helper.Labels))
		if !isManagedHelperSummary(helper) {
			logger.Printf("cleanup_skip_incomplete_labels id=%q", shortID(helper.ID))
			continue
		}
		friendlyName := helper.Labels[labelFriendlyName]
		if err := validateCleanupFriendlyName(friendlyName); err != nil {
			logger.Printf("cleanup_target_invalid id=%q friendly=%q error=%q", shortID(helper.ID), friendlyName, err)
			errs = append(errs, fmt.Errorf("helper 容器 %q 的 friendly name 不安全，已跳过挂载回收: %w", helper.ID, err))
		} else {
			target := targets[friendlyName]
			target.FriendlyName = friendlyName
			target.ContainerIDs = append(target.ContainerIDs, helper.ID)
			target.Volumes = append(target.Volumes, helper.Labels[labelVolume])
			targets[friendlyName] = target
		}
		if err := removeContainer(ctx, api, helper.ID); err != nil {
			logger.Printf("cleanup_remove_error id=%q error=%q", shortID(helper.ID), err)
			errs = append(errs, fmt.Errorf("清理 helper 容器 %q 失败: %w", helper.ID, err))
			if friendlyName != "" {
				blockedTargets[friendlyName] = struct{}{}
			}
			continue
		}
		logger.Printf("cleanup_removed id=%q", shortID(helper.ID))
		removed++
	}

	unmounted := 0
	skipped := 0
	pending := 0
	for _, target := range sortedCleanupTargets(targets) {
		if _, blocked := blockedTargets[target.FriendlyName]; blocked {
			logger.Printf("cleanup_umount_skip_blocked friendly=%q containers=%q", target.FriendlyName, strings.Join(target.ContainerIDs, ","))
			errs = append(errs, fmt.Errorf("挂载点 %s 没有回收，因为对应 helper 容器没有清理完成", cleanupTargetPath(cfg.VisibleRoot, target.FriendlyName)))
			pending++
			continue
		}
		result, err := cleanupUnmount(ctx, api, cfg, opts, target, logger)
		if result.Output != "" {
			logger.Printf("cleanup_umount_output friendly=%q output=%q", target.FriendlyName, result.Output)
		}
		if err != nil {
			logger.Printf("cleanup_umount_error friendly=%q error=%q", target.FriendlyName, err)
			errs = append(errs, err)
			pending++
			continue
		}
		if result.Unmounted {
			unmounted++
			continue
		}
		if result.Skipped {
			skipped++
		}
	}

	logger.Printf("cleanup_done removed=%d unmounted=%d skipped=%d pending=%d errors=%d", removed, unmounted, skipped, pending, len(errs))
	if err := writef(out, "已清理 %d 个 helper 容器，已回收 %d 个传播挂载，跳过 %d 个非挂载路径", removed, unmounted, skipped); err != nil {
		errs = append(errs, err)
	}
	if pending > 0 {
		if err := writef(out, "，待处理 %d 个挂载点", pending); err != nil {
			errs = append(errs, err)
		}
	}
	if err := writef(out, "\n"); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func listBackupHelpers(ctx context.Context, api DockerAPI) ([]container.Summary, error) {
	result, err := api.ContainerList(ctx, dockerclient.ContainerListOptions{
		All:     true,
		Filters: make(dockerclient.Filters).Add("label", labelProject+"="+labelTrue),
	})
	if err != nil {
		return nil, fmt.Errorf("扫描 helper 容器失败: %w", err)
	}
	return result.Items, nil
}

func listProjectHelpers(ctx context.Context, api DockerAPI) ([]container.Summary, error) {
	result, err := api.ContainerList(ctx, dockerclient.ContainerListOptions{
		All:     true,
		Filters: make(dockerclient.Filters).Add("label", labelProject+"="+labelTrue),
	})
	if err != nil {
		return nil, fmt.Errorf("扫描 helper 容器失败: %w", err)
	}
	return result.Items, nil
}

func removeContainer(ctx context.Context, api DockerAPI, id string) error {
	_, err := api.ContainerRemove(ctx, id, dockerclient.ContainerRemoveOptions{
		Force: true,
	})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}
