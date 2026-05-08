package bridge

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"
	dockerclient "github.com/moby/moby/client"
)

func Restore(ctx context.Context, api DockerAPI, cfg Config, opts RestoreOptions, out io.Writer, logger Logger) error {
	if err := validateRestoreInputs(cfg, opts); err != nil {
		return err
	}

	logger.Printf("restore_start source=%q target=%q session=%q bridge_source=%q restore_root=%q helper_image=%q allow_same=%t allow_non_empty=%t", opts.SourceVolume, opts.TargetVolume, opts.SessionID, cfg.BridgeSource, cfg.RestoreVisibleRoot, cfg.HelperImage, opts.AllowSourceTarget, opts.AllowNonEmptyTarget)

	source, err := api.VolumeInspect(ctx, opts.SourceVolume, dockerclient.VolumeInspectOptions{})
	if err != nil {
		logger.Printf("restore_source_inspect_error source=%q error=%q", opts.SourceVolume, err)
		if cerrdefs.IsNotFound(err) {
			return restoreSourceVolumeNotFoundError(opts.SourceVolume)
		}
		return restoreSourceVolumeInspectError(opts.SourceVolume, err)
	}

	friendlyName, err := friendlyNameForVolume(source.Volume)
	if err != nil {
		logger.Printf("restore_source_friendly_error source=%q error=%q", opts.SourceVolume, err)
		return restoreSourceFriendlyNameError(opts.SourceVolume, err)
	}
	sessionID := opts.SessionID
	if sessionID == "" {
		sessionID = defaultRestoreSessionID(opts.SourceVolume, opts.TargetVolume)
	}
	session := restoreSession{
		SourceVolume: opts.SourceVolume,
		TargetVolume: opts.TargetVolume,
		FriendlyName: friendlyName,
		SessionID:    sessionID,
		TargetPath:   restoreVisiblePath(cfg, friendlyName),
	}

	target, created, err := ensureRestoreTargetVolume(ctx, api, opts, session, logger)
	if err != nil {
		return err
	}
	session.TargetCreated = created
	logger.Printf("restore_target_ready target=%q created=%t labels=%q mountpoint=%q", target.Name, created, formatLabels(target.Labels), target.Mountpoint)

	if !opts.AllowNonEmptyTarget {
		if err := ensureVolumeEmpty(ctx, api, cfg, session, logger); err != nil {
			return err
		}
	}

	action, err := ensureRestoreHelper(ctx, api, cfg, session, logger)
	if err != nil {
		return err
	}
	session.HelperAction = action

	status, err := waitForRestoreVisibleMount(ctx, cfg, session, logger)
	if err != nil {
		logger.Printf("restore_visible_error session=%q status=%s error=%q", session.SessionID, status.String(), err)
		return restoreVisibleMountError(cfg, session, err)
	}
	logger.Printf("restore_visible_ok session=%q status=%s", session.SessionID, status.String())

	if err := printRestoreInstructions(out, cfg, opts, session); err != nil {
		return err
	}
	logger.Printf("restore_done session=%q action=%q target_path=%q", session.SessionID, session.HelperAction, session.TargetPath)
	return nil
}

func validateRestoreInputs(cfg Config, opts RestoreOptions) error {
	if strings.TrimSpace(cfg.BridgeSource) == "" {
		return restoreBridgeSourceMissingError()
	}
	if strings.TrimSpace(cfg.RestoreVisibleRoot) == "" {
		return restoreRootMissingError()
	}
	if strings.TrimSpace(cfg.HelperImage) == "" {
		return restoreHelperImageMissingError()
	}
	if strings.TrimSpace(opts.SourceVolume) == "" {
		return restoreSourceVolumeMissingError()
	}
	if strings.TrimSpace(opts.TargetVolume) == "" {
		return restoreTargetVolumeMissingError()
	}
	if opts.SourceVolume == opts.TargetVolume && !opts.AllowSourceTarget {
		return restoreSameSourceTargetError(opts.SourceVolume)
	}
	if opts.SessionID != "" && sanitizePathName(opts.SessionID) != opts.SessionID {
		return restoreUnsafeSessionIDError(opts)
	}
	if opts.SnapshotTime == "" {
		return nil
	}
	if strings.ContainsAny(opts.SnapshotTime, "\x00\r\n") {
		return restoreUnsafeSnapshotTimeError()
	}
	return nil
}

func ensureRestoreTargetVolume(ctx context.Context, api DockerAPI, opts RestoreOptions, session restoreSession, logger Logger) (volume.Volume, bool, error) {
	inspected, err := api.VolumeInspect(ctx, opts.TargetVolume, dockerclient.VolumeInspectOptions{})
	if err == nil {
		return inspected.Volume, false, nil
	}
	if !cerrdefs.IsNotFound(err) {
		logger.Printf("restore_target_inspect_error target=%q error=%q", opts.TargetVolume, err)
		return volume.Volume{}, false, restoreTargetVolumeInspectError(opts.TargetVolume, err)
	}

	labels := restoreTargetVolumeLabels(session)
	logger.Printf("restore_target_create target=%q labels=%q", opts.TargetVolume, formatLabels(labels))
	created, err := api.VolumeCreate(ctx, dockerclient.VolumeCreateOptions{
		Name:   opts.TargetVolume,
		Labels: labels,
	})
	if err != nil {
		logger.Printf("restore_target_create_error target=%q error=%q", opts.TargetVolume, err)
		return volume.Volume{}, false, restoreTargetVolumeCreateError(opts.TargetVolume, err)
	}
	return created.Volume, true, nil
}

