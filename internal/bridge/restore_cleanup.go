package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
)

func RestoreCleanup(ctx context.Context, api DockerAPI, sessionID string, out io.Writer, logger Logger) error {
	sessionID = strings.TrimSpace(sessionID)
	logger.Printf("restore_cleanup_start session=%q", sessionID)
	helpers, err := listRestoreHelpers(ctx, api, sessionID)
	if err != nil {
		logger.Printf("restore_cleanup_list_error session=%q error=%q", sessionID, err)
		return restoreCleanupListError(sessionID, err)
	}
	logger.Printf("restore_cleanup_list session=%q count=%d", sessionID, len(helpers))

	var errs []error
	removed := 0
	for _, helper := range helpers {
		logger.Printf("restore_cleanup_candidate id=%q names=%q state=%q labels=%q", shortID(helper.ID), strings.Join(helper.Names, ","), helper.State, formatLabels(helper.Labels))
		if !isManagedRestoreHelperSummary(helper, sessionID) {
			logger.Printf("restore_cleanup_skip_incomplete_labels id=%q", shortID(helper.ID))
			continue
		}
		if err := removeContainer(ctx, api, helper.ID); err != nil {
			logger.Printf("restore_cleanup_remove_error id=%q error=%q", shortID(helper.ID), err)
			errs = append(errs, fmt.Errorf("清理恢复 helper 容器 %q 失败: %w", helper.ID, err))
			continue
		}
		logger.Printf("restore_cleanup_removed id=%q", shortID(helper.ID))
		removed++
	}

	logger.Printf("restore_cleanup_done session=%q removed=%d errors=%d", sessionID, removed, len(errs))
	message := fmt.Sprintf("已清理 %d 个恢复 helper 容器，目标 volume 不会被删除\n", removed)
	if sessionID != "" {
		message = fmt.Sprintf("已清理 %d 个恢复 helper 容器，session=%s，目标 volume 不会被删除\n", removed, sessionID)
	}
	if err := writeLine(out, strings.TrimSuffix(message, "\n")); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
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
