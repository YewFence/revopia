package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"
	dockerclient "github.com/moby/moby/client"
)

func Prepare(ctx context.Context, api DockerAPI, cfg Config, out io.Writer, logger Logger) error {
	if strings.TrimSpace(cfg.BridgeSource) == "" {
		return fmt.Errorf("bridge source 不能为空")
	}
	if strings.TrimSpace(cfg.VisibleRoot) == "" {
		return fmt.Errorf("visible root 不能为空")
	}
	if strings.TrimSpace(cfg.HelperImage) == "" {
		return fmt.Errorf("helper image 不能为空")
	}

	logger.Printf("prepare_start bridge_source=%q visible_root=%q helper_image=%q", cfg.BridgeSource, cfg.VisibleRoot, cfg.HelperImage)
	volumes, err := api.VolumeList(ctx, dockerclient.VolumeListOptions{
		Filters: make(dockerclient.Filters).Add("label", labelBackupEnable+"="+labelTrue),
	})
	if err != nil {
		logger.Printf("volume_list_error error=%q", err)
		return fmt.Errorf("扫描 Docker volume 失败: %w", err)
	}
	logger.Printf("volume_list count=%d warnings=%q", len(volumes.Items), strings.Join(volumes.Warnings, "; "))
	for _, vol := range volumes.Items {
		logger.Printf("volume_found name=%q backup_name=%q mountpoint=%q labels=%q", vol.Name, vol.Labels[labelBackupName], vol.Mountpoint, formatLabels(vol.Labels))
	}

	specs, specErr := buildVolumeSpecs(volumes.Items)
	if len(specs) == 0 {
		if specErr != nil {
			logger.Printf("volume_specs_error error=%q", specErr)
			return specErr
		}
		logger.Printf("volume_specs_empty")
		return writeLine(out, "没有发现启用备份的 Docker volume")
	}

	var errs []error
	if specErr != nil {
		logger.Printf("volume_specs_partial_error error=%q", specErr)
		errs = append(errs, specErr)
	}
	for _, spec := range specs {
		logger.Printf("volume_prepare_start volume=%q friendly=%q expected_helper=%q visible_path=%q", spec.VolumeName, spec.FriendlyName, helperContainerName(spec.VolumeName), visiblePath(cfg, spec))
		action, err := ensureHelper(ctx, api, cfg, spec, logger)
		if err != nil {
			logger.Printf("volume_prepare_error volume=%q friendly=%q error=%q", spec.VolumeName, spec.FriendlyName, err)
			errs = append(errs, fmt.Errorf("准备 volume %q 失败: %w", spec.VolumeName, err))
			continue
		}
		status, err := waitForVisibleMount(ctx, cfg, spec, logger)
		if err != nil {
			logger.Printf("volume_visible_error volume=%q friendly=%q status=%s error=%q", spec.VolumeName, spec.FriendlyName, status.String(), err)
			errs = append(errs, fmt.Errorf("volume %q 没有在 %q 中变成可见挂载: %w", spec.VolumeName, visiblePath(cfg, spec), err))
			continue
		}
		logger.Printf("volume_visible_ok volume=%q friendly=%q status=%s", spec.VolumeName, spec.FriendlyName, status.String())
		if err := writef(out, "%s %s -> %s\n", action, spec.VolumeName, visiblePath(cfg, spec)); err != nil {
			errs = append(errs, err)
			continue
		}
	}

	if target, ok := kopiaSnapshotPathForSource(cfg, specs, os.Getenv("KOPIA_SOURCE_PATH")); ok {
		logger.Printf("prepare_kopia_snapshot_path source=%q target=%q", os.Getenv("KOPIA_SOURCE_PATH"), target)
		if err := writef(out, "KOPIA_SNAPSHOT_PATH=%s\n", target); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) == 0 {
		logger.Printf("prepare_done result=ok")
	} else {
		logger.Printf("prepare_done result=error count=%d", len(errs))
	}
	return errors.Join(errs...)
}

