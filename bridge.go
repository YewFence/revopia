package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const (
	labelBackupEnable = "backup.enable"
	labelBackupName   = "backup.name"

	labelProject      = "kopia.volume-bridge"
	labelVolume       = "kopia.volume-bridge.volume"
	labelFriendlyName = "kopia.volume-bridge.name"

	labelTrue = "true"

	helperNamePrefix = "kopia-volume-bridge-"
	helperTargetRoot = "/bridge"
)

var helperCommand = []string{"sleep", "infinity"}

type dockerAPI interface {
	VolumeList(context.Context, dockerclient.VolumeListOptions) (dockerclient.VolumeListResult, error)
	ContainerList(context.Context, dockerclient.ContainerListOptions) (dockerclient.ContainerListResult, error)
	ContainerInspect(context.Context, string, dockerclient.ContainerInspectOptions) (dockerclient.ContainerInspectResult, error)
	ContainerCreate(context.Context, dockerclient.ContainerCreateOptions) (dockerclient.ContainerCreateResult, error)
	ContainerStart(context.Context, string, dockerclient.ContainerStartOptions) (dockerclient.ContainerStartResult, error)
	ContainerRemove(context.Context, string, dockerclient.ContainerRemoveOptions) (dockerclient.ContainerRemoveResult, error)
}

type bridgeConfig struct {
	BridgeSource string
	HelperImage  string
}

type volumeSpec struct {
	VolumeName   string
	FriendlyName string
}

func defaultBridgeConfig() bridgeConfig {
	return bridgeConfig{
		BridgeSource: getenvDefault("KOPIA_VOLUME_BRIDGE_SOURCE", "/mnt/volumes-backup"),
		HelperImage:  getenvDefault("KOPIA_VOLUME_BRIDGE_HELPER_IMAGE", "alpine"),
	}
}

func prepare(ctx context.Context, api dockerAPI, cfg bridgeConfig, out io.Writer) error {
	if strings.TrimSpace(cfg.BridgeSource) == "" {
		return fmt.Errorf("bridge source 不能为空")
	}
	if strings.TrimSpace(cfg.HelperImage) == "" {
		return fmt.Errorf("helper image 不能为空")
	}

	volumes, err := api.VolumeList(ctx, dockerclient.VolumeListOptions{
		Filters: make(dockerclient.Filters).Add("label", labelBackupEnable+"="+labelTrue),
	})
	if err != nil {
		return fmt.Errorf("扫描 Docker volume 失败: %w", err)
	}

	specs, specErr := buildVolumeSpecs(volumes.Items)
	if len(specs) == 0 {
		if specErr != nil {
			return specErr
		}
		fmt.Fprintln(out, "没有发现启用备份的 Docker volume")
		return nil
	}

	var errs []error
	if specErr != nil {
		errs = append(errs, specErr)
	}
	for _, spec := range specs {
		action, err := ensureHelper(ctx, api, cfg, spec)
		if err != nil {
			errs = append(errs, fmt.Errorf("准备 volume %q 失败: %w", spec.VolumeName, err))
			continue
		}
		fmt.Fprintf(out, "%s %s -> %s\n", action, spec.VolumeName, path.Join("/volumes", spec.FriendlyName))
	}

	return errors.Join(errs...)
}

