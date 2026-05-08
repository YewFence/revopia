package cmd

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
	"strconv"
	"strings"
	"time"

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
	BridgeSource  string
	VisibleRoot   string
	HelperImage   string
	VerifyTimeout time.Duration
}

type volumeSpec struct {
	VolumeName   string
	FriendlyName string
}

func defaultBridgeConfig() bridgeConfig {
	return bridgeConfig{
		BridgeSource:  getenvDefault("KOPIA_VOLUME_BRIDGE_SOURCE", "/mnt/volumes-backup"),
		VisibleRoot:   getenvDefault("KOPIA_VOLUME_BRIDGE_VISIBLE_ROOT", "/volumes"),
		HelperImage:   getenvDefault("KOPIA_VOLUME_BRIDGE_HELPER_IMAGE", "alpine"),
		VerifyTimeout: defaultVerifyTimeout,
	}
}

func prepare(ctx context.Context, api dockerAPI, cfg bridgeConfig, out io.Writer, logger eventLogger) error {
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
		fmt.Fprintln(out, "没有发现启用备份的 Docker volume")
		return nil
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
		fmt.Fprintf(out, "%s %s -> %s\n", action, spec.VolumeName, visiblePath(cfg, spec))
	}

	if target, ok := kopiaSnapshotPathForSource(cfg, specs, os.Getenv("KOPIA_SOURCE_PATH")); ok {
		logger.Printf("prepare_kopia_snapshot_path source=%q target=%q", os.Getenv("KOPIA_SOURCE_PATH"), target)
		fmt.Fprintf(out, "KOPIA_SNAPSHOT_PATH=%s\n", target)
	}

	if len(errs) == 0 {
		logger.Printf("prepare_done result=ok")
	} else {
		logger.Printf("prepare_done result=error count=%d", len(errs))
	}
	return errors.Join(errs...)
}

