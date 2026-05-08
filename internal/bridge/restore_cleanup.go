package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/moby/moby/api/types/container"
)

func RestoreCleanup(ctx context.Context, api DockerAPI, sessionID string, out io.Writer, logger Logger) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return restoreCleanupSessionMissingError()
	}
	logger.Printf("restore_cleanup_start session=%q", sessionID)
	helpers, err := listRestoreHelpersForSession(ctx, api, sessionID)
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
	if err := writef(out, "已清理 %d 个恢复 helper 容器，session=%s，目标 volume 不会被删除\n", removed, sessionID); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func isManagedRestoreHelperSummary(summary container.Summary, sessionID string) bool {
	return summary.Labels[labelProject] == labelTrue &&
		summary.Labels[labelMode] == modeRestore &&
		summary.Labels[labelSession] == sessionID &&
		summary.Labels[labelSourceVolume] != "" &&
		summary.Labels[labelTargetVolume] != "" &&
		summary.Labels[labelFriendlyName] != ""
}
