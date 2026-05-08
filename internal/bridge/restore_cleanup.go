package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
)

type restoreCleanupTarget struct {
	SessionID    string
	SourceVolume string
	TargetVolume string
	FriendlyName string
	ContainerIDs []string
}

func RestoreCleanup(ctx context.Context, api DockerAPI, cfg Config, opts CleanupOptions, sessionID string, out io.Writer, logger Logger) error {
	sessionID = strings.TrimSpace(sessionID)
	if strings.TrimSpace(cfg.BridgeSource) == "" {
		return restoreBridgeSourceMissingError()
	}
	if strings.TrimSpace(cfg.RestoreVisibleRoot) == "" {
		return restoreRootMissingError()
	}
	if strings.TrimSpace(cfg.HelperImage) == "" {
		return restoreHelperImageMissingError()
	}

	logger.Printf("restore_cleanup_start session=%q bridge_source=%q restore_root=%q helper_image=%q lazy_unmount=%t", sessionID, cfg.BridgeSource, cfg.RestoreVisibleRoot, cfg.HelperImage, opts.LazyUnmount)
	helpers, err := listRestoreHelpers(ctx, api, sessionID)
	if err != nil {
		logger.Printf("restore_cleanup_list_error session=%q error=%q", sessionID, err)
		return restoreCleanupListError(sessionID, err)
	}
	logger.Printf("restore_cleanup_list session=%q count=%d", sessionID, len(helpers))

	var errs []error
	targets := make(map[string]restoreCleanupTarget)
	blockedTargets := make(map[string]struct{})
	removed := 0
	for _, helper := range helpers {
		logger.Printf("restore_cleanup_candidate id=%q names=%q state=%q labels=%q", shortID(helper.ID), strings.Join(helper.Names, ","), helper.State, formatLabels(helper.Labels))
		if !isManagedRestoreHelperSummary(helper, sessionID) {
			logger.Printf("restore_cleanup_skip_incomplete_labels id=%q", shortID(helper.ID))
			continue
		}
		targetKey := restoreCleanupTargetKey(helper)
		target := targets[targetKey]
		target.SessionID = helper.Labels[labelSession]
		target.SourceVolume = helper.Labels[labelSourceVolume]
		target.TargetVolume = helper.Labels[labelTargetVolume]
		target.FriendlyName = helper.Labels[labelFriendlyName]
		target.ContainerIDs = append(target.ContainerIDs, helper.ID)
		targets[targetKey] = target

		if err := validateCleanupFriendlyName(target.FriendlyName); err != nil {
			logger.Printf("restore_cleanup_target_invalid id=%q friendly=%q error=%q", shortID(helper.ID), target.FriendlyName, err)
			errs = append(errs, fmt.Errorf("恢复 helper 容器 %q 的 friendly name 不安全，已跳过挂载回收: %w", helper.ID, err))
			blockedTargets[targetKey] = struct{}{}
		}
		if err := removeContainer(ctx, api, helper.ID); err != nil {
			logger.Printf("restore_cleanup_remove_error id=%q error=%q", shortID(helper.ID), err)
			errs = append(errs, fmt.Errorf("清理恢复 helper 容器 %q 失败: %w", helper.ID, err))
			blockedTargets[targetKey] = struct{}{}
			continue
		}
		logger.Printf("restore_cleanup_removed id=%q", shortID(helper.ID))
		removed++
	}

	unmounted := 0
	skipped := 0
	pending := 0
	for _, target := range sortedRestoreCleanupTargets(targets) {
		targetKey := restoreCleanupTargetKeyFromTarget(target)
		if _, blocked := blockedTargets[targetKey]; blocked {
			logger.Printf("restore_cleanup_umount_skip_blocked session=%q target=%q friendly=%q containers=%q", target.SessionID, target.TargetVolume, target.FriendlyName, strings.Join(target.ContainerIDs, ","))
			errs = append(errs, fmt.Errorf("恢复挂载点 %s 没有回收，因为对应恢复 helper 容器没有清理完成或标签不安全", cleanupTargetPath(cfg.RestoreVisibleRoot, target.FriendlyName)))
			pending++
			continue
		}

		result, err := restoreCleanupUnmount(ctx, api, cfg, opts, target, logger)
		if result.Output != "" {
			logger.Printf("restore_cleanup_umount_output session=%q target=%q friendly=%q output=%q", target.SessionID, target.TargetVolume, target.FriendlyName, result.Output)
		}
		if err != nil {
			logger.Printf("restore_cleanup_umount_error session=%q target=%q friendly=%q error=%q", target.SessionID, target.TargetVolume, target.FriendlyName, err)
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

	logger.Printf("restore_cleanup_done session=%q removed=%d unmounted=%d skipped=%d pending=%d errors=%d", sessionID, removed, unmounted, skipped, pending, len(errs))
	message := fmt.Sprintf("已清理 %d 个恢复 helper 容器，已回收 %d 个恢复挂载，跳过 %d 个非挂载路径，目标 volume 不会被删除", removed, unmounted, skipped)
	if sessionID != "" {
		message = fmt.Sprintf("已清理 %d 个恢复 helper 容器，session=%s，已回收 %d 个恢复挂载，跳过 %d 个非挂载路径，目标 volume 不会被删除", removed, sessionID, unmounted, skipped)
	}
	if pending > 0 {
		message += fmt.Sprintf("，待处理 %d 个挂载点", pending)
	}
	if err := writeLine(out, message); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func restoreCleanupUnmount(ctx context.Context, api DockerAPI, cfg Config, opts CleanupOptions, target restoreCleanupTarget, logger Logger) (cleanupUnmountResult, error) {
	return cleanupUnmountAt(ctx, api, cfg, opts, cleanupUnmountSpec{
		LogPrefix:       "restore_cleanup_umount",
		FriendlyName:    target.FriendlyName,
		ContainerIDs:    target.ContainerIDs,
		Volumes:         []string{target.TargetVolume},
		VisibleRoot:     cfg.RestoreVisibleRoot,
		ContainerTarget: restoreCleanupContainerTargetPath(target.FriendlyName),
	}, logger)
}

func sortedRestoreCleanupTargets(targets map[string]restoreCleanupTarget) []restoreCleanupTarget {
	keys := make([]string, 0, len(targets))
	for key := range targets {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]restoreCleanupTarget, 0, len(keys))
	for _, key := range keys {
		target := targets[key]
		sort.Strings(target.ContainerIDs)
		result = append(result, target)
	}
	return result
}

func restoreCleanupTargetKey(summary container.Summary) string {
	return strings.Join([]string{
		summary.Labels[labelSession],
		summary.Labels[labelSourceVolume],
		summary.Labels[labelTargetVolume],
		summary.Labels[labelFriendlyName],
	}, "\x00")
}

func restoreCleanupTargetKeyFromTarget(target restoreCleanupTarget) string {
	return strings.Join([]string{
		target.SessionID,
		target.SourceVolume,
		target.TargetVolume,
		target.FriendlyName,
	}, "\x00")
}

func restoreCleanupContainerTargetPath(friendlyName string) string {
	return path.Join(helperTargetRoot, restoreTargetSubdir, friendlyName)
}

func isManagedRestoreHelperSummary(summary container.Summary, sessionID string) bool {
	if sessionID != "" && summary.Labels[labelSession] != sessionID {
		return false
	}
	return summary.Labels[labelProject] == labelTrue &&
		summary.Labels[labelMode] == modeRestore &&
		summary.Labels[labelSession] != "" &&
		summary.Labels[labelSourceVolume] != "" &&
		summary.Labels[labelTargetVolume] != "" &&
		summary.Labels[labelFriendlyName] != ""
}

func listRestoreHelpers(ctx context.Context, api DockerAPI, sessionID string) ([]container.Summary, error) {
	filters := make(dockerclient.Filters).
		Add("label", labelProject+"="+labelTrue).
		Add("label", labelMode+"="+modeRestore)
	if sessionID != "" {
		filters.Add("label", labelSession+"="+sessionID)
	}
	result, err := api.ContainerList(ctx, dockerclient.ContainerListOptions{
		All:     true,
		Filters: filters,
	})
	if err != nil {
		return nil, fmt.Errorf("扫描恢复 helper 容器失败: %w", err)
	}
	return result.Items, nil
}