func restoreTargetVolumeLabels(session restoreSession) map[string]string {
	return map[string]string{
		labelProject:       labelTrue,
		labelMode:          modeRestore,
		labelSession:       session.SessionID,
		labelSourceVolume:  session.SourceVolume,
		labelTargetVolume:  session.TargetVolume,
		labelFriendlyName:  session.FriendlyName,
		labelRestoreTarget: labelTrue,
		labelCreatedAt:     time.Now().UTC().Format(time.RFC3339),
		labelCreatedBy:     "volume-backup/" + appVersion,
	}
}

func ensureVolumeEmpty(ctx context.Context, api DockerAPI, cfg Config, session restoreSession, logger Logger) error {
	options := emptyCheckContainerCreateOptions(cfg, session)
	logger.Printf("restore_empty_check_create name=%q image=%q mounts=%q", options.Name, cfg.HelperImage, formatCreateMounts(options.HostConfig.Mounts))
	created, err := api.ContainerCreate(ctx, options)
	if err != nil {
		logger.Printf("restore_empty_check_create_error target=%q error=%q", session.TargetVolume, err)
		return restoreEmptyCheckCreateError(cfg, session.TargetVolume, err)
	}
	defer func() {
		if err := removeContainer(ctx, api, created.ID); err != nil {
			logger.Printf("restore_empty_check_remove_error id=%q error=%q", shortID(created.ID), err)
		}
	}()

	wait := api.ContainerWait(ctx, created.ID, dockerclient.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := api.ContainerStart(ctx, created.ID, dockerclient.ContainerStartOptions{}); err != nil {
		logger.Printf("restore_empty_check_start_error id=%q error=%q", shortID(created.ID), err)
		return restoreEmptyCheckStartError(session.TargetVolume, err)
	}

	select {
	case err := <-wait.Error:
		if err != nil {
			logger.Printf("restore_empty_check_wait_error id=%q error=%q", shortID(created.ID), err)
			return restoreEmptyCheckWaitError(session.TargetVolume, err)
		}
	case result := <-wait.Result:
		if result.StatusCode != 0 {
			logger.Printf("restore_empty_check_non_empty id=%q status=%d", shortID(created.ID), result.StatusCode)
			return restoreTargetVolumeNotEmptyError(session)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	logger.Printf("restore_empty_check_ok target=%q", session.TargetVolume)
	return nil
}

func emptyCheckContainerCreateOptions(cfg Config, session restoreSession) dockerclient.ContainerCreateOptions {
	name := "kopia-volume-restore-empty-check-" + hashString(session.SessionID+":"+session.TargetVolume)
	return dockerclient.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:           cfg.HelperImage,
			Cmd:             slices.Clone(emptyCheckCommand),
			NetworkDisabled: true,
			Labels: map[string]string{
				labelProject:      labelTrue,
				labelMode:         modeRestore,
				labelSession:      session.SessionID,
				labelTargetVolume: session.TargetVolume,
			},
		},
		HostConfig: &container.HostConfig{
			AutoRemove:  false,
			NetworkMode: container.NetworkMode("none"),
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeVolume,
					Source: session.TargetVolume,
					Target: "/target",
				},
			},
		},
	}
}

func waitForRestoreVisibleMount(ctx context.Context, cfg Config, session restoreSession, logger Logger) (visiblePathStatus, error) {
	deadline := time.Now().Add(cfg.VerifyTimeout)
	var status visiblePathStatus
	for {
		status = inspectVisiblePath(session.TargetPath)
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
		logger.Printf("restore_visible_wait session=%q target=%q status=%s", session.SessionID, session.TargetVolume, status.String())
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return status, ctx.Err()
		case <-timer.C:
		}
	}
}

func printRestoreInstructions(out io.Writer, cfg Config, opts RestoreOptions, session restoreSession) error {
	if err := writef(out, "%s %s -> %s\n", session.HelperAction, session.TargetVolume, session.TargetPath); err != nil {
		return err
	}
	if err := writef(out, "RESTORE_SESSION_ID=%s\n", session.SessionID); err != nil {
		return err
	}
	if err := writef(out, "RESTORE_TARGET_PATH=%s\n", session.TargetPath); err != nil {
		return err
	}
	if session.TargetCreated {
		if err := writef(out, "已创建目标 volume %s\n", session.TargetVolume); err != nil {
			return err
		}
	} else {
		if err := writef(out, "复用目标 volume %s\n", session.TargetVolume); err != nil {
			return err
		}
	}
	if err := writeLine(out, "推荐的 Kopia 恢复命令如下"); err != nil {
		return err
	}
	if opts.SourceDirectoryID != "" {
		return writef(out, "kopia snapshot restore %s %s\n", opts.SourceDirectoryID, session.TargetPath)
	}
	sourcePath := visiblePath(cfg, volumeSpec{VolumeName: session.SourceVolume, FriendlyName: session.FriendlyName})
	if err := writef(out, "kopia snapshot list %s\n", sourcePath); err != nil {
		return err
	}
	if opts.SnapshotTime != "" {
		return writef(out, "kopia snapshot restore %s %s --snapshot-time %s\n", sourcePath, session.TargetPath, opts.SnapshotTime)
	}
	return writef(out, "kopia snapshot restore %s %s --snapshot-time latest\n", sourcePath, session.TargetPath)
}
