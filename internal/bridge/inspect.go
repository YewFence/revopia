package bridge

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/moby/moby/api/types/volume"
	dockerclient "github.com/moby/moby/client"
)

func InspectState(ctx context.Context, api DockerAPI, cfg Config, out io.Writer, logger Logger) error {
	if err := writef(out, "配置 bridge_source=%s visible_root=%s helper_image=%s\n", cfg.BridgeSource, cfg.VisibleRoot, cfg.HelperImage); err != nil {
		return err
	}
	logger.Printf("inspect_start bridge_source=%q visible_root=%q helper_image=%q", cfg.BridgeSource, cfg.VisibleRoot, cfg.HelperImage)

	rootStatus := inspectVisiblePathFunc(cfg.VisibleRoot)
	if err := writef(out, "可见根路径 %s\n", rootStatus.String()); err != nil {
		return err
	}
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
	if err := writef(out, "Docker volume 总数 %d，启用备份的 volume 数 %d\n", len(volumes.Items), len(enabled)); err != nil {
		return err
	}
	logger.Printf("inspect_volume_list total=%d enabled=%d", len(volumes.Items), len(enabled))

	specs, specErr := buildVolumeSpecs(enabled)
	if specErr != nil {
		if err := writef(out, "volume 名称检查发现问题 %v\n", specErr); err != nil {
			return err
		}
		logger.Printf("inspect_volume_specs_error error=%q", specErr)
	}

	specByVolume := make(map[string]volumeSpec, len(specs))
	for _, spec := range specs {
		specByVolume[spec.VolumeName] = spec
	}

	if len(enabled) == 0 {
		if err := writeLine(out, "没有发现 backup.enable=true 的 Docker volume"); err != nil {
			return err
		}
	}
	for _, vol := range enabled {
		friendly, err := friendlyNameForVolume(vol)
		if err != nil {
			if err := writef(out, "volume %s friendly_name_error=%v labels=%s\n", vol.Name, err, formatLabels(vol.Labels)); err != nil {
				return err
			}
			logger.Printf("inspect_volume_error name=%q error=%q labels=%q", vol.Name, err, formatLabels(vol.Labels))
			continue
		}
		spec := volumeSpec{VolumeName: vol.Name, FriendlyName: friendly}
		status := inspectVisiblePathFunc(visiblePath(cfg, spec))
		if err := writef(out, "volume %s backup_name=%q friendly=%s helper=%s mountpoint=%s visible=%s labels=%s\n", vol.Name, vol.Labels[labelBackupName], friendly, helperContainerName(vol.Name), vol.Mountpoint, status.String(), formatLabels(vol.Labels)); err != nil {
			return err
		}
		logger.Printf("inspect_volume name=%q backup_name=%q friendly=%q helper=%q mountpoint=%q visible=%s labels=%q", vol.Name, vol.Labels[labelBackupName], friendly, helperContainerName(vol.Name), vol.Mountpoint, status.String(), formatLabels(vol.Labels))
	}

	helpers, err := listProjectHelpers(ctx, api)
	if err != nil {
		logger.Printf("inspect_helper_list_error error=%q", err)
		return err
	}
	if err := writef(out, "helper 容器数 %d\n", len(helpers)); err != nil {
		return err
	}
	logger.Printf("inspect_helper_list count=%d", len(helpers))
	if len(helpers) == 0 {
		if err := writeLine(out, "没有发现 revopia=true 的 helper 容器"); err != nil {
			return err
		}
	}

	for _, helper := range helpers {
		volumeName := helper.Labels[labelVolume]
		spec, knownVolume := specByVolume[volumeName]
		inspected, err := api.ContainerInspect(ctx, helper.ID, dockerclient.ContainerInspectOptions{})
		if err != nil {
			if err := writef(out, "helper %s inspect_error=%v names=%s labels=%s\n", shortID(helper.ID), err, strings.Join(helper.Names, ","), formatLabels(helper.Labels)); err != nil {
				return err
			}
			logger.Printf("inspect_helper_error id=%q names=%q error=%q labels=%q", shortID(helper.ID), strings.Join(helper.Names, ","), err, formatLabels(helper.Labels))
			continue
		}
		matches := knownVolume && helperMatches(inspected.Container, cfg, spec)
		if err := writef(out, "helper %s names=%s state=%s volume=%s friendly=%s known_volume=%t config_match=%t %s\n", shortID(helper.ID), strings.Join(helper.Names, ","), helper.State, volumeName, helper.Labels[labelFriendlyName], knownVolume, matches, describeInspect(inspected.Container, cfg, spec)); err != nil {
			return err
		}
		logger.Printf("inspect_helper id=%q names=%q state=%q volume=%q friendly=%q known_volume=%t config_match=%t %s", shortID(helper.ID), strings.Join(helper.Names, ","), helper.State, volumeName, helper.Labels[labelFriendlyName], knownVolume, matches, describeInspect(inspected.Container, cfg, spec))
	}

	logger.Printf("inspect_done")
	return nil
}