func cleanup(ctx context.Context, api dockerAPI, out io.Writer, logger eventLogger) error {
	logger.Printf("cleanup_start")
	helpers, err := listProjectHelpers(ctx, api)
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

	fmt.Fprintf(out, "已清理 %d 个 helper 容器\n", removed)
	logger.Printf("cleanup_done removed=%d errors=%d", removed, len(errs))
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

func ensureHelper(ctx context.Context, api dockerAPI, cfg bridgeConfig, spec volumeSpec, logger eventLogger) (string, error) {
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

func createAndStartHelper(ctx context.Context, api dockerAPI, cfg bridgeConfig, spec volumeSpec, logger eventLogger) error {
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

func inspectState(ctx context.Context, api dockerAPI, cfg bridgeConfig, out io.Writer, logger eventLogger) error {
	fmt.Fprintf(out, "配置 bridge_source=%s visible_root=%s helper_image=%s\n", cfg.BridgeSource, cfg.VisibleRoot, cfg.HelperImage)
	logger.Printf("inspect_start bridge_source=%q visible_root=%q helper_image=%q", cfg.BridgeSource, cfg.VisibleRoot, cfg.HelperImage)

	rootStatus := inspectVisiblePath(cfg.VisibleRoot)
	fmt.Fprintf(out, "可见根路径 %s\n", rootStatus.String())
	logger.Printf("inspect_visible_root status=%s", rootStatus.String())

	volumes, err := api.VolumeList(ctx, dockerclient.VolumeListOptions{})
	if err != nil {
		logger.Printf("inspect_volume_list_error error=%q", err)
		return fmt.Errorf("扫描 Docker volume 失败: %w", err)
	}

	enabled := make([]volume.Volume, 0)
	for _, vol := range volumes.Items {
		if vol.Labels[labelBackupEnable] == labelTrue {
			enabled = append(enabled, vol)
		}
	}
	sort.Slice(enabled, func(i, j int) bool {
		return enabled[i].Name < enabled[j].Name
	})
	fmt.Fprintf(out, "Docker volume 总数 %d，启用备份的 volume 数 %d\n", len(volumes.Items), len(enabled))
	logger.Printf("inspect_volume_list total=%d enabled=%d", len(volumes.Items), len(enabled))

	specs, specErr := buildVolumeSpecs(enabled)
	if specErr != nil {
		fmt.Fprintf(out, "volume 名称检查发现问题 %v\n", specErr)
		logger.Printf("inspect_volume_specs_error error=%q", specErr)
	}

	specByVolume := make(map[string]volumeSpec, len(specs))
	for _, spec := range specs {
		specByVolume[spec.VolumeName] = spec
	}

	if len(enabled) == 0 {
		fmt.Fprintln(out, "没有发现 backup.enable=true 的 Docker volume")
	}
	for _, vol := range enabled {
		friendly, err := friendlyNameForVolume(vol)
		if err != nil {
			fmt.Fprintf(out, "volume %s friendly_name_error=%v labels=%s\n", vol.Name, err, formatLabels(vol.Labels))
			logger.Printf("inspect_volume_error name=%q error=%q labels=%q", vol.Name, err, formatLabels(vol.Labels))
			continue
		}
		spec := volumeSpec{VolumeName: vol.Name, FriendlyName: friendly}
		status := inspectVisiblePath(visiblePath(cfg, spec))
		fmt.Fprintf(out, "volume %s backup_name=%q friendly=%s helper=%s mountpoint=%s visible=%s labels=%s\n", vol.Name, vol.Labels[labelBackupName], friendly, helperContainerName(vol.Name), vol.Mountpoint, status.String(), formatLabels(vol.Labels))
		logger.Printf("inspect_volume name=%q backup_name=%q friendly=%q helper=%q mountpoint=%q visible=%s labels=%q", vol.Name, vol.Labels[labelBackupName], friendly, helperContainerName(vol.Name), vol.Mountpoint, status.String(), formatLabels(vol.Labels))
	}

	helpers, err := listProjectHelpers(ctx, api)
	if err != nil {
		logger.Printf("inspect_helper_list_error error=%q", err)
		return err
	}
	fmt.Fprintf(out, "helper 容器数 %d\n", len(helpers))
	logger.Printf("inspect_helper_list count=%d", len(helpers))
	if len(helpers) == 0 {
		fmt.Fprintln(out, "没有发现 kopia.volume-bridge=true 的 helper 容器")
	}

	for _, helper := range helpers {
		volumeName := helper.Labels[labelVolume]
		spec, knownVolume := specByVolume[volumeName]
		inspected, err := api.ContainerInspect(ctx, helper.ID, dockerclient.ContainerInspectOptions{})
		if err != nil {
			fmt.Fprintf(out, "helper %s inspect_error=%v names=%s labels=%s\n", shortID(helper.ID), err, strings.Join(helper.Names, ","), formatLabels(helper.Labels))
			logger.Printf("inspect_helper_error id=%q names=%q error=%q labels=%q", shortID(helper.ID), strings.Join(helper.Names, ","), err, formatLabels(helper.Labels))
			continue
		}
		matches := knownVolume && helperMatches(inspected.Container, cfg, spec)
		fmt.Fprintf(out, "helper %s names=%s state=%s volume=%s friendly=%s known_volume=%t config_match=%t %s\n", shortID(helper.ID), strings.Join(helper.Names, ","), helper.State, volumeName, helper.Labels[labelFriendlyName], knownVolume, matches, describeInspect(inspected.Container, cfg, spec))
		logger.Printf("inspect_helper id=%q names=%q state=%q volume=%q friendly=%q known_volume=%t config_match=%t %s", shortID(helper.ID), strings.Join(helper.Names, ","), helper.State, volumeName, helper.Labels[labelFriendlyName], knownVolume, matches, describeInspect(inspected.Container, cfg, spec))
	}

	logger.Printf("inspect_done")
	return nil
}

type eventLogger struct {
	out io.Writer
}

func (l eventLogger) Printf(format string, args ...any) {
	if l.out == nil {
		return
	}
	values := append([]any{time.Now().Format(time.RFC3339Nano)}, args...)
	_, _ = fmt.Fprintf(l.out, "ts=%s "+format+"\n", values...)
}

type visiblePathStatus struct {
	Path        string
	Exists      bool
	IsDir       bool
	IsMount     bool
	EntryCount  int
	EntrySample []string
	Err         string
}

func (s visiblePathStatus) String() string {
	parts := []string{
		"path=" + strconv.Quote(s.Path),
		fmt.Sprintf("exists=%t", s.Exists),
		fmt.Sprintf("dir=%t", s.IsDir),
		fmt.Sprintf("mount=%t", s.IsMount),
		fmt.Sprintf("entries=%d", s.EntryCount),
	}
	if len(s.EntrySample) > 0 {
		parts = append(parts, "sample="+strconv.Quote(strings.Join(s.EntrySample, ",")))
	}
	if s.Err != "" {
		parts = append(parts, "err="+strconv.Quote(s.Err))
	}
	return strings.Join(parts, " ")
}

func waitForVisibleMount(ctx context.Context, cfg bridgeConfig, spec volumeSpec, logger eventLogger) (visiblePathStatus, error) {
	deadline := time.Now().Add(cfg.VerifyTimeout)
	var status visiblePathStatus
	for {
		status = inspectVisiblePath(visiblePath(cfg, spec))
		if status.Exists && status.IsMount {
			return status, nil
		}
		if cfg.VerifyTimeout <= 0 || time.Now().After(deadline) {
			if !status.Exists {
				return status, fmt.Errorf("路径不存在")
			}
			if !status.IsMount {
				return status, fmt.Errorf("路径存在但不是挂载点")
			}
			return status, fmt.Errorf("路径不可用")
		}
		logger.Printf("visible_wait volume=%q friendly=%q status=%s", spec.VolumeName, spec.FriendlyName, status.String())
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status, ctx.Err()
		case <-timer.C:
		}
	}
}

func inspectVisiblePath(target string) visiblePathStatus {
	status := visiblePathStatus{Path: filepath.Clean(target)}
	info, err := os.Stat(status.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return status
		}
		status.Err = err.Error()
		return status
	}
	status.Exists = true
	status.IsDir = info.IsDir()

	isMount, err := isMountPoint(status.Path)
	if err != nil {
		status.Err = err.Error()
	} else {
		status.IsMount = isMount
	}

	if status.IsDir {
		entries, err := os.ReadDir(status.Path)
		if err != nil {
			if status.Err == "" {
				status.Err = err.Error()
			}
			return status
		}
		status.EntryCount = len(entries)
		limit := min(len(entries), 8)
		status.EntrySample = make([]string, 0, limit)
		for _, entry := range entries[:limit] {
			name := entry.Name()
			if entry.IsDir() {
				name += "/"
			}
			status.EntrySample = append(status.EntrySample, name)
		}
	}
	return status
}

func visiblePath(cfg bridgeConfig, spec volumeSpec) string {
	return filepath.Join(cfg.VisibleRoot, spec.FriendlyName)
}

func kopiaSnapshotPathForSource(cfg bridgeConfig, specs []volumeSpec, sourcePath string) (string, bool) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return "", false
	}

	cleanSource := filepath.Clean(sourcePath)
	for _, spec := range specs {
		target := visiblePath(cfg, spec)
		if filepath.Clean(target) == cleanSource {
			return target, true
		}
		rawTarget := filepath.Join(cfg.VisibleRoot, sanitizePathName(spec.VolumeName))
		if filepath.Clean(rawTarget) == cleanSource {
			return target, true
		}
	}
	return "", false
}