func buildVolumeSpecs(volumes []volume.Volume) ([]volumeSpec, error) {
	sort.Slice(volumes, func(i, j int) bool {
		return volumes[i].Name < volumes[j].Name
	})

	usedNames := make(map[string]string, len(volumes))
	specs := make([]volumeSpec, 0, len(volumes))
	var errs []error

	for _, vol := range volumes {
		friendlyName, err := friendlyNameForVolume(vol)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if otherVolume, ok := usedNames[friendlyName]; ok {
			errs = append(errs, fmt.Errorf("volume %q 和 %q 清洗后都指向 %q", otherVolume, vol.Name, friendlyName))
			continue
		}

		usedNames[friendlyName] = vol.Name
		specs = append(specs, volumeSpec{
			VolumeName:   vol.Name,
			FriendlyName: friendlyName,
		})
	}

	return specs, errors.Join(errs...)
}

func ensureHelper(ctx context.Context, api DockerAPI, cfg Config, spec volumeSpec, logger Logger) (string, error) {
	expectedName := helperContainerName(spec.VolumeName)
	helpers, err := listHelpersForVolume(ctx, api, spec.VolumeName)
	if err != nil {
		return "", err
	}
	logger.Printf("helper_list volume=%q expected_name=%q count=%d", spec.VolumeName, expectedName, len(helpers))

	recreated := false
	for _, helper := range helpers {
		logger.Printf("helper_candidate id=%q names=%q state=%q labels=%q", shortID(helper.ID), strings.Join(helper.Names, ","), helper.State, formatLabels(helper.Labels))
		if !containerHasName(helper, expectedName) {
			logger.Printf("helper_remove_unexpected_name id=%q expected_name=%q", shortID(helper.ID), expectedName)
			if err := removeContainer(ctx, api, helper.ID); err != nil {
				return "", err
			}
			recreated = true
			continue
		}

		inspected, err := api.ContainerInspect(ctx, helper.ID, dockerclient.ContainerInspectOptions{})
		if err != nil {
			if cerrdefs.IsNotFound(err) {
				logger.Printf("helper_inspect_not_found id=%q", shortID(helper.ID))
				recreated = true
				continue
			}
			logger.Printf("helper_inspect_error id=%q error=%q", shortID(helper.ID), err)
			return "", err
		}
		logger.Printf("helper_inspected %s", describeInspect(inspected.Container, cfg, spec))

		if helperMatches(inspected.Container, cfg, spec) {
			if inspected.Container.State != nil && inspected.Container.State.Running {
				logger.Printf("helper_reuse id=%q", shortID(helper.ID))
				return "复用", nil
			}
			if _, err := api.ContainerStart(ctx, helper.ID, dockerclient.ContainerStartOptions{}); err != nil {
				if !cerrdefs.IsNotFound(err) {
					logger.Printf("helper_start_error id=%q error=%q", shortID(helper.ID), err)
					return "", err
				}
				logger.Printf("helper_start_not_found id=%q", shortID(helper.ID))
				recreated = true
				continue
			}
			logger.Printf("helper_started id=%q", shortID(helper.ID))
			return "启动", nil
		}

		logger.Printf("helper_remove_mismatch id=%q", shortID(helper.ID))
		if err := removeContainer(ctx, api, helper.ID); err != nil {
			return "", err
		}
		recreated = true
	}

	if err := createAndStartHelper(ctx, api, cfg, spec, logger); err != nil {
		return "", err
	}
	if recreated {
		return "重建", nil
	}
	return "创建", nil
}

func createAndStartHelper(ctx context.Context, api DockerAPI, cfg Config, spec volumeSpec, logger Logger) error {
	options := helperCreateOptions(cfg, spec)
	logger.Printf("helper_create name=%q image=%q mounts=%q labels=%q", options.Name, cfg.HelperImage, formatCreateMounts(options.HostConfig.Mounts), formatLabels(options.Config.Labels))
	created, err := api.ContainerCreate(ctx, options)
	if err != nil {
		logger.Printf("helper_create_error name=%q error=%q", options.Name, err)
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("helper 镜像 %q 不存在，请先拉取这个镜像: %w", cfg.HelperImage, err)
		}
		if cerrdefs.IsAlreadyExists(err) || cerrdefs.IsConflict(err) {
			return fmt.Errorf("helper 容器名称 %q 已被占用: %w", options.Name, err)
		}
		return err
	}
	logger.Printf("helper_created id=%q name=%q", shortID(created.ID), options.Name)

	if _, err := api.ContainerStart(ctx, created.ID, dockerclient.ContainerStartOptions{}); err != nil {
		logger.Printf("helper_start_error id=%q error=%q", shortID(created.ID), err)
		_ = removeContainer(ctx, api, created.ID)
		return err
	}
	logger.Printf("helper_started id=%q", shortID(created.ID))
	return nil
}

