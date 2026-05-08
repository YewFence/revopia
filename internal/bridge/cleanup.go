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

func Cleanup(ctx context.Context, api DockerAPI, out io.Writer, logger Logger) error {
	logger.Printf("cleanup_start")
	helpers, err := listBackupHelpers(ctx, api)
	if err != nil {
		logger.Printf("cleanup_list_error error=%q", err)
		return err
	}
	logger.Printf("cleanup_list count=%d", len(helpers))

	var errs []error
	removed := 0
	for _, helper := range helpers {
		logger.Printf("cleanup_candidate id=%q names=%q state=%q labels=%q", shortID(helper.ID), strings.Join(helper.Names, ","), helper.State, formatLabels(helper.Labels))
		if !isManagedHelperSummary(helper) {
			logger.Printf("cleanup_skip_incomplete_labels id=%q", shortID(helper.ID))
			continue
		}
		if err := removeContainer(ctx, api, helper.ID); err != nil {
			logger.Printf("cleanup_remove_error id=%q error=%q", shortID(helper.ID), err)
			errs = append(errs, fmt.Errorf("清理 helper 容器 %q 失败: %w", helper.ID, err))
			continue
		}
		logger.Printf("cleanup_removed id=%q", shortID(helper.ID))
		removed++
	}

	logger.Printf("cleanup_done removed=%d errors=%d", removed, len(errs))
	if err := writef(out, "已清理 %d 个 helper 容器\n", removed); err != nil {
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