func isMountPoint(target string) (bool, error) {
	content, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	cleanTarget := filepath.Clean(target)
	for _, line := range strings.Split(string(content), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		mountPoint, err := decodeMountInfoPath(fields[4])
		if err != nil {
			return false, err
		}
		if filepath.Clean(mountPoint) == cleanTarget {
			return true, nil
		}
	}
	return false, nil
}

func decodeMountInfoPath(raw string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			b.WriteByte(raw[i])
			continue
		}
		if i+3 >= len(raw) {
			return "", fmt.Errorf("invalid mountinfo escape %q", raw[i:])
		}
		value, err := strconv.ParseInt(raw[i+1:i+4], 8, 32)
		if err != nil {
			return "", err
		}
		b.WriteByte(byte(value))
		i += 3
	}
	return b.String(), nil
}

func describeInspect(found container.InspectResponse, cfg bridgeConfig, spec volumeSpec) string {
	state := "<nil>"
	if found.State != nil {
		state = fmt.Sprintf("status=%s running=%t exit=%d error=%q", found.State.Status, found.State.Running, found.State.ExitCode, found.State.Error)
	}
	image := "<nil>"
	labels := ""
	cmd := ""
	if found.Config != nil {
		image = found.Config.Image
		labels = formatLabels(found.Config.Labels)
		cmd = strings.Join(found.Config.Cmd, " ")
	}
	networkMode := ""
	autoRemove := false
	mounts := ""
	if found.HostConfig != nil {
		networkMode = string(found.HostConfig.NetworkMode)
		autoRemove = found.HostConfig.AutoRemove
		mounts = formatCreateMounts(found.HostConfig.Mounts)
	}
	return fmt.Sprintf("image=%q cmd=%q %s network=%q autoremove=%t labels=%q mounts=%q expected_match=%t", image, cmd, state, networkMode, autoRemove, labels, mounts, helperMatches(found, cfg, spec))
}

func formatCreateMounts(mounts []mount.Mount) string {
	parts := make([]string, 0, len(mounts))
	for _, item := range mounts {
		piece := fmt.Sprintf("%s:%s->%s", item.Type, item.Source, item.Target)
		if item.ReadOnly {
			piece += ":ro"
		}
		if item.BindOptions != nil && item.BindOptions.Propagation != "" {
			piece += ":propagation=" + string(item.BindOptions.Propagation)
		}
		parts = append(parts, piece)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+labels[key])
	}
	return strings.Join(parts, ",")
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
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