func helperCreateOptions(cfg Config, spec volumeSpec) dockerclient.ContainerCreateOptions {
	return dockerclient.ContainerCreateOptions{
		Name: helperContainerName(spec.VolumeName),
		Config: &container.Config{
			Image:           cfg.HelperImage,
			Cmd:             slices.Clone(helperCommand),
			Labels:          helperLabels(spec),
			NetworkDisabled: true,
		},
		HostConfig: &container.HostConfig{
			AutoRemove:  true,
			NetworkMode: container.NetworkMode("none"),
			Mounts:      helperMounts(cfg, spec),
		},
	}
}

func helperLabels(spec volumeSpec) map[string]string {
	return map[string]string{
		labelProject:      labelTrue,
		labelMode:         modeBackup,
		labelVolume:       spec.VolumeName,
		labelFriendlyName: spec.FriendlyName,
	}
}

func helperMounts(cfg Config, spec volumeSpec) []mount.Mount {
	return []mount.Mount{
		bridgeBindMount(cfg),
		{
			Type:     mount.TypeVolume,
			Source:   spec.VolumeName,
			Target:   path.Join(helperTargetRoot, spec.FriendlyName),
			ReadOnly: true,
		},
	}
}

func bridgeBindMount(cfg Config) mount.Mount {
	return mount.Mount{
		Type:   mount.TypeBind,
		Source: cfg.BridgeSource,
		Target: helperTargetRoot,
		BindOptions: &mount.BindOptions{
			Propagation: mount.PropagationRShared,
		},
	}
}

func helperMatches(found container.InspectResponse, cfg Config, spec volumeSpec) bool {
	if found.Config == nil || found.HostConfig == nil {
		return false
	}
	if found.Config.Image != cfg.HelperImage {
		return false
	}
	for key, want := range helperLabels(spec) {
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
	return mountsMatch(found.HostConfig.Mounts, helperMounts(cfg, spec))
}

func mountsMatch(found, want []mount.Mount) bool {
	for _, expected := range want {
		if !hasMount(found, expected) {
			return false
		}
	}
	return true
}

func hasMount(found []mount.Mount, expected mount.Mount) bool {
	for _, item := range found {
		if item.Type != expected.Type || item.Target != expected.Target || item.ReadOnly != expected.ReadOnly {
			continue
		}
		switch expected.Type {
		case mount.TypeBind:
			if filepath.Clean(item.Source) != filepath.Clean(expected.Source) {
				continue
			}
			if item.BindOptions == nil || expected.BindOptions == nil {
				continue
			}
			if item.BindOptions.Propagation != expected.BindOptions.Propagation {
				continue
			}
			return true
		case mount.TypeVolume:
			if item.Source == expected.Source {
				return true
			}
		}
	}
	return false
}

func listHelpersForVolume(ctx context.Context, api DockerAPI, volumeName string) ([]container.Summary, error) {
	result, err := api.ContainerList(ctx, dockerclient.ContainerListOptions{
		All: true,
		Filters: make(dockerclient.Filters).
			Add("label", labelProject+"="+labelTrue).
			Add("label", labelVolume+"="+volumeName),
	})
	if err != nil {
		return nil, fmt.Errorf("扫描 helper 容器失败: %w", err)
	}
	return result.Items, nil
}

func isManagedHelperSummary(summary container.Summary) bool {
	mode := summary.Labels[labelMode]
	return summary.Labels[labelProject] == labelTrue &&
		(mode == "" || mode == modeBackup) &&
		summary.Labels[labelVolume] != "" &&
		summary.Labels[labelFriendlyName] != ""
}