func cleanup(ctx context.Context, api dockerAPI, out io.Writer) error {
	helpers, err := listProjectHelpers(ctx, api)
	if err != nil {
		return err
	}

	var errs []error
	removed := 0
	for _, helper := range helpers {
		if !isManagedHelperSummary(helper) {
			continue
		}
		if err := removeContainer(ctx, api, helper.ID); err != nil {
			errs = append(errs, fmt.Errorf("清理 helper 容器 %q 失败: %w", helper.ID, err))
			continue
		}
		removed++
	}

	fmt.Fprintf(out, "已清理 %d 个 helper 容器\n", removed)
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

func ensureHelper(ctx context.Context, api dockerAPI, cfg bridgeConfig, spec volumeSpec) (string, error) {
	expectedName := helperContainerName(spec.VolumeName)
	helpers, err := listHelpersForVolume(ctx, api, spec.VolumeName)
	if err != nil {
		return "", err
	}

	recreated := false
	for _, helper := range helpers {
		if !containerHasName(helper, expectedName) {
			if err := removeContainer(ctx, api, helper.ID); err != nil {
				return "", err
			}
			recreated = true
			continue
		}

		inspected, err := api.ContainerInspect(ctx, helper.ID, dockerclient.ContainerInspectOptions{})
		if err != nil {
			if cerrdefs.IsNotFound(err) {
				recreated = true
				continue
			}
			return "", err
		}

		if helperMatches(inspected.Container, cfg, spec) {
			if inspected.Container.State != nil && inspected.Container.State.Running {
				return "复用", nil
			}
			if _, err := api.ContainerStart(ctx, helper.ID, dockerclient.ContainerStartOptions{}); err != nil {
				if !cerrdefs.IsNotFound(err) {
					return "", err
				}
				recreated = true
				continue
			}
			return "启动", nil
		}

		if err := removeContainer(ctx, api, helper.ID); err != nil {
			return "", err
		}
		recreated = true
	}

	if err := createAndStartHelper(ctx, api, cfg, spec); err != nil {
		return "", err
	}
	if recreated {
		return "重建", nil
	}
	return "创建", nil
}

func createAndStartHelper(ctx context.Context, api dockerAPI, cfg bridgeConfig, spec volumeSpec) error {
	options := helperCreateOptions(cfg, spec)
	created, err := api.ContainerCreate(ctx, options)
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return fmt.Errorf("helper 镜像 %q 不存在，请先拉取这个镜像: %w", cfg.HelperImage, err)
		}
		if cerrdefs.IsAlreadyExists(err) || cerrdefs.IsConflict(err) {
			return fmt.Errorf("helper 容器名称 %q 已被占用: %w", options.Name, err)
		}
		return err
	}

	if _, err := api.ContainerStart(ctx, created.ID, dockerclient.ContainerStartOptions{}); err != nil {
		_ = removeContainer(ctx, api, created.ID)
		return err
	}
	return nil
}

func helperCreateOptions(cfg bridgeConfig, spec volumeSpec) dockerclient.ContainerCreateOptions {
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
		labelVolume:       spec.VolumeName,
		labelFriendlyName: spec.FriendlyName,
	}
}

func helperMounts(cfg bridgeConfig, spec volumeSpec) []mount.Mount {
	return []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: cfg.BridgeSource,
			Target: helperTargetRoot,
			BindOptions: &mount.BindOptions{
				Propagation: mount.PropagationRShared,
			},
		},
		{
			Type:     mount.TypeVolume,
			Source:   spec.VolumeName,
			Target:   path.Join(helperTargetRoot, spec.FriendlyName),
			ReadOnly: true,
		},
	}
}

func helperMatches(found container.InspectResponse, cfg bridgeConfig, spec volumeSpec) bool {
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

func listHelpersForVolume(ctx context.Context, api dockerAPI, volumeName string) ([]container.Summary, error) {
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

func listProjectHelpers(ctx context.Context, api dockerAPI) ([]container.Summary, error) {
	result, err := api.ContainerList(ctx, dockerclient.ContainerListOptions{
		All:     true,
		Filters: make(dockerclient.Filters).Add("label", labelProject+"="+labelTrue),
	})
	if err != nil {
		return nil, fmt.Errorf("扫描 helper 容器失败: %w", err)
	}
	return result.Items, nil
}

func removeContainer(ctx context.Context, api dockerAPI, id string) error {
	_, err := api.ContainerRemove(ctx, id, dockerclient.ContainerRemoveOptions{
		Force: true,
	})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return err
	}
	return nil
}

func helperContainerName(volumeName string) string {
	sum := sha256.Sum256([]byte(volumeName))
	return helperNamePrefix + hex.EncodeToString(sum[:8])
}

func friendlyNameForVolume(vol volume.Volume) (string, error) {
	if name := sanitizePathName(vol.Labels[labelBackupName]); name != "" {
		return name, nil
	}
	if name := sanitizePathName(vol.Name); name != "" {
		return name, nil
	}
	return "", fmt.Errorf("volume %q 没有可用的路径安全名称，已跳过", vol.Name)
}

func sanitizePathName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		if isSafeNameRune(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}

	name := strings.Trim(b.String(), ".-")
	if name == "." || name == ".." {
		return ""
	}
	return name
}

func isSafeNameRune(r rune) bool {
	return r >= 'a' && r <= 'z' ||
		r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' ||
		r == '.' ||
		r == '_' ||
		r == '-'
}

func containerHasName(summary container.Summary, name string) bool {
	for _, item := range summary.Names {
		if strings.TrimPrefix(item, "/") == name {
			return true
		}
	}
	return false
}

func isManagedHelperSummary(summary container.Summary) bool {
	return summary.Labels[labelProject] == labelTrue &&
		summary.Labels[labelVolume] != "" &&
		summary.Labels[labelFriendlyName] != ""
}

func getenvDefault(key, fallback string) string {
	if value := strings.TrimSpace(getenv(key)); value != "" {
		return value
	}
	return fallback
}

var getenv = func(key string) string {
	return strings.TrimSpace(strings.Trim(os.Getenv(key), "\x00"))
}
