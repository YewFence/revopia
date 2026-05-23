package bridge

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	dockerclient "github.com/moby/moby/client"
)

func ensureRestoreHelper(ctx context.Context, api DockerAPI, cfg Config, session restoreSession, logger Logger) (string, error) {
	expectedName := restoreHelperContainerName(session.SessionID)
	helpers, err := listRestoreHelpersForSession(ctx, api, session.SessionID)
	if err != nil {
		return "", err
	}
	logger.Printf("restore_helper_list session=%q expected_name=%q count=%d", session.SessionID, expectedName, len(helpers))

	recreated := false
	for _, helper := range helpers {
		logger.Printf("restore_helper_candidate id=%q names=%q state=%q labels=%q", shortID(helper.ID), strings.Join(helper.Names, ","), helper.State, formatLabels(helper.Labels))
		if !containerHasName(helper, expectedName) {
			logger.Printf("restore_helper_remove_unexpected_name id=%q expected_name=%q", shortID(helper.ID), expectedName)
			if err := removeContainer(ctx, api, helper.ID); err != nil {
				return "", err
			}
			recreated = true
			continue
		}

		inspected, err := api.ContainerInspect(ctx, helper.ID, dockerclient.ContainerInspectOptions{})
		if err != nil {
			if cerrdefs.IsNotFound(err) {
				logger.Printf("restore_helper_inspect_not_found id=%q", shortID(helper.ID))
				recreated = true
				continue
			}
			logger.Printf("restore_helper_inspect_error id=%q error=%q", shortID(helper.ID), err)
			return "", err
		}
		logger.Printf("restore_helper_inspected %s", describeRestoreInspect(inspected.Container, cfg, session))

		if restoreHelperMatches(inspected.Container, cfg, session) {
			if inspected.Container.State != nil && inspected.Container.State.Running {
				logger.Printf("restore_helper_reuse id=%q", shortID(helper.ID))
				return "复用", nil
			}
			if _, err := api.ContainerStart(ctx, helper.ID, dockerclient.ContainerStartOptions{}); err != nil {
				if !cerrdefs.IsNotFound(err) {
					logger.Printf("restore_helper_start_error id=%q error=%q", shortID(helper.ID), err)
					return "", err
				}
				logger.Printf("restore_helper_start_not_found id=%q", shortID(helper.ID))
				recreated = true
				continue
			}
			logger.Printf("restore_helper_started id=%q", shortID(helper.ID))
			return "启动", nil
		}

		logger.Printf("restore_helper_remove_mismatch id=%q", shortID(helper.ID))
		if err := removeContainer(ctx, api, helper.ID); err != nil {
			return "", err
		}
		recreated = true
	}

	if err := createAndStartRestoreHelper(ctx, api, cfg, session, logger); err != nil {
		return "", err
	}
	if recreated {
		return "重建", nil
	}
	return "创建", nil
}

func createAndStartRestoreHelper(ctx context.Context, api DockerAPI, cfg Config, session restoreSession, logger Logger) error {
	options := restoreHelperCreateOptions(cfg, session)
	logger.Printf("restore_helper_create name=%q image=%q mounts=%q labels=%q", options.Name, cfg.HelperImage, formatCreateMounts(options.HostConfig.Mounts), formatLabels(options.Config.Labels))
	created, pulled, err := createContainerWithAutoPull(ctx, api, options, logger, "restore_helper")
	if err != nil {
		logger.Printf("restore_helper_create_error name=%q error=%q", options.Name, err)
		if cerrdefs.IsNotFound(err) {
			return restoreHelperImageNotFoundError(cfg.HelperImage, err)
		}
		if cerrdefs.IsAlreadyExists(err) || cerrdefs.IsConflict(err) {
			return restoreHelperNameConflictError(session, options.Name, err)
		}
		return restoreHelperCreateError(session, err)
	}
	logger.Printf("restore_helper_created id=%q name=%q image_pulled=%t", shortID(created.ID), options.Name, pulled)

	if _, err := api.ContainerStart(ctx, created.ID, dockerclient.ContainerStartOptions{}); err != nil {
		logger.Printf("restore_helper_start_error id=%q error=%q", shortID(created.ID), err)
		_ = removeContainer(ctx, api, created.ID)
		return restoreHelperStartError(session, err)
	}
	logger.Printf("restore_helper_started id=%q", shortID(created.ID))
	return nil
}

func restoreHelperCreateOptions(cfg Config, session restoreSession) dockerclient.ContainerCreateOptions {
	return dockerclient.ContainerCreateOptions{
		Name: restoreHelperContainerName(session.SessionID),
		Config: &container.Config{
			Image:           cfg.HelperImage,
			Cmd:             slices.Clone(helperCommand),
			Labels:          restoreHelperLabels(session),
			NetworkDisabled: true,
		},
		HostConfig: &container.HostConfig{
			AutoRemove:  true,
			NetworkMode: container.NetworkMode("none"),
			Mounts:      restoreHelperMounts(cfg, session),
		},
	}
}

func restoreHelperLabels(session restoreSession) map[string]string {
	return map[string]string{
		labelProject:      labelTrue,
		labelMode:         modeRestore,
		labelSession:      session.SessionID,
		labelSourceVolume: session.SourceVolume,
		labelTargetVolume: session.TargetVolume,
		labelFriendlyName: session.FriendlyName,
	}
}

func restoreHelperMounts(cfg Config, session restoreSession) []mount.Mount {
	return []mount.Mount{
		bridgeBindMount(cfg),
		{
			Type:   mount.TypeVolume,
			Source: session.TargetVolume,
			Target: path.Join(helperTargetRoot, restoreTargetSubdir, session.FriendlyName),
		},
	}
}

func restoreHelperMatches(found container.InspectResponse, cfg Config, session restoreSession) bool {
	if found.Config == nil || found.HostConfig == nil {
		return false
	}
	if found.Config.Image != cfg.HelperImage {
		return false
	}
	for key, want := range restoreHelperLabels(session) {
		if found.Config.Labels[key] != want {
			return false
		}
	}
	if !slices.Equal(found.Config.Cmd, helperCommand) {
		return false
	}
	if found.HostConfig.NetworkMode != container.NetworkMode("none") {
		return false
	}
	if !found.HostConfig.AutoRemove {
		return false
	}
	return mountsMatch(found.HostConfig.Mounts, restoreHelperMounts(cfg, session))
}

func listRestoreHelpersForSession(ctx context.Context, api DockerAPI, sessionID string) ([]container.Summary, error) {
	result, err := api.ContainerList(ctx, dockerclient.ContainerListOptions{
		All: true,
		Filters: make(dockerclient.Filters).
			Add("label", labelProject+"="+labelTrue).
			Add("label", labelMode+"="+modeRestore).
			Add("label", labelSession+"="+sessionID),
	})
	if err != nil {
		return nil, fmt.Errorf("扫描恢复 helper 容器失败: %w", err)
	}
	return result.Items, nil
}
